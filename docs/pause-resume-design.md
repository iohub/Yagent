# 整体停止与继续：暂停闸门 + 委派检查点重放 设计方案

> **本文是实施规范**，读者是执行改动的 Coding-Agent。
> 所有"今天如何"的断言都带 `file:line`，核对时间 2026-10-14（分支 `main`，commit `fe7af06`）。
> 前置阅读：`AGENTS.md` §6.2（执行循环）、§6.4（Blackboard/Artifact）、§6.7（任务生命周期与 rollout）、
> §11 坑 11/12/16；`docs/blackboard-handoff-design.md` §3（Handoff v2）。
> 本文取代 `AGENTS.md:385` 关于 rollout 类型含 `world_state` 的描述 —— 该类型已于 2026-10-14 删除，
> 现由 §5.3 的 `agent_checkpoint` 承接其职责。

---

## 0. 决策记录（已定，不再讨论）

| # | 决策点 | 选定方案 |
|---|--------|----------|
| D1 | 分层形态 | **A（活暂停闸门）+ B（检查点重放）+ C（交接兜底）三层同构落地**：同一个"继续"动作按可用性依次降级。B 是承重层（唯一能跨进程/换机器的），A 是进程内快路径，C 是重放不可行时的兜底 |
| D2 | 停止粒度 | **step 边界**（一次 LLM 调用 + 其工具批次完成之后）。不做工具中途暂停 —— `run_bash` 与流式 LLM 调用只认 ctx，中途停必然留下不可判定的副作用 |
| D3 | 检查点载体 | **rollout 内新增 `agent_checkpoint` envelope**，不新建存储。理由见 §3.1：子 Agent 的 rollout 已经同步逐条写了全部消息，检查点只需存"身份 + 停止原因 + 工作区状态"，**不需要复制消息** |
| D4 | 关联身份 | `delegation_id` = **Director 侧 `delegate_*` 的 `tool_call_id`**，经 ctx 下传，写进子 Agent 的 `session_meta`；同时记 `parent_session_id` 使父子 rollout 可双向定位 |
| D5 | 重放入口 | **executor 从 ctx 读 `ResumeState`**（照 `memory.WithRolloutWriter` 的既有 ctx-value 模式，`rollout_writer.go:21-34`），六个子 Agent 的 `Run(ctx, input)` 签名**一个都不改** |
| D6 | 收尾写法 | 恢复完成后**向原 `call_id` 追加第二条 `function_call_output`**，并把 reader 的配对语义从 first-wins 改成 **last-wins**，与 LLM 侧对齐（§4.3）。这是一条需要签字的行为变更 |
| D7 | 恢复决策权 | **代码决定恢复，LLM 不参与**。Director 的模型只会看到"那次委派成功返回了"，不需要理解"暂停/恢复"这个概念 |
| D8 | Browser 委派 | **不做精确重放，一律走 C**。浏览器状态（页面、滚动位置、JS 上下文、CDP 会话）本质不可序列化，且 `[browser] idle_ttl` 默认 15m 会回收会话 |
| D9 | 落地节奏 | 按 §12 的 P0→P1→P2→P3 四阶段，**P0 是独立可合的前置修复**（四条真 bug/缺口，不依赖本方案任何部分） |

D1、D6 是风险最高的两项，因此 §10 的一致性闸门是**硬约束**，不是可选项。

---

## 1. 需求与语义定义

用户的原始诉求：**"可以整体停止、继续"**，且明确指出今天的缺陷是"停止了不是从 sub agent 开始，而是从 director 继续"，
并要求**"要能关终端 / 崩了 / 换机器再回来"**。

据此固定三个术语，全文不再混用：

| 术语 | 语义 | 可继续性 |
|------|------|----------|
| **暂停 pause** | 用户主动，在 step 边界停住，执行状态完整保留 | 必须可继续 |
| **终止 cancel** | 今天的 Esc / `cancel_task` 行为，ctx 取消，栈解开 | **本方案之后也可继续**（靠 B 从 rollout 重放） |
| **失败 fail** | LLM 错误耗尽重试、熔断器打开、max_steps | 不可继续（ Director 自己会重试或收尾） |

关键洞察：**A 与 B 的差别只是"进程是否还活着"**。

```
用户点"继续"
  ├─ 进程活着且 gate 在  → A：放开 gate，从阻塞点原样往下走（零成本、零损失）
  ├─ 进程死了/换了机器    → B：按 delegation_id 找子 Agent rollout，重建 messages 重放
  └─ B 判定不可重放       → C：合成一份真实报告交回 Director，由它重新委派（降级但诚实）
```

三条路共用同一份检查点记录、同一个 `delegation_id`、同一个 UI 入口。

---

## 2. 现状与根因（逐条带证据）

### R1（最致命）取消被降级成"一次失败的委派"，Director 完全不知情

`RunAgentLoop` **顶部没有 `ctx.Err()` 检查**（`executor.go:375` 直接进 `for i := 0; i < cfg.MaxSteps; i++`）。
取消的实际解开路径是绕一圈：

1. 取消落在工具里 → `ad.Call` 报错 → 结果变成 `"Error: context canceled"` 并被**当成正常工具结果 commit**
   （`executor.go:728-756`，还写进了子 Agent 的 rollout）
2. 下一步 `GenerateContent`（`executor.go:550-552`）在已取消的 ctx 上失败 → 进重试 backoff →
   select 命中 `<-ctx.Done()` → `return ExecutorResult{}, ctx.Err()`（`executor.go:392-393`）
3. `coding.go:405-408` `if err != nil { return AgentResult{}, err }` → `FinalizeResultFull`（`result.go:186`）不跑
   → 无 Summary / Deliverable / Handoff / artifact
4. `delegateTo` 仍写一张便签（`director_surface.go:143` → `director.go:506-513`，`status=error`、正文空）
5. `applyEnhancedCommander` 返回 `("", err)`（`director.go:481-483`）
6. runner 分类错误后返回（`director.go:761-773`）→ `execPlannerToolCall` 把它变成 `"Error: ..."` 工具结果
   （`planner_run.go:582-591`）→ **写进 Director 的 memory / messages / rollout**（`planner_run.go:613, 616-621, 623-628`）
   → **Director 继续下一个 step**

后果：用户按了停止，Director 却认为"委派失败了"，会自行决定重做、换 Agent 或收尾。
**这是"停不下来"与"只能从 Director 继续"的共同根因。**

### R2 子 Agent 的执行状态在返回时蒸发，且所有错误路径主动丢弃 History

`executor.go:221-223`：

```go
messages := []llm.Message{systemMsg, userMsg}
history := make([]llm.Message, 0)
history = append(history, systemMsg, userMsg)
```

这两行是硬编码的，`ExecutorConfig`（`executor.go:26-62`）**没有任何初始状态字段**。
三个错误出口全部 `return ExecutorResult{}, err`（`executor.go:392-393`、`:607-612`、`:832`），
只有成功路径才带 `History` 出来（`:710`、`:768`）。

`history` 的唯一消费者是 `ConvertLLMHistoryToMemory`（`executor.go:835`）→ `AgentResult.Memory` →
`injectSubAgentMemory`（`director.go:379-408`），而它注入的是一条 `IsSubAgent: true` 的摘要消息，
`planner_run.go:87-89` 在构造 LLM messages 时**明确跳过所有 `IsSubAgent` 消息**。
也就是说：子 Agent 的完整上下文对 Director 永远不可见（这是设计意图，避免上下文爆炸），
但同时也意味着**没有任何在途副本可供恢复**。

### R3 父子 rollout 之间没有关联 id

