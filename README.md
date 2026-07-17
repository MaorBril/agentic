# agentic

**Run Claude Code on any model, with a budget.**

agentic wraps Claude Code in a thin local router. Your sessions look and feel exactly like `claude` — same TUI, same tools, same updates — but the model behind them can be Anthropic, OpenAI, xAI, or anything OpenAI-compatible (Ollama, vLLM, OpenRouter, DeepSeek, Groq). Every token is metered, priced, and checked against budgets you set.

```bash
agentic                  # Claude Code, tracked, on your default profile
agentic -p cheap         # same session, cheaper models
agentic --model grok     # one-off model override
agentic cost             # where did today's $4.31 go?
```

## Why

Claude Code is a great harness, and it keeps getting better — forking it means losing that. But it only talks to one provider, and it doesn't answer two questions you eventually ask: *how much did that session cost?* and *can I run the cheap parts on a cheap model?*

agentic answers both without touching Claude Code itself. Claude Code officially supports pointing at a gateway via `ANTHROPIC_BASE_URL`; agentic is that gateway, plus the CLI around it.

## How it works

```
agentic (launcher) ──▶ claude (unmodified, auto-updating)
                          │  ANTHROPIC_BASE_URL
                          ▼
                   local router (127.0.0.1)
                   ├─ anthropic: byte-faithful passthrough
                   ├─ openai dialect: full request/stream translation
                   ├─ usage log (SQLite) + pricing
                   └─ budget gate
                          ▼
        Anthropic · OpenAI · xAI · Ollama · vLLM · OpenRouter · ...
```

There is no daemon. The first `agentic` session binds the router port and serves everyone; when it exits, another running session takes over within a couple of seconds. The last session out turns off the lights.

Model names are aliases you define. Claude Code treats model IDs as opaque strings, so `ANTHROPIC_MODEL=grok` flows straight through and the router resolves it. Anything starting with `claude-` passes through to Anthropic untouched — background tasks keep working even when your main model is something else entirely.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/maorbril/agentic/main/install.sh | sh
agentic setup
```

Or from source: `go install github.com/maorbril/agentic@latest`

## Configure

Everything lives in `~/.agentic/config.yaml`, and everything is editable from the terminal:

```bash
agentic providers add openai --type openai --base-url https://api.openai.com/v1 \
    --key-env OPENAI_API_KEY --max-tokens-param max_completion_tokens
agentic models add gpt --provider openai --id gpt-5.2 --reasoning effort --max-output 16384
agentic models test gpt          # 1-token probe: did I configure it right?
agentic budget set --daily 25
```

Edits apply to live sessions immediately — the CLI hot-reloads the running router.

A profile bundles a main model, a small/fast model for background tasks, tier mappings (so `/model opus` resolves inside the profile), and optional budgets:

```yaml
profiles:
  main:  {model: sonnet, small_fast: haiku, tiers: {opus: opus, sonnet: sonnet, haiku: haiku}}
  cheap: {model: gpt, small_fast: gpt, budget: {daily: 5.00}}
  local: {model: qwen, small_fast: qwen}
  subscription: {passthrough: true}   # plain claude, subscription billing, no tracking
```

## Dynamic routing

Instead of picking models by hand, let a cheap LLM triage every task:

```bash
agentic routing set auto --classifier haiku \
    --deep opus --standard sonnet --light qwen
```

`auto` now behaves like a model (`/model auto`, or `profiles: {model: auto}`). On each new user turn, the classifier reads the request and assigns a tier — planning and hard debugging go `deep`, ordinary coding goes `standard`, mechanical edits and verification go `light`. The decision sticks for the rest of the turn (tool results don't re-trigger it), so a task never flips models mid-flight. Classification failures fall back to `--default` (standard), and every decision is logged:

```
$ grep autoroute ~/.agentic/router.log
... alias=auto tier=deep model=opus
... alias=auto tier=light model=qwen
```

`agentic cost --by model` then shows how spend actually distributed across tiers. Each classification costs one tiny request to the classifier model (~$0.0005 with haiku).

## Budgets

Daily, weekly, and monthly caps — global and per profile. When a cap is hit, the router refuses the *next* request with a clear message that shows up right in the Claude Code TUI; in-flight responses are never cut. Warnings surface in the statusline (`agentic setup` registers it), which shows live session and daily spend:

```
main · sonnet · sess $0.84 · day $4.31/$25 [██░░░░]
```

`agentic cost` breaks spend down by model, profile, or session, and `--json` gives you the raw rows.

## The fine print

Two things you should understand before routing through agentic:

- **Billing.** Traffic through the router is billed to **API keys**, not your Claude Pro/Max subscription. OAuth credentials are never proxied. For subscription billing, use a `passthrough: true` profile — normal claude, no tracking.
- **Fidelity.** Non-Anthropic models work through translation, but Claude Code's prompts and tool patterns are tuned for Claude, so expect them to be clunkier in the main loop. They shine as cheap workhorses for background tasks and subagents. Specific gaps: no prompt caching on OpenAI-dialect backends (provider-side implicit caching still shows up as cache reads), thinking blocks are display-only, Anthropic server tools (web search, code execution) are unavailable on translated models, `top_k` is dropped, stop sequences truncate to four, and token counting for translated models is a deliberate ~15% overestimate so auto-compact fires early instead of overflowing context. Set `max_output` on models whose output cap is below what Claude Code requests (it asks for 32K).

## Works with clauder

If [clauder](https://github.com/MaorBril/clauder) is installed, agentic launches sessions through `clauder wrap`, so your instances get persistent memory and can message each other. The two tools are independent; each works without the other.

## Commands

| Command | What it does |
|---|---|
| `agentic [-p profile] [--model alias] [-- args]` | launch Claude Code (args after `--` go to claude) |
| `agentic setup` | first-run config, token, statusline registration |
| `agentic cost [--week\|--month] [--by model\|profile\|session]` | spend report |
| `agentic models add/list/remove/test/update-prices` | model aliases |
| `agentic providers add/list/remove` | upstream providers |
| `agentic profiles list/show` · `agentic budget set` | profiles and caps |
| `agentic config get/set` | any config key |
| `agentic router run/status` | headless router / who's leader |
| `agentic doctor` | diagnose the installation |

## Keys

Provider keys are referenced by environment variable name. They resolve in order: process environment → `~/.agentic/env` (a `KEY=value` file, mode 0600, created by `setup`). Put keys in `~/.agentic/env` — the router reads it directly, so sessions work no matter which shell launched them, and the config file never holds a secret.

## Security notes

The router binds `127.0.0.1` only and requires a per-install token (created by `setup`, mode 0600), so other local processes can't spend on your keys.

## License

MIT
