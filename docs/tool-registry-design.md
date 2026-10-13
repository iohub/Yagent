# 工具注册表重构 + Deferred Tool Schema 设计方案

> **本文是实施规范**，读者是执行改动的 Coding-Agent。
> 事实基础见 `docs/tool-registration.md`（现状逐行核对，本文所有"今天如何"的断言都以那份文档的 `file:line` 为准）。
> 核对时间：2026-10-13。

---

## 0. 决策记录（已定，不再讨论）

| # | 决策点 | 选定方案 |
|---|--------|----------|
| D1 | schema 存放 | `internal/agents/tools.json` → **搬到 `internal/toolspec/schemas.json`**，由 leaf 包 `//go:embed`；Go 侧 Spec 提供能力标签/执行器/频率档，注册表启动即校验 1:1 |
| D2 | Agent 工具面表达 | **能力画像自动匹配**：Agent 声明 `Profile{Grant, Deny, Palette}`，注册表按 `Effect` 标签发放；不再有 per-agent 名单，也不再有 `lookupToolFunc` |
| D3 | 冷工具 schema 取回 | **`load_tool` 元工具 + 直调兜底**：system prompt 里放冷工具目录（name + 一句话），模型调 `load_tool(names[])` 取全量 schema；若模型跳过直接调冷工具，不返回 "not found"，而是把 schema 塞进错误消息并当场 materialize |
| D4 | 落地节奏 | **一次性重构到位**（单个落地批次，内部仍按 §10 的次序执行并逐步验证） |

D2 与 D4 是本方案里风险最高的两项，因此 §8 的一致性闸门（golden surface + 注册表校验）是**硬约束**，不是可选项。

---

## 1. 目标与非目标

### 目标

1. **单一注册表**：一个工具的名字、描述、参数 schema、能力标签、执行器、可用性条件、频率档，**只在一处声明**。
2. **消灭手工 lookup**：删除 5 个 per-agent switch（`coding.go:209`、`repo.go:91`、`chat.go:63`、`devops.go:70`、`director.go:262`）+ Director 的第二套 `getToolFunc`（`director.go:571`）+ 4 个就地 appender。
3. **消灭 8 张按名索引的元数据表**（背景文档 §5）：`knownReadOnlyTools`、`nonParallelToolNames`、`dangerousTools`+`pathParamNames`+`Check` 的 switch、`toolTruncationPriority`、`isInteractiveUserTool`、`[tools.llm]` 硬编码四槽、TUI `skipBodyTools`/`formatToolParams` 的按名分支、`browserToolSpecs` 的独立对账。全部由 Spec 的能力标签派生。
4. **静默失效 → 启动即失败**：声明与实现不一致、Spec 缺字段、能力标签自相矛盾，一律在 `Registry.Validate()` 里变成错误（生产 `slog.Error` + 拒绝启动该工具，测试直接 `t.Fatal`）。
5. **压低每步 token**：低频工具只暴露 `name + 一句话`，选中后再拉全量 schema。实测收益见 §7.6。

### 非目标

- 不改 `Adapter.Call()` 的执行语义（JSON 解析 → guard → fn → `json.Marshal`）。
- 不改 `toolbatch` 的调度算法（它继续吃一个 `IsReadOnly` 谓词）。
- 不引入插件式/外部工具加载（MCP 仍只有 codeseek 一个 server，仍走手写桥接）。
- 不动 `internal/llm` 的引擎与路由。
- 不做 `go mod tidy`（`go-rod/rod` 是未决项）。

---

## 2. 包结构

```
internal/toolspec/          新增。leaf 包：只 import stdlib + internal/llm
  spec.go        Spec / Effect / Frequency / Validate
  caps.go        Effect 位掩码常量 + 派生查询（IsReadOnly/IsSerial/NeedsAuth/...）
  profile.go     Profile{Agent, Grant, Deny, Palette, Promote, Demote}
  registry.go    Register / MustRegister / Get / Specs / Match / Validate
  deps.go        Deps 接口 + 各窄接口（FileOps/SearchOps/.../Guard/Delegator）
  schemas.go     //go:embed schemas.json + 解析 + 与 Spec 集合双向 1:1 校验
  surface.go     Surface：deferred schema 的纯状态机（可脱离 Agent 单测）
  toolspec_test.go / registry_test.go / surface_test.go / golden_test.go
  testdata/tool_surfaces.golden

internal/tools/
  adapter.go     基本不变；readOnly 改为构造时从 toolspec 派生
  build.go       BuildDeps（实现 toolspec.Deps）+ Build(profile, deps)
  specs_*.go     每个工具的实现文件里 toolspec.MustRegister(Spec{...})
  workspace_guard.go   删 dangerousTools/pathParamNames，改查 toolspec
  browser_operations.go  保留 spec 表的 plan/prepare/render，但 name/schema/caps 交给 toolspec

internal/agents/
  toolsets.go    7 个 Profile 常量（能力画像）—— 唯一需要人读的地方
  coding.go repo.go chat.go devops.go browser_agent.go director.go
                 删 switch / appender，改成 surface := tools.NewSurface(profile, deps)
  ports.go       工具类窄接口下移到 toolspec/deps.go；本文件只留 Agent 层端口
  tool_timeout.go  删除（isInteractiveUserTool → toolspec.IsInteractive）
  meta.prompt.md   "Available Tools Pool" 表格改为由注册表生成注入

internal/compression/priority.go   删除，改查 toolspec.TruncationPriority
internal/tui/render.go:18          skipBodyTools 删除，改查 toolspec.DisplayMode
internal/config/config.go          新增 [tools.deferred]
```

**依赖方向**（已用 `go list -deps` 核实无环）：
`toolspec → llm`；`tools → toolspec`；`agents → tools, toolspec`；`compression → toolspec`；`tui → toolspec`。
`internal/tools` 的传递依赖里不含 `compression/memory/thinklink/agents/knowledge`，所以 `compression → toolspec` 与 `tui → toolspec` 都安全。

**为什么 toolspec 只准 import `internal/llm`**：`Spec.Build` 需要 `llm.Engine`（micro_agent/deepthinking/design/consolidate_knowledge 都是 LLM 背书的工具）。其余一切依赖（guard、MCP、委派、checkpoint、boardID）都用 **toolspec 自己声明的窄接口**，由 `internal/tools` 或 `internal/agents` 侧的具体类型结构化满足。这样 leaf 性质可被 `scripts/` 加一条守卫脚本钉住。

