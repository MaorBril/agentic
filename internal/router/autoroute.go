package router

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/maorbril/agentic/internal/anthropic"
	"github.com/maorbril/agentic/internal/backend"
	"github.com/maorbril/agentic/internal/config"
)

// autoRouter implements dynamic tier routing: a cheap classifier model
// assesses each new user turn and picks deep/standard/light; the decision
// sticks for the rest of the turn (tool_result continuations) so a task
// doesn't flip models mid-flight.
type autoRouter struct {
	classify func(ctx context.Context, rule config.RouteRule, cfg *config.Config, summary string) (string, error)
	log      *slog.Logger

	mu    sync.Mutex
	cache map[string]decision // key: session id (or user-text hash when unattributed)
}

type decision struct {
	userHash string
	tier     string
	at       time.Time
}

const classifierPrompt = `You route requests inside a coding agent to a model tier. Reply with exactly one word: deep, standard, or light.

deep: planning, architecture, debugging hard problems, multi-step reasoning, ambiguous or high-stakes decisions
standard: writing or modifying code, ordinary multi-tool tasks
light: mechanical edits, renames, formatting, summaries, verifying provided output, short factual answers

Request to classify:
`

// route picks the concrete model alias for a dynamically-routed request. The
// returned reason is a free-text note for observability — non-empty when
// size-aware routing remapped the classifier's choice (e.g.
// "size:light→standard"); empty for a plain classifier decision.
func (a *autoRouter) route(ctx context.Context, rule config.RouteRule, cfg *config.Config, raw []byte, sessionID string) (alias, tier, reason string) {
	fallback := rule.Default
	if fallback == "" {
		fallback = "standard"
	}
	if _, ok := rule.Tiers[fallback]; !ok {
		for t := range rule.Tiers {
			fallback = t
			break
		}
	}

	req, err := anthropic.ParseRequest(raw)
	if err != nil {
		return rule.Tiers[fallback], fallback, ""
	}

	// Size-aware fit: which tiers can hold this request? Required==0 and no
	// byte caps means no tier has a known limit — the backward-compat fast
	// path (no estimate, no filtering).
	fit := classifyTierFit(cfg, rule, req, int64(len(raw)))

	userText, isNewTurn := lastUserText(req)
	hash := hashText(userText)
	key := sessionID
	if key == "" {
		key = hash
	}

	a.mu.Lock()
	prev, hasPrev := a.cache[key]
	a.mu.Unlock()

	// Continuations (tool results coming back) and retries of the same
	// turn keep the tier that opened the turn.
	if hasPrev && (!isNewTurn || prev.userHash == hash) {
		if _, ok := rule.Tiers[prev.tier]; ok {
			if fit.Eligible[prev.tier] {
				return rule.Tiers[prev.tier], prev.tier, ""
			}
			// The sticky tier can't hold this (larger) continuation.
			// Remap upward without re-classifying, and pin the cache so the
			// rest of the turn stays on the remapped tier.
			remapped := remapTier(cfg, rule, fit, prev.tier)
			a.logFit(rule, fit, prev.tier, remapped, "sticky")
			a.cacheDecision(key, hash, remapped)
			return rule.Tiers[remapped], remapped, "size:sticky:" + prev.tier + "→" + remapped
		}
	}
	if userText == "" {
		return rule.Tiers[fallback], fallback, ""
	}

	// When size filtering has left exactly one viable tier, skip the
	// classifier call — there's nothing to decide.
	if t, ok := onlyEligibleTier(fit); ok {
		a.logFit(rule, fit, "", t, "only")
		a.cacheDecision(key, hash, t)
		return rule.Tiers[t], t, ""
	}

	summary := fmt.Sprintf("(conversation: %d messages, %d tools available)\n%s",
		len(req.Messages), len(req.Tools), truncate(userText, 2000))
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tier, err = a.classify(cctx, rule, cfg, summary)
	if err != nil || rule.Tiers[tier] == "" {
		tier = fallback
	}

	// The classifier has no notion of size; if it picked a tier the request
	// won't fit, remap upward to the smallest tier that does.
	if fit.Required > 0 && !fit.Eligible[tier] {
		chosen := tier
		tier = remapTier(cfg, rule, fit, chosen)
		a.logFit(rule, fit, chosen, tier, "remap")
		reason = "size:" + chosen + "→" + tier
	}

	a.cacheDecision(key, hash, tier)
	return rule.Tiers[tier], tier, reason
}

