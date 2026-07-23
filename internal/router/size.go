package router

import (
	"math"
	"sort"

	"github.com/maorbril/agentic/internal/anthropic"
	"github.com/maorbril/agentic/internal/config"
	"github.com/maorbril/agentic/internal/tokens"
)

// defaultReservedOutput is the output headroom reserved when neither the
// request's max_tokens nor the model's MaxOutput constrains it. Sits in the
// router (not tokens) because it is a routing/dispatch policy, not an
// estimator property. Tunable: smaller = tighter fit (more remapping), larger
// = safer (more conservative).
const defaultReservedOutput = 8192

// reservedOutput returns the output headroom to reserve when testing whether
// a request fits a context budget. Prefers the request's own max_tokens
// (what the model will actually try to produce), caps it against the model's
// MaxOutput, and falls back to defaultReservedOutput when neither applies.
func reservedOutput(reqMaxTokens, modelMaxOutput int) int {
	r := reqMaxTokens
	if modelMaxOutput > 0 && (r == 0 || r > modelMaxOutput) {
		r = modelMaxOutput
	}
	if r <= 0 {
		return defaultReservedOutput
	}
	return r
}

// tierBudget resolves the context budget for a tier's model alias. Returns 0
// when the budget is unknown (the tier is then treated as having infinite
// capacity) or the alias fails to resolve (defensive — config validation
// should prevent that).
func tierBudget(cfg *config.Config, alias string) int {
	if cfg == nil {
		return 0
	}
	r, err := cfg.Resolve(alias)
	if err != nil {
		return 0
	}
	return r.Model.ContextBudget()
}

// fitDecision summarizes the size-aware filtering of a route rule's tiers.
type fitDecision struct {
	Eligible map[string]bool // tier name -> fits (budget unknown => true)
	Required int64           // estimated input + reserved output; 0 when no estimate
	EstInput int64           // the estimate, or 0 when no tier had a known budget
	Filtered []string        // tiers excluded because they don't fit (sorted)
}

// classifyTierFit computes which tiers can hold the request. When no tier has
// a known budget, it returns every tier eligible with Required==0 and computes
// no estimate — the backward-compat fast path (all pre-Phase-3 configs).
func classifyTierFit(cfg *config.Config, rule config.RouteRule, req *anthropic.MessagesRequest) fitDecision {
	elig := map[string]bool{}
	for t := range rule.Tiers {
		elig[t] = true
	}

	anyKnown := false
	for _, alias := range rule.Tiers {
		if tierBudget(cfg, alias) > 0 {
			anyKnown = true
			break
		}
	}
	if !anyKnown {
		return fitDecision{Eligible: elig}
	}

	est := tokens.Estimate(req)
	// Shared output cap: the largest MaxOutput among tiers, conservative (bias
	// toward over-reserving, which biases toward remapping up — safe).
	maxCap := 0
	for _, alias := range rule.Tiers {
		if m, ok := cfg.Models[alias]; ok && m.MaxOutput > maxCap {
			maxCap = m.MaxOutput
		}
	}
	reqMax := 0
	if req != nil {
		reqMax = req.MaxTokens
	}
	required := est + int64(reservedOutput(reqMax, maxCap))

	var filtered []string
	for tier, alias := range rule.Tiers {
		b := tierBudget(cfg, alias)
		if b == 0 || required <= int64(b) {
			elig[tier] = true
		} else {
			elig[tier] = false
			filtered = append(filtered, tier)
		}
	}
	sort.Strings(filtered)
	return fitDecision{Eligible: elig, Required: required, EstInput: est, Filtered: filtered}
}

// onlyEligibleTier returns the single eligible tier when exactly one remains,
// and ok=true. Used to skip the classifier call when size filtering has left
// only one viable tier.
func onlyEligibleTier(fit fitDecision) (tier string, ok bool) {
	if fit.Required == 0 {
		return "", false // filtering inactive
	}
	for t, ok2 := range fit.Eligible {
		if ok2 {
			if ok {
				return "", false // more than one eligible
			}
			tier, ok = t, true
		}
	}
	return tier, ok
}

// remapTier takes a classifier-chosen tier that was filtered out by fit and
// returns the smallest-budget tier that still fits. Tiers with an unknown
// budget (infinite) are preferred over overflow but not chosen as "smallest
// that fits" when a known-budget tier fits. If nothing fits, returns the
// largest-budget tier (best-effort) so the request still dispatches — the
// dispatch guard then returns a clean error if it truly can't fit. A tier
// that is already eligible is returned unchanged.
func remapTier(cfg *config.Config, rule config.RouteRule, fit fitDecision, chosen string) string {
	if fit.Eligible[chosen] {
		return chosen
	}
	type cand struct {
		tier   string
		budget int
	}
	var known []cand
	var unknown []string
	for tier := range fit.Eligible {
		if !fit.Eligible[tier] {
			continue
		}
		b := tierBudget(cfg, rule.Tiers[tier])
		if b > 0 {
			known = append(known, cand{tier, b})
		} else {
			unknown = append(unknown, tier)
		}
	}
	// Smallest known budget that fits.
	if len(known) > 0 {
		sort.Slice(known, func(i, j int) bool { return known[i].budget < known[j].budget })
		return known[0].tier
	}
	// Nothing known fits — prefer an unknown-budget (infinite) tier.
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return unknown[0]
	}
	// Nothing fits at all — largest known budget, best-effort.
	var all []cand
	for tier, alias := range rule.Tiers {
		if b := tierBudget(cfg, alias); b > 0 {
			all = append(all, cand{tier, b})
		}
	}
	if len(all) == 0 {
		return chosen // no known budgets anywhere; nothing to remap to
	}
	sort.Slice(all, func(i, j int) bool { return all[i].budget > all[j].budget })
	return all[0].tier
}

// promptTooLong reports whether the resolved model has a known context budget
// that the estimated input (plus reserved output headroom) exceeds. Returns
// false when the budget is unknown (no guard) or the request fits. required is
// the estimated input + reserved output, budget the model's context budget.
func promptTooLong(route config.Resolved, req *anthropic.MessagesRequest) (overflow bool, required int64, budget int) {
	budget = route.Model.ContextBudget()
	if budget <= 0 {
		return false, 0, 0
	}
	reqMax := 0
	if req != nil {
		reqMax = req.MaxTokens
	}
	required = tokens.Estimate(req) + int64(reservedOutput(reqMax, route.Model.MaxOutput))
	return required > int64(budget), required, budget
}

// budgetSortKey orders budgets ascending with unknown (0) treated as +Inf,
// so known budgets sort first and unknown last.
func budgetSortKey(b int) int {
	if b <= 0 {
		return math.MaxInt
	}
	return b
}
