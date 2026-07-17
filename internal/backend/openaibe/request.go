// Package openaibe translates the Anthropic Messages API to the OpenAI
// Chat Completions dialect (OpenAI, xAI, Ollama, vLLM, OpenRouter, …).
package openaibe

import (
	"fmt"

	"github.com/maorbril/agentic/internal/anthropic"
	"github.com/maorbril/agentic/internal/config"
	"github.com/maorbril/agentic/internal/openai"
)

// TranslateRequest maps an Anthropic request onto a ChatRequest. Fidelity
// gaps (documented in the README): cache_control stripped, server tools
// dropped, thinking blocks not round-tripped, top_k dropped, stop
// sequences truncated to 4.
func TranslateRequest(req *anthropic.MessagesRequest, route config.Resolved) (*openai.ChatRequest, error) {
	out := &openai.ChatRequest{
		Model:  route.Model.ID,
		Stream: req.Stream,
	}
	if req.Stream {
		out.StreamOptions = &openai.StreamOptions{IncludeUsage: true}
	}

	if sys := req.System.Text(); sys != "" {
		out.Messages = append(out.Messages, openai.ChatMessage{Role: "system", Content: sys})
	}

	for _, msg := range req.Messages {
		translated, err := translateMessage(msg)
		if err != nil {
			return nil, err
		}
		out.Messages = append(out.Messages, translated...)
	}

	for _, t := range req.Tools {
		if t.IsServerTool() {
			continue // web_search etc. — cannot translate; Claude Code degrades gracefully
		}
		out.Tools = append(out.Tools, openai.Tool{
			Type:     "function",
			Function: openai.Function{Name: t.Name, Description: t.Description, Parameters: t.InputSchema},
		})
	}

	if tc := req.ToolChoice; tc != nil {
		switch tc.Type {
		case "auto":
			out.ToolChoice = "auto"
		case "any":
			out.ToolChoice = "required"
		case "none":
			out.ToolChoice = "none"
		case "tool":
			out.ToolChoice = map[string]any{"type": "function", "function": map[string]string{"name": tc.Name}}
		}
		if tc.DisableParallelToolUse {
			f := false
			out.ParallelToolCalls = &f
		}
	}

	// max_tokens parameter name is provider-specific (o-series/GPT-5 use
	// max_completion_tokens).
	if route.Provider.MaxTokensParam == "max_completion_tokens" {
		out.MaxCompletionTokens = req.MaxTokens
	} else {
		out.MaxTokens = req.MaxTokens
	}

	reasoning := route.Model.Reasoning
	switch reasoning {
	case "effort":
		out.ReasoningEffort = effortFromBudget(req.Thinking)
		// Reasoning models reject sampling params.
	case "passive", "none", "":
		out.Temperature = req.Temperature
		out.TopP = req.TopP
	}

	if n := len(req.StopSequences); n > 0 {
		if n > 4 {
			n = 4 // OpenAI limit
		}
		out.Stop = req.StopSequences[:n]
	}
	return out, nil
}

// translateMessage fans one Anthropic message out to one or more OpenAI
// messages. Ordering constraint: role:"tool" results must immediately
// follow the assistant tool_calls message, so tool_results come first and
// remaining text/images become a trailing user message.
func translateMessage(msg anthropic.Message) ([]openai.ChatMessage, error) {
	switch msg.Role {
	case "assistant":
		out := openai.ChatMessage{Role: "assistant"}
		text := ""
		for _, b := range msg.Content {
			switch b.Type {
			case "text":
				if text != "" {
					text += "\n"
				}
				text += b.Text
			case "tool_use":
				out.ToolCalls = append(out.ToolCalls, openai.ToolCall{
					ID:       b.ID,
					Type:     "function",
					Function: openai.FunctionCall{Name: b.Name, Arguments: string(b.Input)},
				})
			case "thinking", "redacted_thinking":
				// Dropped on resend — no OpenAI slot, and DeepSeek requires omitting it.
			}
		}
		if text != "" {
			out.Content = text
		}
		if out.Content == nil && len(out.ToolCalls) == 0 {
			return nil, nil
		}
		return []openai.ChatMessage{out}, nil

	case "user":
		var out []openai.ChatMessage
		var parts []openai.ContentPart
		for _, b := range msg.Content {
			switch b.Type {
			case "tool_result":
				content := b.FlatText()
				if b.IsError {
					content = "Error: " + content
				}
				out = append(out, openai.ChatMessage{Role: "tool", ToolCallID: b.ToolUseID, Content: content})
			case "text":
				parts = append(parts, openai.ContentPart{Type: "text", Text: b.Text})
			case "image":
				if b.Source == nil {
					continue
				}
				url := b.Source.URL
				if b.Source.Type == "base64" {
					url = fmt.Sprintf("data:%s;base64,%s", b.Source.MediaType, b.Source.Data)
				}
				parts = append(parts, openai.ContentPart{Type: "image_url", ImageURL: &openai.ImageURL{URL: url}})
			}
		}
		if len(parts) > 0 {
			m := openai.ChatMessage{Role: "user"}
			if len(parts) == 1 && parts[0].Type == "text" {
				m.Content = parts[0].Text
			} else {
				m.Content = parts
			}
			out = append(out, m)
		}
		return out, nil

	default:
		return nil, fmt.Errorf("unsupported message role %q", msg.Role)
	}
}

// effortFromBudget maps thinking budget_tokens to reasoning_effort.
func effortFromBudget(t *anthropic.Thinking) string {
	if t == nil || t.Type == "disabled" {
		return ""
	}
	switch {
	case t.BudgetTokens == 0:
		return "medium" // adaptive thinking — no budget signal
	case t.BudgetTokens <= 1024:
		return "low"
	case t.BudgetTokens <= 8192:
		return "medium"
	default:
		return "high"
	}
}
