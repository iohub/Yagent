# internal/config — TOML 加载、归一化、文本级写回

> 范围：本目录内不可派生的约束。全局见仓库根 `AGENTS.md` §7。
> 引导**界面**在 `internal/tui/setup*.go`，这里只负责"值从哪来、怎么写回去"。

## 边界

解析顺序（`--config` 原样使用、绝不自动创建 → `~/.yagent/config/config.toml` 不存在则用内嵌模板生成，
0600）、三级 provider 解析、缺省值归一化、文本级写回、provider 生命周期与首启判据。
**不负责这些值怎么被用**——各消费方读结构体或全局单例。

## 硬约束

**1. `validate()` 是缺省值唯一写入点，往里加提前 return 之前先确认下面的归一化段还会被执行**
（`config.go:621-622`）。曾经 Bedrock 豁免写成 `return nil`，于是 max steps、`[browser]` 缺省、
TUI 键位、压缩参数**全部停在零值**——表现不是报错，是能力静默缺失（根 AGENTS.md §11 坑 14）。
回归 `TestLoadFromFile_BedrockNoLongerExempted`（`validate_test.go:110`）同时断言"归一化照常发生"。

**2. "0 有语义 / 缺省为真"的键一律看键的存在性，不看零值。**
段级 `sectionHasDecodedKey`（`config.go:353-365`）+ 单键 `md.IsDefined`
（`:486-487,505-507,518-519,530-531,542-543,554-555`）；九个内部判定字段（mcp/skills 共用一行注释）
的理由逐条写在 `:228-252`。**只剩失效键的段落 == 没写过**（`:352`，`[browser] enabled` 踩过）。
两套取值约定别混：`max_parallel_*` 用 1 = 严格串行、≤0 = 默认；
而 `[llm] max_in_flight_requests` 的 **0 有语义（不限流）**，所以必须靠存在性区分"没写 → 8"与"写了 0 → 不限"。

**3. 写 config.toml 只能文本级替换。** `SetKeyInSection` 是唯一内核（`persist.go:13-23`），
`RemoveKeyInSection` / `RemoveSection` 与三个 `Persist*` 都建在它上面。
禁止 `toml.NewEncoder` 整份重编码——模板里每个键都靠注释自我说明，重编码会把用户注释蒸发。
两个细节：段头必须 trim 后逐字符相等，否则 `[global.llm]` 会匹配到 `[global.llm.providers.x]`
（`:212-215`）；多行字符串里的假段头由 mask 排除（`:248-252`）。落盘走 `util.AtomicWrite`
且权限继承原文件（`:323-333`）。

**4. 已删除的键必须逐条点名。** `removedKeyHints` / `removedProviderKeyHints` 与测试夹具必须互为
镜像，判据是解码器 `Undecoded()`（`config.go:258-292`）。`removedKeyReports` 单独可调用，
注释写明理由："只写进 slog 就没有测试能让它失败"。闸门 `TestRemovedKeysAreNamed`
（`removed_keys_test.go:18`）与 `TestTemplatesTriggerNoRemovedKeyWarnings`（`:108`，误报 = 每次启动刷屏）。

**5. provider 名要过 `providerNameRe`**——点号/引号会造出嵌套表，后果是"写完下次启动加载不了"
（`setup.go:34-47`）。`api_key` 与 `api_key_env` 互斥且必须**显式删另一行**（`:98-120`）；
`temperature == 0` 既不写也不删（"调用方没说 = 继承"，`:85-104`）；被任何 `use_provider` /
`fallback_providers` 引用的 provider 拒绝删除（`:148-159`）——留下一个"加载即失败"的文件比拒绝更糟。
引用扫描走 `DecodeRaw`，因为"想删坏 provider 时配置正好加载不了"（`:161-169`）。

**6. 首启判据全部从配置本身推导，不许引入"看过一次就不再问"的标记文件**（`setup.go:362-394`）——
那是会漂移的第二份事实。`created` 只能来自 `EnsureConfigExists`，模板落盘 0600（`default_config.go:14-21`）。

**7. 全局单例只接受正方向写入**：`SetGlobalLLMTimeout` 拒绝非正值（`llm_timeout.go:26-34`）；
`FormatLLMTimeout` 的输出必须能被 `time.ParseDuration` 读回（`:42-45`）。

**7.1 `[mcp]` / `[skills]` 与 `[blueprint]` 同构**：`enable` / `status_line` 的缺省是"开"而 Go 零值是
false，所以靠 `MCPPresence` / `SkillsPresence`（`md.IsDefined`）判键；`[[mcp.servers]].enabled` 是数组元素，
`MetaData.IsDefined` 拿不到"这一条写没写"，所以用 `*bool`（`mcp.go` 的 `IsEnabled()`）。
`MCPEnabled()` / `SkillsEnabled()` 各自吞下 `YAGENT_MCP=0` / `YAGENT_SKILLS=0`（只认精确 `"0"`）；
server 名字要进工具名前缀，`validateMCPServers` 在 `validate()` 里拒收重名/非法字符/`codeseek` 保留名。

**8. 三个 kill-switch 是各自机制的唯一启用判据**：`BlueprintEnabled()`（`blueprint.go:163-173`）、
`ResumeEnabled()`（`resume.go:122-130`）、`ConcurrencyEnabled()`（`concurrency.go:5-14`），环境变量
只认精确 `"0"`。`internal/blueprint` 自己不读环境变量。判据要写在 `Normalize*` 函数**内部**而不是
调用点——两处都写的后果是一处忘，而漏掉那处的表现只是"还是并发"。

