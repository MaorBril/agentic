// Package config loads and validates ~/.agentic/config.yaml.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	ProviderAnthropic = "anthropic"
	ProviderOpenAI    = "openai"

	DefaultPort = 41100
)

type Config struct {
	Version        int                  `yaml:"version"`
	DefaultProfile string               `yaml:"default_profile"`
	Router         Router               `yaml:"router"`
	Providers      map[string]Provider  `yaml:"providers"`
	Models         map[string]Model     `yaml:"models"`
	Routing        map[string]RouteRule `yaml:"routing"`
	Profiles       map[string]Profile   `yaml:"profiles"`
	Budgets        *Budget              `yaml:"budgets"`
	Pricing        map[string]Price     `yaml:"pricing"`
}

// RouteRule is a dynamic alias: a cheap classifier model assesses each new
// user turn and picks a tier, so e.g. `model: auto` plans on a frontier
// model and executes on open weights without manual switching.
type RouteRule struct {
	Classifier string            `yaml:"classifier"` // model alias used to classify
	Default    string            `yaml:"default"`    // tier when classification fails ("" = standard)
	Tiers      map[string]string `yaml:"tiers"`      // deep/standard/light -> model alias
}

type Router struct {
	Port int `yaml:"port"`
}

type Provider struct {
	Type      string `yaml:"type"` // "anthropic" | "openai"
	BaseURL   string `yaml:"base_url"`
	APIKeyEnv string `yaml:"api_key_env"`
	APIKey    string `yaml:"api_key"`
	// MaxTokensParam is the OpenAI-dialect parameter name for the output
	// limit: "max_tokens" (default) or "max_completion_tokens".
	MaxTokensParam string `yaml:"max_tokens_param"`
}

// Key resolves the provider's API key: config literal, then process
// environment, then ~/.agentic/env (so keys don't depend on which shell
// launched the router leader). Empty is valid for unauthenticated local
// endpoints.
func (p Provider) Key() string {
	if p.APIKey != "" {
		return p.APIKey
	}
	if p.APIKeyEnv == "" {
		return ""
	}
	if v := os.Getenv(p.APIKeyEnv); v != "" {
		return v
	}
	return EnvFileLookup(p.APIKeyEnv)
}

type Model struct {
	Provider string `yaml:"provider"`
	ID       string `yaml:"id"`
	// Reasoning: "" (no reasoning param, sampling kept), "none" (like ""
	// but also explicitly sends reasoning_effort=none — required by
	// GPT-5-class models to accept function tools on
	// /v1/chat/completions), "effort" (map budget_tokens to
	// reasoning_effort, sampling dropped), "passive" (model always
	// reasons; parse reasoning_content, sampling kept).
	Reasoning string `yaml:"reasoning"`
	// MaxOutput clamps requested max_tokens to the model's output cap
	// (Claude Code asks for 32K+; many models cap lower).
	MaxOutput int    `yaml:"max_output"`
	Pricing   *Price `yaml:"pricing"`
	// ContextWindow is the model's nominal input context window in tokens.
	// Claude Code assumes ~200K; when the real window differs, the router
	// scales reported token counts so auto-compact fires at the right
	// relative fullness (see internal/tokens/scale.go).
	ContextWindow int `yaml:"context_window"`
	// EffectiveContext caps the usable context below the nominal window —
	// an attention budget for models that degrade well before their
	// advertised limit. Unset means the full window is usable.
	EffectiveContext int `yaml:"effective_context"`
}

// ContextBudget is the number of input tokens the model can usefully hold:
// the smaller of ContextWindow and EffectiveContext, considering only set
// fields. 0 means unknown (no scaling applied).
func (m Model) ContextBudget() int {
	switch {
	case m.ContextWindow > 0 && m.EffectiveContext > 0:
		return min(m.ContextWindow, m.EffectiveContext)
	case m.EffectiveContext > 0:
		return m.EffectiveContext
	default:
		return m.ContextWindow
	}
}

type Profile struct {
	Model       string            `yaml:"model"`
	SmallFast   string            `yaml:"small_fast"`
	Tiers       map[string]string `yaml:"tiers"` // opus/sonnet/haiku -> alias
	Budget      *Budget           `yaml:"budget"`
	Passthrough bool              `yaml:"passthrough"`
	TimeoutMS   int               `yaml:"timeout_ms"`
}

type Budget struct {
	Daily    float64 `yaml:"daily"`
	Weekly   float64 `yaml:"weekly"`
	Monthly  float64 `yaml:"monthly"`
	WarnAt   float64 `yaml:"warn_at"`
	HardStop *bool   `yaml:"hard_stop"`
}

// Price is USD per million tokens.
type Price struct {
	Input      float64 `yaml:"input" json:"input"`
	Output     float64 `yaml:"output" json:"output"`
	CacheRead  float64 `yaml:"cache_read" json:"cache_read"`
	CacheWrite float64 `yaml:"cache_write" json:"cache_write"`
}

