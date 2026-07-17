package router

import (
	"context"
	"errors"
	"testing"

	"github.com/maorbril/agentic/internal/config"
)

func testRule() config.RouteRule {
	return config.RouteRule{
		Classifier: "cheap",
		Default:    "standard",
		Tiers:      map[string]string{"deep": "opus", "standard": "sonnet", "light": "qwen"},
	}
}

func newAuto(answer string, err error, calls *int) *autoRouter {
	return &autoRouter{
		cache: map[string]decision{},
		classify: func(ctx context.Context, rule config.RouteRule, cfg *config.Config, summary string) (string, error) {
			*calls++
			return answer, err
		},
	}
}

func body(msgs string) []byte {
	return []byte(`{"model":"auto","max_tokens":100,"messages":[` + msgs + `]}`)
}

func TestAutoRouteClassifiesNewTurn(t *testing.T) {
	calls := 0
	a := newAuto("deep", nil, &calls)
	alias, tier := a.route(context.Background(), testRule(), nil, body(`{"role":"user","content":"design the architecture for a big refactor"}`), "s1")
	if alias != "opus" || tier != "deep" || calls != 1 {
		t.Errorf("alias=%s tier=%s calls=%d", alias, tier, calls)
	}
}

func TestAutoRouteSticksWithinTurn(t *testing.T) {
	calls := 0
	a := newAuto("deep", nil, &calls)
	newTurn := body(`{"role":"user","content":"plan the migration"}`)
	a.route(context.Background(), testRule(), nil, newTurn, "s1")

	// Continuation: assistant tool_use answered by tool_result — no new user text.
	continuation := body(`{"role":"user","content":"plan the migration"},
	  {"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"read_file","input":{}}]},
	  {"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"data"}]}`)
	alias, tier := a.route(context.Background(), testRule(), nil, continuation, "s1")
	if alias != "opus" || tier != "deep" {
		t.Errorf("continuation: alias=%s tier=%s", alias, tier)
	}
	if calls != 1 {
		t.Errorf("classifier ran %d times; continuations must reuse the decision", calls)
	}

	// A genuinely new user turn re-classifies.
	a.classify = func(ctx context.Context, rule config.RouteRule, cfg *config.Config, summary string) (string, error) {
		calls++
		return "light", nil
	}
	alias, tier = a.route(context.Background(), testRule(), nil, body(`{"role":"user","content":"rename that variable"}`), "s1")
	if alias != "qwen" || tier != "light" || calls != 2 {
		t.Errorf("new turn: alias=%s tier=%s calls=%d", alias, tier, calls)
	}
}

func TestAutoRouteFallsBackOnFailure(t *testing.T) {
	calls := 0
	a := newAuto("", errors.New("classifier down"), &calls)
	alias, tier := a.route(context.Background(), testRule(), nil, body(`{"role":"user","content":"hello"}`), "s2")
	if alias != "sonnet" || tier != "standard" {
		t.Errorf("fallback: alias=%s tier=%s", alias, tier)
	}

	// Garbage classifier answer also falls back.
	a = newAuto("banana", nil, &calls)
	alias, tier = a.route(context.Background(), testRule(), nil, body(`{"role":"user","content":"hello"}`), "s3")
	if alias != "sonnet" || tier != "standard" {
		t.Errorf("garbage answer: alias=%s tier=%s", alias, tier)
	}
}

func TestRoutingConfigValidation(t *testing.T) {
	yaml := `
providers:
  anthropic: {type: anthropic, base_url: https://api.anthropic.com, api_key_env: A}
models:
  opus:  {provider: anthropic, id: claude-opus-4-8}
  haiku: {provider: anthropic, id: claude-haiku-4-5}
routing:
  auto:
    classifier: haiku
    default: standard
    tiers: {deep: opus, standard: opus, light: haiku}
`
	if _, err := config.Parse([]byte(yaml)); err != nil {
		t.Fatalf("valid routing config rejected: %v", err)
	}

	bad := map[string]string{
		"unknown classifier": `
providers:
  anthropic: {type: anthropic, base_url: https://x, api_key_env: A}
models:
  opus: {provider: anthropic, id: claude-opus-4-8}
routing:
  auto: {classifier: ghost, tiers: {deep: opus}}
`,
		"unknown tier target": `
providers:
  anthropic: {type: anthropic, base_url: https://x, api_key_env: A}
models:
  opus: {provider: anthropic, id: claude-opus-4-8}
routing:
  auto: {classifier: opus, tiers: {deep: ghost}}
`,
		"collides with model alias": `
providers:
  anthropic: {type: anthropic, base_url: https://x, api_key_env: A}
models:
  opus: {provider: anthropic, id: claude-opus-4-8}
routing:
  opus: {classifier: opus, tiers: {deep: opus}}
`,
	}
	for name, y := range bad {
		if _, err := config.Parse([]byte(y)); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}
