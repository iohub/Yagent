# 配置面裁剪 + 首次使用引导 设计方案

> **本文是实施规范**，读者是执行改动的 Coding-Agent。
> 所有"今天如何"的断言都带 `file:line`，核对时间 2026-10-15（分支 `feat-config-trim-onboarding`，基线 main@9bfa894）。
> 前置阅读：`AGENTS.md` §7（配置系统）、§11 坑 3/4/13/14、`docs/config-guide.md`。

---

## 0. 决策记录（已定，不再讨论）

| # | 决策点 | 选定方案 |
|---|--------|----------|
| D1 | 裁剪力度 | **连根拔**：模板删键 + struct 删字段 + `validate()` 删兜底 + 死代码一起删。旧配置残留这些键 → 走统一移除告警点名并给替代写法。**不留兼容 shim** |
| D2 | 撒谎的键 | **补成真语义**，5 处（§3）：`enable_ultimate_compression`、压缩双键 AND、`step_retries` 不下子 Agent、Coding 侧 `keep_plans` 硬编码、`[agents.llm.browser]` 不存在、TUI 键位是"追加别名"而非重映射 |
| D3 | 引导范围 | **Provider 全生命周期 + 首启必答 5 项**（`lang` / yolo 档位 / `[routing] mode` / `[browser] enabled` / `[llm] timeout`）。不做全量键编辑器 |
| D4 | 密钥存放 | **明文继续留 config.toml**（写入时把文件权限收到 0600），新增 `api_key_env = "VAR"` 作为出口。不做 `${VAR}` 通用内插（会把字面含 `$` 的 key 变成定时炸弹） |
| D5 | 入口 | **首启自动 + 手动**：模板刚生成 / 生效 provider 的 key 是占位符 / 配置加载失败 → 直接进引导；另加 TUI `/setup` 与 `yagent setup` 子命令。同时修掉"配置坏了只写日志文件 + 静默 `os.Exit(1)`" |
| D6 | 写回层形态 | 把 `persist.go` 三个几乎同形的函数**收敛成一个 `SetKeyInSection(path, section, key, value)`**，`PersistUseProvider/PersistRoutingMode/PersistLLMTimeout` 退化成薄封装；provider 块写/删复用同一套段定位机器（§4） |
| D7 | 向导形态 | **model 拥有的全屏模式**（与 `/resume` 同构），不用 DialogStack：多字段表单 + 摘要屏用对话框链会碎成 10 个 dialog，且 View 优先级里对话框在 timeline 之下（`tui_view.go:57-62` vs `tui_update.go:42-74` 的不对称） |
| D8 | 连通性验证 | **真发一次最小请求**（`max_tokens=16`、无工具、30s 上限）。不引入"只校验形状"的假验证。取 16 而不是 1：有些 provider 拒 `max_tokens < 某下限`，探测失败会被误读成"密钥不对" |
| D9 | 落地顺序 | A→F 六段，每段结束时 `go build ./... && go vet ./... && go test ./...` 必须干净（§9） |

---

## 1. 现状事实（改动前，逐条已核实）

### 1.1 死键（写了没人读，或只进日志）

| 键 | 唯一"读者" | 判定 |
|---|---|---|
| `[app] enable_streaming` | `internal/llm/llm.go:281` 的 `slog.Debug` | 只进日志。`AppConfig` 仅此一个字段 → 整表连 struct 一起删 |
| `[keywords] default_path` / `hot_reload` / `[[keywords.dict]]` | `config.go:556-568` 自己写、`hasKeywordsConfig()`（`config.go:729-737`）自己读 | 自循环，不外传。真实词典路径硬编码在 `internal/tui/tui_model.go:722-723` |
| `[git_checkpoint] auto_checkpoint` | `internal/agents/git_checkpoint.go:220` 打一条 deprecation 警告 | 唯一效果是**每步刷一条警告**（`coding.go:399-401` → `executor.go:886-897`），比无效果更糟 |
| `[git_checkpoint] checkpoint_interval` / `max_checkpoints` | `git_checkpoint.go:62-63` 拷进结构体后零读者 | 死 |
| `[enhanced_commander] max_delegation_depth` | `config.go:570-571` 兜底成 3 | 死 |
| `[agent] meta_max_steps` | `config.go:513-514` 兜底 | 死：Meta 是单次 LLM 调用（`meta.go`），无 step 循环 |
| `[tools.deferred] hot` / `cold` | `config/deferred_tools.go:51` 打 `len()` | 调档实际只走画像 Promote/Demote（`tools/surface.go:118`） |
| `[browser] enable_skills` | `internal/browserctl/config.go:73` 拷字段 | `browserctl.Config.EnableSkills`（`:45`）零读者 |
| `[codeseek.knowledge]` **全部 5 个键** | `enabled` 被 `config.go:710-712` 见 false 就写回 true；其余 4 个读点（`injector.go:88,97,120,147,152`）全在 `injector.go:69` 的 `if disabled := true; disabled { return }` 之后 | 整段死。**注意**：模板注释（`default_config.toml:229` 附近）说"写路径不受影响所以 enabled 有意义"是错的——`enabled` 根本关不掉 |
| provider `aws_profile` / `model_provider` | 无 | 死（`memory/rollout_types.go:39` 那个 `ModelProvider` 是另一个 JSON struct） |
| provider `aws_region` | `config.go:481` 非空校验 | 只校验不用：没有任何 Bedrock 客户端，豁免后照样要发给 OpenAI 兼容端点 |
| `config.DetectBedrockProvider` | 只有 `provider_test.go:277` | 死函数 |
| `config.DefaultKeywordsConfig` | 无调用者 | 死函数 |

