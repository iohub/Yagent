# Yagent 工具注册机制（背景文档）

> **用途**：这是后续做方案设计（新增工具 / 重构注册机制 / 引入工具权限模型等）时的**共同事实基础**。
> 本文只描述当前代码的真实状态，不提建议、不做取舍。所有结论都带 `file:line`，核对时间 2026-10-13。
> 与 `AGENTS.md` 冲突之处以本文为准，差异集中在第 8 节。

---

## 0. 一句话与一张图

**工具名（字符串）是唯一的对齐轴**：声明在 `tools.json`，实现在 Go，权限/并发/截断/渲染各自有独立的名字表。
任何一处名字对不上，**不会编译失败、不会运行时报错，只会静默少一个工具**。

```
internal/agents/tools.json            41 条声明（name/description/parameters）
  └─ agents/tools.go:7                //go:embed → ToolsJSON []byte
      └─ 6 处各自 json.Unmarshal      director.go:250 coding.go:148 repo.go:84
                                      chat.go:55 devops.go:63 browser_agent.go:81
          └─ 名字门控（三种范式）
              ├─ A. 表驱动 + switch    coding/repo/chat/devops/director
              ├─ B. 声明式 spec 表     tools/browser_operations.go:60 browserToolSpecs
              └─ C. 完全就地声明       delegate_* / knowledge / checkpoint / read_note / read_artifact
                  └─ tools.NewAdapter(name, desc, fn).WithSchema(params).WithReadOnlyIfKnown()
                      └─ tools.SetGuardOnAdapters(adapters, guard)   adapter.go:148
                          └─ agent.Adapters []*tools.Adapter
                              └─ ExecutorConfig.Adapters             executor.go:28
                                  ├─ 运行期补 read_note/read_artifact  executor.go:190-207
                                  ├─ ToToolDef() × N + SortToolDefs    executor.go:209-213  ← 本回合冻结快照
                                  └─ llm.GenerateContent(..., toolDefs) → 模型
                                      └─ 回调：按名线性扫描找 Adapter → Call()
                                          JSON 解析 → WorkspaceGuard 授权 → fn → json.Marshal → tool 消息
```

---

## 1. 声明层：tools.json

| 事实 | 位置 |
|------|------|
| `//go:embed tools.json` → `var ToolsJSON []byte` | `internal/agents/tools.go:7-8` |
| 反序列化目标 `tools.ToolDefinition{Name, Description, Parameters map[string]interface{}}` | `internal/tools/adapter.go:19-23` |
| 当前条目数 | **41**（`python3 -c json.load` 实测；AGENTS.md §3 写的 27 已过期） |
| 键集合 | 恰好 `{name, description, parameters}`，无重名，**每条都有 parameters** |
| 解析次数 | 6 次独立 unmarshal（同一个 blob，每个 Agent 构造期各解一次） |
| 解析失败处理 | 5 处只 `slog.Error` 后继续（→ 该 Agent 空工具表）；`browser_agent.go:83` 直接 `return nil` |
| 校验 | **无**：不查 name 唯一性、不查 parameters 是否为合法 JSON Schema、不查与 Go 实现是否对应 |
| 唯一一致性闸门 | `tools.TestBrowserToolSpecsMatchToolsJSON`（`internal/tools/browser_operations_test.go:88-121`），只覆盖 `browser_*` |

**不在 tools.json 里、只存在于 Go 的名字**：`delegate_repo` / `delegate_coding` / `delegate_chat` / `delegate_devops` / `delegate_meta` / `read_note` / `read_artifact` / 全部 `delegate_<custom>`。
`delegate_browser` 是例外：它在 tools.json 里，但**只有 Coding 侧读它的 description+schema**（`coding.go:179-204`）；
Director 侧的 `delegate_browser` 用生成的描述 `browserDelegateDescription`（`director.go:393-394`）+ `buildDelegateSchema`（`:410`）。

**在 tools.json 里但没有任何 agent switch 命中的"孤儿声明"（5 个）**：
`git_checkpoint_list`、`git_checkpoint_rollback`、`git_checkpoint_create`、`consolidate_knowledge`、`prune_history`
——它们由 appender 用**另一份就地手写的 name/description/schema** 注册（见 §8.3），tools.json 那五条是死文本。

---

## 2. Adapter 层（`internal/tools/adapter.go`）

