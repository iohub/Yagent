# 结构化并发：supervisor + 委派隔离 + 落盘管道 设计方案 v1

> 状态：待实施。分支建议 `feat-structured-concurrency`（当前 `feat-blueprint-v2` 上的蓝图 R2–R6 与本方案无耦合）。
> 所有行号核对时间 2026-10-17，HEAD `5b26228`。带 ✓ 的行号是本文作者逐条打开文件看过的；
> 带 † 的来自探查阶段，实施时必须先复核（见附录 A）。

---

## 0. 决策记录（已定，不再讨论）

| # | 决策 | 理由 |
|---|------|------|
| **D1** | 新增叶子包 `internal/supervise`，**全仓裸 `go` 收归一处**；不引入 `golang.org/x/sync/errgroup` | errgroup 给不了我们真正缺的三件事：goroutine 的**具名**（泄漏时能说出是哪一条）、统一 recover（今天 12 个站点裸奔，其中两条会调 LLM）、以及挂在 ctx 上的**分层归属**（委派结束后要能等它自己那批）。为这三件事引入一个外部依赖不如自己写 100 行 |
| **D2** | **每次委派新建 Agent 实例**（`SpawnForDelegation`），嵌套的 Agent 指针（Coding→Browser、Repo→DevOps）改成工厂 | 内建 Agent 的结构体几乎全是不可变装配（见 §6.2 的字段清单），真正可变的状态只有 `thinkLink` 和 Surface。单例复用是并发委派的根障碍，而克隆的成本是纯内存的 |
| **D3** | rollout 写入**入队即返回**，但六个停点**强制 drain** | 停点中最硬的一条是检查点：`WriteAgentCheckpoint` 之前必须排干，否则 B 层重放被判成不可信（§7.3，本方案最大的耦合风险） |
| **D4** | **删除 `ExecutorConfig.Adapters`**，只留 `Surface` 一条解析路径 | R1 那个 bug 的根就是"同一个概念两处真相"。留着 `Adapters` 就是留着第二条会漂走的路径；~20 个测试夹具改成 Surface 是一次性机械成本 |
| **D5** | **取消 `pendingSubAgentMemory` 单槽**，注入移到 `delegateTo` 内、按自己的 `callID` 直接做 | 单槽在并发委派下必然张冠李戴（R3）。它存在的唯一理由是"executor 拿不到 Director 的 memory"，而 `delegateTo` 本来就同时握着 result 和 callID |
| **D6** | `ConversationMemory` 加锁 + `Snapshot()` 读法 | 它今天是裸 slice（memory.go:62-65 ✓），并发委派下多个 goroutine 往同一 slice 追加 |
| **D7** | 并行准入**只看能力标签**，不做参数/目标路径分析 | 加参数分析等于引入一份新的、没人能验证的"独立性"推断。写类工具已经靠 `Mutates`/`Serial` 串行化，够用 |
| **D8** | 配置面只加两个键 + 一个 kill-switch；rollout 队列**不设任何旋钮** | 队列深度/批量是工程细节，没有"用户改了能观察到什么"的合理答案。本仓刚因 13 组"解析了但没人读"的键做过清理（AGENTS.md §7），不要再往回长 |
| **D9** | 投机并行（双模型投票、压缩与推理重叠、委派期预取蓝图）**不在本方案内** | 这些是新能力，需要各自的收益论证。本方案只负责把并发地基做对，让它们变成"接得上"的状态 |
| **D10** | 一条分支落全部，阶段只是**提交切分顺序**，不是"先合一半" | 半套并发的表现比没并发更糟：并发了但账本会漂、隔离了但没开并发等于零收益 |

---

## 1. 需求与语义定义

"让 yagent 更多用 goroutine"在这个仓库里有三个互不相同的目标，必须先分开，否则讨论会互相踩：

- **G1 单步内的工具并发**：一次 LLM 回复给了 5 个 `read_file`/`search_by_regex`，它们之间无依赖 → 应并发。今天**机制存在但完全不生效**（R1）。
- **G2 跨委派的并发**：Director 一条回复里同时发 `delegate_repo` + `delegate_chat` → 两个子 Agent 应并行跑。今天 Director 侧准入是对的，但被 R3/R4/R5/R6 四处分歧卡住，**不敢真开**。
- **G3 后台化（off-thread）**：与模型语义无关的落盘、索引、日志、清理，从 step 关键路径上挪走。今天**一行都没挪**（R2）。

本方案覆盖 G1 + G2 + G3，顺序为 G3 → G1 → G2（先让账本与生命周期可信，再放大并发面）。

**"并发"在本仓的边界**（不变的语义，必须守住）：

1. 写类工具与写类委派**继续串行**（`delegate_coding/devops/browser/meta` 由 `Mutates`/`Serial` 挡在 `director.go:988`）。
2. `agent_exit` / `ask_user_for_help` 永远独占，不参与并行段（`isInteractiveUserTool`，`executor.go:792`）。
3. 一个 step 内工具的**提交顺序**恒等于模型给出 `tool_calls` 的顺序 —— `toolbatch` 已经保证 commit 在父线程按 index 执行（`toolbatch/run.go:83-85`），不许改。
4. 暂停闸门仍然只在 step 边界阻塞（`executor.go:421` ✓）；并发**不新增**可中断点。

---

## 2. 现状与根因（逐条带证据）

### R1（最致命）子 Agent 的工具并行在生产里根本没生效 ✓

`internal/agents/executor.go:790-801` 的准入谓词：

```go
isParallelizable := func(i int) bool {
    name := toolCalls[i].Function.Name
    if isInteractiveUserTool(name) || isSerialTool(name) { return false }
    for _, t := range cfg.Adapters {        // ← 只扫 Adapters
        if t.Name() == name { return t.IsReadOnly() }
    }
    return false                             // ← 生产路径恒走这里
}
```

而生产里 `cfg.Adapters` 是空的：

- 五个子 Agent 全部只设 Surface —— `coding.go:376` ✓、`repo.go:164` ✓、`chat.go:71` ✓、`devops.go:80` ✓、`browser_agent.go:114` ✓；
- 全仓 grep `Adapters:` 在非测试代码里**零命中**；
- `executor.go:261-278` 那段往 `Adapters` 追加 `read_note`/`read_artifact` 的代码，前提是 `cfg.Surface == nil`，生产不成立。

于是 `toolbatch.Plan`（`plan.go:31`）拿到的谓词恒 false → 全是单元素串行段 → `toolbatch.Run`（`executor.go:858` ✓）虽然被调用，却**一个 goroutine 都不开**。

三处佐证这是"漏接"而不是"设计如此"：

1. 同一个文件里 `resolveAdapter`（`:150-165` ✓）和 `defsForStep`（`:287-292` ✓）都正确做了"Surface 优先、回退 Adapters"，**只有这个谓词没有**；
2. Director 侧的同名谓词 `director.go:987-993` ✓ 走 `resolveTool`（`:975-985` ✓，Surface 优先），是对的 —— 两边不对称本身就是证据；
3. `director.prompt.md:84,155` 向模型承诺"执行器会并发跑这些调用"，这句话对子 Agent 是假的。

**为什么没人发现**：`executor_parallel_test.go` 的 8 个用例全部注入 `Adapters:`（`:210,266,331,415,478,531,573,612,644`），钉住的是一条生产不存在的路径。这是 AGENTS.md §11 坑 25（"配置项存在 ≠ 配置项生效"）的并发版。

### R2 承载智能的那条路径 100% 同步在一根 goroutine ✓

`RunAgentLoop` 一步之内：pause 闸门（`:421` ✓）→ 心跳（`:427`）→ 消息归一化（`:459`）→ 三档压缩（`:472`，其中 emergency 在 `:515` ✓ **同步调一次 90s 上限的 LLM**）→ 发布事件（`:597-603`）→ **阻塞式 LLM 调用**（`:605-607` ✓）→ 计量刷新（`:611`）→ rollout 写 token 账（`:696`）→ assistant 落盘（`:764`）→ thinklink 抽取（`:732-762`）→ 工具批（`:849-887`）→ 逐条提交含 rollout 写（`:831` ✓）→ step 事件（`:892-894`）。

全在一根线程上，**下一步的 LLM 调用必须等上一步的落盘完成**。真正开 goroutine 的只有四类：`toolbatch`（R1 说的那条，生产不触发）、dispatcher 每 consumer 一条、`activity` 看门狗、MCP `readLoop`。全仓 **没有一处 errgroup**，`tui/tui_resume_stop.go:23` † 还专门写了注释拒绝用它、手写 cap 8 的信号量。