---

## 3. ToolSpec 定义

```go
// internal/toolspec/spec.go
type Spec struct {
    Name string          // 唯一键，等于模型看到的 function name
    Brief string         // 一句话介绍（≤40 rune）。Frequency=Cold 时必填，用于目录
    Effect Effect        // 能力标签位掩码，必填非零 —— 决定发给谁 + 派生所有元数据
    Frequency Frequency  // Hot / Warm / Cold，缺省 Hot
    Mutates bool         // 有副作用（文件/进程/网络/浏览器状态）
    Serial bool          // 必须串行（改动共享状态，即使只读）
    BlocksOnUser bool    // 会阻塞等待用户响应 → 豁免工具超时
    NeedsAuth bool       // 触发 WorkspaceGuard 授权
    PathParams []string  // NeedsAuth 时参与工作区边界检查的参数名
    TruncPriority int    // 0=优先截断 1=常规 -1=受保护（压缩用）
    Display DisplayMode  // Compact（TUI 不展开正文）/ Full
    ModelSlot string     // "" 或 [tools.llm.<slot>] 的槽名
    Available func(Deps) (ok bool, reason string) // 运行期可用性；nil = 恒可用
    Describe func(Deps) string                    // 动态描述；nil = 用 schemas.json 的 description
    Build func(Deps) (ToolFunc, error)            // 执行器工厂，必填
}

type ToolFunc func(ctx context.Context, params map[string]interface{}) (interface{}, error)
```

**描述与 schema 来自 `schemas.json`**（D1）：`Spec` 不重复存 description/parameters，注册表在 `init` 后把两者拼起来。
`Describe != nil` 时覆盖 description（用于 `delegate_browser` 的能力降级文案）。

### 3.1 校验规则（`Registry.Validate()`，启动即跑）

| 规则 | 失败后果 |
|------|----------|
| `Name` 唯一；`schemas.json` 与 Spec 集合**双向** 1:1 | fatal |
| `Effect != 0`；`Build != nil` | fatal |
| `Frequency == Cold ⇒ Brief != ""` 且 `len([]rune(Brief)) <= 40` | fatal |
| `Effect & (EffectFSWrite\|EffectShell\|EffectBrowserEval) != 0 ⇒ NeedsAuth == true` | fatal |
| `NeedsAuth && len(PathParams) > 0 ⇒` 每个 PathParam 必须在 schemas.json 的 `properties` 里存在 | fatal |
| `Mutates ⇒ !Effect.Has(EffectReadOnly)`；`Serial ⇒ 不得被标为可并发` | fatal |
| `BlocksOnUser ⇒ Serial == true && Frequency == Hot` | fatal |
| `Effect & EffectFlow != 0 ⇒ Frequency == Hot`（`agent_exit`/`ask_user_for_help`/`load_tool` 不可延迟） | fatal |
| `TruncPriority == -1 ⇒ Mutates == false`（受保护的工具结果不能是有副作用的写操作回执） | warn |
| 每个 Profile 解析结果非空，且**必含 `agent_exit`**（除非该 Profile 显式 `Deny` 并在 golden 里留痕） | fatal |

这条清单直接封死背景文档 §8 的 1/2/3/7/8 五个缺口。

---

## 4. 能力标签体系

```go
// internal/toolspec/caps.go
type Effect uint64

const (
    // 只读族
    EffectFSRead      Effect = 1 << iota // read_file / list_dir / print_dir_tree
    EffectSearchRegex                    // search_by_regex
    EffectCodeSemantic                   // semantic_search / query_code_skeleton / query_code_snippet
    EffectCodeGraph                      // find_function_callee / find_function_caller / query_call_graph
    EffectContext                        // read_note / read_artifact
    // 认知族（LLM 背书，无副作用）
    EffectCogReflect                     // thinking
    EffectCogSubLLM                      // micro_agent
    EffectCogDeep                        // deepthinking
    EffectCogDesign                      // design
    // 写族
    EffectFSWrite                        // create_file / search_replace_in_file / delete_file / rename_file
    EffectShell                          // run_bash
    EffectBrowser                        // browser_*（观察类）
    EffectBrowserEval                    // browser_eval（单独一档： NeedsAuth）
    EffectKnowledgeWrite                 // consolidate_knowledge
    EffectKnowledgePrune                 // prune_history
    EffectCheckpoint                     // git_checkpoint_*
    // 控制族
    EffectFlow                           // agent_exit / ask_user_for_help / load_tool
    EffectDelegate                       // delegate_*
)

// 派生查询（替代 8 张表）
func IsReadOnly(name string) bool        // ← 替代 adapter.go:76 knownReadOnlyTools
func IsSerial(name string) bool          // ← 替代 executor.go:76 nonParallelToolNames
func IsInteractive(name string) bool     // ← 替代 agents/tool_timeout.go:13
func NeedsAuth(name string) bool         // ← 替代 workspace_guard.go:44 dangerousTools
func PathParams(name string) []string    // ← 替代 workspace_guard.go:56 pathParamNames
func TruncationPriority(name string) int // ← 替代 compression/priority.go:7
func DisplayMode(name string) DisplayMode// ← 替代 tui/render.go:18 skipBodyTools
func ModelSlot(name string) string       // ← 替代 config.go:290 getToolOverride 的硬编码 switch
```

**派生规则**（写死在 `caps.go`，不许各调用点自己判断）：
- `IsReadOnly = !Mutates && !Serial && Effect 只含只读/认知/上下文族`。
  → 今天 `find_function_*`、`query_call_graph`、`read_note`、`read_artifact`、`browser_inspect`、`browser_snapshot` 因为不在 7 项白名单里而被当成有副作用（背景文档 §8.6），重构后它们**自动变成可并发**。这是刻意的行为变更，见 §9.7。
- `IsSerial = Serial || BlocksOnUser || Effect & (EffectFlow|EffectDelegate写类|EffectBrowser状态类) != 0`。
- `WorkspaceGuard.Check` 改为：`if !toolspec.NeedsAuth(name) { return false,"" }` → 再按 `Effect` 分派到 `checkFileOp`/`checkBash`/`checkBrowserEval`。**分派依据从"名字 switch"变成"能力标签"**，封死"进了 dangerousTools 但 Check 里没有 case ⇒ 声明无效"这个坑（背景文档 §5 表 3）。

---