```go
type ToolFunc func(ctx context.Context, params map[string]interface{}) (interface{}, error)   // :15

type Adapter struct {                                                                          // :26-36
    name, description string
    fn          ToolFunc
    schema      map[string]interface{}
    guard       *WorkspaceGuard
    readOnly    bool   // 零值 false = fail-safe：未显式声明的工具一律视为有副作用，不可并发
}
```

| API | 位置 | 值得注意的行为 |
|-----|------|----------------|
| `NewAdapter(name, desc, fn)` | `:38` | 装一个 catch-all `defaultSchema{input:string}`（`:42-50`）——**忘记 `WithSchema` 就等于告诉模型"这个工具只有一个字符串参数"** |
| `WithSchema(map)` | `:61` | 无条件覆盖；传 `nil`（tools.json 缺 parameters 时）→ `llm.FunctionDef.Parameters` 为 nil → `omitempty` → 模型看到"无参数" |
| `WithReadOnly(bool)` | `:69` | 显式声明可并发 |
| `knownReadOnlyTools` | `:76-84` | **只有 7 个名字**：read_file / list_dir / print_dir_tree / search_by_regex / semantic_search / query_code_skeleton / query_code_snippet |
| `WithReadOnlyIfKnown()` | `:93-98` | 按名命中白名单才置 true；未命中保持 false |
| `SetGuard` / `SetGuardOnAdapters` | `:114` `:148` | **非链式**，且只对调用那一刻的切片生效 → 之后 append 的适配器必须再调一次 |
| `Call(ctx, inputJSON) (string, error)` | `:118-145` | ① `json.Unmarshal`，**失败则退化成 `{"input": 原始串}`**；② `guard != nil` 才做 `Check`→`RequestAuth`，**guard 为 nil 时完全不检查**；③ `fn(ctx, params)`；④ 结果 `json.Marshal`（string 结果会带引号）；⑤ 错误原样上抛 |
| `ToToolDef()` | `:179-188` | → `llm.ToolDef{Type:"function", Function:{name,description,parameters}}` |
| `SortToolDefs` / `ComputeToolDefsHash` | `:157` `:166` | 按名升序；hash 仅供日志（唯一调用点 `director.go:217-228`） |

**关键**：`Call()` 里没有任何截断/大小限制。工具输出体积由各自实现（`read_file` 1000 行 / 10KB、browser `max_output_bytes`=24000）和跨回合压缩（`compression.TruncateToolResultsToBudget`，`executor.go:363`）负责。

---

## 3. Agent 装配层：三种注册范式

### A. 表驱动 + 名字 switch（coding / repo / chat / devops / director）

统一形状：`for _, def := range toolDefs { switch def.Name { case "...": fn = ... ; default: continue } ; append(NewAdapter(...).WithSchema(def.Parameters).WithReadOnlyIfKnown()) }`

| Agent | 构造 | builder | 名字门控 | 命中的工具 |
|-------|------|---------|----------|------------|
| Coding | `NewCodingAgent` `coding.go:119`（参数 `CodingAgentParams:88`） | `buildCodingAdapters:146` | `lookupToolFunc(name,p):209-256`（唯一有独立函数的） | read_file, search_replace_in_file, create_file, run_bash, search_by_regex, delete_file, rename_file, list_dir, print_dir_tree, semantic_search, query_code_skeleton, query_code_snippet, find_function_callee, find_function_caller, micro_agent, deepthinking, design, agent_exit, ask_user_for_help(条件) |
| Repo | `NewRepoAgent` `repo.go:63` | `(a).buildAdapters:82` | 内联 switch `:91-121` | read_file, search_by_regex, list_dir, print_dir_tree, semantic_search, query_code_skeleton, query_code_snippet, find_function_callee, find_function_caller, query_call_graph, deepthinking, ask_user_for_help(条件) —— **无 agent_exit** |
| Chat | `NewChatAgent` `chat.go:39` | `buildChatAdapters:53` | `:63-82` | micro_agent, thinking, deepthinking, agent_exit, ask_user_for_help(条件) |
| DevOps | `NewDevOpsAgent` `devops.go:46` | `buildDevOpsAdapters:61` | `:70-97` | run_bash, read_file, list_dir, print_dir_tree, search_by_regex, thinking, micro_agent, agent_exit, ask_user_for_help(条件) |
| Director | `NewDirectorAgent` `director.go:130` | `buildToolAdapters:236` | `:262-280` | search_by_regex, list_dir, read_file, print_dir_tree, deepthinking, ask_user_for_help(条件)；另在 `:238-246` 就地塞 agent_exit + read_artifact + read_note |
| Custom | 运行期 | `registerCustomAgent` `director.go:632` | `getToolFunc:571-606`（**第二套 switch**，含 search_replace_in_file/create_file/run_bash/thinking/micro_agent/deepthinking/…） | 由 Meta 输出的 `ToolsUsed` ∩ `toolDefMap`，末尾补 agent_exit（`:656-661`） |

