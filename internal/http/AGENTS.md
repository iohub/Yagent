# internal/http — REST / WebSocket 与任务生命周期

> 范围：本目录内不可派生的约束。全局见仓库根 `AGENTS.md` §6.9。

## 边界

gin REST + melody WebSocket + `TaskManager` 的任务生命周期。不负责暂停语义本身（在
`internal/pause`），不负责 Agent 装配（在 `internal/app`）。

## 硬约束

**1. 每个建任务 ctx 的地方都必须走 `NewTaskContext`**（`task_gate.go:8-30`）。闸门与并发域
**只挂在 ctx 上、不在结构体里另存一份**：`Task.Gate()` 是从 ctx 读回来的，`supervise.From(ctx)` 同理。
两处各存一份的话，任何"换了 ctx 忘了换闸门"的地方都会让 `/pause` 停在一个没人在等的闸门上——
那条路径没有报错，只是什么都不发生。漏用 `NewTaskContext` 不会静默失败（`/pause` 会拒绝并说明），
但表现是"这一条路径上的任务从来停不了"，而它正是最常被新增的那条路径。

**2. 续跑分支换 ctx 就是换闸门**（`server.go:244-247`）：旧闸门属于已经结束的那一轮，
留着只会停错对象。

**3. 锁不许跨过 IO。** 6 个状态点（`SetTaskResult/Error/Progress` + pause/continue/cancel）在
`tm.lock` 内**只编字节**，`taskAction.deliver` 在 Unlock 之后做 `Socket.Write`/`Broadcast`
（`task_manager.go:95-140,168-190,247-282,291-322,326-344`）。理由是一个卡住的浏览器标签能顶住
全部状态更新与读锁。要测这条不变量得能把 IO 卡住，所以有 `writeSession` / `melodyBroadcast` /
`flushHook` 三个 seam——**探针必须用不需要投递的路径**，否则带 socket 的 setter 会停在它自己的
慢客户端上，那测的不是"别人的锁压着"（`task_manager_concurrency_test.go:17-42`）。

**4. `paused` 是第五档状态**，因为闸门不产生 `task_complete`（`types.go:70-79`）。
`ContinueTask` 的判据是"状态仍 paused **且**闸门仍按着"，而不是"闸门按过"（`task_manager.go:286-291,305-310`）——
暂停后被取消的必须显式拒绝。`Blocked` 在按下瞬间必为 false，不许骗人（`:270-279`）。

**5. 任务表终态过期只在 `CreateTask` 时清扫**，不起定时 goroutine；`running/paused` 一律不动
（`task_manager.go:64-80`）。`taskRetention` 是 seam（`:31-33`）。

**6. 每任务一个调度器且走 `NewDefaultMessageDispatcher()`**（`task_executor.go:35`、`websocket.go:182`）——
容量只有一处定义（根 AGENTS.md §11 坑 35）。收尾必须 `Shutdown + WarnDropped + Drain 任务域`
（`task_executor.go:43-50,103-121`）：不等的话周期保存会带着已取消的 taskID 继续写文件。

**7. `seq` / `delegation_id` 放进 realtime envelope 的 `Data` map，不改
`protocol.AgentEventEnvelope`**（`task_executor.go:72-77`）——那是生成物，且里面还有生成器不产出的
手写类型（见 `internal/AGENTS.md` 的 protocol 一节）。WS 向**所有**连接广播，没有按会话过滤。

## 入口

`task_gate.go`（最短的契约文件）、`task_manager.go`、`task_executor.go`。

## 闸门

`task_pause_test.go:37 TestTaskContextCarriesSuperviseDomain`（两条任务共用一条域即红）、
`:69 TestTaskContextCarriesGate`（改成结构体另存一份即红）、`:99`、`:137 TestPauseContinueRoundTrip`、
`:182 TestContinueRefusesCancelledWhilePaused`；
`task_manager_concurrency_test.go:46 TestSlowSocketDoesNotBlockTaskManager`、
`:93 TestPauseBroadcastDoesNotHoldTaskManagerLock`、`:136`、`:170`。

**并发域的第二条纪律**：跑任务的那条 goroutine **不进任务自己的域**（`supervise` 挂在 ctx 上、
收尾时 `Drain`，把执行体自己算进去就是收尾时等自己 → 死锁）。
