# yagent 设计方案：文件系统即共享记忆（Agent 黑板）+ delegate context_refs

> 状态：设计已完成，待实施评审
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

### 3.2 boardID 统一机制（TUI/HTTP 双模式）

| 模式 | 现有 ID | boardID | 注入点 |
|---|---|---|---|
| TUI | session_id `20060102_150405_xxxxxxx`（internal/memory/rollout_writer.go） | 同 session_id | app.go 会话启动处写入 ctx 再启动 Director |
| HTTP | task_id uuid（internal/http/task_manager.go:43） | 同 task_id | task 创建后、spawn 运行时写入 ctx |
| 兜底 | 无 | Director 构造时生成 `adhoc_<ts>_<rand4>`（进程内一次） | director 构造函数 |

传递链：入口 → `blackboard.WithBoardID(ctx)` → Director 的所有 delegate 调用透传 ctx → delegate handler 内 `BoardIDFrom(ctx)` → 写 note / 标注 header。TUI resume 场景天然复用同一目录，无需额外设计。

### 3.3 黑板目录结构与 note 文件格式

```
~/.yagent/notes/{boardID}/
└── {agent_name}/                     # 每 agent 一个子目录，支撑 "agent:<name>" 粗粒度引用
    └── 20250601T090000Z_a1b2.md      # 文件名 = UTC 紧凑时间戳 + 4 位随机后缀（唯一+可排序）
~/.yagent/data/artifacts/{projectID}/ # 不动；note 内以 ID 引用
```

note 文件 = JSON front-matter + Markdown body（两行 `---` 之间为一个 JSON 对象，stdlib 编解码，无新依赖）。front-matter 字段：

| 字段 | 说明 |
|---|---|
| version | 格式版本（=1），为未来演进留位 |
| note_ref | `{agent}/{filename-stem}`，即 context_refs 中 `note:` 后的值 |
| board_id | 自描述，防拷贝/移动后失联 |
| agent | 产出者 agent 名 |
| type | `deliverable`（Phase 1 唯一）；预留 `handoff`/`context` |
| created_at | RFC3339 UTC |
| tool | 来源工具名（`delegate` 或 `delegate_{custom}`） |
| task | 原始委派任务文本（截断至 300 字符，供下游理解意图） |
| summary | ≤200 字符一句话摘要（注入索引用） |
| artifact_ids | []，关联 artifact |
| body_size | 字符数，供预算裁剪决策 |
| status | ok / error（agent.Run 失败时也留痕，便于下游知情） |

body 规则：有 artifact → body = 摘要+要点（500 token 级，不复制 artifact）；无 artifact（小结果）→ body = 完整结果文本（磁盘便宜，注入侧再裁剪，保证零丢失）。

**note 写入发生在 director 的 delegate handler 内（Run 返回后）**，不在 FinalizeResult 内——完全不需要动 result.go 的生成逻辑和其它所有 agent 实现，改动面最小。

### 3.4 delegate schema 变更与可引用 ID 标注

`delegate` 与 `delegate_{custom}` 共用的 schema builder 增加可选数组参数 `context_refs: string[]`，引用语法：

| ref 形态 | 语义 | 解析行为 |
|---|---|---|
| `agent:<name>` | 该 agent 在本 board 的全部交付物 | 取最新 ≤3 条 |
| `note:<agent>/<stem>` | 具体一条 note | 取 1 条 |
| `artifact:<id>` | 已有 artifact（不复制内容） | 校验存在，注入索引行 + read_artifact 提示 |

结果头部格式升级：

```
[Sub-Agent Result: repo_agent] note: repo_agent/20250601T090000Z_a1b2 (1832 chars)
  | artifact: art_7f3e (48210 chars total) | unresolved refs: 0
<500 token 摘要照旧>
```

- 实现方式：`FormatForDirector`（result.go:48-127）增加可选 noteRef 参数，空值时输出与现状字节级一致；若调用点意外地多，退路是在 delegate handler 里对头部做一次性文本拼接，不扩散改动。
- `unresolved refs: N`：Director 传入的无效 ref 被忽略但被告知，便于自纠正。
- Director 系统提示增加一段简短指引："收到 [Sub-Agent Result] 头部的 note:/artifact: ID 后，下游任务直接放入 context_refs，不要转述内容；要求精确数值/路径的接力任务必须携带 refs。"

### 3.5 注入策略：混合式（自动索引+预算内内联 + 按需读全文）

注入动作发生在 delegate handler：解析 refs → 组装 Context Pack → 前置拼入 `task` 字符串首位 → 调 `agent.Run(ctx, 拼接后输入)`。System prompt / ExecutorConfig / executor.go 零改动。

Context Pack 结构（文本示意）：