条件注册点：`ask_user_for_help` 在 **FullYoloMode** 下被 6 个 Agent 各自跳过（`coding.go:248`、`repo.go:115`、`chat.go:76`、`devops.go:91`、`director.go:274`、`browser_agent.go:102`、`director.go:599`）。

### B. 声明式 spec 表（Browser，绕开 switch）

```go
type browserToolSpec struct{                                                        // tools/browser_operations.go:47-58
    name        string
    plan        func(...) ([][]string, error)   // 要执行的 browser-control argv 序列
    prepare     func(..., sessionDir string) error
    render      func(...) string
    guardURLParams        []string
    tolerateTailFailure   bool
}
var browserToolSpecs = []browserToolSpec{...}   // :60-278，14 条
```
`BuildAdapters(defs, guard)`（`:292-326`）：以 **tools.json 提供 schema/description**、spec 表提供执行器，
并**双向对账**（声明未实现 / 实现未声明都进 `errs`）。调用点 `browser_agent.go:87-91` 把 errs 降级成 `slog.Warn`。
`BrowserAgent` 再手工补 agent_exit / ask_user_for_help（`:96-108`）。
`browser_open`/`browser_tabs`/`browser_eval` 因共享当前标签页与 ref 状态，被硬写进串行名单（§4）。

### C. 就地 append（不经过 tools.json 的注册路径）

| 工具 | 注册函数 | 位置 |
|------|----------|------|
| 6 个内建 delegate | `buildDelegateAdapters` | `director.go:318-497`，按 `disabledAgents` 过滤 `:500-516` |
| delegate_browser（Coding 侧） | `newBrowserDelegateAdapter` | `coding.go:179-205`（browser==nil → 不注册；`Capability()` 为假 → 描述换成降级文案 `browserDelegateDescription`） |
| delegate_devops（Repo 侧） | 内联字面量 | `repo.go:128-149`（闭包晚读 `a.devops`，由 `SetDevOpsAgent` `repo.go:174` 注入；nil 时返回提示文案而非报错） |
| prune_history / consolidate_knowledge | `createKnowledgeToolAdapters` | `coding.go:491-547`；sourceAgent 为空时**只注册 prune**（Director 走这条 `director.go:199`） |
| git_checkpoint_* | `createCheckpointToolAdapters` | `coding.go:550-631`，在 `Run()` 里按条件 append 到 `cfg.Adapters`（`coding.go:431-436`） |
| read_artifact / read_note | `newReadArtifactAdapter` / `newReadNoteAdapter` | `read_artifact_tool.go:33-52` / `read_note_tool.go:20-46`；Director 在 `director.go:245-246` 注册，子 Agent 由 executor 自动补 |
| delegate_\<custom\> | `registerCustomAgent` | `director.go:632-694`，末尾 `a.Adapters = append(...)` `:692`；注册表 `director/meta_handler.go:48-59`（key = `delegate_<snake_name>`，重复注册不覆盖） |

---

## 4. 运行期：从 Adapters 到模型，再回到执行

### 子 Agent（`internal/agents/executor.go`）

