// Package router serves the Anthropic Messages API surface Claude Code
// talks to, dispatching per-model to provider backends.
package router

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/maorbril/agentic/internal/anthropic"
	"github.com/maorbril/agentic/internal/backend"
	"github.com/maorbril/agentic/internal/backend/anthropicbe"
	"github.com/maorbril/agentic/internal/backend/openaibe"
	"github.com/maorbril/agentic/internal/budget"
	"github.com/maorbril/agentic/internal/config"
	"github.com/maorbril/agentic/internal/pricing"
	"github.com/maorbril/agentic/internal/store"
)

type Server struct {
	cfg     atomic.Pointer[config.Config]
	pricing atomic.Pointer[pricing.Table]
	token   string
	dataDir string
	store   *store.Store
	anth    *anthropicbe.Backend
	oai     *openaibe.Backend
	gate    *budget.Gate
	auto    *autoRouter
	goal    *goalRouter
	log     *slog.Logger
}

func NewServer(cfg *config.Config, token, dataDir string, st *store.Store, logger *slog.Logger) *Server {
	s := &Server{
		token: token, dataDir: dataDir, store: st,
		anth: anthropicbe.New(), oai: openaibe.New(), log: logger,
	}
	s.cfg.Store(cfg)
	s.pricing.Store(pricing.Load(dataDir, cfg))
	s.gate = budget.NewGate(cfg, st, logger)
	s.auto = &autoRouter{classify: s.classifyViaBackend, cache: map[string]decision{}, log: logger}
	s.goal = &goalRouter{classify: s.classifyGoalViaBackend}
	return s
}

// Reload re-reads the config file; called by /agentic/reload so config CLI
// edits apply to live sessions without restart.
func (s *Server) Reload() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	s.cfg.Store(cfg)
	s.pricing.Store(pricing.Load(s.dataDir, cfg))
	s.gate.SetConfig(cfg)
	return nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /agentic/health", s.handleHealth)
	mux.HandleFunc("POST /agentic/reload", s.auth(s.handleReload))
	mux.HandleFunc("POST /v1/messages", s.auth(s.handleMessages(false)))
	mux.HandleFunc("POST /v1/messages/count_tokens", s.auth(s.handleMessages(true)))
	// Catch-all: unknown /v1/* endpoints go to the default anthropic
	// provider so new Claude Code calls keep working.
	mux.HandleFunc("/v1/", s.auth(s.handleCatchAll))
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "version": Version})
}

func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	if err := s.Reload(); err != nil {
		anthropic.WriteError(w, 500, "api_error", "reload failed: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("x-api-key")
		if got == "" {
			got = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			anthropic.WriteError(w, 401, "authentication_error",
				"agentic router: invalid local token (launch sessions via `agentic`)")
			return
		}
		next(w, r)
	}
}

func (s *Server) handleMessages(countTokens bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			anthropic.WriteError(w, 400, "invalid_request_error", "agentic: reading body: "+err.Error())
			return
		}
		env, err := anthropic.ParseEnvelope(raw)
		if err != nil || env.Model == "" {
			anthropic.WriteError(w, 400, "invalid_request_error", "agentic: request body is not a Messages API request")
			return
		}
		cfg := s.cfg.Load()
		sessionID := r.Header.Get("X-Agentic-Session")
		resolveAlias := env.Model
		if rule, ok := cfg.Routing[env.Model]; ok {
			chosen, tier, reason := s.auto.route(r.Context(), rule, cfg, raw, sessionID)
			s.log.Info("autoroute", "alias", env.Model, "tier", tier, "model", chosen, "reason", reason)
			resolveAlias = chosen
			if sessionID != "" {
				if err := s.store.RecordRouteDecision(sessionID, env.Model, tier, chosen, reason, time.Now()); err != nil {
					s.log.Warn("route decision insert failed", "err", err)
				}
				worthy, reason, isNewTurn := s.goal.check(r.Context(), rule, cfg, raw, sessionID)
				if worthy {
					if injected, err := injectGoalReminder(raw, reason); err == nil {
						raw = injected
					} else {
						s.log.Warn("goal reminder injection failed", "err", err)
					}
					s.log.Info("autogoal", "session", sessionID, "reason", reason)
				}
				// Continuations (tool results) never re-classify, so they
				// must not clobber a decision recorded when the turn opened.
				if isNewTurn {
					if err := s.store.RecordGoalDecision(sessionID, worthy, reason, time.Now()); err != nil {
						s.log.Warn("goal decision insert failed", "err", err)
					}
				}
			}
		}
		route, err := cfg.Resolve(resolveAlias)
		if err != nil {
			anthropic.WriteError(w, 404, "not_found_error", "agentic: "+err.Error()+" (see ~/.agentic/config.yaml)")
			return
		}

		// Dispatch-time prompt-too-long guard: for a model with a known
		// context budget, refuse requests the budget can't hold before they
		// reach upstream and fail with a mangled, provider-specific error.
		// 400 (not 413) so Claude Code doesn't retry-spin — same rationale as
		// the budget gate below. count_tokens never dispatches, so skip it.
		if !countTokens {
			if req, perr := anthropic.ParseRequest(raw); perr == nil {
				if overflow, required, budget := promptTooLong(route, req); overflow {
					msg := fmt.Sprintf("agentic: request too large for model %q context budget "+
						"(estimated %d + reserved output exceeds budget %d); "+
						"reduce the conversation or switch models",
						route.Model.ID, required, budget)
					anthropic.WriteError(w, 400, "invalid_request_error", msg)
					s.log.Info("prompt_too_long",
						"model", route.Model.ID, "alias", resolveAlias,
						"estimated_input", required, "budget", budget)
					return
				}
			}
		}

		profile := r.Header.Get("X-Agentic-Profile")
		if !countTokens {
			if msg := s.gate.Check(profile); msg != "" {
				// 400 deliberately — 429/5xx would make Claude Code retry-spin;
				// a 400 surfaces the message verbatim in the TUI.
				anthropic.WriteError(w, 400, "invalid_request_error", msg)
				return
			}
		}

		call := &backend.Call{Raw: raw, Envelope: env, Route: route, Header: r.Header, Query: r.URL.Query()}
		var be backend.Backend
		switch route.Provider.Type {
		case config.ProviderAnthropic:
			be = s.anth
		case config.ProviderOpenAI:
			be = s.oai
		default:
			anthropic.WriteError(w, 501, "api_error",
				fmt.Sprintf("agentic: provider type %q not implemented (model %q)", route.Provider.Type, env.Model))
			return
		}
		var res backend.Result
		if countTokens {
			res = be.CountTokens(r.Context(), call, w)
		} else {
			res = be.Messages(r.Context(), call, w)
		}

		if !countTokens {
			s.recordUsage(r, route, env.Model, res, time.Since(start))
		}
	}
}