### R3 委派结果靠**单槽**交接 ✓

`director.go:77` `pendingSubAgentMemory *AgentResult`（一个指针，不是 map），`:487-491` set / `:493-499` take-and-clear，`:792-796` ✓ 在每次工具调用之后 `take()` 然后 `injectSubAgentMemory(*psm, callID, name)`。

两个并发 `delegate_repo` 的时序是：A 完成→set(A)，B 完成→set(B)（覆盖），父线程给 A 提交→take 拿到的是 **B**，并以 A 的 `callID` 注入记忆。**摘要挂错委派，且不报错**。有 mutex 保护不崩，语义照样是错的。

### R4 `ConversationMemory` 无锁 ✓

`memory/memory.go:62-65` 是 `Messages []ChatMessage` 裸 slice，没有任何同步字段。planner 线程在 `planner_run.go` 里 `AddToolMessage`/`AddAssistantMessage`，子 Agent 的结果注入也在写它 —— 今天靠串行委派侥幸不撞，R1 修完 + G2 一开就是直接竞态。

### R5 事件无序、TUI 流式槽**按 Agent 名一个槽** ✓

`messaging/message_event.go:66-89` ✓ 的 `Event` 有 `ID/Source/TraceID/Metadata`，**没有序号、没有委派字段**。TUI 的 `aiStreamActiveEntries[agentName]`（`tui_update_tasks.go:269,330,354` †）一个名字一个槽 → 两个并发 Coding 的 `ai_chunk` 会粘成一坨。便签/artifact 靠 `boardID`+`agent` 目录区分，但**展示层没有这个凭据**。

### R6 日志按**进程全局** taskID 路由 ✓

`logging/logger.go:28-33` ✓ 有 `currentTaskID string` + `taskMu sync.RWMutex`（锁本身没问题），问题是它是**进程单值**：`SetCurrentTaskID`（`:205-212` ✓）由每个任务设置（`tui_tasks.go:210` †、`task_executor.go:117` †），并发任务/多 tab 的 `llm-*.log`、`tool-*.log` 会写进**彼此的目录**，`ensureLLMLogFile` † 还在无锁改全局 `llmLogger/llmLogFile`。

### R7 锁内做 IO / 起子进程（三处，全部可复现）

| 位置 | 持的是什么锁 | 锁里干了什么 |
|------|-------------|-------------|
| `datamanager/data_manager.go:122-129` ✓ | 单任务 `ws.mu` | `doFlush` 文件写 **加** `updateIndexFromFile`（`os.Stat` + 整文件 parse） |
| `datamanager/data_manager.go:252-268` † | 全局 `dm.mu` + 每个 `ws.mu` | `FlushAll` 遍历所有任务做文件 IO |
| `agents/git_checkpoint.go:137-139` ✓ | `g.mu` | `OnAgentStart` 里跑多个 git 子进程（切分支/stash） |
| `http/task_manager.go:88-96,124-126` ✓ | 全局 `tm.lock` | `SetTaskProgress` 持锁跨 `task.Socket.Write` —— **一个慢 WebSocket 客户端串行化整个任务管理器** |

### R8 无人拥有、无人等待的 goroutine †

- `agents/knowledge_hook.go:38` ✓ 裸 `go func()` + `context.Background()` + 60s：每个子任务结果起一条，**无队列、无 WaitGroup、永不等待**；`repo.go:208`/`coding.go:419` † 各调一次。
- `agents/consolidation_worker.go` †：`Submit` 队列满就 drop+warn（`:91-99`），`Stop()` 能阻塞 ~90s，close 之后再 `Submit` **直接 panic**（`repo.go:201` †）。
- `browserctl/manager.go:463` † 的 idle sweeper 没有 WaitGroup → `Close` 从不 join。
- **12 个站点没有 recover**（†，含上面两条会调 LLM 的），能让进程崩。
- `tui/tui_update_tasks.go:1026-1028` †：`newCancel` 直接覆盖 `m.currentTask.CancelFunc`，旧的那个没人调 → ctx 泄漏。

### R9 rollout 的计数字段被两套同步混用 ✓

`memory/rollout_writer.go:252-256` ✓ 在 `w.mu` 里做裸 `w.msgCounter++`，而 `:362-366` ✓ 的 `NextMessageID()` 对**同一个字段**做 `atomic.AddInt32(&w.msgCounter, 1)`。两套同步管一个变量 = 真竞态。`Close()`（`:400-406` †）还在不持锁的情况下碰 `file`/`closed`。

### R10 每条记录一次 `write(2)`，停点还要整文件重读 †

`RolloutWriter` 每条记录 = `writeEnvelope` → `encoder.Encode` 直接写 `*os.File`，**无 bufio**；一个 step 约 3–6 条。更关键的是终止路径：`WriteCheckpoint` → `CollectWorkspace` → `FinalizeCheckpoint` **重读整个 rollout 文件**（`rollout_checkpoint.go:104-105,167` †）。

### R11 MCP 请求没有自己的 deadline ✓

`mcp/client.go:55-66` ✓：`NewMCPClient` 把 `cfg.RequestTimeout` 归一化进局部变量 `timeout`，**结构体字面量里没有它**。于是请求只有调用方 ctx；`app.go:203-213` † 传进来的值是一行死配置。附带 `IsAlive()`（`:214` †）无锁读 `cmd.ProcessState`，而 `cmd.Wait()` 会写它。

### R12 两个"错了不报错"的小口子

- `recovery/circuit_breaker.go:38-48` ✓：`case "half-open": return true` 对**每一个**调用者放行 → 熔断恢复瞬间惊群（本该只放一个探针）。
- `http/task_manager.go`：函数表里**没有 `RemoveTask`**（`CreateTask/AddTask/SetTask*/GetTask/sendTaskUpdate/Broadcast/Pause/Continue/Cancel`，✓）→ 每个 Task（含 300 条 memory + ctx）永久留在 map 里。
- `messaging/message_publisher.go:19-26` ✓：`SetDispatcher` 无锁写字段，`Publish` 无锁读，而 `app.go:580` ✓ 每个任务都换一次 dispatcher。

> 诚实说明：`go test -race ./internal/messaging/... ./internal/memory/... ./internal/datamanager/...` 现在是**绿的**（2026-10-17 实跑，exit 0）。这不代表 R9/R12/R4 不存在，只代表**现有测试没有并发地打这些路径**。本方案里凡"钉住竞态"的测试都必须写成 `-race` 下会红的形状。

---

## 3. 目标与非目标

**目标**

1. G1 真生效：子 Agent 一步内的多个只读工具确实并行，并有一条**用 Surface 装配**的证据型测试钉住。
2. G2 可安全开启：并发委派不再会串记忆、串摘要、串流式输出、串日志目录。
3. G3：step 关键路径上不再有落盘；同时 `/resume` 依赖的账本**可信度不降**（靠停点 drain 保证，见 §7.3）。
4. goroutine 生命周期收归一处：每条都有名字、都有 recover、都在某次 `Drain` 里被等过。
5. R7 的四处锁内 IO、R11/R12 三个"静默失效"修掉。

**非目标**

- 投机并行（D9）：双模型投票、后台预压缩、委派期预取蓝图/知识。
- 打开写类委派的并发（`delegate_coding` 保持串行，D7 + §1 边界 1）。
- 事件总线换成 ring buffer 或换第三方库。
- 多进程/分布式。
- Blueprint R2–R6 的扫描器并发（那是蓝图自己的 worker pool，落在本方案的 L1 之上，另行设计）。

---

## 4. 架构总览

四层，依赖方向严格向下，每层一个守卫守住：

```
L4 并行面与观测    G1 谓词修复 · 委派并发上限 · 事件 seq/delegation_id · in-flight 限流 · 日志 ctx 化
        │
L3 落盘管道        RolloutWriter 入队 + 六个停点强制 drain（其余 sink 复用同一 supervisor 语义）
        │
L2 委派隔离        SpawnForDelegation（每委派新实例）· callID 直投注入 · ConversationMemory 加锁
        │
L1 结构化并发      internal/supervise（叶子包：具名 goroutine + recover + ctx 归属 + Drain）
```