| 环节 | 位置 | 事实 |
|------|------|------|
| 载体 | `:28` | `ExecutorConfig.Adapters []*tools.Adapter`，**切片，不是 map** |
| 运行期注入 | `:190-207` | ctx 带 blackboard boardID 且启用时，按名查重后补 `read_note`/`read_artifact`。这是唯一在 Agent 之外追加工具的地方 |
| 工具定义快照 | `:209-213` | `toolDefs` 在 **每次 `RunAgentLoop` 开始时算一次**并 `SortToolDefs`；循环内对 `cfg.Adapters` 的修改不会影响本回合已发出的定义 |
| 名字解析 | `:109-140` | `runToolCall` **线性扫描取第一个同名 Adapter**；未命中 → 上层写 `"Tool %s not found"`（`:698`/`:731`）回灌给模型。重名 = 先注册者赢，后注册者静默失效 |
| 错误语义 | `:128-132` | 工具报错/超时**不中断循环**，字符串化进 tool 消息：`Error: tool execution timed out after 180 seconds` / `Error: %v` |
| panic | `:721-727` | 并行批次里 recover，转成 `Error: panic: %v` |
| 落账 | `:664-692` | `commitToolCall` → `llm.Message{Role:RoleTool, ToolCallID, ToolName}` 进 messages+history+rollout，并 `Publish("tool_call_result")` |
| 收尾 | `:627-646` `:703-705` | 无 tool_call → 若 `NoActionRetryLimit` 未耗尽则注入提醒，否则以纯文本结束；`StopOnFinish && agent_exit` → 立即返回 |
| 超时 | `:74` `:121-123` | `defaultToolTimeout=180s`，仅 `isInteractiveUserTool` 豁免（`tool_timeout.go:13-15`：**只有 ask_user_for_help**） |
| 并行性 | `:650-662` | 谓词 = 非 interactive && 不在 `nonParallelToolNames` && `Adapter.IsReadOnly()`；`nonParallelToolNames`（`:76-83`）= agent_exit, delegate_meta, browser_open, browser_tabs, browser_eval |
| 调度 | `internal/agents/toolbatch/` | `Plan` 把连续可并行项合成段（`plan.go:31-56`），`Run` 用 buffered channel 当信号量、结果按原序提交（`run.go:53-85`）；并发度 `[agent] max_parallel_readonly_tools`，`NormalizeMaxParallelReadOnlyTools`：≤0→4，>8→8，**1 = 关死并发**（`:85-96`） |

### Director（自成一套）

| 环节 | 位置 | 事实 |
|------|------|------|
| 定义快照 | `director.go:973-979` → `director/planner_run.go:97-101` | `Tools.Specs()` 每次 `Planner.Run` 取一次 + 排序 |
| 执行门控 | `planner_run.go:564-593` | **只有出现在 `toolDefs` 里的名字才会被执行**；否则 `Tool %s not found`。→ 本回合 Meta 新注册的 `delegate_x`（`director.go:692` 追加到 Adapters）**要到下一次 `Planner.Run`（即下一回合）才对模型可见/可调用** |
| 名字解析 | `director.go:984-986` | 同样线性扫描 first-match |
| 超时 | `director.go:994-1008` | 普通工具 120s；`delegate_*` 用 `activity.WithIdleTimeout`（`delegate_idle_timeout`/`delegate_total_timeout`）；interactive 豁免 |
| 并行性 | `director.go:1164-1174` | 与 executor 同逻辑，读的是 `a.Adapters` |

---

## 5. 八张按工具名索引的元数据表

改一个工具的行为，实际是在改这 8 处**互不相干**的名字表；这就是"注册"的真实成本。

| # | 表 | 位置 | 决定什么 | 漏掉的后果 |
|---|-----|------|----------|-----------|
| 1 | `knownReadOnlyTools` | `tools/adapter.go:76-84` | 能否并发 | 工具能跑，但永远串行 |
| 2 | `nonParallelToolNames` | `agents/executor.go:76-83` | 强制串行 | 该并行的被串行；反之可能竞态 |
| 3 | `dangerousTools` + `pathParamNames` + `Check` 的 switch | `tools/workspace_guard.go:44-53` / `:56-61` / `:94-105` | 是否要用户授权 | **进 `dangerousTools` 但 `Check` 里没有 case → 返回 `(false,"")`，即声明无效** |
| 4 | `toolTruncationPriority` | `compression/priority.go:7-22` | 压缩时先截谁（0=先截，-1=永不截） | 默认 1（次要），`deepthinking`/`design` 是唯一受保护区 |
| 5 | `isInteractiveUserTool` | `agents/tool_timeout.go:13-15` | 是否豁免 180s | 阻塞等待用户的工具会被砍在 180s |
| 6 | `browserToolSpecs` | `tools/browser_operations.go:60-278` | 浏览器工具的 argv/渲染/URL 守卫 | `BuildAdapters` 报差异，但只 `slog.Warn` |
| 7 | `[tools.llm.*]` 槽位 | `config/config.go:129-139` + `getToolOverride:290-303` | 工具级独立模型 | **只有 micro_agent / thinking / deepthinking / design 四个槽**；其余工具只能吃 Agent 引擎 |
| 8 | TUI/consumer 的按名 switch | `tui/render.go:18-26` `skipBodyTools`；`tui/styles.go:65` `DisplayToolName`；`tui/render.go:656`、`tui/tui_render.go:566` `formatToolParams`；`messaging/consumers/tui.go:375-382` | 界面怎么显示参数/是否折叠正文 | 工具能跑，界面显示成裸 JSON。注：颜色是 `ToolBgColor` 的哈希（16 色），**不需要登记** |