### 1.2 活着但说谎的键

1. `enable_ultimate_compression`：`config.go:576-578` 把 false 写回 true，第三档**永远关不掉**。
2. 压缩要 `enable && enable_context_compression` 同时为真（`coding.go:382`、`repo.go:174`、`devops.go:85`、`browser_agent.go:119`、`director.go:931`）。
3. `[llm] step_retries`：只有 Director（`director.go:147`→`director/recovery.go`）和 Meta（`app.go:447`→`meta.go:73`）生效；`ExecutorConfig.StepRetries`（`executor.go:41`，读点 `:427`）**全仓无写入点**，六个子 Agent 恒为 0。
4. `ultimate_compression_keep_plans`：`coding.go:388` 硬编码 `cfg.UltimateKeepPlans = 0`，配置值到不了 Coding。
5. `[agents.llm.browser]`：`AgentsLLMConfig`（`config.go:119-127`）没有 Browser 字段，写了只报 undecoded。
6. TUI 键位是**追加别名**：`tui_model.go:745+` 建 `editKeyMap[custom] = default`，消费点 `tui_update_keys.go:498-500`（command）/`:841-844`（edit）只翻译按键。自定义键能用，默认键仍然有效且无法释放。另 `tui_model.go:787-809` 的 `tab_1..9` 在 `cfg != nil` 守卫**之前**解引用 → 潜在 nil panic。

### 1.3 首启路径上的洞

- `getConfigPath()`（`main.go:391-417`）：`--config` 原样使用且**不创建**；否则 `~/.yagent/config/config.toml`，缺失时由 `config.EnsureConfigExists`（`default_config.go:16-41`）写内嵌模板；`./config/config.toml` 只在 `UserHomeDir()` 失败时到达 —— `AGENTS.md` §7 说的三级顺序与事实不符。
- 创建失败 → `panic()`（`main.go:403,413`）；加载失败 → `slog.Error`（进日志文件）+ `os.Exit(1)`（`main.go:146-147`），**终端上什么都没有**。
- 模板自带 `use_provider = "deepseek_v4_pro"` 和一个 `api_key = "your-key"` 的完整 provider 块，`validate()`（`config.go:457-492`）只查非空 → 全新机器校验通过、TUI 正常启动，直到第一次提交任务才在 LLM 调用上失败。`"your-key"` 在全部 `.go` 里零出现，没有任何占位符检测；除 `default_config.go:18` 的 `os.Stat` 外没有"首次运行"概念。
- 写回只有 `PersistUseProvider` / `PersistRoutingMode` / `PersistLLMTimeout`（`persist.go:187/107/25`，文本级保注释 + `atomicWriteFile`）。**没有任何函数能写 provider 块或新建 `[global.llm.providers.X]`**。
- 可复用：`components.DialogStack`、`components/dialog_list.go` 的列表排版原语、`common/styles.go:749` `DialogBorderStyle`、`tui_chrome.go:100/128` 的 `renderPanelTop/renderPanelBottom`、`models.ContextWindow` / `models.Benchmarks`（478 条目录，`dist/data/models.json`）。带输入框的先例只有 `components/user_help_dialog.go:97-108` 的 textarea（**必须** `SetBounds` 后再 `Push`、**必须** `d.Focus()` 否则按键全丢、**必须**放行 `PasteMsg`/`taskCompleteMsg`）。

---

## 2. 阶段 A：连根拔（§1.1 的 13 项）

### 2.1 删什么

