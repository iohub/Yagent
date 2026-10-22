# 外部能力接入设计：通用 MCP + 技能（Skills）

Yagent 今天有两块"半成品"：`internal/mcp` 是一个只对着 codeseek 说话的 stdio JSON-RPC 客户端，
`internal/skills` 是一个只被 TUI 斜杠补全消费的扁平 `.md` 加载器。两者都缺同一件东西——
**外部能力进不了模型的视野**。本稿给出把它们接进既有工具面（`toolspec` 注册表 + `Surface` 三档
deferred）的完整设计。

| 项 | 值 |
|---|---|
| 适用模块 | `internal/mcp`、`internal/tools`、`internal/toolspec`、`internal/skills`、`internal/agents`、`internal/config`、`internal/app`、`internal/tui` |
| 关键类型 | `mcp.Manager`、`mcp.DiscoveredTool`、`toolspec.Spec.Inline`、`toolspec.Deps.ExternalSpecs`、`skills.Registry`、`skills.LoadAccount` |
| 关键配置 | `[[mcp.servers]]`、`[mcp]`、`[skills]` |
| 声明面基线 | `schemas.json` **49 → 50**（新增 `use_skill`） |
| 证据来源 | 全部基于当前代码实测（每条带 `file:line`） |

---

## 目录

