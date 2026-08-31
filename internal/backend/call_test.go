package backend

import (
	"net/http"
	"testing"

	"github.com/maorbril/agentic/internal/config"
)

func TestPromptCacheKeyRequiresOptIn(t *testing.T) {
	hdr := http.Header{"X-Agentic-Session": []string{"sess-1"}}
	off := &Call{Header: hdr, Route: config.Resolved{Provider: config.Provider{Type: config.ProviderOpenAI}}}
	if got := off.PromptCacheKey(); got != "" {
		t.Errorf("key sent without provider opt-in: %q", got)
	}
	on := &Call{Header: hdr, Route: config.Resolved{
		Provider: config.Provider{Type: config.ProviderOpenAI, PromptCacheKey: true}}}
	if got := on.PromptCacheKey(); got != "sess-1" {
		t.Errorf("PromptCacheKey() = %q, want sess-1", got)
	}
	// An unattributed request has no session to key on.
	anon := &Call{Header: http.Header{}, Route: on.Route}
	if got := anon.PromptCacheKey(); got != "" {
		t.Errorf("unattributed request got a key: %q", got)
	}
}

func TestScaleBudgetPrefersGauge(t *testing.T) {
	c := &Call{Route: config.Resolved{Model: config.Model{ContextWindow: 32_768}}}
	if got := c.ScaleBudget(); got != 32_768 {
		t.Errorf("ScaleBudget() = %d, want the model's own budget", got)
	}
	c.GaugeBudget = 1_000_000
	if got := c.ScaleBudget(); got != 1_000_000 {
		t.Errorf("ScaleBudget() = %d, want the rule's anchor", got)
	}
}
