# yagent 设计方案：文件系统即共享记忆（Agent 黑板）+ delegate context_refs

> 状态：设计已完成，待实施评审（v2 — 基于代码侦察修正）
> 日期：2026-10-10
> 范围：Agent 间通信架构改造（跨模块：delegate schema / director / 新 blackboard 包）

## 一、背景与问题诊断

当前 Agent 间通信全部经由 Director 中介：delegate 工具只有 `task` 字符串参数（`internal/agents/director.go:134-280`），Agent 接口 `Run(ctx, input string)` 也是纯字符串——Sub-Agent 之间无法直接通信，Director 被迫当"内容搬运工"。

现有通信链路存在两个"有损跳"：

```
Sub-Agent 完整输出
  └─ 跳1: FinalizeResult → artifact 落盘 + 500 token 摘要 (result.go:48-127)
        └─ Director 上下文只留摘要 + artifact_id
              └─ 跳2: Director 重写 delegate(task 字符串) → 下游
                    └─ 没转述到的 = 永久丢失（artifact_id 甚至从没被下游用过）
```

根因分层：

| 层级 | 根因 | 性质 |
|---|---|---|
| R1 架构 | 星型拓扑下 Director 同时承担"路由"与"传输通道"，上下文窗口是唯一媒介 | 主因（拓扑缺陷） |
| R2 接口 | `Run(ctx, input string)` 与 `delegate(task string)` 均为纯字符串，无结构化引用传递能力 | 主因（表达力缺陷） |
| R3 身份 | Agent 不知道自己属于哪个 session/task，无隔离域，无法落盘到"本次会话的共享区" | 主因（缺失维度） |
| R4 语义 | artifact 有"落盘"无"跨 Agent 可寻址传递"语义；路径按 project 而非 session 组织 | 次因（能力缺口） |
| R5 配置 | RepoContextStore 硬编码 repo→coding/browser 两向透传，不可泛化 | 次因 |

定性：这不是 bug 修复，而是**拓扑缺陷的最小修补**——引入一个按 session 隔离、可寻址、可结构化引用的共享落盘层，让 Director 从"内容搬运工"退化为"引用传递者"。

## 二、方案对比与选型

- 方案 A：统一存储——artifact 迁入 notes/{board}/，单一落盘体系。优：概念单一。劣：read_artifact 路径逻辑、历史数据迁移、project 维度检索全部受冲击，爆炸半径大。**否决（留作长期演进方向）**。
- 方案 B：引用层——notes 是 session 视图（元数据+摘要+artifact 引用），artifact 不动。优：零迁移、零接口变更、复用 artifact/read_artifact/工具/executor 全部现有设施，改动集中在 director.go + 一个新包。劣：存在 note_id / artifact_id 两个 ID 空间（用前缀语法 + 结果头同时标注两者消解）。**✅ 采纳**。
- 方案 C：全共享工作区（agents 自由读写同一目录）。劣：并发冲突、读写顺序、权限、Agent 行为不可控，违背最小化原则。**否决**。

选 B，并预留向 A 演进的 hook（note 元数据已含 artifact_ids，未来 artifact 元数据可补 boardID 字段，无需迁移即可归组）。

## 三、详细设计

### 3.1 关键不变式

| 不变式 | 内容 |
|---|---|
| I1 | `Agent` 接口零改动 |
| I2 | artifact 路径与 ID 零迁移，read_artifact 零改动 |
| I3 | notes 引用不拷贝；并行安全靠"唯一文件名+原子 rename"，不引入共享索引文件 |
| I4 | 注入默认预算内自动内联；超限降级为索引行 + 按需读 |
| I5 | boardID 缺失 → 不在主流程报错：写 note 跳过（debug 日志）；Director 实例级兜底生成一次性 `adhoc_<ts>_<rand4>` |
| I6 | Meta-Agent 自定义 delegate 工具自动继承全部行为（schema 共用同一 builder） |
| I7 | Phase 1 协议零改动，无新 WS 事件 |
| I8 | read_note / read_artifact 由 executor 层自动注入：当 ctx 携带 boardID 时，`RunAgentLoop` 自动追加这两个工具到 sub-agent 的 adapter 列表（去重），sub-agent 可自助读取截断内容，无需经 Director 中转 |

### 3.2 boardID 统一机制（TUI/HTTP 双模式）