| 层 | 新增/改动包 | 叶子守卫 |
|----|-------------|---------|
| L1 | `internal/supervise`（新，只依赖标准库） | `scripts/check_supervise_leaf.sh`（新，照 `check_blueprint_leaf.sh` 写） |
| L2 | `internal/agents`、`internal/app`、`internal/memory` | 现有 `check_no_agents_import.sh` 不动 |
| L3 | `internal/memory` | `scripts/check_no_inline_atomic_write.sh` 保持（仍只 `util/atomic.go` 一处） |
| L4 | `internal/agents`、`internal/llm`、`internal/logging`、`internal/tui`、`internal/http` | `scripts/check_no_bare_go.sh`（新，硬门） |

**为什么是这个顺序**：L1 先落地，因为它把所有现存泄漏治理完且**不新增任何并发**（零语义风险，可独立评审）；L3 其次，因为账本可信是 G2 的前提；L2 是"敢并发"的前提；L4 才真正放大并发面。

---

## 5. L1：`internal/supervise`

### 5.1 API（**C1 已落地，以下是实现的原样**）

```go
package supervise // 只 import 标准库，守卫 scripts/check_supervise_leaf.sh

type Group struct { /* ctx, 名字, 父域, live map, subs, quiet chan, failures */ }

// Root 是进程级域（sync.Once）。没有 ctx、或 ctx 上没有域时的落脚点。
func Root() *Group

// New 建一条子域。父域从 ctx 读（读不到挂 Root）。
// **返回两个值**：只攥着 *Group 而不用新 ctx，下游 From(ctx) 仍会拿到父域 = 白建。
func New(ctx context.Context, name string) (*Group, context.Context)

// Go 起一条具名 goroutine：recover panic → slog.Error(域路径 + 名字 + 栈) 并计入 Failures。
// fn 收到的是**域 ctx 本身**，不是派生的 cancel ctx —— 本包不制造取消（§1 边界、坑 17）。
func (g *Group) Go(name string, fn func(ctx context.Context))

// Drain 等本域 + 全部子域的成员退出，最长 timeout；返回还没退出的成员清单。
// 空切片 = 排干；排干的子域会自动从父域摘除（否则父域永远看不见"安静"，
// 因为 release 只在 live 与 subs 同时为空时才唤醒）。
func (g *Group) Drain(timeout time.Duration) []Lease

func (g *Group) Pending() []Lease              // 不等待，只报告
func (g *Group) Failures() map[string]int      // 被 recover 掉的 panic，按名字聚合
func From(ctx context.Context) *Group          // 永不 nil；所有方法对 nil receiver 安全
func WithGroup(ctx context.Context, g *Group) context.Context
func LeaseNames(leaked []Lease) []string             // 把清单压成人能读的一行一条

type Lease struct{ Name, Key, Owner string; StartedAt time.Time; Age time.Duration }
```

与原稿的三处偏离，都是有意的：

1. **删掉了 `Sub`**：`New` 已经从 ctx 解析父域，两个入口只会让"这条域挂在哪"变成第二个需要推断的事实。
2. **`New` 返回 `(g, ctx)` 而不是单个 `*Group`**：域归属靠 ctx，返回单值等于把最容易用错的一步留给调用方自觉。
3. **不用 `sync.WaitGroup` 实现**：`Go` 可能在 `Drain` 进行中发生（工具批就是这个形状），
   而 `WaitGroup` 明确禁止 Add-after-Wait 那种时序。改用锁内计数 + `quiet` 关闭广播，
   等待者在锁内领 channel、释放者在锁内关闭它 —— 既不漏唤醒也不攒陈旧信号。

### 5.2 谁建根域

| 入口 | 域名字 | Drain 时机 |
|------|---------|-----------|
| 任务 ctx（**唯一收口点 `http.NewTaskContext`**，TUI/HTTP 共用） | `task`（挂在 ctx 上） | 任务执行体收尾时（`http.ExecuteTask` 的 defer、TUI 的 `drainTaskDomain`） |
| 跑任务的那条 goroutine 本身 | `http/task/{id}`（**Root 的成员，不进任务域**） | 它自己就是执行体，进域等于收尾时等自己 → 死锁 |
| 进程级后台（MCP readLoop/init 看门狗、sweeper、consolidation、datamanager、共享记忆、`run_bash` 僵尸回收、自动知识整理、TUI 的 fzf/尾读） | 各自一条域或 Root 成员 | `Yagent.Close` 末尾 `Root().Drain(3s)`，等不到就 `slog.Warn` 报名字 |

两点与初稿不同，都记在这儿以免变成"文档说一套代码做一套"：

- **`Close` 的预算是 3s，不是 30s。** 用户按 ctrl+c 之后等 30 秒才算退出，比泄漏更难接受；
  退出路径的纪律是"报出来"，不是"一定等到"。
- **`knowledge_hook` 挂 Root 而不是委派域**（初稿 §5.2 举例说它会在委派结束时被等）。
  挂委派域意味着 `delegateTo` 要等一次最长 60s 的知识提取 LLM 调用，症状是
  "子 Agent 早答完了，界面还转一分钟"。它的 ctx 也保持 `context.Background()` 派生：
  工具 ctx 在委派返回时会被取消，而这份知识写库不该跟着蒸发。


**还没落地的部分（C4）**：委派域（`delegate_<target>#{callID}`）由 `delegateTo` 创建后，
返回时**不** Drain —— 与 `knowledge_hook` 的理由同源：委派返回不该被它起的后台工作的时长拖住。
它留给任务收尾随任务域一起等。

### 5.3 与 `pause` 的关系（不许混）

`internal/pause` 的闸门**不取消 ctx**、只在 step 边界阻塞（AGENTS.md §11 坑 17）。`supervise` 也不取消 ctx —— `Go` 的 fn 拿到的是父 ctx 本身，不是派生的 cancel ctx。两者都只在"边界"起作用，所以并发**不新增可中断点**：并行段里的工具调用被暂停时照样跑完（§1 边界 4）。

### 5.4 硬门：`check_no_bare_go.sh`

- 扫 `internal/**` 与 `main.go`，`_test.go` 豁免；
- 命中 `\bgo\s+func\s*\(` 或 `\bgo\s+[a-zA-Z_][\w.]*\(` 即红，除非该行属于 `internal/supervise/`；
- 输出必须点名文件与行号，**不做 PENDING 名单**（照 `check_no_manual_lookup.sh` 的先例：棘轮名单是第二个会漂走的事实）。

代价：R8 列的 24 个 † 站点必须**一次迁完**，这是 D1 的选择。迁移是机械的，收益是"以后每条 goroutine 都有名字、有人等"。

---

## 6. L2：每次委派新建实例

### 6.1 接缝

`director_surface.go:30-44` ✓ 的表把目标存成 `agent func() builtinAgent`（返回单例字段 `a.RepoAgent`）。改成：

```go
type delegateSpec struct {
    target    string
    noteAgent string          // 便签目录名不变，旧 /resume 才认得（:27-29 的注释承诺过）
    spawn     func(ctx context.Context) builtinAgent   // ← 每委派一个新实例
}
```

`delegateTo`（`:110-145` ✓）里 `agent := spec.spawn(ctx)`，其余流程不动。

### 6.2 `SpawnForDelegation`：哪些字段能拷、哪些必须新

按实测的字段清单（我逐个 struct 看过，`repo.go`/`coding.go`/`chat.go`/`devops.go`/`browser_agent.go`）：

| 处置 | 字段 | 依据 |
|------|------|------|
| **直接拷** | `promptFmt` `env` `params` `maxSteps` `stepRetries` `ecCfg` `guard` `mcpClient` `knowledge` `microAgent` `browser`(控制面) `memStore` `worker` `BaseAgent{LLM, Publisher}` | 都是构造期装配好的不可变接线或本身带锁的 sink；`LLM` 是**引擎指针快照**，正好复刻 AGENTS.md §6.3 承诺的"子 Agent 在下一次委派换新引擎" |
| **必须新建** | `CodingAgent.thinkLink`（`:152` ✓ `thinklink.NewStore(0)`，`:351` ✓ `AddUserInput`，`:394` ✓ 交给 cfg） | 挂在单例上 = 两次 Coding 委派共用一份思维日志。顺带修掉一个既有 bug：见 §6.3 |
| **改成工厂** | `RepoAgent.devops`、`CodingAgent.BrowserAgent` | 现在由 `app.go:494` † `SetDevOpsAgent` / `SetBoardWriter`（`app.go:571` †）注入具体实例；浅拷会让克隆体仍指向共享单例，隔离是假的 |
| **无需动** | Surface | 各家 `Run()` 里已经现建（`coding.go:156` ✓ `a.surfaceFor(ctx, gcm)`、`repo.go:164` ✓、`browser_agent.go:78` ✓）；rollout writer 也由 Director 每委派建（`director.go:136-139` ✓） |

