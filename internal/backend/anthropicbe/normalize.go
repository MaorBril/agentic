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
