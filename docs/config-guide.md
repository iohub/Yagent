# Yagent Configuration Guide

> **Scope**: this is the user-facing reference for `config.toml` — every section, the setup wizard,
> the provider resolution chain, failover, and scenario examples. Related deep dives:
> - [Anthropic API configuration](./anthropic-config.md) — Claude access and Extended Thinking
> - [LLM failover configuration](./llm-fallback-config.md) — multi-provider fallback strategies
> - [Context compression trigger design](./compression-trigger-design.md) — how the threshold is
>   derived and measured
> - [Pause / resume design](./pause-resume-design.md) — what the `[resume]` budget controls
>
> Every key listed here has a consumer in the code (checked 2026-10-16). Keys that were parsed but
> never read have been deleted rather than documented: [§7](#7-removed-keys-what-to-write-instead)
> maps each old name to what to write instead, and startup names any that are still in your file.

---

## 1. Configuration file basics

### 1.1 Which file is loaded — one file, no layering

Yagent reads **exactly one** TOML file. There is no merge between an "user layer", a "project
layer" and a "built-in layer" (older versions of this document claimed there was). `getConfigPath()`
in `main.go` picks the first applicable entry:

| Order | Path | Behaviour when missing |
|:-----:|------|------------------------|
| 1 | `--config` / `-c <path>` | Used verbatim; **never created**, never defaulted. A path that cannot be loaded is a startup failure — the wizard in TUI mode, otherwise the stderr report in §9 |
| 2 | `$HOME/.yagent/config/config.toml` | Generated from the embedded template (this is the normal path) |
| 3 | `./config/config.toml` | Reached **only** if the home-directory lookup itself fails — it is not a second-tier search, and a project-level file is never consulted on a normal machine |

The repo's `config/config.toml` is therefore an **annotated reference catalogue**, not an active config
layer: the file that actually runs is the one in `~/.yagent/config/`, generated from
`internal/config/default_config.toml`. Both carry the same key set — `template_test.go` fails if either
contains a key the schema does not read.

Because a single file is loaded, a key you change in `config/config.toml` while running from
`~/.yagent/config/config.toml` has no effect at all.

### 1.2 First-run generation

When the chosen path does not exist, `config.EnsureConfigExists()` materialises the embedded template
`internal/config/default_config.toml` with an atomic write (`.tmp` then rename), so an interrupted
write cannot leave a half-written config. The file is created **mode `0600`** — it may hold a literal
API key, and `config.toml` is exactly the kind of file people paste into questions whole. A later
write-back keeps the mode of the file it found. An existing file is **never** overwritten.

The generated template is deliberately *not* a usable configuration: `use_provider` points at a
provider whose `api_key` is the placeholder `your-key`. Startup recognises that (§2.3) and opens the
setup wizard instead of pretending to be ready — the alternative was "TUI starts fine, then the very
first task fails in an LLM call whose error only reaches a log file".

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

The setup wizard (§2.3) writes through the same text-level layer — `SetKeyInSection`,
`RemoveKeyInSection`, `RemoveSection`. Values are replaced in place, never re-encoded, so your comments
and line ordering survive; `PersistUseProvider` / `PersistRoutingMode` / `PersistLLMTimeout` are now
thin wrappers over that one function.

Keys that no longer exist are named on every startup (§7), so an old file tells you what to change
instead of quietly ignoring what you wrote.

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

Validation requires `use_provider`, that provider's `model`, and a non-empty `api_base_url` plus a key
— for **every** provider, with no vendor exemption (§4.1). The key may come from `api_key_env` instead
of the file. A literal key that still reads `your-key` counts as "not filled in" and opens the wizard
(§2.3). Validation never contacts the endpoint; the wizard's probe does.

### 2.2 Switching the default provider

```toml
[global.llm]
use_provider = "local"   # move everything to the local model
```

### 2.3 The setup wizard

Hand-editing provider blocks is the slow path: the key has to land in the right table, and nothing in
the file tells you whether the endpoint accepts it. The wizard configures providers plus the choices a
first run has to make, can test the endpoint before saving, and writes comment-preservingly (§1.3).

| Entry | Behaviour |
|-------|-----------|
| `yagent setup` | Runs the wizard on its own — no agent assembly, no MCP, no requirement that the current config loads. A broken `config.toml` is precisely when it is needed most. Without a terminal it refuses and points at this document. |
| `/setup` in the TUI | The same wizard as a full-screen mode. Refused while a task is running or a dialog is open. |
| Automatically at startup | When the config file was just created, when the effective provider's `api_key` is still a placeholder, or when loading/validation fails. With a terminal available in TUI mode this opens the wizard; `--prompt` and `http` have no UI to fall back on, so they get the stderr report described in §9. |

Screens, in order: welcome → provider list → provider form → connectivity probe → default provider →
first-run choices → review & write. On the list screen: `a` add, `e`/`Enter` edit, `d` delete, `t`
probe, `c` choose the default provider.

What it collects and where each answer goes:

| Screen | Keys written |
|--------|--------------|
| Provider form | `[global.llm.providers.<name>]`: `model`, `api_base_url`, `api_key` **or** `api_key_env`, `api_format`, `reasoning_effort`, `max_tokens`, `context_window` |
| Default provider | `[global.llm] use_provider` |
| First-run choices | `[agent] lang` · `[agent] yolo_mode` + `full_yolo_mode` · `[routing] mode` · `[browser] enabled` · `[llm] timeout` |

Worth knowing before using it:

- **Nothing is written until the review screen.** Everything before it is a queued operation; `q` exits
  from any screen and the file stays byte-for-byte unchanged. On the review screen `Enter` applies the
  queue in order (providers written → providers deleted → `use_provider` → first-run keys → `[llm]
  timeout`); the first failure stops the sequence and names the operation that failed, so a partial run
  is never reported as a success.
- The probe is a real request — one minimal completion (no tools, 30 s ceiling, tiny `max_tokens`). On
  failure the provider's own error text is shown verbatim, not paraphrased, because that text is what
  you go and search for.
- Choosing `api_key_env` for a variable that is not set in this shell is allowed and only annotated —
  the point is that the file stays free of secrets and the next start (or the probe) reports it.
- `temperature` is deliberately not a form field: a bare `float64` cannot tell "wrote `0.0`" from "left
  it out", and `0` already means "not set" (§4.1). Whatever is in the file is carried over untouched.
- A provider still referenced anywhere — `global.llm` / `agents.llm.*` / `tools.llm.*` `use_provider`,
  or any `fallback_providers` list — **cannot be deleted**. The wizard names the reference and makes
  you move it first rather than writing a file that fails to load on the next start.
- Provider names must match `[A-Za-z0-9_-]{1,64}` (a dot or quote in a table name would build a nested
  or malformed file). Renaming an existing provider is blocked: a "renamed" block is a *second* block,
  and the old one would stay behind pretending to be the active configuration.
- `/setup` inside a running TUI changes only the file — restart to load it; the process keeps the
  engine it started with. `yagent setup` has no running process to conflict with.

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
| `[codeseek]` | CodeSeek MCP code-analysis engine | No |
| `[browser]` | Browser-Agent driving the external `browser-control` CLI | No |
| `[blackboard]` | Cross-agent note board budgets | No |
| `[resume]` | Stop / resume checkpoint and replay budgets | No |
| `[enhanced_commander]` | Context compression | No |
| `[git_checkpoint]` | Git checkpoint / branch lifecycle | No |
| `[keywords]` | Keyword completion (one key) | No |
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
| `api_base_url` | string | — | Required, with no vendor exemption (see the note below); `validate()` demands it of the effective provider |
| `api_key` | string | — | Required unless `api_key_env` supplies it. On the effective provider, a placeholder value (`your-key`, `changeme`, all-`x`, …) counts as "not filled in" and opens the wizard (§2.3) |
| `api_key_env` | string | `""` | Name of an environment variable holding the key. Expanded into `api_key` **once, at load time**, so both engines and every other consumer read the same field — nothing has to remember "check the variable first". Unset variable: startup fails naming the variable if that provider is the effective one, otherwise a warning at startup and a failure only if it is actually used. Mutually exclusive with the literal key — the wizard drops `api_key` when you set this. There is no general `${VAR}` interpolation, deliberately: it would turn every key containing a `$` into a time bomb |
| `temperature` | float | not sent | **`0` is treated as "not set"**: the field is a plain `float64`, so "wrote `0.0`" and "left it out" are indistinguishable, and `0` means the request omits `temperature`. Forcing greedy decoding via config is not currently possible. |
| `max_tokens` | int | not sent | Engine-level default. Priority: per-call override > this value > field omitted. The Anthropic API requires it, so an unset value falls back to 8192. |
| `context_window` | int | `0` | **Affects only the TUI context progress bar.** It is not used for the compression threshold (§5.9). `0`/unset = no bar. |
| `reasoning_effort` | string | `""` | openai side: passed through verbatim, plus `thinking={type:enabled}`. anthropic side: mapped to `thinking.budget_tokens` — `low`/`medium`/`high`/`max` = 1024/8192/24576/32000, `max_tokens` is raised to budget+4096 and `temperature` forced to 1.0. An unrecognised value means "thinking off"; `budget_tokens: 0` is never sent. |
| `api_format` | string | `"openai"` | `"openai"` or `"anthropic"` (native Messages API) |
| `fallback_providers` | array | `[]` | Failover list; needs `[llm] enable_fallback = true` |

**No vendor exemptions.** `model` + `api_base_url` + a key are required for *every* provider. This
repo has no Bedrock-specific client, so a provider named `bedrock*` must point `api_base_url` at an
OpenAI-compatible gateway like any other one. The old exemption was `strings.HasPrefix(effectiveProvider,
"bedrock")` — decided by the *name*, not by `api_format` — and it enabled nothing: a real Bedrock setup
with any other name still had both keys demanded, while a merely *named* `bedrock*` skipped the checks
and then failed on the first call. The three keys behind it (`aws_region`, `aws_profile`,
`model_provider`) are gone (§7).

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

Seven agent names, all live:

| Agent | Section |
|-------|---------|
| Director | `[agents.llm.director]` |
| Coding | `[agents.llm.coding]` |
| Repo | `[agents.llm.repo]` |
| Chat | `[agents.llm.chat]` |
| DevOps | `[agents.llm.devops]` |
| Browser | `[agents.llm.browser]` |
| Meta | `[agents.llm.meta]` |

`config.getAgentOverride()` matches each of them, also under the legacy `<name>-agent` spelling.
`[agents.llm.browser]` was added in this pass — before it, the section was reported as an undecoded key
and silently dropped, so the Browser Agent could only be moved by the table-level `use_provider` or the
global default.

```toml
[agents.llm.coding]
use_provider = "deepseek_v4_pro"

[agents.llm.browser]
use_provider = "local"
```

Precedence above this static override: a model picked in the TUI at runtime outranks it, and with
`[routing] mode = "auto"` the routing tier outranks the per-agent `use_provider` (§5.4).

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
| `meta_retry_count` | int | `5` | Retries when Meta emits invalid JSON |
| `delegate_idle_timeout` | duration | derived | See below |
| `delegate_total_timeout` | duration | `0` (= off) | Hard cap per delegation |
| `max_parallel_readonly_tools` | int | `4` | Parallelism of a read-only tool run: `≤0` → 4, `>8` clamps to 8, **`1` = strictly serial (kill switch)** |
| `lang` | string | `""` | Conversation language. Empty injects no instruction; e.g. `"Chinese"`, `"English"` |

Step caps: `0` or an omitted key falls back to the code defaults listed above — the same numbers.
Meta is not in this list: it is a single LLM call with no step loop, so only `meta_retry_count` means
anything for it (`meta_max_steps` used to be normalised to 50 and read by nobody).

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
| `step_retries` | int | `0` | Step-level LLM retries, `0` = off. Applies to the Director, Meta **and every sub-agent**. This pass wired it through: `ExecutorConfig.StepRetries` had no writer anywhere in the repo, so sub-agent loops always ran with 0 no matter what was configured |
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

There is no per-tool list here any more: the `hot` / `cold` arrays were parsed, logged by length, and
read by nobody. Moving a tool between tiers is a code-side decision, made through a profile's
`Promote` / `Demote` lists in `internal/toolspec`.

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

### 5.5 `[codeseek]` — code-analysis engine

| Field | Type | Default | Notes |
|-------|------|---------|-------|
| `binary_path` | string | `""` | Empty → extract the embedded `dist/bin` to `~/.yagent/bin/codeseek`. Neither available = MCP off, agents fall back to plain file tools |
| `mcp_args` | array | `["serve", "--mcp"]` | MCP launch arguments |
| `request_timeout` | int | `30` | Request timeout, seconds (`≤0` → 30) |

`[codeseek.knowledge]` is deleted, not merely inert. Knowledge injection is hardcoded off — `Inject()`
in `internal/knowledge/injector.go` returns early — so its four budget keys had no reachable reader,
and `enabled` never worked either: `validate()` wrote `true` back whenever it saw `false`. The budgets
are file constants in `injector.go` now (`injMaxEntries = 8`, `injMaxTokens = 2000`, `injMinScore = 0.3`,
`injRerank = true`) and become configuration again only if injection returns.

The write path was never affected: `consolidate_knowledge` talks straight to MCP.

### 5.6 `[browser]` — Browser-Agent

The Browser Agent drives a real Chrome through the external `browser-control` CLI; Go never speaks
CDP itself.

> **Section-presence trap**: whether this section counts as "configured" is decided by *at least one
> key the current schema recognises* — and `enabled` is never defaulted. **If you write this section,
> write `enabled` too**, otherwise the browser capability is silently off. Legacy keys from the old
> stack (`headless`, `browser_path`, ...) are named in a startup warning and ignored, as is the
> deleted `enable_skills`.

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
| `enable_context_compression` | bool | `false` | The **single** switch for all three tiers. There used to be a second boolean, `[enhanced_commander] enable`, that the code ANDed with this one — it is deleted, so writing one key no longer silently does nothing |
| `context_compression_threshold` | int | none | Caps the catalog-derived threshold (see above) |
| `tool_result_keep_tokens` | int | `200` | Tokens kept per tool result after tier-1 truncation (`0`/omitted → 200) |
| `enable_ultimate_compression` | bool | `true` **when the key is absent** | Tier 3. `validate()` used to write `true` back whenever it saw `false`, which made tier 3 impossible to switch off; the value is respected now. Since the default is "on" while Go's zero value is `false`, the distinction is decided by **TOML key presence**: omit the key → on, write `false` → off |
| `ultimate_compression_keep_plans` | int | `0` | Thought & Plan blocks kept when rebuilding; `0` = keep all. Reaches the Director and every compressed sub-agent — the Coding side used to hardcode `0`, so a configured value never got there |

Delegation depth is not configurable: `max_delegation_depth` was normalised to 3 and read by nobody.

Scope: Director + Repo / Coding / DevOps / Browser. Chat and Meta are never compressed.

Debug valve: `YAGENT_COMPRESS_THRESHOLD`, when set, *is* the threshold and bypasses both the catalog
and the config (re-read on every call, invalid values fall back silently). Never use it in production
— compression would fire before the model is actually full, throwing away context.

### 5.10 `[git_checkpoint]` — checkpoint and branch lifecycle

Checkpoints are created by the agent itself, at milestones, through the `git_checkpoint_create` tool.

| Field | Type | Default | Notes |
|-------|------|---------|-------|
| `enabled` | bool | `true` (only when the section is absent) | Master switch (the workspace must also be a git repo) |
| `agent_branch_prefix` | string | `"agent"` | Working branch prefix → `agent/<session>` |
| `checkpoint_tag_prefix` | string | `"checkpoint/coding"` | Checkpoint tag prefix |
| `stash_dirty_worktree` | bool | `true` | Stash uncommitted changes at start |
| `squash_on_exit` | bool | `true` | Squash the agent branch's commits into one at exit |
| `auto_merge_on_exit` | bool | `false` | `false` = keep the branch squashed for manual review; `true` = squash-merge into the user branch |
| `cleanup_agent_branch` | bool | `true` | Delete the agent branch at exit (only when it really was merged) |
| `cleanup_checkpoint_tags` | bool | `true` | Delete checkpoint tags at exit |
| `generate_commit_message` | bool | `true` | Use an LLM for checkpoint commit messages |

**Section presence is all-or-nothing for booleans.** If `[git_checkpoint]` does not appear at all (or
holds only keys the schema does not know), every field takes the default above. Once you write the
section, **each boolean is yours to set** — omitting one means `false`, and only the two prefixes are
filled in for you. The previous rule was different and worse: an empty `agent_branch_prefix` replaced
the *entire* section with defaults, so `enabled = false` was silently flipped back to `true` and the
one way to turn snapshots off was to write nothing at all.

There is no automatic per-step checkpoint: `auto_checkpoint`, `checkpoint_interval` and
`max_checkpoints` are deleted. The first one's only remaining effect was a deprecation warning printed
every step, and nothing read the other two to trim tags or branches.

### 5.11 `[keywords]` — completion dictionary

Dictionary paths are **hardcoded**: `~/.yagent/keywords.txt` (user) plus
`<project>/.yagent/keywords.txt` (project), loaded in `internal/tui/tui_model.go`.

| Field | Type | Default | Notes |
|-------|------|---------|-------|
| `disable_completion` | bool | `false` | `true` turns off keyword completion while typing |

`default_path`, `hot_reload` and `[[keywords.dict]]` are deleted: they were parsed, written back into
defaults by `validate()`, and read by nothing outside that loop. The two paths above are the only
dictionaries.

### 5.12 `[tui]` — keybindings

Anything omitted keeps the Go default; the values below *are* the current defaults. Format is
Bubble Tea **v2** key-event strings, lowercase: modifiers `"ctrl+e"` / `"alt+s"` / `"alt+enter"`,
plain keys `"j"` / `"?"`, special keys `"enter"` / `"esc"` / `"tab"` / `"space"`.

**A binding replaces, it does not alias.** Setting `submit_task = "ctrl+s"` makes `ctrl+s` submit the
task **and** frees `alt+s`, which then does nothing. The old implementation only added
`custom → default` to the translation table, so a "moved" action stayed reachable on its default key
and that key could never be unbound — someone who relocated `quit` still got the program killed by
`ctrl+c`.

Two actions cannot share one key: on a conflict the action declared **first** in the table keeps it,
the other stays on its own default, and everything dropped is collapsed into a single startup warning
(`[tui.keybindings] 键位冲突，按声明顺序保留先者`). Keys that no action claims pass through untouched.

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

### 5.13 `task_timeout` — root field

| Field | Type | Default | Notes |
|-------|------|---------|-------|
| `task_timeout` | duration | `"0s"` | Global task timeout — **only for http mode** (`http.NewTaskContext`, `TaskManager`). `"0s"`/omitted = off; e.g. `"30m"`. TUI and `--prompt` are not governed by it |

### 5.14 Environment variables

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
api_key_env = "ANTHROPIC_API_KEY"   # keeps the secret out of config.toml
reasoning_effort = "high"    # Extended Thinking: budget 24576, max_tokens raised to 28672
```

Note that `temperature` is not configurable while thinking is on — the engine sends `1.0`. With
`api_key_env`, leaving that variable unset makes startup fail naming it (§4.1).

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
To exercise compression on a real run, either cap the threshold, or use the debug valve:

```toml
[enhanced_commander]
enable_context_compression = true
context_compression_threshold = 40000
```

```bash
YAGENT_COMPRESS_THRESHOLD=40000 ./yagent   # bypasses catalog and config; never in production
```

To check that tier 3 really is switchable, write `enable_ultimate_compression = false` — the value is
honoured now, and tiers 1 and 2 keep running without it (§5.9).

### 6.5 Keeping routing but pinning one role high

```toml
[routing]
mode = "auto"
[routing.baselines]
coding = "high"      # fallback tier when the classifier fails
devops = "fast"
```

---

## 7. Removed keys: what to write instead

Every key below used to decode without error and reach no consumer. They are deleted from the schema
rather than documented — the honest fix for "I changed it and nothing happened" is to make the key
unnameable. Change log: [config-trim-onboarding-design.md](./config-trim-onboarding-design.md).

**Old files are not silently ignored.** The decoder's undecoded keys are matched against the registry in
`internal/config/config.go` (`removedKeyHints` / `removedProviderKeyHints`), and every hit is named in a
startup warning along with the hint shown below (the warning prints those hints verbatim, in Chinese). A
whole removed block is reported once, not once per child key. `yagent setup` / `/setup` (§2.3) rewrites
the file cleanly; `config/config.toml` in a checkout is the current annotated catalogue.

| Removed key | What to write instead |
|-------------|---------------------|
| `app.enable_streaming` (and the whole `[app]` table) | Nothing — both engines always use the streaming API, and the key never had a reader |
| `keywords.default_path` | Nothing — dictionary paths are fixed to `~/.yagent/keywords.txt` and `<project>/.yagent/keywords.txt` |
| `keywords.hot_reload` | Nothing — the hot-reload code is removed |
| `keywords.dict` (`[[keywords.dict]]`) | Nothing — the dictionary list is those two paths; custom entries had no reader |
| `agent.meta_max_steps` | Nothing — Meta is one LLM call with no step loop; only `meta_retry_count` means anything |
| `enhanced_commander.enable` | Nothing beyond `enable_context_compression` — compression used to require **both** booleans |
| `enhanced_commander.max_delegation_depth` | Nothing — delegation depth is not config-driven |
| `git_checkpoint.auto_checkpoint` | Nothing — checkpoints are created by the `git_checkpoint_create` tool at milestones |
| `git_checkpoint.checkpoint_interval` | Nothing — the auto-checkpoint hook is deleted |
| `git_checkpoint.max_checkpoints` | Nothing — no code read it to trim tags or branches |
| `tools.deferred.hot` / `tools.deferred.cold` | Nothing — retiering goes through a toolspec profile's `Promote` / `Demote` lists |
| `browser.enable_skills` | Nothing — site playbook injection was never implemented |
| `[codeseek.knowledge]` (whole block) | Nothing — knowledge injection is hard-disabled (`injector.go` returns early), and `enabled` never took effect because `validate()` wrote `true` back |
| `global.llm.providers.<name>.aws_region` | Point `api_base_url` + `api_key` at an OpenAI-compatible gateway — there is no Bedrock-specific client here |
| `global.llm.providers.<name>.aws_profile` / `.model_provider` | Nothing — no reader |

**Not removed, but easy to misread.** These values carry meaning that an omitted key does not, and the
distinction is made by TOML *key presence*, not by zero values: `blackboard.index_max_notes = 0` (index
injection off) · `blackboard.index_line_chars = 0` (do not clip lines) · `blackboard.ttl = "0"` (never
clean up) · `resume.max_replay_messages = 0` (unlimited) · `tools.deferred.budget_tokens` negative (no
budget) · `enhanced_commander.enable_ultimate_compression` absent (on) versus `= false` (off).

**Keys that were alive but lying.** They behave as documented now, so if you already had them written
the effect may differ from the last run: `[llm] step_retries` reaches every sub-agent (§5.2),
`ultimate_compression_keep_plans` reaches Coding (§5.9), `enable_ultimate_compression = false` really
switches tier 3 off (§5.9), `[agents.llm.browser]` is honoured (§4.3), and a written `[git_checkpoint]`
section keeps its booleans as written instead of an empty prefix replacing the whole block — which used
to flip `enabled = false` back to `true` (§5.10). Remapped TUI keys are bound *only* to the new key
(§5.12).

---

## 8. Sections the code never had

These names have no struct at all, so they are not in the removed-key registry either (§7) — a file
carrying them produces **no** startup warning, and the lines just sit there. Delete them by hand.

| Section | Why it is gone |
|---------|----------------|
| `[messaging]` | The event bus is built with hardcoded values (`BufferSize=1000` etc.); nothing maps to config |
| `[codexray]` (and sub-sections) | Removed; code analysis is provided by `[codeseek]` |
| `[director]` | No matching struct; fields were never read |
| `[tools_registry]` | Placeholder for "phase 2"; zero references |
| `[context]` | Present in old generated files: `enable_auto_compact`, `max_context_tokens`, `summarization_*`, `keep_recent_rounds` — `Config` has no such struct. Compression is configured in `[enhanced_commander]` (§5.9) |
| `[app]` | Struct deleted with its only key (§7) |

---

## 9. FAQ

### Q: My change had no effect

1. **Check which file you edited** — only one file is loaded (§1.1). `config/config.toml` in a checkout
   is an example, not an active layer; run `yagent -c ./config/config.toml` to use it deliberately.
2. **Check the precedence chain** (§4.2) — a `[tools.llm.*]` or `[agents.llm.*]` override will beat the
   global `use_provider` you just changed, and with `mode = "auto"` the routing tier beats the per-agent
   one (§4.3).
3. **Check whether the key still exists** — §7 lists the deleted ones, §8 the sections that never had a
   struct. A deleted key is named on every startup; a §8 section is not.
4. **Check the startup log** — `Loading configuration config_path=... created=...` names the file,
   `[browser]` / `[blackboard]` / `[resume]` warn about keys they did not recognise, and removed keys are
   listed with what to write instead.
5. **Check section presence** — writing `[git_checkpoint]`, `[browser]`, `[blackboard]` or `[resume]`
   with a key missing can mean `false` / "section absent" rather than a default (§5.6, §5.7, §5.8, §5.10).
6. **Validate the TOML**: `python3 -c "import tomllib; tomllib.load(open('config.toml','rb'))"`.

### Q: startup prints `yagent: configuration is not usable`

That is the whole failure report for non-interactive entries — before it, the error went to
`~/.yagent/logs` and the process exited without printing anything on the terminal:

```
yagent: configuration is not usable
  reason: <the validation error, or: provider "aliyun" still has api_key = "your-key">
  file:   /Users/you/.yagent/config/config.toml
  fix:    run `yagent setup` (or edit the file; see docs/config-guide.md)
  logs:   ~/.yagent/logs
```

In TUI mode with a terminal available you get the wizard instead (§2.3); `--prompt` and `http` get these
lines and exit code 2. The reason is one of: the file was just created, the effective provider's key is
still a placeholder, or loading failed.

### Q: `provider 'xxx' not found in configuration`

The `use_provider` value (in `global.llm`, `agents.llm` or `tools.llm`) has no matching
`[global.llm.providers.xxx]` section. Names are case-sensitive and exact.

### Q: How do I switch models?

- Globally: `[global.llm].use_provider`
- Per agent: `[agents.llm.<agent>].use_provider` (seven names, `browser` included — §4.3)
- Per tool: `[tools.llm.<tool>].use_provider`
- At runtime: `/model` (or Alt+M), which can persist the choice back into the file
- Through the wizard: `yagent setup` / `/setup`, with a live probe available before anything is written (§2.3)

### Q: How do I get a fresh config after corrupting the file?

Delete it and start Yagent; the embedded template is written atomically, mode `0600` (§1.2), and the
wizard opens because its key is still the placeholder. Or run the wizard against the damaged file
directly — it reads the file as text and does not need it to load:

```bash
rm ~/.yagent/config/config.toml
yagent
# or, keeping the file:
yagent setup
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