克隆实现就是 `cp := *a` + 上面两行的替换，**成本可忽略**：`surfaceFor`（`deps.go:28` ✓）是 profile→注册表查询 + 切片构造，`depsFor`（`deps.go:17` ✓）只是填结构体。唯一外部读取是 `config.GlobalDeferredTools()`（原子）与一处 `slog.Warn`。

### 6.3 顺带修掉的一处既有串味（有意的行为变更，需评审）

AGENTS.md §6.5 明说 Browser/Repo/DevOps 的 thinklink store 必须"在 `Run()` 内按委派创建——`RebuildPrompt` 会保留全部用户输入，放构造期会让历次委派的任务串进同一次重建"。而 `coding.go:152` ✓ 恰恰是**挂在构造期**的。

改成每委派一份之后：Coding 的第三档重建不再把上一次委派的用户输入算进来。**这会改变 ultimate 压缩的输出内容**，所以必须：
1. 在 `internal/agents` 加一条 e2e，断言两次 Coding 委派的重建原料互不含对方输入；
2. 在实施前确认 † 是否 repo/devops/browser 真的按 AGENTS.md 的说法在 `Run()` 内建（本方案写作时未逐家核实）。

### 6.4 取消单槽（D5，**C4 已落地，实现方式与原稿不同**）

原稿打算"在 `applyEnhancedCommander` 里按 callID 直接注入"。落地时用了更严的形状：**Director 上不再留任何跨调用的交接状态**，改成每次调用一个载体：

```go
// 由 directorToolRunner.Call 在派生 toolCtx 之前挂进 ctx
type delegationOutcome struct { result AgentResult; has bool }
func withDelegationOutcome(ctx) (context.Context, *delegationOutcome)
func recordDelegationOutcome(ctx, result AgentResult)   // delegateTo / 自定义 Agent / Meta 路径调
```

`ad.Call` 返回后由**同一个 runner goroutine** 读它 —— 写与读在同一条线程上，
happens-before 由"调用返回"本身保证，不需要锁，也不可能拿到别人的结果。
ctx 上没有载体时记录是 no-op：记忆注入是锦上添花，不该因为载体缺失让一次成功的委派失败。

一起删掉的：`pendingSubAgentMemory` 字段、`pendingSubAgentMu`、`set/takePendingSubAgentMemory`、
`injectSubAgentMemoryMu`，以及 `compression.NewContextCompressor` 的 `clearPendingSubAgent`
回调参数（槽没了就没有对象可清；留着它就是把一个没人读的旋钮留在签名上）。
顺带修掉一处：委派注册的 Info 日志原本只存在于**已删除的死分支**里，生产一声不响。

还有一个既有缺陷是在这条路上撞出来的：`func() builtinAgent { return a.RepoAgent }` 在指针为 nil 时
返回的是"装着 nil 的 interface"，所以 `delegateTo` 里 `agent == nil` 那条"not wired"守卫**永远不成立**，
未装配的子 Agent 会一路走到 nil 解引用。现在 `delegateTargets` 在具体类型上先判 nil，
由 `TestDelegateTo_UnwiredSubAgentReturnsError` 钉住。

### 6.5 `ConversationMemory` 加锁（D6，**C4 已落地**）

原稿说"加一把锁、方法内用"。落地时发现锁只对走方法的访问有效，而 `Messages` 是**导出字段**：
包外有 31 处直接读写（`datamanager` 甚至 `mem.Messages = append(...)`），锁保护不了它们。
所以把字段改成不导出的 `messages` + `sync.RWMutex`，让**编译器**列出全部需要改的站点：

- 读：`Snapshot()`（拷贝）；`Size()`；`LastMessage() (ChatMessage, bool)`。
  旧版 `GetLastMessage()` 返回 `*ChatMessage` 指向 slice 内部 —— 拿着它既绕过锁又能改写
  别人正在追加的内存，属于"封装漏到底"，已换成一份拷贝。
- 写：`Append(...)`（与 `Add*` 同一条溢出/配对修复逻辑）、`Replace(msgs)`
  （从磁盘/rollout 整体装载，**不**触发 MaxSize —— 截断会改变 `/resume` 恢复出来的历史长度）。
- `GetMessages()` 的注释一直写着 "thread-safe copy"，而它返回的是内部 slice 本身；
  现在名副其实（15 个调用点一个都不用改）。
- JSON 线上形态 `{"messages":[…],"max_size":n}` 由 `MarshalJSON/UnmarshalJSON` 原样保住 ——
  `memory_test.go` 里就有一处整个结构体往返，封装不许改协议。

一处**有意的行为变更**：`injectSubAgentMemory` 以前裸 append 到导出字段，绕过 MaxSize 与配对修复；
现在走 `Append`，所以子 Agent 摘要开始参与溢出截断（以前它会无上限增长）。


### 6.6 Custom Agent 落进 Surface

Meta 运行期注册现在走 `a.Adapters = append(a.Adapters, ad)`（`director_surface.go:303` †）。D4 删掉 `Adapters` 之后，这条路改用**已存在的** `Surface.Add(specs ...*toolspec.Spec)`（`surface.go:354` ✓）。这不是新增 API，是把最后一处旁挂接回注册表。

---

## 7. L3：rollout 入队 + 停点强制 drain

### 7.1 形状（**C2 已落地**，公开方法一个都没改）

`RolloutWriter` 有 20 个公开方法、6 个 Agent 侧写入点、10+ 测试调用点。所以只换内部实现：

```go
type RolloutWriter struct {
    mu      sync.Mutex  // 只保护计数器 / closed / 入队
    enabled bool        // 构造后不变（未启用时 q==nil，全部路径都是 no-op）
    closed  bool        // 只在 mu 下读写（原来被 atomic 与裸 ++ 两套同步管同一个计数器，R9）
    q        chan rolloutJob   // cap 256；rolloutJob{data []byte; reply chan error}
    quit     chan struct{}
    loopDone chan struct{}
    closeOnce sync.Once; closeErr error
    // file / buf / pendingErr 只归写循环那个 goroutine 碰，生产路径一个字节都不读
    file *os.File; buf *bufio.Writer; pendingErr error
}
```

- 每个 `Write*` = 序列化 + 入队，**立即返回**；`Close` 之后入队被 `closed` 挡掉，不 panic 也不 hang。
- 写循环挂在 `supervise.Root()`，名字 `memory/rollout-writer/<文件名>`；刷盘判据 = 攒满 32 条 **或** 20ms **或** 收到 flush 请求 **或** 收到 quit（quit 时先把队列排空）。
- **写循环绝不取 `w.mu`**：生产者在持锁状态下往 `q` 里塞，队满时阻塞等空位；写循环若也要拿这把锁才能刷盘，就是"生产者等消费者排空、消费者等生产者放锁"的死锁，而且只在大输出时出现。这条约束是整个实现里唯一不能妥协的地方。
- 顺序由 channel FIFO + 单消费者天然保证；`NextMessageID`/`NextTurn`/`msgCounter` 统一到 `mu` 下（删掉 `atomic.AddInt32`，R9）。
- `Close()` = 置 `closed` → `close(quit)` → **join** 写循环 → 关 file；`sync.Once` 保证二次 `Close` 是幂等的 join 而不是提前返回。
- 队满**不丢记录**（阻塞生产者）。兜底：写循环因 panic 被 supervise 兜住而提前退出时（`loopDone` 已关而 `quit` 没关），入队会走 `case <-w.loopDone` 分支报 `slog.Error` 并返回 error —— 没有这一条，生产者就永久挂在持锁的 send 上。

### 7.2 排干点：**两个收口点，不是六处仪式调用**

初稿列了六处"必须 drain 的地方"。落地时发现它们可以折叠成两条**结构性保证**，而且比六处更可靠 —— 因为靠"每个调用点都记得"的约定，漏一个就是静默的账本造假：

| 收口点 | 覆盖 |
|--------|------|
| **读回之前**：`(*RolloutWriter).FinalizeCheckpoint(base)` —— 先 `Flush` 再按整份文件算指纹 | §7.3 那个最危险的耦合。`WriteCheckpoint` 用它，`resume_degrade` 夹具也用它；把两步合成一步，就没人能只做一步 |
| **坐标落地之后**：`WriteCheckpoint` 写完 `agent_checkpoint` 再 `Flush` 一次 | 停路径每会话只走一次，不在热路径上；少了这一趟，"停在哪里"这条记录本身可能随崩溃一起消失 |
| `Close()` join | 所有委派（`createRolloutWriter` 的 defer）与进程收尾 |