| 位置 | 动作 |
|---|---|
| `internal/config/config.go` | 删 `AppConfig` 及 `Config.App`；`KeywordsConfig` 只留 `DisableCompletion`（删 `DictConfig`、`DefaultPath`、`HotReload`、`Dicts`）；`GitCheckpointConfig` 删 `AutoCheckpoint/CheckpointInterval/MaxCheckpoints`；`EnhancedCommanderConfig` 删 `MaxDelegationDepth`；`AgentConfig` 删 `MetaMaxSteps`；`DeferredToolsConfig` 删 `Hot/Cold`；`BrowserConfig` 删 `EnableSkills`；`CodeSeekConfig` 删 `Knowledge` 字段与 `KnowledgeConfig` 类型；`ProviderConfig` 删 `AWSRegion/AWSProfile/ModelProvider`；删 `DetectBedrockProvider` |
| `internal/config/defaults.go` | 删 `DefaultMaxSteps.Meta`、`DefaultKeywordsConfig`、`DefaultKnowledgeConfig`、`DefaultGitCheckpointConfig` 里三个死字段 |
| `internal/config/config.go` `validate()` | 删 `:556-568`（keywords 兜底）、`:570-571`、`:513-514`、`:601-605`、`:709-724`、`:479-483`（Bedrock 豁免连同 `isBedrock` 分支一起删，改成无条件要求 `api_key`+`api_base_url`）。**绝不能在归一化段之前加 `return`**（`:476-478` 的注释与 AGENTS §11 坑 14 仍然成立） |
| `internal/config/deferred_tools.go` | `:49-54` 日志去掉 `forced_hot/forced_cold` |
| `internal/agents/git_checkpoint.go` | 删 `Config.AutoCheckpoint/CheckpointInterval/MaxCheckpoints`（`:23-25`、`:40-42`、`:61-63`）与 `OnStepEnd` 方法（`:207-230`） |
| `internal/agents/executor.go` + `coding.go` | 删 `ExecutorConfig.OnStepEnd`（`:50`、`:886-897`）与 `coding.go:399-401` 的挂载。这个钩子从设计上只为那条警告服务，删了才是真的收干净 |
| `internal/browserctl/config.go` | 删 `EnableSkills`（`:45`、`:73`） |
| `internal/knowledge/injector.go` | 4 个注入参数改为文件内常量 `injLimit=8 / injMaxTokens=2000 / injMinScore=0.3 / injRerank=true`，加一行注释指回 `injector.go:69` 的 TODO（恢复注入时再变回配置）。`k.cfg` 字段与其读写点一起清掉 |
| `internal/app/app.go:288-291` | injector 构造条件从 `Knowledge.Enabled && MCP != nil` 改为 `MCP != nil`（enabled 本来就恒为 true，行为不变） |
| `internal/llm/llm.go:281` | 去掉 `streaming_enabled` 属性；若该 `slog.Debug` 因此没有实参了，整条删掉 |
| 两份 TOML | 删上述所有键与其说明文字；`[app]`、`[keywords]`（只留 `disable_completion`）、`[codeseek.knowledge]` 三段按新形状重写 |

### 2.2 移除键的统一告警（不让旧配置静默失效）

`LoadFromFile` 现有三段 per-section 告警（`config.go:404-436`）。新增一张移除登记表并在解码后立即跑一遍：

```go
// removedKeyHints: 已删除的 TOML 路径 → 该写什么替代（空串 = 什么都不必写，功能已不存在）
var removedKeyHints = map[string]string{
    "app":                              "",
    "app.enable_streaming":             "",
    "keywords.default_path":            "词典路径固定为 ~/.yagent/keywords.txt 与 <project>/.yagent/keywords.txt",
    "keywords.hot_reload":              "",
    "keywords.dict":                    "",
    "agent.meta_max_steps":             "Meta 是单次 LLM 调用，只有 meta_retry_count 有意义",
    "enhanced_commander.max_delegation_depth": "",
    "git_checkpoint.auto_checkpoint":   "检查点由 git_checkpoint_create 工具创建",
    "git_checkpoint.checkpoint_interval": "",
    "git_checkpoint.max_checkpoints":   "",
    "tools.deferred.hot":               "调档只走 toolspec 画像的 Promote/Demote",
    "tools.deferred.cold":              "调档只走 toolspec 画像的 Promote/Demote",
    "browser.enable_skills":            "",
    "codeseek.knowledge":               "知识注入目前硬关闭（injector.go:69），预算已内联；enabled 从未生效过",
}
```

落地版与这段草稿的三处差别（以 `internal/config/config.go` 的 `removedKeyHints` 为准）：

1. **父表名不进登记表**（草稿里的 `"app"`）。段头本身不会出现在 `Undecoded()` 里，登记了也永远不响；
   只有 `app.enable_streaming` 这类**键**会。子键靠"向上归并"落到父表那一行，所以
   `keywords.dict` 有登记而 `keywords.dict.name/files/type/builtin_type` 没有。
2. **provider 段的死键另开一张表** `removedProviderKeyHints`（按路径第五段匹配，因为 provider 名是用户自定义的）。
   路径是 `global.llm.providers.<name>.<key>` —— 键名在第五段，写实现时按第四段查会一个都不响（第一版就是这么错的，
   被 `removed_keys_test.go` 抓出来）。
3. **hint 不留空串**：空串在渲染处兜成"没有读者，已删除"，但登记表里每条都写了具体去向，
   因为"告警点名"的价值就在于告诉用户下一步敲什么。