> **v2 修正**：原设计误用 rollout_writer 的 `generateSessionID()`（`YYYYMMDD_HHMMSS_<8hex>`）作为 TUI boardID。实际上该 ID 是**每次 delegate 调用**创建 RolloutWriter 时独立生成的（`director.go:818 createRolloutWriter`），不是会话级 ID。正确的 boardID 来源是 `a.taskID`——TUI 和 HTTP 均在任务启动时通过 `SetTaskID` 注入，且两者都是 UUID v4。

| 模式 | ID 来源 | boardID | 注入点 |
|---|---|---|---|
| TUI | `tui_tasks.go:46` `uuid.New().String()` → `app.NewTaskRequest(ctx, taskID)` → `director.SetTaskID(taskID)` | 同 taskID（UUID v4） | `ProcessCodingTaskWithCallback` 调用 `SetTaskID` 后、`Run` 前 |
| HTTP | `task_manager.go:44` `uuid.New().String()` → 同上链路 | 同 taskID（UUID v4） | 同上 |
| 兜底 | 无 | Director 构造时生成 `adhoc_<ts>_<rand4>`（进程内一次） | `NewDirectorAgent` 构造函数末尾 |

传递链：`SetTaskID(taskID)` → Director 内部 `a.taskID` → delegate handler 内 `blackboard.WithBoardID(ctx, a.taskID)` → 写 note / 标注 header。

TUI resume 场景：resume 时复用同一 taskID（从 rollout 日志恢复），notes 目录天然可寻址，无需额外设计。

**adhoc 兜底实现**：在 `NewDirectorAgent` 末尾，若 `a.taskID == ""`（理论上不应发生，但防御性编程），生成 `adhoc_<unix_ts>_<rand4>` 并存入 `a.taskID`。这保证 blackboard 包永远不需要处理空 boardID。

### 3.3 黑板目录结构与 note 文件格式

```
~/.yagent/notes/{boardID}/
└── {agent_name}/                     # 每 agent 一个子目录，支撑 "agent:<name>" 粗粒度引用
    └── 20250601T090000Z_a1b2.md      # 文件名 = UTC 紧凑时间戳 + 4 位随机后缀（唯一+可排序）
~/.yagent/data/artifacts/{projectID}/ # 不动；note 内以 ID 引用
```

> **v2 修正**：代码侦察确认 `~/.yagent` 无统一 base helper（各模块独立构造：`artifact/store.go:59`、`datamanager/data_manager.go:56`、`memory/rollout_writer.go:74`）。blackboard 包自建 `NotesRootDir()` helper，模式与 `artifact.DefaultStore()` 对齐：优先读环境变量 `YAGENT_NOTES_ROOT`（测试用），否则 `filepath.Join(homeDir, ".yagent", "notes")`，homeDir 失败时降级为 `"."`。

note 文件 = JSON front-matter + Markdown body（两行 `---` 之间为一个 JSON 对象，stdlib 编解码，无新依赖）。front-matter 字段：

| 字段 | 说明 |
|---|---|
| version | 格式版本（=1），为未来演进留位 |
| note_ref | `{agent}/{filename-stem}`，即 context_refs 中 `note:` 后的值 |
| board_id | 自描述，防拷贝/移动后失联 |
| agent | 产出者 agent 名 |
| type | `deliverable`（Phase 1 唯一）；预留 `handoff`/`context` |
| created_at | RFC3339 UTC |
| tool | 来源工具名（`delegate_repo` / `delegate_coding` / `delegate_{custom}` 等） |
| task | 原始委派任务文本（截断至 300 字符，供下游理解意图） |
| summary | ≤200 字符一句话摘要（注入索引用） |
| artifact_ids | []string，关联 artifact ID |
| body_size | 字符数，供预算裁剪决策 |
| status | `ok` / `error`（agent.Run 失败时也留痕，便于下游知情） |

body 规则：有 artifact → body = 摘要+要点（500 token 级，不复制 artifact）；无 artifact（小结果）→ body = 完整结果文本（磁盘便宜，注入侧再裁剪，保证零丢失）。

**note 写入发生在 director 的 delegate handler 内（Run 返回后）**，不在 FinalizeResult 内——完全不需要动 result.go 的生成逻辑和其它所有 agent 实现，改动面最小。