// cacheDecision stores a tier decision under key, flushing the cache when it
// exceeds 1000 entries.
func (a *autoRouter) cacheDecision(key, hash, tier string) {
	a.mu.Lock()
	if len(a.cache) > 1000 {
		a.cache = map[string]decision{}
	}
	a.cache[key] = decision{userHash: hash, tier: tier, at: time.Now()}
	a.mu.Unlock()
}

// logFit emits a Debug-level autoroute_size event when size filtering is
// active. from is the classifier-chosen (or sticky) tier; to is the tier
// actually used. kind is "sticky" | "only" | "remap".
func (a *autoRouter) logFit(rule config.RouteRule, fit fitDecision, from, to, kind string) {
	if a.log == nil {
		return
	}
	a.log.Debug("autoroute_size",
		"estimated_input", fit.EstInput,
		"required", fit.Required,
		"excluded", fit.Filtered,
		"from", from,
		"to", to,
		"kind", kind,
	)
}

// lastUserText returns the newest user-authored text and whether the last
// message is a fresh user turn (vs a tool_result continuation).
func lastUserText(req *anthropic.MessagesRequest) (string, bool) {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		msg := req.Messages[i]
		if msg.Role != "user" {
			continue
		}
		text, hasToolResult := "", false
		for _, b := range msg.Content {
			switch b.Type {
			case "text":
				if text != "" {
					text += "\n"
				}
				text += b.Text
			case "tool_result":
				hasToolResult = true
			}
		}
		if text != "" {
			return text, i == len(req.Messages)-1 && !hasToolResult
		}
		if hasToolResult {
			return "", false // continuation; keep scanning is pointless — turn already classified
		}
	}
	return "", false
}

func hashText(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// classifyViaBackend runs the classifier request through the router's own
// backends and parses the one-word tier answer.
func (s *Server) classifyViaBackend(ctx context.Context, rule config.RouteRule, cfg *config.Config, summary string) (string, error) {
	resp, err := s.runClassifier(ctx, rule, cfg, classifierPrompt+summary, 8)
	if err != nil {
		return "", err
	}
	for _, block := range resp.Content {
		if block.Type == "text" {
			word := strings.ToLower(strings.TrimSpace(block.Text))
			word = strings.Trim(word, ".\"' \n")
			return word, nil
		}
	}
	return "", fmt.Errorf("classifier returned no text")
}

// runClassifier sends a single-turn prompt to a classifier model alias
// through the router's own backends (no network hop out and back through
// the local port) and returns the parsed Anthropic-shaped response. Shared
// by any classification pass (tier routing, goal detection, ...).
func (s *Server) runClassifier(ctx context.Context, rule config.RouteRule, cfg *config.Config, prompt string, maxTokens int) (*anthropic.MessagesResponse, error) {
	route, err := cfg.Resolve(rule.Classifier)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(map[string]any{
		"model":      rule.Classifier,
		"max_tokens": maxTokens,
		"messages": []map[string]any{
			{"role": "user", "content": prompt},
		},
	})
	if err != nil {
		return nil, err
	}
	env, _ := anthropic.ParseEnvelope(body)
	call := &backend.Call{Raw: body, Envelope: env, Route: route, Header: http.Header{}, Query: nil}

	rec := newMemWriter()
	var be backend.Backend
	switch route.Provider.Type {
	case config.ProviderAnthropic:
		be = s.anth
	case config.ProviderOpenAI:
		be = s.oai
	default:
		return nil, fmt.Errorf("classifier provider type %q unsupported", route.Provider.Type)
	}
	res := be.Messages(ctx, call, rec)
	if res.Status != 200 {
		return nil, fmt.Errorf("classifier request failed: %d %s", res.Status, res.ErrType)
	}
	var resp anthropic.MessagesResponse
	if err := json.Unmarshal(rec.buf.Bytes(), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// memWriter is an in-memory http.ResponseWriter for internal requests.
type memWriter struct {
	header http.Header
	buf    bytes.Buffer
	status int
}

func newMemWriter() *memWriter { return &memWriter{header: http.Header{}, status: 200} }

func (m *memWriter) Header() http.Header         { return m.header }
func (m *memWriter) WriteHeader(code int)        { m.status = code }
func (m *memWriter) Write(p []byte) (int, error) { return m.buf.Write(p) }
