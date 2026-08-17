package config

import (
	"strings"
	"testing"
)

const testYAML = `
version: 1
default_profile: main
providers:
  anthropic: {type: anthropic, base_url: https://api.anthropic.com, api_key_env: ANTHROPIC_API_KEY}
  local:     {type: openai, base_url: "http://localhost:11434/v1", api_key_env: ""}
models:
  sonnet: {provider: anthropic, id: claude-sonnet-5}
  qwen:   {provider: local, id: qwen3-coder-30b, pricing: {input: 0, output: 0}}
profiles:
  main: {model: sonnet, small_fast: sonnet, tiers: {haiku: sonnet}}
  sub:  {passthrough: true}
budgets: {daily: 10.0}
`

func TestParseAndResolve(t *testing.T) {
	cfg, err := Parse([]byte(testYAML))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Router.Port != DefaultPort {
		t.Errorf("port default = %d, want %d", cfg.Router.Port, DefaultPort)
	}

	// Exact alias.
	r, err := cfg.Resolve("sonnet")
	if err != nil || r.Model.ID != "claude-sonnet-5" || r.ProviderName != "anthropic" {
		t.Errorf("resolve sonnet = %+v, %v", r, err)
	}
	r, err = cfg.Resolve("qwen")
	if err != nil || r.Provider.Type != ProviderOpenAI {
		t.Errorf("resolve qwen = %+v, %v", r, err)
	}

	// Built-in claude-* passthrough default — load-bearing for background
	// haiku calls when the main model is overridden.
	r, err = cfg.Resolve("claude-haiku-4-5")
	if err != nil || r.Model.ID != "claude-haiku-4-5" || r.ProviderName != "anthropic" {
		t.Errorf("resolve claude-haiku-4-5 = %+v, %v", r, err)
	}

	if _, err := cfg.Resolve("nonexistent"); err == nil {
		t.Error("resolve nonexistent should fail")
	}
}

func TestValidationErrors(t *testing.T) {
	cases := map[string]string{
		"unknown provider type": `
providers:
  x: {type: gemini, base_url: http://x}
`,
		"model references unknown provider": `
models:
  m: {provider: nope, id: foo}
`,
		"profile references unknown model": `
profiles:
  p: {model: nope}
`,
		"default_profile undefined": `
default_profile: ghost
`,
		"unknown yaml field (typo detection)": `
modles:
  m: {provider: x, id: foo}
`,
		"effective_context above context_window": `
providers:
  local: {type: openai, base_url: http://x}
models:
  m: {provider: local, id: foo, context_window: 32000, effective_context: 64000}
`,
		"negative context_window": `
providers:
  local: {type: openai, base_url: http://x}
models:
  m: {provider: local, id: foo, context_window: -1}
`,
		"context fields on anthropic provider (passthrough never scales)": `
providers:
  anthropic: {type: anthropic, base_url: https://api.anthropic.com}
models:
  m: {provider: anthropic, id: claude-sonnet-5, effective_context: 60000}
`,
		"pin_tiers without a model to pin to": `
providers:
  anthropic: {type: anthropic, base_url: https://api.anthropic.com}
models:
  m: {provider: anthropic, id: claude-sonnet-5}
profiles:
  p: {pin_tiers: true}
`,
	}
	for name, yaml := range cases {
		if _, err := Parse([]byte(yaml)); err == nil {
			t.Errorf("%s: expected error", name)
		} else if !strings.Contains(err.Error(), "config") && !strings.Contains(err.Error(), "field") {
			t.Logf("%s: %v", name, err)
		}
	}
}