**原子写实现**（与 `artifact/store.go:144 write()` 对齐）：
1. `os.MkdirAll(dir, 0o700)`
2. 写 `{filename}.md.tmp`（0o600）
3. `os.Rename` 到最终路径
4. rename 失败时 `os.Remove` tmp 文件

### 3.4 delegate schema 变更与可引用 ID 标注

`delegate` 与 `delegate_{custom}` 共用的 schema builder 增加可选数组参数 `context_refs: string[]`，引用语法：

| ref 形态 | 语义 | 解析行为 |
|---|---|---|
| `agent:<name>` | 该 agent 在本 board 的全部交付物 | 取最新 ≤3 条 |
| `note:<agent>/<stem>` | 具体一条 note | 取 1 条 |
| `artifact:<id>` | 已有 artifact（不复制内容） | 校验存在，注入索引行 + read_artifact 提示 |

**schema 变更点**（共 7 处，全部在 `director.go`）：

| 位置 | 工具 | 行号（当前） |
|---|---|---|
| `NewDirectorAgent` | delegate_repo | 161-167 |
| `NewDirectorAgent` | delegate_coding | 185-191 |
| `NewDirectorAgent` | delegate_chat | 206-212 |
| `NewDirectorAgent` | delegate_devops | 227-233 |
| `NewDirectorAgent` | delegate_browser | 252-261 |
| `NewDirectorAgent` | delegate_meta | 357-363 |
| `registerCustomAgent` | delegate_{custom} | 718-724 |

> **v2 改进**：抽取 `buildDelegateSchema(description string) map[string]interface{}` 辅助函数，7 处共用，避免重复且保证 context_refs 定义一致。

结果头部格式升级：

```
[Sub-Agent Result: repo_agent] note: repo_agent/20250601T090000Z_a1b2 (1832 chars)
  | artifact: art_7f3e (48210 chars total) | unresolved refs: 0
<500 token 摘要照旧>
```

- 实现方式：`FormatForDirector`（`result.go:110-125`）增加可选 `noteRef string` 参数，空值时输出与现状字节级一致。生产调用点恰好 2 处（`director.go:796 injectSubAgentMemory`、`director.go:886 applyEnhancedCommander`），改动面极小。
- `injectSubAgentMemory` 调用时传空字符串（memory 摘要不需要 noteRef 头部）。
- `applyEnhancedCommander` 增加 `noteRef string` 参数，由 delegate handler 传入。
- `unresolved refs: N`：Director 传入的无效 ref 被忽略但被告知，便于自纠正。
- Director 系统提示增加一段简短指引："收到 [Sub-Agent Result] 头部的 note:/artifact: ID 后，下游任务直接放入 context_refs，不要转述内容；要求精确数值/路径的接力任务必须携带 refs。"

### 3.5 注入策略：混合式（自动索引+预算内内联 + 按需读全文）

注入动作发生在 delegate handler：解析 refs → 组装 Context Pack → 前置拼入 `task` 字符串首位 → 调 `agent.Run(ctx, 拼接后输入)`。System prompt / ExecutorConfig / executor.go 零改动。

> **v2 关键修正**：Context Pack 仅拼入传给 `subAgent.Run` 的 task 字符串。用于 rollout writer（`createRolloutWriter(kind, task)`）和 note 元数据（`task` 字段）的仍是**原始 task**，保证日志和 note 记录不被注入内容污染。

Context Pack 结构（文本示意）：

```
## Referenced Context (blackboard)
[note] repo_agent/2025..._a1b2 — 摘要一行 — body 在预算内则内联（≤2000 字符/条）
[note] repo_agent/2025..._c3d4 — (content truncated, call read_note(ref="repo_agent/2025..._c3d4") for full text)   ← 超预算降级
[artifact] art_7f3e (48210 chars) — 请调用 read_artifact("art_7f3e") 获取全文
[unresolved] note:foo/bar — ignored
---
（原文 task 逐字保留在后）
```

预算默认值（常量，Phase 2 再配置化）：总注入 ≤6000 字符；单 note 内联 ≤2000 字符；`agent:` 引用最多 3 条；refs 最多 10 个；重复 ref 去重。

**按需读取**：