初稿里另外四点为什么不需要代码：

- **「每条终止路径返回前」**：executor 与 planner 的终止块都是 `写终止事件 → 写检查点`（`executor.go:377-378`、`planner_rollout.go:57-58`），队列 FIFO 意味着检查点那趟 flush 已经把终止事件带走了；正常跑完的 `WriteRunComplete` 由紧随的 `Close` 覆盖。
- **「`ScanResumeCandidates`/`ReadRolloutForReplay` 读之前」**：`/resume` 读的是**上一次进程**留下的文件，那时 writer 早已被 `Close` join。同进程内既要写又要读的路径（e2e/降级夹具）由上面两个收口点覆盖 —— 落地时有 3 个测试因为漏 flush 而红，正是这条规则的实证（见 §11.2 M2）。
- **「暂停时」**：闸门停在 step 边界，而 step 边界上的记录最多滞后 20ms；杀进程丢的是"最后 20ms"，改造前丢的是"最后一步的全部"，两者都不是可恢复的保证。要更强的保证就得让 /pause 拿到 writer 句柄，而它手上只有任务 ctx 上没有 writer —— 留作后续，不在这里假装有。
- **「每次 `WriteCompacted` 之后」**：`compacted` 只是又一条记录，指纹规则在读侧统一处理前缀替换；它不需要独立 flush，需要 flush 的是**读回**那一刻（已被收口点 1 覆盖）。

`Flush(ctx)` 尊重 ctx：取消时返回 `ctx.Err()` 但**不丢队列**（`TestFlushHonoursCancelledContext` 钉的就是这条 —— 取消一次 Flush 不该带走记录）。


### 7.3 为什么 drain 必须在指纹之前（风险正面说清）

AGENTS.md §11 坑 19：`agent_checkpoint.digest` 是**读侧**用"重建整份文件"的规则算出来的 sha256，比较发生在 `ReadRolloutForReplay` 重算之后。

入队之后，"此刻内存里有哪些记录"与"磁盘上有哪些记录"之间出现了一个窗口。如果检查点写在 drain 之前：

1. 停点时磁盘少几条记录；
2. 恢复时读侧按磁盘算指纹，与当时写下的 digest 不符；
3. `assessReplay` 判"指纹不符" → **整次重放降级成交接报告**；
4. 症状恰好落在它最该起作用的地方——一个触发过压缩的长任务。

所以 `TestCheckpointDigestSeesQueuedRecords` 是本方案的承重测试（§11 M2）：造一条"队列里还有未 flush 记录"的夹具，断言写出的 checkpoint 之后立刻 `ReadRolloutForReplay` 必须**匹配**。

---

## 8. L4：并行面与观测

### 8.1 修谓词并删掉第二条路径（D4，**C3 已落地**）

落地时比初稿多做了一步：初稿打算让谓词"改成复用 `resolveAdapter`"，但那仍是**两份同形判据**
（Director 一份、executor 一份），而 R1 的成因恰恰是两份里的一份漂走。现在是**一个函数**：

```go
// executor.go —— 两个循环共用这一份；判据只有一处实现
func toolCallIsParallelizable(surface *tools.Surface, name string) bool {
    if isInteractiveUserTool(name) || isSerialTool(name) { return false }
    if surface == nil { return false }
    ad, state := surface.Lookup(name)
    if state == tools.StateUnknown || ad == nil { return false }
    return ad.IsReadOnly()          // 由 Effect 的 !Mutates && !Serial 派生
}
```

`RunAgentLoop` 的闭包与 `DirectorAgent.isToolParallelizable` 都调它（后者原本多一条
`state == StateReady`，比 executor 严；统一成"冷工具也算可并行"—— 冷工具在这一批里只会收到
teach 回执、不执行副作用，取回后下一步才真正并发，所以放宽是安全的）。

删掉的东西：

- `ExecutorConfig.Adapters` 字段；`resolveAdapter` 的线性扫描分支；`defsForStep` 的 `frozenDefs`；
  `executor.go:253-274` 的运行期 `read_note`/`read_artifact` 注入块（三个工具的正经来源是
  `specs.go:118` 的 `EffectContext` 注册），连带两个因此变成死代码的
  `newReadNoteAdapter` / `newReadArtifactAdapter`（−2.7KB）。
- `DirectorAgent.Adapters` 字段与三处写入；`directorToolRunner.Specs()` 的快照分支；
  `resolveTool` 的回退分支。`registerCustomAgent` 现在只走 `Surface.Add`
  —— 顺带修掉一件小事：生产路径（surface 非 nil）以前**不打任何注册日志**，
  Info 那行只存在于已删除的死分支里。
- `executor.go` 不再 import `blackboard`。

测试侧：夹具改走新增的 `tools.NewSurfaceFromAdapters(display, adapters...)`。
它存在的理由就写在函数注释里——**不让测试自带第二个载体**；31 处 `Adapters:` 字面量与
`cfg.Adapters =` 赋值全部机械迁移。另外新增 `parallel_admission_test.go`：直接拿
`ProfileRepo`/`ProfileCoding`/`ProfileChat` 真实画像装配的工具面问这个判据，
所以画像或能力标签漂走时会红，而不是只有"并行没开"时会红。
`TestAgentSurfacesGolden` 零漂移（生产工具面一个字没变）—— 这条改动删的是**没人读的第二份真相**。


### 8.2 委派并发上限

Director 侧准入已经正确（`:987-993` ✓，`delegate_repo`/`delegate_chat` 是只读 → 进并行段）。要补的只有**上限与真值**：

- `max_parallel_delegations` 信号量（默认 2，0=关）包在 `delegateTo` 外面，走同一条 `toolbatch.Run` 的语义：**已取消的元素照样要产出 tool 结果**（`toolbatch/run.go:66-78` †），避免"缺一条 tool message"导致 provider 400（AGENTS.md §11 坑 5 的配对不变量）。
- 便签命名：**初稿这条是错的，落地时证伪**。`GenerateNoteFilename()`（`blackboard/board.go:121` ✓）
  用的是"UTC 秒级时间戳 + 2 字节随机后缀"，两次并发同名委派本来就不可能撞名。
  所以这里**不加**委派坐标（改了反而会让 `/resume` 认不出旧便签），只补一条契约测试：
  `TestConcurrentWriteNotesNeverCollide` 并发写 24 张，断言 24 个不同 noteRef ——
  将来谁把 stem 改成"按任务文本命名"，它会先红，而不是等两次委派的结果互相吃掉才发现。
- `repoCtx.Set` 的 last-writer-wins（`director.go:817-824` †）：两次并发 repo 探索会互相顶掉。处置 = 保留共享 store（它是给后续轮次的缓存，不是交付物），但把冲突记一条 `slog.Debug`，并在便签里各留自己那份。**不改成交付路径**，交付走 handoff。

### 8.3 事件与展示真值（**C5 已落地，两处与原稿不同**）

- 落地方式与原稿不同：**没有动 `protocol/agent-events.yaml`，也没有重跑生成器**。
  `messaging.Event` 是手写文件（`internal/messaging/message_event.go`，非 DO NOT EDIT），
  所以 `Seq int64` + `DelegationID string` 直接加在那里就够了；Web 侧信封
  `protocol.AgentEventEnvelope` 才是生成物，于是那两个坐标放进 `realtime` 消息的 `Data` map
  （`gin.H` 是手建的）—— 原稿"改 yaml 再手工粘回两份 Go 产物"那条流程整个避开了。
- 坐标是**绑定一次**而不是逐点传：`RunAgentLoop` 入口
  `cfg.Publisher = pub.ForDelegation(memory.DelegationIDFrom(ctx))`。
  `ForDelegation` 返回的视图与母体共享同一个 `publisherCore`，所以任务换 dispatcher 时
  已绑定的视图跟着走（`TestForDelegationSharesDispatcherWithParent` 钉的就是这件事 —— 若把它
  写成拷走 dispatcher 指针，绑定后的事件会静默发往旧 dispatcher）。
  绑定不进取 `EventBus` 接口，用能力断言：没绑定的后果只是少一个坐标，不该逼测试 mock 造假方法。

### 8.4 provider 侧限流

并行一开，同一引擎指针会被并发 `GenerateContent`（今天已经可能：`deepthinking`/`design`/`micro_agent` 都是只读、可并发，† 由 `toolbatch` 起 goroutine）。加一个**全局进程级**信号量：

```go
llm.WithInFlightLimit(n int)   // [llm] max_in_flight_requests，默认 8，0=不限
```

