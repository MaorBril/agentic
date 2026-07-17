package router

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maorbril/agentic/internal/config"
	"github.com/maorbril/agentic/internal/store"
)

const testToken = "test-token"

func newTestServer(t *testing.T, upstreamURL string, budgets *config.Budget) (*httptest.Server, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{
		Router: config.Router{Port: 0},
		Providers: map[string]config.Provider{
			"fake": {Type: config.ProviderOpenAI, BaseURL: upstreamURL},
		},
		Models: map[string]config.Model{
			"fake-model": {Provider: "fake", ID: "fake-upstream-1"},
		},
		Profiles: map[string]config.Profile{
			"main": {Model: "fake-model"},
		},
		Budgets: budgets,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "agentic.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv := NewServer(cfg, testToken, dir, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, st
}

func post(t *testing.T, url, token, body string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("x-api-key", token)
	req.Header.Set("content-type", "application/json")
	req.Header.Set("X-Agentic-Session", "sess-test")
	req.Header.Set("X-Agentic-Profile", "main")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(raw)
}

// TestOpenAIStreamThroughRouter drives the full path: Anthropic-shaped
// streaming request in, OpenAI upstream, Anthropic SSE out, usage row in
// SQLite.
func TestOpenAIStreamThroughRouter(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("upstream path = %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"model":"fake-upstream-1"`) {
			t.Errorf("alias not resolved upstream: %s", body)
		}
		if !strings.Contains(string(body), `"include_usage":true`) {
			t.Error("stream_options.include_usage missing")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"id":"x","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"x","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"x","choices":[],"usage":{"prompt_tokens":11,"completion_tokens":3}}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	ts, st := newTestServer(t, upstream.URL, nil)
	resp, body := post(t, ts.URL+"/v1/messages", testToken,
		`{"model":"fake-model","max_tokens":50,"stream":true,"messages":[{"role":"user","content":"hello"}]}`)

	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Errorf("content-type: %s", resp.Header.Get("Content-Type"))
	}
	for _, want := range []string{
		"event: message_start", "event: ping", "event: content_block_start",
		`"text":"hi"`, `"text_delta"`, `"stop_reason":"end_turn"`, "event: message_stop",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("SSE missing %q in:\n%s", want, body)
		}
	}

	// Usage attributed and recorded.
	rows, err := st.SpendSince(time.Now().Add(-time.Minute), "session")
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	if rows[0].Key != "sess-test" || rows[0].InputTokens != 11 || rows[0].OutputTokens != 3 {
		t.Errorf("usage row: %+v", rows[0])
	}
}

// TestAutoRouteEndToEnd exercises dynamic routing through the full server:
// the classifier call and the routed call both hit the fake upstream.
func TestAutoRouteEndToEnd(t *testing.T) {
	var seenModels []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Model string `json:"model"`
		}
		json.Unmarshal(body, &req)
		seenModels = append(seenModels, req.Model)
		w.Header().Set("Content-Type", "application/json")
		if req.Model == "classifier-upstream" {
			fmt.Fprint(w, `{"id":"c","choices":[{"index":0,"message":{"role":"assistant","content":"deep"},"finish_reason":"stop"}],"usage":{"prompt_tokens":50,"completion_tokens":1}}`)
			return
		}
		fmt.Fprint(w, `{"id":"m","choices":[{"index":0,"message":{"role":"assistant","content":"planned"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`)
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := &config.Config{
		Providers: map[string]config.Provider{
			"fake": {Type: config.ProviderOpenAI, BaseURL: upstream.URL},
		},
		Models: map[string]config.Model{
			"cheap": {Provider: "fake", ID: "classifier-upstream"},
			"big":   {Provider: "fake", ID: "deep-upstream"},
			"small": {Provider: "fake", ID: "light-upstream"},
		},
		Routing: map[string]config.RouteRule{
			"auto": {Classifier: "cheap", Default: "light",
				Tiers: map[string]string{"deep": "big", "light": "small"}},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "agentic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := NewServer(cfg, testToken, dir, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, respBody := post(t, ts.URL+"/v1/messages", testToken,
		`{"model":"auto","max_tokens":50,"messages":[{"role":"user","content":"architect the whole system"}]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, respBody)
	}
	if len(seenModels) != 2 || seenModels[0] != "classifier-upstream" || seenModels[1] != "deep-upstream" {
		t.Errorf("upstream saw %v, want [classifier-upstream deep-upstream]", seenModels)
	}
	if !strings.Contains(respBody, `"model":"auto"`) {
		t.Errorf("alias not echoed: %s", respBody)
	}
}

func TestBudgetHardStop(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("request must not reach upstream when over budget")
	}))
	defer upstream.Close()

	ts, st := newTestServer(t, upstream.URL, &config.Budget{Daily: 0.01})
	// Pre-record spend over the cap.
	st.RecordUsage(store.UsageEvent{TS: time.Now(), Profile: "main", CostUSD: 0.02, Priced: true})

	resp, body := post(t, ts.URL+"/v1/messages", testToken,
		`{"model":"fake-model","max_tokens":50,"messages":[{"role":"user","content":"hello"}]}`)
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400 (429/5xx would retry-spin)", resp.StatusCode)
	}
	if !strings.Contains(body, "daily budget exceeded") || !strings.Contains(body, "invalid_request_error") {
		t.Errorf("body: %s", body)
	}
}

func TestAuthRequired(t *testing.T) {
	ts, _ := newTestServer(t, "http://127.0.0.1:1", nil)
	resp, _ := post(t, ts.URL+"/v1/messages", "wrong-token", `{"model":"fake-model","messages":[]}`)
	if resp.StatusCode != 401 {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestCountTokensEstimate(t *testing.T) {
	ts, _ := newTestServer(t, "http://127.0.0.1:1", nil) // upstream never reached
	resp, body := post(t, ts.URL+"/v1/messages/count_tokens", testToken,
		`{"model":"fake-model","messages":[{"role":"user","content":"`+strings.Repeat("word ", 100)+`"}]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "input_tokens") {
		t.Errorf("body: %s", body)
	}
}