> **v3 修正**：read_note / read_artifact 由 executor 层自动注入——`RunAgentLoop` 检测到 ctx 携带 boardID 时，自动追加这两个工具到 sub-agent 的 adapter 列表（去重检查防止 Director 自身重复注册）。Sub-Agent 看到 Context Pack 中截断的 note 或 artifact 引用时，可直接调用 read_note / read_artifact 自助获取全文，无需经 Director 中转重新 delegate。

- artifact 全文：sub-agent 直接调用 `read_artifact`（executor 自动注入）
- 被裁剪的 note 全文：sub-agent 直接调用 `read_note`（executor 自动注入，镜像 `read_artifact_tool.go` 实现）

### 3.6 自定义 Agent（registerCustomAgent）的特殊处理

> **v2 新增**：代码侦察发现自定义 Agent 的 delegate 路径与标准 delegate 不同，需要单独处理。

标准 delegate 路径：
```
handler → subAgent.Run(ctx, task) → AgentResult → applyEnhancedCommander(kind, task, result, err) → FormatForDirector
```

自定义 Agent 路径（`director.go:711-724`）：
```
handler → executeCustomAgent(ctx, ca, adapters, task) → (string, error)
```

`executeCustomAgent`（`director.go:733`）内部已调用 `setPendingSubAgentMemory(&agentResult)`，但返回的是原始 string，不经过 `applyEnhancedCommander`。

**处理方案**：在自定义 Agent 的 delegate handler 闭包内，`executeCustomAgent` 返回后：
1. 从 `takePendingSubAgentMemory()` 取出 `AgentResult`（已有机制）
2. 写 note（与标准路径相同逻辑）
3. 调用 `applyEnhancedCommander(ca.Name, task, result, err, noteRef)` 统一格式化返回

这样自定义 Agent 与标准 Agent 的 note 写入和结果格式化完全对齐，无需修改 `executeCustomAgent` 内部逻辑。

### 3.7 与 artifact / RepoContextStore 的整合决策

| 对象 | 决策 | 理由 |
|---|---|---|
| artifact 存储 | 完全不动（Phase 1） | 已有落盘+工具+历史数据；notes 通过 ID 引用即可形成 board→artifact 映射 |
| read_artifact | 完全不动 | 下游读全文的按需通道现成 |
| RepoContextStore | 冻结现状，标记 superseded（Phase 1 保留 `coding.go:369` / `browser_agent.go:188` 不动） | 泛化重写=高改动面；Phase 3 可选：repo 内容镜像为 `type=context` 的 note，coding/browser 改经注入机制读取后再删旧通路 |
| 两套落盘混乱问题 | 以不变式 I3 消解：全文唯一；note 只是元数据+摘要+引用 | 杜绝重复拷贝 |
| 并发 | 唯一文件名 + temp+Rename 原子写；无共享索引文件（Phase 1），list+sort 即索引 | 无锁、无损坏窗口 |
| 生命周期 | 目录永久保留（支持 TUI resume）；Phase 3 增加 `notes prune`（默认 TTL 30 天，手动/启动可选）；`.tmp` 残留由 prune 兜底 | 不自动删除，杜绝数据丢失事故 |

## 四、实施计划（依赖顺序 S0→S5）

**S0 — 侦察确认（已完成，结论如下）**

| 假设 | 验证结果 | 影响 |
|---|---|---|
| ① delegate handler 是唯一子 Agent 调用咽喉 | ✅ 确认。标准 delegate 6 个 + 自定义 delegate 均经 handler 闭包。`executeCustomAgent` 是自定义 Agent 的唯一入口。 | 无需补充注入口 |
| ② FormatForDirector 调用点数量 | ✅ 恰好 2 处生产调用（`director.go:796`、`director.go:886`） | 改动面极小，直接加参数 |
| ③ read_artifact 对全部 agent 可见 | ❌→✅ **v3 已解决**：原仅 Director 可见（`director.go:376`）。现由 executor 层自动注入：`RunAgentLoop` 检测 ctx 携带 boardID 时追加 read_note + read_artifact（去重），所有 sub-agent 均可自助读取。 | 不变式 I8 更新 |
| ④ `~/.yagent` base 目录存在统一解析 helper | ❌ **无统一 helper**。各模块独立构造（artifact/store.go、datamanager、rollout_writer）。 | blackboard 包自建 `NotesRootDir()` |
| ⑤ TUI sessionID 在首次 delegate 前可用 | ⚠️ **修正**：TUI 使用 `uuid.New().String()` 作为 taskID（`tui_tasks.go:46`），经 `SetTaskID` 注入 Director。rollout_writer 的 sessionID 是每次 delegate 独立生成的，不是会话级 ID。 | boardID = taskID（UUID），非 rollout sessionID |
| ⑥ go.mod 无 yaml 依赖 | ✅ 确认。采用 JSON front-matter。 | 无新依赖 |
| ⑦ FormatForDirector 调用点在 1-3 处 | ✅ 恰好 2 处 | 无需退路方案 |

