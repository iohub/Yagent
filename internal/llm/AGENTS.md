# internal/llm — 引擎、路由、故障转移、在飞闸门

> 范围：本目录内不可派生的约束。全局见仓库根 `AGENTS.md` §2.3 与 §6.3。

## 边界

`Engine` 抽象 + 两套协议实现（openai-go SDK / 自研 Anthropic Messages）、provider→引擎的解析与缓存、
auto-routing 三档、故障转移、进程级在飞闸门、以及**发出/读入的字节形状**。
不负责工具循环、不负责压缩阈值推导（`internal/compression`）；输入 token 的**跨 provider 归一**在本包
（第 12 条），怎么展示是 `internal/tui` 的事。

## 硬约束

**1. 引擎构造函数收整个 `*config.ProviderConfig`，不收散参数**
（`engine_openai.go:36-40`、`engine_anthropic.go:111-112`）。散参数时每往 `ProviderConfig` 加一个键
就多一处"配置写了但没人读"——`max_tokens` 曾经正是这样失效的（请求体里根本没有它，thinking 模型
因此能一口气生成 39k token）。

**2. `0` 一律按"没配"处理，且不发这个字段**（`engine_openai.go:28-32,162-167`、
`engine_anthropic.go:100,269-279`）。优先级：`单次 CallOptions > provider 默认 > 不发`。
显式发 `temperature: 0` 会把服务端默认采样顶成贪心解码；`ProviderConfig.Temperature` 是裸
`float64`，分不清"写了 0.0"与"没写"。anthropic 侧未配 `max_tokens` 时兜底 8192（Messages API 必填）。

**3. 认不出的 `reasoning_effort` = 整个 thinking 字段缺席**（`engine_anthropic.go:304-316,323-336`）。
映射是 `low|medium|high|max → 1024/8192/24576/32000`；**绝不发 `budget_tokens: 0`**——provider 要求
≥1024，一个拼错的 effort 会让此后每次调用都 400。此时也不得连带把 `temperature` 钉成 1.0。
开思考时 `max_tokens` 至少 `budget+4096`。

**4. 在飞闸门是进程级，不是每引擎一份**（`inflight.go:8-19`）：429 记在账号上，两个引擎各放 8 格
不等于"这个账号上最多 8 个在飞"。挂在 `GenerateContent` 入口而不是 transport 层（重试在引擎内部循环，
只有这里能保证"一次逻辑请求只占一格、退避期间不额外放行"）。**取格子发生在 `llm.timeout` 之前**，
且必须 `select` 上 `ctx.Done()`（`:37-52`）——放错顺序的后果是排队时间被算进 timeout 与 activity 的
idle 计时，一次正常的排队被当成"provider 慢"（根 AGENTS.md §11 坑 37）。`SetInFlightLimit` 同值不得重建 sem。

**5. SDK 自带重试必须关着**：`option.WithMaxRetries(0)`（`engine_openai.go:67`），退避自己算并钳到 ctx
剩余预算（`:519-578`、`engine_anthropic.go:395-429`）。`context.Canceled` 永不可重试（`netretry.go:44-53`）。

**6. `Client.Engine` 只能经 `DefaultEngine()` 读**（`llm.go:662-667`，接口值两个机器字会撕裂）。
解析顺序：运行时覆盖 > 路由档位 > provider 配置（`router.go:360-378`）。**任何重建引擎的地方都必须
保留 `*RoutingEngine` 包装**，否则 auto-routing 静默失效——契约测试在 `internal/app/assembly_test.go`。

**7. `NormalizeMessages` 必须幂等、四阶段顺序固定**（`messages.go:7-38`），不要在引擎里另写一份
配对修复（它与 `memory` 的读侧配对修复必须同语义）。`FunctionDef.Parameters` 是 map，靠 encoding/json
的字典序稳定——换 JSON 库必须开 `SortMapKeys`，否则工具声明 hash 变、provider 前缀缓存失效（`engine.go:36-41`）。

**8. `FallbackEngine` 必须过滤"不存在的 provider"与"自引用"**（`fallback.go:41-59`）。