## 5. Agent 能力画像（D2）

```go
// internal/toolspec/profile.go
type Profile struct {
    Agent   string
    Grant   Effect  // 命中的能力
    Deny    Effect  // 显式排除（同族但不给）
    Palette Effect  // 仅 Director 用：可发放给 Meta 设计的 Custom Agent 的上限
    Promote []string // 把某工具从 Cold 提到 Hot（per-agent 覆盖频率档）
    Demote  []string // 反向
}

func Match(p Profile, deps Deps) []*Spec  // Grant 命中 && !Deny && Available(deps).ok
```

### 5.1 七个画像

| Agent | Grant | Deny | 备注 |
|-------|-------|------|------|
| Director | `FSRead｜SearchRegex｜CogDeep｜Flow｜KnowledgePrune｜Context｜Delegate` | `FSWrite｜Shell｜CodeSemantic｜CodeGraph｜CogReflect｜CogSubLLM｜CogDesign｜Checkpoint｜Browser*` | 保持"只读 + 委派"。`Palette` = 今天 `getToolFunc` 的集合 ∪ Meta 广告过的 `CodeSemantic`（见 §9.4） |
| Coding | `FSRead｜FSWrite｜SearchRegex｜Shell｜CodeSemantic｜CodeGraph｜Cog*｜Flow｜Knowledge*｜Checkpoint｜Delegate` | `Delegate` 里只留 `delegate_browser`（用 `Deny` + Spec 的 `Agents` 约束，见下） | 补齐 `thinking`/`query_call_graph`（§9.1） |
| Repo | `FSRead｜SearchRegex｜CodeSemantic｜CodeGraph｜CogDeep｜Flow｜Knowledge*｜Context` | `CogReflect｜CogSubLLM｜CogDesign` | 补齐 `agent_exit`（§9.2）；`delegate_devops` 走 Spec 的 `Available` |
| Chat | `CogReflect｜CogSubLLM｜CogDeep｜Flow` | 其余全部 | 无任何文件系统能力 |
| DevOps | `Shell｜FSRead｜SearchRegex｜CogReflect｜CogSubLLM｜Flow` | `FSWrite｜Code*` | 与今天一致 |
| Browser | `Browser｜BrowserEval｜Flow` | 其余全部 | 14 个工具由 `EffectBrowser` 整族发放 |
| Custom | 运行期由 Meta 的 `tools_used` ∩ `Director.Palette` 决定 | — | `registerCustomAgent` 变成 `Match(customProfile, deps)` |

**"同族但只给一个"怎么表达**（Coding 只能委派 browser，Director 能委派全部）：
`delegate_*` 每条 Spec 额外带一个 `Audience []string`（允许的 Agent 名，空 = 全部）。
`Match` 的判据是 `Grant 命中 && !Deny && Audience 允许 && Available`。
`Audience` 是**唯一**允许出现 Agent 名字的地方，且它写在 Spec 上（工具自述"我给谁用"），不是写在 Agent 上——这样新增委派工具时必须显式声明受众，不会静默扩权。

### 5.2 与今日工具面的逐一对账

重构后必须逐一比对 `Match(profile)` 的输出与今天的实际注册结果。差异只有两类，且都必须写进 §9：
- **刻意修正**（§9.1/9.2/9.4）：Coding 补 `thinking`+`query_call_graph`；Repo 补 `agent_exit`；Custom 补 `semantic_search`+`query_code_*`。
- **等价**：其余必须完全一致，由 golden 测试钉住。

---

## 6. 注册与构建

### 6.1 声明：Spec 与实现同文件

```go
// internal/tools/file_operations.go（示例）
func init() {
    toolspec.MustRegister(toolspec.Spec{
        Name:          "read_file",
        Brief:         "按行区间或整读文件，带大文件保护",
        Effect:        toolspec.EffectFSRead,
        Frequency:     toolspec.Hot,
        TruncPriority: 0,
        Display:       toolspec.DisplayCompact,
        Build:         func(d toolspec.Deps) (toolspec.ToolFunc, error) { return d.Files().ExecuteReadFile, nil },
    })
}
```

**自注册**（`init`）是关键：实现与声明物理相邻，"实现了 Execute* 但忘了加 case"这类失效从根上不可能发生。
`schemas.json` 提供 description + parameters，缺一条就 fatal（§3.1）。

### 6.2 依赖注入：`Deps`

```go
// internal/toolspec/deps.go
type Deps interface {
    Files() FileOps; Search() SearchOps; Sys() SysOps; Edit() EditOps
    RepoOps() RepoOps; Flow() FlowOps; Cog() CogOps
    MCP() MCPClient; LLM() llm.Engine
    Guard() Guard                       // 窄接口，*tools.WorkspaceGuard 结构化满足
    Delegator() Delegator               // Agent 层实现（Director）
    Checkpointer() Checkpointer         // nil = 非 git 仓库或未启用
    BoardID() string                    // "" = 无 blackboard
    Browser() BrowserCapability         // nil/不可用 → delegate_browser 描述降级
    ProjectPath() string; FullYolo() bool; AgentName() string
}
```

`internal/tools.BuildDeps` 是实现体，由 `internal/app/app.go` 在装配时填充（今天 `globalctx.GlobalCtx` 的字段基本一一对应，`app.go:195-253` 那段几乎不动）。
`agents/ports.go` 里的 `FileToolSet`/`SearchToolSet`/`SysToolSet`/`EditToolSet`/`RepoToolSet`/`FlowToolSet`/`Thinker`/`DeepThinker`/`Designer`/`MicroAgentRunner` **下移**到 `toolspec/deps.go`（改名去掉 `ToolSet` 后缀），`ports.go` 只留 Agent 层端口（`EventBus`/`PromptFormatter`/`Env`/`RepoContextStore`/`KnowledgeProvider`/`BrowserControl`）。

### 6.3 构建：一个函数取代 5 个 switch + 4 个 appender

```go
// internal/tools/build.go
func NewSurface(profile toolspec.Profile, deps toolspec.Deps) (*Surface, error)
```
- 内部：`toolspec.Match(profile, deps)` → 逐条 `spec.Build(deps)` → `NewAdapter(name, describe, fn).WithSchema(schema).WithReadOnly(toolspec.IsReadOnly(name))` → **统一 `SetGuardOnAdapters`**（不再有"append 之后忘了再调一次"的坑，背景文档 §8.9）。
- 返回 `*Surface`（§7），Agent 只持有 Surface。
- `Build` 出错（`Available` 为假、`Build` 返回 error）→ 该工具跳过 + `slog.Warn`，但**同时记入 `Surface.Skipped()`**，由启动自检在 Director 上打印一次汇总（今天这些信息散落在 6 处 `slog.Error`/`slog.Warn`）。