**S1 — blackboard 包（纯新增，不接线，可独立编译测试）**

| 文件 | 内容 |
|---|---|
| `internal/blackboard/board.go` | `NotesRootDir()`（env `YAGENT_NOTES_ROOT` → `~/.yagent/notes` → `"."` 降级）；boardID 清洗/校验（`[A-Za-z0-9_.-]`，长度 ≤128）；目录懒创建；原子写（temp+rename，与 artifact/store.go 对齐） |
| `internal/blackboard/note.go` | `Note` 结构体、JSON front-matter 编解码（`---\n{json}\n---\n{body}`）、文件名生成（`20060102T150405Z_<rand4>.md`）、`WriteNote(boardID, agent, task string, result AgentResult) (noteRef string, err error)`、`ReadNote(boardID, noteRef string) (Note, error)`、`ListNotes(boardID, agent string, limit int) ([]NoteMeta, error)` |
| `internal/blackboard/context.go` | `WithBoardID(ctx, id) context.Context` / `BoardIDFrom(ctx) string` / `EnsureBoardID(ctx, fallback func() string) context.Context` |
| `internal/blackboard/resolve.go` | 三种 ref 语法解析（`agent:`/`note:`/`artifact:`）、`ResolveRefs(boardID string, refs []string, artifactExists func(string) bool) (pack ContextPack, unresolved []string)`、预算裁剪逻辑、ContextPack 文本组装 |
| `internal/blackboard/*_test.go` | 见第五节测试矩阵 |

验证：`go build ./... && go test ./internal/blackboard/ -v -count=1`。

**S2 — 入口接线（小改动）**

| 文件 | 动作 | 验证 |
|---|---|---|
| `internal/agents/director.go` | ① `DirectorAgent` struct 增加 `boardID string` 字段；② `SetTaskID` 同时设置 `a.boardID = taskID`；③ `NewDirectorAgent` 末尾：若 `a.boardID == ""` 则生成 adhoc 兜底 | 单测：无 SetTaskID 时仍能产出 note 到 adhoc 目录 |
| `internal/agents/director.go` | 所有 delegate handler 闭包内：`ctx = blackboard.WithBoardID(ctx, a.boardID)` 后再调 `subAgent.Run` | TUI 跑一次带 delegate 的会话，观察 `~/.yagent/notes/{taskID}/` 出现 |

> **v2 修正**：原设计计划在 `app.go` 和 `task_manager.go` 注入 boardID 到 ctx。实际上 Director 已有 `a.taskID` 字段（`SetTaskID` 注入），无需修改上游入口。改动完全收敛在 `director.go` 内部。

**S3 — 核心链路（最重一步，集中在 director.go + result.go + prompt）**

| 文件 | 动作 | 验证 |
|---|---|---|
| `internal/agents/director.go` | ① 抽取 `buildDelegateSchema(desc string) map[string]interface{}` 辅助函数（含 `context_refs` 可选数组参数），7 处 delegate schema 统一调用；② 标准 delegate handler：取 `context_refs` → `blackboard.ResolveRefs` → 组装 Context Pack → 前置拼入 task → `subAgent.Run(ctx, packedTask)` → 写 note → `applyEnhancedCommander(kind, originalTask, result, err, noteRef)`；③ 自定义 Agent delegate handler：`executeCustomAgent` 返回后 → `takePendingSubAgentMemory` → 写 note → `applyEnhancedCommander` | 集成测试：假 agent A 产出 → 结果头含 note ref → 再 delegate B 带该 ref → 捕获 B 收到的 input 含 Context Pack 且 task 原文完整 |
| `internal/agents/result.go` | `FormatForDirector(toolName string, r AgentResult, noteRef string) string`：noteRef 为空时输出与旧格式字节级一致；非空时在 header 行追加 `note: {noteRef} ({bodySize} chars)` | 黄金输出测试：noteRef 为空时与旧输出逐字节一致 |
| `internal/agents/director.go` | `applyEnhancedCommander` 增加 `noteRef string` 参数，透传给 `FormatForDirector`；`injectSubAgentMemory` 调用 `FormatForDirector` 时传空字符串 | 编译通过 + 现有测试不破坏 |
| Director 系统提示文件（`internal/agents/director.prompt.md`） | 增加 ~5 行 context_refs 使用指引 | TUI 会话走 repo→coding 接力，检查 Director 确实发出 context_refs（看日志） |
| 全链路日志 | debug 级：boardID、refs 解析结果、note 写入路径、预算裁剪量、unresolved refs | 可观测性验收 |

