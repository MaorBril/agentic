// Package anthropicbe forwards requests to the Anthropic API byte-faithfully,
// teeing usage out of the response.
package anthropicbe

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/maorbril/agentic/internal/anthropic"
	"github.com/maorbril/agentic/internal/backend"
)

type Backend struct {
	client *http.Client
}

func New() *Backend {
	return &Backend{client: &http.Client{Transport: backend.NewTransport()}}
}

func (b *Backend) Messages(ctx context.Context, call *backend.Call, w http.ResponseWriter) backend.Result {
	return b.forward(ctx, call, w, "/v1/messages", true)
}

func (b *Backend) CountTokens(ctx context.Context, call *backend.Call, w http.ResponseWriter) backend.Result {
	return b.forward(ctx, call, w, "/v1/messages/count_tokens", false)
}

func (b *Backend) forward(ctx context.Context, call *backend.Call, w http.ResponseWriter, path string, tee bool) backend.Result {
	body := call.Raw
	// Byte-faithful when the alias already is the upstream model id —
	// cache_control, thinking blocks, and unknown future fields survive.
	if call.Envelope.Model != call.Route.Model.ID {
		var err error
		body, err = rewriteForModel(call.Raw, call.Route.Model.ID)
		if err != nil {
			anthropic.WriteError(w, 400, "invalid_request_error", "agentic: could not parse request body: "+err.Error())
			return backend.Result{Status: 400, ErrType: "invalid_request_error"}
		}
	}

	u := strings.TrimSuffix(call.Route.Provider.BaseURL, "/") + path
	if q := call.Query.Encode(); q != "" {
		u += "?" + q
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		anthropic.WriteError(w, 500, "api_error", "agentic: "+err.Error())
		return backend.Result{Status: 500, ErrType: "api_error"}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", call.Route.Provider.Key())
	for _, h := range []string{"anthropic-version", "anthropic-beta"} {
		if v := call.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	if req.Header.Get("anthropic-version") == "" {
		req.Header.Set("anthropic-version", "2023-06-01")
	}

	resp, err := b.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return backend.Result{Status: 499, ErrType: "client_disconnect"}
		}
		anthropic.WriteError(w, 500, "api_error", fmt.Sprintf("anthropic upstream: %v", err))
		return backend.Result{Status: 502, ErrType: "api_error"}
	}
	defer resp.Body.Close()

	// Upstream responses (including errors) are already Anthropic-shaped;
	// pass status, content headers, and body through unchanged.
	for _, h := range []string{"Content-Type", "Cache-Control", "anthropic-ratelimit-requests-remaining", "retry-after", "request-id"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	res := backend.Result{Status: resp.StatusCode}
	if resp.StatusCode >= 400 {
		res.ErrType = anthropic.ErrorTypeForStatus(resp.StatusCode)
	}
	if !tee || resp.StatusCode >= 400 {
		buf, _ := io.ReadAll(resp.Body)
		w.Write(buf)
		if resp.StatusCode >= 400 {
			res.ErrMsg = errSnippet(buf)
		}
		return res
	}

	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		res.Usage = copySSEWithUsageTee(resp.Body, w)
		return res
	}

	buf, err := io.ReadAll(resp.Body)
	if err == nil {
		var parsed struct {
			Usage anthropic.Usage `json:"usage"`
		}
		json.Unmarshal(buf, &parsed)
		res.Usage = parsed.Usage
	}
	w.Write(buf)
	return res
}

// copySSEWithUsageTee streams the SSE body through untouched while watching
// data: lines for message_start (input + cache tokens) and message_delta
// (output tokens). Flushes after every line so streaming stays live.
func copySSEWithUsageTee(r io.Reader, w http.ResponseWriter) anthropic.Usage {
	var usage anthropic.Usage
	flusher, _ := w.(http.Flusher)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		w.Write(line)
		w.Write([]byte("\n"))
		if flusher != nil {
			flusher.Flush()
		}
		if data, ok := bytes.CutPrefix(line, []byte("data: ")); ok {
			scanUsage(data, &usage)
		}
	}
	return usage
}

// errSnippet extracts a short error message from an Anthropic error body.
func errSnippet(body []byte) string {
	var apiErr anthropic.APIError
	msg := ""
	if json.Unmarshal(body, &apiErr) == nil && apiErr.Error.Message != "" {
		msg = apiErr.Error.Message
	} else {
		msg = strings.TrimSpace(string(body))
	}
	if len(msg) > 200 {
		msg = msg[:200] + "…"
	}
	return msg
}

func scanUsage(data []byte, usage *anthropic.Usage) {
	// Cheap pre-filter before JSON parsing.
	if !bytes.Contains(data, []byte(`"usage"`)) {
		return
	}
	var ev struct {
		Type    string `json:"type"`
		Message struct {
			Usage anthropic.Usage `json:"usage"`
		} `json:"message"`
		Usage anthropic.Usage `json:"usage"`
	}
	if json.Unmarshal(data, &ev) != nil {
		return
	}
	switch ev.Type {
	case "message_start":
		usage.InputTokens = ev.Message.Usage.InputTokens
		usage.CacheReadInputTokens = ev.Message.Usage.CacheReadInputTokens
		usage.CacheCreationInputTokens = ev.Message.Usage.CacheCreationInputTokens
	case "message_delta":
		if ev.Usage.OutputTokens > 0 {
			usage.OutputTokens = ev.Usage.OutputTokens
		}
	}
}