- `NewRolloutWriter` 每次自己 `generateSessionID()`（`rollout_writer.go:84`，时间戳+随机数），
  父子文件的 session_id 毫无关系；文件名只带 agent 名（`rollout_writer.go:100-107`）
- `injectSubAgentMemory(*psm, "", name)` 传的 toolCallID 是**空串**（`director.go:752-753`）→ `ParentID` 恒空
- `ResponseItem`（`rollout_types.go:73-85`）根本不序列化 `GroupID` / `ParentID` / `IsSubAgent`
- `RolloutSessionInfo`（`rollout_sessions.go:37-58`）只有 `TaskID` / `BoardID` / `AgentName`，
  head-scan 也只提取这三项（`rollout_sessions.go:264-265`）

后果：`/resume` 的清单能把子 Agent 文件列成 `sub:{agent}` 附属行，但**无法回答"这个文件对应 Director 的哪一次委派"**。

### R4 没有 step cursor，`world_state` 已删

`turn_aborted` 只带 `{reason, step_number, max_steps, stop_reason, has_delegated}`
（`rollout_types.go:238-254`），且**子 Agent 侧 `StopReason` 恒空**（`executor.go:342` 只填 `Steps`/`MaxSteps`，
对比 `planner_run.go:158-165` 的 Director 侧填了四项）。
`OnStepEnd` 是唯一的 per-step hook，但它只在**挂了 hook 且该 step 有 tool_calls** 时才触发（`executor.go:812`），
今天同时满足的只有 Coding 的 git checkpoint（`coding.go:398-400`）。

### R5 reader 与 LLM 侧的配对语义相反（潜在 bug，与本方案无关也该修）

同一个 `call_id` 出现两条 tool output 时：

- **LLM 侧 last-wins**：`llm.repairToolCallPairs` 用 map 收集（`llm/messages.go:126-141`），
  `matchedResponses[next.ToolCallID] = next` 后者覆盖前者；Director 侧的
  `validateAndRepairToolCallPairs`（`director.go:939-997`）同构
- **reader 侧 first-wins**：`normalizeToolPairing` 命中一次就置 `openSet[id] = -1`（`rollout_reader.go:787-789`），
  第二条同 id 的 output 走 `info.DroppedOrphanOutputs++` 被**丢弃**（`rollout_reader.go:792-793`）

后果：同一份 rollout，进程内喂给 LLM 与 `/resume` 后喂给 LLM 会得到**不同**的结果。
D6 的收尾写法依赖 last-wins，所以这条必须先统一。

### R6 git checkpoint 在取消路径上给出假状态

`OnAgentExit`（`git_checkpoint.go:225-288`）跑在**已取消的 ctx** 上：

```go
g.session.Completed = true          // :232 无条件
dirty, _ := g.isWorktreeDirty(ctx)  // :241 ctx 已取消 → 必然失败 → dirty=false
_, _ = g.runGitCommand(ctx, "add", "-A")   // :243 错误被丢弃
_ = g.cleanupCheckpointTags(ctx)    // :280 失败
_, _ = g.clearState()               // :284 不依赖 ctx → 成功删除状态文件
```

后果：停止之后，agent 分支原样留着、tag 没清、状态文件被删、`Completed=true`。
**"工作区改了什么"没有任何可靠记录**，而"继续"必须建立在这个记录上。

### R7 子 Agent 的 thinklink 不落盘

`executor.go:672-687`：T&P 块只 `Publish("thinklink_entry", ...)` 到事件总线，
对比 Director 侧 `planner_run.go:505-512` **同时**写进 rollout。
后果：子 Agent 恢复后 ultimate 压缩（`executor.go:499-538`）没有重建原料；
`/resume` 恢复的 thinklink 条目只有 Director 的。

### R8 `SessionMeta.Git` 恒空

`rollout_types.go:37` 定义了 `Git GitInfo`，但两个写入点
（`executor.go:296-303`、`planner_run.go:123-130`）都没填。
全仓唯一构造 `GitInfo` 的地方是测试（`rollout_test.go:556`）。
后果：无法判断"恢复时工作区是否还是暂停时那个状态"。

### R9 换机器会找不到 rollout 目录

`ComputeProjectID`（`project_context.go:77-100`）= `basename + "_" + sha256(绝对路径)[:8]`。
绝对路径一变（换机器、换目录、容器挂载点不同）→ projectID 变 →
`~/.yagent/data/rollout/{projectID}/` 对不上。

**但这条不是阻塞项**：`ListRolloutSessions(root, limit)` 本来就从 root 全量 walk 深度≤2 的所有 projectID
（`rollout_sessions.go:83-129`），清单并不按 projectID 过滤。所以只要 `~/.yagent` 同步过去，
换机器后仍能列出会话 —— 缺的是"这个检查点属于哪个仓库"的判据，由 §5.3 的 `workspace` 字段（git origin_url + sha）补上。

### R10 没有暂停这个状态

- `http.TaskStatus` 只有 `running/finished/failed/cancelled`（`http/types.go:70-74`）
- 全仓无 `signal.Notify`（grep `os.Signal` 零命中），`--prompt` 模式连 cancel func 都没有（`main.go:304` 用 `context.Background()`）
- 无 `/stop` `/pause` `/continue` 命令（`tui_update_commands.go:85` 那一带只有 `resume`/`rs`）
- TUI 取消后 `handleTaskCompleteMsg` 会**换新 ctx 并标 finished**（`tui_update_tasks.go:1023-1032`），旧 ctx 永久取消

### R11 暂停会被 idle/total 超时杀掉

`activity.WithIdleTimeout`（`activity.go:131-206`）的监视 goroutine：

- `idleFor = now.Sub(last.at)`（`activity.go:180`）→ `Heartbeat` 能续（`activity.go:215-225`）
- `totalElapsed = now.Sub(startTime)`（`activity.go:181`）→ **`Heartbeat` 续不了**，`startTime` 是创建时固定的（`activity.go:156`）

默认 `delegate_idle_timeout=60s` / `delegate_total_timeout=20m`（`director.go:741-744`）。
所以"暂停"若不特殊处理：60s 后被 idle 杀掉；就算持续心跳，20m 后仍被 total 杀掉。

---

## 3. 目标与非目标

### 目标

1. 一个"停止"动作，能在 step 边界干净地停住整条链（Director + 任意深度子 Agent），且不破坏执行状态
2. 一个"继续"动作，能从**停止时那个子 Agent 的那一步**接着跑，而不是回到 Director 重新委派
3. 上述两点在**进程重启后、换机器后**依然成立（前提：`~/.yagent` 与工作区 git 状态同步）
4. 停止原因可区分：暂停 / 终止 / 超时 / 失败，Director 与 UI 都能据此采取不同行为
5. 恢复不可行时**显式降级并告知**，绝不静默换语义（`AGENTS.md` §11 反复强调的头号病是"错了不报错"）

### 非目标

1. **不做工具中途暂停**。`run_bash`、流式 LLM 调用、CDP 操作无法在中途安全停住；暂停点最长需等一个
   `defaultToolTimeout`（180s，`executor.go:79`）
2. **不恢复工作区**。我们恢复的是 Agent 的执行状态；文件与 git 状态由用户自己同步。检查点只**记录并校验**它
3. **不做 Browser 委派的精确重放**（D8）
4. **不做多任务并行暂停的调度器**。gate 是 per-task 的；同时暂停多个任务只是多个独立 gate
5. **不改 `/resume` 的既有语义**。今天的"从 Director 继续"仍然是默认路径，新能力是它的一个额外选项
6. **不做 `--prompt` 非交互模式的暂停**（它连 cancel 都没有）。跨进程继续对它天然可用（检查点在盘上），
   但触发入口留到 §14 未决项