放在两个引擎的 `GenerateContent` 入口（`engine_openai.go` / `engine_anthropic.go` 的公共前置），理由：429 是 provider 全局属性，不是每引擎属性。transport 今天只有 `MaxIdleConns:10 / MaxIdleConnsPerHost:5` †（那是连接复用，不是并发闸门）。

### 8.6 C5 里刻意没做的两件，以及为什么（原 §8.5 日志 ctx 化并入 ②）

**① `ai_chunk` 合批（原稿 §8.3 的第 3 条）。** 队列容量收口时发现了真正的病根：
文档写的"每消费者 1000"被 6 个调用点各手写 `NewMessageDispatcher(100)` 压成了十分之一，
所以"默认值"从来没生效过（AGENTS.md §11 坑 35）。修完之后，1000 格的队列 + 每秒一条限频 warn
+ 收尾一次 `WarnDropped` 已经让丢弃既少又可见。
合批是在此之上的**吞吐优化**，代价是给流式热路径加一条 goroutine + 一个 flush 协议
（错一位的表现是"流被截在半句"或"最后一个 token 永远不到"）——
在没有实测证据说明 1000 格仍会丢之前，它是用真风险换假收益。
留给有人拿 `Dropped()` 说出"还在丢"之后的那一天。

**② 日志 `currentTaskID` 的 ctx 化（§8.5 原稿）。** 原稿写得太轻。真实形状是：
`llm.go` 有 6 处、`tool_logger.go` 有 3 处在**没有 ctx 参数**的函数里读这个全局
（`LogLLMContent(title, content)`、`ensureLLMLogFile()`），而"改成 per-task handle"
意味着把 `llmLogger/llmLogFile` 两个全局变成带锁的 map —— 那是 llm 层的一次小型重构，
不顺手塞进 C5。
现状的实际危害也要说准：它只在**两个任务同时跑**时才错（多 tab / 并发 HTTP 任务），
后果是"这条记录进了别人的日志目录"，属可调试性损伤而非用户可见的正确性问题；
而 C5 已经消除了同一种并发下**用户可见**的那个症状（流式文字互相粘连）。
所以它排在 C6 之前独立做，且要连着 `LogDelegateCall` 每次 `Sync` 的问题一起处理。

---

## 9. 正确性修复清单（都随 L1–L4 落地，不单独立项）

| 问题 | 位置 | 修法 | 钉住它的测试 |
|------|------|------|-------------|
| 锁内文件写 + 索引 parse | `datamanager/data_manager.go:122-129` ✓ | 序列化在锁内、IO 与 index 更新在锁外；或整条 flush 走 supervisor 的 worker | `TestFlushDoesNotHoldWriterLockAcrossIO`（锁外起一个会阻塞的假写，断言第二个任务不被顶住） |
| `FlushAll` 持全局锁跨所有任务 IO | `:252-268` † | 快照 writer 状态 → 释放 `dm.mu` → 逐任务 drain | `TestFlushAllScalesLinearlyNotGloballySerialized` |
| 检查点锁内跑 git 子进程 | `git_checkpoint.go:137-139` ✓ | 子进程在锁外跑，只有状态落库在锁内 | `TestGitCheckpointSubprocessNotUnderLock` |
| `tm.lock` 跨 `Socket.Write` | `task_manager.go:88-96,124-126` ✓ | 锁内拷贝要发的 `TaskUpdate`，锁外广播 | `TestSlowSocketDoesNotBlockTaskManager`（假 session 阻塞 200ms，断言另一任务的状态更新不排队） |
| Task 永不移除 | `task_manager.go` 无 `RemoveTask` ✓ | 加 `RemoveTask(taskID)`，由 `ExecuteTask` 的 defer 在 TTL 之后调；或 `time.Time` 过期清扫 | `TestCompletedTaskIsEvicted` |
| MCP 请求无 deadline | `mcp/client.go:55-66` ✓ | 把 `timeout` 放进结构体，`CallTool` 用 `context.WithTimeout(ctx, c.requestTimeout)` | `TestCallToolHonoursRequestTimeout`（假服务器永不回包） |
| `IsAlive` 无锁读 ProcessState | `:214` † | 与 `cmd.Wait()` 同锁，或改成 `alive atomic.Bool` | `TestIsAliveRaceFree`（-race） |
| breaker half-open 惊群 | `circuit_breaker.go:38-48` ✓ | half-open 只放**一个**探针（CAS 布尔），其余仍拒绝 | `TestHalfOpenAdmitsExactlyOneProbe` |
| publisher 无锁换 dispatcher | `message_publisher.go:19-26` ✓ | `dispatcher atomic.Pointer[MessageDispatcher]` | `TestPublisherSwapUnderLoad`（-race 下的 e2e） |
| rollout 计数两套同步 | `rollout_writer.go:256,366` ✓ | 统一到 `mu`（§7.1） | `TestRolloutCountersUnderConcurrency`（-race） |
| consolidation close 后 Submit panic | `consolidation_worker.go:91-99` † | `Submit` 走 `supervise.From(ctx)`，closed 后返回 error 而非 panic；drop 策略改成有界阻塞 | `TestSubmitAfterStopReturnsError` |
| cancel 被覆盖不释放 | `tui_update_tasks.go:1026-1028` † | 赋新值前调旧 cancel；根域 Drain 兜底 | `TestSupersededTaskIsCancelled` |
| `ws.mu` 与 `Memory` 裸读 | `tui_tasks.go:194-205` † + `memory.go:62-65` ✓ | D6 的锁 + `Snapshot()` | `TestPeriodicSaveReadsSnapshot`（-race） |
| 12 站点无 recover | R8 | 全部走 `Group.Go`（§5.1 内含 recover） | `TestSuperviseRecoversPanickedGoroutine` |

---

## 10. 配置面（只有两个新键）

```toml
[agent]
max_parallel_readonly_tools = 4   # 已存在；R1 修完才真的到达六个子 Agent
max_parallel_delegations     = 2  # 新。0 = 串行（等价于今天的行为）

[llm]
max_in_flight_requests = 8        # 新。0 = 不限
```

- 每个新键都必须有一个**"改这个值就失败"**的测试（AGENTS.md §11 坑 25 的承诺）：`max_parallel_delegations=1` 时两条并发委派必须串行到可测出墙钟差；`max_in_flight_requests=1` 时两个并发 LLM 调用不能同时在飞。
- kill-switch：环境变量 `YAGENT_CONCURRENCY=0` → `config.ConcurrencyEnabled()` 是**唯一判据**，被 G1（工具批）、G2（委派并发）、L4（合批）三处共用。照 `blackboard.Enabled()` / `config.ResumeEnabled()` 的先例：关掉是连写都不写，不留半套开关。
- rollout 队列的深度/批量/flush 间隔**不设键**（D8）。
- `[resume]` / `[blackboard]` 的键语义不变；注意 rollout 转异步后，`[resume]` 依赖的"检查点前 drain"是硬保证而非配置选项。

---

## 11. 验收与闸门

### 11.1 新增测试（承重的那几条先列）

| 测试 | 断言的是 | 为什么它能失败 |
|------|---------|--------------|
| `TestSubAgentToolBatchActuallyOverlaps` | 用 **Surface**（不是 Adapters）装配一个子 Agent，两个各 sleep 200ms 的只读工具，整步墙钟 < 350ms | 直接测 R1。谓词退回扫 Adapters → 恒串行 → 400ms → 红 |
| `TestCheckpointDigestSeesQueuedRecords` | 队列里还有未 flush 记录时写的检查点，`ReadRolloutForReplay` 必须**匹配指纹** | §7.3。去掉 drain → 降级成交接报告 → 红 |
| `TestConcurrentCodingDelegatesKeepOwnThinkLink` | 两次并发 Coding 委派的重建原料互不含对方输入 | §6.3。`SpawnForDelegation` 若返回单例 → 红 |
| `TestParallelDelegateInjectsUnderOwnCallID` | 两个并发 `delegate_repo` 的摘要各挂在自己的 `callID` 下 | D5。恢复单槽 → 张冠李戴 → 红 |
| `TestMemorySnapshotSurvivesConcurrentAppend`（-race） | 并行注入 + planner 追加不撞 | D6。不加锁 → -race 红 |
| `TestRolloutQueuePreservesOrderUnderLoad` | 灌 5000 条后磁盘 JSONL 顺序与入队顺序逐条一致 | 队列 + 多生产者。改成多 writer goroutine → 红 |
| `TestNoBareGoFunc`（守卫） | `internal/**` 无裸 `go` | 新加一条裸 spawn → 红 |
| `TestSuperviseReportsEveryNamedGoroutine` | `Drain` 超时能列出**哪条**卡住 | 去掉名字表 → 只能报"有泄漏"报不出是谁 |
| `TestEventsCarrySeqAndDelegationID` + TUI 双槽测试 | 同名 Agent 并发时流式输出不混 | R5。按名字的槽 → 红 |
| `TestNoteStemsAreUniquePerDelegation` | 并发同名委派的便签不互相覆盖 | §8.2。共享 stem → 一份消失 → 红 |
| 每条新配置键各一条"改值即失败"测试 | §10 | 键失效（本仓的历史病） |

