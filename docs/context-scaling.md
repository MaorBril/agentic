# Virtual context scaling

Different models have different context windows — and different *usable*
windows, since attention quality often degrades well before the advertised
limit. Claude Code manages its own memory (auto-compact, tool-result
trimming) but sizes everything against the model id it was launched with: a
`claude-*` name means a ~200K window. It cannot be told the real window of
whatever the router actually dispatched to.

So the router lies to it — proportionally.

## How it works

Every model can declare its real context size in `~/.agentic/config.yaml`:

```yaml
models:
  qwen:  {provider: local,  id: qwen3-coder-30b, context_window: 32768}
  gpt:   {provider: openai, id: gpt-5.2,         context_window: 400000}
  glm:   {provider: z,      id: glm-4.7,         context_window: 200000, effective_context: 60000}
```

The model's **budget** is `min(context_window, effective_context)`. Every
token count the router reports to the client — the `count_tokens` estimate
and the input-side `usage` fields on responses — is multiplied by

```
factor = 200_000 / budget
```

A 32K model that is half full reports ~100K tokens; Claude Code's gauge
reads 50% and its auto-compact fires at the same *relative* fullness it
would on a real Claude model — which is exactly when the 32K window is
nearly exhausted. The same math runs in reverse for big windows: a 400K
model reports half its true count, so Claude Code doesn't throw away
context at 200K it didn't need to.

`effective_context` is the attention knob. A model with a nominal 200K
window that gets unreliable past 60K should be configured with
`effective_context: 60000` — the client then compacts at 60K real tokens
and the model always operates in its coherent range.

Rules of the lie:

- **Only the client is lied to.** Budgets, pricing, the SQLite usage log,
  and the router log all record true upstream usage. The scaled number is
  stored *alongside* it (`reported_input`, `ctx_budget` columns) so the
  gap is auditable.
- **Only input-side counts scale** (`input_tokens`, cache read/write).
  Output tokens stay true — they roll into the next request's input count
  anyway.
- **Rounding is always up**, preserving the deliberate bias-high property
  of the token estimator: compacting early is harmless, blowing the real
  window is fatal.
- **Unset means untouched.** Models without `context_window` /
  `effective_context` get factor 1. The Anthropic passthrough backend is
  byte-faithful and never scales (a Claude model's real window matches the
  client's assumption; `effective_context` on an anthropic-provider model
  currently has no effect — set it on translated models only).

## Interaction with dynamic routing

With `model: auto`, different turns land on models with different budgets.
The gauge is always relative to the *current* model, so it can jump when
the tier changes — a conversation at 30% of opus's budget may be 90% of a
local model's. That jump is correct (it reflects real headroom) but it
means a light-tier turn can trigger a compact a deep-tier turn wouldn't.
Deterministic size-aware tier filtering (don't route a conversation to a
model it doesn't fit) is Phase 3, not yet implemented.

## Evaluating it

Three layers, from cheap to real:

1. **Invariant tests** (`internal/tokens/scale_test.go`): proportionality
   and round-up bias across budgets from 8K to 1M.
2. **Simulation eval** (`internal/router/ctxscale_eval_test.go`): plays a
   growing Claude-Code-shaped conversation against the real router with a
   fake upstream, for each budget tier, and asserts (a) the reported
   fraction of the assumed window always equals the true fraction of the
   real budget, and (b) the simulated auto-compact trigger (92% of gauge)
   lands past 92% but within one turn-increment of the real budget —
   never after it. Run with:

   ```bash
   go test ./internal/router/ -run TestContextScalingEval -v
   ```

3. **Live sessions** (`agentic context [session-id]`): per-request
   trajectory of true tokens vs reported tokens vs budget, with compaction
   points marked. This is the research surface.

## Researching `effective_context` for a model

The nominal window is in the model card; the *effective* window is an
empirical property you measure. Suggested loop:

1. Start with `context_window` only (nominal). Run real sessions.
2. Watch `agentic context` and the router log's `ctx_pct` field. Correlate
   quality failures — wrong edits, forgotten instructions, tool-call
   flailing, upstream `4xx` at high fullness — with the fullness percentage
   at which they happened. The `err_type` column in the trajectory makes
   hard failures visible; soft degradation you judge from the session.
3. Set `effective_context` a comfortable margin below the fullness where
   degradation starts, and re-run. The client now compacts before the
   model enters its mushy zone.
4. For a sharper measurement, run a needle-in-a-haystack probe: seed a fact
   early in a session, pad the context to N% fullness with real work, then
   ask for the fact. The fullness where recall breaks is the effective
   window. (Published long-context benchmarks — RULER, NIAH variants — give
   a starting point per model family, but local quantized builds often
   underperform their upstream numbers, so verify locally.)

Queries against `~/.agentic/agentic.db` for aggregate research:

```sql
-- error rate by context fullness decile, per model
SELECT model,
       CAST(10.0 * (input_tokens+cache_read_tokens+cache_write_tokens) / ctx_budget AS INT) AS decile,
       COUNT(*) AS requests,
       SUM(CASE WHEN err_type != '' THEN 1 ELSE 0 END) AS errors
FROM usage_events
WHERE ctx_budget > 0
GROUP BY model, decile ORDER BY model, decile;
```

## Known limitations

- The gauge jump on tier switches (above).
- `count_tokens` for translated models is an estimate (~3.5 chars/token,
  +10% margin), not a tokenizer. Scaling preserves the bias but also
  scales the estimation error; on very small budgets (≤8K) the margin can
  cost a few hundred usable tokens.
- Anthropic passthrough models are never scaled, so `effective_context`
  can't yet force early compaction on a real Claude model.
- Claude Code's compact threshold (~92%) is its own moving target; the
  scaling is threshold-agnostic (pure proportionality), so threshold
  changes upstream don't break correctness, only the eval's simulated
  trigger constant.