---

## 4. 架构总览

```
                    ┌─ 停止 ────────────────────────────────┐
用户 /pause ──→ Gate.Pause()          用户 Esc ──→ CancelFunc()
                    │                       │
        ┌───────────┴───────────┐           │
        │ step 边界阻塞（A）     │           │ ctx 取消，栈解开
        │ activity 计时冻结      │           │
        └───────────┬───────────┘           │
                    │                       │
                    └───────┬───────────────┘
                            ↓
              写 agent_checkpoint（stop_kind/step/workspace/digest）
              写 turn_aborted（既有，补齐 StopReason）
                            ↓
        ┌───────────────────┴────────────────────┐
   进程活着                                  进程死了
        │                                        │
   A：Gate.Resume()                     B：ReadRolloutForReplay
      从阻塞点原样继续                       → ResumeState 进 ctx
                                             → RunAgentLoop 从 StartStep 继续
                                        ┌────┴─────┐
                                     重建成功     重建失败/校验不过
                                        │          │
                                        │     C：合成 degraded 报告
                                        │        → 原 call_id 第二条 output
                                        │        → Director 重新委派
                                        └────┬─────┘
                                             ↓
                          向原 call_id 追加第二条 function_call_output
                          （reader last-wins，D6/§4.3）
```

### 4.1 A 层：暂停闸门（进程内）

新增 `internal/pause` 包，照 `memory.WithRolloutWriter`（`rollout_writer.go:21-34`）与
`activity` 的 sink（`activity.go:42-58`）的既有 ctx-value 模式：

```go
// internal/pause/gate.go（新增）
package pause

type Gate struct {
    mu       sync.Mutex
    paused   bool
    resumeCh chan struct{}   // 每次 Resume 关闭并重建
    reason   string
}

func NewGate() *Gate
func WithGate(ctx context.Context, g *Gate) context.Context
func From(ctx context.Context) *Gate        // 无 gate 时返回 nil，调用方 no-op

// Wait 在未暂停时立即返回 nil；暂停时阻塞到 Resume 或 ctx 取消。
// 阻塞期间以 1s 间隔向 activity 上报心跳（续 idle，见 §4.2 处理 total）。
func (g *Gate) Wait(ctx context.Context) error

func (g *Gate) Pause(reason string)          // 幂等
func (g *Gate) Resume()                       // 幂等
func (g *Gate) Paused() bool
```

**两个接入点，各一行**：

- `executor.go:375` 循环体开头（`stepNumber++` 之前）：
  ```go
  if err := pause.From(ctx).Wait(ctx); err != nil {
      return ExecutorResult{}, err     // ctx 取消 → 走既有 abort 路径
  }
  ```
  注意 `Wait` 必须对 nil receiver 安全（`func (g *Gate) Wait`，`g == nil` 直接 return nil），
  这样六个子 Agent 与所有测试调用点无需改动。
- `planner_run.go:195` 循环体开头（`p.state.Step = i + 1` 之前），同形态；
  返回时走 `writeAborted("paused", "paused")` 而不是 `cancelled`。

**为什么 gate 而不是"cancel + 重建 ctx"**：ctx 一旦取消不可复活。子 Agent 的 ctx 已经被
`toolbatch.Run`（`executor.go:781`）、`runToolCall` 的 `cancelCtx`（`executor.go:125`）、
以及正在执行的 `ad.Call` 捕获；重建意味着整棵树重接，而重接点分散在 4 层。
gate 是一个纯粹的新增阻塞点，不动任何既有 ctx 语义。

**嵌套委派自动继承**：gate 在 ctx 上，`delegateTo` 把 ctx 往下传（`director_surface.go:135-140`），
所以 Director 暂停时，正在跑的 Coding 以及它委派的 Browser 都会在各自的 step 边界停住。
不需要额外接线。

### 4.2 activity 计时冻结（R11）

`Heartbeat` 只能续 idle，续不了 total（R11）。改 `activity`：

```go
// activity.go sink 结构新增
type sink struct {
    parent      *sink
    last        atomic.Pointer[record]
    pausedAt    atomic.Int64      // unix nano，0 = 未暂停
    pausedTotal atomic.Int64      // 累计暂停纳秒
}

func Pause(ctx context.Context)    // 沿 parent 链置 pausedAt（若已暂停则 no-op）
func Resume(ctx context.Context)   // pausedTotal += now - pausedAt; pausedAt = 0
```

监视 goroutine（`activity.go:164-203`）改两处：

```go
idleFor := now.Sub(last.at)
totalElapsed := now.Sub(startTime) - time.Duration(s.pausedTotal.Load())
if pa := s.pausedAt.Load(); pa != 0 {
    totalElapsed -= now.Sub(time.Unix(0, pa))
    idleFor = 0                      // 暂停期间不计空闲
}
```

`pause.Gate.Wait` 在阻塞期间调 `activity.Pause(ctx)` / 返回前调 `activity.Resume(ctx)`，
两个包不互相 import（`pause` → `activity` 单向）。

**幂等与竞态**：`Pause`/`Resume` 沿 parent 链走（照 `Heartbeat` 的循环实现，`activity.go:222-224`），
嵌套委派时内外层同时冻结。必须写测试钉住"暂停 3× idle 阈值后不被取消"与
"暂停时长不计入 total"（§10 G4）。

### 4.3 收尾语义：为什么是 last-wins（D6 / R5）

恢复完成后，那次委派需要一个结果交回 Director。分两种情况：

**情况一：A 路径（gate）**。Director 的 planner 还阻塞在 `execPlannerToolCall`
（`planner_run.go:563-596`）里，那次工具调用**从未返回过**，所以从未提交过任何 tool result。
gate 放开 → 子 Agent 跑完 → 正常返回 → `commitPlannerToolCall`（`planner_run.go:598-635`）写一条正常结果。
**没有重复 call_id 问题，A 路径完全干净。**

**情况二：B 路径（跨进程）**。cancel 时 Director **已经**把 `"Error: context canceled"` 作为该 call_id
的 tool result 提交并落盘（R1 第 6 步）。恢复后需要把真实结果也交回去。三个选项：

| 选项 | 做法 | 评价 |
|------|------|------|
| (a) | 向原 call_id 追加第二条 output，reader 改 last-wins | **选定**。append-only JSONL 的天然语义就是"后写的覆盖先写的"；顺带修掉 R5 这个潜在 bug；Director 上下文看起来就是一次成功的委派 |
| (b) | 开一个"续跑轮"，用新的 user 消息告知结果 | 不改 reader，但 Director 上下文里永远留着一条 `"Error: context canceled"`，模型会把它当失败信号，可能重做 |
| (c) | 改写 rollout 中间那条记录 | 违反 append-only，破坏 `/resume` 与人工排查的既有假设，否决 |

(a) 的 reader 改动很小 —— 保持流式、保持输出顺序，只把"命中即置 -1"改成"记住输出位置，重复则原位覆盖"：

```go
// rollout_reader.go normalizeToolPairing 改动
emittedAt := make(map[string]int)          // call_id → out 中的下标
...
if m.Type == MessageTypeTool && m.ToolCallID != nil {
    id := *m.ToolCallID
    if idx, ok := emittedAt[id]; ok {      // 同一 call_id 的第二条：原位覆盖（last-wins）
        out[idx] = m
        info.SupersededOutputs++           // 新计数器，不再算 DroppedOrphanOutputs
        continue
    }
    if pos, ok := openSet[id]; ok && pos >= 0 {
        openSet[id] = -1
        emittedAt[id] = len(out)
        out = append(out, m)
        continue
    }
    info.DroppedOrphanOutputs++
    continue
}
```

