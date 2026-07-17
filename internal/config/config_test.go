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
	}
	for name, yaml := range cases {
		if _, err := Parse([]byte(yaml)); err == nil {
			t.Errorf("%s: expected error", name)
		} else if !strings.Contains(err.Error(), "config") && !strings.Contains(err.Error(), "field") {
			t.Logf("%s: %v", name, err)
		}
	}
}
