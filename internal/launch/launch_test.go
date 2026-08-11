package launch

import (
	"slices"
	"strings"
	"testing"

	"github.com/maorbril/agentic/internal/config"
)

// envVal returns the value of key in env, or "" if unset.
func envVal(env []string, key string) string {
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return strings.TrimPrefix(kv, prefix)
		}
	}
	return ""
}

// Without pin_tiers, small_fast/tiers map through unchanged and no pin header
// is sent — the pre-existing per-tier routing behavior must be untouched.
func TestSessionEnvWithoutPinTiers(t *testing.T) {
	prof := config.Profile{
		Model:     "gpt-5.6-sol",
		SmallFast: "haiku",
		Tiers:     map[string]string{"opus": "opus", "sonnet": "sonnet", "haiku": "haiku"},
	}
	env := sessionEnv(nil, "http://127.0.0.1:41100", "tok", "sess-1", "main", prof, prof.Model)

	if got := envVal(env, "ANTHROPIC_MODEL"); got != "gpt-5.6-sol" {
		t.Errorf("ANTHROPIC_MODEL = %q, want gpt-5.6-sol", got)
	}
	if got := envVal(env, "ANTHROPIC_SMALL_FAST_MODEL"); got != "haiku" {
		t.Errorf("ANTHROPIC_SMALL_FAST_MODEL = %q, want haiku (from small_fast, not pinned)", got)
	}
	if got := envVal(env, "ANTHROPIC_DEFAULT_OPUS_MODEL"); got != "opus" {
		t.Errorf("ANTHROPIC_DEFAULT_OPUS_MODEL = %q, want opus (from tiers, not pinned)", got)
	}
	if got := envVal(env, "CLAUDE_CODE_SUBAGENT_MODEL"); got != "" {
		t.Errorf("CLAUDE_CODE_SUBAGENT_MODEL = %q, want unset when pin_tiers is off", got)
	}
	headers := envVal(env, "ANTHROPIC_CUSTOM_HEADERS")
	if strings.Contains(headers, "X-Agentic-Pin-Model") {
		t.Errorf("custom headers should not carry a pin when pin_tiers is off: %q", headers)
	}
}

// With pin_tiers, every tier fallback — including Claude Code's own subagent
// spawn variable — must resolve to the profile's main model, and the router
// must receive X-Agentic-Pin-Model so it enforces the pin server-side too.
func TestSessionEnvWithPinTiers(t *testing.T) {
	prof := config.Profile{
		Model:     "gpt-5.6-sol",
		SmallFast: "haiku",                                               // must be overridden
		Tiers:     map[string]string{"opus": "opus", "sonnet": "sonnet"}, // must be overridden
		PinTiers:  true,
	}
	env := sessionEnv(nil, "http://127.0.0.1:41100", "tok", "sess-1", "main", prof, prof.Model)

	for _, key := range []string{
		"ANTHROPIC_SMALL_FAST_MODEL",
		"ANTHROPIC_DEFAULT_OPUS_MODEL",
		"ANTHROPIC_DEFAULT_SONNET_MODEL",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL",
		"CLAUDE_CODE_SUBAGENT_MODEL",
	} {
		if got := envVal(env, key); got != "gpt-5.6-sol" {
			t.Errorf("%s = %q, want gpt-5.6-sol (pinned)", key, got)
		}
	}
	headers := envVal(env, "ANTHROPIC_CUSTOM_HEADERS")
	if !strings.Contains(headers, "X-Agentic-Pin-Model: gpt-5.6-sol") {
		t.Errorf("custom headers missing pin for router enforcement: %q", headers)
	}
	if !strings.Contains(headers, "X-Agentic-Session: sess-1") || !strings.Contains(headers, "X-Agentic-Profile: main") {
		t.Errorf("custom headers missing session/profile attribution: %q", headers)
	}
}

// Pinned rather than read from autoApprovedTools: this is the set that
// `clauder wrap --slave` used to pass, and silently dropping an entry would
// change every session's permission posture.
var wantTools = []string{
	"Read", "Write", "Edit", "Glob", "Grep", "Bash(*)",
	"WebFetch", "WebSearch", "mcp__clauder__*",
}

// claude's --allowedTools is variadic — it consumes args until the next flag,
// so a caller's bare prompt sitting after it is eaten as a tool name and
// claude exits with "Input must be provided". Caller args have to come first.
func TestBuildChild(t *testing.T) {
	got := buildChild(Options{
		InstanceName: "backend",
		ClaudeArgs:   []string{"--resume", "fix the bug"},
	})

	want := []string{"claude", "--resume", "fix the bug", "--name", "backend"}
	for _, tool := range wantTools {
		want = append(want, "--allowedTools", tool)
	}
	if !slices.Equal(got, want) {
		t.Errorf("buildChild:\n got  %v\n want %v", got, want)
	}
}

func TestBuildChildWithoutInstanceName(t *testing.T) {
	got := buildChild(Options{})
	if got[0] != "claude" {
		t.Fatalf("child should be claude, got %q", got[0])
	}
	if slices.Contains(got, "--name") {
		t.Errorf("no instance name should leave claude to derive one: %v", got)
	}
}
