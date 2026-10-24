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

**10. 两代消费者：生产全部是旧版广播，而旧版拿到的是裁剪副本。**
`RegisterConsumer(MessageConsumer)` 广播收全部类型、`Subscribe(Consumer)` 按 `Types()` 过滤
（`message_consumer.go:6-21`、`message_dispatcher.go:246-265,266-291`）——**非测试代码里 `Subscribe` 零调用点**
（生产 9 处注册全是 `RegisterConsumer`：`main.go:360`、`internal/tui/tui_tasks.go:207,283`、
`internal/http/task_executor.go:57,86`、`internal/http/websocket.go:210,216`、`internal/app/app.go:317,547`）。
两条推论：① 新增事件类型**不必**改任何 `Types()`，因为没人用过滤订阅；② 投递给旧版的是
`toMessageEvent` 造的副本，**只搬 Type/From/Content/Timestamp/Metadata 五个字段**
（`message_dispatcher.go:384`、`:396-404`），所以 `ID`/`Seq`/`DelegationID`/`Priority`/`TraceID` 到不了旧消费者手里。
**现状要读清楚**：第 6 条把委派坐标盖在事件上、第 1 条把 `Seq` 盖在 `Publish` 里，但真实交付端拿到的是零值 ——
`internal/http/task_executor.go:76-77` 往 realtime 信封里塞的 `seq`/`delegation_id` 与
`internal/tui` 按委派分槽所读的那两个字段，在当前投递路径上恒为 0/空串。要修的是投递（旧副本带上坐标，
或让 HTTP/TUI 走 `Subscribe`），**不是**再往发布侧加一遍坐标。
`TestLegacyConsumerReceivesAdaptedCopy` 钉的是"只搬 5 个字段"这个现状本身，不等于"坐标已送达"。

**11. 事件类型的生成物不能整体重跑生成器。** `internal/protocol/agent_events.go:1` 写着 DO NOT EDIT，
但 `InteractionType`（`:46-55`）、`InferInteractionType`（`:382`）、`isBooleanPair`（`:398`）生成器不产出，
而它把 `AgentEventEnvelope.Data` 手工写成 `interface{}` + 类型 switch（`:216-223`、`:340-374`），
`scripts/protoc-gen-yagent/generator_go.go:105-120` 发的是 `json.RawMessage` 那一版
（落在 `protocol/go/agent-events.go:191-198,315-345`，该文件零 Go 引用方）。两份产物今天实测差 507 行，
整体重跑会同时冲掉手写类型与投递形状，打断授权对话框（消费方清单见 `internal/AGENTS.md` 的 protocol 一节）。
可行做法：改 `protocol/agent-events.yaml` → 只覆盖 `ts/agent-events.ts`、`ts/render-mapping.ts`、
`agent-events.schema.json` → 把生成器**新增的那几块**逐字粘进两份 Go 产物（`docs/skills-and-mcp-design.md:382-383`）。
yaml 本身还落后于总线：`model_routed`、`context_*_compressed`、`thinklink_entry` 都没登记（`task_paused` 有）。

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

**已知覆盖缺口**（钉第 10 条）：没有任何用例断言 `Seq`/`DelegationID` **到达消费者**——
`TestDispatcherStampsMonotonicSeq` 只测发布侧盖号，`TestLegacyConsumerReceivesAdaptedCopy`
只测"副本不带 `ID`"。要修投递路径（旧副本带上坐标 / HTTP 与 TUI 改走 `Subscribe`）之前先补一条
端到端夹具，否则改坏了也还是全绿。第 11 条没有测试（生成器与产物的漂移只能靠人核对）。