`flushOpen`（`rollout_reader.go:741-769`）不变：只有真正没有 output 的 call 才合成
`"会话中断前未返回结果"`。

**必须配一条一致性闸门测试**（§10 G3）：同一份含重复 output 的消息序列，
`llm.NormalizeMessages`（`llm/messages.go:13`）与 reader 的 `normalizeToolPairing`
对同一 call_id 必须选出**同一条**。否则两侧语义会再次漂移，而漂移的表现是"恢复后 Director 行为诡异"，
极难排查。

---

## 5. 数据模型

### 5.1 `delegation_id` 贯通（D4 / R3）

新增 ctx-value（放 `internal/agents`，与 `ResumeState` 同文件）：

```go
type delegationCtxKey struct{}
func WithDelegationID(ctx context.Context, id string) context.Context
func DelegationIDFrom(ctx context.Context) string
```

写入点：`planner_run.go` 的 `execPlannerToolCall`（`:563`）在调用委派类工具前把 `tc.ID` 放进 ctx。
但 `execPlannerToolCall` 拿到的是 `callCtx`，而工具执行经 `p.cfg.Tools.Call(callCtx, ...)`
→ `directorToolRunner.Call`（`director.go:711`）→ `ad.Call(toolCtx, ...)`（`director.go:748`）→
`delegateTo(ctx, ...)`（`director_surface.go:110`）。所以最省事的注入点是
**`directorToolRunner.Call` 里判定 `isDelegate` 的那个 switch**（`director.go:734-747`）：

```go
case isDelegate:
    cancelCtx = WithDelegationID(cancelCtx, /* 需要 tc.ID */)
    toolCtx, toolCancel = activity.WithIdleTimeout(cancelCtx, ...)
```

问题：`directorToolRunner.Call(ctx, name, argsJSON)` 的签名里**没有 tool_call_id**
（它是 `director.PlannerTools` 接口，`planner_run.go:581` 调用）。两个选择：

- **(i) 扩接口**：`Call(ctx, name, argsJSON, callID string)`。改一处接口 + 一处调用 + 测试里的假实现。
- **(ii) 在 planner 侧注入**：`execPlannerToolCall` 里 `callCtx = agents.WithDelegationID(callCtx, tc.ID)`。
  但 `planner` 是 `director` 子包，**禁止 import `yagent/internal/agents`**（`scripts/check_no_agents_import.sh`，
  `AGENTS.md` §9.2）。所以 key 类型必须放在 `director` 子包或更下游的中立包。

**选定 (i) + key 放 `internal/memory`**：`memory.WithDelegationID` / `memory.DelegationIDFrom`。
`memory` 已经是 rollout 的归属包，`director` 与 `agents` 都依赖它，不产生新边、不违反分层守卫。
扩接口的代价是 `PlannerTools.Call` 多一个参数 —— 这是显式的、编译期可见的，比 ctx 里偷偷传更安全。

消费点：`createRolloutWriter`（`director.go:430-451`）新增 `delegationID` 参数
（或从 ctx 取），写进 `SessionMeta.DelegationID` 与 `SessionMeta.ParentSessionID`。
后者需要 Director 把自己当前 rollout 的 session_id 存到 `a` 上（照 `a.currentMemory` 的
set/defer-clear 模式，`director.go:797-798`；注意并发：委派类工具是 `Mutates=true` 不会并行，
但仍应与 `injectSubAgentMemoryMu`（`director.go:380`）同一把锁保护）。

顺带修 R3 的另一半：`director.go:752-753` 的
`a.injectSubAgentMemory(*psm, "", name)` 把空串换成真实 `callID`（(i) 之后 runner 手上有它），
使 `ParentID`（`director.go:401`）第一次有值。

### 5.2 `SessionMeta` 扩展

```go
// rollout_types.go:21-38 新增三字段（全部 omitempty，存量文件读出来是空串）
type SessionMeta struct {
    ...
    DelegationID    string  `json:"delegation_id,omitempty"`     // 父 Director 的 tool_call_id；Director 自己的文件为空
    ParentSessionID string  `json:"parent_session_id,omitempty"` // 父 rollout 的 session_id
    Git             GitInfo `json:"git,omitempty"`                 // 既有字段，R8：从今天起真正填
}
```

`Git` 的填充点：`executor.go:296-303` 与 `planner_run.go:123-130`。
取值走 `git rev-parse HEAD` / `git rev-parse --abbrev-ref HEAD` / `git config --get remote.origin.url`，
**必须用 `context.WithoutCancel(ctx)`**（Go 1.21+，仓库是 1.25.8）—— 否则在取消路径上重蹈 R6。
失败时留空并 `slog.Warn`，不阻塞启动。

清单侧同步：`RolloutSessionInfo`（`rollout_sessions.go:37-58`）加 `DelegationID` / `ParentSessionID`，
head-scan 在 `case "session_meta"`（`rollout_sessions.go:252-271`）里提取。

### 5.3 `agent_checkpoint` envelope（D3 / R4）

新增顶层类型（与 `session_meta` / `turn_aborted` / `compacted` 同级）：

```go
// rollout_types.go 新增
const RecordAgentCheckpoint = "agent_checkpoint"

type WorkspaceState struct {
    GitSHA     string   `json:"git_sha,omitempty"`
    GitBranch  string   `json:"git_branch,omitempty"`
    GitOrigin  string   `json:"git_origin,omitempty"`
    Dirty      bool     `json:"dirty"`
    DirtyFiles []string `json:"dirty_files,omitempty"`  // git status --porcelain，上限 200 条
    Cwd        string   `json:"cwd,omitempty"`
}

type AgentCheckpoint struct {
    SchemaVersion int            `json:"schema_version"`       // 1
    StopKind      string         `json:"stop_kind"`            // paused|cancelled|idle_timeout|total_timeout|max_steps
    AgentName     string         `json:"agent_name"`           // coding|repo|devops|chat|browser|meta|director
    DelegationID  string         `json:"delegation_id,omitempty"`
    Step          int            `json:"step"`                 // 已完成步数 = 重放起点
    MaxSteps      int            `json:"max_steps"`
    MessageCount  int            `json:"message_count"`        // 重放后应有的 llm.Message 条数（校验）
    Digest        string         `json:"digest"`               // sha256(重放消息序列规范化 JSON)[:16]
    Model         string         `json:"model,omitempty"`
    Tier          string         `json:"tier,omitempty"`       // fast|balanced|high
    Workspace     WorkspaceState `json:"workspace"`
    Nested        []string       `json:"nested_delegation_ids,omitempty"`
    CreatedAt     string         `json:"created_at"`
}
```

**刻意不存的三样东西**（都可从 rollout 派生，存了就会与代码漂移）：

| 不存 | 派生方式 |
|------|----------|
| 消息正文 | rollout 的 `response_item` 已经是同步全量（`executor.go:356-368, 689, 755`） |
| `Materialized`（已取回的冷工具） | 扫 rollout 里 `load_tool` 的 `function_call` + 成功的 `function_call_output`；`Surface.RestoreMaterialized` 已是现成 API（`director_surface.go:76`） |
| thinklink T&P 块 | 对重放出的 assistant 正文调 `compression.ExtractThoughtAndPlanBlocks`（`executor.go:673` 已在用） |

**写入点**：不新增 per-step 写入（那会让 rollout 体积翻倍且毫无收益 —— 文件尾就是游标）。
只在停止路径写一次，复用既有的终止块：

- 子 Agent：`executor.go:337-348` 的 deferred 块，在 `WriteRunAborted` 旁边加 `WriteAgentCheckpoint`
- Director：`planner_run.go:171-175` 的 `writeAborted`，同样加一条