### 6.4 构造期 vs Run 期

今天 `git_checkpoint_*` 必须在 `coding.go:431-436` 于 Run 期 append（`GitCheckpointManager` 每次 Run 才创建），`read_note`/`read_artifact` 必须在 `executor.go:190-207` 运行期注入。重构后统一为：**Surface 在 Run 期构建**（`Agent.Run()` 开头 `tools.NewSurface(profile, depsForThisRun)`），构造函数只缓存 `Profile` 与 `Deps` 的静态部分。

- 代价：每次 Run 重建 ~25 个 Adapter（纯内存，微秒级，可忽略）。
- 收益：`coding.go` 的 Run 期 append、`executor.go:190-207` 的运行期注入、`repo.go:174` 的 `SetDevOpsAgent` 晚绑定**全部删除**——`Available(deps)` 用 `deps.Checkpointer() != nil` / `deps.BoardID() != ""` / `deps.DevOps() != nil` 表达同一件事。
- Director 例外：它的 Surface 必须**跨回合存活**（Meta 注册的 Custom Agent 要持久），所以 Director 持有长生命周期 Surface + `Surface.Add(profile)` 增量注册（见 §7.5）。

---

## 7. Deferred Tool Schema（D3）

### 7.1 Surface 状态机

```go
// internal/toolspec/surface.go（纯逻辑，无 Agent 依赖，可单测）
type Surface struct {
    mu           sync.Mutex
    specs        []*Spec              // Match 结果（全量，含冷工具）
    adapters     map[string]*Adapter  // 由 tools 侧填入
    materialized map[string]bool      // append-only
    budget       int                  // 每步 full-def token 预算
}

func (s *Surface) Defs() []llm.ToolDef  // 每步调用：Hot ∪ Warm ∪ materialized(Cold)，按名排序
func (s *Surface) Catalog() string      // 未 materialize 的冷工具目录（注入 system prompt）
func (s *Surface) Materialize(names []string) (schemaJSON string, err error)
func (s *Surface) Lookup(name string) (ad *Adapter, state ToolState)
                                        // state ∈ {Ready, ColdKnown, Unknown}
func (s *Surface) Skipped() []SkippedTool
```

`Catalog()` 输出形如（每行 ≈ 25 token，对比全量声明的 200-440 token）：

```
## Deferred tools — call `load_tool` with the names you need before using them
- consolidate_knowledge: 把本任务关键结论固化进知识库
- prune_history: 知识库条目列举/合并/删除
- design: 编码前产出结构化实施方案
- deepthinking: 常规手段失败后的多维深度分析
- query_call_graph: 查询函数调用图（上下游 N 层）
...
```

### 7.2 `load_tool` 元工具

```json
{ "name": "load_tool",
  "parameters": { "type":"object",
    "properties": { "names": { "type":"array", "items":{"type":"string"},
                               "minItems":1, "maxItems":8,
                               "description":"要取回完整 schema 的工具名（来自 deferred 目录）"}},
    "required":["names"] } }
```
Spec：`Effect: EffectFlow`（⇒ 强制 Hot，§3.1）、`Mutates:false`、`Serial:true`、`TruncPriority:-1`（schema 回执不许被压缩截断，否则模型拿到残缺 schema）、`Display: Compact`。
返回：每个名字的 `{name, description, parameters}` JSON + 一行提示 `Now call the tool with these parameters.`。
未知名字 → 返回可用冷工具名单（不报错，让模型自纠）。

**批量**（`maxItems: 8`）是刻意的：每次 materialize 都会让 provider 侧的前缀缓存失效一次，批量把 N 次失效摊成 1 次。

### 7.3 直调兜底（关键，决定"永不死路"）

`executor.go` 的 not-found 分支（今天 `:698`/`:731` 写 `"Tool %s not found"`）改为：

```go
ad, state := surface.Lookup(tc.Function.Name)
switch state {
case Ready:      // 正常执行
case ColdKnown:  // 模型没先 load_tool
    schemaJSON, _ := surface.Materialize([]string{tc.Function.Name})
    toolResult = "Error: schema for " + name + " was not loaded. Full schema:\n" + schemaJSON +
                 "\nRetry the call now with these parameters."
case Unknown:
    toolResult = fmt.Sprintf("Tool %s not found. Deferred tools available: %s", name, surface.CatalogNames())
}
```
- 这条结果走正常 tool 消息通道，**不触发 `NoActionRetryLimit`**（它不是"纯文本无动作"）。
- Director 侧 `planner_run.go:591-593` 的同名分支同样改造。
- `Serial` 工具在并行批次里的处理不变（`toolbatch` 已经串行化）。

### 7.4 与既有机制的交互（必须一起改，否则不生效）

| 交互点 | 今天 | 改成 |
|--------|------|------|
| 子 Agent 工具定义快照 | `executor.go:209-213`，**每次 RunAgentLoop 只算一次** | 每步开头 `toolDefs := surface.Defs()` |
| Director 工具定义快照 | `planner_run.go:97-101`，每次 `Planner.Run` 冻结 | 同上，每步重算 |
| Director 执行门控 | `planner_run.go:564-593`：不在 `toolDefs` 里的名字 → "not found" | 改为查 `surface.Lookup`（否则冷工具永远不可执行） |
| `ExecutorConfig` | `Adapters []*tools.Adapter` | `Surface *tools.Surface`（保留 `Adapters` 一个版本周期给测试用） |
| `read_note`/`read_artifact` 注入 | `executor.go:190-207` 按名查重后 append | 删除，改由 `EffectContext` + `deps.BoardID()` |
| rollout / `/resume` | materialize 状态不持久 → 续跑要重新付费 | 把 `materialized` 名单写进 `world_state` 记录，`ReadRolloutMemory` 时恢复 |
| `ComputeToolDefsHash` | 只在 `director.go:217-228` 启动日志用一次 | 每步 debug 级日志（缓存一致性排查用） |

### 7.5 运行期注册（Meta / Custom Agent）

