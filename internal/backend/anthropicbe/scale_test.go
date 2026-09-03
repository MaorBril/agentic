package anthropicbe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const scaleSSE = `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":100,"output_tokens":1,"cache_read_input_tokens":50,"cache_creation_input_tokens":8}}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":42}}

`

// A rule anchored to a 1M gauge must make its Anthropic tier report on
// that same scale — otherwise the client's gauge jumps every time routing
// crosses between a translated model and a passthrough one, which is the
// whole thing the anchor exists to prevent.
func TestPassthroughScalesStreamedUsageToGauge(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(scaleSSE))
	}))
	defer up.Close()

	call := mkCall(t, `{"model":"claude-sonnet-5","stream":true,"messages":[]}`, up.URL, "claude-sonnet-5")
	call.GaugeBudget = 1_000_000 // factor 0.2
	rec := httptest.NewRecorder()
	res := New().Messages(context.Background(), call, rec)

	// Usage reported back to the router is always TRUE: budgets and
	// pricing must never see a scaled number.
	if res.Usage.InputTokens != 100 || res.Usage.CacheReadInputTokens != 50 {
		t.Errorf("true usage distorted: %+v", res.Usage)
	}
	if res.ReportedInput != 32 { // ceil(0.2*100) + ceil(0.2*50) + ceil(0.2*8)
		t.Errorf("ReportedInput = %d, want 32", res.ReportedInput)
	}

	out := rec.Body.String()
	if !strings.Contains(out, `"input_tokens":20`) || !strings.Contains(out, `"cache_read_input_tokens":10`) {
		t.Errorf("client-facing message_start not scaled:\n%s", out)
	}
	// Output tokens stay true — they roll into the next request's input.
	if !strings.Contains(out, `"output_tokens":42`) {
		t.Errorf("message_delta output tokens were rewritten:\n%s", out)
	}
	// Everything else on the event survives the rewrite.
	if !strings.Contains(out, `"msg_1"`) || !strings.Contains(out, "event: message_start") {
		t.Errorf("event structure lost:\n%s", out)
	}
}

// The default path must stay byte-for-byte: no gauge, no rewriting.
func TestPassthroughUnscaledStreamIsByteIdentical(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(scaleSSE))
	}))
	defer up.Close()

	call := mkCall(t, `{"model":"claude-sonnet-5","stream":true,"messages":[]}`, up.URL, "claude-sonnet-5")
	rec := httptest.NewRecorder()
	res := New().Messages(context.Background(), call, rec)

	if rec.Body.String() != scaleSSE {
		t.Errorf("unscaled stream altered:\n got %q\nwant %q", rec.Body.String(), scaleSSE)
	}
	if res.ReportedInput != 158 {
		t.Errorf("ReportedInput = %d, want the true 158", res.ReportedInput)
	}
}

func TestPassthroughScalesJSONUsage(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"msg_1","type":"message","content":[{"type":"text","text":"hi"}],
		  "usage":{"input_tokens":1000,"output_tokens":10,"cache_read_input_tokens":500}}`))
	}))
	defer up.Close()

	call := mkCall(t, `{"model":"claude-sonnet-5","messages":[]}`, up.URL, "claude-sonnet-5")
	call.GaugeBudget = 400_000 // factor 0.5
	rec := httptest.NewRecorder()
	res := New().Messages(context.Background(), call, rec)

	var got struct {
		ID      string `json:"id"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens     int64 `json:"input_tokens"`
			OutputTokens    int64 `json:"output_tokens"`
			CacheReadTokens int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body unparseable after rewrite: %v\n%s", err, rec.Body.String())
	}
	if got.Usage.InputTokens != 500 || got.Usage.CacheReadTokens != 250 {
		t.Errorf("usage not scaled: %+v", got.Usage)
	}
	if got.Usage.OutputTokens != 10 {
		t.Errorf("output tokens scaled: %d", got.Usage.OutputTokens)
	}
	if got.ID != "msg_1" || len(got.Content) != 1 || got.Content[0].Text != "hi" {
		t.Errorf("non-usage fields lost in rewrite: %+v", got)
	}
	if res.Usage.InputTokens != 1000 {
		t.Errorf("true usage distorted: %+v", res.Usage)
	}
}

// A payload the rewriter cannot understand is forwarded as-is: stale
// numbers beat a response the client cannot read.
func TestScaleHelpersFailOpen(t *testing.T) {
	junk := []byte(`{"usage": not json`)
	if got := scaleResponseBody(junk, 0.5); string(got) != string(junk) {
		t.Errorf("unparseable body altered: %s", got)
	}
	data := []byte(`{"type":"content_block_delta","usage":{"input_tokens":5}}`)
	if got := scaleSSEData(data, 0.5); string(got) != string(data) {
		t.Errorf("non-message_start event rewritten: %s", got)
	}
	noUsage := []byte(`{"type":"message_start","message":{"id":"m"}}`)
	if got := scaleSSEData(noUsage, 0.5); string(got) != string(noUsage) {
		t.Errorf("usage-free event rewritten: %s", got)
	}
}