同时补齐 R4：`executor.go:342` 的 `RunState` 填上 `StopReason`（今天恒空），
取值与 `StopKind` 同源。

`Digest` 的作用是**防漂移**：重放时按同样规则重算，不一致说明 rollout 被截断/损坏/被 GC 动过 → 降级到 C。
规范化规则必须固定并写测试：按 `role|content|tool_call_id|sorted(tool_calls)` 拼接后 sha256。

### 5.4 停止原因的来源

`StopKind` 必须区分四种，判据是 `context.Cause`（`director.go:762-769` 已有这套错误分类）：

| StopKind | 判据 |
|----------|------|
| `paused` | `pause.Gate.Wait` 返回 gate 的哨兵错误（不是 ctx.Err()）；或停止时 gate 处于 paused |
| `idle_timeout` | `errors.As(cause, &*activity.IdleTimeoutError)` |
| `total_timeout` | `errors.As(cause, &*activity.TotalTimeoutError)` |
| `cancelled` | `errors.Is(err, context.Canceled)` 且非上述 |
| `max_steps` | 走到 `executor.go:832` / `planner_run.go:656` |

gate 的哨兵错误必须是**可区分的类型**，不能复用 `context.Canceled`：

```go
// internal/pause/gate.go
type AbandonedError struct{ Reason string }   // 暂停中被 ctx 取消（用户 Esc 了一个已暂停的任务）
func (e *AbandonedError) Error() string
```

`Wait` 的语义：暂停中 ctx 被取消 → 返回 `*AbandonedError`（内含 ctx 的 cause），
调用方据此写 `stop_kind=cancelled` 而不是 `paused`。

---

## 6. B 层：重放

### 6.1 `ReadRolloutForReplay`（新增，不复用 `ReadRolloutMemory`）

`ReadRolloutMemory`（`rollout_reader.go:175`）返回 `*ConversationMemory`（`ChatMessage` 形状），
是为 TUI 展示与 Director memory 设计的，它会：丢 system 消息（除非 `KeepSystemMessages`，`rollout_reader.go:496`）、
丢 `Reasoning`（`:462`，只可选保留 summary）、工具输出截到 20000 rune（默认值 `:41`，截断实现 `:691`）、
上限 2000 条（默认值 `:39`）、
`opts.SubAgent` 时把**所有**消息打上 `IsSubAgent=true`（`:706-710`）—— 而 `planner_run.go:87-89` 会把这些全过滤掉。

重放要喂回**同一个模型**，丢 reasoning 会断掉 Anthropic 的 thinking 链
（`AGENTS.md` §2.3：reasoning/thinking 会回灌 provider），截断工具输出会让模型看到与暂停前不同的世界。
所以新增独立读侧：

```go
// internal/memory/rollout_replay.go（新增）
type Replay struct {
    Messages     []llm.Message          // 逐条重建，保留 Reasoning 与完整工具输出
    Step         int                    // 重放起点
    Materialized []string               // 从 load_tool 派生
    ThinkLink    []ReplayThinkLinkEntry // {Kind, Content, Step}，memory 本地形状，避免 import thinklink
    Digest       string                 // 重算的 digest
    Checkpoint   *AgentCheckpoint       // 文件里没有则为 nil（崩溃场景）
    DelegationID string
    AgentName    string
    Model        string
    Degraded     []string               // 显式记录重建过程中丢了什么，绝不静默
}

type ReplayOptions struct {
    MaxMessages int    // 0 = 不限
    TokenBudget int    // 0 = 不限；超预算则返回 ErrReplayTooLarge（调用方降级到 C）
    EstimateTokens func(string) int
}

func ReadRolloutForReplay(path string, opts ReplayOptions) (*Replay, error)
```

`memory` 已经 import `llm`（`rollout_convert.go` 的 `LLMMessageToResponseItems(msg llm.Message, ...)`，
被 `executor.go:362` 调用），所以返回 `[]llm.Message` 不产生新依赖边。
`ThinkLink` 用 memory 本地形状而不是 `thinklink.Entry`，避免 `memory → thinklink` 新边；
由 `agents` 侧转换。

`Degraded` 是硬要求：任何"没能完整重建"的情况都必须在这里留一条字符串
（例：`"checkpoint missing (crash?): step derived from message count"`、
`"reasoning not persisted for 3 messages"`、`"tool output truncated at source"`）。
它会一路带到 UI 与 Director 的工具结果头部。

### 6.2 `ResumeState` 与 executor seam（D5）

```go
// internal/agents/resume.go（新增）
type ResumeState struct {
    Messages     []llm.Message
    StartStep    int
    Materialized []string
    ThinkLink    []thinklink.Entry   // 需要 thinklink.Store 新增 Seed([]Entry)；写侧 Snapshot() 已存在（thinklink.go:100）
    DelegationID string
    Digest       string
}

func WithResumeState(ctx context.Context, s *ResumeState) context.Context
func resumeStateFrom(ctx context.Context) *ResumeState
```

`RunAgentLoop` 改三处（`executor.go`）：

```go
// 1) :221-223 之后
startStep := 0
if rs := resumeStateFrom(ctx); rs != nil {
    messages = rs.Messages                       // 已含 system + user + 全部历史
    history = append([]llm.Message(nil), rs.Messages...)
    startStep = rs.StartStep
    if cfg.Surface != nil && len(rs.Materialized) > 0 {
        cfg.Surface.RestoreMaterialized(rs.Materialized)   // 既有 API，director_surface.go:76
    }
    if cfg.UltimateThinkLink != nil && len(rs.ThinkLink) > 0 {
        cfg.UltimateThinkLink.Seed(rs.ThinkLink)           // thinklink.Store 新增
    }
    // 不写 system/user 的 rollout：它们在原文件里已经有了，重放写进新文件即可
}

// 2) :370-371 的 writeRollout(systemMsg)/writeRollout(userMsg) 包进 if rs == nil

// 3) :375
for i := startStep; i < cfg.MaxSteps; i++ {
```

**六个子 Agent（coding/repo/chat/devops/browser/meta）与 Custom Agent 一个都不用改** ——
它们只是把 ctx 传给 `RunAgentLoop`。这是 D5 选 ctx-value 而不是扩 `builtinAgent` 接口
（`director_surface.go:23-25`）的全部理由。

`stepNumber`（`executor.go:326`）初值改为 `startStep`，使 `turn_aborted` / `agent_checkpoint`
的 step 计数在多次暂停-继续之间单调递增。

### 6.3 Director 侧：`PendingResumption`（D7）

```go
// internal/agents/director/types.go 新增
type Resumption struct {
    DelegationID string
    ToolName     string   // "delegate_coding"
    AgentName    string   // "coding"
    RolloutPath  string
    Replay       *memory.Replay
}

type PlanInput struct {
    Input   string
    Mem     *memory.ConversationMemory
    TaskID  string
    Resumed bool
    PendingResumption *Resumption   // 新增
}
```

`planner.Run` 在进 LLM 循环**之前**（`planner_run.go:190-194` 那段 initial rollout 写入之后）处理它：

1. 用 `memory.WithDelegationID` + `WithResumeState` + `WithRolloutWriter`（**新的** writer，
   续写会产生第二个 `_{agent}.jsonl` 文件，靠 `delegation_id` 关联）构造 ctx
2. 调 `p.cfg.Tools.Call(ctx, resumption.ToolName, originalArgsJSON, resumption.DelegationID)`
   —— 原始 args 从 Director rollout 里那条 dangling `function_call` 取（`ResponseItem.Arguments`，
   `rollout_types.go:83`）