`Surface.Add(specs ...*Spec)`：追加 + 立即可执行（`Lookup` 走 adapters 表），但**要到下一步的 `Defs()` 才对模型可见**。
今天的行为是"本回合模型看不见，只有代码直接 `ad.Call` 能跑"（背景文档 §4 Director 快照限制 + `director.go:466-488`）；改成每步重算 `Defs()` 之后，**同一回合的下一步就能看见**——这是 §9.6 的行为变更。
Custom Agent 的工具由 `Match(Profile{Grant: metaWanted ∩ director.Palette}, deps)` 得出，`registerCustomAgent`（`director.go:632-694`）随之缩到 ~20 行。

### 7.6 分档提议与实测收益

分档判据：**一次 Run 内期望调用次数 ≤1 → Cold；2-3 → Warm；≥4 → Hot**。
配置可覆盖：

```toml
[tools.deferred]
enabled = true
budget_tokens = 2500        # 每步 full-def 预算；超预算按 Warm→Cold 顺序降级
hot  = ["read_file", "run_bash"]        # 强制热
cold = ["print_dir_tree"]               # 强制冷
```
校验规则：`Effect & EffectFlow != 0` 的名字**不允许**出现在 `cold` 里（§3.1）。

实测（`schemas.json` 字节数 ÷ 4 估算 token，目录行按 name+60B 计）：

| Agent | 今天/步 | Hot/步 | 冷工具数 | 目录/步 | 改后/步 | 省/步 | 典型步数 | 单次委派省 |
|-------|--------:|-------:|--------:|--------:|--------:|------:|--------:|-----------:|
| Coding | 3328 T | 2224 T | 6 | 112 T | 2337 T | **990 T** | 40 | ~39.6 K |
| Repo | 2270 T | 1227 T | 6 | 115 T | 1342 T | **927 T** | 15 | ~13.9 K |
| Browser | 2195 T | 1657 T | 5 | 91 T | 1749 T | **446 T** | 60 | ~26.8 K |
| Director | 1529 T | 980 T | 2 | 36 T | 1017 T | **511 T** | 20 | ~10.2 K |

一个典型的"Director + 一次 Repo + 两次 Coding"任务约省 **100 K token/任务**（不含 provider 侧缓存差异）。
提议的 Cold 名单（17 条，按声明体积排序）：`print_dir_tree`(1162B)、`consolidate_knowledge`(1144B)、`deepthinking`(1031B)、`prune_history`(953B)、`design`(836B)、`query_call_graph`(592B)、`browser_tabs`(580B)、`browser_scroll`(543B)、`find_function_caller`(498B)、`find_function_callee`(496B)、`git_checkpoint_rollback`(446B)、`query_code_skeleton`(391B)、`git_checkpoint_create`(394B)、`browser_select`(365B)、`browser_press`(347B)、`browser_type`(316B)、`git_checkpoint_list`(275B)。

**代价**：每个冷工具首次使用多一次 `load_tool` 往返（或一次兜底重试）。所以 `Promote`/`Demote` 的 per-agent 覆盖很重要——例如 Repo 的 `query_call_graph`/`find_function_caller` 是主力工具，应在 Repo 的 Profile 里 `Promote` 到 Hot；Browser 的 `browser_tabs` 在多标签任务里应 Promote。**首版按上表落地，跑 3-5 个真实任务后按 `load_tool` 的实际调用频次调档**（§11 未决项）。

---

## 8. 一致性闸门（D2/D4 的补偿控制，硬约束）

能力画像自动匹配 + 一次性落地，意味着"某个工具悄悄出现在不该出现的 Agent 上"成为主要风险。三道闸门：

1. **Golden surface 测试**（`toolspec/golden_test.go` + `testdata/tool_surfaces.golden`）
   固定 7 个 Agent 的解析结果：工具名（排序）+ 每个的 tier + readOnly + needsAuth + serial。
   任何 Spec/Profile 改动都会让 golden 变红，**必须显式改写 golden 文件才能通过** —— 这个 diff 就是评审物。
   同时钉住 §9 的三条刻意变更，防止后续被无意回退。
2. **注册表自检**（`Registry.Validate()`，§3.1）
   在 `app.go` 装配前调用；生产环境失败 → `slog.Error` + 该工具不注册 + 启动日志汇总；测试环境 → `t.Fatal`。
   另加 `TestSchemasJSONMatchesSpecs`：双向对账（取代今天只覆盖 browser 的 `TestBrowserToolSpecsMatchToolsJSON`，该测试删除）。
3. **架构守卫脚本**（`scripts/`，与既有两个脚本同级）
   - `check_toolspec_leaf.sh`：`go list -deps ./internal/toolspec` 只允许出现 stdlib + `yagent/internal/llm`。
   - `check_no_toolspec_import.sh`：`internal/toolspec` 不得 import `tools`/`agents`/`compression`/`tui`。
   - `check_file_sizes.sh` 的适用范围扩到 `internal/toolspec/`（非测试文件 ≤300 行）。

**测试基线**：`director_test.go:278`/`:941`、`browser_agent_test.go:79`、`coding`/`repo`/`devops`/`chat` 的注册断言全部改为读 golden，不再硬编码名单。

---

## 9. 行为变更清单（一次性重构会顺带改变的，逐条签字）