现有闸门不变：`bash scripts/check_all.sh`、`RACE=true bash scripts/check_all.sh`、`TestAgentSurfacesGolden`（工具面若因 `Adapters` 删除而漂移，必须以显式改写 golden 的方式被评审，AGENTS.md §9.3 第 4 步）。

### 11.2 变异验证（声称"能失败"就必须真让它红一次）

M1 谓词改回恒 false（R1 原样） → 红 `TestRunAgentLoop_ReadOnlyBatch_ParallelEvidence` + `TestParallelAdmission_*`（**C3 实测**）
M2 删掉检查点前的 `Flush` → 红 `TestCheckpointDigestSeesQueuedRecords`
M3 删掉 `ConversationMemory` 的锁 → `-race` 红（待 C4）
M4 `SpawnForDelegation` 改成 `return a`（返回单例） → 红 `TestConcurrentCodingDelegatesKeepOwnThinkLink`（待 C4）
M5 把 `applyEnhancedCommander` 的注入改回写单槽 → 红 `TestParallelDelegateInjectsUnderOwnCallID`（待 C4）
M6 在任一站点把 `Group.Go` 换回裸 `go func` → 红 `check_no_bare_go.sh`
M7 `writeEnvelope` 绕过队列直写文件 → 红 `TestRolloutQueuePreservesOrderUnderLoad`（顺序/双写）

### 11.2.1 已跑过的变异与结果（不是计划，是实测）

| 变异 | 拿掉的东西 | 结果 |
|------|-----------|------|
| M6 | 在 `internal/http` 塞一个裸 `go func` | 守卫 exit 1 并点名 `internal/http/zz_mutant.go:6`；删掉后 exit 0 ✅ |
| M2a | `(*RolloutWriter).FinalizeCheckpoint` 里读回前的 `Flush` | **红**：`stored digest 05f39c7c… != rebuilt 00713603…`、`stored MessageCount = 191, rebuilt 200` ✅ |
| M2b | `WriteCheckpoint` 写完检查点后的那次 `Flush` | **红**：`文件里没有检查点 —— WriteCheckpoint 没落盘` ✅（这条是测试逼出来的第二个收口点，初稿只写了"读回前排干"） |
| — | `toolbatch` 用域 ctx 取代传入 ctx | **红**：`exec 收到的 ctx 标记 = ""`，即取消/deadline 会丢 ✅ |
| — | `supervise` 的 6 条变异（不等子域 / 不 rethrow / 不唤醒 / 不摘子域 / 不记 panic / 不排空队列） | 5 条红；第 1 条**当初没红**，因为测试只断言"报告"没断言"等待"，补延迟断言后才红 ✅ |
| — | `stopTaskBackground` 里 drain 与 close 顺序颠倒 | **红**：实测收尾耗时 5.001s（预测的就是这个数） ✅ |
| M1 | `toolCallIsParallelizable` 改回恒 false（R1 的形状） | **红 6 处**：`TestRunAgentLoop_ReadOnlyBatch_ParallelEvidence` 报 `expected max concurrency >= 2, got 1`，`TestParallelAdmission_*` 点名"生产 Repo 画像下 read_file 应可并行" ✅ |
| M1' | 反向：判据改回恒 true | **红**：`create_file / search_replace_in_file / delete_file / rename_file / run_bash / delegate_devops` 全被点名"不该进并行批次" ✅ —— 一条断言两个方向都能红，才说明它钉的是判据本身 |
| — | `TestRunAgentLoop_ReadOnlyBatch_SerialKillSwitch`（对照臂，不是变异） | 断 `max_parallel_readonly_tools=1` 时峰值恰为 1 且整步 ≥60ms —— 没有这条，"峰值 ≥2"可能只是计量器自己的毛病 |
| M4 | `CodingAgent.SpawnForDelegation` 保留新实例但 `cp.thinkLink = a.thinkLink`（共享可变状态） | **红**：A 的重建原料里原样印着"任务 B：跑测试"，且"单例的 store 被写了 2 条" ✅ —— 失败输出本身就是那个跨委派串味的样本 |
| M5 | 恢复单槽交接 | **不可编译**：`pendingSubAgentMemory` 字段已经不存在。这是刻意的 —— 结构性删除比"写一条会红的测试"更强，第二份真相没有落脚处 ✅ |
| M6' | `delegateTargets` 去掉具体类型的 nil 判断（回到 `return a.RepoAgent`） | **红** `TestDelegateTo_UnwiredSubAgentReturnsError`：未装配的五个目标全部不再返回 "not wired" 而是走到 nil 解引用 ✅ |
| — | `ConversationMemory` 的并发读写（16 写者 + 4 读者，`-race`） | `TestConversationMemoryConcurrentAppendKeepsEveryMessage`：960 条一条不少；字段不导出后编译器把 31 处包外直读全部点名 ✅ |
| M-C5a | `streamSlotKey` 忽略 `DelegationID`（退回只按 Agent 名分槽） | **红**：`第一条流的内容 = ""`、`第二条 = "AAABBBAAABBB"` —— 复现的正是当年那件事 ✅ |
| M-C5b | `ForDelegation` 绑定了但没换出去（`_ = scoped.ForDelegation(id)`） | **红**：`7/7 条事件没有委派坐标（model_info/llm_call_start/ai_stream_start/ai_stream_end/llm_call_end/ai_response/tool_call_start）` ✅ —— 一条测试同时看住"漏一个点"和"整条链都没绑" |
| M-C5c | `ForDelegation` 拷走 dispatcher 而不是共享 core | **红** `TestForDelegationSharesDispatcherWithParent`：任务换 dispatcher 后，已绑定的视图仍发往旧的 ⇒ 事件静默消失 ✅ |

每条变异都在工作树的临时副本上做、跑完立刻用 `/tmp` 里的备份复原并 `diff` 确认一致（本仓 memory 记录的规矩：绝不对未 commit 的文件跑 `git checkout` 还原）。

### 11.3 量化验收（不是"感觉快了"）

- **G1**：一条 4 个只读工具、每个 200ms 的合成 step，墙钟从 ~800ms 降到 ~250ms（串行 kill-switch 下仍 ~800ms，用作对照臂）。
- **G2**：两条并发 `delegate_repo`（各 mock 3 次 LLM 调用）的墙钟 ≤ 1.15 × 单条。
- **G3（C2 实测，Apple M1 Pro，`-benchtime=20000x -count=3`，数字稳定）**：一条 160 字节的 `response_item`，
  热路径成本 **3.0µs（入队）vs 7.1µs（入队+每条刷盘 ≈ 改造前行为）** —— 省 2.4×。
  一个 step 约 3-6 条，所以省下的是 **十几到二十几微秒**：相对一次几百毫秒的 LLM 调用**可以忽略**。
  诚实的结论是：C2 的价值不在墙钟，而在①退出/收尾时有了一次 join（原来 `Close` 不 join，
  "Close 返回了但最后几条还在队列里"是可能状态），②系统调用条数按批折叠，
  ③为 C6 放量后"多路并发一起写"预留了单写者顺序。**不要把这条改动当成性能优化来验收。**
- 全仓 `go test ./...` 墙钟不涨（本仓对耗时基线很敏感：AGENTS.md §8.3 记录 26 包约 12s）。

---

## 12. 实施顺序（一条分支的提交切分，全部落地才合）