3. 拿到结果后 `in.Mem.AddToolMessage(result, resumption.DelegationID)` + `writeDirectorRollout(tool msg)`
   —— 这就是 §4.3 的第二条 output
4. 然后正常进循环。Director 的 LLM 看到的是"委派成功返回了"，它不知道发生过暂停

**这一步不调 LLM**，所以恢复本身是确定性的、零 token 成本的（除了子 Agent 重放自己的调用）。

`PendingResumption` 的传递通道复用既有路径：`TaskRequest.WithResumed`（`app.go:597`）→
`DirectorAgent.SetResumedTask`（`director.go:426-428`）旁边加 `SetPendingResumption`，
在 `run()`（`director.go:789-901`）里取出来塞进 `PlanInput`，并照 `resumedTask` 的做法用完即清
（`director.go:790-791`）。

### 6.4 降级判据 → C（必须显式）

按顺序检查，任一命中就降级，**并把原因写进 `Degraded` 与 UI**：

| # | 判据 | 为什么不能重放 |
|---|------|----------------|
| 1 | 找不到带 `delegation_id` 的子 Agent rollout | R3 之前的存量文件；或 `~/.yagent` 没同步 |
| 2 | `Digest` 与重算值不符 | 文件被截断/损坏/被 GC 动过 |
| 3 | `MessageCount` 与实际重建条数不符 | 同上 |
| 4 | 重放后 token 超 `replay_token_budget` | 会立刻触发压缩，重放失去意义 |
| 5 | `Workspace.GitSHA` 与当前 HEAD 不同 | 工作区已变，子 Agent 的"我已经改了 X"是假的 |
| 6 | `AgentName == "browser"` | D8 |
| 7 | `StopKind == "max_steps"` | 重放只会立刻再次耗尽步数 |
| 8 | 重放过程中 LLM 报错（模型/provider 变了） | 不可重试成原样 |

判据 5 的策略可配（`degrade_on_workspace_change = "warn" | "block"`，默认 `warn` = 降级但继续）。
**注意**：判据 5 在"换机器"场景下几乎必然命中 —— 用户 pull 之后 HEAD 就变了。
所以 C 不是罕见的兜底路径，而是换机器场景的**主路径**，必须做好（§7）。

---

## 7. C 层：交接兜底

复用 Handoff v2（`docs/blackboard-handoff-design.md` §3）与既有便签机制，**不新增存储**。

### 7.1 `NoteInput.Status` 扩词表

`note.go:61` 的注释今天是 `// "ok" or "error"`，扩为 `ok | error | paused | degraded`。
`NoteMeta.Degraded`（`note.go:48`）已存在（v1 回落标记），复用它承载降级原因。

### 7.2 降级报告的合成

输入：子 Agent rollout 的尾部 + 派生出的 T&P 块 + `agent_checkpoint`。
输出：一份结构化报告，作为原 `call_id` 的第二条 tool result，同时落一张 `status=degraded` 的便签：

```
[Delegation Resumed as Handoff — 精确重放不可用]
degradation: <判据编号与原因，逐条列出>
agent: coding   stopped_at: step 37/150   stop_kind: cancelled
workspace: git sha abc1234 → def5678 (changed), dirty files: 3

## 已确认完成
- <从最后 N 条 assistant 消息与成功的 tool output 提取>

## 进行到一半（副作用状态未知）
- <最后一个未配对/报错的 tool call>

## 剩余计划
- <最后一个 <thought> 块里 [ ] 与 [>] 的条目，原文照抄>

## 建议的续委派任务
<一段可直接作为 delegate_* task 参数的文本>
```

**不调 LLM 合成**（那会引入不确定性与额外成本，且暂停时可能正好没有可用 provider）。
纯程序化提取：T&P 块用 `compression.ExtractThoughtAndPlanBlocks`，
"已完成"用成功的 `function_call_output` 的工具名 + 首行，
"进行到一半"用 `DanglingToolCalls`（`rollout_reader.go:760`）。

### 7.3 Director prompt 一条

`director.prompt.md` 加一段（照 `AGENTS.md` §9.1"改行为要同时改 prompt 与代码"）：
收到 `[Delegation Resumed as Handoff]` 开头的工具结果时，**不要重做已完成的部分**，
直接用"建议的续委派任务"发起下一次委派，并把便签 ref 放进 `context_refs`。

---

## 8. UI 与 API

### 8.1 任务状态

`http/types.go:70-74` 加一档：

```go
TaskStatusPaused = "paused"
```

`Task`（`http/types.go:76-89`）加 `Resumption *ResumptionInfo`（agent/step/stop_kind/degraded 摘要），
供 `GET /api/task_status` 与 TUI 侧栏展示。

### 8.2 TUI

- 命令：`/pause`、`/continue`（`tui_update_commands.go` 的命令表，与既有 `resume`/`rs` 同处）
- 键位：**待定**，列入 §14。约束来自 `AGENTS.md` §6.8：快捷键提示只出现在 tab 行右侧与面板底边框，
  状态栏不含任何键帽；新增键位必须过 `tui_chrome_test.go` 的宽度矩阵
- 暂停态渲染：侧栏 Activity 显示 `⏸ PAUSED coding step 37/150`；
  按 `AGENTS.md` §6.8 的单一渲染器原则，这个字符串只在 model 的快照方法里算一次
- **事件通道必须保持武装**：`listenForEvents` 是一次性 cmd、事件无重放队列（`AGENTS.md` §11 坑 6）。
  暂停期间没有事件产生，但恢复后必须有；且暂停态若推入任何 dialog，
  弹窗守卫只放行 KeyMsg/taskEventMsg/tickMsg，新增消息类型要显式放行
- `handleTaskCompleteMsg`（`tui_update_tasks.go:1023-1032`）今天会**换新 ctx 并标 finished** ——
  暂停不能走这条路，否则 gate 所在的 ctx 被换掉，恢复无门。必须新增一条 paused 分支，
  保留原 ctx 与原 CancelFunc
- `/resume` 对话框（`components/resume_dialog.go`）在检测到 `agent_checkpoint` 时多一个选项：
  `从 coding step 37 继续`（B/C 路径）vs 既有的 `从 Director 继续`

### 8.3 HTTP

- `POST /api/pause_task`、`POST /api/continue_task`（照 `handleCancelTask`（`server.go:385-411`）
  与 `TaskManager.CancelTask`（`task_manager.go:148-164`）的形态）
- WebSocket 的 `realtime` envelope 加 `task_paused` / `task_resumed`
  → **必须改 `protocol/agent-events.yaml` 再重新生成**（`AGENTS.md` §6.6：不要手改
  `internal/protocol/agent_events.go`，文件头写着 DO NOT EDIT）

### 8.4 配置

```toml
[resume]
enable = true                       # 总开关
replay_token_budget = 0             # 0 = 用模型目录阈值；超预算降级到 C
max_replay_messages = 2000          # 0 = 不限
degrade_on_workspace_change = "warn" # warn | block
checkpoint_workspace = true         # 停止时是否跑 git status（大仓库可能慢）
```

整表缺省时按 `DefaultResumeConfig()` 填。**不要重蹈 `[blackboard]` 的覆辙**
（`AGENTS.md` §11 坑 13）：这里没有"0 有语义"的键，所以只看段级存在性即可，
但 kill-switch 仍给一个环境变量 `YAGENT_RESUME=0`，与 `YAGENT_BLACKBOARD=0` 同构。

---

## 9. 副作用诚实性（R6，必须先修）

`git_checkpoint.go:225-288` 的 `OnAgentExit` 改造：