```
## Referenced Context (blackboard)
[note] repo_agent/2025..._a1b2 — 摘要一行 — body 在预算内则内联（≤2000 字符/条）
[note] repo_agent/2025..._c3d4 — (moved to index only, use read_note)   ← 超预算降级
[artifact] art_7f3e (48210 chars) — 请调用 read_artifact("art_7f3e") 获取全文
[unresolved] note:foo/bar — ignored
---
（原文 task 逐字保留在后）
```

预算默认值（常量，Phase 2 再配置化）：总注入 ≤6000 字符；单 note 内联 ≤2000 字符；`agent:` 引用最多 3 条；refs 最多 10 个；重复 ref 去重。

按需读取：artifact 全文走现有 read_artifact（零改动）；被裁剪的 note 走新增 read_note 工具（镜像 read_artifact_tool.go 的实现与注册方式，保证自定义 Agent 同样可见）。

### 3.6 与 artifact / RepoContextStore 的整合决策

| 对象 | 决策 | 理由 |
|---|---|---|
| artifact 存储 | 完全不动（Phase 1） | 已有落盘+工具+历史数据；notes 通过 ID 引用即可形成 board→artifact 映射 |
| read_artifact | 完全不动 | 下游读全文的按需通道现成 |
| RepoContextStore | 冻结现状，标记 superseded（Phase 1 保留 coding.go:369 / browser_agent.go:188 不动） | 泛化重写=高改动面；Phase 3 可选：repo 内容镜像为 `type=context` 的 note，coding/browser 改经注入机制读取后再删旧通路 |
| 两套落盘混乱问题 | 以不变式 I3 消解：全文唯一；note 只是元数据+摘要+引用 | 杜绝重复拷贝 |
| 并发 | 唯一文件名 + temp+Rename 原子写；无共享索引文件（Phase 1），list+sort 即索引 | 无锁、无损坏窗口 |
| 生命周期 | 目录永久保留（支持 TUI resume）；Phase 3 增加 `notes prune`（默认 TTL 30 天，手动/启动可选）；`.tmp` 残留由 prune 兜底 | 不自动删除，杜绝数据丢失事故 |

## 四、实施计划（依赖顺序 S1→S5）

**S0 — 侦察确认（只读不改）**：grep/阅读确认 6 项：① delegate handler 是否唯一子 Agent 调用咽喉（含 registerCustomAgent 路径）；② FormatForDirector 调用点数量；③ read_artifact 的注册点与自定义 Agent 的可见性；④ app.go 中 sessionID 生成与 Director 构造的先后顺序；⑤ Director 系统提示文本所在文件；⑥ `~/.yagent` base 目录解析的现有 helper。任何一项与假设不符时按预案退路调整。

**S1 — blackboard 包（纯新增，不接线，可独立编译测试）**