- 判据用 `md.Undecoded()`；`[codeseek.knowledge]` 整段命中时只报一次段名，别按子键刷屏。
- Bedrock 单独处理：豁免整体删除后，任何 provider 缺 `api_key`/`api_base_url` 都会失败，错误信息写清"本仓库没有 Bedrock 专用客户端，请指向 OpenAI-compatible 端点"。
- 必测：`internal/config/removed_keys_test.go:TestRemovedKeysAreNamed` —— 登记表与夹具**互为镜像**
  （表里有键而无夹具 = 承诺没人验证；有夹具而表里无键 = 漂移）。报告由 `removedKeyReports(md)` 返回可断言的
  字符串列表，`warnRemovedKeys` 只负责把它写进 slog；只返回 void 的话没有任何测试能让它失败。
  另加 `TestTemplatesTriggerNoRemovedKeyWarnings`：两份模板绝不能出现在报告里（误报会让每次启动都刷一条）。

---

## 3. 阶段 B：补真语义（§1.2）

| # | 改动 | 位置 | 行为差异与补偿 |
|---|---|---|---|
| B1 | `enable_ultimate_compression = false` 真的关掉第三档 | 删 `config.go:576-578`；改 `validate_test.go` 的 `TestLoadFromFile_UltimateCompressionIsForcedOn` → `TestLoadFromFile_UltimateCompressionRespectsFalse` | 曾写 false 的人从此真关掉。AGENTS §6.5 那句"写 false 也关不掉"必须同步删除 |
| B2 | 压缩只留一个键：`enable_context_compression`，删 `enable` | 删 `EnhancedCommanderConfig.Enable`；5 个 AND 点（`coding.go:382`/`repo.go:174`/`devops.go:85`/`browser_agent.go:119`/`director.go:931`）改读单键；`removedKeyHints["enhanced_commander.enable"] = "只需 enable_context_compression"` | (true,false) 想关的仍然关；(false,true) 想开却被打死的这次真开了；缺省仍是模板的 false，**没有任何人在本意相反时被打开** |
| B3 | `step_retries` 下发到子 Agent | `agents/deps.go` 的五个 params 结构体加 `StepRetries int`（与 `MaxSteps` 同形），`app.go` 构造处灌 `ca.config.LLM.StepRetries`，各 Agent `cfg.StepRetries = p.StepRetries` | 默认 0 = 关，因此对现有用户零差异；`e2e_step_retries_test.go` 用"第一次调用报错、第二次成功"的脚本引擎断言子 Agent 确实重试了 |
| B4 | Coding 侧 `UltimateKeepPlans` 真取配置 | `coding.go:388` `cfg.UltimateKeepPlans = 0` → `ec.UltimateCompressionKeepPlans` | 与 Repo/DevOps/Browser（`repo.go:182` 等）对齐；模板里那句"Coding 侧硬编码 0"的警告删掉 |
| B5 | 补 `[agents.llm.browser]` | `AgentsLLMConfig` 加 `Browser *AgentLLMOverride`，`getAgentOverride`（`config.go:288-305`）加 `"browser-agent"/"browser"` 分支；两份模板补一段 | Browser Agent 的 provider 终于能单独配；`provider_test.go` 加一条解析用例 |
| B6 | 键位改成真重映射 | `tui_model.go:743-844` 改成表驱动：每个动作先播种 `default→default`，用户给了别的键则 `custom→default` **且** `default→""`；两个消费点（`tui_update_keys.go:498-500`、`:841-844`）把 `""` 当作未绑定直接放行 | 默认没配的人行为不变；配过的人在 `TestKeybindingReplacesNotAdds` 里钉住"新键生效 + 旧键释放"。顺带消灭 `tab_1..9` 的守卫顺序 bug |

---

## 4. 阶段 C：写回层（provider 块是引导页的前置能力）

### 4.1 收敛 `persist.go`

今天 `PersistLLMTimeout`/`PersistRoutingMode`/`PersistUseProvider`（`persist.go:25/107/187`）是同形三段：读文件 → `multilineMask` → 找段头 → 求 `secEnd` → 找键行 → 改值或插行 → `atomicWriteFile`。收敛成：

```go
// SetKeyInSection 在 [section] 内把 key 设为 renderedValue（调用方负责渲染成合法 TOML 右值）。
// 段缺失 → 末尾追加；键缺失 → 段内最后一个非空行后插入；都不动其它行、注释、缩进。
func SetKeyInSection(path, section, key, renderedValue string) error

// RemoveSection 删除 [section] 整段（段头到下一个段头之前），用于删 provider。
func RemoveSection(path, section string) error
```

- `replaceUseProviderValue` 的"保留行内注释与等号两侧空白"逻辑提升为 `replaceKeyValueOnLine(line, key, value)`，三个旧函数共用。
- 旧名字保留为薄封装（TUI 有 4 处调用：`tui_update_keys.go:310`、`:366`，`tui_update_commands.go:419` 等），签名不变。
- **仍然禁止整文件重编码**（`persist.go:182-184` 的规则）：`toml.NewEncoder` 会把用户注释全蒸发。

### 4.2 provider 写 API（新文件 `internal/config/setup.go`）