```go
func (g *GitCheckpointManager) OnAgentExit(ctx context.Context, agentErr error) error {
    // 收尾必须能跑：ctx 在取消/暂停路径上已死，但 git 命令与 ctx 无关
    cleanCtx := context.WithoutCancel(ctx)

    interrupted := ctx.Err() != nil || pause.From(ctx).Paused()
    g.mu.Lock()
    if g.ended { g.mu.Unlock(); return nil }
    g.ended = true
    g.session.Completed = !interrupted          // ← 不再无条件 true
    if interrupted { g.session.Interrupted = true }
    g.mu.Unlock()

    dirty, _ := g.isWorktreeDirty(cleanCtx)
    if dirty {
        _, _ = g.runGitCommand(cleanCtx, "add", "-A")
        _, _ = g.runGitCommand(cleanCtx, "commit", "-m",
            fmt.Sprintf("checkpoint: interrupted at %s", stopKindOf(ctx)))
    }
    if interrupted {
        // 保留分支、保留 tag、保留状态文件：这是"继续"时判断工作区的唯一依据
        return g.persistState(cleanCtx)
    }
    ... 既有 squash / cleanup / clearState 路径 ...
}
```

`runGitCommand`（`git_checkpoint.go:706-716`）今天用 `exec.CommandContext(ctx, ...)`，
换 `cleanCtx` 后就能真正执行。**错误仍被 `_, _ =` 丢弃** —— 这一条要改：
收尾失败必须 `slog.Error` 且反映到 `agent_checkpoint.Workspace`（否则又是一次静默失效）。

---

## 10. 一致性闸门（D1/D6 的补偿控制，硬约束）

照 `AGENTS.md` §8.3 与 `docs/blackboard-handoff-design.md` §11 的做法：
每条不变量都要有一个**能失败的测试**，而不是"跑一遍看没报错"。

| # | 闸门 | 钉住什么 | 位置 |
|---|------|----------|------|
| G1 | `TestPauseStopsAtStepBoundary` | 暂停发生在工具批次中间时，当前批次**跑完**才停；不多跑一个 step | `internal/agents/e2e_pause_test.go`（新增） |
| G2 | `TestResumeReplaysExactMessages` | 重放后第一步发给 LLM 的 messages 与暂停前**逐条相等**（含 Reasoning），step 计数连续 | 同上 |
| G3 | `TestToolPairingNormalizersAgree` | 同一 call_id 两条 output 时，`llm.NormalizeMessages` 与 reader 的 `normalizeToolPairing` 选出**同一条** | `internal/memory/rollout_pairing_test.go`（新增） |
| G4 | `TestIdleTimeoutFrozenWhilePaused` / `TestTotalTimeoutExcludesPaused` | 暂停 3× idle 阈值不被取消；total 计时扣掉暂停时长 | `internal/agents/activity/activity_test.go` |
| G5 | `TestCancelWritesUsableCheckpoint` | 硬 cancel 后盘上有一条 `agent_checkpoint`，且 `ReadRolloutForReplay` 能重建出可重放状态 | `internal/memory/rollout_replay_test.go`（新增） |
| G6 | `TestDegradationIsNeverSilent` | §6.4 的八条判据**每条**都有一个能触发它的夹具，且 `Degraded` 非空、UI/工具结果头部可见 | `internal/agents/resume_degrade_test.go`（新增） |
| G7 | `TestInterruptedCheckpointKeepsGitState` | ctx 已取消时收尾 git 命令**真的执行了**（用临时仓库断言 commit 存在）、状态文件保留、`Completed != true` | `internal/agents/git_checkpoint_test.go` |
| G8 | `TestNoPerStepCheckpointWritten` | 正常跑 N 步只产生 1 条 `agent_checkpoint`（防将来有人加 per-step 快照把 rollout 撑爆） | `internal/memory/rollout_replay_test.go` |
| G9 | 既有闸门不许红 | `TestAgentSurfacesGolden`（不加新工具，golden 不应变）、`TestMessagesForLLMCallIsIdempotent`、`scripts/check_all.sh`、`RACE=true` 跑 `activity`+`agents`（gate 是并发原语） | — |

G6 是 `AGENTS.md` §11 坑 15 那条教训的直接应用：**只测"生产路径没降级"等于没测**，
删掉任何一条判据都不会有人发现。

G2 的实现约束：e2e harness 的脚本引擎**按 Agent 身份分派、不按全局调用序号**
（`AGENTS.md` §8.3），且**必须先看 ctx**（忽略 ctx 的 mock 会把"取消没传播"掩盖掉）。
新增的暂停/恢复用例要沿用这两条，否则会写出一个看起来通过、实际测不到东西的测试。

---

## 11. 行为变更清单（一次性落地会顺带改变的，逐条签字）

| # | 变更 | 影响面 | 是否可回退 |
|---|------|--------|-----------|
| B1 | reader 的 tool 配对从 first-wins 改 **last-wins**（§4.3） | 所有 `/resume`；`DroppedOrphanOutputs` 语义变（新增 `SupersededOutputs`） | 可，改回一个分支 |
| B2 | `SessionMeta` 开始真正填 `Git`（R8） | rollout 文件多三行 git 信息；启动时多 3 次 git 调用（`WithoutCancel`，失败留空） | 可 |
| B3 | 子 Agent 的 `turn_aborted` 开始带 `StopReason`（R4） | 存量文件为空，读侧需容忍 | 可 |
| B4 | 子 Agent 的 thinklink 开始落 rollout（R7） | 子 Agent rollout 体积增加（每条 T&P 一行） | 可 |
| B5 | git checkpoint 中断路径**不再** squash/cleanup/删状态（§9） | 工作区会留下 agent 分支与 tag，需要用户或后续恢复来清 | 可，但会重新引入 R6 |
| B6 | `injectSubAgentMemory` 的 `ParentID` 第一次有值（§5.1） | Director memory 里那条 `IsSubAgent` 摘要消息可被溯源；`planner_run.go:87-89` 仍过滤它，LLM 上下文不变 | 可 |
| B7 | `PlannerTools.Call` 签名多一个 `callID` 参数（§5.1） | 编译期可见，改一处接口 + 一处调用 + 测试假实现 | 可 |
| B8 | `TaskStatus` 新增 `paused`（§8.1） | HTTP 客户端若对 status 做穷举 switch 会漏一档 | 可 |
| B9 | Esc 的语义**不变**（仍是硬 cancel），但现在 cancel 之后也能继续 | 用户预期变化：以前"停了就废了"，现在"停了还能捞回来" | — |

B1 与 B5 需要重点评审：B1 改的是恢复语义，B5 改的是工作区留痕。

---

## 12. 实施次序

### P0 前置修复（0.5-1 天，**独立可合，不依赖本方案任何部分**）

每条都是独立的真 bug/缺口，即使 A/B/C 全不做也该修：

- **P0.1** reader last-wins + G3 闸门（B1 / R5）
- **P0.2** git checkpoint 取消路径（§9 / R6）+ G7
- **P0.3** `SessionMeta.Git` 落盘（B2 / R8）
- **P0.4** 子 Agent thinklink 落 rollout（B4 / R7）+ 子 Agent `turn_aborted` 补 `StopReason`（B3 / R4）

验证：`bash scripts/check_all.sh` 全绿；P0.2 需要 `RACE=true` 跑 `internal/agents`。

### P1 活暂停（A，1-2 天）

- **P1.1** `internal/pause` 包（Gate + ctx value + `AbandonedError`）
- **P1.2** `activity` 计时冻结（§4.2）+ G4
- **P1.3** executor / planner 两个接入点（§4.1）+ G1
- **P1.4** `TaskStatusPaused` + TUI `/pause` `/continue` + `handleTaskCompleteMsg` 的 paused 分支（§8.2）
- **P1.5** HTTP 两个端点 + `protocol/agent-events.yaml` 加 `task_paused`/`task_resumed` 并重新生成
- **P1.6** 停止原因四分（§5.4）：`StopKind` 落到 `turn_aborted.StopReason`，Director 侧
  `planner_run.go:646-648` 区分 `paused` 与 `cancelled`