**9. `api_format` 只有两态，分支只有一处**（`llm.go:290-292` 问 `ProviderConfig.IsAnthropic()`，
它是 `EqualFold` 而非精确匹配，`internal/config/config.go:31-33`）。**没有 Bedrock 专用客户端**：
`api_base_url` + `api_key` 对每个 provider 一视同仁必填（`internal/config/config.go:620-623`），
所谓 Bedrock 配置只能指向一个 OpenAI-compatible 网关——按名字前缀豁免那两项不等于多出一种协议。

**10. `reasoning_effort` 在两侧是两件事，不许互相"对齐"成一份词表。**
openai 侧非空即**原样透传**并**额外**发 `thinking={type:enabled}`（`engine_openai.go:169-173`）——
DeepSeek 的思考模式要求两者都在；effort 的取值合法性由 provider 判，本包不设 `low|medium|high|max` 白名单。
anthropic 侧才走第 3 条的 budget 映射。配套的一半在内容上：流式读侧从 `delta.RawJSON()` 抽
`reasoning_content`（`engine_openai.go:206-211`），回灌 assistant 轮时必须经 `SetExtraFields`
（`:326-341`）——openai-go 的类型不暴露这个字段，漏掉的症状是思考模型每轮都"忘了自己想过什么"，
不报错。没配 effort 时两个字段都不许出现在请求体里（多数网关对未知字段报错）。

**11. `RoutingEngine` 每次调用现场解析，档位分类失败绝不阻塞任务。**
`Router.Classify` 用 **fast 档引擎**跑一次 `{"tier":"..."}`：30s 上限、`MaxTokens: 32`、
任务文本截到 2000 字符，超时/出错/解析失败一律 `slog.Warn` 后回落调用方传入的 fallback
（即该角色的 baseline，`router.go:265-300`）。`ResolveTierEngine`（`:383-`）在 `RunAgentLoop`
入口把 `*RoutingEngine` 解成具体引擎——这就是"运行中换模型不需要任务空闲"的实现位置
（被并发替换的那几个引擎指针的读写规矩见第 6 条与 `internal/app/AGENTS.md` 硬约束 3）。

**12. 两个 provider 的"输入 token"口径相反，跨引擎只认 `TotalInputTokens`。**
OpenAI 的 `prompt_tokens` **已含**缓存命中，直接照抄就是总输入（`engine_openai.go:264-265,439-440`）；
Anthropic 的 `input_tokens` **不含** cache，总输入是三项相加（`engine_anthropic.go:502-503,694-695`）。
字段口径的唯一声明在 `engine.go:67-70`。混用的表现不是崩溃而是**同一个标签出两个不同的数**——
消费方是 `internal/tui` 的 token 面板，它一律走归一化后的 `TotalInputTokens`
（`internal/tui/token_panel.go:127-129` 的 `normalizedInput`）；压缩判定同样只读它
（`internal/compression/truncate.go:68`）。

**13. finish_reason / stop_reason 只在一处归一，Response 只有一个读取口**
（`engine.go:117-136`、`:148-213`）。两套词表在 `normalizeFinishReason` 汇成同一组常量
（openai `length` / anthropic `max_tokens` → `FinishLength`；`content_filter` / `refusal` →
`FinishContentFilter`）；**认不出的取值原样留着**——既不猜成截断也不报错，消费侧不许再 switch 一次。
四条引擎出口（`engine_openai.go:393`、`:179`、`engine_anthropic.go:488`、`:540`）一律过
`validateResponse`（`engine.go:168-178`）：200 而零 choices、零 content、无产出的流在这里变
`ErrEmptyCompletion`，拒答变 `ErrRefusedCompletion`——**不是空 Choice**。两处循环当年直接
`resp.Choices[0]`，一次网关吞掉上游错误就崩在任务中间，读取因此只准经 `Response.FirstChoice`。
标记只加在**交回给模型**的那一份（`ContentWithNotice` / `AssistantMessage`），provider 原字节不改写：
rollout 指纹与 llm 日志吃的是它。`ToolCallNote`（`:204`）的判据必须与 `internal/tools` 的
`Adapter.Call` 裸字符串兜底同形（两侧都调 `UsableToolArguments`，那是本包唯一的判据；
那边硬约束 9 的另一半）——判据不一致时"回执说明截断"会挂在一条
生产永远不成立的条件上。

## 入口

`engine.go`（接口与 CallOptions）、`inflight.go`、`engine_anthropic.go`（`buildRequestBody` 一段最集中）、
`router.go`。

## 闸门

协议层用 `httptest` 假服务端**钉字节形状**，不是钉行为：