另有依赖注入层：`agents/ports.go` 的窄接口 `FileToolSet:52` / `SearchToolSet:63` / `SysToolSet:69` / `EditToolSet:75` / `RepoToolSet:81` / `FlowToolSet:92` / `Thinker:99` / `DeepThinker:105` / `Designer:111` / `MicroAgentRunner:117` / `EventBus:16` / `Env:30` / `PromptFormatter:23` / `RepoContextStore:38` / `KnowledgeProvider:45` / `BrowserControl:123`；实现在 `internal/tools/*_operations.go` 等。

---

## 6. 装配顺序（`internal/app/app.go`，`sync.Once` 内）

```
:195-253  globalctx.GlobalCtx —— 构造全部工具实现（FileOps/SearchOps/SysOps/ReplaceTool/
          ThinkingTool/MicroAgentTool/FlowOps/RepoOps/DeepThinkingTool/DesignTool/CodeSeekMCP）
:256-261  KnowledgeInjector（依赖 CodeSeekMCP）
:264-265  FlowOps.UserConfirmMgr = userConfirmMgr（反问能力）
:268-272  guard = NewWorkspaceGuard(workDir, userConfirmMgr)，并把 confirmMgr 注册为事件 consumer
:275-288  YoloMode / FullYoloMode：guard.SetYoloMode(true)；FullYolo ⇒ Yolo
:290-317  各 Agent maxSteps（config.DefaultMaxSteps 兜底）
:324      disabledAgents（-d/--disable-agents，parseDisabledAgents:716）
:329-335  各 Agent 的 engine（含 RoutingEngine 包装）
:355-447  子 Agent：Repo → Chat → Meta → DevOps → repoAgent.SetDevOpsAgent(devops) → Browser
:449-472  Coding（收 Edit/RepoOps/Browser *BrowserAgent ⇒ 必须在 Browser 之后）
:475-503  Director（收全部子 Agent + Config + LLMClient）
```

**顺序约束**：① Coding 依赖 Browser 实例（`delegate_browser` 闭包）；② Repo 的 `delegate_devops` 用**晚绑定**绕开构造顺序（`:435`）；③ Director 的 `buildDelegateAdapters` 必须在结构体填充后调用（`director.go:315-318` 注释）。

---

## 7. 委派类工具的注册细节

`delegate_*` 的闭包体是固定五件事（`director.go:319-497`）：
取 `task` 参数 → `resolveDelegateRefs`（`context_refs` → ContextPack）→ `blackboard.WithBoardID` + `createRolloutWriter` → `routeCtx`（auto-routing 档位写入 ctx，发 `model_routed`）→ `XxxAgent.Run` → `writeDelegateNote` + `applyEnhancedCommander`（超预算则落 Artifact，返回摘要+ID）。

schema 由 `buildDelegateSchema(taskDesc)`（`director.go:857-870`）生成，只声明 `task` 一个必填参数。
并行性靠**显式** `WithReadOnly(true)`（如 `director.go:334` 的 delegate_repo），因为名字白名单不覆盖 `delegate_*`；写文件类委派（delegate_coding）与 `delegate_meta` 保持串行。