| # | 提交 | 内容 | 独立可评审性 |
|---|------|------|-------------|
| C1 | `feat(supervise)` | **已落地**（2026-10-17，分支 `feat-structured-concurrency`）：新叶子包 `internal/supervise` + 11 条自带变异的单测 + 两把守卫（`check_supervise_leaf.sh`、`check_no_bare_go.sh`，已接入 `check_all.sh` 成为第九把）+ **24 处裸 go 全部迁完**（零残留，守卫实测绿）+ 5 处原本无人等待的收尾接上 join（messaging `Shutdown`、mcp 两条 `Shutdown`、browserctl `Close`、datamanager `Stop`、`Yagent.Close`）+ `NewTaskContext` 同点挂并发域（与闸门同一个收口点，理由与 §11 坑 17 那条一致）+ consolidation 的 `Stop` 幂等化（原来二次 close 会 panic） | 零模型语义风险，泄漏治理到手 |
| C2 | `fix(memory)` | **已落地**（2026-10-17）：rollout 入队 + 写循环 + `Flush` + 计数器统一到一把锁（R9）+ `Close` 加锁并 join（R10）；排干点折叠成**两个收口点**（§7.2 记了为什么不是六处，以及"暂停时"这一条为什么暂时做不到）；新测试 8 条 + 基准 2 条；`internal/memory` 加进 `RACE_PKGS` | 有 §7.3 风险，靠 M2 兜；**M2 实测有效**（见 §11.2） |
| C3 | `fix(agents)` | **已落地**（2026-10-17）：判据收成 `toolCallIsParallelizable` 一个函数（两个循环共用）+ 删 `ExecutorConfig.Adapters`/`DirectorAgent.Adapters` 两条第二真相 + 31 处夹具迁 `NewSurfaceFromAdapters` + 真实画像的准入测试 + 串行对照臂；`TestAgentSurfacesGolden` 零漂移 | **G1 在此真正生效**（M1 变异实测红） |
| C4 | `refactor(agents)` | **已落地**（2026-10-17）：`ConversationMemory.messages` 转为不导出 + `RWMutex`（编译器列出 31 处包外直读，全部改走 `Snapshot/Append/Replace/LastMessage`；JSON 契约用 `MarshalJSON` 保住）+ 单槽交接换成**每次调用的 ctx 载体**（连带删掉 `clearPendingSubAgent` 这个没人读的旋钮）+ `SpawnForDelegation` 五家 + 嵌套 `devopsFor`/`browserFor` + 顺手修掉"类型化 nil 装进 interface 让 not-wired 守卫永不成立"的既有缺陷 | G2 的安全前提就位 |
| C5 | `feat(messaging,tui,http)` | **部分落地**（2026-10-17）：信封 `Seq`+`DelegationID`（避开生成物）+ 执行侧一次绑定 + TUI 三张槽表按 `agent#delegation` 分（显示名仍用裸名）+ 队列容量收成一个 `NewDefaultMessageDispatcher()`（原先 6 处手写 100，把"默认 1000"压成 1/10）+ `Dropped()`/`WarnDropped()` + publisher dispatcher 换成原子（R12 一项提前）+ 便签 stem 证伪原稿并钉成契约。<br>**未做**：`ai_chunk` 合批、日志 `currentTaskID` 的 ctx 化（见 §8.6） | 观测真值到位；两件未做的都有独立理由 |
| C6 | `feat(concurrency)` | 打开委派并发上限 + `max_in_flight_requests` + §9 的锁内 IO 与静默失效清单 | 唯一"提速"的提交 |

C1/C2/C3 之间没有依赖倒置；C6 必须在 C1–C5 之后，因为它才是"放量"。

**C1 的两处实现偏离**（已回写进 §5.1/§5.2，避免文档与代码各说一套）：`New` 返回 `(g, ctx)` 且没有 `Sub`；
`Close` 的 drain 预算 3s 而非 30s；`knowledge_hook` 与 `run_bash` 僵尸回收挂 Root 而非任务/委派域。
另有两处**刻意保留本地 WaitGroup**：`toolbatch.Run`（提交前必须等"这一批"的每个槽位算完，
Drain 的超时语义会把零值结果提交进对话）与 `tui_resume_stop`（同理，等的是这一批文件）。

---

## 13. 风险与回退

| 风险 | 表现 | 补偿控制 |
|------|------|---------|
| 429 / provider 限流 | 并行一开，在飞请求 ×4 | `max_in_flight_requests`（§8.4）；`YAGENT_CONCURRENCY=0` 一键回串行 |
| 检查点指纹与队列窗口 | B 层静默降级成交接报告（最难查的一类） | 六 drain 点 + M2 变异 + `TestCheckpointDigestSeesQueuedRecords` |
| Coding thinklink 下沉改变 ultimate 压缩内容 | 重建出的上下文比今天少 | 有意的行为变更（§6.3），写进 AGENTS.md 并在 e2e 里钉形状 |
| 删 `Adapters` 波及 ~20 个测试 | 大 diff，容易漏 | `TestAgentSurfacesGolden` + `check_no_manual_lookup.sh`；`check_test_hygiene.sh` 会抓住"改完没有失败能力"的夹具 |
| 事件字段新增撞上生成器漂移 | 手改 Go 产物容易与 yaml 不一致 | 按 §8.3 的既有流程（只覆盖 ts/schema 三个产物，Go 手工粘贴）；`docs/pause-resume-design.md` §15.3 第 7 条仍是未完成项 |
| 并发后 TUI 事件量翻倍丢字 | 界面少字，看起来像渲染 bug | 队列 100→1000 + chunk 合批（§8.3）；`dropped` 计数进 ⧉ 面板，让丢字**可见** |
| 同名 Agent 并发暴露今天不存在的时序 | 长任务里的新竞态 | `RACE=true` 进 CI 前置的本地闸门；e2e 用真实便签盘/rollout（AGENTS.md §8.3 的"只 mock 模型"原则） |

回退：`YAGENT_CONCURRENCY=0` 回到串行（G1/G2/合批三处共用一个判据）；rollout 队列的退路是**把 `Flush` 调成每次入队后立即 drain**（等价于今天的同步写），公开 API 不变所以不用回滚代码。

---

## 14. 明确不做（以及为什么）

1. **不把写类委派并行化。** `delegate_coding` 的 `Mutates` 是设计上的屏障，`prompt.md:84` 那句"MUST be dispatched ONE at a time"是对的；本方案只保证**这句话不再被 executor 的谓词意外扩大到所有工具**。
2. **不做参数级独立性分析**（D7）。
3. **不上 `errgroup`**（D1）。
4. **不做投机并行/双模型投票/后台预压缩**（D9）——它们各自需要收益论证，而 `emergency.go:135` † 那个同步 90s 的 LLM 调用会在 R2 清单里单独备案，等本方案落地后再评估。
5. **不把 dispatcher 换成通用 pub-sub 库**：今天"每 consumer 一条 goroutine + drop-on-full"的取舍是对的，缺的只是序号与容量。

---

## 附录 A：证据状态

- 我逐条打开并核对过的行号（✓）：`executor.go:150-165,257-292,419-429,513-517,604-608,775-869`、`coding.go:152,351,376,394`、`repo.go:164`、`chat.go:71`、`devops.go:80`、`browser_agent.go:78,114`、`director.go:77,487-516,768-796,975-993`、`director_surface.go:23-44,51-81,110-145`、`deps.go:17-42`、`memory/memory.go:55-70`、`memory/rollout_writer.go:30-50,139-320,356-406`（方法表）、`messaging/message_event.go:60-92`、`messaging/message_publisher.go:1-40`、`http/task_manager.go:88-137` + 方法表（确认无 `RemoveTask`）、`mcp/client.go:50-70`、`recovery/circuit_breaker.go:38-48`、`datamanager/data_manager.go:118-132`、`agents/git_checkpoint.go:134-142`、`logging/logger.go:28-34,205-212`、`tools/surface.go`（方法表，确认 `Add`/`Lookup`/`Materialize` 已存在）、`agents/toolbatch/*`（签名）、`app/app.go:580,902`、`browserctl/manager.go:124-145`、`knowledge_hook.go:34-42`。
- 只来自探查、**实施时必须复核**（†）：goroutine 站点总数（24）与逐站点是否有 recover；`main.go:317`/`tui_tasks.go:169`/`task_executor.go:33` 的 BufferSize=100；`message_dispatcher.go:102-118` 的 drop 代码；`aiStreamActiveEntries` 按名字的槽；`rollout_checkpoint.go:104-105,167` 的整文件重读；`consolidation_worker.go` 的 drop/panic；`tui_update_tasks.go:1026-1028` 的 cancel 覆盖；repo/devops/browser 的 thinklink store 是否真在 `Run()` 内创建。
- `go test -race ./internal/messaging/... ./internal/memory/... ./internal/datamanager/...` = **exit 0**（2026-10-17 实跑）。因此本方案里所有"钉竞态"的测试都必须是新写的、能在 `-race` 下因为**新的并发夹具**而红的测试，不能指望现有用例。
