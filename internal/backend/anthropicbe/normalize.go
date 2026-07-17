package anthropicbe

import (
	"bytes"
	"encoding/json"
	"strings"
)

// rewriteForModel swaps the model field and normalizes capability-gated
// parameters for the target model. Needed because Claude Code picks its
// thinking/sampling config by pattern-matching the model NAME — an alias
// like "auto" or "cheap" matches nothing, so it sends legacy parameters
// that current Claude models reject.
func rewriteForModel(raw []byte, model string) ([]byte, error) {
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	m["model"] = model
	normalizeForModel(m, model)
	return json.Marshal(m)
}

func normalizeForModel(m map[string]any, model string) {
	switch {
	case strings.HasPrefix(model, "claude-fable") || strings.HasPrefix(model, "claude-mythos"):
		// Thinking is always on; any explicit config (enabled, disabled,
		// budget_tokens) returns a 400. Sampling params are removed.
		delete(m, "thinking")
		deleteSampling(m)

	case strings.HasPrefix(model, "claude-opus-4-7"),
		strings.HasPrefix(model, "claude-opus-4-8"),
		strings.HasPrefix(model, "claude-sonnet-5"):
		// budget_tokens is removed on these models; adaptive is the only
		// on-mode. Non-default sampling params are rejected.
		if thinkingType(m) == "enabled" {
			m["thinking"] = map[string]any{"type": "adaptive"}
		}
		deleteSampling(m)

	case strings.HasPrefix(model, "claude-opus-4-6"),
		strings.HasPrefix(model, "claude-sonnet-4-6"):
		// budget_tokens is deprecated here; adaptive is supported and
		// preferred. Sampling params are still allowed — leave them.
		if thinkingType(m) == "enabled" {
			m["thinking"] = map[string]any{"type": "adaptive"}
		}
	}
	// Older models (haiku-4-5, sonnet-4-5, …) keep enabled+budget_tokens.

	if !supportsEffort(model) {
		if oc, ok := m["output_config"].(map[string]any); ok {
			delete(oc, "effort")
			if len(oc) == 0 {
				delete(m, "output_config")
			}
		}
	}
	if !strings.HasPrefix(model, "claude-opus-4-8") {
		// Mid-conversation system messages are Opus 4.8-only; fold them
		// into the preceding user turn so tier switches don't 400.
		foldSystemMessages(m)
	}
}

// supportsEffort reports whether the model accepts output_config.effort
// (Opus 4.5+, Sonnet 4.6+, Fable/Mythos; Haiku and older Sonnets reject it).
func supportsEffort(model string) bool {
	for _, prefix := range []string{
		"claude-fable", "claude-mythos",
		"claude-opus-4-5", "claude-opus-4-6", "claude-opus-4-7", "claude-opus-4-8",
		"claude-sonnet-4-6", "claude-sonnet-5",
	} {
		if strings.HasPrefix(model, prefix) {
			return true
		}
	}
	return false
}

// foldSystemMessages merges {"role":"system"} entries inside messages[]
// into the nearest preceding user message as a <system-reminder> text
// block, preserving role alternation.
func foldSystemMessages(m map[string]any) {
	msgs, ok := m["messages"].([]any)
	if !ok {
		return
	}
	var out []any
	for _, raw := range msgs {
		msg, ok := raw.(map[string]any)
		if !ok || msg["role"] != "system" {
			out = append(out, raw)
			continue
		}
		text := contentText(msg["content"])
		if text == "" {
			continue
		}
		reminder := map[string]any{"type": "text",
			"text": "<system-reminder>\n" + text + "\n</system-reminder>"}
		// Find the nearest preceding user message to absorb the block.
		folded := false
		for i := len(out) - 1; i >= 0; i-- {
			prev, ok := out[i].(map[string]any)
			if !ok || prev["role"] != "user" {
				continue
			}
			prev["content"] = append(contentBlocks(prev["content"]), reminder)
			folded = true
			break
		}
		if !folded {
			out = append(out, map[string]any{"role": "user", "content": []any{reminder}})
		}
	}
	m["messages"] = out
}

// contentText flattens a message content (string or block list) to text.
func contentText(content any) string {
	if s, ok := content.(string); ok {
		return s
	}
	text := ""
	for _, b := range contentBlocks(content) {
		if block, ok := b.(map[string]any); ok && block["type"] == "text" {
			if t, ok := block["text"].(string); ok {
				if text != "" {
					text += "\n"
				}
				text += t
			}
		}
	}
	return text
}

// contentBlocks returns content as a block list, converting the string form.
func contentBlocks(content any) []any {
	if blocks, ok := content.([]any); ok {
		return blocks
	}
	if s, ok := content.(string); ok && s != "" {
		return []any{map[string]any{"type": "text", "text": s}}
	}
	return nil
}

func thinkingType(m map[string]any) string {
	t, _ := m["thinking"].(map[string]any)
	s, _ := t["type"].(string)
	return s
}

func deleteSampling(m map[string]any) {
	delete(m, "temperature")
	delete(m, "top_p")
	delete(m, "top_k")
}
