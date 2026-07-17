package anthropicbe

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/maorbril/agentic/internal/anthropic"
	"github.com/maorbril/agentic/internal/backend"
	"github.com/maorbril/agentic/internal/config"
)

func mkCall(t *testing.T, body, baseURL, upstreamModel string) *backend.Call {
	t.Helper()
	env, err := anthropic.ParseEnvelope([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return &backend.Call{
		Raw:      []byte(body),
		Envelope: env,
		Route: config.Resolved{
			Alias:        env.Model,
			ProviderName: "anthropic",
			Provider:     config.Provider{Type: "anthropic", BaseURL: baseURL, APIKey: "sk-test"},
			Model:        config.Model{ID: upstreamModel},
		},
		Header: http.Header{"Anthropic-Version": []string{"2023-06-01"}, "Anthropic-Beta": []string{"claude-code-20250219"}},
		Query:  url.Values{},
	}
}

func TestByteFaithfulPassthrough(t *testing.T) {
	body := `{"model":"claude-sonnet-5","max_tokens":100,"temperature":0.5,"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}]}`
	var gotBody []byte
	var gotHeaders http.Header
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"msg_1","usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":3,"cache_creation_input_tokens":2}}`))
	}))
	defer up.Close()

	call := mkCall(t, body, up.URL, "claude-sonnet-5") // alias == upstream
	rec := httptest.NewRecorder()
	res := New().Messages(context.Background(), call, rec)

	if string(gotBody) != body {
		t.Errorf("body was modified:\n got %s\nwant %s", gotBody, body)
	}
	if gotHeaders.Get("x-api-key") != "sk-test" {
		t.Error("provider key not injected")
	}
	if gotHeaders.Get("anthropic-beta") != "claude-code-20250219" {
		t.Error("anthropic-beta not forwarded")
	}
	if res.Usage.InputTokens != 10 || res.Usage.OutputTokens != 5 ||
		res.Usage.CacheReadInputTokens != 3 || res.Usage.CacheCreationInputTokens != 2 {
		t.Errorf("usage = %+v", res.Usage)
	}
}

func TestModelRewriteOnlyTouchesModel(t *testing.T) {
	body := `{"model":"cheap","max_tokens":100,"temperature":0.5,"messages":[]}`
	var gotBody map[string]json.RawMessage
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &gotBody)
		w.Write([]byte(`{}`))
	}))
	defer up.Close()

	call := mkCall(t, body, up.URL, "claude-haiku-4-5")
	New().Messages(context.Background(), call, httptest.NewRecorder())

	if string(gotBody["model"]) != `"claude-haiku-4-5"` {
		t.Errorf("model = %s", gotBody["model"])
	}
	if string(gotBody["temperature"]) != "0.5" {
		t.Errorf("temperature mangled: %s (json.Number must be preserved)", gotBody["temperature"])
	}
}

func TestSSEUsageTee(t *testing.T) {
	sse := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"usage":{"input_tokens":100,"output_tokens":1,"cache_read_input_tokens":50,"cache_creation_input_tokens":7}}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"hello"}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":42}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n") + "\n"

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(sse))
	}))
	defer up.Close()

	call := mkCall(t, `{"model":"claude-sonnet-5","stream":true,"messages":[]}`, up.URL, "claude-sonnet-5")
	rec := httptest.NewRecorder()
	res := New().Messages(context.Background(), call, rec)

	if rec.Body.String() != sse {
		t.Errorf("stream body altered:\n got %q\nwant %q", rec.Body.String(), sse)
	}
	if res.Usage.InputTokens != 100 || res.Usage.OutputTokens != 42 ||
		res.Usage.CacheReadInputTokens != 50 || res.Usage.CacheCreationInputTokens != 7 {
		t.Errorf("usage = %+v", res.Usage)
	}
}

func TestUpstreamErrorPassthrough(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(429)
		w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`))
	}))
	defer up.Close()

	call := mkCall(t, `{"model":"claude-sonnet-5","messages":[]}`, up.URL, "claude-sonnet-5")
	rec := httptest.NewRecorder()
	res := New().Messages(context.Background(), call, rec)

	if rec.Code != 429 || res.ErrType != "rate_limit_error" {
		t.Errorf("status=%d errType=%q", rec.Code, res.ErrType)
	}
	if !strings.Contains(rec.Body.String(), "slow down") {
		t.Errorf("error body not passed through: %s", rec.Body.String())
	}
}