**Meta / 自定义 Agent 的注册链**（运行期改工具表）：
```
delegate_meta 闭包 (director.go:412-497)
 → MetaAgent 单次 LLM（无工具：GenerateContent(..., nil, ...)，meta.go:98）
 → ParseMetaAgentOutput / ExtractJSONObject (director/meta_handler.go:100/125)
 → toSnakeCase (director.go:608) → registerCustomAgent (director.go:632)
   ├─ metaHandler.Register（key 重复则整条放弃，meta_handler.go:48-59）
   ├─ 按 ToolsUsed 走 getToolFunc（director.go:571）+ toolDefMap 取 schema；未命中 → slog.Warn 跳过（:644/:649）
   ├─ SetGuardOnAdapters（:663）
   └─ a.Adapters = append(a.Adapters, delegateAdapter)（:692）
 → 立即执行一次（`director.go:466-488`：直接在 a.Adapters 里找到刚 append 的那个 delegateAdapter 并调 `ad.Call`，
   **绕过 planner 的 toolDefs 快照**，所以本回合能用；但模型要到下一回合才"看得见"它）
```

---

## 8. 现状缺口（做方案时应当正面处理的）

1. **静默失效是第一大特征**：unmarshal 失败只记日志（`coding.go:148`）、switch 未命中 `default: continue`、browser 对账差异只 `slog.Warn`、Custom Agent 的工具名不认识只 `slog.Warn`。没有任何一处让进程退出或让测试红。
2. **只有一致性闸门且覆盖面极窄**：全仓唯一硬闸门是 browser 的 `TestBrowserToolSpecsMatchToolsJSON`；其余 Agent 的"注册了什么"靠 `director_test.go:278/941`、`browser_agent_test.go:79` 这类**硬编码名单**断言，新增工具不会自动被检查。
3. **两份 schema 真相**：`consolidate_knowledge`/`prune_history`/`git_checkpoint_*` 在 tools.json 有声明，但注册时用 Go 里的 `pruneSchema`(`coding.go:454`)、`consolidateSchema`(`:507`)、checkpoint 内联 schema(`:569/591/619`) —— **改 tools.json 那 5 条不生效**（`coding.go:166` 的循环对它们走 `lookupToolFunc` default，压根没注册）。同理 `agent_exit`：Director 用的是 `director.go:238` 的手写 schema，不是 tools.json 第 18 条。
4. **Repo-Agent 没有 `agent_exit`**（`repo.go:91-121` 无该 case），`Run` 也未设 `StopOnFinish`/`NoActionRetryLimit`（`repo.go:207-228`）→ 它的收尾路径是"纯文本回复即结束"。AGENTS.md §4 声称 Repo 有 agent_exit，与代码不符。
5. **Coding 缺 `thinking` 与 `query_call_graph`**：`CodingAgentParams.Thinker`（`coding.go:98`）注入了但 `lookupToolFunc` 没有 case；`RepoToolSet.ExecuteCallGraph` 存在（`ports.go:81`）却只在 Repo 上注册（`repo.go:110`）。AGENTS.md §5.2 的"Coding 有 thinking"与 §11 坑 5 自相矛盾。
6. **只读白名单只有 7 项**，与语义无关：`find_function_callee/caller`、`query_call_graph`、`thinking`、`micro_agent`、`deepthinking`、`design`、`read_note`、`read_artifact`、`browser_inspect`/`browser_snapshot` 这些**确实无副作用**的工具都被当作有副作用 → 并发收益被限制在文件/检索四件套上。
7. **重名不会被拒绝**：`runToolCall` first-match（`executor.go:109`），Agent 侧 append 不查重（只有 executor 的 read_note/read_artifact 查）。同一 Agent 一旦注册两个同名工具，后者永久不可达。
8. **`readOnly` 与 `dangerous` 是两条独立轴**：一个工具可以既是只读又需要授权（`browser_eval` 不在 `knownReadOnlyTools`，恰好在 `dangerousTools`），也可以既危险又可并发（理论上）；当前代码没有任何地方交叉校验。
9. **guard 缺失即无检查**：`Adapter.guard == nil` 时 `Call` 跳过全部授权（`adapter.go:126`）。`SetGuardOnAdapters` 是"调用时刻的切片快照"，漏一次（如在 appender 之后才 append）就得到一个绕过 workspace 边界的工具——`coding.go:166` vs `:170`、`repo.go:150` vs `:155` 的双次调用就是在补这个。
10. **工具输出统一被 `json.Marshal`**（`adapter.go:140-144`）：string 返回值到模型/压缩层时带引号，`toolTruncationPriority` 与 TUI 渲染看到的都是 JSON 文本；纯文本工具想省 token 得自己返回 map 之外的形态。
11. **`AGENTS.md` 的数字已过期**：tools.json 是 41 条（不是 27），`Adapter` 的 `knownReadOnlyTools` 只有 7 项，`nonParallelToolNames` 有 5 项（文档写 2 项），`dangerousTools` 有 6 项含 `browser_eval`（文档 §5.4 只列 5 项）。
12. **工具级模型槽位是硬编码的 4 个**（`config.go:290-303`）：新增一个想用独立小模型的工具，除了加代码没有配置路径。

