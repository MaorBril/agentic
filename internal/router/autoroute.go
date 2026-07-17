package router

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

// route picks the concrete model alias for a dynamically-routed request.
func (a *autoRouter) route(ctx context.Context, rule config.RouteRule, cfg *config.Config, raw []byte, sessionID string) (alias, tier string) {
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
		return rule.Tiers[fallback], fallback
	}
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
			return rule.Tiers[prev.tier], prev.tier
		}
	}
	if userText == "" {
		return rule.Tiers[fallback], fallback
	}

	summary := fmt.Sprintf("(conversation: %d messages, %d tools available)\n%s",
		len(req.Messages), len(req.Tools), truncate(userText, 2000))
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tier, err = a.classify(cctx, rule, cfg, summary)
	if err != nil || rule.Tiers[tier] == "" {
		tier = fallback
	}

	a.mu.Lock()
	if len(a.cache) > 1000 {
		a.cache = map[string]decision{}
	}
	a.cache[key] = decision{userHash: hash, tier: tier, at: time.Now()}
	a.mu.Unlock()
	return rule.Tiers[tier], tier
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
// backends and parses the one-word answer.
func (s *Server) classifyViaBackend(ctx context.Context, rule config.RouteRule, cfg *config.Config, summary string) (string, error) {
	route, err := cfg.Resolve(rule.Classifier)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(map[string]any{
		"model":      rule.Classifier,
		"max_tokens": 8,
		"messages": []map[string]any{
			{"role": "user", "content": classifierPrompt + summary},
		},
	})
	if err != nil {
		return "", err
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
		return "", fmt.Errorf("classifier provider type %q unsupported", route.Provider.Type)
	}
	res := be.Messages(ctx, call, rec)
	if res.Status != 200 {
		return "", fmt.Errorf("classifier request failed: %d %s", res.Status, res.ErrType)
	}
	var resp anthropic.MessagesResponse
	if err := json.Unmarshal(rec.buf.Bytes(), &resp); err != nil {
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