| # | 变更 | 依据 | 风险与处置 |
|---|------|------|-----------|
| 9.1 | **Coding 获得 `thinking` 与 `query_call_graph`** | 背景文档 §8.5：`CodingAgentParams.Thinker` 已注入但无 case；`RepoToolSet.ExecuteCallGraph` 已存在 | 低。Coding 工具数 +2；`thinking` 是 Cold 档，不增加每步开销。需同步 `coding.prompt.md` 说明何时用 |
| 9.2 | **Repo 获得 `agent_exit`，并设 `cfg.StopOnFinish = true`** | 背景文档 §8.4：`repo.go:91-121` 无该 case，`repo.go:207-228` 未设 StopOnFinish | **中**。只加 `agent_exit` 而不设 `StopOnFinish` 会导致"调了退出但循环继续"（`executor.go:703` 要求两者同时成立）。必须成对改，并在 `repo.prompt.md` 明确"仅在分析完成或确实阻塞时调用"。golden 里留痕 |
| 9.3 | **`consolidate_knowledge`/`prune_history`/`git_checkpoint_*`/Director 的 `agent_exit` 改用 schemas.json 的声明** | 背景文档 §8.3：这 5+1 条今天用的是 Go 里另一份 schema/description，改 tools.json 无效 | 低但**需人工合并文案**：Go 里那份描述更长（含使用时机）。合并后写回 schemas.json，删除 `coding.go:454`(pruneSchema)/`:507`(consolidateSchema)/`:569,591,619`(checkpoint 内联)/`director.go:238-244`(agent_exit 内联) |
| 9.4 | **Custom Agent 真的能用 `semantic_search`/`query_code_skeleton`/`query_code_snippet`** | `meta.prompt.md:72-74` 广告了这三个，但 `director.go:571-606` 的 `getToolFunc` 没有 case ⇒ 今天静默丢弃（`director.go:644` 只 `slog.Warn`） | 低。需要给 Director 注入 `RepoOps`（`app.go:475-503` 今天没有）；Meta 的工具池表格改为注册表生成，消灭第 4 份漂移源 |
| 9.5 | **`delegate_*` 6 个闭包合并为 1 个泛化实现 + 6 条 Spec** | `director.go:319-497` 六段结构完全相同（取 task → resolveDelegateRefs → boardID+rollout → routeCtx → Run → writeDelegateNote → applyEnhancedCommander） | 中。约 180 行 → 约 40 行。`Delegator` 窄接口必须覆盖 `routeCtx` 的档位语义与 `applyEnhancedCommander` 的 Artifact 落盘；`WithReadOnly(true)` 的三处（`:334`/`:368`）改由 Spec 的 `Effect`+`Audience` 表达，**并行性必须逐条对账**（`planner_parallel_test.go:211/246/267` 已有断言可复用） |
| 9.6 | **Meta 新注册的 Custom Agent 在同一回合的下一步即可见** | 今天 `planner_run.go:97` 冻结快照 ⇒ 只有代码直调能跑（`director.go:466-488`） | 低，且是改进。`Defs()` 改为每步重算的自然结果 |
| 9.7 | **`find_function_*`/`query_call_graph`/`read_note`/`read_artifact`/`browser_inspect`/`browser_snapshot`/`browser_status`/`browser_wait`/认知类工具变为可并发** | 背景文档 §8.6：只读白名单只有 7 项，与语义无关 | **中，且有前置修复**。MCP 客户端的**响应侧已经是按 id 多路复用的**（`mcp/client.go:301-330` 注册 `requests[id]`、`:332-383` readLoop 按 `resp.ID` 派发、`nextID` 是 atomic），但**请求侧 `sendJSON` 写 stdin 管道时没有加锁**（`mcp/client.go:396-407`，`c.stdin` 来自 `cmd.StdinPipe()`）。单次 write ≤ PIPE_BUF(4096) 时 POSIX 保证原子，但 `consolidate_knowledge`（content ≤1500 字符）与长 query 会超过，并发写会交错成非法 JSON-RPC ⇒ readLoop 报 "MCP invalid JSON" 且两个请求一起挂到超时。今天不会触发**仅仅因为**这些工具都不在 7 项只读白名单里、从不并行。⇒ **第 3 步之前必须先给 `sendJSON` 加一把写锁**（§10 步 0）。浏览器观察类（`browser_inspect`/`snapshot`/`status`/`wait`）共享 CDP 连接与当前标签页，**首版一律标 `Serial`**（与今天行为一致），确认无竞态后再放开 |
| 9.8 | **`delegate_browser` 的描述在 Director 与 Coding 两侧统一** | 今天 Director 永远用 `browserDelegateDescription`（`director.go:393-394`），Coding 用 schemas.json 的文案、仅能力缺失时才降级（`coding.go:186-190`） | 低。统一为 `Spec.Describe(deps)`：能力可用时用 schemas.json 文案，不可用时两侧都降级 |
| 9.9 | **冷工具首步多一次往返；每次 materialize 使 provider 侧前缀缓存失效一次** | §7.6 | 低。仓库今天**没有** `cache_control`（`grep -rn cache_control internal/llm/` 无命中），Anthropic 侧本来就没有显式缓存；OpenAI 侧的隐式前缀缓存每次 materialize 失效一次，用 `load_tool` 的批量参数摊薄 |
| 9.10 | **`run_bash`/`browser_eval` 的授权判据从"名字在 dangerousTools 里"变成"`NeedsAuth` 标签 + Effect 分派"** | §4 派生规则 | 低。行为等价，但封死"进了名单却没有 Check case"的静默失效。`safeSystemPaths` 白名单逻辑不动 |

---

## 10. 实施次序（单批次落地，内部按此顺序并逐步验证）

每一步的验证命令都是 `go build ./... && go vet ./...` + 该步涉及的包测试；**不启动任何长驻进程**（TUI/http/Chrome 都不行）。