1. [背景：两个半成品的实测账](#1-背景两个半成品的实测账)
2. [目标与非目标](#2-目标与非目标)
3. [总体形状](#3-总体形状)
4. [A. 动态工具规格（不进全局注册表）](#4-a-动态工具规格不进全局注册表)
5. [B. MCP：协议面补全 + Manager 生命周期](#5-b-mcp协议面补全--manager-生命周期)
6. [C. Skills：磁盘约定、合并语义、use_skill](#6-c-skills磁盘约定合并语义use_skill)
7. [D. 配置面](#7-d-配置面)
8. [E. 注入点清单（逐处 file:line）](#8-e-注入点清单逐处-fileline)
9. [F. 校验、账目与静默失效防线](#9-f-校验账目与静默失效防线)
10. [G. 测试闸门与变异清单](#10-g-测试闸门与变异清单)
11. [H. 实施顺序（W1–W7）](#11-h-实施顺序w1w7)
12. [I. 已知边界与刻意不做的事](#12-i-已知边界与刻意不做的事)
13. [J. 文档同步清单](#13-j-文档同步清单)

---

## 1. 背景：两个半成品的实测账

### 1.1 Skills：机制只到"斜杠命令"

| 事实 | 证据 |
|---|---|
| 只认扁平 `*.md`，子目录直接跳过 ⇒ 业界 `<name>/SKILL.md` 约定一条都读不到 | `internal/skills/skills.go:42-50` |
| 没有 front-matter：`Name` = 文件名去后缀，`Description` = 第一行去 `#`，`Content` = 全文 | `internal/skills/skills.go:59-79` |
| 消费面只有一处：TUI `/` 前缀补全 → Tab 高亮 + Enter → 整篇正文拼进用户消息 | `internal/tui/tui_update_autocomplete.go:128-192`、`internal/tui/tui_update_keys.go:1005-1012` |
| 模型完全看不见技能：七个 `*.prompt.md` 里 `skill` 零命中，`Deps` 没有技能通道，注册表里没有 `use_skill` | `internal/toolspec/deps.go:98-142` |
| 作用域是**替换**不是合并：项目里只要有一条，`~/.yagent/skills` 永久不可见 | `main.go:234`、`main.go:384` |
| 三个入口两份同形代码 + 一个漏网：`runTUI` 与 `runPrompt` 各写 17 行逐字相同，`runHTTP` 压根不加载 | `main.go:224-241`、`main.go:374-391`、`main.go:258-329` |
| 内置命令与技能挤在同一个补全列表里逐个特判，命名空间会撞 | `internal/tui/tui_update_keys.go:971-1003` |
| 手打 `/技能名 参数` 回车**不会**执行技能（`/` 前缀默认是搜索） | `internal/tui/tui_update_commands.go:27-41` |
| 目录存在但不可读 ⇒ `LoadSkills` 返回 `(nil, err)`，调用点 `.Count()` nil 解引用 | `internal/skills/skills.go:37-40,116-120` + `main.go:234`（`internal/AGENTS.md:83-89` 已记未修） |
| 曾经想接、被砍：`enable_skills` 是已删键 | `internal/config/config.go:276`（`browser.enable_skills → "站点 playbook 注入未实现"`） |

`internal/skills/skills_test.go` 六个用例全部钉的是"扁平 .md"这个当前形状，改约定必须连它一起重写。

### 1.2 MCP：传输层是好的，协议面和装配面是半成品

传输层质量不差，不要动它：`sendJSON` 持写锁防交错（`internal/mcp/client.go:50-52,428-444`）、
每条请求 deadline 取调用方 ctx 与 `RequestTimeout` 的较小值（`:324-332`）、乱序响应按 ID 配对
（`:402-413`）、goroutine 全进 `supervise` 域且 Shutdown 会 join（`:112-126,266-270`）、
假服务器 = 重新执行测试二进制（`internal/mcp/client_process_test.go:22-33`）。

半成品在协议面与装配面：

1. **从不调 `tools/list`**。发送点只有 `initialize` / `notifications/initialized` / `tools/call` /
   `shutdown`（`client.go:135,164,202,247`）。后果可量化：codeseek 服务端 `all_tools()` 暴露 **9** 个工具
   （`codeseek/rust-core/src/mcp/tools.rs:13`、`server.rs:117,160`），Go 侧手写桥只接了 **6** 个
   （`internal/tools/repo_operations.go` 里只有 search/skeleton/snippet/callees/callers/callgraph）——
   `codeseek_init` / `codeseek_status` / `codeseek_list` 永远调不到。而 `codeseek_status` 恰好能回答
   "索引建到哪了"，现在只能靠 `ErrIndexBuilding` 那句中文提示让 Agent 自己猜（`client.go:19-21`）。
2. **`initialize` 的 `instructions` 被丢掉**：`InitializeResult` 没有这个字段
   （`internal/mcp/protocol.go:60-64`），而 codeseek 真的发了（`server.rs:196`，一整段工具使用说明）。
3. **单 server 硬编码**：`internal/app/app.go:194-231` 只构造 codeseek 一个客户端；配置面只有
   `[codeseek] binary_path / mcp_args / request_timeout`（`internal/config/config.go:860-864`）。
   用户没有任何"加一个自己的 MCP server"的入口。
4. **通知只打日志**：`notifications/tools/list_changed` 落到一条 Info（`client.go:417-426`），不重算工具面。
5. **崩了不会重来**：`CallTool` 开头 `IsAlive()` 为假就直接 error（`client.go:186-189`），
   无重连、无退避、没有健康状态上事件总线。
6. **协议类型只覆盖 tools**：`ServerCapabilities` 只有 `Tools`，`ClientCapabilities` 是空结构体
   （`protocol.go:51,67-74`）。
7. **结果解析靠猜形状**：`parseCallResult` / `parseSearchResult` 各试 3-4 种 unmarshal 形态
   （`repo_operations.go:185-260`），正是因为没有 `tools/list` 的输出契约可对。

### 1.3 顺带发现的三处漂移（本分支一并修）

| 漂移 | 事实 |
|---|---|
| `internal/toolspec/AGENTS.md:19-20` 写"条数基线**硬写 51**" | 测试里是 **49**（`internal/toolspec/registry_test.go:412-424`） |
| `internal/toolspec/spec.go:154-155` 注释称 `Validate()` "装配期调用：生产环境把问题打到日志" | 全仓只有 `internal/agents/registry_gate_test.go:28` 调它，**生产从不自检** |
| `internal/AGENTS.md:83-89` 记的 skills nil 解引用 | 未修，本稿 §6.6 修掉后同步该条 |

---

## 2. 目标与非目标

### 目标

1. 用户能在 `config.toml` 里加一个 MCP server，它的工具**自动**出现在 Agent 的工具面上，
   模型直接 `call` 而不是先查再拼 JSON。
2. codeseek 从"唯一特例"变成"第一个内建 server"，顺手接回漏掉的 3 个工具。
3. server 崩溃能重连，工具列表变了能重算，状态变化**可读**（日志 + 事件 + TUI）。
4. 技能变成 Agent 能自己发现并按需取回的能力：目录常驻、正文按需，复用 deferred 的成本模型。
5. 技能作用域改成合并，三个入口共用一份加载器，nil 崩溃修掉。
6. 所有"没接上"的情况都留下**一行能读出来的账**，不静默。

### 非目标（本稿明确不做）

- MCP 的 `resources` / `prompts` / `sampling` 三类协议面：yagent 目前没有消费方，
  加类型等于加第二份会漂走的事实（`ClientCapabilities` 保持空结构体，只在需要时再扩）。
- OAuth / HTTP+SSE 传输：只做 stdio。远端 server 用 `command = "npx"` 这类本地代理承接。
- 技能携带可执行脚本（`scripts/`）与 `allowed-tools` 的**强制**：本稿只解析并作为咨询性文本回吐（§6.5）。
  执行面是独立一次改动，要和 WorkspaceGuard / 授权回路一起设计。
- HTTP 侧新增 `/api/skills`、`/api/mcp` 端点：本稿只修"`runHTTP` 不加载技能"这个不对称（§6.6）。
- 运行中热加载技能文件或 server 配置：与 `internal/dict` 一致，改配置要重启（`internal/AGENTS.md` dict 一节）。

---

## 3. 总体形状

```
config.toml
  [[mcp.servers]] ──┐                        .yagent/skills/<name>/SKILL.md
  [codeseek]  ──────┤                        ~/.yagent/skills/<name>/SKILL.md
                    ▼                                  │
            internal/mcp.Manager                       ▼
            （多 server 生命周期 +               internal/skills.Registry
              tools/list 发现 + 重连）           （合并 + 来源账 + 目录渲染）
                    │                                  │
        []DiscoveredTool                        []Skill + Catalog()
                    ▼                                  ▼
        internal/tools.BuildExternalSpecs       toolspec: use_skill (静态, Hot)
                    │                                  │
                    ▼                                  ▼
            toolspec.Deps.ExternalSpecs  ──────►  tools.Surface.rebuild()
                                                   （静态画像匹配 + 动态追加）
                                                            │
                                                            ▼
                                        Defs()（每步重算）/ Catalog()（Cold 目录）
                                                            │
                                                            ▼
                            executor.go 的 systemPrompt += Surface.Catalog() + SkillCatalog
                            director.go 的 prompts hook  += 同上 + MCP 状态行
```

三个关键选择，每个都有既有代码作理由：

| 选择 | 理由 |
|---|---|
| 动态工具**不进**全局 `registry`，走 `Deps.ExternalSpecs` | §4.1 三条证据 |
| 外部工具一律 **Cold**，靠既有 `load_tool` 取 schema | deferred 目录已经会为 Cold 工具渲染 `name + Brief`（`internal/tools/surface.go:236-249`），不需要新机制 |
| 技能目录进 **system prompt 稳定前缀**，不进 messages | 与 deferred 目录同一条路（`internal/agents/executor.go:250-260`）；进 messages 的东西会被第二/三档压缩吞掉（根 `AGENTS.md` §11 坑 11） |

---

## 4. A. 动态工具规格（不进全局注册表）

### 4.1 为什么不进全局 registry

`toolspec.registry` 的注释写着"init() 期写入，之后只读"（`internal/toolspec/spec.go:73-78`）。
把它变成运行期可变会同时打穿三处：

1. **golden 会变成运行期状态的函数**。`TestAgentSurfacesGolden` 用 `p.ProbeDeps()` 渲染
   （`internal/agents/registry_gate_test.go:76-100`），探针不知道有哪些 server，
   于是 golden 一动不动 —— 正是根 `AGENTS.md` §11 坑 39 那个失效形状（"探针少绑一次 ⇒ 六个画像都没有这个工具"）。
2. **Meta 的工具池会被外部工具污染**。`renderCustomAgentToolPool` 直接遍历 `toolspec.Specs()`
   （`internal/agents/deps.go:83-104`），Custom Agent 于是能拿到第三方 server 的能力，
   而 `Palette` 的整个意义就是"Custom Agent 拿不到 Director 没有的能力"
   （`registry_gate_test.go:256-287`）。
3. **`Validate()` 的双向 1:1 会当场报错**。"已注册但 schemas.json 里没有对应声明"
   （`spec.go:162-166`）对每个外部工具都成立，而那条检查的价值恰恰在于它是启动错误。

所以：**静态注册表继续是 `init()` 期写入的只读事实，动态规格走另一条通道**，
两条通道在 `Surface.rebuild()` 汇合。这不是"两套实现"——`Spec` 类型、能力标签、档位、
guard 挂载、`Skipped()` 记账全部复用，只有"从哪来"不同。

### 4.2 `Spec` 自带 schema

外部工具的 description/parameters 来自 `tools/list`，不在 `schemas.json` 里。给 `Spec` 加一个字段：

```go
// Inline 是动态注册（外部 MCP 工具）自带的模型接口声明。
// 静态工具一律留空 —— 它们的 description/parameters 只在 schemas.json 里（§4.1 第 3 条）。
Inline *Schema
```

`Description()` 与 `Parameters()` 的取值顺序改成 **`Describe` 钩子 > `Inline` > `GetSchema`**
（`internal/toolspec/schemas.go:132-153`）。静态路径逐字不变（`Inline == nil`），
所以 golden 与 `TestSpecDescriptionFallbackChain`（`registry_test.go:472`）都不动。

### 4.3 命名与冲突

| 规则 | 判据 |
|---|---|
| 名字 = `mcp__<server>__<tool>` | 前缀让"这是外部工具"在日志、TUI、rollout 里一眼可辨；`__` 分隔符与静态名不可能撞（静态名全是单段 snake_case） |
| 字符集清洗：`[^a-zA-Z0-9_-]` → `_` | provider 的 function name 只允许 `[a-zA-Z0-9_-]{1,64}` |
| 超 64 字符 → 截到 55 + `-` + `sha256(原名)` 前 8 hex | 确定性、可复现，与 `projectid.Compute` 同风格（`internal/projectid/projectid.go:24-44`） |
| server 名在配置里必须唯一 | `config.validate()` 报错，不静默后者覆盖前者 |
| 与静态注册表同名 → **静态胜**，动态项记进账 | `Surface.rebuild()` 追加时判重（`surface.go:103-128`），理由同 `Surface.Add()` 的既有防御（`:390-396`） |
| 同一个 server 内同名 → 不可能（`tools/list` 的 name 是主键），仍按"先到先得 + 记账"处理 | 防御性 |

### 4.4 能力标签：两个新 Effect 位

`Effect` 位掩码今天只用到 `1<<25`（`internal/toolspec/effect.go:47-49`），余量充足：

```go
const (
    EffectExternalRead  Effect = 1 << 26 // 外部 server 的只读工具
    EffectExternalWrite Effect = 1 << 27 // 外部 server 的写类工具
    EffectSkill         Effect = 1 << 28 // use_skill
)
```

`EffectExternalWrite` **必须**加进 `writeEffects` 与 `authEffects`（`effect.go:51-62`），
于是"`Mutates=false` 的写工具"和"不过授权的危险工具"这两条既有校验自动覆盖外部工具
（`spec.go:181-187`）——不需要为外部工具另写一套安全规则。

**副作用判定来源**（按优先级）：

1. `tools/list` 的 `annotations.readOnlyHint == true` ⇒ `EffectExternalRead`、`Mutates=false`、`Serial=false`（可并发）。
2. `annotations.destructiveHint == true` 或 `readOnlyHint == false` ⇒ `EffectExternalWrite`、`Mutates=true`、`Serial=true`、`NeedsAuth=true`。
3. **没有 annotations（今天的绝大多数 server，含 codeseek）⇒ 一律按写处理**（fail-safe，
   与 `queries.go:1-16` "未注册名字一律不可并发"同一个取向）。

第 3 条的代价是"只读的第三方工具也要授权、也不能并发"。这是刻意的：判错的后果是模型多问一次，
判对的后果是第三方 server 拿到一个不受守卫的写入口（`spec.go:181-184` 那条校验存在的理由）。
想放宽就写配置（§7.1 的 `allow_write` 与 `read_only` 名单），**放宽必须是显式意图**。

### 4.5 画像矩阵

| 画像 | `EffectExternalRead` | `EffectExternalWrite` | `EffectSkill` |
|---|---|---|---|
| director | ✅ | ❌（`Deny: EffectFSWrite \| EffectShell` 的同一取向：Director 绝不直接产生副作用） | ✅ |
| coding | ✅ | ✅（仅当 server 配了 `allow_write = true`） | ✅ |
| repo | ✅ | ❌（只读审计员） | ✅ |
| chat | ❌（无文件系统能力的画像也不该有外部 IO） | ❌ | ✅ |
| devops | ✅ | ❌（它已经有 `run_bash`，外部写工具是重复且更难审计的入口） | ❌ |
| browser | ❌（工具面刻意只有 14 个浏览器动作 + flow） | ❌ | ❌ |

`ProfileDirector.Palette` **不加**任何外部位：Custom Agent 拿不到第三方 server 的能力，
理由照抄 `profiles.go:31-33` 那条（"扩它属于行为变更，需要单独签字"）。

这张表的 diff 就是评审物：改它必然让 `testdata/tool_surfaces.golden` 变红（§10）。

### 4.6 档位：一律 Cold

外部工具默认 `Frequency: Cold`，`Brief` = 该工具 description 的第一句、按 40 rune 上限截断
（`spec.go:150-152` 的 `maxBriefRunes`）。于是它们自动出现在既有 deferred 目录里，
模型用既有 `load_tool`（一批最多 8 个）取回 schema，`materialized` 只增不减、
`/resume` 靠 `RestoreMaterialized` 接续（`surface.go:420-448`）——**零新机制**。

预算降级（`applyBudget`，`surface.go:132-146`）只动 Warm，所以 Cold 的外部工具不会被"再降一次"；
但它们的目录行会计入 `estimatedTokens`（`:178-188` 对未 materialize 的 Cold 只算
`len(name) + len(Brief) + 8`）。**外部工具太多会把 Warm 挤成 Cold**，这是期望行为
（目录比声明便宜一个数量级），但要有上限：`[mcp] max_tools_per_server`（默认 40，§7.1），
超出的按名字排序截断并在账里写明。

一处必须改的既有代码：`catalogBrief` 先查 `toolspec.Brief(name)`（静态注册表），
动态工具查不到会回落到"adapter description 的第一句"（`surface.go:537-553`）。
回落路径能用，但会让 `Spec.Brief` 这个字段对动态工具形同虚设。改法：`Adapter` 带上 `brief`
（`rebuild`/`Add` 从 `spec.Brief` 填），`catalogBrief` 的取值顺序改成
`ad.brief > toolspec.Brief(name) > description 首句`。

### 4.7 可用性 = 健康状态，不可用要留话

动态 `Spec` 的 `Available` 钩子返回 Manager 里该 server 的当前状态：

```go
Available: func(*toolspec.Deps) (bool, string) {
    if !health.Ready(server) { return false, health.Reason(server) } // "server github 未连接: exit status 1（第 2/3 次重连）"
    return true, ""
}
```

不可用的工具会被 `rebuild` 记进 `Skipped()` 并由 `surfaceFor` 打成一条汇总 warn
（`internal/agents/deps.go:42-48`）。这条既有通道就是"为什么这个工具没了"的答案，
不需要新日志。

**但 `Skipped()` 只进日志，模型看不见**（`read_blueprint` 的教训是"不静默摘工具"，
根 `AGENTS.md` §6.4）。所以另外注入**一行 MCP 状态**进 system prompt（§8）：

```
## MCP servers
- codeseek: ready (9 tools, 6 curated + 3 external)
- github: offline (exit status 1, retry 2/3) — its 12 tools are unavailable this run
```

一行、有上限、四态可辨（ready / degraded / offline / disabled），与蓝图的形状②同一套纪律。

---

## 5. B. MCP：协议面补全 + Manager 生命周期

### 5.1 协议面补全（`internal/mcp/protocol.go`）

| 补什么 | 为什么 |
|---|---|
| `ListToolsResult{Tools []DiscoveredTool}`；`DiscoveredTool{Name, Description, InputSchema map[string]interface{}, Annotations *ToolAnnotations}` | `tools/list` 的返回契约，取代 `repo_operations.go:185-260` 那种"试四种形状" |
| `InitializeResult.Instructions string` | codeseek 真的发了（`server.rs:196`），今天被丢；收下后作为该 server 的说明进 `/mcp` 面板与状态账 |
| `ServerCapabilities.ListChanged` 已有（`protocol.go:71-74`），但没人读 | Manager 据它决定要不要监听 `notifications/tools/list_changed` |
| `ToolAnnotations{Title, ReadOnlyHint, DestructiveHint, IdempotentHint, OpenWorldHint}` | §4.4 的副作用判定来源；缺省 = 按写处理 |
| `ListToolsParams{Cursor}` + 分页循环 | MCP 的 `tools/list` 允许 `nextCursor` 分页；不循环就会静默少工具（与"静默失效"头号特征同族） |
| 客户端 `Capabilities` 保持空结构体 | 我们不消费 resources/prompts/sampling，声明支持就是撒谎 |

`ClientCapabilities` 不改（`protocol.go:51`）：MCP 允许客户端零能力。

### 5.2 `Manager`：多 server 生命周期

新文件 `internal/mcp/manager.go`（传输层 `client.go` 一行不动）：

```go
type ServerConfig struct {           // 来自 config，Manager 不读 config（依赖方向：app → mcp）
    Name, Command string
    Args          []string
    Env           map[string]string
    WorkingDir    string
    RequestTimeout time.Duration
    Enabled       bool
    AllowWrite    bool
    Tools         []string  // 空 = 全部；非空 = 白名单
    Exclude       []string
    MaxTools      int
}

type ServerState int  // StateDisabled / StateStarting / StateReady / StateOffline

type Manager struct {
    servers map[string]*serverEntry   // entry = *MCPClient + state + tools + instructions + 重连记账
    ...
}

func (m *Manager) Start(ctx) error                     // 全部 server 并发启动，不阻塞（照抄 app.go:213-230 的异步形状）
func (m *Manager) WaitForReady(ctx) error              // 有界等待：startup_timeout
func (m *Manager) DiscoveredTools() []DiscoveredTool   // 只返回 Ready 的 server，带 server 归属
func (m *Manager) Call(ctx, server, tool string, args map[string]interface{}) (*ToolCallResult, error)
func (m *Manager) Health() []ServerHealth              // 状态行渲染的唯一来源
func (m *Manager) Close()                              // 逐个 Shutdown，Drain 报名字（照抄 app.go:852-881）
```

**状态机与重连**：

```
disabled ──(enabled=false)                       不进任何面，状态行写明"disabled by config"
starting ──(Start + WaitForReady 成功)──► ready
ready    ──(readLoop EOF / IsAlive=false)──► offline ──(退避重连成功)──► ready
offline  ──(重连次数用尽)──► offline(终态)，状态行写"gave up after N attempts"
```

- 重连 goroutine 一律 `supervise.From(ctx).Go("mcp/reconnect-"+name, …)`：
  裸 `go` 全仓硬禁（`scripts/check_no_bare_go.sh`），且 `Close()` 要能 join 并报名字。
- 退避：`reconnect_backoff` 起，指数翻倍，上限 `5m`，次数 `max_reconnects`（默认 3）。
- **重连成功后重新 `tools/list` 并 diff**：工具集变了就打一条账（`+a +b −c`）并发事件（§5.4）。
- 崩溃检测不轮询：`readLoop` 的 defer 已经把 `alive` 置 false（`client.go:364-374`），
  Manager 在 `Call` 失败与 `Health` 求值时顺带检查，另有一条 `mcp/watch-<name>` 租约等 `WaitForReady`
  （照抄 `app.go:221-229`）。

**运行中 server 上线的可见性**：`Surface` 每次 Run 重建（8 个构造点全走
`internal/agents/deps.go:36`），所以"重连成功"在**下一次委派**生效，不是下一步。
这是刻意边界，写进 §12。

### 5.3 codeseek 收编：curated 优先

codeseek 变成 Manager 里的一个内建 server（由 `[codeseek]` 构造，配置键**一个不删**，
否则 `removedKeyHints` 会点名告警、老配置会漂）：

| 规则 | 理由 |
|---|---|
| 6 个手写桥（`semantic_search` 等）**原样保留**，包括 `ErrIndexBuilding` 的降级文案（`client.go:19-21`、`repo_operations.go:154-160`） | 它们做的是"结果整形 + 降级话术"，不是透传；换成通用透传会让 Repo-Agent 的行为回归 |
| 这 6 个的**服务端名**（`codeseek_search` 等）进 curated 名单，动态注册时排除 | 同一个能力出现两个名字，模型会随机选一个，而两边的结果形状不同 |
| 剩下的 `codeseek_init` / `codeseek_status` / `codeseek_list` 走通用路径，成为 `mcp__codeseek__status` 等 | 用内建 server 验证通用路径，而不是拿用户的第三方 server 当试验田 |
| `instructions` 收进状态账与 `/mcp` 面板 | 它本来就是"这个引擎怎么用"的权威说明 |

curated 名单是 `internal/tools/mcp_specs.go` 里的一个显式常量表（`map[server]map[toolName]bool`），
**只此一处**，并有测试断言"名单里的每个名字都真的存在对应静态工具"（否则名单会烂在那里）。

### 5.4 事件与账目

新增一个事件类型 `mcp_server_status`，payload：
`{server, state, tools, curated, external, instructions_first_line, error, attempt, max_attempts}`。

发布点：状态迁移（starting→ready / ready→offline / offline→ready / gave-up）、
`tools/list` diff 非空。**SSOT 是 `protocol/agent-events.yaml`**，而生成器与两份 Go 产物已经漂移
（根 `AGENTS.md` §6.6 那条警告），所以按既有可行做法：改 yaml →
只覆盖 `ts/agent-events.ts`、`ts/render-mapping.ts`、`agent-events.schema.json` →
把生成器**新增的那几块**逐字粘进 `protocol/go/agent-events.go` 与 `internal/protocol/agent_events.go`。
这一步是本设计里唯一需要碰生成物纪律的地方，W6 单独列。

账目一律"一行、可归堆"：

```
mcp: 2 servers, 12 tools (9 external + 3 curated-excluded), 1 offline
  github: offline — exit status 1 (attempt 2/3); 12 tools unavailable
  codeseek: ready — 9 tools; 6 curated (semantic_search…), 3 external (mcp__codeseek__status…)
```

同一段文本三处出口：启动日志、`/mcp` 面板、system prompt 的状态行（§4.7）。
**一个字符串三处用**，不各渲染一份（`FormatForDirector` 与便签尾注同源是既有先例，根 `AGENTS.md` §6.4）。

---

## 6. C. Skills：磁盘约定、合并语义、use_skill

### 6.1 磁盘约定（两种都认）

| 形态 | 判定 | 说明 |
|---|---|---|
| `<dir>/<name>/SKILL.md` | 目录 + 固定文件名 | 新约定；技能可以带 `references/`、`assets/` 等同级文件（本稿不解释它们，只是**不删不挡**） |
| `<dir>/<name>.md` | 扁平文件 | 兼容既有用户与 `skills_test.go` 的夹具 |

`name` 取目录名或文件名去后缀；两者同名时**目录式胜**（它更完整），并记进账。
目录深度只扫一层（不递归 `references/` 去找更多 SKILL.md）。

### 6.2 front-matter：JSON 优先，扁平 key: value 容错

仓库里没有 YAML 依赖（`go.mod` 只有 `BurntSushi/toml`），而便签的 front-matter 早就是
**JSON + `---` 分隔**（`internal/blackboard/note.go:14-33,238-256`）。技能沿用同一形状：

```markdown
---
{
  "name": "code-review",
  "description": "对一段 diff 做安全与可读性审查",
  "allowed-tools": ["read_file", "search_by_regex"],
  "arguments": ["diff 或 PR 链接"]
}
---
正文……
```

同时容错**扁平 YAML 子集**（从其他生态导入的技能几乎都是这个形状）：

```markdown
---
name: code-review
description: 对一段 diff 做安全与可读性审查
allowed-tools: read_file, search_by_regex
---
```

解析规则（`internal/skills/frontmatter.go`，**不是 YAML 解析器**，注释里要写明）：
只认 `key: value` 与 `key: [a, b]` / `key: a, b` 两种，不认嵌套、不认多行、不认锚点。
认不出的行 → `slog.Warn` 一行 + 记进账，**不静默丢**。

三档回落，每档都留话：

1. JSON front-matter 解析成功 → 用它。
2. 扁平 key: value 解析出至少 `description` → 用它，账里标 `frontmatter: yaml-subset`。
3. 都没有（或 `---` 块解析失败）→ 沿用今天的行为：`description` = 第一行去 `#`，账里标 `frontmatter: none`。
   既有 6 个测试用例走的就是这档，逐字不变。

### 6.3 合并语义 + 来源账

加载顺序：`~/.yagent/skills`（用户级）→ `.yagent/skills`（项目级）→ `[skills] dirs`（配置追加）。
**后者覆盖前者**，与今天"整体替换"相反（`main.go:234`）。

```go
type LoadAccount struct {
    Total      int
    BySource   map[string][]string // "user" / "project" / "extra:<dir>" → 技能名
    Overridden []Override          // {Name, Winner, Loser, Reason}
    Rejected   []Rejected          // {Path, Reason}：读不出、front-matter 坏、重名同层
}
```

账目一行进启动日志 + `/skills` 面板：

```
skills: 7 loaded (project: 4, user: 3), 1 overridden (code-review: project ▸ user), 0 rejected
```

**Rejected 逐条可读**是硬要求（同 `Surface.Skipped()` 的理由，`internal/agents/deps.go:34-48`）：
"我的技能怎么没生效"必须有一行答案。

### 6.4 目录注入 + `use_skill`

**目录**（`Registry.Catalog(maxTokens) string`）：

```
## Skills
Reusable procedures written by the user. Call `use_skill` with the name to fetch the full body, then follow it.
- code-review: 对一段 diff 做安全与可读性审查
- release-notes: 从 git log 生成发布说明
(2 more truncated — call use_skill with the name directly)
```

- 每行 = `name: description`，description 截到 `catalog_line_chars`（默认 120 rune，同便签索引的取值风格）。
- 总预算 `[skills] catalog_max_tokens`（默认 600）。超出按名字排序保留前 N 条 + 一行截断标记
  （**截断必须写进正文**，蓝图的 `TRUNC` 四态是同一个教训，根 `AGENTS.md` §6.4）。
- 目录是**稳定前缀**：技能集在一次进程内不变，所以进 system prompt 而不是每轮重算的 messages。

**`use_skill`**（静态工具，`schemas.json` 第 50 条）：

| 字段 | 值 | 理由 |
|---|---|---|
| `Effect` | `EffectSkill` | §4.4 |
| `Frequency` | `Hot` | 目录已经在 system prompt 里点名它了，让它自己再 Cold 一次等于强迫模型先 `load_tool` 才能取技能（`load_tool` 必须 Hot 是同一个理由，`surface.go:569-593`） |
| `Mutates` / `Serial` | false / false | 只读本地文件 ⇒ 可并发 |
| `Trunc` | `TruncNormal` | 正文可重取，不该受保护 |
| `Display` | `DisplayFull` | TUI 展开正文，用户要看见技能内容 |
| 参数 | `name`(必填) `arguments`(可选字符串) `offset`/`limit`(可选分页) | 分页照 `read_note`/`read_blueprint` 的既有形状 |

返回：front-matter 事实（来源路径、`allowed-tools`、`arguments` 声明）+ 正文（按
`[skills] body_max_tokens`，默认 4000，超出给 `truncated: continue with offset=N`）。

**没有技能时不摘工具**：`use_skill` 恒可用，返回
`"no skills found — put them in .yagent/skills/<name>/SKILL.md or ~/.yagent/skills/"`。
这是 `read_blueprint` 那条决定的逐字复用（`internal/toolspec/deps.go:120-122`）。

### 6.5 参数与 allowed-tools：咨询性，不强制

- `arguments` 里的 `$1`/`$2`/`$ARGUMENTS` 做**文本替换**后回吐（用户从 TUI 传的 `arguments` 字符串）。
- `allowed-tools` 只作为一行事实回吐给模型（`This skill expects: read_file, search_by_regex`），
  **不改工具面**。真要强制就得动 `Surface` 的能力数学与 `Palette` 校验，那是 §2 非目标里那条独立改动。
  注释与文档都必须写明"不强制"，否则就是第二句空话（同 `enable_streaming` 那类"读了也没用"的键，
  根 `AGENTS.md` §11 坑 25）。

### 6.6 三个入口共用一份加载器 + 修 nil 崩溃

```go
// internal/skills
func Load(dirs []string) (*Registry, LoadAccount)   // 永不返回 nil Registry
```

- `Load` 内部对每个目录：不存在 → 跳过（不记账）；存在但读不出 → 记进 `Rejected`，**继续**。
  今天 `(nil, err)` + 调用点 `.Count()` 的 nil 解引用（`internal/AGENTS.md:83-89`）从签名上消失。
- 装配收进 `internal/app`：`Yagent.SkillRegistry` 由 `Init` 一处填（`internal/app/app.go:54`），
  `main.go` 那两份逐字相同的 17 行（`:224-241`、`:374-391`）删掉，`runHTTP` 因此**自动**获得技能
  （今天的不对称：HTTP 模式压根不加载）。
- `LoadSkills(dirPath)` 保留为薄封装还是删掉？删。它只有一个语义（单目录），而三个调用点都要合并语义；
  留着就是第二份会漂走的事实。`skills_test.go` 六个用例改成打 `Load([]string{dir})`。

### 6.7 TUI 侧

| 改动 | 判据 |
|---|---|
| `/skills` 命令 → 对话框列出 `name / description / 来源 / front-matter 档` | 复用 `components.DialogStack` + `common/list_metrics.go`；入栈方必须 `SetBounds`，`dialogListChrome` 要跟着行数走（`internal/tui/AGENTS.md` 硬约束 7、8） |
| `/mcp` 命令 → 对话框列出 server 状态账（§5.4 那段文本） | 同上 |
| 补全列表里内置命令与技能分开：技能行加 `skill` 角标，内置命令加 `cmd` 角标 | 今天两者混在一个 `[]string` 里靠硬编码特判（`tui_update_keys.go:971-1003`），撞名会静默吞掉技能 |
| 撞名时**内置命令胜** + 加载期一条 warn | 与 `tui_keymap` 抢键的既有规则同取向（根 `AGENTS.md` §11 坑 29） |
| 手打 `/code-review 看看这个 diff` + Enter **直接执行技能**，`arguments` = 去掉首个 token 的剩余文本 | 今天必须先让补全弹出来再按 Tab（`tui_update_commands.go:27-41` 的 `/` 分支只认显式列出的内置命令，其余当搜索）。规则：首个 token 精确命中技能名 → 执行技能；否则照旧搜索 |
| 弹窗守卫放行清单不动，但新对话框必须透传 `taskCompleteMsg` 并重新武装 `listenForEvents` | `internal/tui/AGENTS.md` 硬约束 1、2 |

---

## 7. D. 配置面

### 7.1 `[[mcp.servers]]` 与 `[mcp]`

```toml
[mcp]
enable = true                 # 键存在性判定；kill-switch 是 YAGENT_MCP=0
startup_timeout = "30s"       # 启动期 tools/list 的有界等待
reconnect_backoff = "5s"      # 重连起始退避，指数翻倍，上限 5m
max_reconnects = 3            # 用尽即终态 offline，状态行写 "gave up"
max_tools_per_server = 40     # 超出按名排序截断 + 记账
status_line = true            # 关掉 §4.7 那行 system prompt 注入

[[mcp.servers]]
name = "github"                       # 唯一；进工具名前缀
command = "npx"
args = ["-y", "@modelcontextprotocol/server-github"]
env = { GITHUB_PERSONAL_ACCESS_TOKEN = "..." }
enabled = true
request_timeout = 30                  # 秒；≤0 → 30（兜底只在 mcp.NewMCPClient 一处）
allow_write = false                   # 默认 false：写类工具不进任何画像（§4.4/§4.5）
read_only = []                        # 显式声明"这些工具虽然没 annotations 但只读"⇒ 免授权、可并发
tools = []                            # 空 = 全部；非空 = 白名单
exclude = []
```

约定与既有配置纪律一致：

- **`enable`/`enabled` 靠键的存在性判定**，不看零值（根 `AGENTS.md` §11 坑 26；
  `[browser]`/`[blackboard]`/`[resume]` 三处同一套办法）。
- `request_timeout` 的兜底**只在 `mcp.NewMCPClient` 一处**，`app` 与 Manager 都不许再抄一个 30
  （`internal/app/AGENTS.md` 硬约束 6）。
- 两份模板都要加这段：`internal/config/default_config.toml`（首启生成的就是它）与
  `config/config.toml`（人读的参考），且 `TestTemplatesDecodeCompletelyAndValidate` 要求
  **每个键都有 struct tag 消费方**（`internal/config/template_test.go:14-58`）。
- 模板注释用英文（既有模板通篇英文注释）。

### 7.2 `[skills]`

```toml
[skills]
enable = true                          # 键存在性判定；kill-switch 是 YAGENT_SKILLS=0
dirs = []                              # 追加目录；默认 user + project 两级已经在内
inject_into = ["director", "coding", "repo", "chat"]   # 谁的系统提示里出现目录
catalog_max_tokens = 600
catalog_line_chars = 120
body_max_tokens = 4000
```

`inject_into` 与 `[blueprint] inject_into` 同语义（关的是**常驻注入**，不关 `use_skill` 这个工具本身
——`read_blueprint` 的既有决定，`internal/app/AGENTS.md` 硬约束 5）。
`config.SkillsEnabled()` / `config.MCPEnabled()` 是各自机制的**唯一**启用判据，
写在 `Normalize*` 内部而不是调用点（`internal/config/AGENTS.md` 硬约束 8）。

### 7.3 启动自检终于落地

`app.Init` 里加一次 `toolspec.Validate()` + `toolspec.ValidateDynamic(externalSpecs)`，
问题打 `slog.Error` 并逐条列出（不 `os.Exit`：静态注册表的问题是构建期 bug，测试已经拦；
运行期退出会让"配置坏了"和"代码坏了"混成同一个症状）。
这把 `spec.go:154-155` 那句注释变成真话（§1.3）。

---

## 8. E. 注入点清单（逐处 file:line）

| # | 位置 | 加什么 |
|---|---|---|
| 1 | `internal/toolspec/deps.go:98-142` | `ExternalSpecs []*Spec`（动态规格）+ `Skills SkillSource`（窄接口：`Catalog(maxTokens int) string` / `Body(name string, off, lim int) (string, error)`） |
| 2 | `internal/tools/surface.go:103-128` | `rebuild()` 在静态匹配之后追加 `deps.ExternalSpecs`，同名判重、guard 统一挂、档位取 `spec.Frequency`（Cold） |
| 3 | `internal/tools/surface.go:537-553` | `catalogBrief` 取值顺序改为 `ad.brief > toolspec.Brief(name) > description 首句`（§4.6） |
| 4 | `internal/agents/deps.go:22-32` | `depsFor` 增两个参数（`ext ExternalProvider`、`skills SkillSource`）——**六个调用点少绑一个编译不过**，这是刻意的强制函数（同 `view *BlueprintView` 的理由，`:18-21`） |
| 5 | `internal/agents/executor.go:250-260` | `systemPrompt += cfg.SkillCatalog`（在 `Surface.Catalog()` 之后） |
| 6 | `internal/agents/director.go:909-936` | prompts hook 里 `+= a.skills.Resident()` 与 `+= a.mcpStatus.Line()`（紧挨 `a.blueprint.Resident()`） |
| 7 | `internal/app/app.go:162-231` | codeseek 客户端构造改成 Manager 的一个 server entry；`Init` 末尾 `Manager.Start` + 有界 `WaitForReady` |
| 8 | `internal/app/app.go:852-881` | `Close()` 次序：蓝图停 → **Manager 停** → `supervise.Root().Drain` 最后并报名字 |
| 9 | `main.go:224-241` / `:374-391` | 删（技能装配收进 `app.Init`，§6.6） |
| 10 | `internal/toolspec/schemas.json` | +1 条 `use_skill`；基线 49 → **50**（`registry_test.go:412-424` 与 agents 侧 gate 同步改） |
| 11 | `internal/tui/tui_update_commands.go:27-41` | `/skills`、`/mcp` 显式列进 `/` 分支；技能名精确命中 → 执行技能 |
| 12 | `protocol/agent-events.yaml` + 两份 Go 产物 | `mcp_server_status`（按 §5.4 的手工移植纪律） |

**不改**的：`toolbatch`（并发判据仍只看 `IsReadOnly`，外部工具经 §4.4 派生）、
`WorkspaceGuard`（外部工具没有路径参数，`NeedsAuth` 走"无 PathParams 的危险操作授权"那条既有分支）、
`repo_operations.go` 的 6 个桥（§5.3）、`Surface.Add()`（Meta 的 Custom Agent 专用，动态规格不走它）。

---

## 9. F. 校验、账目与静默失效防线

这个仓库的头号病害是"错了不报错"（根 `AGENTS.md` §11 坑 25/33/38/40/42/43）。
本设计每一道拒收都必须留一行可读的账：

| 失败形状 | 今天的表现 | 本设计 |
|---|---|---|
| server 起不来 | `slog.Warn` 一条，模型侧工具凭空消失（`app.go:216`） | 状态行 + `Skipped()` 汇总 + `/mcp` 面板 + `mcp_server_status` 事件 |
| `tools/list` 失败但进程活着 | 不可能发生（今天压根不调） | 状态 `degraded`，账写"进程在但没拿到工具清单"，工具**不注册**（宁缺毋滥） |
| server 崩了 | 每次调用返回 "MCP client is not alive"（`client.go:186-189`） | Manager 重连；重连期间状态 `offline` + 状态行；用尽写 "gave up after N" |
| 外部工具与静态工具同名 | 不可能发生 | 静态胜 + 账一行 |
| 外部工具太多挤爆目录 | 不可能发生 | `max_tools_per_server` 截断 + 账一行（截断写进正文，不是只写日志） |
| 技能 front-matter 坏 | 不可能发生（今天不解析） | `Rejected` 逐条 + 回落到"第一行当 description" |
| 技能重名 | 不可能发生（今天整体替换） | `Overridden` 逐条（谁盖了谁） |
| 技能目录读不出 | **nil 解引用崩溃**（§1.1） | `Rejected` 一行，继续加载其余目录 |
| 注册表自检失败 | 生产从不检查（§1.3） | 启动 `slog.Error` 逐条（§7.3） |

一条通用纪律照抄蓝图的账目出口规则：**同一个账目字符串，多处出口共用一份实现**
（§5.4 那三处、§6.3 那两处）。各渲染一份的结果是一边漂走（根 `AGENTS.md` §6.4）。

---

## 10. G. 测试闸门与变异清单

### 10.1 必须改的既有闸门

| 闸门 | 改动 |
|---|---|
| `TestSchemasLoadAndDescribeEveryEntry`（`registry_test.go:417-424`） | 49 → **50**，注释加一行 "49 → 50：技能读通道 use_skill" |
| `TestAgentSurfacesGolden`（`registry_gate_test.go:48-69`） | `-update` 重写；diff 必须只多出 `use_skill`（外部工具**不进** golden，§4.1 第 1 条） |
| `TestDerivedMetadataMatchesLegacyTables`（`:110-229`） | `use_skill` 进 `signedReadOnlyAdditions`（只读本地文件、无共享写端，理由照 `read_blueprint` 那条写，`:139-141`） |
| `TestProbeDepsIsCapabilityComplete`（`toolspec/profile_test.go:420`） | `testDeps()` 必须绑 `Skills` 探针 —— 否则 golden 里 `use_skill` 会**静默消失**（坑 39 的原形状） |
| `TestMetaPromptToolPoolMatchesPalette`（`registry_gate_test.go:292-312`） | `use_skill` 不在 `Palette` 里 ⇒ 不该出现在工具池表格；这条测试会自动验证 |
| `internal/skills/skills_test.go` 六个用例 | 改成 `Load([]string{dir})`，扁平 `.md` 那档断言逐字保留（§6.2 第 3 档） |
| `internal/config/template_test.go` | 两份模板加新段后仍须完全解码（每个键都有消费方） |

### 10.2 新增闸门

| 测试 | 钉住什么 | 变异（改回去必须红） |
|---|---|---|
| `TestStaticRegistryNeverCarriesExternalTools` | `toolspec.Names()` 里没有 `mcp__` 前缀 | 把动态规格塞进全局 registry ⇒ 红 |
| `TestBuildExternalSpecs_NamingAndCharset` | 名字前缀、清洗、>64 截断 + hash 确定性 | 去掉 hash 后缀 ⇒ 两个长名工具撞成同一个 |
| `TestBuildExternalSpecs_NoAnnotationsIsWrite` | 没 annotations ⇒ `Mutates`/`Serial`/`NeedsAuth` 全真 | 改成默认只读 ⇒ 红（安全默认被翻掉） |
| `TestBuildExternalSpecs_ReadOnlyHintIsParallel` | `readOnlyHint=true` ⇒ `IsReadOnly()` 为真 | — |
| `TestSurface_ExternalSpecsAreColdAndListed` | 外部工具进目录、不进 `Defs()`，`load_tool` 取回后进 `Defs()` | 把默认档位改 Hot ⇒ 红 |
| `TestManager_ReconnectsAndRediscovers` | 假 server 崩 → 重连 → `tools/list` 重跑 → 工具集恢复；diff 非空时发事件 | 去掉重连 ⇒ 红 |
| `TestManager_PaginatesToolsList` | `nextCursor` 两页都收 | 只读第一页 ⇒ 红（静默少工具） |
| `TestManager_CuratedWinsForCodeseek` | codeseek 的 6 个桥不产生 `mcp__codeseek__*` 重名工具，另外 3 个产生 | 删 curated 名单 ⇒ 红 |
| `TestE2EExternalToolReachesModel` | 真实 dispatcher + 假 MCP server + 脚本化 LLM：模型 `load_tool` → 调外部工具 → 结果回到对话 | 漏接 `Deps.ExternalSpecs` ⇒ 红（e2e 是"文件出现"级断言，照 `e2e_blueprint_test.go`） |
| `TestSkillsLoad_MergesAndAccounts` | user + project 合并、覆盖记账、坏 front-matter 进 Rejected | 改回"整体替换" ⇒ 红 |
| `TestSkillsLoad_UnreadableDirDoesNotPanic` | nil 崩溃回归 | 恢复 `(nil, err)` ⇒ 红 |
| `TestSkillCatalogRespectsBudget` | 超预算截断 + 截断标记进正文 | 去掉标记 ⇒ 红 |
| `TestUseSkill_NoSkillsExplainsWhy` | 空目录时返回修复指引而不是错误 | — |
| `TestTUI_SlashSkillRunsFromTypedText` | 手打 `/name args` + Enter 执行技能，`arguments` = 剩余文本 | 只认高亮项 ⇒ 红 |

假 MCP server 一律走既有的"重新执行测试二进制"路子（`client_process_test.go:22-33` 认
`YAGENT_TEST_FAKE_MCP_SERVER`），新增模式：`tools-list` / `tools-list-paged` / `crash-after-list` /
`list-changed` / `slow-list`。**不要**改成 `go run`（会把编译时间算进单测，`-race` 下起不了进程）。

耗时纪律：重连退避必须有 seam（照 `datamanager.taskFlushDebounce`、`agents.stepRetryBackoffBase`），
测试不许 `time.Sleep` 等退避。

---

## 11. H. 实施顺序（W1–W7）

一条分支落完，不分批上线；每个工作包自带验收断言，做完即可 `bash scripts/check_all.sh` 绿。

| 包 | 内容 | 验收断言 |
|---|---|---|
| **W0** | 三处漂移修复（§1.3）+ 启动自检落地（§7.3） | `toolspec/AGENTS.md` 与测试基线同源；`Validate()` 在 `app.Init` 被调用（一条测试断言调用发生） |
| **W1** | `toolspec`：`Spec.Inline`、三个 Effect 位、`ValidateDynamic`、画像矩阵、`use_skill` schema | `TestStaticRegistryNeverCarriesExternalTools`、golden diff 只多 `use_skill`、基线 50 |
| **W2** | `internal/mcp`：协议面补全（`tools/list` 分页、`instructions`、annotations）+ `Manager` 状态机与重连 | `TestManager_*` 五条全绿；`client.go` 传输层 diff 为零 |
| **W3** | `internal/tools/mcp_specs.go`：动态规格构造（命名、档位、Effect、curated 名单）+ `Surface.rebuild` 追加 + `catalogBrief` 改造 | `TestBuildExternalSpecs_*`、`TestSurface_ExternalSpecsAreColdAndListed` |
| **W4** | 装配：`Deps.ExternalSpecs`/`Skills`、`depsFor` 加参、`app.Init` 起 Manager、`Close` 次序 | `internal/agents/e2e_*` harness 同步（它照抄 `app.go:112-292` 的接线顺序，`internal/app/AGENTS.md` 硬约束 1）；`TestE2EExternalToolReachesModel` |
| **W5** | `internal/skills`：目录约定 + front-matter + 合并 + 账 + `Catalog`/`Body` + `use_skill` 执行器；`main.go` 两份重复删掉 | `TestSkills*` 五条；`runHTTP` 也有技能（一条断言） |
| **W6** | 注入与事件：executor/director 两处 system prompt、`mcp_server_status`（yaml + 手工移植）、状态行渲染单源 | 一条测试断言"状态行与 `/mcp` 面板与日志是同一个字符串" |
| **W7** | TUI：`/skills`、`/mcp` 两个对话框 + 补全分栏 + 手打斜杠执行技能；宽度矩阵测试 | `TestTUI_SlashSkillRunsFromTypedText`、新对话框进 `TestView_ExactHeight` 系列与 `dialogListChrome` 对账 |

依赖顺序是硬的：W1 → W3 → W4（Deps 字段先有，构造器才能填，装配才能接）；W2 与 W1 可并行；
W5 只依赖 W1 的 `EffectSkill`；W6 依赖 W4+W5；W7 依赖 W6。

---

## 12. I. 已知边界与刻意不做的事

1. **server 中途上线要到下一次委派才可见**。`Surface` 每次 Run 重建（8 个构造点，
   `internal/agents/deps.go:36`），运行中不重算工具面。改这个需要"运行中换工具面"的能力，
   而工具列表来回抖动会让 provider 前缀缓存失效（`surface.go:16-19` 的既有理由）。
2. **`tools/list_changed` 不触发热重算**，只在下次重建时生效 + 立刻打一条账与事件。
3. **外部工具没有 `PathParams`**，所以 `WorkspaceGuard` 的边界检查对它们不适用；
   写类外部工具的防线是 `NeedsAuth`（用户授权）而不是路径检查。想给某个外部工具做路径守卫，
   需要在 server 配置里显式声明参数名——本稿不做（§2 非目标）。
4. **`/resume` 时外部工具集可能已经变了**：`RestoreMaterialized` 只恢复还存在的名字
   （`surface.go:434-448` 已经这么做了），消失的工具静默不恢复。这**不**进 `assessReplay`
   的降级判据（那八条判的是"这份重放可不可信"，而工具集变化不影响已发生的事实）。
5. **技能正文可以很大**：`body_max_tokens` 截断 + `offset` 分页，不做 LLM 摘要
   （摘要是 artifact 那条路的活，`internal/artifact`）。
6. **`allowed-tools` 不强制**（§6.5）。写进文档与代码注释，别让它变成第二句空话。
7. **codeseek 会往仓库的 `AGENTS.md`/`CLAUDE.md` 尾部写注入块**
   （`codeseek/rust-core/src/mcp/server.rs:12-37` 的注入常量 + `:436` 的 `maybe_inject_mcp_guidance`）。
   本设计不改这个行为，但 `/mcp` 面板要写明"该 server 会修改仓库文件"——
   用户有权知道一个"只读代码引擎"在写他们的 `AGENTS.md`。
8. **不做 server 级 token 配额**：外部工具结果进对话后由既有三档压缩兜（`internal/compression`），
   截断优先级一律 `TruncFirst`（大体积、可重取）。

---

## 13. J. 文档同步清单

| 文件 | 改什么 |
|---|---|
| `AGENTS.md` §3 模块地图 | `internal/mcp` 一行改成"stdio JSON-RPC 客户端 + 多 server Manager + codeseek 桥"；`internal/skills` 改成"技能注册表：目录约定 + front-matter + 合并 + use_skill 读通道" |
| `AGENTS.md` §5.2 工具总览 | 表里加 `use_skill`；"49 条"改"50 条 + 运行期动态外部工具（不进静态基线）" |
| `AGENTS.md` §5.3 MCP 与 codeseek | 整节重写：从"只有一个 MCP server、没有通用多 server 配置"改成 `[[mcp.servers]]` + curated 规则 + 重连 |
| `AGENTS.md` §6.4 上下文与知识 | 加 Skills 一段（目录常驻 + 正文按需 + 账目出口） |
| `AGENTS.md` §7 配置系统 | 顶层表加 `[mcp]` / `[[mcp.servers]]` / `[skills]`；声明面基线 49 → 50 |
| `AGENTS.md` §9.3 新增工具步骤 | 加一条"外部工具走动态通道，不进 schemas.json，判据见本设计 §4" |
| `AGENTS.md` §11 | 新增坑：**"动态规格不进全局注册表"的三条理由**（§4.1），以及"没 annotations 一律按写处理"的 fail-safe |
| `internal/toolspec/AGENTS.md` | 修 51 → 49→50 的基线漂移；加"静态 registry 只读、动态走 `Deps.ExternalSpecs`"这条硬约束 |
| `internal/AGENTS.md` | mcp 一节从"从不调 tools/list"改成"调，且分页"；skills 一节的 nil 崩溃条目删掉（已修），换成合并语义与账目 |
| `internal/tools/AGENTS.md`、`internal/app/AGENTS.md`、`internal/tui/AGENTS.md` | 各加本设计引入的硬约束（`catalogBrief` 取值顺序、`Close` 次序含 Manager、两个新对话框的 `dialogListChrome` 对账） |
| `docs/config-guide.md` | 三段新配置的键表；**`[skills]` 的 `allowed-tools` 必须写明"不强制"** |
| `README.md` / `README_zh.md` | 技能那段（`:214`、`:301` / `:291`）从"扁平 .md 当斜杠命令"改成目录式 + 模型可自己发现；MCP 那段加"可接第三方 server" |
| `docs/ARCHITECTURE.md` / `.en.md` | 仍然整体过期（根 `AGENTS.md` §11 坑 1），本稿只在 MCP/skills 两节做最小更正，不做大修 |
