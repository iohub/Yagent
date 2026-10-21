# internal/messaging — 事件总线

> 范围：本目录内不可派生的约束。全局见仓库根 `AGENTS.md` §6.6。

## 边界

自研事件总线（`MessageDispatcher` / `Publisher` / 两代 Consumer 接口）与丢弃记账。
**不理解事件语义**——类型 SSOT 在 `protocol/agent-events.yaml`（但该文件与生成物已经落后于实际总线）。

## 硬约束

**1. 同一条 `*Event` 会并发分发给多个消费者，消费者必须只读、禁止改字段**
（包注释 `message_dispatcher.go:1-8`）。`Seq` 因此在 `Publish` 里"最后一次被写"（`:224-228`）。

**2. 普通事件满载即丢、绝不阻塞生产者；关键事件走独立旁路。**
`user_help_needed` / `user_help_response` 永不丢（mutex+slice FIFO，防御上限 4096，超限
drop-oldest 并记 error）。只有"同一消费者、同一类别"承诺顺序（`:3-6,24-34,85-122`）。

**3. 绝不 close 事件 channel**——并发 send 会 panic。只 close `stop` + 原子标志，收尾由消费
goroutine 自己排空（`:8-9,304,351-372`）。

**4. 队列容量只有一处定义。** 生产 6 个建调度器的地方全部走 `NewDefaultMessageDispatcher()`
（`message_dispatcher.go:150-158`；`main.go:317,394`、`internal/http/task_executor.go:35`、
`internal/http/websocket.go:182`、`internal/tui/tui_tasks.go:202,278`）。历史事实是六处都手写 `NewMessageDispatcher(100)`，
把文档承诺的 1000 压成十分之一，而**那个 1000 从来没生效过**——满载丢弃比设计早一个数量级发生，
表现是"界面偶尔少字"，日志里只有一条限频 warn（根 AGENTS.md §11 坑 35）。不要再手写数字。

**5. 满载丢弃必须可被知道**：`Dropped()` 是总数，任务收尾调 `WarnDropped(scope)`
（HTTP 任务 / 两个 TUI 闭包 / WS 会话各自报告）。

**6. 委派坐标由 `ForDelegation` 绑定一次，不是在 10 个发布点各传一遍**
（`message_publisher.go:16-21,45-51`；绑定发生在 `internal/agents/executor.go:219-228`）。
绑定用**能力断言**而不是给 `EventBus` 加接口方法——没绑定的正确后果是少一个坐标，
不该逼五个测试 mock 各写一个假方法。绑定视图与母体**共享同一个 core**（`:11-13,20-21`），
否则换 dispatcher 之后事件静默消失。`publish()` 是所有对外发布口的唯一落点（`:63-64`），
新增事件类型不可能漏坐标。

**7. `Event.Clone()` 不复制 `Seq` / `DelegationID`**（`message_event.go:199-223`），
别把它当"复制一份再投递"的手段。

**8. goroutine 只能经 `internal/supervise` 起**（`check_no_bare_go.sh` 硬禁，无 PENDING 名单）；
`Shutdown` 按 30s 预算 `Drain` 并**报名字**（`:307-311`），不是一句"超时"。

**9. 目录名 ≠ 包名**：`internal/messaging/consumers` 里两个文件都声明 `package messaging`
（`consumers/websock.go:1`、`consumers/tui.go:1`），靠 import 别名使用。

## 入口

`message_dispatcher.go`（含包注释那四条全局约定）、`message_publisher.go`、`message_event.go`。

## 闸门

- `envelope_test.go:38 TestDispatcherStampsMonotonicSeq`（序号留洞即红——"界面少了字"第一次变成
  可回答的问题）、`:64 TestForDelegationSharesDispatcherWithParent`（core 拷成值即红）、
  `:107`（空坐标不产生新视图）、`:116 TestDroppedCountsOverflow`。
- `dispatcher_test.go:130 TestDispatcherOverflowDropsWithoutBlocking`——注释点名旧断言
  "`count>1000` 在只发 1000 条时永远为假"（`:133-134`）；`:160` 旁路 FIFO。
- `dispatcher_lifecycle_test.go:111`（recover，否则单次 panic 让该消费者永久静默）、
  `:174`（legacy 收的是适配拷贝）、`:344 TestCriticalEventSurvivesFullNormalQueue`、
  `:383 TestCriticalBypassDropsOldestOverLimit`、`:426`（shutdown 后注册被拒）、
  `:450`（Shutdown 排干旁路）、`:475`（丢弃 warn 限频 ≤1/s）、`:287,316`（零值归一 + 每消费者独立缓冲）。
- 跨包：`internal/agents/e2e_lifecycle_test.go` 与 `internal/agents/event_delegation_test.go:40`。
