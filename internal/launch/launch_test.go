package launch

import (
	"slices"
	"testing"
)

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