```go
type ProviderDraft struct {
    Name          string   // TOML 表名，需过 IsValidProviderName
    Model         string
    APIBaseURL    string
    APIKey        string   // 与 APIKeyEnv 互斥，两者都填以 APIKeyEnv 为准
    APIKeyEnv     string   // 环境变量名；非空则 config.toml 里不出现明文
    APIFormat     string   // openai | anthropic
    MaxTokens     int
    ContextWindow int
    Temperature   float64  // 0 = 不写这一行（与"0 一律按没配"的既有语义一致）
    ReasoningEffort string
}
func IsValidProviderName(s string) error          // [A-Za-z0-9_-]{1,64}，且不能是已存在的表名冲突
func WriteProvider(path string, d ProviderDraft) error
func DeleteProvider(path, name string) error
```

- 段落名固定 `[global.llm.providers.<name>]`；值一律 `tomlBasicString`，整数原样。
- `Temperature` 为 0 时**不写键**（写 `0.0` 也没有区别，但模板已说明 0=没配，别造噪声）。
- 删除 provider 后必须清理引用：若 `global.llm.use_provider` 或任一 `agents.llm.*`/`tools.llm.*` 仍指向它，`DeleteProvider` 返回错误并点名是哪一行 —— 让引导页去处理，而不是留下加载即失败的文件。
- 写完 `atomicWriteFile` 已继承原文件权限；首建时把模板落成 **0600**（`default_config.go:32` 的 0644 改掉），因为里面可能有明文 key。

### 4.3 `api_key_env`（D4）

- `ProviderConfig` 加 `APIKeyEnv string \`toml:"api_key_env,omitempty"\``。
- 新增 `func (p *ProviderConfig) ResolveAPIKey() (string, error)`：`APIKeyEnv` 非空 → 取 `os.Getenv`，缺失即 `fmt.Errorf("provider %q: api_key_env=%s is not set in the environment", …)`；否则返回 `APIKey`。
- `validate()`：生效 provider 用 `ResolveAPIKey` 判定（配了 `api_key_env` 就不要求 `api_key`）；错误必须是**人话 + 变量名**。
- `llm.NewClient` / `getOrCreateEngine`（`llm.go:338`）改走 `ResolveAPIKey`，并且遍历所有 provider，对 `api_key_env` 未设置的发 `slog.Warn`（生效的那个直接 fail）。
- `${VAR}` 通用内插**明确不做**（D4 的理由）。

---

## 5. 阶段 D：首启检测 + 修掉静默失败

```go
// internal/config/setup.go
type SetupReason int
const (
    SetupNotNeeded SetupReason = iota
    SetupFreshInstall          // 模板刚由本次进程生成
    SetupPlaceholderKey        // 生效 provider 的 key 是占位符
    SetupBroken                // 加载/校验失败，附原始错误
)
func NeedsSetup(cfg *config.Config, path string, fileCreated bool, loadErr error) (SetupReason, string)
func IsPlaceholderKey(s string) bool   // ""、"your-key"、"sk-your-key"、"changeme"、"REPLACE_ME"、纯 "xxx…"
```

- `EnsureConfigExists` 签名改成 `(created bool, err error)`（`main.go:402,412` 两处调用同步）。
- `getConfigPath` 不再 `panic`，改返回 `(path string, created bool, err error)`。
- `main.go`：`runTUI` / `runHTTP` / `runPrompt` 三处的失败路径统一成——
  - TTY 且 TUI 模式 → 带着 `SetupReason` 启动 TUI 并直接进向导；
  - 非 TTY（`--prompt` / `http`）→ **stderr 打三行**：哪条键坏了 / 配置在哪个文件 / `请运行 yagent setup`，退出码 2。绝不再只进 `~/.yagent/logs`。
- 手动入口：TUI `/setup`（要显式加进 `processCommand` 的斜杠分支，否则是搜字符串 "setup"——AGENTS §11 坑 17 同一个洞）；`yagent setup` 子命令（装配完就进向导，保存后落回正常 TUI）。
- 向导**不新增标记文件**：判据全从配置本身推导（fresh/placeholder/broken），避免"标记与配置两份事实"的老问题。

---

## 6. 阶段 E：引导向导（D7）

> **2026-10-16 修订**：向导的呈现与交互已被 `docs/setup-wizard-ui-design.md` 取代并落地
> （四段阶段轨 / 必填 4 格 + `ctrl+o` 可选区 / `ctrl+p` 端点预设 / `f2` 合并密钥来源 /
> 探测 spinner / diff 摘要）。本节保留的是当时的决策记录 D1–D9，**其中 §6.2 的屏序细节与 §6.3 的
> 第 2、4 条已按新文档更正**；未兑现项在每段末尾点名。

### 6.1 状态与文件

`internal/tui/setup.go`（状态机 + 现状读取 + 落盘队列 + 探测）、`setup_keys.go`（按键与表单）、
`setup_render.go`（各屏排版 + `RunSetup` 独立宿主）、`setup_embed.go`（主 TUI 的全屏模式宿主）。model 上挂：

