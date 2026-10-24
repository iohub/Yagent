# internal/app — 一次性装配

> 范围：本目录内不可派生的约束。全局见仓库根 `AGENTS.md` §6.7。

## 边界

一次性装配 MCP / 工具引擎 / 守卫 / 七个 Agent / Director（`sync.Once`），以及每轮 `TaskRequest`
的装配。蓝图的**运行时唯一持有者**。**不负责** LLM-工具循环，也不写业务判据（判据在 `config` 与
`blueprint` 各自包里）。

**转派**（在本包里找它们是找错地方）：常驻七个 Agent 各构造一份单例（`app.go:406-502`），但**每次委派
新建实例**在 `internal/agents/spawn.go:29-35`；Meta 设计的自定义 Agent 注册成 `delegate_<name>`
并在本任务内永久可用在 `internal/agents/director_surface.go:284`（`MaxSteps=15` 的理由写在 `:260`），
其输出解析在 `internal/agents/director/meta_handler.go:100`。装配层不许复刻这三件事。

## 硬约束

**1. `Init` 的装配顺序本身是被照抄的契约。** `internal/agents/e2e_authorization_test.go:29` 明写
harness 按 `app.go:112-292` **同一条顺序**接线——换顺序就测到一条生产不存在的路（根 AGENTS.md §11
坑 33 同族：测试与生产必须共用同一个载体）。一处连点是硬的：`userConfirmMgr.SetPublisher`
必须先于 `RegisterConsumer`（`app.go:311-318`）。

**2. 任何重建 Agent 引擎的地方都必须保留 `*llm.RoutingEngine` 包装**（`app.go:743-747` 的 `agentEngine`），
否则 auto-routing 静默失效（历史 commit 0c87c8a）。闸门
`TestAgentEngineAlwaysWrapsWithRoutingEngine`（`assembly_test.go:37`）只看返回类型、不驱动 `resolve()`。

**3. `engine` 字段只能经 `setEngine()/defaultEngine()` 读写**（`app.go:95-106`）：TUI 换模型与任务线程
并发，接口值的两个机器字会撕裂。deepthinking / design 的引擎同理只能经 `SetToolProvider` 的热替换分支换
（`app.go:777-799`；design 明确不挂路由档位，`app.go:187-192`）。**运行中换模型不需要任务空闲**：
子 Agent 在下一次委派换新引擎，Director 下一个 step 即生效，正在飞的那次调用不变。

**4. 嵌入二进制无条件解出，与 `[codeseek] binary_path` 无关**（`app.go:194-202`）——`browserctl`
的二级查找依赖 `~/.yagent/bin`（见 `internal/browserctl/AGENTS.md`）。提取失败只 `slog.Debug`（`:198`），
默认级别下不可见；配置值优先于解出值（`:204-217`）。

**5. 蓝图"关掉"与"被护栏拒绝"必须可区分。** `resolveBlueprintRoot`（`app.go:873-878`）是
`[blueprint] enable` 的唯一读取点，三种结果必须可区分（字段注释 `app.go:868-872`、
`blueprint_root_test.go:32,48,62,88`）——把"被护栏拒绝"
压成"关掉"，用户就永远不知道为什么没有蓝图。启动必须**同步**派生一次，孤儿老化要用快照
（`blueprint_runtime.go:57-62`）；`inject_into` 只关常驻注入，**不关读通道与落盘钩子**
（`:129-143`）。关掉整套机制时必须"什么都不动"：不起线程、不建目录、不进 supervise 域
（`blueprint_runtime_test.go:30,65,83`）。

**5.1 "这个项目没有蓝图"只有一个表达：`buildBlueprint()` 返回 nil。** 派生、注入、工具、落盘钩子、GC
五处共问同一个 nil 结果（`blueprint_runtime.go:20-25`，实际分派 `:37-49`），所以消费方**不许**再读一遍
`config.BlueprintEnabled()`——两处判据迟早分叉，而分叉的表现是"蓝图关着、某个注入点还在要视图"。
nil 接收者在 `viewFor` 一类方法上是刻意设计（`blueprint_runtime.go:132-135`）。

**6. MCP 请求超时的兜底只在 `mcp.NewMCPClient` 一处**，不许在 app 再抄一份默认值。
便签 GC 只在装配最开头跑一次，且失败绝不阻断启动（`app.go:136-147`）。
`Close()` 的次序：蓝图先停 → 浏览器 → **`mcpMgr.Close()/ForceClose()` → `supervise.Root().Drain` 最后**并报名字。
Manager 的 goroutine 属于它自己的域，等它先退干净，根域剩下的才是真正的后台活。

