package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/maorbril/agentic/internal/config"
	"github.com/maorbril/agentic/internal/launch"
)

// registerStatusline merges a statusLine entry into ~/.claude/settings.json
// (read-merge-write; an existing different statusline is left alone).
func registerStatusline() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, "settings.json")
	settings := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &settings); err != nil {
			return fmt.Errorf("existing %s is not valid JSON: %w", path, err)
		}
	}
	if existing, ok := settings["statusLine"]; ok {
		if m, ok := existing.(map[string]any); ok && m["command"] == "agentic statusline" {
			fmt.Println("✓ statusline already registered")
			return nil
		}
		fmt.Println("· a statusline is already configured in ~/.claude/settings.json — leaving it alone")
		fmt.Println(`  (to use agentic's: set "statusLine": {"type":"command","command":"agentic statusline"})`)
		return nil
	}
	settings["statusLine"] = map[string]any{"type": "command", "command": "agentic statusline"}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	fmt.Println("✓ registered agentic statusline in ~/.claude/settings.json")
	return nil
}

const defaultConfig = `# ~/.agentic/config.yaml — edit directly or via the agentic CLI.
version: 1
default_profile: main

router:
  port: 41100          # fixed — leader election binds this port

providers:
  anthropic:
    type: anthropic                      # native passthrough, no translation
    base_url: https://api.anthropic.com
    api_key_env: ANTHROPIC_API_KEY
  # openai:
  #   type: openai                       # OpenAI-dialect translation (phase 2)
  #   base_url: https://api.openai.com/v1
  #   api_key_env: OPENAI_API_KEY
  #   max_tokens_param: max_completion_tokens
  # xai:
  #   type: openai
  #   base_url: https://api.x.ai/v1
  #   api_key_env: XAI_API_KEY
  # local:
  #   type: openai                       # Ollama / vLLM / any OpenAI-compatible server
  #   base_url: http://localhost:11434/v1
  #   api_key_env: ""

models:                # alias -> upstream; aliases flow into ANTHROPIC_MODEL
  opus:   {provider: anthropic, id: claude-opus-4-8}
  sonnet: {provider: anthropic, id: claude-sonnet-5}
  haiku:  {provider: anthropic, id: claude-haiku-4-5}
  # gpt:  {provider: openai, id: gpt-5.2, reasoning: effort, max_output: 16384}
  # grok: {provider: xai, id: grok-4}

profiles:
  main:
    model: sonnet
    small_fast: haiku
    tiers: {opus: opus, sonnet: sonnet, haiku: haiku}
  subscription:
    passthrough: true  # normal claude login, no router, no cost tracking

budgets:
  daily: 50.00
  warn_at: 0.8
  hard_stop: true
`

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "First-run configuration",
	RunE: func(cmd *cobra.Command, args []string) error {
		dataDir, err := config.DataDir()
		if err != nil {
			return err
		}
		if _, err := exec.LookPath("claude"); err != nil {
			fmt.Println("⚠ claude binary not found — install Claude Code first: https://claude.com/claude-code")
		} else {
			fmt.Println("✓ claude binary found")
		}

		path, _ := config.Path()
		if _, err := os.Stat(path); err == nil {
			fmt.Printf("✓ config already exists at %s (edit it directly)\n", path)
		} else {
			if _, err := config.Parse([]byte(defaultConfig)); err != nil {
				return fmt.Errorf("internal: default config invalid: %w", err)
			}
			if err := os.WriteFile(path, []byte(defaultConfig), 0o600); err != nil {
				return err
			}
			fmt.Printf("✓ wrote %s\n", path)
		}

		if _, err := launch.Token(dataDir); err != nil {
			return err
		}
		fmt.Println("✓ router token ready")

		if os.Getenv("ANTHROPIC_API_KEY") == "" {
			fmt.Println("⚠ ANTHROPIC_API_KEY is not set — the router bills via API keys, not your Claude subscription.")
			fmt.Println("  Set it in your shell profile, or use `agentic -p subscription` for normal subscription claude.")
		} else {
			fmt.Println("✓ ANTHROPIC_API_KEY set")
		}

		if err := registerStatusline(); err != nil {
			fmt.Printf("⚠ could not register statusline: %v\n", err)
		}

		if _, err := exec.LookPath("clauder"); err == nil {
			fmt.Println("✓ clauder found — sessions will launch via `clauder wrap` (cross-instance messaging + memory)")
		} else {
			fmt.Println("· clauder not installed (optional): https://github.com/MaorBril/clauder")
		}

		fmt.Println("\nDone. Start a session with: agentic")
		return nil
	},
}