```go
setupMode     bool
setupStep     setupStep        // welcome / providerList / providerForm / probe / globalDefault / firstRun / summary
setupProviders []setupProviderRow   // 从 cfg.Global.LLM.Providers 快照
setupCursor   int
setupFieldIdx int
setupDraft    config.ProviderDraft
setupReason   config.SetupReason
setupErr      string           // 上一轮 probe 或写入的原始错误，逐字显示
```

进入：`enterSetupMode(reason)` —— 清空 `taskRunning` 假设（首启时没有任务），与 `enterResumeMode`（`tui_resume.go:391-411`）一样先重置其它叠加模式。

### 6.2 屏序

```
1 welcome      解释要配什么、配置文件在哪、为什么现在问这个。Enter 开始 / Esc 跳过（不写任何东西）
2 providerList 现有 provider 表格：名字 / model / base_url / key（明文显示前 4 后 2，配 api_key_env 显示变量名）
               a 新增  e 编辑  d 删除  t 连通性测试  数字键快捷选
               【更正】Enter 保持"编辑"（它是唯一能改块的地方），"设为默认"改成 s ——
               同名键在两屏不同义比多按一次更贵。徽章取代文本 flag，占位密钥显形为 `fix key`。
3 providerForm 必填 4 格：name → api_base_url → 密钥(api_key ⇄ api_key_env 一行两态，f2 切) → model
                         可选 4 格（api_format / reasoning_effort / max_tokens / context_window）默认折叠，ctrl+o 展开
               Tab/Enter 下一格（折叠态的"末格"是 model），Ctrl+S 随时存草稿，Esc 放弃
               ctrl+p 端点预设只填空字段；model 命中内嵌目录时回填 context_window（手输过就不覆盖）
               【更正】"auto-routing 会归到哪一档"没做：`internal/models` 只导出 ContextWindow 与
               Benchmarks，`ClassifyTiers` 是对候选集分档、不是给单个模型定档 —— 报不出来就不报。
               【本轮补】api_base_url 进必填：旧校验只查 name/model/密钥，而 validate() 要求每个
               provider 都有 URL，于是表单能放行出一份"下次启动加载失败"的文件
4 probe        一次最小请求（max_tokens=16，无 tools，超时 30s）。成功：model + 延迟 + 回包文本；
               失败：provider 返回的原话逐字显示，不做美化。Enter 回 3 改，p 重测
5 globalDefault 从 provider 列表里选 use_provider（写 [global.llm]）
6 firstRun     5 行可编辑：lang（""/中文/English）· yolo 档位（off/workspace/full）·
               routing mode（auto/fixed）· browser enabled（true/false）· [llm] timeout（5m/10m/15m/30m）
               （代码取的是 5m/10m/15m/30m：模板缺省 10m 的邻域，改文档不改档位）
7 summary      将要写入的变更按 PROVIDERS/DEFAULT/CHOICES 分组显示成人话 diff（+ − → ~ =），
               每条 op 的 note 原文保留为次级行（applyOps 失败时报的就是它），Enter 落盘、Esc 不落盘
```

- 落盘顺序：`WriteProvider` → `DeleteProvider` → `SetKeyInSection("global.llm","use_provider")` → `[agent] lang/yolo_mode/full_yolo_mode` → `[routing] mode` → `[browser] enabled` → `[llm] timeout`。**这不是事务**：`applyOps` 逐条落盘、遇错立即停止，并把失败的 `note` 原文留在面板上 —— 报告"失败但前面几条已写入"才是实话，包装成"什么都没发生"是在骗人。
- 写完后必须让**运行中的进程**也生效：复用现成的 runtime 路径（`client.SwitchProvider` / `SetLLMTimeout` / `SetRoutingMode`，见 `tui_update_keys.go:240-301` 的"先 runtime 再 persist"次序），而不是只改文件。
  **仍未兑现**（2026-10-16 的 UI 刷新明确把它划出范围）：`/setup` 写完只改文件并提示重启，
  运行中的引擎不变。摘要屏与退出信息都把这件事说出来，不假装生效。

### 6.3 必须照办的 TUI 约束（AGENTS §11 坑 6 / §6.8）

1. `setupMode` 的 Update 必须把 `taskEventMsg` 交给 `handleTaskEventMsg`、`taskCompleteMsg` 交给 `handleTaskCompleteMsg`（后者是 `taskRunning` 唯一清零点），并重新 `listenForEvents` —— 抄 `tui_resume.go:181-188` 的透传形状。
2. textarea 只在 `providerForm` 存在，样式沿用 `newTaskInput`（`tui_model.go:633-688`：`SetVirtualCursor(true)`、prompt 色 39、焦点态 bg 235/236），进入该步必须 `Focus()` + 返回 `textarea.Blink`。
   ✅ 已兑现，且收成一份实现：`applyTextareaTokens`（`tui_model.go`）同时喂主输入框与向导的表单字段
   —— 两边各写一遍的话，配置表单（用户对项目的第一印象）会比聊天框朴素。
