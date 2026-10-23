# internal/config — TOML 加载、归一化、文本级写回

> 范围：本目录内不可派生的约束。全局见仓库根 `AGENTS.md` §7。
> 引导**界面**在 `internal/tui/setup*.go`，这里只负责"值从哪来、怎么写回去"。

## 边界

解析顺序（`--config` 原样使用、绝不自动创建 → `~/.yagent/config/config.toml` 不存在则用内嵌模板生成，
0600）、三级 provider 解析、缺省值归一化、文本级写回、provider 生命周期与首启判据。
**不负责这些值怎么被用**——各消费方读结构体或全局单例。

## 硬约束

**1. `validate()` 是缺省值唯一写入点，往里加提前 return 之前先确认下面的归一化段还会被执行**
（`config.go:614-620`）。曾经 Bedrock 豁免写成 `return nil`，于是 max steps、`[browser]` 缺省、
TUI 键位、压缩参数**全部停在零值**——表现不是报错，是能力静默缺失（根 AGENTS.md §11 坑 14）。
回归 `TestLoadFromFile_BedrockNoLongerExempted`（`validate_test.go:107-166`）同时断言"归一化照常发生"。

**2. "0 有语义 / 缺省为真"的键一律看键的存在性，不看零值。**
段级 `sectionHasDecodedKey`（`config.go:365-380`）+ 单键 `md.IsDefined`（`:501-503,516,524-551`）；
七个内部判定字段的理由逐条写在 `:232-255`。**只剩失效键的段落 == 没写过**（`:366-368`，
`[browser] enabled` 踩过）。两套取值约定别混：`max_parallel_*` 用 1 = 严格串行、≤0 = 默认；
而 `[llm] max_in_flight_requests` 的 **0 有语义（不限流）**，所以必须靠存在性区分"没写 → 8"与"写了 0 → 不限"。

**3. 写 config.toml 只能文本级替换。** `SetKeyInSection` 是唯一内核（`persist.go:13-23`），
`SetKeysInSection` / `RemoveKeyInSection` / `RemoveSection` 与三个 `Persist*` 都建在它上面。
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

## 入口

`config.go`（结构 + `LoadFromFile:493-557` + `validate:572-`）、`persist.go`、`setup.go`、`defaults.go`。

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