**9. 依赖面**：内部只依赖 `util`（`go list -deps` 实测）。`[blueprint]` 的默认值与渲染器兜底常数
靠跨包对账钉同源（`blueprint_defaults_test.go:9-16`），**不许为省一个常数反转依赖方向**。

**10. `api_base_url`+`api_key` 对生效 provider 一视同仁必填，没有按名字的豁免；`api_key_env` 只在一处展开。**
Bedrock 豁免已连字段整体删除，`aws_region/aws_profile/model_provider` 进 provider 级失效键登记表
（`config.go:272-276`）；生效 provider 缺 key/url = **加载即失败**（`:623-627`），不再是"推迟到第一次调用"。
`ApplyAPIKeyEnv`（`:36-43`）是唯一展开点，此后所有消费方读同一个 `APIKey` 字段；展开循环（`:591-607`）
必须把结果**写回 map**——map 值是拷贝，漏写回则 `GetProvider` 拿到的还是空 key。生效 provider 变量没设 =
启动失败并点名；其它 provider 只 warn（为另一台机器准备的条目不该让本机起不来）。
注意 `WriteProvider` 自身的必填判据更弱（"URL 与密钥不全空"，`setup.go:60-62`），向导侧必须另行把
`api_base_url` 设为必填。闸门 `TestNeedsSetupCoversEveryTrigger`（`template_test.go:80`）内
`:116-142` 钉两个方向：变量已设 → 展开进 `APIKey`；未设 → 加载失败且 err 点名变量名。

**11. `[browser]`/`[blackboard]`/`[resume]` 的整表缺省判定共用一条 `sectionHasDecodedKey`，但各有判定入口与字段。**
`browserSectionConfigured`（`config.go:367-369`）是唯一的包装；判据不能用 `enabled` 的零值——缺省就是
false，用它判等于把新能力静默关掉（`:488` 明写，`:352` 同句还记着"只剩失效键的段落 == 没写过"）。
判定字段落点：`blackboardSectionDefined`（`:497`）、`resumeSectionDefined`（`:510`）。
`[blackboard]` 的三个显式 0 阀门（关索引注入 / 不裁剪行 / 不清理）逐键走 `md.IsDefined`（`:505-507`），
而 `summary_max_runes`/`max_notes_per_board` **没有**"关"的语义，<=0 一律归默认、不加标记
（`blackboard.go:26,33`，判据全文 `:50-61`）——同一段里两种键并存，逐条核对再决定要不要 presence 字段。

**12. 认不出的字符串枚举值必须 warn 后回落默认，不许静默按其中一种策略走。**
`degrade_on_workspace_change` 两态是常量而非裸串（`resume.go:15-20`，注释给出理由："写错一个字母的表现
是默默按另一种策略走"）；归一化 switch 对认不出的值 `slog.Warn` 后回落 `warn`（`:84-94`）。
缺省选 `warn` 而不是 `block`：换机器/pull 后 HEAD 必然变，默认 block 会让 B 层在最主要的场景里失效。
闸门 `TestResumeUnknownWorkspacePolicyFallsBackToWarn`（`resume_test.go:76`）与
`TestResumeExplicitBlockPolicyKept`（`:86`）。

**13. `removedKeyHints` 的登记形态本身就是判据：键名一律小写、只登记会出现的最长路径。**
父表本身不进 `Undecoded()`，子键靠向上归并落到父表这一行（`config.go:254`）——登记写短路径永远匹配不上，
且不报错。provider 名是用户自定义的，`removedProviderKeyHints` 按**第四段**匹配而不是整条路径（`:272`）。
闸门 `TestRemovedProviderKeysAreNamed`（`removed_keys_test.go:67`）与硬约束 4 的两条同族。

## 入口

`config.go`（结构 + `LoadFromFile:477-561` + `validate:576-`）、`persist.go`、`setup.go`、`defaults.go`。

## 闸门

- `template_test.go:14 TestTemplatesDecodeCompletelyAndValidate`：模板里出现 schema 不认识的键即红；
  `:61 TestTemplateShipsPlaceholderKey`、`:80 TestNeedsSetupCoversEveryTrigger`。
- `validate_test.go:299 UltimateCompressionRespectsFalse`、`:170 AppliesDefaultsForAbsentSections`、
  `:337 TestLoadConfigSameAsLoadFromFile`（**不存在的文件必须是 error**，不能静默用默认）、
  `:355 EnsureConfigExistsDoesNotOverwrite`。
- 存在性判定的四张脸：`inflight_test.go:14`（期望值刻意写字面量 8——默认被改成 0 时这条必须红）、`:40`、
  `blackboard_test.go:54,155`、`resume_test.go:49,76,115`、`browser_config_test.go:112,135`。
- `persist_test.go:45,249,267`、`setup_test.go:46,128,187,321`（`SetKeyInSection_KeepsFileMode`）。
- `provider_test.go:42,69,94`：下划线写法**刻意不认**，注释明确要求记下来防止有人顺手加 case。

**已知覆盖缺口**：`provider_test.go:95-102` 的 spellings 表覆盖 director/coding/repo/chat/meta/devops，
**没有 `browser`**，而 `config.go:424-425` 确实认 `browser-agent`——`[agents.llm.browser]` 的别名
写法目前没有任何测试钉住。