// DataDir returns ~/.agentic, creating it if needed.
func DataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".agentic")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func Path() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yaml"), nil
}

// Load reads and validates the config file. A missing file returns
// os.ErrNotExist so callers can suggest `agentic setup`.
func Load() (*Config, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

func Parse(data []byte) (*Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) Validate() error {
	if c.Router.Port == 0 {
		c.Router.Port = DefaultPort
	}
	for name, p := range c.Providers {
		switch p.Type {
		case ProviderAnthropic, ProviderOpenAI:
		default:
			return fmt.Errorf("config: provider %q has unknown type %q (want %q or %q)",
				name, p.Type, ProviderAnthropic, ProviderOpenAI)
		}
		if p.BaseURL == "" {
			return fmt.Errorf("config: provider %q missing base_url", name)
		}
	}
	for alias, m := range c.Models {
		if _, ok := c.Providers[m.Provider]; !ok {
			return fmt.Errorf("config: model %q references unknown provider %q", alias, m.Provider)
		}
		if m.ID == "" {
			return fmt.Errorf("config: model %q missing id", alias)
		}
		switch m.Reasoning {
		case "", "none", "effort", "passive":
		default:
			return fmt.Errorf("config: model %q has unknown reasoning %q", alias, m.Reasoning)
		}
		if m.ContextWindow < 0 || m.EffectiveContext < 0 {
			return fmt.Errorf("config: model %q has negative context size", alias)
		}
		if m.ContextWindow > 0 && m.EffectiveContext > m.ContextWindow {
			return fmt.Errorf("config: model %q effective_context %d exceeds context_window %d",
				alias, m.EffectiveContext, m.ContextWindow)
		}
	}
	for name, prof := range c.Profiles {
		if prof.Passthrough {
			continue
		}
		for what, alias := range map[string]string{"model": prof.Model, "small_fast": prof.SmallFast} {
			if alias == "" {
				continue
			}
			if !c.isModelRef(alias) {
				return fmt.Errorf("config: profile %q %s references unknown model alias %q", name, what, alias)
			}
		}
		for tier, alias := range prof.Tiers {
			if !c.isModelRef(alias) {
				return fmt.Errorf("config: profile %q tier %q references unknown model alias %q", name, tier, alias)
			}
		}
	}
	if c.DefaultProfile != "" {
		if _, ok := c.Profiles[c.DefaultProfile]; !ok {
			return fmt.Errorf("config: default_profile %q not defined", c.DefaultProfile)
		}
	}
	for name, r := range c.Routing {
		if _, clash := c.Models[name]; clash {
			return fmt.Errorf("config: routing %q collides with a model alias", name)
		}
		if _, ok := c.Models[r.Classifier]; !ok {
			return fmt.Errorf("config: routing %q classifier references unknown model alias %q", name, r.Classifier)
		}
		if len(r.Tiers) == 0 {
			return fmt.Errorf("config: routing %q has no tiers", name)
		}
		for tier, alias := range r.Tiers {
			if _, ok := c.Models[alias]; !ok {
				return fmt.Errorf("config: routing %q tier %q references unknown model alias %q", name, tier, alias)
			}
		}
		if r.Default != "" {
			if _, ok := r.Tiers[r.Default]; !ok {
				return fmt.Errorf("config: routing %q default %q is not a tier", name, r.Default)
			}
		}
	}
	return nil
}

// isModelRef reports whether name is a usable main-model reference: either a
// concrete model alias or a dynamic routing rule (e.g. "auto"). Profiles and
// tiers accept both so `model: auto` is valid config, not a validation error.
func (c *Config) isModelRef(name string) bool {
	if _, ok := c.Models[name]; ok {
		return true
	}
	_, ok := c.Routing[name]
	return ok
}

// Resolved is a model alias resolved to its provider.
type Resolved struct {
	Alias        string
	ProviderName string
	Provider     Provider
	Model        Model
}

// Resolve maps a model id from a request to a provider + upstream model.
// Resolution order: exact alias -> built-in default (claude-* passes
// through to the "anthropic" provider unchanged).
func (c *Config) Resolve(alias string) (Resolved, error) {
	if m, ok := c.Models[alias]; ok {
		return Resolved{Alias: alias, ProviderName: m.Provider, Provider: c.Providers[m.Provider], Model: m}, nil
	}
	if strings.HasPrefix(alias, "claude-") {
		if p, ok := c.Providers[ProviderAnthropic]; ok {
			return Resolved{
				Alias:        alias,
				ProviderName: ProviderAnthropic,
				Provider:     p,
				Model:        Model{Provider: ProviderAnthropic, ID: alias},
			}, nil
		}
		return Resolved{}, fmt.Errorf("model %q needs an %q provider in config", alias, ProviderAnthropic)
	}
	return Resolved{}, fmt.Errorf("unknown model alias %q", alias)
}