**S4 — read_note 工具（小步）**

新建 `internal/agents/read_note_tool.go`，镜像 `read_artifact_tool.go` 的实现：

- 工具名：`read_note`
- 参数：`ref`（string，必填，格式 `<agent>/<stem>`）、`start_line`（int，可选）、`max_lines`（int，可选）
- 实现：从 ctx 取 boardID → `blackboard.ReadNote(boardID, ref)` → 分页返回 body
- 注册：`director.go` adapter 列表中 `newReadArtifactAdapter()` 旁增加 `newReadNoteAdapter()`
- 可见性：executor 层自动注入（`RunAgentLoop` 检测 ctx boardID → 追加 read_note + read_artifact，去重），所有 sub-agent 均可自助读取

验证：单测 + 裁剪场景 E2E（注入降级后 Director 能自助读全文再 re-delegate）。

**S5 — 加固（可选同 PR）**

- 预算常量集中到 `blackboard/budget.go`
- 写失败降级路径测试（只读 FS / 磁盘满 → agent 主流程不失败，仅 slog.Warn）
- `notes/` 磁盘用量日志（debug 级，启动时统计）
- Kill-switch：环境变量 `YAGENT_BLACKBOARD=0` + config.toml `[blackboard] enabled = false`（双保险）
- README 一节说明黑板语义与清理方式

Phase 3（不在本次必须范围）：`notes prune` 命令、RepoContextStore 收编、note_created UI 事件、注入预算配置化。

## 五、验证策略（测试矩阵）

| 层 | 用例 | 期待 |
|---|---|---|
| 单元 | front-matter 编解码往返、损坏文件容错 | 无损；损坏文件跳过不 panic |
| 单元 | 并行 20 goroutine 同 agent 写 note；读并发 | 全部落盘、文件名无冲突、reader 永不见半文件（原子 rename） |
| 单元 | boardID 清洗（非法字符/超长/空）与 adhoc 兜底 | 目录名安全；空 board 不崩溃 |
| 单元 | ref 解析：三种语法 + 非法 + 重复 + 不存在的 note/artifact | 非法项进 unresolved，不影响其余 |
| 单元 | 预算裁剪：0/1/多条/超大 note | 总量 ≤ 预算；降级行出现 |
| 单元 | `FormatForDirector` noteRef 为空时黄金输出 | 与旧格式逐字节一致 |
| 集成 | 标准 delegate A → 头含 note ref → delegate B 带 ref | B 的 input 含 Context Pack；task 原文逐字保留在后 |
| 集成 | 自定义 agent（registerCustomAgent）注册后的 schema | 含 context_refs；delegate_{name} 行为与标准 delegate 一致 |
| 集成 | 自定义 agent delegate → 写 note → 结果头含 noteRef | 与标准 delegate 输出格式一致 |
| 集成 | rollout writer 记录的 task 是原始 task（不含 Context Pack） | 日志不被注入内容污染 |
| E2E (TUI) | repo_agent→coding_agent 真实接力 | `notes/{taskID}/` 有两条交付物；coding 读到上游文件路径等关键信息 |
| E2E (HTTP) | 双并行 task | 两个 UUID 目录互不干扰；并行 delegate 无冲突 |
| 边界 | 只读 FS / 磁盘满 | agent 主流程不失败，仅 slog.Warn |
| 边界 | TUI resume 旧 session（同 taskID） | 旧 notes 可继续被引用 |
| 边界 | kill-switch `YAGENT_BLACKBOARD=0` | delegate 完全回到现状（不解析 refs、不写 note、头部不变） |

## 六、风险与回滚