3. `WindowSizeMsg` 与 `MouseMsg`/`FocusMsg`/`BlurMsg`/`tea.PasteMsg` 都要放行（`PasteMsg` 的坑已在 `tui_update.go:58-65` 踩过一次）。
4. 排版只用现成令牌：`common.DialogBorderStyle`、`renderPanelTop/renderPanelBottom`（`tui_chrome.go:100/128`）、`KeyHintStyle` 键帽、强调色 141/62/214/39/76。宽度不足（`<40`）时给"请把终端拉宽"的一行提示而不是空白。
   ✅ 已兑现（2026-10-16）：`setupTop/setupBottom` 那套自造 chrome 已删，向导直接调 `tui_chrome.go`
   的三个函数；列表原语上提到 `common/list_metrics.go` 与对话框共用。
   附带补了**高度**闸门（原文只守宽度）：`<18` 行给一行提示，超出部分走滚动窗口 ——
   旧版长错误 + 8 条 op 会溢出到 AltScreen 之外（AGENTS §11 坑 30）。
5. `setupMode` 在 `View()` 优先级里排在 timeline/thinklink **之上**（首启时它们是空的），在 `Update()` 里同样最高优先级，避免 D7 提到的不对称。

---

## 7. 阶段 F：文档与模板同步

- 两份 TOML（`config/config.toml` 与 `internal/config/default_config.toml`）：键集必须完全一致，只删不动活着键的语义；头部说明改成"首次运行请用 `yagent setup` 或 TUI 里的 `/setup`"。**注释一律英文**（现有约定）。
- `docs/config-guide.md`（745 行）：删掉所有被移除键的段落，新增"引导向导"和 `api_key_env`。
- `AGENTS.md`：§7 重写解析顺序（`./config/config.toml` 只在 `UserHomeDir()` 失败时到达）；§6.5 去掉"两个键 AND"与"第三档关不掉"；§11 坑 4（模板里的 `[context]` 表）已不存在——核对确认可删；§11 坑 14（Bedrock 豁免）改成"豁免已删除，Bedrock 需要真实 OpenAI 兼容端点"。
- `README.md` 的配置一节同步删死键示例。

---

## 8. 测试与闸门

| 不变量 | 由谁钉 |
|---|---|
| 死键全部消失且旧配置被点名 | `internal/config/removed_keys_test.go`（每个 `removedKeyHints` 条目都要有一条能响的告警用例） |
| 归一化段仍完整执行 | 现有 `TestLoadFromFile_BedrockSkipsOnlyKeyChecks` 改写为 `…BedrockNoLongerExempted`，并保留"缺省值都被归一化"的断言（AGENTS §11 坑 14 的教训） |
| 第三档真能关 | `TestLoadFromFile_UltimateCompressionRespectsFalse` |
| `step_retries` 下到子 Agent | `internal/agents/e2e_step_retries_test.go`（脚本引擎按 Agent 身份分派，不看全局序号——AGENTS §8.3） |
| provider 写回不破坏注释 | `internal/config/setup_test.go` 往返表：新建段 / 改值保行内注释 / 删段 / 删仍被引用的 provider 必须报错 / 0600 |
| `api_key_env` 落进请求头 | `internal/llm/engine_openai_test.go` 现有 `httptest` 假服务端家族加一条：env → Authorization 头；未设置 → 启动错误含变量名 |
| 键位是替换不是追加 | `internal/tui/tui_keymap_remap_test.go`：新键触发动作 + 旧键变未绑定 |
| 向导不吃掉 `taskCompleteMsg` | `internal/tui/setup_test.go:TestSetupModePassesTaskCompleteThrough`（必须用 `chromeTestModel`：最小 fixture 没有 textarea，`handleTaskCompleteMsg` 会 nil panic，测到的是 fixture 而不是透传），形状照 `tui_model_switch_test.go:153-166` |
| 向导渲染 | `internal/tui/setup_test.go:TestWizardRenderWidthMatrix`，宽度 `{40,50,60,72,80,100,120,160,200,240}` × 七屏，断言**每行显示宽度 ≤ 终端宽度**（动态文本撑破面板 = 整屏错位） |
| 向导状态机 | `internal/tui/setup_test.go`：happy path 真写文件并 `LoadFromFile` 读回、摘要前放弃逐字节不变、删被引用的 provider 被拒、数字字段过滤字母、名称不可改、坏配置仍能进、首选项映射 |
| 键位是替换不是追加 | `internal/tui/tui_keymap_remap_test.go`（新键生效 + 旧键解绑 + 冲突保留先声明者 + `keyUnbound` 不匹配任何 case） |
| 首启三种判据 | `internal/config/template_test.go:TestNeedsSetupCoversEveryTrigger`：fresh / placeholder / broken 各一条，且"配置正常时绝不进向导"、`api_key_env` 展开与缺变量报错 |
| 模板与 schema 同源 | `internal/config/template_test.go:TestTemplatesDecodeCompletelyAndValidate`：两份模板的每个键都必须被解码（无 `Undecoded`）且能 `LoadFromFile`；`TestTemplateShipsPlaceholderKey` 保证占位符判据不被"顺手填个可用 key"灭掉 |

