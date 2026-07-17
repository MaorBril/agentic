// Package backend defines the upstream-provider interface the router
// dispatches to.
package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/maorbril/agentic/internal/anthropic"
	"github.com/maorbril/agentic/internal/config"
)

// Call is one client request, carrying both the raw body (byte-faithful
// passthrough) and the resolved route.
type Call struct {
	Raw      []byte
	Envelope anthropic.Envelope
	Route    config.Resolved
	Header   http.Header
	Query    url.Values
}

// Result is what a backend reports after serving a call.
type Result struct {
	Usage   anthropic.Usage
	Status  int
	ErrType string // empty on success
}

type Backend interface {
	// Messages serves one /v1/messages call, writing the Anthropic-shaped
	// response (JSON or SSE) directly to w. Usage may be partial on error.
	Messages(ctx context.Context, call *Call, w http.ResponseWriter) Result
	// CountTokens serves one /v1/messages/count_tokens call.
	CountTokens(ctx context.Context, call *Call, w http.ResponseWriter) Result
}

// NewTransport builds the shared upstream transport: connect timeouts but
// no overall request deadline — agent turns stream for many minutes.
func NewTransport() *http.Transport {
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 120 * time.Second,
	}
}

// RewriteModel replaces only the "model" field in a raw request body,
// leaving every other byte-equivalent field intact (numbers preserved via
// json.Number).
func RewriteModel(raw []byte, model string) ([]byte, error) {
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	m["model"] = model
	return json.Marshal(m)
}