---

## 9. 新增一个工具要动的全部位置（以代码为准的清单）

```
1) 实现：internal/tools/ 里加 Execute* 或 *Tool 结构；若属于既有能力域，先扩 agents/ports.go 的窄接口
2) 装配：app.go:195-253 在 globalctx 里构造实现；再在各 *AgentParams 里注入（coding.go:88 / repo.go:42 / …）
3) 声明：tools.json 加 {name, description, parameters}——或就地 WithSchema（两条路互斥，别两边都写，见 §8.3）
4) 注册：在该 Agent 的门控里加 case（coding.go:209 / repo.go:91 / chat.go:63 / devops.go:70 /
        director.go:262 / 或 browser_operations.go:60 的 spec 表 / 或独立 appender）
        ⤷ Director 若要给 Custom Agent 用，还要改 director.go:571 的 getToolFunc
5) 元数据（§5 的 8 张表按需）：knownReadOnlyTools / nonParallelToolNames /
        dangerousTools+pathParamNames+Check 的 case / toolTruncationPriority /
        isInteractiveUserTool / [tools.llm] 槽 / TUI 的 skipBodyTools+formatToolParams
6) guard：确保它所在切片在 SetGuardOnAdapters 之后没有再被 append
7) prompt：对应 <agent>.prompt.md 里描述何时使用（注册 ≠ 模型会用）
8) 测试：internal/tools/*_test.go + agents 的执行循环测试；若加 browser 工具，
        browser_operations_test.go:88 的闸门会自动要求两边同步
9) 验证：go build ./… ; go vet ./… ; go test ./internal/agents/... ./internal/tools/... -count=1
        （scripts/check_file_sizes.sh 与 check_no_agents_import.sh 也跑一遍）
```

---

## 10. 速查索引

| 我要找 | 位置 |
|--------|------|
| 工具声明 | `internal/agents/tools.json`（41 条）+ `internal/agents/tools.go:7` |
| 声明→结构 | `internal/tools/adapter.go:19`（`ToolDefinition`） |
| Adapter 与 Call | `internal/tools/adapter.go:26` / `:118` |
| 只读白名单 | `internal/tools/adapter.go:76` |
| Coding 的门控 | `internal/agents/coding.go:209`（`lookupToolFunc`） |
| Repo / Chat / DevOps 的门控 | `repo.go:91` / `chat.go:63` / `devops.go:70` |
| Director 的两个门控 | `director.go:262`（自己）/ `director.go:571`（Custom Agent） |
| 委派注册 | `director.go:318`（6 个）/ `coding.go:179`（Coding 侧 browser）/ `repo.go:128`（Repo 侧 devops） |
| 运行期注册 | `director.go:632` + `director/meta_handler.go:48` |
| 浏览器声明式表 | `internal/tools/browser_operations.go:47`（类型）`:60`（14 条）`:292`（BuildAdapters） |
| 子 Agent 执行循环 | `internal/agents/executor.go:142`（RunAgentLoop）`:109`（按名找）`:209`（定义快照）`:650`（并行谓词） |
| Director 执行循环 | `internal/agents/director/planner_run.go:97`（快照）`:552-593`（执行门控） |
| 并发调度 | `internal/agents/toolbatch/{plan.go:31, run.go:53}` |
| 授权 | `internal/tools/workspace_guard.go:44/56/65` + `internal/tools/user_confirm.go:42`（RequestConfirmation）`:88`（RequestUserHelp）`:159`（Consume 按 request_id 配对） |
| 截断优先级 | `internal/compression/priority.go:7` |
| 工具级引擎 | `internal/llm/llm.go:589`（GetToolEngine）+ `internal/config/config.go:290` |
| Agent 装配 | `internal/app/app.go:195-253`（工具实现）`:268`（guard）`:355-503`（Agent） |