闸门：`bash scripts/check_all.sh`（build + vet + 五把守卫 + 全量单测）。改动到 `tui`/`executor` 并发性状时另跑 `RACE=true bash scripts/check_all.sh`。
`scripts/check_file_sizes.sh` 注意：`persist.go` 收敛后应变短；`tui_setup.go` 是新文件，超 300 行直接红 —— 渲染部分必须放 `tui_setup_render.go`。

---

## 9. 实施顺序（每步结束都要能 build + test）

1. **A 裁剪**：config struct/validate/兜底 → 死代码（git_checkpoint `OnStepEnd` 链、browserctl、knowledge 常量、llm 日志）→ 移除告警表 → 两份模板 → 同步 `validate_test/provider_test/defaults_test`。
2. **B 补真**：B1→B6，每处独立 commit。
3. **C 写回层**：`SetKeyInSection/RemoveSection` 收敛（旧函数转薄封装，`persist_test.go` 原表必须全绿）→ `setup.go` provider 写/删 → `api_key_env` + `ResolveAPIKey`。
4. **D 首启检测与失败 UX**：`EnsureConfigExists/getConfigPath` 签名改造 → `main.go` 三处 → `yagent setup` → `/setup`。
5. **E 向导 UI**：状态机与 7 屏渲染 → textarea 步 → probe → 落盘与 runtime 生效。
6. **F 文档**：AGENTS.md / config-guide.md / README / 模板注释。

---

## 10. 明确不做

- 不做 `${VAR}` 通用字符串内插（D4）。
- 不做"全量 live 键编辑器"（D3）。
- 不恢复知识注入（`injector.go:69` 的 TODO 保持原样，等它自己回来时再把四个预算变回配置）。
- 不做密钥进 keychain；本次只把 config.toml 收到 0600 + 给一条环境变量出口。
- 不新增 `enabled` 段级布尔到 `[blackboard]`/`[resume]`（AGENTS §7 记录的"零值静默关新能力"教训仍然适用，kill-switch 继续走 `YAGENT_BLACKBOARD=0` / `YAGENT_RESUME=0`）。

---

## 11. 落地时发现的额外四件事（原方案没写，属于同一类病）

1. **`[git_checkpoint] enabled = false` 以前根本关不掉**。`validate()` 的写法是
   `if AgentBranchPrefix == "" { c.GitCheckpoint = 整份默认 }` —— 只写 `enabled = false` 的人
   前缀为空，于是整段被默认值顶回去，`Enabled` 变回 true 且无任何提示。
   现在改成与 `[browser]`/`[blackboard]` 同一套段存在性判定（`gitCheckpointSectionDefined`），
   写过任一键就只兜两个前缀（空前缀会拼出 `/<session>` 这种坏分支名），bool 尊重用户写的零值。
   由 `validate_test.go` 的 `off` 断言钉住。
2. **`ExecutorConfig.OnStepEnd` 整条链路只为一条警告存在**：`coding.go` 挂载 → `executor.go` 每步调用 →
   `git_checkpoint.go:OnStepEnd` 只在 `AutoCheckpoint` 为真时 `slog.Warn`。删掉它顺带解决一件没人注意的事：
   `sub_agent_activity` 这条 rollout 记录以前挂在这个钩子里，因此**只有"Coding + git checkpoint"**那一条路径
   会写步数事实，其余子 Agent 的 `MaxStepObserved`（`rollout_reader.go` 的 `EffectiveSteps` 二级来源）
   只能靠 assistant 轮次猜。现在它在每个有工具调用的 step 都写，六个子 Agent 一致。
3. **`WriteProvider` 对 `temperature` 采用"没说就继承"**。草稿里是"`draft.Temperature <= 0` 就删那一行"，
   看起来更声明式，实际会埋雷：表单不问温度，于是"打开过一次表单再保存"就抹掉一个用户设置。
   规则收敛成——**只有 `api_key` / `api_key_env` 这一对语义互斥的键会被删**，其余表单管理的键
   （`api_format` / `reasoning_effort` / `max_tokens` / `context_window`）留空即删，
   文件管理而表单不问的键一律不碰。由 `setup_test.go:TestWriteProvider_PreservesUntouchedTemperature` 钉住。
4. **`SetKeysInSection` / `providerKeyOrder` 是必需的，不是便利**：provider 块一次要落 5-8 个键，
   Go 的 map 无序；不加固定顺序的话同一份输入两次写出字节不同的文件，
   "模板与 schema 同源"那条测试会变成偶发。固定顺序：
   `model → api_base_url → api_key → api_key_env → api_format → reasoning_effort → max_tokens → context_window → temperature`。

**测试卫生的收获**：`knowledge/injector_test.go` 的 `TestInject_SkipPaths` 原本四行输入，
`enabled=false` 那一行与其余三行走的是同一个 early return，删掉它不是"少测一条"，而是**去掉一条永远不可能失败的断言**
（`[codeseek.knowledge] enabled` 被 validate() 顶回 true，那个分支在生产里不可达）。
