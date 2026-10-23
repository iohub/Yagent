# internal/app — 一次性装配

> 范围：本目录内不可派生的约束。全局见仓库根 `AGENTS.md` §6.7。

## 边界

一次性装配 MCP / 工具引擎 / 守卫 / 七个 Agent / Director（`sync.Once`），以及每轮 `TaskRequest`
的装配。蓝图的**运行时唯一持有者**。**不负责** LLM-工具循环，也不写业务判据（判据在 `config` 与
`blueprint` 各自包里）。

## 硬约束

**1. `Init` 的装配顺序本身是被照抄的契约。** `internal/agents/e2e_authorization_test.go:29` 明写
harness 按 `app.go:112-292` **同一条顺序**接线——换顺序就测到一条生产不存在的路（根 AGENTS.md §11
坑 33 同族：测试与生产必须共用同一个载体）。一处连点是硬的：`userConfirmMgr.SetPublisher`
必须先于 `RegisterConsumer`（`app.go:295-302`）。

**2. 任何重建 Agent 引擎的地方都必须保留 `*llm.RoutingEngine` 包装**（`app.go:757-764` 的 `agentEngine`），
否则 auto-routing 静默失效（历史 commit 0c87c8a）。闸门
`TestAgentEngineAlwaysWrapsWithRoutingEngine`（`assembly_test.go:36`）只看返回类型、不驱动 `resolve()`。

**3. `engine` 字段只能经 `setEngine()/defaultEngine()` 读写**（`app.go:87-98`）：TUI 换模型与任务线程
并发，接口值的两个机器字会撕裂。deepthinking / design 的引擎同理只能经 `SetEngine()` 换
（`:139-160`，design 明确不挂路由档位）。**运行中换模型不需要任务空闲**：子 Agent 在下一次委派换新引擎，
Director 下一个 step 即生效，正在飞的那次调用不变。

**4. 嵌入二进制无条件解出，与 `[codeseek] binary_path` 无关**（`app.go:162-172`）——`browserctl`
的二级查找依赖 `~/.yagent/bin`（见 `internal/browserctl/AGENTS.md`）。提取失败只 `slog.Debug`（`:168`）。

**5. 蓝图"关掉"与"被护栏拒绝"必须可区分。** `resolveBlueprintRoot`（`app.go:890`）是
`[blueprint] enable` 的唯一读取点，三种结果必须可区分（字段注释 `app.go:55-60`、
`blueprint_root_test.go:32,48,62,88`）——把"被护栏拒绝"
压成"关掉"，用户就永远不知道为什么没有蓝图。启动必须**同步**派生一次，孤儿老化要用快照
（`blueprint_runtime.go:57-62`）；`inject_into` 只关常驻注入，**不关读通道与落盘钩子**
（`:129-143`）。关掉整套机制时必须"什么都不动"：不起线程、不建目录、不进 supervise 域
（`blueprint_runtime_test.go:30,65,83`）。

**6. MCP 请求超时的兜底只在 `mcp.NewMCPClient` 一处**，不许在 app 再抄一份默认值。
便签 GC 只在装配最开头跑一次，且失败绝不阻断启动。
`Close()` 的次序：蓝图先停 → 浏览器 → **`mcpMgr.Close()/ForceClose()` → `supervise.Root().Drain` 最后**并报名字。
Manager 的 goroutine 属于它自己的域，等它先退干净，根域剩下的才是真正的后台活。

**6.1 外部能力的装配点只有这里。** `mcpServerConfigs` 把 `[codeseek]` 与 `[[mcp.servers]]` 翻成
`mcp.ServerConfig`（Manager 不读 config）；`rebuildExternalSpecs` 是 `tools.BuildExternalSpecs` 的唯一调用点
（启动一次 + `OnToolsChanged`）；`ExternalSpecs()` 返回拷贝 —— 六个 Agent 每轮重建工具面都会问它。
`codeseekCuratedTools` 只此一处声明（6 个手写桥的服务端名）。

**6.2 技能注册表在 `Init` 里加载一次**（`skills.Load` 的合并语义），三个入口共用；`SkillSource()`
必须显式判 nil（把 nil 具体类型指针装进 interface 之后它不等于 nil）。`main.go` 里那两份逐字相同的
加载代码已删除 —— 不要加回去。

## 入口

`app.go`（`Init` 115-576、`TaskRequest` 589-702）、`blueprint_runtime.go`、`assembly_test.go`。

## 闸门

`assembly_test.go:36`（去 `RoutingEngine` 包装即红，含无 client 回落分支）、`:240`
`TestEngineFieldSurvivesConcurrentProviderSwitch`（删 `engineMu` 只在 `-race` 下红）、
`:71 TestTaskRequestBuilders`（少接一个 `With` = 看不见历史或换错板）、`:130`、`:167`、`:217`；
`blueprint_runtime_test.go` 与 `blueprint_root_test.go` 全部。

本包覆盖率长期偏低（2026-10-14 实测 16.9%），低覆盖率的共性是"错了不报错，只是静默换模型/静默丢工具"，
所以这里补的是**契约**而不是行数。