| 步 | 内容 | 验收 |
|----|------|------|
| 0 | **前置修复**：`internal/mcp/client.go` 给 `sendJSON` 加写锁（新增 `writeMu sync.Mutex`，只包住 `c.stdin.Write`），消除并发写 stdin 管道交错的风险（§9.7） | `go test ./internal/mcp/... -race -count=1`；补一个并发 `CallTool` 的 race 测试 |
| 1 | 新建 `internal/toolspec`：`Effect`/`Frequency`/`Spec`/`Profile`/`Deps`/`Registry`/`Validate`；把 `agents/ports.go` 的工具类窄接口下移 | `go build ./internal/toolspec`；`scripts/check_toolspec_leaf.sh` 通过 |
| 2 | `git mv internal/agents/tools.json internal/toolspec/schemas.json`；写 `schemas.go` 的 embed + 解析；`agents/tools.go` 的 `ToolsJSON` 暂留为 `toolspec.SchemasJSON()` 的别名 | `go build ./...`；6 处 unmarshal 仍能编译 |
| 3 | 为全部 41 条 + `load_tool` + `delegate_*`(6) + `delegate_devops` + `read_note`/`read_artifact` 写 Spec（含 Effect/Frequency/TruncPriority/Display/NeedsAuth/PathParams/Serial/BlocksOnUser/Available/Describe/Build），在各实现文件 `init()` 里 `MustRegister` | `TestSchemasJSONMatchesSpecs` 双向 1:1 通过；`Registry.Validate()` 无 fatal |
| 4 | 写 7 个 Profile（`agents/toolsets.go`）+ `Match` + golden 测试；**先只跑测试，不改 Agent 代码** | golden 文件生成后，人工逐行对账 §5.2；§9.1/9.2/9.4 的差异必须出现在 golden diff 里 |
| 5 | `internal/tools/build.go`：`BuildDeps` + `NewSurface`；`Surface.Defs/Catalog/Materialize/Lookup/Add` + 单测 | `surface_test.go` 覆盖：冷工具不可见→load_tool→可见；直调兜底；未知名字；append-only；并发 Materialize |
| 6 | 改 6 个 Agent：删 switch/appender，`Run()` 里建 Surface；`ExecutorConfig.Adapters` → `Surface`；`executor.go:190-213`/`:650-661`/`:694-707` 改造 | `go test ./internal/agents/... -count=1 -timeout 300s` |
| 7 | 改 Director：`buildToolAdapters`/`buildDelegateAdapters`/`getToolFunc`/`registerCustomAgent` → Surface + `Delegator`；`planner_run.go:97-101`/`:552-593` 每步重算 + Lookup 兜底 | `go test ./internal/agents/... ./internal/agents/director/...`；`scripts/check_no_agents_import.sh`、`check_file_sizes.sh` 通过 |
| 8 | 收编元数据表：删 `knownReadOnlyTools`/`nonParallelToolNames`/`tool_timeout.go`/`compression/priority.go`/`tui/render.go:18`/`workspace_guard.go:44-61`，全部改查 `toolspec` | `go test ./internal/compression/... ./internal/tui/... ./internal/tools/...`；TUI 渲染/尺寸矩阵测试全绿 |
| 9 | 配置：`[tools.deferred]`（enabled/budget_tokens/hot/cold）+ `DefaultBrowserConfig` 同风格的整表缺省处理；`config.toml` 模板与 `docs/config-guide.md` 补文档 | `go test ./internal/config/...` |
| 10 | `meta.prompt.md` 的工具池改为注册表生成注入；`app.go` 给 Director 注入 `RepoOps` | Meta 相关测试 + 人工检查生成的表格与 `Palette` 一致 |
| 11 | rollout：`materialized` 写进 `world_state`，`/resume` 恢复 | `go test ./internal/memory/...`；`tui_resume_restore_test.go` 绿 |
| 12 | 文档：更新 `AGENTS.md` §3（tools.json 路径与条目数）/§4（各 Agent 工具面 + Repo 的 agent_exit）/§5.1-5.2（Adapter 与注册表）/§9.3（新增工具步骤重写）/§11（坑 5 已消除，新增 deferred 相关坑）；`docs/tool-registration.md` 标注"已被本方案取代，保留作历史现状记录" | 人工通读 |

**回滚**：整个批次在一个分支上，golden 文件与 `Registry.Validate()` 是主要防线；若步 0 之后实测发现上游 codeseek 不能并发处理在飞请求（§11 未决项 1 的残余风险），只需把 codeseek 桥接的那几条 Spec 标 `Serial`（不改架构，行为退回今天）。

---

## 10.5 落地状态（2026-10-13）

**已全部落地**：步 0-8 与步 12。七个 Agent（Director + Coding/Repo/Chat/DevOps/Browser/Custom）
的装配全部走 `tools.NewSurface(profile, deps)`；`lookupToolFunc`、`getToolFunc`、
`buildCodingAdapters`、`buildChatAdapters`、`buildDevOpsAdapters`、`buildBrowserAdapters`、
`buildToolAdapters`、`buildDelegateAdapters`（六段闭包）、`createKnowledgeToolAdapters`、
`createCheckpointToolAdapters`、`newBrowserDelegateAdapter`、`BrowserOperationsTool.BuildAdapters`
以及 8 张按名索引表全部删除。`scripts/check_no_manual_lookup.sh` 的 PENDING 名单已清空。

**步 9-11 的两项缓做已改为更好的做法**：
- Meta 的工具池表：`meta.prompt.md` 改为 `{{TOOL_POOL_TABLE}}` 占位符，由
  `renderCustomAgentToolPool()` 从注册表 + Palette 生成；`TestMetaPromptToolPoolMatchesPalette`
  断言"表里有的 = Palette 能发的"，杜绝再次广告未实现的工具。
- deferred 状态的持久化：**没往 rollout 写**（跨进程恢复的收益只有几百 token，却要动 TUI resume 链路）。
  改为更实在的一种：Director 的 `materializedAcrossTurns` 让已取回的冷工具**跨对话轮**保留，
  避免同一会话第二轮重新 `load_tool`（`TestMaterializedCarriesAcrossDirectorTurns`）。
  子 Agent 的工具面按委派新建，本来就不该跨委派复用，故不需要持久化。

**已知存量问题（非本次引入）**：`scripts/check_file_sizes.sh` 对
`internal/agents/director/planner_run.go` 报超限（652 > 300 行，HEAD 时已 645 行）。

---

## 11. 实施偏差与发现（落地时更新，2026-10-13）

已落地部分相对本方案的偏离，以及闸门实际抓出来的问题：

### 11.1 架构偏离（都是往更好的方向，但后续实施要按这里写的做）

| 方案里写的 | 实际做的 | 原因 |
|-----------|----------|------|
| `Surface` 放 `internal/toolspec/surface.go` | 放 **`internal/tools/surface.go`** | Surface 要持有 `*tools.Adapter` 并调 `ToToolDef()`，放 toolspec 会形成 toolspec→tools→toolspec 的环。toolspec 保持纯数据 + 画像匹配 |
| Spec 写在各实现文件的 `init()`（§6.1） | 写在 `internal/tools/specs_{file,code,cog,flow,knowledge,browser}.go` 与 `internal/agents/specs.go` | 同包同域分组。"实现与声明同处"的保证由 `Validate()` 的 schemas↔Spec 双向 1:1 承担，比物理相邻更硬 |
| 7 个画像放 `internal/agents/toolsets.go` | 放 **`internal/toolspec/profiles.go`** | 画像是纯数据，放 toolspec 后 `ValidateProfile` 与 golden 测试能自包含，`agents` 只是引用 |
| `Deps` 是接口，`internal/tools.BuildDeps` 实现它 | `Deps` 是**结构体**（字段为窄接口，nil = 缺失） | 避免为 12 个依赖再造一层接口；`Available` 直接判 nil 即可 |
| toolspec 可 import `internal/llm` | toolspec **零 yagent 依赖** | 认知/知识类工具的执行器改由 `Deps` 传入已绑定的 `ToolFunc`，于是连 llm 都不需要，leaf 性质更干净 |
| 配置键 `[tools.deferred] enabled = true` | **`disable = false`** | `bool` 零值是 false，用 `enabled` 会让"老配置文件没写这一段"变成"静默关闭新能力"——正是背景文档 §7 里 `[browser] enabled` 踩过的坑。缺省即开启 |
| Meta 的工具池"由注册表生成" | 用 **`MatchNames(names, palette, deps)`** | Custom Agent 的 `tools_used` 本质是**名字列表**（LLM 在运行期产出），不是能力集合。名字语义才能同时做 palette 上限校验与 `agent_exit` 强制补齐 |