**P1 交付即可用**：TUI 里能停能继续，零损失。但进程一死就回到今天 —— 这是 P2 要解决的。

### P2 检查点与重放（B，3-4 天）

- **P2.1** `delegation_id` 贯通（§5.1）：ctx key 放 `memory`、`PlannerTools.Call` 扩参（B7）、
  `SessionMeta` 两字段、`RolloutSessionInfo` 同步、`ParentID` 修正（B6）
- **P2.2** `agent_checkpoint` envelope + 两个写入点（§5.3）+ G8
- **P2.3** `ReadRolloutForReplay`（§6.1）+ `thinklink.Store.Seed` + G5
- **P2.4** executor 重放 seam（§6.2）+ G2
- **P2.5** `PendingResumption` + planner 预执行阶段（§6.3）
- **P2.6** 降级判据八条（§6.4）+ G6
- **P2.7** 恢复入口：`/resume` 对话框的"从子 Agent 继续"选项（§8.2）
- **P2.8** `[resume]` 配置表 + 两个 TOML 模板同步（§8.4）

### P3 交接兜底（C，1 天）

- **P3.1** `NoteInput.Status` 扩词表 + `Degraded` 承载降级原因（§7.1）
- **P3.2** 降级报告合成（§7.2，纯程序化，不调 LLM）
- **P3.3** `director.prompt.md` 一段（§7.3）

合计约 **6-8 天**。P0 与 P1 可以先合，P2 是主体，P3 收尾。

---

## 13. 禁止事项

1. **不要把暂停实现成"cancel + 重建 ctx"**。ctx 不可复活，且子 Agent 的 ctx 已被
   `toolbatch.Run`（`executor.go:781`）、`runToolCall`（`executor.go:125`）与在飞的 `ad.Call` 捕获，
   重接点分散在 4 层（§4.1）
2. **不要在 gate 里持锁等待 LLM 返回**。`Wait` 只在 step 边界被调用，阻塞的是循环 goroutine 自己；
   `Pause`/`Resume` 必须是纯原子/短临界区（照 `activity.Heartbeat` 的 non-blocking 约定，`activity.go:208-214`）
3. **不要让 Director 的 LLM 决定要不要恢复**（D7）。恢复是代码路径，模型只看到一次成功的委派。
   把恢复决策交给模型会导致"有时候重做、有时候接着跑"的不可复现行为
4. **不要新增 per-step 全量消息快照**。rollout 已经是同步全量（`executor.go:356-368`），
   重复存会让 rollout 体积翻倍且毫无收益（G8 钉住）
5. **不要在 checkpoint 里存 system prompt 全文**。它可由 `prompt.md` + `RepoContext` + `Surface.Catalog()`
   重算（`executor.go:201-211`）；存了就会与代码漂移，而漂移的表现是"恢复后 Agent 行为变了"
6. **不要静默降级**。§6.4 的八条判据任一命中都必须写 `Degraded`、发事件、在工具结果头部标注（G6）
7. **不要在 `director` 子包里 import `internal/agents`**（`scripts/check_no_agents_import.sh`）。
   §5.1 选 `memory` 放 ctx key 就是为了这条
8. **不要手改 `internal/protocol/agent_events.go`**（文件头 DO NOT EDIT）。新事件类型改
   `protocol/agent-events.yaml` 再重新生成（`AGENTS.md` §6.6）
9. **不要给 Browser 委派做精确重放**（D8）。浏览器状态不可序列化，硬做的结果是"看起来恢复了，
   实际上页面早就不是那个页面"
10. **不要在取消路径上用原 ctx 跑收尾命令**（R6 的教训）。所有 cleanup 一律 `context.WithoutCancel`
11. **不要让 `handleTaskCompleteMsg` 在暂停时换 ctx**（`tui_update_tasks.go:1023-1032`）。
    换了就等于把 gate 所在的 ctx 树整个丢掉

---

## 14. 未决项（实施中遇到请先报告，不要自行决定）

1. **TUI 键位**：`/pause` `/continue` 命令行已定，键位待定。需要先查 `tui_model.go` 的 keymap
   与既有 10 个 dialog 的键位占用，避免冲突；且按 `AGENTS.md` §6.8，
   提示只能出现在 tab 行右侧与面板底边框
2. **换机器时 `~/.yagent` 的同步方式**：本方案假设 rollout + notes 能同步过去（R9 已论证清单侧不受
   projectID 影响）。是否要额外把 `agent_checkpoint` 冗余写一份到仓库内（如 `.yagent/checkpoints/`，
   跟着 git 走）？倾向**不写**（污染工作区、且与工作区 git 状态形成循环依赖），但需要确认
3. **`--prompt` 非交互模式**：它连 cancel func 都没有（`main.go:304`）。要不要加 `--continue <taskID>`？
   倾向 P2 之后单独一次改动
4. **暂停期间用户能否继续输入新消息**：多任务/多 tab 场景下 gate 是 per-task 的，
   但 TUI 的输入框是全局的。暂停一个任务时输入的新消息该进哪个任务？
5. **`delegate_total_timeout` 与长时间暂停**：§4.2 让 total 计时扣掉暂停时长，
   意味着一个暂停了三天的任务恢复后仍有完整的 20m 预算。这是期望行为吗？
   倾向是（用户主动暂停不该被算成任务耗时），但需要确认
6. **重放时的路由档位**：`Tier` 存在 checkpoint 里，但恢复时模型目录可能已变
   （`dist/data/models.json` 是被跟踪的，`AGENTS.md` §11 坑 2）。
   档位失效时是回落到角色 baseline（`[routing.baselines]`）还是重新 `Classify`？
   倾向回落 baseline（确定性优先），并在 `Degraded` 里记一条
7. **嵌套委派的恢复深度**：Coding → Browser 的二级委派，Browser 没有独立 rollout writer
   （`coding.go:204` 直接调 `a.BrowserAgent.Run`，消息混进 coding 的文件）。
   D8 已把 Browser 排除在精确重放外，但"暂停时 Browser 正在跑"这个状态怎么表达？
   倾向：暂停在 Coding 的 step 边界生效，Browser 的当前动作跑完后一起停；
   恢复时 Browser 那部分走 C
8. **`artifact` 与 `notes` 的 GC 与检查点的相互作用**：`[blackboard] ttl` 默认 168h
   会整板清理（`AGENTS.md` §6.4）。一个暂停超过 168h 的任务，它的便签板可能已经被 GC 了。
   是否要让带 `agent_checkpoint` 的 board 免于 GC？

---

## 15. 落地状态

| 阶段 | 状态 | 备注 |
|------|------|------|
| P0 前置修复 | 未开始 | 四条独立真 bug，可先合 |
| P1 活暂停（A） | 未开始 | 交付即拿到可用的停止/继续 |
| P2 检查点重放（B） | 未开始 | 承重层，跨进程/换机器靠它 |
| P3 交接兜底（C） | 未开始 | 换机器场景的实际主路径（§6.4 判据 5） |

落地后请更新本表，并同步 `AGENTS.md`：§6.2（执行循环多一个 gate 接入点）、
§6.6（新事件类型）、§6.7（rollout 新记录类型 —— 顺手修掉 `world_state` 那句过期描述）、
§7（`[resume]` 配置表）、§11（新增坑：暂停不能走 `handleTaskCompleteMsg` 的换 ctx 路径）。
