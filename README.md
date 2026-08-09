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

## Compared to other options

There are three ways people solve "I want Claude Code but not locked to one provider":

| | agentic | claude-code-router / claude-code-proxy forks / LiteLLM | OpenCode, Crush, Goose, Aider |
|---|---|---|---|
| **Harness** | Real Claude Code, unmodified, auto-updating | Real Claude Code, unmodified | Different harness entirely — own prompts, tools, TUI |
| **Runs as** | Static Go binary, no daemon (leader election over a fixed port) | Daemon / server process you deploy and administer | Standalone CLI you run instead of Claude Code |
| **Cost & budgets** | First-class CLI: `agentic cost`, live statusline, hard-stop daily/weekly/monthly budgets | Usually a dashboard (LiteLLM) or not built in | Varies by tool, rarely budget-gated |
| **Model routing** | Aliases + a built-in LLM-classifier tier router (`auto`), sticky per turn | Rule-based routing configs; no classifier-based tiering | Manual model switch, no auto-routing |
| **Memory** | Composes with [clauder](https://github.com/MaorBril/clauder) — separate binary, optional | Not their concern | Varies |

The short version: agentic doesn't try to be a better harness than Claude Code — it keeps Claude Code exactly as Anthropic ships it and only swaps what's behind `ANTHROPIC_BASE_URL`. If you want a different agent loop altogether, OpenCode/Crush/Goose/Aider are the right layer to look at instead. If you want a gateway you deploy and administer for a team, LiteLLM is a more mature choice for that. agentic is for a single developer who wants `claude`, unmodified, with a budget and a cheap-model escape hatch, installed in one command and running with nothing to operate.

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

To update later, run `agentic update` (or `agentic update --check` to see if one's available without installing it). This updates agentic itself; Claude Code keeps auto-updating on its own regardless.

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

## Auto Goal

Whenever a `routing:` alias like `auto` is in play, a second, independent classifier pass looks at each new user turn and asks a different question: not "which tier?" but "does this look like it needs a persistent loop rather than a single reply?" — monitoring a long build, retrying until a condition holds, babysitting a deploy, polling for external state.

When the answer is yes, agentic doesn't (and can't) start a loop itself — the router only ever sees request and response bodies, it has no execution context inside the Claude Code process. Instead it appends a system-reminder to the request naming the harness's own mechanisms directly:

```
<system-reminder>
agentic: this task looks well suited to a recurring goal loop rather than a
single reply (polling a long build). If a persistent loop would help —
checking back on progress, retrying until a condition holds, babysitting a
long-running process — call ScheduleWakeup with prompt
"<<autonomous-loop-dynamic>>" and a reason, or invoke the /loop skill. This
is a suggestion, not a requirement: ignore it for tasks that finish in one
pass.
</system-reminder>
```

Claude Code decides whether to act on it — the reminder is a nudge, not a command. Decisions are logged (`grep autogoal ~/.agentic/router.log`) and, like tier decisions, surfaced in the statusline (`⟳ goal (polling a long build)`).

This rides on the same classifier alias as dynamic routing, so it's on wherever `routing: auto` is configured, at the cost of one extra cheap classifier call per new turn alongside the tier call.

## Context scaling

Claude Code sizes its auto-compact against the ~200K window of the Claude model it thinks it's talking to. Routed models rarely match that: a local qwen holds 32K, GPT holds 400K, and many models get unreliable well before their advertised limit. Declare what a model really holds and the router scales every token count it reports so the client's context gauge — and therefore auto-compact — tracks the *real* window:

```bash
agentic models add qwen --provider local --id qwen3-coder-30b --context-window 32768
agentic models add glm  --provider z --id glm-4.7 --context-window 200000 --effective-context 60000
```

`effective_context` is the attention knob: the client compacts at 60K real tokens even though the window is nominally 200K, keeping the model in its coherent range. Pricing and budgets always record true usage; `agentic context` shows a session's true-vs-reported trajectory for tuning these numbers. Details and research methodology: [docs/context-scaling.md](docs/context-scaling.md).

## Model evaluations

`agentic eval` compares a baseline model with a model under test on the same coding tasks. Each arm runs non-interactive Claude Code in isolation, records router usage and route decisions under its own session ID, and produces a patch. An optional judge sees blinded patches and verifier evidence; it never sees model names, cost, or execution order.

There are two executor types. A local manifest supplies its repository, setup command, and verifier directly:

```yaml
version: 1
name: local-sample
tasks:
  - id: django-11001
    repo: /path/to/prepared/django
    base: main
    prompt: Fix the issue described in this task. Run relevant tests.
    verifier:
      run: [python, -m, pytest, tests/example_test.py]
      timeout: 10m
```

A SWE-bench manifest delegates repository checkout, dependency setup, official task images, test-patch application, and FAIL_TO_PASS/PASS_TO_PASS grading to the pinned official harness:

```yaml
version: 1
name: swebench-smoke
dataset:
  type: swebench
  source: princeton-nlp/SWE-bench_Verified
  split: test
  tasks:
    - astropy__astropy-14309
sandbox:
  type: docker
```

The same manifest is in [`examples/swebench-smoke.yaml`](examples/swebench-smoke.yaml). SWE-bench runs require Docker and Python 3.10 or newer with the exact supported package version:

```bash
python3 -m venv ~/.agentic/swebench-venv
~/.agentic/swebench-venv/bin/pip install 'swebench==4.1.0'

agentic eval run examples/swebench-smoke.yaml \
  --python ~/.agentic/swebench-venv/bin/python \
  --baseline opus --mut auto --judge sonnet \
  --attempts 1 --timeout 45m --output ~/.agentic/evals/swebench-smoke
agentic eval report ~/.agentic/evals/swebench-smoke
```

A real one-task run comparing `kimi-k3` against `opus` produced:

```text
swebench-smoke: baseline=opus mut=kimi-k3 judge=sonnet pairs=1
wins: baseline 1 · mut 0 · ties 0 · judge errors 0 · infra pairs 0
verifier passes: baseline 1 · mut 1 — run failures: baseline 0 · mut 0 · infra failures 0
TASK                    ATTEMPT  WINNER    BASELINE       MUT
astropy__astropy-14309  1        baseline  complete/pass  complete/pass
```

Both patches passed the official SWE-bench grader. The blinded judge preferred the baseline because it exactly matched the upstream fix and was narrower, while `kimi-k3` used a broader but still correct defensive fix. This is one smoke-test data point, not a statistically meaningful model ranking; use multiple tasks and attempts for comparisons you intend to act on.

The adapter checks Python, the exact SWE-bench API, and Docker before making a model request. It asks the official harness to build or reuse each instance image, starts a fresh candidate container, installs the Linux Claude Code native binary there, extracts the patch, and submits it to the official grader in a separate clean container. SWE-bench 4.1.0 builds x86_64 images; Docker Desktop runs them under emulation on Apple Silicon. Initial image builds can take a while.

Docker candidates reach the normal loopback-only router through a temporary relay. The relay binds an ephemeral host port for the eval duration, accepts only `/v1/*`, and requires the existing per-install router token. It shuts down when the run ends. Use `--keep-containers` only while debugging because it leaves candidate containers behind.

Use `--task id` to select instances, `--seed` to reproduce launch order and judge blinding, `--resume` to skip completed pairs, and `--json` for machine-readable output. Infrastructure failures are unscored and never sent to the judge; `--resume` retries those pairs. `--judge none` decides only from verifier pass/fail, with equal outcomes recorded as ties.

Artifacts include raw Claude output, patches, container logs, official SWE-bench reports, FAIL_TO_PASS/PASS_TO_PASS details, blinded judge mappings, per-candidate usage and route traces, pair results, the resolved dataset metadata/fingerprint, and `summary.json`. Local setup and verifier commands execute on the host, so review third-party local manifests before running them.

## Budgets

Daily, weekly, and monthly caps — global and per profile. When a cap is hit, the router refuses the *next* request with a clear message that shows up right in the Claude Code TUI; in-flight responses are never cut. Warnings surface in the statusline (`agentic setup` registers it), which shows live session and daily spend:

```
main · sonnet · sess $0.84 · day $4.31/$25 [██░░░░]
```

`agentic cost` breaks spend down by model, profile, or session, and `--json` gives you the raw rows.

## The fine print

Two things you should understand before routing through agentic:

- **Billing.** Traffic through the router is billed to **API keys**, not your Claude Pro/Max subscription. OAuth credentials are never proxied. For subscription billing, use a `passthrough: true` profile — normal claude, no tracking.
- **Fidelity.** Non-Anthropic models work through translation, but Claude Code's prompts and tool patterns are tuned for Claude, so expect them to be clunkier in the main loop. They shine as cheap workhorses for background tasks and subagents. Specific gaps: no prompt caching on OpenAI-dialect backends (provider-side implicit caching still shows up as cache reads), thinking blocks are display-only, Anthropic server tools (web search, code execution) are unavailable on translated models, `top_k` is dropped, stop sequences truncate to four, and token counting for translated models is a deliberate ~15% overestimate so auto-compact fires early instead of overflowing context. Set `max_output` on models whose output cap is below what Claude Code requests (it asks for 32K), and `context_window` on models whose window differs from the ~200K Claude Code assumes (see [Context scaling](#context-scaling)).

## Subagents on any model

Claude Code's built-in Agent tool takes a fixed `model` parameter (`sonnet | opus | haiku | fable`), so a routed alias like `qwen` can't be picked through it — subagents are stuck on the Claude tiers even when your best tool for the job is something else. A subagent *definition's* `model:` frontmatter has no such limit, and behind agentic's local endpoint Claude Code passes that string straight through, so agentic generates one subagent per configured model alias:

```bash
agentic agents sync     # writes ~/.claude/agents/agentic-<alias>.md, one per model alias
agentic agents list     # what's implied by your config, and what's pending
```

Every model you've configured becomes selectable by name — `subagent_type: "agentic-qwen"`, `"agentic-grok"`, `"agentic-gpt-5-6-sol"` — and its traffic routes, prices, and budgets like any other agentic request. The set is derived from your own `models:` map, so it's whatever *you* configured; nothing is hardcoded.

When your aliases change, the next `agentic` launch offers to refresh them (once — decline and it stays quiet until the aliases change again; `AGENTIC_NO_AGENT_SYNC=1` opts out entirely). Only files prefixed `agentic-` are ever written or removed, so your own subagents are never touched.

## Finding another session

Claude Code sessions can message each other, but addressing one is awkward: session names are auto-derived from whatever that session is doing (`daily-case-runtime`), so the name you'd actually say is the project directory — and `ListAgents`, the only thing that can mint the `[ref]` a cross-session `SendMessage` needs, doesn't show directories.

`agentic peers` closes that gap by matching on both:

```
$ agentic peers labs-service-secondlife-be
Best match for "labs-service-secondlife-be":
  daily-case-runtime             busy  ~/code/secondlife/labs-service         started 12h ago

To message it: call ListAgents for its [ref], then SendMessage to
"daily-case-runtime [ref]" — the bare name works after first contact.
```

With no argument it lists every session you can reach. Sessions on a build older than Claude Code 2.1.224 register no socket and are unreachable until restarted — they're reported as a count rather than silently omitted. When a query matches several sessions equally well, it says so instead of picking one.

`agentic setup` writes this workflow into `~/.claude/CLAUDE.md`, between `<!-- agentic:peers:start -->` markers, so every session — agentic-launched or plain `claude` — knows to resolve names this way. Re-running setup refreshes that block and leaves the rest of the file alone.

## Works with clauder

agentic spawns `claude` directly and grants each session an auto-approved tool set for autonomous operation (`Read Write Edit Glob Grep Bash(*) WebFetch WebSearch mcp__clauder__*`). `--name` becomes claude's own session name.

Cross-instance messaging is native to Claude Code — sessions register under `~/.claude/sessions` and reach each other over a peer socket — so agentic no longer launches through `clauder wrap`. See [Finding another session](#finding-another-session) for addressing them. [clauder](https://github.com/MaorBril/clauder) remains a useful companion for **persistent memory**, which it provides over its own MCP server registration and therefore works no matter how the session was started. The two tools are independent; each works without the other.

`--no-clauder` is accepted but deprecated: every session is a bare claude now, so there is no wrap layer to opt out of.

## Commands

| Command | What it does |
|---|---|
| `agentic [-p profile] [--model alias] [-- args]` | launch Claude Code (args after `--` go to claude) |
| `agentic setup` | first-run config, token, statusline + peer-guidance registration |
| `agentic peers [name]` | find another Claude Code session to message |
| `agentic cost [--week\|--month] [--by model\|profile\|session]` | spend report |
| `agentic context [session-id]` | context-fullness trajectory (true vs reported tokens) |
| `agentic eval run/report` | paired model evaluation and artifact report |
| `agentic agents list/sync` | subagent definitions for your model aliases |
| `agentic models add/list/remove/test/update-prices` | model aliases |
| `agentic providers add/list/remove` | upstream providers |
| `agentic profiles list/show` · `agentic budget set` | profiles and caps |
| `agentic config get/set` | any config key |
| `agentic router run/status` | headless router / who's leader |
| `agentic doctor` | diagnose the installation |
| `agentic update [--check]` | update agentic itself to the latest release |

## Keys

Provider keys are referenced by environment variable name. They resolve in order: process environment → `~/.agentic/env` (a `KEY=value` file, mode 0600, created by `setup`). Put keys in `~/.agentic/env` — the router reads it directly, so sessions work no matter which shell launched them, and the config file never holds a secret.

## Security notes

The router binds `127.0.0.1` only and requires a per-install token (created by `setup`, mode 0600), so other local processes can't spend on your keys.

## License

MIT