func (s *Server) recordUsage(r *http.Request, route config.Resolved, alias string, res backend.Result, dur time.Duration) {
	u := res.Usage
	if u == (anthropic.Usage{}) && res.ErrType == "" {
		return
	}
	cost, priced := s.pricing.Load().Cost(route.Model.ID,
		u.InputTokens, u.OutputTokens, u.CacheReadInputTokens, u.CacheCreationInputTokens)
	budget := route.Model.ContextBudget()
	ev := store.UsageEvent{
		TS:               time.Now(),
		SessionID:        r.Header.Get("X-Agentic-Session"),
		Profile:          r.Header.Get("X-Agentic-Profile"),
		Provider:         route.ProviderName,
		Model:            route.Model.ID,
		Alias:            alias,
		InputTokens:      u.InputTokens,
		OutputTokens:     u.OutputTokens,
		CacheReadTokens:  u.CacheReadInputTokens,
		CacheWriteTokens: u.CacheCreationInputTokens,
		CostUSD:          cost,
		Priced:           priced,
		RequestID:        r.Header.Get("request-id"),
		Status:           res.Status,
		ErrType:          res.ErrType,
		CtxBudget:        budget,
		ReportedInput:    res.ReportedInput,
	}
	if err := s.store.RecordUsage(ev); err != nil {
		s.log.Warn("usage insert failed", "err", err)
	}
	s.gate.Add(ev.Profile, cost)
	if res.Status >= 400 && res.ErrMsg != "" {
		s.log.Warn("upstream error",
			"model", alias, "upstream", route.Model.ID, "status", res.Status, "err", res.ErrMsg)
	}
	attrs := []any{
		"model", alias, "upstream", route.Model.ID, "status", res.Status,
		"in", u.InputTokens, "out", u.OutputTokens,
		"cache_read", u.CacheReadInputTokens, "cost_usd", fmt.Sprintf("%.4f", cost),
		"ms", dur.Milliseconds(),
	}
	if budget > 0 {
		trueIn := u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
		attrs = append(attrs,
			"ctx_budget", budget,
			"ctx_reported", res.ReportedInput,
			"ctx_pct", fmt.Sprintf("%.1f", 100*float64(trueIn)/float64(budget)))
	}
	s.log.Info("request", attrs...)
}

// handleCatchAll forwards unrecognized /v1/* calls to the default
// anthropic provider unmodified.
func (s *Server) handleCatchAll(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Load()
	p, ok := cfg.Providers[config.ProviderAnthropic]
	if !ok {
		anthropic.WriteError(w, 404, "not_found_error", "agentic: no anthropic provider configured for "+r.URL.Path)
		return
	}
	u := strings.TrimSuffix(p.BaseURL, "/") + r.URL.Path
	if r.URL.RawQuery != "" {
		u += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, u, r.Body)
	if err != nil {
		anthropic.WriteError(w, 500, "api_error", "agentic: "+err.Error())
		return
	}
	req.Header = r.Header.Clone()
	req.Header.Set("x-api-key", p.Key())
	req.Header.Del("Authorization")
	resp, err := s.anthClient().Do(req)
	if err != nil {
		anthropic.WriteError(w, 500, "api_error", "anthropic upstream: "+err.Error())
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	flushCopy(w, resp.Body)
}

var catchAllClient = &http.Client{Transport: backend.NewTransport()}

func (s *Server) anthClient() *http.Client { return catchAllClient }

func flushCopy(w http.ResponseWriter, r io.Reader) {
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}