| 文件 | 内容 |
|---|---|
| internal/blackboard/board.go | 根路径解析（复用 base helper）、boardID 清洗/校验（`[A-Za-z0-9_.-]`，长度 ≤128）、目录懒创建、原子写（temp+rename） |
| internal/blackboard/note.go | Note 结构、JSON front-matter 编解码、文件名生成（时间戳+rand4）、从 AgentResult+task 构造 note、读取 |
| internal/blackboard/context.go | WithBoardID / BoardIDFrom / adhoc 兜底生成 |
| internal/blackboard/resolve.go | 三种 ref 语法解析、ContextPack 组装、预算裁剪、artifact 存在性校验（定义最小接口避免包循环依赖） |
| internal/blackboard/*_test.go | 见第五节测试矩阵 |

验证：`go build ./... && go test ./internal/blackboard/`。

**S2 — 入口接线（小改动）**

| 文件 | 动作 | 验证 |
|---|---|---|
| internal/app/app.go | 会话启动处将 sessionID 写入 ctx 后传给 Director | TUI 跑一次带 delegate 的会话，观察 ~/.yagent/notes/{session_id}/ 出现 |
| internal/http/task_manager.go | 创建 task_id 后写入 ctx | HTTP 服务跑一个任务，观察 notes/{uuid}/ |
| internal/agents/director.go | 构造函数：ctx 无 boardID 时生成 adhoc 兜底（不改函数签名，Director struct 存字段） | 单测：无 ctx 注入时仍能产出 note 到 adhoc 目录 |

**S3 — 核心链路（最重一步，集中在 director.go + result.go + prompt）**

| 文件 | 动作 | 验证 |
|---|---|---|
| internal/agents/director.go | ① schema builder 加 context_refs（delegate 与 registerCustomAgent 共用，单点改动）；② delegate handler：取 refs → resolve → 组装 Context Pack 前置拼 task → Run(ctx, ...)；③ Run 返回：写 note → 组装带 noteRef 的结果头 | 集成测试：假 agent A 产出 → 结果头含 note ref → 再 delegate B 带该 ref → 捕获 B 收到的 input 含 Context Pack 且 task 原文完整 |
| internal/agents/result.go | FormatForDirector 加可选 noteRef 参数（空=输出不变）；更新其调用点 | 黄金输出测试：noteRef 为空时与旧输出逐字节一致 |
| Director 系统提示文件（S0 定位） | 增加 ~5 行 context_refs 使用指引 | TUI 会话走 repo→coding 接力，检查 Director 确实发出 context_refs（看日志） |
| 全链路日志 | debug 级：board、refs 解析结果、note 写入路径、预算裁剪量 | 可观测性验收 |

**S4 — read_note 工具（小步）**：新建 internal/agents/read_note_tool.go，镜像 read_artifact_tool.go 的构造与注册方式，按 `note:<ref>` 读全文（按 boardID 限域）。验证：单测 + 裁剪场景 E2E（注入降级后 agent 能自助读全文）；确认自定义 Agent 工具列表含 read_note。

**S5 — 加固（可选同 PR）**：预算常量集中；写失败降级路径测试；notes/ 磁盘用量日志；README 一节说明黑板语义与清理方式。

Phase 3（不在本次必须范围）：`notes prune` 命令、RepoContextStore 收编、note_created UI 事件、注入预算配置化。

## 五、验证策略（测试矩阵）

| 层 | 用例 | 期待 |
|---|---|---|
| 单元 | front-matter 编解码往返、损坏文件容错 | 无损；损坏文件跳过不 panic |
| 单元 | 并行 20 goroutine 同 agent 写 note；读并发 | 全部落盘、文件名无冲突、reader 永不见半文件（原子 rename） |
| 单元 | boardID 清洗（非法字符/超长/空）与 adhoc 兜底 | 目录名安全；空 board 不崩溃 |
| 单元 | ref 解析：三种语法 + 非法 + 重复 + 不存在的 note/artifact | 非法项进 unresolved，不影响其余 |
| 单元 | 预算裁剪：0/1/多条/超大 note | 总量 ≤ 预算；降级行出现 |
| 集成 | delegate A → 头含 note ref → delegate B 带 ref | B 的 input 含 Context Pack；task 原文逐字保留在后 |
| 集成 | 自定义 agent（registerCustomAgent）注册后的 schema | 含 context_refs；delegate_{name} 行为与 delegate 一致 |
| 集成 | result.go 空 noteRef 黄金输出 | 与旧格式逐字节一致 |
| E2E (TUI) | repo_agent→coding_agent 真实接力 | notes/{session_id}/ 有两条交付物；coding 读到上游文件路径等关键信息 |
| E2E (HTTP) | 双并行 task | 两个 uuid 目录互不干扰；并行 delegate 无冲突 |
| 边界 | 只读 FS / 磁盘满 | agent 主流程不失败，仅日志报错 |
| 边界 | TUI resume 旧 session | 旧 notes 可继续被引用 |

## 六、风险与回滚

- 一级回滚（免重编译）：环境变量 kill-switch（如 `YAGENT_BLACKBOARD=0`）→ delegate 完全回到现状（不解析 refs、不写 note、头部不变）。
- 二级回滚：回滚二进制即可——所有改动纯增量：无 artifact 迁移、无协议变更、无 schema 破坏（delegate 旧调用方不传 context_refs 时行为与今天完全一致）。
- 残留物：orphan 的 notes/ 目录无害且可人工删除；`.tmp` 残留由 rename 语义保证不产生"半成品正式文件"。
- 触发回滚的条件：token 消耗显著上升（注入预算失控）、注入内容引发下游 agent 行为异常、写盘影响主流程延迟明显。

## 七、显式假设（S0 需验证）

1. delegate handler 是子 Agent 执行的唯一咽喉（含 Meta-Agent 路径）；若还存在其它子 agent 调用点，需补同样的注入口。
2. `Agent.Run` 的 ctx 在全部调用链中可用且被透传。
3. read_artifact 注册对全部 agent（含自定义）可见——决定 read_note 的注册位置与可见性对齐。
4. `~/.yagent` base 目录存在统一解析 helper，可复用于 notes 根路径。
5. TUI sessionID 在首次 delegate 前可用；HTTP task_id 在 spawn 前可用。
6. go.mod 无 yaml 依赖 → 采用 JSON front-matter（若已有 yaml 依赖且团队偏好，可替换编解码层，不影响架构）。
7. FormatForDirector 调用点在 1-3 处；若远超预期，改用 delegate handler 内头部拼接的退路方案。