- `TestOpenAIEngine_ProviderDefaultsReachWire`（`engine_openai_test.go:275`）：provider 默认掉出请求体、
  或未配却发 `temperature:0`，即红。
- `engine_anthropic_test.go:231,271,286`：budget 映射、未识别 effort 仍发 thinking、
  thinking 时 `max_tokens`/`temperature` 没抬。
- `inflight_test.go:101,144,218,264,301`：判据取自**假服务端的实际并发峰值**而不是计数器；
  限流改成每引擎一份、排队算进 timeout、同值重建闸门都会红。
- `TestOpenAIRetrySemantics`（`engine_openai_test.go:426`）：SDK 重试被打开（服务端收到 >1 次请求）
  或 400 被重试即红。
- `TestOpenAIEngine_Streaming`（`engine_openai_test.go:504`）：断言请求体里
  `stream_options.include_usage` 为真（构造在 `engine_openai.go:184-186`，断言在 `:545`）——
  没有它 OpenAI 兼容网关不在最后一个 chunk 回 usage，token 面板与压缩判定同时断了计量来源。
- `TestAnthropicParseStreamResponse`（`engine_anthropic_test.go:521`）：流式 `tool_use` 装配退回
  "按 index 连续遍历"就丢整批工具（`engine_anthropic.go:717-725`）。
- 读取口那一组（`engine_response_test.go`）：`TestFirstChoiceRejectsEmptyAndNil`（nil 与零 choice
  必须是错误）、`TestNormalizeFinishReason`（两套词表 + 认不出的原样留着）、
  `TestContentWithNoticeMarksOnlyTruncatedTurns`（标记只加在交回侧，provider 原字节不动）、
  `TestToolCallNoteOnlyForCutOffIncompleteArgs`（含裸字符串入参那条——它就是工具层兜底会走到的形状）、
  `TestToolCallNote_CriterionMatchesAdapterFallback`（`tool_arguments_test.go`：同一批入参逐条对账
  `ToolCallNote` 与 `UsableToolArguments`，两侧判据一分叉即红）。
- finish_reason 穿过引擎边界（`engine_openai_test.go:628-`、`engine_anthropic_test.go:411-`）：
  `TestOpenAIEngine_EmptyChoicesIsError` / `_TruncationReachesChoice` / `_RefusalIsError` /
  `_UnknownFinishReasonKeptVerbatim` / `_StreamingTruncationReachesChoice` /
  `_StreamingNothingProducedIsError`；`TestAnthropicConvertResponse_TruncationIsVisible` /
  `_RefusalIsError` / `_NoContentIsError` / `_UnknownStopReasonKeptVerbatim` /
  `TestAnthropicStream_StopReasonReachesChoice` / `_NothingProducedIsError`。
  删掉任一处 `validateResponse` 调用或 finish 赋值，对应那条立刻红。
- 跨包（判据只有一份，两处循环各要证明自己真的调了）：
  `internal/agents/executor_finish_reason_test.go`（`_NoChoiceResponseIsError` 在旧代码上是 panic，
  不是失败）、`internal/agents/director/planner_finish_reason_test.go`。
- `TestOpenAIEngine_ReasoningEffortAndExtraFields`（`engine_openai_test.go:217`）：两侧字段成对
  （有 effort 时 `reasoning_effort` + `thinking` 都在、单次调用覆盖 provider 默认），
  以及"没配 effort 时两个字段都不许出现"。
- `TestResolveTierEngine`（`router_test.go:71`）、`TestClassify_DisabledFallsBack`（`:165`）：
  路由关掉或分类失败时回落 baseline 而不是让任务失败。
- `router_test.go:101`（`TestRoutingEngine_RuntimeOverrideBeatsTier`）、
  `client_switch_test.go:31,72`、`fallback_test.go:291`、`messages_test.go:198`。
- 跨包契约（不在本包，但去掉 `*RoutingEngine` 包装就会红）：
  `TestAgentEngineAlwaysWrapsWithRoutingEngine`（`internal/app/assembly_test.go:37`）——
  它只看返回类型、不驱动 `resolve()`。

**耗时教训**（根 AGENTS.md §8.3）：不要在这包里跑完整重试序列——baseDelay 是 10s。用短 `cfg.Timeout`
把退避钳到剩余预算，然后断言"服务端只收到 1 次请求"。