**6.1 外部能力的装配点只有这里。** `mcpServerConfigs` 把 `[codeseek]` 与 `[[mcp.servers]]` 翻成
`mcp.ServerConfig`（Manager 不读 config）；`rebuildExternalSpecs` 是 `tools.BuildExternalSpecs` 的唯一调用点
（启动一次 + `OnToolsChanged`）；`ExternalSpecs()` 返回拷贝 —— 六个 Agent 每轮重建工具面都会问它。
`codeseekCuratedBridges` 只此一处声明（6 个手写桥的服务端名，`internal/app/mcp.go:21`，
`TestCuratedBridgesPointAtRegisteredTools` 钉住名单不烂掉）。
外部工具与策略白名单要 `mcpConfigs` 而 Manager 只认启动参数，所以那份 map 必须留在字段上（`app.go:58-60`）；
`specMu` 是 RWMutex 而不是 atomic 数组，因为读侧是六个 Agent 每轮、写侧是 Manager 回调（`app.go:62-66`）。

**6.2 技能注册表在 `Init` 里加载一次**（`skills.Load` 的合并语义），三个入口共用；`SkillSource()`
必须显式判 nil（把 nil 具体类型指针装进 interface 之后它不等于 nil）。`main.go` 里那两份逐字相同的
加载代码已删除 —— 不要加回去。

**6.3 `Manager.Start` 返回后必须当场取到 codeseek 客户端。** `app.go:243-248`：`Start` 只同步 spawn
子进程，`Client("codeseek")` 就在这之后一行——六个手写桥在 `Init` 这一刻捕获该指针，晚一步登记不是"慢"
而是**永久 nil**（比慢更糟）。同一段因此绝不等 initialize / `tools/list`（`app.go:239-241`），
codeseek 冷启动要建索引，等它就是把"终端启动卡在 MCP"做成用户可感知的故障。
判据细节在 `internal/mcp/AGENTS.md` 硬约束 4。

**6.4 静态注册表自检的唯一生产调用点在 `Init` 的 `initOnce` 开头**（`app.go:131-134`），
逐条打日志而不是退出：构建期问题已被 `registry_gate_test` 拦住，运行期退出只会让"配置坏了"与
"代码坏了"共用一个症状。`checkRegistry` 之所以是个函数，只为把假问题喂进去证明这行真的会被调
（`app.go:119-124`，闸门 `assembly_test.go:236 TestInitRunsRegistrySelfCheck` + `:213`）。

**6.5 provider 的三级覆盖不在本包解析。** `ResolveProvider(agent, tool)` 的实现在
`internal/config/config.go:441`，消费点全在 `internal/llm/llm.go`（agent 侧 `:410`、tool 侧 `:461`）。
本包只持有 `main.go` 三个入口传进来的 `*llm.Client`（`app.go:84-92`、`main.go:195,274,328`），
换模型只换 `engine` 字段（第 3 条）；在这里再写一份 provider 查找就是第 25 坑那一类"配置写了没人读"。

**6.6 换 dispatcher 必须同时换三样。** `IntegrateMessaging`（`app.go:539-549`）做：存 `ca.dispatcher`、
`Publisher.SetDispatcher`、把 `UserConfirmMgr` 重注册到新调度器。漏第二件事件发在旧总线上；漏第三件
`user_help_response` 没人收，用户答了却没反应（`SetDispatcher` 之所以够，是因为所有 `ForDelegation`
视图共享同一个 core，`internal/messaging/message_publisher.go:36-43`）。调用点六处
（`main.go:298,362`、`internal/tui/tui_tasks.go:209,285`、`internal/http/task_executor.go:89`、
`internal/http/websocket.go:219`）。**反过来**：跑在 Manager goroutine 上的状态广播必须走
`globalCtx.Publisher`（atomic 指针）而不是 `ca.dispatcher`——它会被 `IntegrateMessaging` 每任务重写
（`internal/app/mcp.go:186-189`）。

## 入口

`app.go`（`Init` 126-537、`TaskRequest` 568-654、`IntegrateMessaging` 539-549）、
`blueprint_runtime.go`、`mcp.go`、`skills.go`、`assembly_test.go`。

## 闸门

`assembly_test.go:37`（去 `RoutingEngine` 包装即红，含无 client 回落分支 `:60`）、`:168`
`TestEngineFieldSurvivesConcurrentProviderSwitch`（删 `engineMu` 只在 `-race` 下红）、
`:72 TestTaskRequestBuilders`（少接一个 `With` = 看不见历史或换错板）、`:131`（`-d` 解析）、
`:213`、`:236`（注册表自检那行被删即红，它读源码而不是跑 `Init`）；
`blueprint_runtime_test.go` 与 `blueprint_root_test.go` 全部。

本包覆盖率长期偏低（2026-10-14 实测 16.9%），低覆盖率的共性是"错了不报错，只是静默换模型/静默丢工具"，
所以这里补的是**契约**而不是行数。
