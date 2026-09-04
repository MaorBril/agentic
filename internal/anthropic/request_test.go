package anthropic

import (
	"strings"
	"testing"
)

// With deferred tool loading on, Claude Code answers its own ToolSearch call
// with tool_reference blocks — no text. The Anthropic API expands those into
// schemas; a translated backend cannot, and flattening them to "" would hand
// an OpenAI-dialect server an empty tool message.
func TestFlatTextRendersToolReferences(t *testing.T) {
	block := ContentBlock{
		Type:      "tool_result",
		ToolUseID: "toolu_1",
		Content: MessageBody{
			{Type: "tool_reference", ToolName: "WebFetch"},
			{Type: "tool_reference", ToolName: "WebSearch"},
		},
	}
	got := block.FlatText()
	if got == "" {
		t.Fatal("tool_reference content flattened to empty text")
	}
	for _, name := range []string{"WebFetch", "WebSearch"} {
		if !strings.Contains(got, name) {
			t.Errorf("FlatText() = %q, missing referenced tool %s", got, name)
		}
	}
	if lines := strings.Split(got, "\n"); len(lines) != 2 {
		t.Errorf("FlatText() = %q, want one line per reference", got)
	}
}

// A reference with no name still has to produce something: the point is that
// the tool result is never empty.
func TestFlatTextUnnamedToolReference(t *testing.T) {
	block := ContentBlock{Type: "tool_result", Content: MessageBody{{Type: "tool_reference"}}}
	if got := block.FlatText(); strings.TrimSpace(got) == "" {
		t.Errorf("FlatText() = %q, want non-empty text for an unnamed reference", got)
	}
}

// Mixed content keeps its text and its references, in order.
func TestFlatTextMixedContent(t *testing.T) {
	block := ContentBlock{
		Type: "tool_result",
		Content: MessageBody{
			{Type: "text", Text: "found 2 tools"},
			{Type: "tool_reference", ToolName: "Monitor"},
		},
	}
	got := block.FlatText()
	if !strings.HasPrefix(got, "found 2 tools\n") || !strings.Contains(got, "Monitor") {
		t.Errorf("FlatText() = %q", got)
	}
}
