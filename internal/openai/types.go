// Package openai holds the Chat Completions wire types the translation
// backend speaks upstream. Hand-rolled — no SDK dependency.
package openai

import "encoding/json"

type ChatRequest struct {
	Model               string         `json:"model"`
	Messages            []ChatMessage  `json:"messages"`
	MaxTokens           int            `json:"max_tokens,omitempty"`
	MaxCompletionTokens int            `json:"max_completion_tokens,omitempty"`
	Temperature         *float64       `json:"temperature,omitempty"`
	TopP                *float64       `json:"top_p,omitempty"`
	Stop                []string       `json:"stop,omitempty"`
	Stream              bool           `json:"stream,omitempty"`
	StreamOptions       *StreamOptions `json:"stream_options,omitempty"`
	Tools               []Tool         `json:"tools,omitempty"`
	ToolChoice          any            `json:"tool_choice,omitempty"`
	ParallelToolCalls   *bool          `json:"parallel_tool_calls,omitempty"`
	ReasoningEffort     string         `json:"reasoning_effort,omitempty"`
	User                string         `json:"user,omitempty"`
}

type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type ChatMessage struct {
	Role       string     `json:"role"`
	Content    any        `json:"content,omitempty"` // string or []ContentPart
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type ContentPart struct {
	Type     string    `json:"type"` // text | image_url
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

type ImageURL struct {
	URL string `json:"url"`
}

type Tool struct {
	Type     string   `json:"type"` // "function"
	Function Function `json:"function"`
}

type Function struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type ToolCall struct {
	Index    *int         `json:"index,omitempty"` // streaming only
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Function FunctionCall `json:"function"`
}

type FunctionCall struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type ChatResponse struct {
	ID      string   `json:"id"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   *Usage   `json:"usage"`
}

type Choice struct {
	Index        int             `json:"index"`
	Message      ResponseMessage `json:"message"`
	FinishReason string          `json:"finish_reason"`
}

type ResponseMessage struct {
	Role             string     `json:"role"`
	Content          string     `json:"content"`
	ReasoningContent string     `json:"reasoning_content,omitempty"` // DeepSeek
	Reasoning        string     `json:"reasoning,omitempty"`         // OpenRouter
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
}

type Usage struct {
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details,omitempty"`
}

// Chunk is one streaming SSE data payload.
type Chunk struct {
	ID      string        `json:"id"`
	Model   string        `json:"model"`
	Choices []ChunkChoice `json:"choices"`
	Usage   *Usage        `json:"usage"`
	Error   *ChunkError   `json:"error"`
}

type ChunkChoice struct {
	Index        int        `json:"index"`
	Delta        ChunkDelta `json:"delta"`
	FinishReason string     `json:"finish_reason"`
}

type ChunkDelta struct {
	Role             string     `json:"role,omitempty"`
	Content          string     `json:"content,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	Reasoning        string     `json:"reasoning,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
}

type ChunkError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

// ErrorResponse is the OpenAI error body.
type ErrorResponse struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	} `json:"error"`
}
