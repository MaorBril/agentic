package openaibe

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/maorbril/agentic/internal/anthropic"
	"github.com/maorbril/agentic/internal/config"
	"github.com/maorbril/agentic/internal/openai"
)

func route(maxTokensParam, reasoning string) config.Resolved {
	return config.Resolved{
		Alias:        "gpt",
		ProviderName: "openai",
		Provider:     config.Provider{Type: "openai", BaseURL: "https://api.openai.com/v1", MaxTokensParam: maxTokensParam},
		Model:        config.Model{ID: "gpt-5.2", Reasoning: reasoning},
	}
}

func parseReq(t *testing.T, body string) *anthropic.MessagesRequest {
	t.Helper()
	req, err := anthropic.ParseRequest([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestRequestTranslation(t *testing.T) {
	body := `{
	  "model": "gpt", "max_tokens": 4096, "stream": true,
	  "temperature": 0.7, "top_k": 40,
	  "stop_sequences": ["a","b","c","d","e"],
	  "system": [{"type":"text","text":"You are helpful.","cache_control":{"type":"ephemeral"}}],
	  "tools": [
	    {"name":"read_file","description":"Read a file","input_schema":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}},
	    {"type":"web_search_20260209","name":"web_search"}
	  ],
	  "tool_choice": {"type":"auto","disable_parallel_tool_use":true},
	  "messages": [
	    {"role":"user","content":"read main.go"},
	    {"role":"assistant","content":[
	      {"type":"thinking","thinking":"I should read it"},
	      {"type":"text","text":"Reading."},
	      {"type":"tool_use","id":"toolu_1","name":"read_file","input":{"path":"main.go"}}
	    ]},
	    {"role":"user","content":[
	      {"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"text","text":"package main"}]},
	      {"type":"text","text":"now explain"}
	    ]}
	  ]
	}`
	out, err := TranslateRequest(parseReq(t, body), route("", "none"))
	if err != nil {
		t.Fatal(err)
	}

	// system → leading system message
	if out.Messages[0].Role != "system" || out.Messages[0].Content != "You are helpful." {
		t.Errorf("system message: %+v", out.Messages[0])
	}
	// tool ordering: assistant tool_calls, then role:"tool" BEFORE trailing user text
	roles := []string{}
	for _, m := range out.Messages {
		roles = append(roles, m.Role)
	}
	want := []string{"system", "user", "assistant", "tool", "user"}
	if strings.Join(roles, ",") != strings.Join(want, ",") {
		t.Errorf("roles = %v, want %v", roles, want)
	}
	asst := out.Messages[2]
	if len(asst.ToolCalls) != 1 || asst.ToolCalls[0].ID != "toolu_1" || asst.ToolCalls[0].Function.Name != "read_file" {
		t.Errorf("assistant tool_calls: %+v", asst.ToolCalls)
	}
	if asst.Content != "Reading." {
		t.Errorf("thinking should be dropped, text kept: %v", asst.Content)
	}
	if out.Messages[3].ToolCallID != "toolu_1" || out.Messages[3].Content != "package main" {
		t.Errorf("tool message: %+v", out.Messages[3])
	}

	// server tool stripped, user tool kept with schema intact
	if len(out.Tools) != 1 || out.Tools[0].Function.Name != "read_file" {
		t.Fatalf("tools: %+v", out.Tools)
	}
	var schema map[string]any
	json.Unmarshal(out.Tools[0].Function.Parameters, &schema)
	if schema["type"] != "object" {
		t.Error("input_schema not passed through")
	}

	if out.ToolChoice != "auto" || out.ParallelToolCalls == nil || *out.ParallelToolCalls {
		t.Errorf("tool_choice: %v parallel: %v", out.ToolChoice, out.ParallelToolCalls)
	}
	if out.MaxTokens != 4096 || out.MaxCompletionTokens != 0 {
		t.Errorf("max_tokens mapping: %d/%d", out.MaxTokens, out.MaxCompletionTokens)
	}
	if out.Temperature == nil || *out.Temperature != 0.7 {
		t.Error("temperature not passed")
	}
	if len(out.Stop) != 4 {
		t.Errorf("stop truncation: %d", len(out.Stop))
	}
	if out.StreamOptions == nil || !out.StreamOptions.IncludeUsage {
		t.Error("stream_options.include_usage missing")
	}
}

func TestMidConversationSystemMessage(t *testing.T) {
	// Claude Code sends {"role":"system"} entries inside messages[].
	body := `{"model":"gpt","max_tokens":10,"messages":[
	  {"role":"user","content":"hi"},
	  {"role":"system","content":"Terse mode enabled."}
	]}`
	out, err := TranslateRequest(parseReq(t, body), route("", ""))
	if err != nil {
		t.Fatal(err)
	}
	last := out.Messages[len(out.Messages)-1]
	if last.Role != "system" || last.Content != "Terse mode enabled." {
		t.Errorf("mid-conversation system: %+v", last)
	}
}

func TestReasoningModes(t *testing.T) {
	body := `{"model":"gpt","max_tokens":100,"temperature":0.5,
	  "thinking":{"type":"enabled","budget_tokens":16000},
	  "messages":[{"role":"user","content":"hi"}]}`

	// effort: budget → reasoning_effort, sampling params dropped
	out, _ := TranslateRequest(parseReq(t, body), route("max_completion_tokens", "effort"))
	if out.ReasoningEffort != "high" {
		t.Errorf("reasoning_effort = %q", out.ReasoningEffort)
	}
	if out.Temperature != nil {
		t.Error("temperature must be dropped for reasoning models")
	}
	if out.MaxCompletionTokens != 100 || out.MaxTokens != 0 {
		t.Errorf("max_completion_tokens mapping: %d/%d", out.MaxTokens, out.MaxCompletionTokens)
	}

	// passive: thinking stripped, sampling kept
	out, _ = TranslateRequest(parseReq(t, body), route("", "passive"))
	if out.ReasoningEffort != "" {
		t.Error("passive must not set reasoning_effort")
	}
	if out.Temperature == nil {
		t.Error("passive keeps sampling params")
	}
}

func TestImageTranslation(t *testing.T) {
	body := `{"model":"gpt","max_tokens":10,"messages":[
	  {"role":"user","content":[
	    {"type":"text","text":"what is this"},
	    {"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBOR"}}
	  ]}]}`
	out, _ := TranslateRequest(parseReq(t, body), route("", ""))
	parts, ok := out.Messages[0].Content.([]openai.ContentPart)
	if !ok || len(parts) != 2 {
		t.Fatalf("content: %+v", out.Messages[0].Content)
	}
	if parts[1].ImageURL.URL != "data:image/png;base64,iVBOR" {
		t.Errorf("image url: %s", parts[1].ImageURL.URL)
	}
}

func TestResponseTranslation(t *testing.T) {
	resp := &openai.ChatResponse{
		ID: "chatcmpl-abc",
		Choices: []openai.Choice{{
			Message: openai.ResponseMessage{
				Role: "assistant", Content: "done",
				ReasoningContent: "let me think",
				ToolCalls: []openai.ToolCall{{
					ID: "call_1", Type: "function",
					Function: openai.FunctionCall{Name: "edit", Arguments: `{"path":"a.go"`}, // truncated JSON
				}},
			},
			FinishReason: "tool_calls",
		}},
		Usage: &openai.Usage{PromptTokens: 100, CompletionTokens: 20,
			PromptTokensDetails: &struct {
				CachedTokens int64 `json:"cached_tokens"`
			}{CachedTokens: 60}},
	}
	out, err := TranslateResponse(resp, "gpt")
	if err != nil {
		t.Fatal(err)
	}
	if out.Model != "gpt" || out.StopReason != "tool_use" {
		t.Errorf("model=%s stop=%s", out.Model, out.StopReason)
	}
	if out.Content[0].Type != "thinking" || out.Content[1].Type != "text" || out.Content[2].Type != "tool_use" {
		t.Fatalf("block order: %+v", out.Content)
	}
	// truncated arguments repaired
	var input map[string]any
	if err := json.Unmarshal(out.Content[2].Input, &input); err != nil || input["path"] != "a.go" {
		t.Errorf("repaired input: %s (%v)", out.Content[2].Input, err)
	}
	// Anthropic invariant: input_tokens excludes cache reads
	if out.Usage.InputTokens != 40 || out.Usage.CacheReadInputTokens != 60 {
		t.Errorf("usage: %+v", out.Usage)
	}
}

func TestRepairJSON(t *testing.T) {
	cases := map[string]bool{
		`{"a":1}`:            true,
		``:                   true, // empty → {}
		`{"a":"unclosed`:     true, // close string + brace
		`{"a":[1,2`:          true,
		`not json at all {{`: false,
	}
	for in, ok := range cases {
		_, err := repairJSON(in)
		if (err == nil) != ok {
			t.Errorf("repairJSON(%q): err=%v, want ok=%v", in, err, ok)
		}
	}
}
