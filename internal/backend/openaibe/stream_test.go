package openaibe

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/maorbril/agentic/internal/anthropic"
)

// runStream feeds recorded OpenAI chunks through the state machine and
// returns the emitted Anthropic events as (event, payload) pairs.
func runStream(t *testing.T, chunks []string) []event {
	t.Helper()
	rec := httptest.NewRecorder()
	state := newStreamState(anthropic.NewSSEWriter(rec), "gpt")
	body := ""
	for _, c := range chunks {
		body += "data: " + c + "\n\n"
	}
	body += "data: [DONE]\n\n"
	usage, errType := state.Run(strings.NewReader(body))
	_ = usage
	if errType != "" {
		t.Fatalf("stream errType=%q", errType)
	}
	return parseEvents(t, rec.Body.String())
}

type event struct {
	name string
	data map[string]any
}

func parseEvents(t *testing.T, raw string) []event {
	t.Helper()
	var out []event
	var name string
	for _, line := range strings.Split(raw, "\n") {
		if v, ok := strings.CutPrefix(line, "event: "); ok {
			name = v
		}
		if v, ok := strings.CutPrefix(line, "data: "); ok {
			var data map[string]any
			if err := json.Unmarshal([]byte(v), &data); err != nil {
				t.Fatalf("bad event data %q: %v", v, err)
			}
			out = append(out, event{name, data})
		}
	}
	return out
}

func names(evs []event) string {
	parts := make([]string, len(evs))
	for i, e := range evs {
		parts[i] = e.name
	}
	return strings.Join(parts, " ")
}

func TestStreamTextOnly(t *testing.T) {
	evs := runStream(t, []string{
		`{"id":"c1","choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"}}]}`,
		`{"id":"c1","choices":[{"index":0,"delta":{"content":"lo"}}]}`,
		`{"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`{"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2}}`,
	})
	want := "message_start ping content_block_start content_block_delta content_block_delta content_block_stop message_delta message_stop"
	if names(evs) != want {
		t.Fatalf("grammar:\n got %s\nwant %s", names(evs), want)
	}
	last := evs[len(evs)-2]
	delta := last.data["delta"].(map[string]any)
	if delta["stop_reason"] != "end_turn" {
		t.Errorf("stop_reason = %v", delta["stop_reason"])
	}
	usage := last.data["usage"].(map[string]any)
	if usage["input_tokens"].(float64) != 10 || usage["output_tokens"].(float64) != 2 {
		t.Errorf("usage: %v", usage)
	}
}

func TestStreamToolCalls(t *testing.T) {
	evs := runStream(t, []string{
		`{"id":"c2","choices":[{"index":0,"delta":{"role":"assistant","content":"Let me check."}}]}`,
		`{"id":"c2","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"read_file","arguments":""}}]}}]}`,
		`{"id":"c2","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`,
		`{"id":"c2","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}`,
		`{"id":"c2","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"bash","arguments":"{\"cmd\":\"ls\"}"}}]}}]}`,
		`{"id":"c2","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`{"id":"c2","choices":[],"usage":{"prompt_tokens":50,"completion_tokens":30,"prompt_tokens_details":{"cached_tokens":20}}}`,
	})

	// Text block, then two tool_use blocks with sequential indices.
	var starts []map[string]any
	for _, e := range evs {
		if e.name == "content_block_start" {
			starts = append(starts, e.data)
		}
	}
	if len(starts) != 3 {
		t.Fatalf("expected 3 content_block_start, got %d: %s", len(starts), names(evs))
	}
	cb1 := starts[1]["content_block"].(map[string]any)
	cb2 := starts[2]["content_block"].(map[string]any)
	if cb1["type"] != "tool_use" || cb1["name"] != "read_file" || cb1["id"] != "call_a" {
		t.Errorf("first tool block: %v", cb1)
	}
	if cb2["name"] != "bash" {
		t.Errorf("second tool block: %v", cb2)
	}
	if starts[1]["index"].(float64) != 1 || starts[2]["index"].(float64) != 2 {
		t.Errorf("block indices: %v %v", starts[1]["index"], starts[2]["index"])
	}

	// input_json_delta fragments reassemble the arguments
	var args string
	for _, e := range evs {
		if e.name == "content_block_delta" {
			d := e.data["delta"].(map[string]any)
			if d["type"] == "input_json_delta" && e.data["index"].(float64) == 1 {
				args += d["partial_json"].(string)
			}
		}
	}
	if args != `{"path":"a.go"}` {
		t.Errorf("reassembled args = %q", args)
	}

	last := evs[len(evs)-2]
	if last.data["delta"].(map[string]any)["stop_reason"] != "tool_use" {
		t.Error("stop_reason should be tool_use")
	}
	if last.data["usage"].(map[string]any)["cache_read_input_tokens"].(float64) != 20 {
		t.Error("cached_tokens not surfaced as cache_read_input_tokens")
	}
	if last.data["usage"].(map[string]any)["input_tokens"].(float64) != 30 {
		t.Error("input_tokens should exclude cache reads")
	}
}

func TestStreamReasoningContent(t *testing.T) {
	evs := runStream(t, []string{
		`{"id":"c3","choices":[{"index":0,"delta":{"reasoning_content":"thinking hard"}}]}`,
		`{"id":"c3","choices":[{"index":0,"delta":{"content":"answer"}}]}`,
		`{"id":"c3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	})
	// thinking block must close with a signature_delta before the text block opens
	seen := []string{}
	for _, e := range evs {
		switch e.name {
		case "content_block_start":
			seen = append(seen, e.data["content_block"].(map[string]any)["type"].(string))
		case "content_block_delta":
			seen = append(seen, e.data["delta"].(map[string]any)["type"].(string))
		}
	}
	want := "thinking thinking_delta signature_delta text text_delta"
	if strings.Join(seen, " ") != want {
		t.Errorf("block sequence:\n got %s\nwant %s", strings.Join(seen, " "), want)
	}
}

func TestStreamSplitToolHeader(t *testing.T) {
	// Some providers split id and name across chunks — args must buffer.
	evs := runStream(t, []string{
		`{"id":"c4","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_x","function":{"arguments":"{\"a\""}}]}}]}`,
		`{"id":"c4","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"late_name","arguments":":1}"}}]}}]}`,
		`{"id":"c4","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	})
	var opened string
	var args string
	for _, e := range evs {
		if e.name == "content_block_start" {
			opened = e.data["content_block"].(map[string]any)["name"].(string)
		}
		if e.name == "content_block_delta" {
			d := e.data["delta"].(map[string]any)
			if d["type"] == "input_json_delta" {
				args += d["partial_json"].(string)
			}
		}
	}
	if opened != "late_name" || args != `{"a":1}` {
		t.Errorf("opened=%q args=%q", opened, args)
	}
}