func TestCLIProviderValidation(t *testing.T) {
	valid := `
providers:
  codex: {type: cli, dialect: codex, sandbox: workspace-write, timeout_ms: 60000}
  grok: {type: cli, dialect: grok, command: /opt/bin/grok}
models:
  codex: {provider: codex}
  grok-fast: {provider: grok, id: grok-code-fast}
`
	cfg, err := Parse([]byte(valid))
	if err != nil {
		t.Fatalf("valid cli config rejected: %v", err)
	}
	if cfg.Providers["codex"].Bin() != "codex" || cfg.Providers["grok"].Bin() != "/opt/bin/grok" {
		t.Errorf("unexpected binaries: codex=%q grok=%q", cfg.Providers["codex"].Bin(), cfg.Providers["grok"].Bin())
	}
	if !cfg.IsCLIAlias("codex") || cfg.IsCLIAlias("missing") {
		t.Errorf("IsCLIAlias returned unexpected result")
	}

	cases := map[string]string{
		"missing dialect":        `providers: {p: {type: cli}}`,
		"unknown dialect":        `providers: {p: {type: cli, dialect: gemini}}`,
		"base url forbidden":     `providers: {p: {type: cli, dialect: codex, base_url: http://x}}`,
		"grok sandbox forbidden": `providers: {p: {type: cli, dialect: grok, sandbox: workspace-write}}`,
		"invalid codex sandbox":  `providers: {p: {type: cli, dialect: codex, sandbox: unrestricted}}`,
		"negative timeout":       `providers: {p: {type: cli, dialect: codex, timeout_ms: -1}}`,
		"cli model pricing forbidden": `
providers: {p: {type: cli, dialect: codex}}
models: {m: {provider: p, pricing: {input: 1, output: 1}}}
`,
		"cli model context forbidden": `
providers: {p: {type: cli, dialect: codex}}
models: {m: {provider: p, context_window: 200000}}
`,
		"cli alias as profile model": `
providers: {p: {type: cli, dialect: codex}}
models: {m: {provider: p}}
profiles: {main: {model: m}}
`,
		"cli alias on passthrough profile": `
providers: {p: {type: cli, dialect: codex}}
models: {m: {provider: p}}
profiles: {sub: {passthrough: true, model: m}}
`,
		"cli alias as classifier": `
providers:
  p: {type: cli, dialect: codex}
  a: {type: anthropic, base_url: https://api.anthropic.com}
models:
  m: {provider: p}
  sonnet: {provider: a, id: claude-sonnet-5}
routing:
  auto: {classifier: m, tiers: {standard: sonnet}}
`,
		"cli alias as task target": `
providers:
  p: {type: cli, dialect: codex}
  a: {type: anthropic, base_url: https://api.anthropic.com}
models:
  m: {provider: p}
  sonnet: {provider: a, id: claude-sonnet-5}
routing:
  auto:
    classifier: sonnet
    tiers: {standard: sonnet}
    tasks: {implementation: m}
`,
	}
	for name, yaml := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(yaml)); err == nil {
				t.Error("expected validation error")
			}
		})
	}
}

func TestTaskRoutingValidation(t *testing.T) {
	base := `
providers:
  anthropic: {type: anthropic, base_url: https://api.anthropic.com}
models:
  haiku: {provider: anthropic, id: claude-haiku-4-5}
  opus: {provider: anthropic, id: claude-opus-5}
  grok: {provider: anthropic, id: grok-4.6}
routing:
  auto:
    classifier: haiku
    default: standard
    tiers: {deep: opus, standard: opus, light: haiku}
    tasks: {implementation: grok, critical_review: opus}
`
	cfg, err := Parse([]byte(base))
	if err != nil {
		t.Fatalf("valid task routing rejected: %v", err)
	}
	if cfg.Routing["auto"].Tasks["implementation"] != "grok" {
		t.Fatalf("task mapping not parsed: %#v", cfg.Routing["auto"].Tasks)
	}

	cases := map[string]string{
		"unknown task label": strings.Replace(base, "implementation: grok", "coding: grok", 1),
		"unknown task alias": strings.Replace(base, "implementation: grok", "implementation: ghost", 1),
	}
	for name, yaml := range cases {
		if _, err := Parse([]byte(yaml)); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

func TestTaskLabelsAreRecognized(t *testing.T) {
	for _, label := range TaskLabels {
		if !IsTaskLabel(label) {
			t.Errorf("TaskLabels contains unrecognized label %q", label)
		}
	}
	if IsTaskLabel("coding") || IsTaskLabel("") {
		t.Error("unknown task labels must be rejected")
	}
}

func TestContextBudget(t *testing.T) {
	cases := []struct {
		name string
		m    Model
		want int
	}{
		{"unset", Model{}, 0},
		{"window only", Model{ContextWindow: 128000}, 128000},
		{"effective only", Model{EffectiveContext: 60000}, 60000},
		{"effective caps window", Model{ContextWindow: 200000, EffectiveContext: 60000}, 60000},
		{"equal", Model{ContextWindow: 32000, EffectiveContext: 32000}, 32000},
	}
	for _, tc := range cases {
		if got := tc.m.ContextBudget(); got != tc.want {
			t.Errorf("%s: ContextBudget() = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestSplitPathQuotedSegments(t *testing.T) {
	got := splitPath(`pricing."gpt-5.5".input`)
	if len(got) != 3 || got[0] != "pricing" || got[1] != "gpt-5.5" || got[2] != "input" {
		t.Errorf("splitPath = %v", got)
	}
	got = splitPath("budgets.daily")
	if len(got) != 2 || got[1] != "daily" {
		t.Errorf("splitPath = %v", got)
	}
}
