package anthropic

import (
	"encoding/json"
	"fmt"
)

// MessagesRequest is the parsed shape of a /v1/messages body, used by the
// translation backend. Passthrough never parses this deeply.
type MessagesRequest struct {
	Model         string          `json:"model"`
	MaxTokens     int             `json:"max_tokens"`
	System        SystemPrompt    `json:"system,omitempty"`
	Messages      []Message       `json:"messages"`
	Tools         []Tool          `json:"tools,omitempty"`
	ToolChoice    *ToolChoice     `json:"tool_choice,omitempty"`
	Temperature   *float64        `json:"temperature,omitempty"`
	TopP          *float64        `json:"top_p,omitempty"`
	TopK          *int            `json:"top_k,omitempty"`
	StopSequences []string        `json:"stop_sequences,omitempty"`
	Stream        bool            `json:"stream,omitempty"`
	Thinking      *Thinking       `json:"thinking,omitempty"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
}

func ParseRequest(raw []byte) (*MessagesRequest, error) {
	var req MessagesRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("parsing messages request: %w", err)
	}
	return &req, nil
}

// SystemPrompt accepts both the string form and the block-array form.
type SystemPrompt []ContentBlock

func (s *SystemPrompt) UnmarshalJSON(data []byte) error {
	var str string
	if json.Unmarshal(data, &str) == nil {
		*s = SystemPrompt{{Type: "text", Text: str}}
		return nil
	}
	var blocks []ContentBlock
	if err := json.Unmarshal(data, &blocks); err != nil {
		return err
	}
	*s = blocks
	return nil
}

// Text concatenates the prompt's text blocks.
func (s SystemPrompt) Text() string {
	out := ""
	for _, b := range s {
		if b.Type == "text" {
			if out != "" {
				out += "\n\n"
			}
			out += b.Text
		}
	}
	return out
}

type Message struct {
	Role    string      `json:"role"`
	Content MessageBody `json:"content"`
}

// MessageBody accepts both the string form and the block-array form.
type MessageBody []ContentBlock

func (m *MessageBody) UnmarshalJSON(data []byte) error {
	var str string
	if json.Unmarshal(data, &str) == nil {
		*m = MessageBody{{Type: "text", Text: str}}
		return nil
	}
	var blocks []ContentBlock
	if err := json.Unmarshal(data, &blocks); err != nil {
		return err
	}
	*m = blocks
	return nil
}

// ContentBlock is the tagged union of Messages API content blocks; only
// the fields for the given Type are populated.
type ContentBlock struct {
	Type string `json:"type"`

	// text / thinking
	Text     string `json:"text,omitempty"`
	Thinking string `json:"thinking,omitempty"`

	// image
	Source *ImageSource `json:"source,omitempty"`

	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// tool_result
	ToolUseID string      `json:"tool_use_id,omitempty"`
	Content   MessageBody `json:"content,omitempty"`
	IsError   bool        `json:"is_error,omitempty"`
}

// FlatText renders a tool_result's content (string or blocks) as text.
func (b ContentBlock) FlatText() string {
	out := ""
	for _, c := range b.Content {
		switch c.Type {
		case "text":
			if out != "" {
				out += "\n"
			}
			out += c.Text
		case "image":
			out += fmt.Sprintf("\n[image omitted from tool result %s]", b.ToolUseID)
		}
	}
	return out
}

type ImageSource struct {
	Type      string `json:"type"` // base64 | url
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

// Tool is a user-defined tool. Anthropic server tools carry a versioned
// Type and no InputSchema; those cannot translate to OpenAI.
type Tool struct {
	Type        string          `json:"type,omitempty"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

func (t Tool) IsServerTool() bool {
	return len(t.InputSchema) == 0 && t.Type != "" && t.Type != "custom"
}

type ToolChoice struct {
	Type                   string `json:"type"`
	Name                   string `json:"name,omitempty"`
	DisableParallelToolUse bool   `json:"disable_parallel_tool_use,omitempty"`
}

type Thinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

// Response types (built by the translation backend).

type MessagesResponse struct {
	ID           string         `json:"id"`
	Type         string         `json:"type"` // "message"
	Role         string         `json:"role"` // "assistant"
	Model        string         `json:"model"`
	Content      []ContentBlock `json:"content"`
	StopReason   string         `json:"stop_reason,omitempty"`
	StopSequence *string        `json:"stop_sequence"`
	Usage        Usage          `json:"usage"`
}
