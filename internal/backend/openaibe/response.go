package openaibe

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/maorbril/agentic/internal/anthropic"
	"github.com/maorbril/agentic/internal/openai"
)

// TranslateResponse maps a non-streaming ChatResponse back to the
// Anthropic response shape. alias is echoed as the model id so Claude
// Code sees the name it asked for.
func TranslateResponse(resp *openai.ChatResponse, alias string) (*anthropic.MessagesResponse, error) {
	out := &anthropic.MessagesResponse{
		ID:    "msg_" + strings.TrimPrefix(resp.ID, "chatcmpl-"),
		Type:  "message",
		Role:  "assistant",
		Model: alias,
	}
	if out.ID == "msg_" {
		out.ID = "msg_agentic"
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("upstream returned no choices")
	}
	choice := resp.Choices[0]

	if r := reasoningText(choice.Message.ReasoningContent, choice.Message.Reasoning); r != "" {
		out.Content = append(out.Content, anthropic.ContentBlock{Type: "thinking", Thinking: r})
	}
	if choice.Message.Content != "" {
		out.Content = append(out.Content, anthropic.ContentBlock{Type: "text", Text: choice.Message.Content})
	}
	for _, tc := range choice.Message.ToolCalls {
		input, err := repairJSON(tc.Function.Arguments)
		if err != nil {
			return nil, fmt.Errorf("tool call %q returned malformed arguments: %w", tc.Function.Name, err)
		}
		out.Content = append(out.Content, anthropic.ContentBlock{
			Type: "tool_use", ID: toolUseID(tc.ID), Name: tc.Function.Name, Input: input,
		})
	}

	out.StopReason = mapFinishReason(choice.FinishReason, len(choice.Message.ToolCalls) > 0)
	if resp.Usage != nil {
		out.Usage = mapUsage(resp.Usage)
	}
	return out, nil
}

func reasoningText(deepseek, openrouter string) string {
	if deepseek != "" {
		return deepseek
	}
	return openrouter
}

func toolUseID(id string) string {
	if id == "" {
		return "toolu_agentic_missing"
	}
	return id
}

func mapFinishReason(fr string, hasToolCalls bool) string {
	switch fr {
	case "tool_calls":
		return "tool_use"
	case "length":
		return "max_tokens"
	case "content_filter":
		return "refusal"
	case "stop", "":
		if hasToolCalls {
			return "tool_use"
		}
		return "end_turn"
	default:
		return "end_turn"
	}
}

// mapUsage keeps the Anthropic invariant that input_tokens excludes cache
// reads, so the cost engine's math is uniform across backends.
func mapUsage(u *openai.Usage) anthropic.Usage {
	var cached int64
	if u.PromptTokensDetails != nil {
		cached = u.PromptTokensDetails.CachedTokens
	}
	in := u.PromptTokens - cached
	if in < 0 {
		in = 0
	}
	return anthropic.Usage{
		InputTokens:          in,
		OutputTokens:         u.CompletionTokens,
		CacheReadInputTokens: cached,
	}
}

// repairJSON validates tool-call arguments, attempting a trailing-brace
// repair on truncation. Silently passing garbage would crash Claude
// Code's tool dispatch.
func repairJSON(args string) (json.RawMessage, error) {
	if strings.TrimSpace(args) == "" {
		return json.RawMessage("{}"), nil
	}
	if json.Valid([]byte(args)) {
		return json.RawMessage(args), nil
	}
	// Close unbalanced braces/brackets (common truncation failure).
	repaired := args
	var stack []byte
	inString, escaped := false, false
	for i := 0; i < len(args); i++ {
		c := args[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			stack = append(stack, '}')
		case '[':
			stack = append(stack, ']')
		case '}', ']':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	if inString {
		repaired += `"`
	}
	for i := len(stack) - 1; i >= 0; i-- {
		repaired += string(stack[i])
	}
	if json.Valid([]byte(repaired)) {
		return json.RawMessage(repaired), nil
	}
	return nil, fmt.Errorf("unrepairable JSON: %.80s", args)
}
