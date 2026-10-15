# Yagent Configuration Guide

> **Scope**: this is the user-facing reference for `config.toml` — every section, the provider
> resolution chain, failover, and scenario examples. Related deep dives:
> - [Anthropic API configuration](./anthropic-config.md) — Claude access and Extended Thinking
> - [LLM failover configuration](./llm-fallback-config.md) — multi-provider fallback strategies
> - [Context compression trigger design](./compression-trigger-design.md) — how the threshold is
>   derived and measured
> - [Pause / resume design](./pause-resume-design.md) — what the `[resume]` budget controls
>
> Every key listed here was checked against the current code (2026-10-15). Keys the code parses
> but never reads are called out in place and collected in [§7](#7-keys-that-are-parsed-but-do-nothing).

---

## 1. Configuration file basics

### 1.1 Which file is loaded — one file, no layering

Yagent reads **exactly one** TOML file. There is no merge between an "user layer", a "project
layer" and a "built-in layer" (older versions of this document claimed there was). `getConfigPath()`
in `main.go` picks the first applicable entry:

| Order | Path | Behaviour when missing |
|:-----:|------|------------------------|
| 1 | `--config` / `-c <path>` | Used verbatim; **never created**, never defaulted. A bad path is a startup error. |
| 2 | `$HOME/.yagent/config/config.toml` | Generated from the embedded template (this is the normal path) |
| 3 | `./config/config.toml` | Reached only if the home directory lookup itself fails |

The repo's `config/config.toml` is therefore a **reference example**, not an active config layer. The
file that actually runs is the one in `~/.yagent/config/` — copy or link it there.

Because a single file is loaded, a key you change in `config/config.toml` while running from
`~/.yagent/config/config.toml` has no effect at all.

### 1.2 First-run generation

When the chosen path does not exist, `config.EnsureConfigExists()` materialises the embedded
template `internal/config/default_config.toml` with an atomic write (`.tmp` then rename), so an
interrupted write cannot leave a half-written config. An existing file is **never** overwritten.

### 1.3 What changes at runtime (and gets written back)

There is no configuration hot-reload: the file is parsed once at startup. (The `fsnotify` module is
still listed in `go.mod` but no Go code imports it any more.) Three things can be changed while
Yagent runs, and each writes its key back into the loaded file with a text-level edit that preserves
comments and ordering (`internal/config/persist.go`):

| TUI control | Key written | Effect |
|-------------|-------------|--------|
| `/model` (also Alt+M) | `[global.llm].use_provider`, `[agents.llm.<agent>]`, `[tools.llm.<tool>]` | Immediate engine re-resolution; `deepthinking` is hot-swapped too |
| `/timeout` | `[llm].timeout` | New global per-call timeout, takes effect from the next call |
| `/route` | `[routing].mode` | Switches auto-routing between `auto` and `fixed` |

Anything else needs a restart.

---

## 2. Quick start

### 2.1 Minimal working configuration

One provider plus the selection of it is enough:

```toml
[global.llm]
use_provider = "deepseek_v4_pro"

[global.llm.providers.deepseek_v4_pro]
model = "deepseek-v4-pro"
max_tokens = 3000
api_base_url = "https://api.deepseek.com/v1"
api_key = "sk-your-key"
```

Validation only requires `use_provider`, a provider with a `model`, and non-empty `api_key` +
`api_base_url` — it never tests whether the key works.

### 2.2 Switching the default provider

```toml
[global.llm]
use_provider = "local"   # move everything to the local model
```

---

## 3. Section index

| Section | Purpose | Required |
|---------|---------|:--------:|
| `[global.llm]` + `[global.llm.providers.*]` | Define LLM providers (model, endpoint, key) | **Yes** (at least one) |
| `[agents.llm.*]` | Per-agent provider override | No |
| `[tools.llm.*]` | Per-tool provider override (highest precedence) | No |
| `[tools.deferred]` | Deferred tool-schema behaviour (catalog + `load_tool`) | No |
| `[agent]` | Agent behaviour: step caps, YOLO, delegation watchdogs | No |
| `[llm]` | Per-call timeout, retries, breaker, failover | No |
| `[routing]` | Benchmark-scored automatic model routing | No |
| `[codeseek]` + `[codeseek.knowledge]` | CodeSeek MCP engine (and the injection block, inert today) | No |
| `[browser]` | Browser-Agent driving the external `browser-control` CLI | No |
| `[blackboard]` | Cross-agent note board budgets | No |
| `[resume]` | Stop / resume checkpoint and replay budgets | No |
| `[enhanced_commander]` | Context compression and commander options | No |
| `[git_checkpoint]` | Git checkpoint / branch lifecycle | No |
| `[keywords]` | Keyword completion (only one live key) | No |
| `[app]` | Legacy application flags (no live keys) | No |
| `[tui]` | TUI keybindings | No |
| `task_timeout` | Global task timeout (root field, http mode only) | No |

---

## 4. LLM configuration

### 4.1 Provider fields

Every provider lives in `[global.llm.providers.<name>]`. A provider is just a base URL plus a key,
so any OpenAI-compatible endpoint works (DashScope, SiliconFlow, DeepSeek, Moonshot, Mistral, Z.ai,
OpenRouter, vLLM, Ollama's OpenAI surface ...).

| Field | Type | Default | Notes |
|-------|------|---------|-------|
| `model` | string | — | Required (e.g. `qwen3-coder-plus`, `deepseek-v4-pro`) |
| `api_base_url` | string | — | Required, except for the Bedrock name-prefix exemption below |
| `api_key` | string | — | Required, except for the Bedrock name-prefix exemption below |
| `temperature` | float | not sent | **`0` is treated as "not set"**: the field is a plain `float64`, so "wrote `0.0`" and "left it out" are indistinguishable, and `0` means the request omits `temperature`. Forcing greedy decoding via config is not currently possible. |
| `max_tokens` | int | not sent | Engine-level default. Priority: per-call override > this value > field omitted. The Anthropic API requires it, so an unset value falls back to 8192. |
| `context_window` | int | `0` | **Affects only the TUI context progress bar.** It is not used for the compression threshold (§5.9). `0`/unset = no bar. |
| `reasoning_effort` | string | `""` | openai side: passed through verbatim, plus `thinking={type:enabled}`. anthropic side: mapped to `thinking.budget_tokens` — `low`/`medium`/`high`/`max` = 1024/8192/24576/32000, `max_tokens` is raised to budget+4096 and `temperature` forced to 1.0. An unrecognised value means "thinking off"; `budget_tokens: 0` is never sent. |
| `api_format` | string | `"openai"` | `"openai"` or `"anthropic"` (native Messages API) |
| `aws_region` | string | `""` | Only consumed as the Bedrock exemption's own requirement (see below) |
| `aws_profile` | string | `""` | Parsed, no consumer |
| `model_provider` | string | `""` | Parsed, no consumer |
| `fallback_providers` | array | `[]` | Failover list; needs `[llm] enable_fallback = true` |

**Bedrock caveat**: the required-key exemption is decided by the *name* of the effective provider —
`strings.HasPrefix(effectiveProvider, "bedrock")` — not by `api_format`. A real Bedrock setup whose
provider is named something else still gets `api_key`/`api_base_url` demanded, and a provider merely
*named* `bedrock*` skips them. There is no Bedrock-specific client; it still speaks the
OpenAI-compatible protocol.

### 4.2 Resolution chain

Highest precedence first:

```
tools.llm.<tool>.use_provider       ← per-tool override
    ↓
tools.llm.use_provider              ← tool default
    ↓
agents.llm.<agent>.use_provider     ← per-agent override
    ↓
agents.llm.use_provider             ← agent default
    ↓
global.llm.use_provider             ← global default
```

`ResolveProvider(agentName, toolName)` walks that chain; if nothing matches, startup fails with
`no LLM provider configured`.

### 4.3 Agent-level overrides

| Agent | Section | Live? |
|-------|---------|:-----:|
| Director | `[agents.llm.director]` | Yes |
| Coding | `[agents.llm.coding]` | Yes |
| Repo | `[agents.llm.repo]` | Yes |
| Chat | `[agents.llm.chat]` | Yes |
| DevOps | `[agents.llm.devops]` | Yes |
| Meta | `[agents.llm.meta]` | Yes |
| **Browser** | `[agents.llm.browser]` | **No — see below** |

The switch in `config.getAgentOverride()` has exactly six cases. `[agents.llm.browser]` is reported
by the TOML decoder as an undecoded key and silently ignored, so the Browser Agent can only be moved
by `[agents.llm].use_provider` or the global default. To give the browser its own provider today,
the only lever is automatic routing's per-role baseline (§5.4) or a runtime `/model` override.

```toml
[agents.llm.coding]
use_provider = "deepseek_v4_pro"
```

### 4.4 Tool-level overrides

| Tool | Section |
|------|---------|
| thinking | `[tools.llm.thinking]` |
| deepthinking | `[tools.llm.deepthinking]` |
| design | `[tools.llm.design]` |
| micro_agent | `[tools.llm.micro_agent]` |

```toml
[tools.llm.deepthinking]
use_provider = "deepseek_v4_pro"
```

### 4.5 Failover (`fallback_providers`)

Declare alternates on a provider; when the primary call fails (429 / 5xx / timeout) the fallback
engine tries them in order. Requires `[llm] enable_fallback = true`. Names that are not defined in
`[global.llm.providers]` are dropped. See
[LLM failover configuration](./llm-fallback-config.md).

Shorthand (declaration order wins):

```toml
fallback_providers = ["deepseek_v4_pro", "local"]
```

Full form (higher weight first; entries without weight keep declaration order):

```toml
fallback_providers = [
    { provider = "deepseek_v4_pro", weight = 10 },
    { provider = "local",           weight = 5  },
]
```

### 4.6 `api_format = "anthropic"` with `reasoning_effort`

`api_format = "anthropic"` switches to the native Messages API, which is what unlocks Claude Extended
Thinking. There `reasoning_effort` is not forwarded verbatim: it selects a `thinking.budget_tokens`
value (1024 / 8192 / 24576 / 32000), the request's `max_tokens` is raised to `budget + 4096`, and
`temperature` is forced to `1.0` because the provider rejects other values while thinking is on.
Details in [Anthropic API configuration](./anthropic-config.md).

---

## 5. Section reference

### 5.1 `[agent]` — agent behaviour

| Field | Type | Default | Notes |
|-------|------|---------|-------|
| `yolo_mode` | bool | `false` | Auto-approves dangerous operations **inside the workspace** (same as `-y`) |
| `full_yolo_mode` | bool | `false` | Full autonomy: implies yolo, allows paths outside the workspace, and removes `ask_user_for_help` (same as `-Y`) |
| `director_max_steps` | int | `100` | Step cap for the Director loop |
| `coding_max_steps` | int | `150` | Step cap for the Coding Agent |
| `repo_max_steps` | int | `50` | Step cap for the Repo Agent |
| `chat_max_steps` | int | `50` | Step cap for the Chat Agent |
| `devops_max_steps` | int | `50` | Step cap for the DevOps Agent |
| `browser_max_steps` | int | `200` | Step cap for the Browser Agent |
| `meta_max_steps` | int | `50` | Parsed and normalised, **no consumer** — Meta emits one LLM call with no loop (§7) |
| `meta_retry_count` | int | `5` | Retries when Meta emits invalid JSON |
| `delegate_idle_timeout` | duration | derived | See below |
| `delegate_total_timeout` | duration | `0` (= off) | Hard cap per delegation |
| `max_parallel_readonly_tools` | int | `4` | Parallelism of a read-only tool run: `≤0` → 4, `>8` clamps to 8, **`1` = strictly serial (kill switch)** |
| `lang` | string | `""` | Conversation language. Empty injects no instruction; e.g. `"Chinese"`, `"English"` |

Step caps: `0` or an omitted key falls back to the code defaults listed above — the same numbers.
`meta_max_steps` is the exception: it is normalised but never read, because Meta produces a single
LLM call rather than running a tool loop.

Delegation watchdogs (`activity.WithIdleTimeout`): when `delegate_idle_timeout` is unset, the idle
threshold is **derived** as `max(10m, [llm] timeout + 5m)`; it must stay well above the longest
heartbeat-free step (one LLM call) or healthy long reasoning gets killed. Writing a value `≤ [llm]
timeout` warns at startup; negative values normalise to 0, i.e. back to the derived value.
`delegate_total_timeout` measures from delegation start; `0` disables it. If it is smaller than the
idle threshold, startup warns because the total cap fires first.

> **YOLO warning**: `yolo_mode = true` skips every confirmation inside the workspace, and
> `full_yolo_mode = true` additionally opens the filesystem outside it and removes the agent's last
> escape hatch (`ask_user_for_help`). Use in controlled environments only.

CLI equivalents: `--yolo` / `-y`, `--full-yolo` / `-Y`.

### 5.2 `[llm]` — inference resilience

| Field | Type | Default | Notes |
|-------|------|---------|-------|
| `timeout` | duration | `10m` | Per-call LLM timeout. Runtime precedence: the global value set from `/timeout` or the Alt+M panel > this setting > `DefaultLLMCallTimeout` (10m). Overriding it at runtime writes this line back. |
| `max_retries` | int | `5` | Engine-level retries — the request count is `max_retries + 1`. The SDK's own retries are disabled so this is the only budget. `≤0` → 5. |
| `step_retries` | int | `0` | Step-level retries in the Director and sub-agent loops, `0` = off |
| `circuit_breaker_threshold` | int | `0` | Consecutive failures before the breaker opens; **`0` = breaker disabled**, it is not created at all |
| `circuit_breaker_reset_timeout` | duration | `60s` when the breaker exists | Only defaulted when `circuit_breaker_threshold > 0` |
| `enable_fallback` | bool | `false` | Master switch for provider-level failover |
| `fallback_max_retries` | int | follows `max_retries` | Retries inside each fallback provider |

### 5.3 `[tools.deferred]` — deferred tool schemas

Each step normally carries full JSON schemas only for "Hot" tools; "Cold" tools appear in a compact
catalog and the model pulls the full schema back with `load_tool`. This block is read by
`internal/agents/deps.go` and therefore shapes every agent's tool surface.

| Field | Type | Default | Notes |
|-------|------|---------|-------|
| `disable` | bool | `false` | `true` = revert to "full schema on every step": no catalog, no `load_tool`. Troubleshooting / A-B comparison. |
| `budget_tokens` | int | `2500` | Per-step declaration budget. **Omitted or `0` → 2500**; **negative = no budget at all** (only the registry's own tiers). Over budget, Warm tools are demoted to Cold, largest declaration first. |
| `hot` / `cold` | array | — | Parsed, **no consumer** — retiering only happens through a profile's `Promote`/`Demote` lists (§7). |

### 5.4 `[routing]` — benchmark-scored automatic routing

With `mode = "auto"` (an empty value also means auto), startup scores **every model configured in any
provider** against the embedded benchmark catalog (`dist/data/models.json`) and picks three tiers:
highest score = high-IQ, median = balanced, lowest = fast. Each delegation runs one difficulty
classification on the fast tier, and the sub-agent executes on the tier it picked; `deepthinking`
always uses the high-IQ tier.

Scoring: the **strict intersection** of the candidates' benchmark entries,
`ai_score = Σ(score × weight)`, where DeepSWE, SWE-Bench Pro and GPQA Diamond weigh 1.5 and
everything else 1.0. Fewer than three scorable candidates, or an empty intersection, disables `auto`
and falls back to fixed-provider behaviour.

| Field | Type | Default | Notes |
|-------|------|---------|-------|
| `mode` | string | `"auto"` | `"auto"` \| `"fixed"` (`fixed` turns routing off entirely) |
| `[routing.baselines].<agent>` | string | per-role default | Tier used when the classifier fails. Values `fast` \| `balanced` \| `high` (`mid`/`cost` normalise to balanced, `smart` to high). Recognised agents: director, coding, repo, chat, devops, browser, meta |

Default baselines without this block: `director=high`, `meta=high`, `coding=balanced`,
`repo=balanced`, `chat=balanced`, `browser=balanced`, `devops=fast`.

```toml
[routing]
mode = "auto"
[routing.baselines]
coding = "high"
repo = "balanced"
```

### 5.5 `[codeseek]` + `[codeseek.knowledge]` — code-analysis engine

| Field | Type | Default | Notes |
|-------|------|---------|-------|
| `binary_path` | string | `""` | Empty → extract the embedded `dist/bin` to `~/.yagent/bin/codeseek`. Neither available = MCP off, agents fall back to plain file tools |
| `mcp_args` | array | `["serve", "--mcp"]` | MCP launch arguments |
| `request_timeout` | int | `30` | Request timeout, seconds (`≤0` → 30) |

`[codeseek.knowledge]` — **inert today**: knowledge injection is hardcoded off (`Inject()` in
`internal/knowledge/injector.go` returns early), so none of these keys produce any effect yet. They
are kept so the defaults do not have to be re-guessed when injection returns. The write path is
unaffected: `consolidate_knowledge` talks straight to MCP and does not read this block.

| Field | Type | Default | Notes |
|-------|------|---------|-------|
| `enabled` | bool | `true` | Only decides whether the injector is constructed |
| `injection_max_tokens` | int | `2000` | Token budget of the injected block |
| `injection_max_entries` | int | `8` | Max entries injected |
| `injection_min_score` | float | `0.3` | Minimum retrieval score |
| `injection_rerank` | bool | `true` | Ask for cross-encoder reranking (auto-degrades if the codeseek reranker is off) |

### 5.6 `[browser]` — Browser-Agent

The Browser Agent drives a real Chrome through the external `browser-control` CLI; Go never speaks
CDP itself.

> **Section-presence trap**: whether this section counts as "configured" is decided by *at least one
> key the current schema recognises* — and `enabled` is never defaulted. **If you write this section,
> write `enabled` too**, otherwise the browser capability is silently off. Legacy keys from the old
> stack (`headless`, `browser_path`, ...) are named in a startup warning and ignored.

| Field | Type | Default | Notes |
|-------|------|---------|-------|
| `enabled` | bool | `true` (only when the section is absent) | Off → `delegate_browser` degrades to "unavailable + reason + how to fix it" |
| `binary_path` | string | `""` | Lookup order: this value → `~/.yagent/bin/browser-control` → `$PATH`; none found = capability off |
| `chrome_path` | string | `""` | Empty → `browser-control` auto-detects Chrome/Chromium |
| `mode` | string | `"headless"` | `headless` \| `headful` \| `attach` (attach connects only, reusing a signed-in browser) |
| `cdp_url` | string | `""` | Required with `mode = "attach"`, e.g. `http://127.0.0.1:9222`; missing is an error |
| `cdp_port_base` | int | `9333` | First auto-launch port, incremented until free. Deliberately avoids 9222/9223 — upstream hard-scans those two when no CDP env var is set |
| `cdp_timeout` | duration | `65s` | Upstream's internal CDP round-trip budget; must cover the longest wait (`browser_wait` up to 60s); its factory default is only 15s. Lower values warn and floor at 65s |
| `command_timeout` | duration | `75s` | Per tool-call cap; must exceed `cdp_timeout` or it is raised to `cdp_timeout + 10s`, otherwise the outer layer kills the process before the model sees the upstream reason |
| `idle_ttl` | duration | `15m` | Close the Chrome we started after this much idleness (attached browsers are never closed) |
| `max_chrome` | int | `1` | Max Chrome instances in this process |
| `workspace_root` | string | `""` | Empty → `~/.yagent/data/browser` |
| `workspace_ttl` | duration | `72h` | Expired workspace cleanup (traces and screenshots included) |
| `max_output_bytes` | int | `24000` | Output budget handed to the model (stdout and stderr clipped separately) |
| `dialog_policy` | string | `"accept"` | `accept` \| `dismiss` \| `ask` |
| `allowed_domains` | array | `[]` | Allowlist; empty = no restriction on the public internet |
| `denied_domains` | array | local + RFC-1918 ranges | Supports `*.domain` and CIDR. `file://` and private ranges are denied by default |
| `profile_dir` | string | `""` | Set it to reuse login state across tasks |
| `enable_skills` | bool | `false` | Parsed, **unimplemented** (site playbooks are P2) |

Browser workspaces land under `{workspace_root}/{delegation}/.browser-control/`; the CLI's state
directory is CWD-relative, so every call goes through `internal/browserctl` with `cmd.Dir` set. Do
not spawn `browser-control` from elsewhere.

### 5.7 `[blackboard]` — cross-agent note board

Each delegation's result is written as a note at
`~/.yagent/notes/{boardID}/{agent}/*.md`; later delegations get those conclusions through
`context_refs` plus the **automatically injected index**. Full design:
[blackboard-handoff-design.md](./blackboard-handoff-design.md).

| Field | Type | Default | Notes |
|-------|------|---------|-------|
| `index_max_notes` | int | `12` | Max notes listed in the index. **`0` = index injection off** (emergency valve); negative or omitted = default |
| `index_line_chars` | int | `160` | Per-line rune budget. **Explicit `0` = do not clip lines**; negative or omitted = default |
| `summary_max_runes` | int | `1200` | Handoff summary budget handed to the Director (same source as the ContextPack excerpt); `≤0` = default |
| `ttl` | duration | `"168h"` | Delete a whole board directory after this long untouched. **`"0"` or `0` = never clean up**; negative is clamped to 0 |
| `max_notes_per_board` | int | `200` | Newest notes kept per board; older ones are deleted. `0` or negative = default — this key has no "off" meaning, raise it with a bigger number |

**Why presence, not zero values**: three keys here have a meaningful `0` (off index / no line clip /
no TTL cleanup) while "section absent" also decodes to `0`. The code therefore consults
`toml.MetaData.IsDefined` for those keys, and treats a section containing only unrecognised keys as
absent — so a missing `index_max_notes` never silently switches the index off. Unrecognised keys in
this section are named in a startup warning.

**The master switch is not here**: the whole board is disabled with `YAGENT_BLACKBOARD=0` (no writes,
no reads, no index injection). There is deliberately no `[blackboard] enabled` key — a boolean zero
value would silently disable a new capability, exactly the hole `[browser] enabled` fell into.
`YAGENT_NOTES_ROOT` overrides the notes root.

Index size ceiling = `index_max_notes × index_line_chars` (1920 runes by default), and it is injected
**every** Director round plus every delegation prefix — a standing cost. Trim `index_max_notes` if it
feels expensive, or set it to `0` to switch the index off.

### 5.8 `[resume]` — stop / resume budgets

Layer B of pause-resume: checkpoints recorded at a stop, and rollout replay on resume. Full design:
[pause-resume-design.md](./pause-resume-design.md).

| Field | Type | Default | Notes |
|-------|------|---------|-------|
| `enable` | bool | `true` | Off = no checkpoints written, no replay; a stopped task can only continue from the Director. **WARNING: writing this section without `enable = true` switches the mechanism off** — `enable` is not re-defaulted once the section exists |
| `replay_token_budget` | int | `0` | Token ceiling for the replayed context; `0` = the model catalog's threshold. Over budget is not an error — it degrades to a handoff report. Negative → 0 |
| `max_replay_messages` | int | `2000` | Max messages rebuilt. **Explicit `0` = unlimited**; negative or omitted = 2000 |
| `degrade_on_workspace_change` | string | `"warn"` | `warn` = HEAD moved but replay proceeds, with a degradation note; `block` = refuse replay and emit the handoff report. An unrecognised value warns, then falls back to `warn` |
| `checkpoint_workspace` | bool | `true` | Run `git status` at stop and record worktree facts in the checkpoint (switch off on enormous repos) |

`warn` is the default rather than `block` because HEAD always moves after a pull or a machine switch;
blocking by default would disable layer B in its main scenario. To opt out of the whole mechanism use
`YAGENT_RESUME=0` — off means not even writing checkpoints, because a half-enabled switch turns "why
does this rollout have no checkpoint" into a new question nobody can answer.

### 5.9 `[enhanced_commander]` — context compression

Three tiers, all in `internal/compression`:

1. **Rule** — `TruncateToolResultsToBudget`: cut low-value tool results first by priority, keeping
   `tool_result_keep_tokens` per result. Results marked `TruncProtected` are never truncated
   (`deepthinking`, `design`, and the `load_tool` catalog entry itself).
2. **Emergency** — move Thought & Plan blocks into thinklink, keep the last 3 messages verbatim,
   summarise the rest through an LLM, rewrite memory as `[system, one user]`.
3. **Ultimate** — if still over budget, rebuild the context from the thinklink log.

Trigger measure = the previous step's provider-reported input tokens plus local estimates for messages
added since (a provider-side count is trusted; missing or rewritten history invalidates it and falls
back to full local estimation, which is the pre-change behaviour).

Threshold = `min(model catalog limit.context × 0.75, context_compression_threshold)` — **both are
ceilings**: the catalog answers "how much this model holds", the config answers "how much I am
willing to pay". Omitted or `≤0` = catalog only; if neither gives a value, 120000. The provider's
`context_window` plays no part in this path (it only drives the TUI bar).

| Field | Type | Default | Notes |
|-------|------|---------|-------|
| `enable` | bool | `false` | Commander master switch |
| `enable_context_compression` | bool | `false` | Compression is on only when **both** `enable` and this are `true` (the code ANDs them) |
| `context_compression_threshold` | int | none | Caps the catalog-derived threshold (see above) |
| `tool_result_keep_tokens` | int | `200` | Tokens kept per tool result after tier-1 truncation |
| `enable_ultimate_compression` | bool | `true` | Tier 3. **Writing `false` does not turn it off**: `validate()` writes `true` back, so today this key only means "on by default" (pinned by `validate_test`) |
| `ultimate_compression_keep_plans` | int | `0` | Thought & Plan blocks kept when rebuilding; `0` = keep all. Applies to Director / Repo / DevOps / Browser only — the **Coding side hardcodes 0**, so this value never reaches it |
| `max_delegation_depth` | int | `3` | Parsed, **no consumer** — delegation depth is not config-driven |

Scope: Director + Repo / Coding / DevOps / Browser. Chat and Meta are never compressed.

Debug valve: `YAGENT_COMPRESS_THRESHOLD`, when set, *is* the threshold and bypasses both the catalog
and the config (re-read on every call, invalid values fall back silently). Never use it in production
— compression would fire before the model is actually full, throwing away context.

### 5.10 `[git_checkpoint]` — checkpoint and branch lifecycle

Checkpoints are created by the agent itself, at milestones, through the `git_checkpoint_create` tool.

| Field | Type | Default | Notes |
|-------|------|---------|-------|
| `enabled` | bool | `true` | Master switch (the workspace must also be a git repo) |
| `agent_branch_prefix` | string | `"agent"` | Working branch prefix → `agent/<session>` |
| `checkpoint_tag_prefix` | string | `"checkpoint/coding"` | Checkpoint tag prefix |
| `stash_dirty_worktree` | bool | `true` | Stash uncommitted changes at start |
| `squash_on_exit` | bool | `true` | Squash the agent branch's commits into one at exit |
| `auto_merge_on_exit` | bool | `false` | `false` = keep the branch squashed for manual review; `true` = squash-merge into the user branch |
| `cleanup_agent_branch` | bool | `true` | Delete the agent branch at exit (only when it really was merged) |
| `cleanup_checkpoint_tags` | bool | `true` | Delete checkpoint tags at exit |
| `generate_commit_message` | bool | `true` | Use an LLM for checkpoint commit messages |
| `auto_checkpoint` | bool | — | **Dead** — the auto-checkpoint hook (`OnStepEnd`) has no caller left |
| `checkpoint_interval` | int | — | **Dead** — same reason |
| `max_checkpoints` | int | `50` | **Dead** — read into the struct but never trims any tag or branch |

If `agent_branch_prefix` is empty the whole block is replaced by the defaults, so a partial
`[git_checkpoint]` section can silently restore other values.

### 5.11 `[keywords]` — completion dictionary

Dictionary paths are **hardcoded**: `~/.yagent/keywords.txt` (user) plus
`<project>/.yagent/keywords.txt` (project), loaded in `internal/tui/tui_model.go`.

| Field | Type | Default | Notes |
|-------|------|---------|-------|
| `disable_completion` | bool | `false` | The **only live key here**: `true` turns off keyword completion while typing |
| `default_path` | string | — | Parsed, no effect (paths are hardcoded) |
| `hot_reload` | bool | — | Parsed, no effect (watcher code was removed) |
| `[[keywords.dict]]` | array | — | Parsed, no effect |

### 5.12 `[app]`

| Field | Type | Notes |
|-------|------|-------|
| `enable_streaming` | bool | Parsed, **no effect** — it only reaches the startup log line. Both engines always use the streaming API |

### 5.13 `[tui]` — keybindings

Anything omitted keeps the Go default; the values below *are* the current defaults. Format is
Bubble Tea **v2** key-event strings, lowercase: modifiers `"ctrl+e"` / `"alt+s"` / `"alt+enter"`,
plain keys `"j"` / `"?"`, special keys `"enter"` / `"esc"` / `"tab"` / `"space"`.

**Edit mode** (`[tui.keybindings.edit]`):

| Field | Default | Action |
|-------|---------|--------|
| `submit_task` | `"alt+s"` | Submit the task |
| `command_mode` | `"ctrl+e"` | Enter command mode |
| `toggle_help` | `"ctrl+h"` | Toggle help |
| `toggle_timeline` | `"ctrl+l"` | Toggle the full-screen timeline |
| `toggle_dashboard` | `"alt+d"` | Collapse / expand the sidebar dashboard |
| `page_down` | `"ctrl+f"` | Page down |
| `page_up` | `"ctrl+b"` | Page up |
| `quit` | `"ctrl+c"` | Quit |
| `switch_model` | `"alt+m"` | Model panel (also where the LLM timeout is set) |
| `new_tab` | `"ctrl+t"` | New session tab |
| `prev_tab` | `"alt+["` | Previous tab |
| `next_tab` | `"alt+]"` | Next tab |
| `clear_session` | `"alt+c"` | Clear the current session |
| `close_tab` | `"alt+w"` | Close the current tab |
| `tab_1` … `tab_9` | `"alt+1"` … `"alt+9"` | Jump to tab N |

**Command mode** (`[tui.keybindings.command]`):

| Field | Default | Action |
|-------|---------|--------|
| `scroll_down` | `"j"` | Scroll one line down |
| `scroll_up` | `"k"` | Scroll one line up |
| `page_down` | `"f"` | Page down |
| `page_up` | `"b"` | Page up |
| `edit_mode` | `"i"` | Enter edit mode |
| `toggle_help` | `"?"` | Show help |
| `toggle_token_panel` | `"alt+t"` | Toggle the token panel |
| `toggle_dashboard` | `"alt+d"` | Collapse / expand the sidebar dashboard |
| `switch_model` | `"alt+m"` | Model panel |
| `quit` | `"ctrl+c"` | Quit |

### 5.14 `task_timeout` — root field

| Field | Type | Default | Notes |
|-------|------|---------|-------|
| `task_timeout` | duration | `"0s"` | Global task timeout — **only for http mode** (`http.NewTaskContext`, `TaskManager`). `"0s"`/omitted = off; e.g. `"30m"`. TUI and `--prompt` are not governed by it |

### 5.15 Environment variables

Out-of-band switches, useful for troubleshooting without editing the file:

| Variable | Effect |
|----------|--------|
| `YAGENT_BLACKBOARD=0` | Disable the note board completely |
| `YAGENT_NOTES_ROOT=<path>` | Relocate the notes root |
| `YAGENT_RESUME=0` | Disable checkpoints and replay completely |
| `YAGENT_ARTIFACT_DISABLE=1` | Disable oversized-result artifacts |
| `YAGENT_ARTIFACT_ROOT=<path>` | Relocate the artifact store |
| `YAGENT_COMPRESS_THRESHOLD=<tokens>` | Debug: force the compression threshold |
| `YAGENT_LOG_LEVEL=<level>` | Log verbosity |
| `YAGENT_BROWSER_DEBUG=<any>` | Extra browserctl debug output |

---

## 6. Scenario examples

### 6.1 Day-to-day: several providers plus failover

```toml
[global.llm]
use_provider = "aliyun"

[llm]
enable_fallback = true
fallback_max_retries = 2

[global.llm.providers.aliyun]
model = "qwen3-coder-plus"
max_tokens = 28000
api_base_url = "https://dashscope.aliyuncs.com/compatible-mode/v1"
api_key = "sk-your-key"
# Rate-limited on the primary: try deepseek first, then local
fallback_providers = ["deepseek_v4_pro", "local"]

[global.llm.providers.deepseek_v4_pro]
model = "deepseek-v4-pro"
max_tokens = 3000
api_base_url = "https://api.deepseek.com/v1"
api_key = "sk-your-key"
reasoning_effort = "max"

[global.llm.providers.local]
model = "Qwen3-30B-A3B"
max_tokens = 3000
api_base_url = "http://127.0.0.1:8000"
api_key = "dummy-key"

# Director on the capable model, Coding on the cheap local one
[agents.llm.director]
use_provider = "aliyun"

[agents.llm.coding]
use_provider = "local"
```

### 6.2 Anthropic access

```toml
[global.llm]
use_provider = "anthropic"

[global.llm.providers.anthropic]
api_format = "anthropic"
model = "claude-sonnet-4-5"
max_tokens = 16000
api_base_url = "https://api.anthropic.com/v1"
api_key = "sk-ant-xxxxxxxxxxxx"
reasoning_effort = "high"    # Extended Thinking: budget 24576, max_tokens raised to 28672
```

Note that `temperature` is not configurable while thinking is on — the engine sends `1.0`.

### 6.3 Local models (vLLM / Ollama compatible endpoints)

```toml
[global.llm]
use_provider = "local"

[global.llm.providers.local]
model = "Qwen3-30B-A3B"
max_tokens = 3000
api_base_url = "http://127.0.0.1:8000"   # vLLM endpoint
api_key = "dummy-key"                      # local servers usually do not check it
```

Make sure the server is up, listening on that port, and speaks the OpenAI chat-completions format.

### 6.4 Verifying compression behaviour

Catalog models have huge windows (1M tokens ⇒ threshold 750k), so a long task may never reach tier 1.
To exercise compression on a real run, either cap the threshold for one provider, or use the debug
valve:

```toml
[enhanced_commander]
enable = true
enable_context_compression = true
context_compression_threshold = 40000
```

```bash
YAGENT_COMPRESS_THRESHOLD=40000 ./yagent   # bypasses catalog and config; never in production
```

### 6.5 Keeping routing but pinning one role high

```toml
[routing]
mode = "auto"
[routing.baselines]
coding = "high"      # fallback tier when the classifier fails
devops = "fast"
```

---

## 7. Keys that are parsed but do nothing

These decode without error and never reach a consumer. The failure mode is "I changed it and nothing
happened", which is why they are worth listing:

| Key | Where | Reality |
|-----|-------|---------|
| `agents.llm.browser` | `[agents.llm.browser]` | Undecoded key — only six agent names exist (§4.3) |
| `enable_streaming` | `[app]` | Startup log only; streaming is always on (§5.12) |
| `keywords.default_path` / `hot_reload` / `[[keywords.dict]]` | `[keywords]` | Dictionary paths are hardcoded (§5.11) |
| `git_checkpoint.auto_checkpoint` / `checkpoint_interval` / `max_checkpoints` | `[git_checkpoint]` | Hook removed / never enforced (§5.10) |
| `agent.meta_max_steps` | `[agent]` | Normalised to 50, read by nobody — Meta is one LLM call, not a loop (§5.1) |
| `enhanced_commander.max_delegation_depth` | `[enhanced_commander]` | Normalised to 3, read by nobody (§5.9) |
| `enhanced_commander.enable_ultimate_compression = false` | `[enhanced_commander]` | `false` is written back to `true` by `validate()` |
| `enhanced_commander.ultimate_compression_keep_plans` | `[enhanced_commander]` | Ignored for the Coding Agent (hardcoded 0) |
| `tools.deferred.hot` / `cold` | `[tools.deferred]` | Retiering happens in profiles, not config (§5.3) |
| `browser.enable_skills` | `[browser]` | Parsed, unimplemented (P2) |
| `provider.aws_profile` / `model_provider` | `[global.llm.providers.*]` | No consumer; `aws_region` only satisfies the Bedrock name-prefix exemption (§4.1) |
| `max_replay_messages = 0`, `blackboard.index_max_notes = 0`, `blackboard.index_line_chars = 0`, `blackboard.ttl = "0"` | — | Not dead, but easy to misread: these explicit `0`s **do** carry meaning, unlike an omitted key |

---

## 8. Sections the code never reads

| Section | Why it is gone |
|---------|----------------|
| `[messaging]` | The event bus is built with hardcoded values (`BufferSize=1000` etc.); nothing maps to config |
| `[codexray]` (and sub-sections) | Removed; code analysis is provided by `[codeseek]` |
| `[director]` | No matching struct; fields were never read |
| `[tools_registry]` | Placeholder for "phase 2"; zero references |
| `[context]` | Present in old generated files: `enable_auto_compact`, `max_context_tokens`, `summarization_*`, `keep_recent_rounds` — `Config` has no such struct. Compression is configured in `[enhanced_commander]` (§5.9) |

---

## 9. FAQ

### Q: My change had no effect

1. **Check which file you edited** — only one file is loaded (§1.1). `config/config.toml` in a checkout
   is an example, not an active layer; run `yagent -c ./config/config.toml` to use it deliberately.
2. **Check the precedence chain** (§4.2) — a `[tools.llm.*]` or `[agents.llm.*]` override will beat the
   global `use_provider` you just changed.
3. **Check whether the key is live at all** — see §7 and §8.
4. **Check the startup log** — `Loading configuration config_path=...` names the file, and
   `[browser]` / `[blackboard]` / `[resume]` warn about keys they did not recognise.
5. **Validate the TOML**: `python3 -c "import tomllib; tomllib.load(open('config.toml','rb'))"`.

### Q: `provider 'xxx' not found in configuration`

The `use_provider` value (in `global.llm`, `agents.llm` or `tools.llm`) has no matching
`[global.llm.providers.xxx]` section. Names are case-sensitive and exact.

### Q: How do I switch models?

- Globally: `[global.llm].use_provider`
- Per agent: `[agents.llm.<agent>].use_provider` (six names, no `browser`)
- Per tool: `[tools.llm.<tool>].use_provider`
- At runtime: `/model` (or Alt+M), which can persist the choice back into the file

### Q: How do I get a fresh config after corrupting the file?

Delete it and start Yagent; the embedded template is written atomically.

```bash
rm ~/.yagent/config/config.toml
yagent
```

### Q: How do I disable an agent?

`--disable-agents` / `-d` at startup:

```bash
yagent --disable-agents browser,devops
```

### Q: Does hot reload exist?

No. The file is parsed once at startup. Only the three runtime controls in §1.3 take effect mid-session,
and each writes its own key back into the file. Restart for anything else — which also means editing the
file while Yagent runs cannot race the loader.
