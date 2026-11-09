# internal/llm/scripted — 生产路径上的确定性 `llm.Engine` 替身

> 范围：本目录内不可派生的约束。全局见仓库根 `AGENTS.md` §8.3/§9.2；编排侧的闸门清单见
> `internal/agents/AGENTS.md` 的「闸门」节；设计取舍见 `docs/plasma-architecture.md` ADR-12。

## 边界

按"这是哪一位 Agent 的调用"回放预设响应的 `llm.Engine` 实现（`scripted.go:34`）。**唯一**载体，
两类消费者共用：`internal/agents` 的 9 个 e2e 文件（经 `e2e_engine_test.go:18-22` 的本地别名），
以及 5a 结构夹具（`internal/plasma` 的编译产物由 `agents` 侧 `FixtureRunner` 驱动，不直接 import 本包）。
**不负责**：真实 provider 调用（`engine_openai.go`/`engine_anthropic.go`）、prompt 质量判定
（ADR-3：质量只能离线用真 LLM 验证）。

## 硬约束

**1. 按 Agent 身份分派，不是全局调用序号**（`GenerateContent` → `identityLocked`，`scripted.go:95`）。
序号会被循环里的补偿动作打断，于是同一份脚本在"重试一次"之后整体错位一格，而所有用例仍然看着像绿的。
闸门 `TestEngine_DispatchesByIdentityNotCallOrder`（`scripted_test.go:13`）。

**2. `GenerateContent` 的第一件事是看 ctx**（`scripted.go:65`）。忽略 ctx 的 mock 会把"取消没传播"
直接掩盖掉，受影响的不是本包而是整条执行链。闸门 `TestEngine_HonoursContext`（`scripted_test.go:27`）。

**3. 步骤在锁外执行**（`e.mu.Unlock()` 在 `scripted.go:89`，`run()` 在其后）。`sync.Mutex` 不可重入，
而脚本常要回看引擎的调用记录（`CallsFor`/`SeenAgents` 都上锁）——锁内回调就是自锁死。
闸门 `TestEngine_StepRunsOutsideLock`（`scripted_test.go:37`）。

**4. 三种身份注入分工**：`New`（`:44`，默认嗅探 `sniffIdentity`，`:182`，按四段固定措辞认
director/repo/coding/chat）给 e2e；`NewWithResolver`（`:49`）给需要自定义别名的场景；
`NewForIdentity`（`:55`）**绕开嗅探**给 5a——生成出来的 prompt 首行不可预测，嗅探必然落 `unknown`。
优先级（`identityLocked`）：固定身份 > `AgentName` 归一化后命中脚本 > 嗅探 > `"unknown"`。

**5. `CallOptions.AgentName` 只是进程内通道**（`internal/llm/engine.go:223-226`）：只给可编程引擎做身份
分派，生产 provider 忽略它，**不进任何 wire 字段**。显示名（`Repo-Agent`）与身份键（`repo`）的换算只在
`NormalizeIdentity`（`:173`）一处——两处就会分叉。闸门 `TestNormalizeIdentity`（`scripted_test.go:111`）、
`TestEngine_AgentNameBeatsSniffing`（`scripted_test.go:64`）、`TestEngine_FixedIdentity`（`scripted_test.go:55`）。

**6. 耗尽回落是纯文本，不是 error**（`SetExhaust`，`scripted.go:123`）。error 会触发 LLM 重试，
把"脚本写少了一步"放大成超时；纯文本让循环走自己的收尾分支并留下可读的一句。
闸门 `TestEngine_ExhaustFallsBackToPlainText`（`scripted_test.go:83`）。

**7. `ToolCall` 的 ID 是 `call_<tool>`，稳定契约**（`scripted.go:223`）。既有配对修复按 last-wins 处理
重复 ID，改格式会让一批链路用例以误导的方式失败（症状是"工具结果丢了"，而不是"ID 变了"）。
闸门 `TestToolCallIDFormatIsStable`（`scripted_test.go:96`）。

**7.1 `TextAndToolCall` 存在的理由：一条响应里同时带正文与工具调用**（真实模型常这样回）。
执行循环只在**没有** `tool_calls` 时才走纯文本收尾，所以"先文本、后调工具"的两步脚本
根本走不到第二步；plasma 5a 的 `exit-contract` 靠这条形状验"交付文本取自 assistant 消息
而不是退出 JSON"。别把它当"两条 step 的简写"合并掉 —— 语义不同（`TextResponse` 之后
`ToolCall` 的那种写法会被当成纯文本收尾）。

**8. import 只准 stdlib + `internal/llm`**，闸门 `scripts/check_llm_scripted.sh`。它查**直接 import**
（含测试文件的 import），刻意不用 `-deps`——本包的依赖 `llm` 自己带着 config/logging/models/util，
传递闭包会把它们全误报成越界。理由：本包一旦 import 装配/工具/配置层，依赖就成环，且替身开始依赖
被测组件，接线错的两边会用同一种方式错。身份解析、能力裁决一律由调用方注入。

**9. `Model()` 恒为 `"scripted-mock"`**（`scripted.go:118`）：它是**接线**替身，不是质量替身。
"选对了模型/路由"的断言必须走 provider 层，不要在这里加第二套模型名。

**10. 谁都不许再写第二份 mock**（根 `AGENTS.md` §11 坑 33）。在 `internal/agents` 看见
`agentScriptedEngine`/`scriptStep`/`newScripted` 时那是**类型别名与构造壳**，不要让它变回独立实现；
新的替身需求往本包加构造器或 `SetExhaust`，不要复制。

## 测试

`scripted_test.go` 共 8 条，全部经导出 API 驱动：上面列出的 7 条 + `TestEngine_*` 系列覆盖
三条纪律、两种身份注入、耗尽回落、ID 格式与归一化。改 `sniffIdentity` 的匹配串之前先看
`internal/agents/*.prompt.md` 的首行——那是它认人的唯一依据；prompt 首行改了而这里没改，
症状是"脚本全落到 unknown"，不会报错。
