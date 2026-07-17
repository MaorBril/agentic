// Package anthropic holds the Messages API wire vocabulary the router
// speaks to Claude Code.
package anthropic

import "encoding/json"

// Envelope is the subset of a /v1/messages request the router itself
// needs; the full body is forwarded or translated from raw bytes.
type Envelope struct {
	Model  string `json:"model"`
	Stream bool   `json:"stream"`
}

func ParseEnvelope(raw []byte) (Envelope, error) {
	var e Envelope
	err := json.Unmarshal(raw, &e)
	return e, err
}

// Usage mirrors the usage block on responses and streaming events.
type Usage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
}

type CountTokensResponse struct {
	InputTokens int64 `json:"input_tokens"`
}