- **一级回滚（免重编译）**：环境变量 `YAGENT_BLACKBOARD=0` 或 config `[blackboard] enabled = false` → delegate 完全回到现状（不解析 refs、不写 note、头部不变）。实现：delegate handler 入口检查开关，关闭时跳过所有 blackboard 逻辑。
- **二级回滚**：回滚二进制即可——所有改动纯增量：无 artifact 迁移、无协议变更、无 schema 破坏（delegate 旧调用方不传 context_refs 时行为与今天完全一致）。
- **残留物**：orphan 的 `notes/` 目录无害且可人工删除；`.tmp` 残留由 rename 语义保证不产生"半成品正式文件"。
- **触发回滚的条件**：token 消耗显著上升（注入预算失控）、注入内容引发下游 agent 行为异常、写盘影响主流程延迟明显。

## 七、显式假设（S0 已验证，保留供后续参考）

1. ✅ delegate handler 是子 Agent 执行的唯一咽喉（含 Meta-Agent 路径）。标准 delegate 6 个 + 自定义 delegate 均经 handler 闭包。
2. ✅ `Agent.Run` 的 ctx 在全部调用链中可用且被透传。
3. ✅→v3 修正：read_artifact 原仅 Director 可见（`director.go:376`）。现由 executor 层自动注入（`RunAgentLoop` 检测 ctx boardID），read_note + read_artifact 对所有 sub-agent 可见，sub-agent 可自助读取截断内容。
4. ❌→修正：`~/.yagent` **无统一 base helper**。blackboard 包自建 `NotesRootDir()`，模式与 `artifact.DefaultStore()` 对齐。
5. ⚠️→修正：TUI 使用 `uuid.New().String()` 作为 taskID（`tui_tasks.go:46`），经 `SetTaskID` 注入 Director。boardID = taskID（UUID v4），非 rollout sessionID。
6. ✅ go.mod 无 yaml 依赖 → 采用 JSON front-matter。
7. ✅ FormatForDirector 生产调用点恰好 2 处（`director.go:796`、`director.go:886`），无需退路方案。

## 附录 A：delegate handler 改造伪代码

```go
// 标准 delegate handler（以 delegate_repo 为例）
func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
    task, ok := params["task"].(string)
    if !ok { return nil, fmt.Errorf("task parameter required") }

    // 1. 解析 context_refs（可选）
    refs := parseContextRefs(params["context_refs"])

    // 2. 注入 boardID 到 ctx
    ctx = blackboard.WithBoardID(ctx, a.boardID)

    // 3. 解析 refs → 组装 Context Pack
    pack, unresolved := blackboard.ResolveRefs(a.boardID, refs, artifactExistsFn)
    packedTask := task
    if pack.Text() != "" {
        packedTask = pack.Text() + "\n---\n" + task
    }

    // 4. 创建 rollout writer（使用原始 task）
    rw := a.createRolloutWriter("repo", task)
    ctx = memory.WithRolloutWriter(ctx, rw)

    // 5. 执行 Sub-Agent（使用拼接后 task）
    result, err := repoAgent.Run(ctx, packedTask)

    // 6. 写 note（使用原始 task 作为元数据）
    noteRef, _ := blackboard.WriteNote(a.boardID, "repo_agent", task, result)
    // 写失败仅 slog.Warn，不阻断主流程

    // 7. 格式化返回（携带 noteRef + unresolved 计数）
    return a.applyEnhancedCommander("repo", task, result, err, noteRef, len(unresolved))
}
```

## 附录 B：文件改动清单

| 文件 | 改动类型 | 改动量估算 |
|---|---|---|
| `internal/blackboard/board.go` | 新增 | ~80 行 |
| `internal/blackboard/note.go` | 新增 | ~150 行 |
| `internal/blackboard/context.go` | 新增 | ~40 行 |
| `internal/blackboard/resolve.go` | 新增 | ~120 行 |
| `internal/blackboard/*_test.go` | 新增 | ~300 行 |
| `internal/agents/director.go` | 修改 | ~80 行增量（schema builder + handler 改造 + read_note 注册） |
| `internal/agents/result.go` | 修改 | ~10 行（FormatForDirector 加参数） |
| `internal/agents/read_note_tool.go` | 新增 | ~80 行 |
| `internal/agents/director.prompt.md` | 修改 | ~5 行 |
| 总计 | | ~860 行新增，~90 行修改 |
