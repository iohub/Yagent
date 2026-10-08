# yagent「thinklink + Thought & Plan → TodoWrite」重构设计文档

> **文档信息**

| 属性 | 值 |
|------|------|
| **标题** | yagent「thinklink + Thought & Plan → TodoWrite」重构设计文档 |
| **版本** | v0.1（草案） |
| **日期** | 2026-10-08 |
| **状态** | 待评审 |
| **适用范围** | Yagent Agent 系统 — thinklink 记录链路、Thought & Plan 解析、上下文压缩（紧急/终极）、TodoWrite 结构化任务清单工具 |
| **作者** | yagent 技术团队 |
| **相关文档** | [ARCHITECTURE.md](./ARCHITECTURE.md)、[knowledge-management-design.md](./knowledge-management-design.md)、[rollout.md](./rollout.md) |

---

## 目录

- [1. 背景与目标](#1-背景与目标)
- [2. 现状机制（已确认的代码事实，含路径与行号）](#2-现状机制已确认的代码事实含路径与行号)
- [3. 问题根因分析](#3-问题根因分析)
- [4. 三套候选方案](#4-三套候选方案)
- [5. 推荐方案 C 详细设计（C+：追加流快照 + 完成台账 + 一版静默兜底）](#5-推荐方案-c-详细设计c追加流快照--完成台账--一版静默兜底)
- [6. 验证策略](#6-验证策略)

---

## 1. 背景与目标

**目标**：将 Director/Coding Agent 的 "thinklink + Thought & Plan" 机制重构为 "TodoWrite 结构化工具"（类似 Claude Code 的 TodoWrite），消除 "prompt 约定输出格式 + Go 正则从自由文本提取" 的脆弱耦合。

核心思路：任务计划/进度从「LLM 自由文本 + 正则回读」的隐式信道，迁移到「tool call + JSON Schema」的原生结构化信道，使任务状态具备**格式契约、参数校验、显式成功/失败、消息历史原生记录**四项保障。

---

## 2. 现状机制（已确认的代码事实，含路径与行号）

> 本节所有路径、行号与行为均为对当前代码库走读确认的事实，作为本次重构的基线。

### 2.1 数据层 internal/thinklink/thinklink.go

- `Store{mu sync.RWMutex, entries []Entry, idSeq, maxEntries}`，`DefaultMaxEntries=200`；`NewStore(maxEntries)` 传 0 时用默认 200。
- `Entry{ID, Kind(KindUserInput|KindThoughtPlan), Content, Timestamp, Step}`。
- API：`AddUserInput(content, step)`（自带去重：最后一条同为用户输入且内容相同则跳过）、`AddThoughtPlan(content, step)`、`Count(kind)`、`RebuildPrompt(keepPlans)`。
- 淘汰语义：超 200 条最旧优先淘汰，但第一条 `KindUserInput` 永不淘汰。

### 2.2 记录链路（被动、隐式）

- 用户输入：`internal/agents/coding.go:336` `a.thinkLink.AddUserInput(input, 0)`；`internal/agents/director/planner_run.go:56` `p.cfg.Journal.AddUserInput(in.Input, 0)`（并发布 "thinklink_entry" 事件供 TUI）。
- Thought & Plan 块：LLM 每轮回复后，`internal/agents/executor.go:509-510` 与 `planner_run.go:525-526` 调用 `compression.ExtractThoughtAndPlanBlocks(choice.Content)` 用正则从自由文本提取，逐块 `AddThoughtPlan(block, i)`。

### 2.3 正则解析 internal/compression/emergency.go:23-76

- `thoughtAndPlanPattern = (?i)thought\s*(?:&|&amp;|＆|and)\s*plan`（兼容全角＆、HTML 实体、"and"、忽略大小写与空白）。
- `ExtractThoughtAndPlanBlocks`：循环 `findNextBlockStart` 匹配正则、回退行首 `"## "` 前缀、以下一块开始或 EOF 为块边界。
- **脆弱点**：prompt 让 LLM 输出固定格式 `## Thought & Plan` 块，Go 侧从自由文本"猜"边界；格式漂移（漏输出/变体标题/中英混杂/截断）即静默漏提或误提，无错误信号。

### 2.4 消费链路（终极压缩，第三级）internal/compression/compressor.go

- `ApplyUltimate(messages, threshold, mem, keepPlansLimit)`：两级常规压缩（tool 结果截断→紧急压缩）后仍超限触发；`userBudget = threshold − system tokens`；`totalPlans = Count(KindThoughtPlan)`；`keep` 从 `min(cfgKeep, totalPlans)` 逐轮递减调 `RebuildPrompt(keep)`；仍超限则 `TruncateToTokenBudget` 硬截断；重建为单条 user 消息（`mem.Clear()` + `AddHumanMessage(prompt)`），并执行 `clearPendingSubAgent` 回调。
- `RebuildPrompt` 输出格式：重置说明横幅 + `=== Original user input(s) ===`（全部用户输入，带时间戳/step）+ `=== Thought & Plan blocks ===`（`[TP-n]` 序号的历史 T&P 块原文）。

### 2.5 紧急压缩（第二级）internal/compression/emergency.go

- `EmergencyCompressMessages`：遍历 assistant 消息 `ExtractThoughtAndPlanBlocks`，保留最后 `DefaultEmergencyCompressKeepLastN=3` 个原始 T&P 块，或走 LLM 总结（输入 20000 / 输出 2000 tokens 上限）。

### 2.6 TUI

- `internal/tui` 监听 "thinklink_entry" 事件，`ctrl+g` 进入 thinklink 全屏视图（用户输入与 T&P 块列表）。

### 2.7 工具注册机制

- `internal/agents/tools.json`（29 个工具，name/description/parameters schema）→ `coding.go` `lookupToolFunc` switch-case 映射执行函数 → `tools.NewAdapter(name, desc, fn).WithSchema(schema)` → `Registry.MustRegister` → Adapters 注入 Executor。Director 侧在 `internal/agents/director.go` 中 `tools.NewAdapter` 直接构造 delegate_* 工具。新增工具需改 4 处：tools.json、执行函数、lookupToolFunc/Adapter 注册、prompt 文档。

### 2.8 相关 prompt

- `internal/agents/director.prompt.md`："Output Format" 要求每轮回复前输出 `## Thought & Plan` 块；"Ultimate Context Compression (thinklink)" 章节要求每次回复包含格式良好的 T&P 块（否则上下文重置后有永久信息缺口）。
- `internal/agents/coding.prompt.md`：同样约定 + "No Tool Calls = Immediate Termination" 章节（纯文本回复=终止；T&P 块不算行动，触发 `executor.go:532` 的提醒注入 "You must immediately invoke a tool...; a `## Thought & Plan` block alone does not constitute action"）。

### 2.9 相关配置

- `internal/config/config.go:725` 区域：`EnableUltimateCompression`（默认 true）、`UltimateCompressionKeepPlans`；`executor.go:73` `UltimateKeepPlans`（0=全部保留）；`NoActionRetryLimit=2`（`executor.go:79`）。

### 2.10 数据流图

```
用户输入 → coding.go:336 / planner_run.go:56 (AddUserInput) → thinklink.Store
LLM 回复 → executor.go:509 / planner_run.go:525 (正则 ExtractThoughtAndPlanBlocks → AddThoughtPlan) → thinklink.Store
thinklink.Store ──RebuildPrompt──► compressor.ApplyUltimate（第三级终极压缩，重建单条 user 消息）
thinklink.Store ──"thinklink_entry"事件──► TUI ctrl+g 全屏视图
```

---

## 3. 问题根因分析

**因果链**：

```
prompt 要求每轮输出固定格式 T&P 块
    ↓
LLM 格式漂移（漏输出/变体标题/中英混杂/截断）
    ↓
正则从自由文本猜边界
    ↓
静默漏提/误提（无错误信号）
    ↓
thinklink 信息缺口
    ↓
终极压缩基于缺口重建
    ↓
模型获得永久错误认知
    ↓
重复劳动或任务失败
```

- **根本原因**：控制信道与数据信道混用——把"计划/状态"这一结构化信息塞进 LLM 自由文本输出信道，再用启发式正则读回。正确做法是原生结构化信道（tool call + JSON Schema）：格式契约、参数校验、显式成功/失败、消息历史原生记录。
- **次要问题**：
  - prompt 膨胀（每轮重复格式指令）与 token 开销；
  - TUI 依赖"正则提取成功"才产生事件；
  - 紧急压缩对同一脆弱链路二次依赖；
  - keepPlans 递减循环本质是"猜历史块预算"而非按信息价值裁剪。

---

## 4. 三套候选方案

### 4.0 共同基线：TodoWrite 工具契约（对齐 Claude Code）

工具名 `TodoWrite`，语义：**全量替换**当前任务清单快照（空数组=清空；未列出的条目视为删除）。参数契约：

- `todos`（array，必填）：
  - `content`（string，必填）：祈使式任务描述
  - `activeForm`（string，必填）：进行时描述，in_progress 时展示
  - `status`（string，必填，枚举 pending/in_progress/completed）

执行函数确定性校验：非空 content/activeForm；status 合法；in_progress 数量 ≤1（>1 拒绝并提示）；todos 长度上限（如 25）；支持空数组。工具结果文本返回紧凑摘要（如 "Todo list updated: 1 in_progress, 2 pending, 1 completed"）。

### 4.1 方案 A：渐进兼容型（TodoWrite 为主 + 正则 fallback）

- **数据模型**：thinklink.Store 双轨——新增 TodoState（当前快照）+ CompletedLedger（完成台账）；旧 Entry/KindThoughtPlan 流保留；KindUserInput 不变。
- **写入链路**：TodoWrite 执行函数 → 写 TodoState/Ledger → 发 "todo_update" 事件；同时旧正则提取继续运行（双写）。
- **终极压缩重建**：重置横幅 + 用户输入 + 当前快照 + 完成台账 + 旧 T&P 块（沿用 keepPlans 递减循环），两来源并存。
- **紧急压缩**：保留"最后 3 个 T&P 块"逻辑，另加"钉住最新 TodoWrite 调用对"。
- **Prompt**：同时保留 T&P 格式约定与新增 TodoWrite 章节（篇幅最大）。
- **稳健性提升**：有限——新增结构化信源，但旧脆弱路径仍运行，双真相源存在不一致风险。
- **风险/迁移**：风险最低（随时回退旧行为）；迁移成本中；长期维护成本最高（双轨同步、双份测试）。
- **测试改造**：旧测试全部保留 + 新增 todo 测试，总量最大。
- **工作量**：4–6 人日。

### 4.2 方案 B：彻底替换·快照重构型

- **数据模型**：thinklink 包重构为三块——InputLog（保留 AddUserInput 去重/首条永不淘汰）、TodoState（当前快照，全量替换）、CompletedLedger（有界完成台账约 200 条）；旧 KindThoughtPlan 概念删除。
- **写入链路**：TodoWrite 执行函数 → 校验 → 覆盖 TodoState → 增量写入 Ledger（对比新旧快照的 completed 迁移）→ 发 "todo_update"。
- **终极压缩重建**：重置横幅 + 用户输入 + `=== Current task list ===`（in_progress→pending→completed 排序）+ `=== Completed tasks ===`（台账，按预算裁剪）；不保留历史快照；keepPlans 循环改为"台账裁剪→硬截断"降级链。
- **紧急压缩**：删除 ExtractThoughtAndPlanBlocks 调用循环；改为识别并钉住"最新 TodoWrite 调用 + 结果"对；LLM 总结路径不变。
- **Prompt**：删除 T&P "Output Format" 章节，改写为 TodoWrite 使用规范；改写 "Ultimate Context Compression" 章节；改写 "No Tool Calls = Immediate Termination" 表述。
- **稳健性提升**：最大——脆弱链路彻底消失，单一真相源。
- **风险/迁移**：大爆炸式改造 + 测试重写量大；迁移成本高。
- **测试改造**：compression 两个测试文件、agents 两个测试文件基本重写；thinklink 测试重写。
- **工作量**：8–12 人日。

### 4.3 方案 C：折中·追加流快照型（推荐）

- **数据模型**：保留 Entry 追加流骨架，新增 KindTodoSnapshot 条目（Content 为快照 JSON）；"当前快照"=最新 Snapshot 条目（派生视图）；另维护 CompletedLedger（显式增量，非差分推断）；KindUserInput 及去重/首条永不淘汰语义完全不变。淘汰策略重设：快照条目优先淘汰、首条输入与最新快照永久保留。
- **写入链路**：TodoWrite 执行函数 → 校验 → 快照与上一条相同时仅更新 step/时间/revision 不追加（防刷流）→ 否则追加 KindTodoSnapshot → 更新 Ledger → 发 "todo_update" 事件（携带完整快照，TUI 无需 diff）。
- **终极压缩重建**：重置横幅 + 用户输入（全部，含时间戳/step）+ `=== Current task list ===`（当前快照渲染）+ `=== Completed tasks ===`（台账）+（可选配置 KeepRecentTodoSnapshots，默认 0）最近 N 条历史快照的进度轨迹；超预算降级顺序：历史快照 → 台账 → 硬截断。
- **紧急压缩**：与 B 相同的"钉住最新 TodoWrite 对"策略；正则调用循环直接移除。
- **Prompt**：同 B。
- **稳健性提升**：与 B 等同（正则主路径消失），且保留"过程记录"惯用法（TUI 时间线、调试价值）。
- **风险/迁移**：风险低-中（diff 最小、characterization 测试以修改为主）；迁移成本中。
- **测试改造**：compression 测试按新重建格式改断言（资产可复用）；agents 测试改 Journal 依赖点；thinklink 测试扩展保留策略。
- **工作量**：6–9 人日。

### 4.4 三方案对比总表

| 维度 | A 渐进兼容 | B 彻底重构 | C 追加流快照 |
|---|---|---|---|
| 正则依赖（主路径） | 保留 | 删除 | 删除 |
| 真相源数量 | 2（不一致风险） | 1 | 1（快照为唯一语义，流为物理载体） |
| 历史保留 | 全（T&P） | 仅完成台账 | 快照流+台账（TUI/调试） |
| 重置恢复力 | 中（双源可能矛盾） | 高 | 高 |
| TUI 改造量 | 中 | 中 | 小（时间线可复用） |
| 迁移成本/风险 | 低/低 | 高/中 | 中/低-中 |
| 测试改造量 | 大（叠加） | 最大（重写） | 中（修改为主） |
| 工作量 | 4–6 人日 | 8–12 人日 | 6–9 人日 |

---

## 5. 推荐方案 C 详细设计（C+：追加流快照 + 完成台账 + 一版静默兜底）

### 5.1 推荐理由

1. **A 不满足核心目标**（消除脆弱耦合），双轨并存引入双真相源，长期成本最高，否决。
2. **B 与 C 端到端语义等价**，C 复用 Entry 追加流骨架：diff 更小、characterization 测试以修改为主、保留 TUI 时间线与调试价值；未来若倾向纯快照模型可零外部影响收敛。
3. **C 天然满足 "AddUserInput 语义 100% 保留"**。

### 5.2 数据模型（thinklink 扩展）

- **TodoStatus**：pending/in_progress/completed。
- **快照条目**：`Entry{Kind: KindTodoSnapshot, Content: 序列化快照, Step, Timestamp}`；快照内含 items（content/activeForm/status）、revision、updatedAt、step。
- **新增读取 API**：`SetTodos(items, step)`（校验+去重+追加+台账+返回是否变化）、`CurrentTodos()`（深拷贝返回当前快照）、`TodoHistory(n)`、`CompletedLedger()`、`Count(kind)` 复用。
- **CompletedLedger**：条目={content, activeForm, completedAtStep, completedAtTime}；容量约 200 FIFO；由 SetTodos 对比前值 status 迁移显式追加（删除条目不算"完成"，重新出现按 content 文本匹配去重）。
- **淘汰策略**：统一 200 上限下，淘汰优先级 = 旧快照 → 其余 → 输入（首条输入永久保留）；最新快照显式钉住。

### 5.3 工具契约

同 4.0 基线（TodoWrite、三字段、全量替换、≤1 个 in_progress、空数组清空、长度上限）；字段名、枚举值、全量替换语义与 Claude Code 逐一对齐；yagent 增加确定性校验属增强而非偏离。

### 5.4 写入链路时序

```
LLM 生成 tool_call(TodoWrite, args)
  → Registry 校验 schema
  → 执行函数：手工校验（枚举/in_progress 唯一性/长度/空串）
  → store.SetTodos(items, step)
       ├─ 与上条快照相同？→ 仅更新 revision/step（不追加）
       ├─ 追加 KindTodoSnapshot 条目（含钉住最新）
       └─ 对比迁移 → 追加 CompletedLedger
  → 事件发布 "todo_update"（全量快照 + revision）
  → 返回紧凑摘要文本（tool result）
```

### 5.5 终极压缩重建改造（compressor.go ApplyUltimate）

- **数据源**：`totalPlans=Count(KindThoughtPlan)` 替换为 `CurrentTodos()`；keepPlans 递减循环删除。
- **渲染顺序**：重置横幅（含新指令："任务清单为权威状态；缺失细节需用工具重新核实"）→ 全部用户输入（时间戳/step，格式不变）→ `=== Current task list ===`（in_progress 置顶，pending 次之，completed 最后带完成标记；渲染 activeForm）→ `=== Completed tasks ===`（台账）。
- **预算降级链**：可选历史快照（配置默认 0 条）→ 完成台账（先截断到 N 条）→ `TruncateToTokenBudget` 硬截断；`mem.Clear()+AddHumanMessage`、`clearPendingSubAgent` 回调保留。
- **无 TodoWrite 调用历史时**：渲染"（无任务清单）"占位。

### 5.6 紧急压缩改造（emergency.go）

- 删除对 `ExtractThoughtAndPlanBlocks` 的调用循环；新增"TodoWrite 钉住"：扫描消息保留最新一个 TodoWrite 的 assistant tool_call + tool result 对（原样），被取代的旧对整体替换为紧凑注记；LLM 总结路径（2000/20000 tokens）不动。

### 5.7 Prompt 改动点

- **`director.prompt.md`**：删除 "Output Format" 的 T&P 强制块；新增 "Task Tracking (TodoWrite)" 章节（何时调用：多步任务开始/单步完成/发现新工作；全量替换语义；计划写入工具而非文本）；重写 "Ultimate Context Compression (thinklink)" 章节（重置后以任务清单为权威、缺失细节需重新核实）。
- **`coding.prompt.md`**：同上；重写 "No Tool Calls = Immediate Termination"（见 5.10-c）；删除 T&P 约定段落。

### 5.8 文件改动清单（相对现状）

**新增**：

| 文件 | 内容 |
|------|------|
| `internal/agents/todo_tool.go` | TodoWrite 执行函数 + 适配器构造（闭包持有 store 与事件发布器），coding 与 director 复用 |
| `internal/thinklink/todo_state.go` | TodoStatus/TodoItem/快照序列化/CurrentTodos/Ledger/去重与淘汰策略（命名可并入 thinklink.go） |

**修改**：

| 文件 | 改动 |
|------|------|
| `internal/thinklink/thinklink.go` | 新增 KindTodoSnapshot；淘汰优先级重设 |
| `internal/agents/tools.json` | 注册 TodoWrite schema |
| `internal/agents/coding.go` | lookupToolFunc 增加 TodoWrite 分支 |
| `internal/agents/director/director.go` | director 侧注册 TodoWrite 适配器 |
| `internal/agents/executor.go` | 删除 509-510 正则提取；改写 532 提醒文案；NoActionRetry 判定纳入"仅 TodoWrite 轮 = 无进展"；UltimateKeepPlans 语义调整（73 行） |
| `internal/agents/director/planner_run.go` | 删除 525-526 正则提取；AddUserInput 与 thinklink_entry 事件保留；接入 TodoWrite 适配器 |
| `internal/compression/compressor.go` | ApplyUltimate 按 5.5 改造 |
| `internal/compression/emergency.go` | 按 5.6 改造；ExtractThoughtAndPlanBlocks 私有化仅过渡期静默兜底 |
| `internal/config/config.go:725` 区域 | UltimateCompressionKeepPlans 更名/语义调整（建议 KeepTodoLedgerLimit，一版过渡映射旧值）；新增 EnableLegacyThoughtPlanFallback（默认 true 一版）与可选 KeepRecentTodoSnapshots（默认 0） |
| `internal/tui`（grep "thinklink_entry" 定位） | 监听 "todo_update"；ctrl+g 视图增加"当前任务清单 + 完成台账"分区 |
| `internal/agents/director.prompt.md`、`internal/agents/coding.prompt.md` | 按 5.7 改动 |

**删除（下一 minor 版本）**：

- `emergency.go` 中 `thoughtAndPlanPattern` 与 `ExtractThoughtAndPlanBlocks` 及附属辅助函数；legacy 渲染分支。

### 5.9 分步实施计划（每步含验证）

| 步骤 | 内容 | 验证 |
|---|---|---|
| 1 | thinklink 扩展（todo_state.go + thinklink.go） | 单测覆盖快照追加/相同快照不追加/首条输入与最新快照永不被淘汰/台账迁移（完成、删除、重现） |
| 2 | TodoWrite 工具（todo_tool.go） | 表驱动单测覆盖非法 status、>1 in_progress、空串、超长、空数组 |
| 3 | 注册接入（tools.json、coding.go、director.go、planner） | 集成冒烟——一次工具调用断言 store、事件、tool result 三者一致 |
| 4 | executor/planner 清理与语义改写（executor.go、planner_run.go、两个 prompt.md） | characterization 场景——纯文本终止、TodoWrite-only 触发提醒且计数、TodoWrite+真实工具恢复正常 |
| 5 | 终极压缩改造（compressor.go、config.go） | golden 测试——重建文本含输入+快照+台账；预算不足降级顺序正确；无快照时占位渲染 |
| 6 | 紧急压缩改造（emergency.go） | 单测——最新 TodoWrite 对被钉住；旧对被替换/丢弃；LLM 总结路径回归 |
| 7 | TUI 适配 | 手工——ctrl+g 视图实时刷新快照与台账；输入列表行为不变 |
| 8 | 过渡策略与回归（legacy 兜底 flag、全量测试、真实低阈值任务演练） | 真实 LLM 下强制触发终极压缩，确认续跑不重复已完成工作 |

### 5.10 关键设计点决策

- **(a) 对齐度**：字段 content/status/activeForm、三态枚举、全量替换语义与 Claude Code 完全一致。
- **(b) 信息量分析**：现状 200 条 T&P 块约 3–8 万 tokens，重建循环正是因超预算才递减保留；正常未重置会话中这些块本就在 assistant 历史里，独特价值仅体现在"重置后"。C+ 的"全部用户输入 + 当前快照 + 完成台账"约 1–4k tokens：目标/约束（输入）与当前状态/已完成事实（快照+台账）覆盖重置恢复的绝大部分价值；残余损失是"过程性理由"。缓解：重置横幅指令模型对缺失细节重新核实；prompt 要求以自描述祈使句书写 content；可选 KeepRecentTodoSnapshots 呈现进度轨迹。**结论**：信息形态从"轨迹"转为"状态+事实"，足以恢复任务上下文。
- **(c) 纯文本终止表述**：prompt 改为"纯文本（无工具调用）回复=立即终止；计划必须写进 TodoWrite，不得在文本中叙述"；`executor.go:532` 提醒文案改为 "a `TodoWrite` call alone does not constitute task progress"；行为约定：仅 TodoWrite 的轮次不计入进展（不重置 NoActionRetry），与旧 T&P-only 行为对齐，防止背靠背 TodoWrite 空转；TodoWrite 属工具调用，不触发"纯文本终止"分支。
- **(d) TUI 适配**：thinklink_entry 输入事件与输入列表不动；新增 "todo_update" 事件（全量快照 payload），ctrl+g 视图增加 "Current Tasks（含 activeForm）+ Completed" 分区，无需 diff 逻辑；标题可由 "thinklink" 改为 "Tasks"（纯展示层）。
- **(e) 用户输入保留**：AddUserInput 及去重、首条永不淘汰语义 100% 保留；仅调整统一容量下的淘汰优先级，确保快照不挤掉输入。
- **(f) 兼容期**：推荐"一版静默兜底"——正则提取函数保留（私有化），仅作纯防御性捕获（不提示、不依赖）；迁移期若模型自发输出旧格式仍追加为 legacy 条目并在重建中作可选段落；默认开启一个 minor 版本后彻底删除。备选：直接删除 + git revert 发布回滚。两者由团队发布节奏决定，推荐前者。

### 5.11 工作量与迁移成本

- 核心开发（步 1–6）约 4–5 人日；TUI 与过渡策略约 1–2 人日；测试改造与真实任务演练约 1–2 人日。**合计 6–9 人日**。
- 无数据迁移（纯内存）。主要成本在测试断言重写与一轮真实压缩演练。

---

## 6. 验证策略

### 6.1 测试矩阵

| 层级 | 覆盖点 |
|------|--------|
| thinklink | 保留策略不变量（首条输入/最新快照钉住）、去重抑制、台账迁移（完成/删除/重现/重复 content） |
| 工具 | schema/手工校验边界；结果摘要格式 |
| executor | 三种回复形态行为矩阵（真实工具 / 仅 TodoWrite / 纯文本）；提醒文案与 NoActionRetry 计数 |
| compression | 终极重建 golden（含/不含快照、台账降级顺序、硬截断）；紧急压缩钉住与旧对替换；LLM 总结路径回归 |
| 集成 | -race 并发（工具写 vs 压缩读）；模拟阈值触发压缩后断言重建文本包含任务状态 |

### 6.2 边界用例

- 空数组清空后重建；
- 全部 completed；
- 压缩恰发生在 TodoWrite 调用与结果之间（对钉住策略）；
- legacy flag 开/关；
- 模型连续两次相同快照（不追加）；
- 200 条上限下输入与快照混合淘汰压力测试。

### 6.3 回滚计划

- **过渡期**：EnableLegacyThoughtPlanFallback 翻转 + 旧版本发布（git revert）。
- **终态（删除后）**：回滚 = 重发上一 release（内存态无数据迁移负担）。
- **阻断标准**：真实任务演练中若重置后出现"重复已完成工作"或"任务丢失"，立即回滚并保留现场消息快照用于诊断。

