// Package tokens estimates input token counts for backends without a
// count_tokens endpoint. Deliberately biased HIGH: Claude Code uses this
// for the auto-compact threshold, and compacting early is harmless while
// blowing the context window is fatal.
package tokens

import (
	"unicode/utf8"

	"github.com/maorbril/agentic/internal/anthropic"
)

const (
	charsPerToken      = 3.5
	perImageTokens     = 1500
	perMessageOverhead = 6
	safetyMargin       = 1.10
)

func Estimate(req *anthropic.MessagesRequest) int64 {
	chars := 0
	for _, b := range req.System {
		chars += utf8.RuneCountInString(b.Text)
	}
	images := 0
	for _, m := range req.Messages {
		for _, b := range m.Content {
			chars += utf8.RuneCountInString(b.Text)
			chars += utf8.RuneCountInString(b.Thinking)
			chars += len(b.Input)
			if b.Type == "image" {
				images++
			}
			for _, inner := range b.Content { // tool_result content
				chars += utf8.RuneCountInString(inner.Text)
				if inner.Type == "image" {
					images++
				}
			}
		}
	}
	for _, t := range req.Tools {
		chars += utf8.RuneCountInString(t.Name) + utf8.RuneCountInString(t.Description) + len(t.InputSchema)
	}
	tokens := float64(chars)/charsPerToken +
		float64(images*perImageTokens) +
		float64(len(req.Messages)*perMessageOverhead)
	return int64(tokens * safetyMargin)
}
