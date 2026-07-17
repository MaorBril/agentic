package openaibe

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"

	"github.com/maorbril/agentic/internal/anthropic"
	"github.com/maorbril/agentic/internal/openai"
)

// streamState turns an OpenAI chunk stream into the exact Anthropic SSE
// grammar Claude Code's SDK validates:
//
//	message_start → ping → { content_block_start → delta* → stop }* →
//	message_delta(stop_reason+usage) → message_stop
type streamState struct {
	sse   *anthropic.SSEWriter
	alias string

	started      bool
	index        int    // next content block index
	openType     string // "", "thinking", "text", "tool"
	openaiToolIx int    // openai tool_call index of the open tool block
	pendingArgs  string // tool args buffered before the block could open
	pendingID    string
	pendingName  string
	havePending  bool

	finishReason string
	sawToolCall  bool
	usage        anthropic.Usage
}

func newStreamState(sse *anthropic.SSEWriter, alias string) *streamState {
	return &streamState{sse: sse, alias: alias, openaiToolIx: -1}
}

// Run consumes the upstream SSE body until EOF or [DONE], returning final
// usage. A mid-stream upstream error is forwarded as an Anthropic error
// event (Claude Code retries on it).
func (s *streamState) Run(body io.Reader) (anthropic.Usage, string) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		data, ok := bytes.CutPrefix(line, []byte("data: "))
		if !ok {
			continue
		}
		if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
			break
		}
		var chunk openai.Chunk
		if err := json.Unmarshal(data, &chunk); err != nil {
			continue // tolerate provider noise between data lines
		}
		if chunk.Error != nil {
			s.sse.ErrorEvent("api_error", "upstream: "+chunk.Error.Message)
			return s.usage, "api_error"
		}
		s.handleChunk(&chunk)
	}
	if err := scanner.Err(); err != nil {
		s.sse.ErrorEvent("api_error", "upstream stream: "+err.Error())
		return s.usage, "api_error"
	}
	s.finalize()
	return s.usage, ""
}

func (s *streamState) handleChunk(chunk *openai.Chunk) {
	if !s.started {
		s.started = true
		s.sse.Event("message_start", map[string]any{
			"type": "message_start",
			"message": anthropic.MessagesResponse{
				ID: "msg_" + chunk.ID, Type: "message", Role: "assistant",
				Model: s.alias, Content: []anthropic.ContentBlock{},
			},
		})
		s.sse.Ping()
	}
	if chunk.Usage != nil {
		s.usage = mapUsage(chunk.Usage)
	}
	if len(chunk.Choices) == 0 {
		return // usage-only final chunk
	}
	choice := chunk.Choices[0]
	delta := choice.Delta

	if r := reasoningText(delta.ReasoningContent, delta.Reasoning); r != "" {
		s.ensureBlock("thinking")
		s.sse.Event("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": s.index - 1,
			"delta": map[string]string{"type": "thinking_delta", "thinking": r},
		})
	}
	if delta.Content != "" {
		s.ensureBlock("text")
		s.sse.Event("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": s.index - 1,
			"delta": map[string]string{"type": "text_delta", "text": delta.Content},
		})
	}
	for _, tc := range delta.ToolCalls {
		s.handleToolDelta(tc)
	}
	if choice.FinishReason != "" {
		s.finishReason = choice.FinishReason
	}
}

func (s *streamState) handleToolDelta(tc openai.ToolCall) {
	ix := 0
	if tc.Index != nil {
		ix = *tc.Index
	}
	newTool := s.openType != "tool" || ix != s.openaiToolIx
	if newTool {
		s.closeBlock()
		s.openaiToolIx = ix
		s.pendingID, s.pendingName, s.pendingArgs = tc.ID, tc.Function.Name, ""
		s.havePending = true
	} else if s.havePending {
		// Still waiting for a name — some providers split id/name/args.
		if tc.ID != "" {
			s.pendingID = tc.ID
		}
		if tc.Function.Name != "" {
			s.pendingName = tc.Function.Name
		}
	}
	if s.havePending {
		s.pendingArgs += tc.Function.Arguments
		if s.pendingName == "" {
			return // can't open the block without a name yet
		}
		s.openToolBlock()
		return
	}
	if tc.Function.Arguments != "" {
		s.argsDelta(tc.Function.Arguments)
	}
}

func (s *streamState) openToolBlock() {
	id := s.pendingID
	if id == "" {
		id = "toolu_agentic_missing"
	}
	s.sse.Event("content_block_start", map[string]any{
		"type": "content_block_start", "index": s.index,
		"content_block": map[string]any{"type": "tool_use", "id": id, "name": s.pendingName, "input": map[string]any{}},
	})
	s.openType = "tool"
	s.sawToolCall = true
	s.index++
	args := s.pendingArgs
	s.havePending = false
	s.pendingID, s.pendingName, s.pendingArgs = "", "", ""
	if args != "" {
		s.argsDelta(args)
	}
}

func (s *streamState) argsDelta(fragment string) {
	s.sse.Event("content_block_delta", map[string]any{
		"type": "content_block_delta", "index": s.index - 1,
		"delta": map[string]string{"type": "input_json_delta", "partial_json": fragment},
	})
}

// ensureBlock opens a block of the wanted type, closing any other open one.
func (s *streamState) ensureBlock(kind string) {
	if s.openType == kind {
		return
	}
	s.closeBlock()
	block := map[string]any{"type": kind}
	if kind == "text" {
		block["text"] = ""
	} else {
		block["thinking"] = ""
	}
	s.sse.Event("content_block_start", map[string]any{
		"type": "content_block_start", "index": s.index, "content_block": block,
	})
	s.openType = kind
	s.index++
}

func (s *streamState) closeBlock() {
	if s.havePending {
		// Tool block that never got a name — open it with a placeholder so
		// buffered args aren't lost.
		if s.pendingName == "" {
			s.pendingName = "unknown_tool"
		}
		s.openToolBlock()
	}
	if s.openType == "" {
		return
	}
	if s.openType == "thinking" {
		// Anthropic grammar: signature_delta before closing a thinking block.
		s.sse.Event("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": s.index - 1,
			"delta": map[string]string{"type": "signature_delta", "signature": ""},
		})
	}
	s.sse.Event("content_block_stop", map[string]any{"type": "content_block_stop", "index": s.index - 1})
	s.openType = ""
	s.openaiToolIx = -1
}

func (s *streamState) finalize() {
	if !s.started {
		// Upstream sent nothing usable.
		s.sse.ErrorEvent("api_error", "upstream produced no chunks")
		return
	}
	s.closeBlock()
	s.sse.Event("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": mapFinishReason(s.finishReason, s.sawToolCall), "stop_sequence": nil},
		"usage": map[string]int64{
			"input_tokens":            s.usage.InputTokens,
			"output_tokens":           s.usage.OutputTokens,
			"cache_read_input_tokens": s.usage.CacheReadInputTokens,
		},
	})
	s.sse.Event("message_stop", map[string]any{"type": "message_stop"})
}