### 11.2 闸门实际抓出来的 5 个问题（都已修）

1. **`delete_file` 的 `PathParams` 有死声明**：历史 `pathParamNames` 挂着 `target_file`，但它的 schema 里从来只有 `file_paths`。由 `Validate()` 的"PathParams 必须存在于 schemas.json"规则抓出。
2. **`git_checkpoint_list` 被误判为有副作用**：`writeEffects` 里塞了 `EffectCheckpoint`，但 list 只读标签。改为由 create/rollback 各自显式声明 `Mutates`。
3. **3 个工具的截断档位划错**：我给 `list_dir`/`delete_file`/`rename_file` 标了 `TruncFirst`，而历史 `toolTruncationPriority` 里它们是常规值。是现存测试 `TestToolTruncationPriority` 的表暴露的 —— 等价性闸门（§11.3）因此必须以现存表为基准。
4. **`Display=DisplayCompact` 范围被我扩大**：我给 codeseek 检索类都标了 Compact，历史 `skipBodyTools` 只有 7 项。已收敛到与历史完全一致。
5. **`Surface` 的 deferred 开关不权威**：`NewSurface` 只在开启时写 `deps.DeferSchemas/LoadTool`，于是调用方或测试探针预填的值会让 `deferred=false` 关不干净（`load_tool` 仍在工具面里却不出现在声明里）。改为两个字段无条件由 `SurfaceOptions` 决定。

另外 `mcp` 的并发原子性测试在去掉写锁时确实变红（`invalid character 'i' after object key:value pair`），证明 §10 步 0 的回归测试有效。

### 11.3 一个必须知道的架构后果：注册表内容取决于链接了哪些包

Spec 靠各包 `init()` 自注册 ⇒ **只 import toolspec 的二进制看不到 tools/agents 注册的 Spec**。
影响：`internal/compression` 的单测里 `toolspec.TruncationPriority("deepthinking")` 会返回"未注册"的默认值。

处理办法（已采用）：
- 需要全量注册表的测试放在 **`internal/agents`**（它链接 tools+agents，看到全部 49 条）——golden surface、`Registry.Validate()`、派生元数据等价性都在 `internal/agents/registry_gate_test.go`。
- `internal/compression` 的测试显式 `_ "yagent/internal/tools"` 把注册表拉进来，并写明原因。

生产链路总是同时链接 tools 与 agents，因此运行期行为不受影响。**新增只依赖 toolspec 的包时，别在它的单测里断言注册表派生值**，除非显式拉入提供方包。

### 11.4 等价性闸门的形态

`TestDerivedMetadataMatchesLegacyTables` 把 6 张 legacy 表**逐字抄进测试**，然后断言派生值与之一致；唯一允许的偏差以显式集合列出（`signedReadOnlyAdditions`、`newTools`）。往这两个集合里加名字，等于在测试里公开承认"我扩大了一个工具的并发面/改了它的展示语义"，必须与 §9 一起评审。

---

## 12. 未决项与禁止事项

### 未决项（实施前必须先答）

1. ~~`mcp.MCPClient.CallTool` 是否并发安全？~~ **已核实（2026-10-13）**：响应侧按 request id 多路复用，是安全的（`mcp/client.go:301-330`、`:332-383`、`nextID` atomic）；**请求侧 `sendJSON` 写 stdin 无锁**（`mcp/client.go:396-407`），超过 PIPE_BUF 的并发写会交错 ⇒ 必须先做 §10 步 0。
   **仍未核实的残余风险**：上游 codeseek（Rust）是否能同时处理多个在飞请求。若它内部串行化，并行调用只增加排队、不影响正确性；若它按行读取但假设单请求，需要实测。→ 步 0 之后用一个并发 `semantic_search` 的集成测试（环境变量门控）验证。
2. **Cold 名单的最终档位**：§7.6 的 17 条是按"期望调用 ≤1 次"划的初值，需跑 3-5 个真实任务、统计 `load_tool` 调用频次与兜底触发次数后调整（含 per-agent `Promote`）。
3. **`budget_tokens` 默认值**：2500 是按 Coding 的 Hot 集（2224 T）留 12% 余量定的，需在 Director（工具最多）上复核。
4. **Custom Agent 是否启用 deferred**：建议**关闭**（`MaxSteps=15`，多一次往返不划算），但要写进 Profile 而不是全局开关。
5. **`Effect` 粒度**：`EffectFSRead` 目前覆盖 `read_file`/`list_dir`/`print_dir_tree` 三个工具。Chat 只要认知族、Director 要全部只读族，所以当前粒度够用；若将来出现"只给 list_dir 不给 read_file"的 Agent，需要拆分。

### 禁止事项

1. **不要在 `internal/toolspec` 里 import `tools`/`agents`/`compression`/`tui`**（守卫脚本会拦）。
2. **不要往 `toolbatch` 里加业务**（它继续只吃一个 `IsReadOnly` 谓词）。
3. **不要在 Spec 之外再建任何按工具名索引的表**。需要新的元数据维度时，加 `Spec` 字段 + 派生函数，不要加 map。
4. **不要让 `Frequency` 覆盖 `EffectFlow`**：`agent_exit`/`ask_user_for_help`/`load_tool` 永远 Hot（§3.1 已 fatal，别绕过）。
5. **不要在 `Adapter.Call()` 里加截断/大小限制**（体积治理属于工具实现与压缩层，保持现状分层）。
6. **不要手改 `internal/protocol/agent_events.go`**（本方案不新增事件类型；若最终决定为 materialize 发事件，必须改 `protocol/agent-events.yaml` 再重新生成）。
7. **不要 `go mod tidy`**（`go-rod/rod` 是未决项）。
8. **不要为了通过 golden 测试而直接改写 golden 文件**——先确认变更是 §9 里签过字的，或经过显式讨论。
