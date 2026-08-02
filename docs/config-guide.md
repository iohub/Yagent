# CodeActor 配置指南

> **定位说明**：本文档是面向用户的**完整配置参考手册**，涵盖所有配置段、优先级机制、故障转移、场景示例等。专项主题请参考：
> - [Anthropic API 配置](./anthropic-config.md) — Claude 模型接入与 Extended Thinking
> - [LLM 故障转移配置](./llm-fallback-config.md) — 多 Provider 降级策略
> - [上下文压缩配置](./context-compression-config.md) — 长对话上下文管理

---

## 1. 配置文件概述

### 1.1 三层配置架构

CodeActor 支持**三层覆盖**的配置文件加载机制，优先级从高到低：

| 优先级 | 路径 | 用途 |
|:------:|------|------|
| **高** | `~/.codeactor/config/config.toml` | 用户级配置，覆盖全局默认值 |
| **中** | `config/config.toml` | 项目级配置，适用于当前工作空间 |
| **低** | `internal/config/default_config.toml` | 内置默认配置，应用层兜底 |

**加载规则**：高层级配置会覆盖低层级的同名配置项。如果某个配置段完全缺失，应用层会通过 `validate()` 自动填充所有字段的默认值。

### 1.2 配置文件路径解析

启动时，CodeActor 按以下顺序确定配置文件路径：

1. **命令行 `--config` / `-c`**：优先使用指定路径（不自动创建文件）
   ```bash
   codeactor --config /path/to/my-config.toml
   ```
2. **用户级默认**：`$HOME/.codeactor/config/config.toml`（不存在时自动生成）
3. **项目级兜底**：`config/config.toml`（不存在时自动生成）

### 1.3 首次运行自动生成

如果配置文件不存在，CodeActor 会自动调用 `EnsureConfigExists()` 从内置模板（`internal/config/default_config.toml`）生成默认配置文件，采用原子写入方式（先写 `.tmp` 再 rename）避免损坏。

### 1.4 热重载支持

配置文件支持**热重载**。修改配置文件后，系统通过 `fsnotify` 自动检测变更，经 500ms 防抖后重新加载配置并通知所有订阅者。

热重载覆盖以下配置段：`global`、`agents`、`tools`、`app`、`agent`、`llm`、`browser`、`keywords`、`task_timeout`。

> **注意**：热重载行为以实际代码为准，部分内部状态变更可能不会自动生效。

---

## 2. 快速开始

### 2.1 最小可运行配置

只需定义一个 Provider 并指定默认使用它：

```toml
[global.llm]
use_provider = "deepseek_v4_pro"

[global.llm.providers.deepseek_v4_pro]
model = "deepseek-v4-pro"
temperature = 0.0
max_tokens = 3000
api_base_url = "https://api.deepseek.com/v1"
api_key = "sk-your-key"
```

保存文件后启动 CodeActor 即可。

### 2.2 切换默认 Provider

修改 `[global.llm]` 中的 `use_provider` 即可切换全局默认：

```toml
[global.llm]
use_provider = "local"   # 切换到本地模型
```

---

## 3. 配置总览表

| 配置段 | 作用 | 是否必需 |
|--------|------|:--------:|
| `[global.llm]` + `[global.llm.providers.*]` | 定义 LLM Provider（模型、API 地址、密钥等） | **是**（至少 1 个） |
| `[global.llm.use_provider]` | 指定默认使用的 Provider | **是** |
| `[agents.llm.*]` | Agent 级 LLM 覆盖 | 否 |
| `[tools.llm.*]` | 工具级 LLM 覆盖 | 否 |
| `[app]` | 应用设置（流式输出等） | 否 |
| `[agent]` | Agent 行为配置（步数限制、YOLO 模式等） | 否 |
| `[llm]` | LLM 推理兜底配置（超时、重试、熔断、故障转移） | 否 |
| `[codeseek]` + `[codeseek.knowledge]` | CodeSeek MCP 代码分析引擎 | 否 |
| `[browser]` | 浏览器配置（视口、超时、并发等） | 否 |
| `[keywords]` | 关键词词典配置 | 否 |
| `[git_checkpoint]` | Git Checkpoint 机制 | 否 |
| `[enhanced_commander]` | 增强型 Commander（分布式认知架构） | 否 |
| `[memory_jsonl]` | Memory JSONL 实时写入 | 否 |
| `[tui]` | TUI 快捷键配置 | 否 |
| `task_timeout` | 全局任务超时（根级字段） | 否 |

---

## 4. LLM 配置详解

### 4.1 Provider 字段表

所有 Provider 配置均位于 `[global.llm.providers.<name>]` 段：

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `model` | string | — | 模型名称（必填，如 `qwen3-coder-plus`、`deepseek-v4-pro`） |
| `temperature` | float | `0.0` | 生成随机性，范围 0.0~1.0 |
| `max_tokens` | int | — | 最大输出 token 数 |
| `api_base_url` | string | — | API 端点地址（必填） |
| `api_key` | string | — | API 密钥（必填） |
| `api_format` | string | `"openai"` | API 格式：`"openai"`（默认）或 `"anthropic"` |
| `reasoning_effort` | string | `""` | 推理强度（DeepSeek thinking 模式：`"low"`/`"medium"`/`"high"`/`"max"`） |
| `aws_region` | string | `""` | AWS Bedrock 区域 |
| `aws_profile` | string | `""` | AWS 配置文件名 |
| `model_provider` | string | `""` | Bedrock 显式 Provider（如 `"anthropic"`、`"amazon"`） |
| `fallback_providers` | array | `[]` | 故障转移备选 Provider 列表 |

### 4.2 优先级链

LLM Provider 解析遵循以下优先级（从高到低）：

```
tools.llm.<tool>.use_provider      ← 工具级覆盖（最高）
    ↓
tools.llm.use_provider             ← 工具默认
    ↓
agents.llm.<agent>.use_provider    ← Agent 级覆盖
    ↓
agents.llm.use_provider            ← Agent 默认
    ↓
global.llm.use_provider            ← 全局默认（最低）
```

### 4.3 Agent 级覆盖列表

| Agent | 配置段 | 默认 Provider |
|-------|--------|:-------------:|
| Director | `[agents.llm.director]` | `deepseek_v4_pro` |
| Coding | `[agents.llm.coding]` | `local` |
| Repo | `[agents.llm.repo]` | `local` |
| Browser | `[agents.llm.browser]` | `local` |
| Chat | `[agents.llm.chat]` | `local` |
| Meta | `[agents.llm.meta]` | `local` |
| DevOps | `[agents.llm.devops]` | `local` |

示例：覆盖 Coding Agent 使用 DeepSeek：

```toml
[agents.llm.coding]
use_provider = "deepseek_v4_pro"
```

### 4.4 工具级覆盖列表

| 工具 | 配置段 | 默认 Provider |
|------|--------|:-------------:|
| thinking | `[tools.llm.thinking]` | `deepseek_v4_pro` |
| deepthinking | `[tools.llm.deepthinking]` | `deepseek_v4_pro` |

### 4.5 故障转移 fallback_providers

每个 Provider 可声明备选列表，当主 Provider 调用失败（429/5xx/超时）时自动切换。详见 [LLM 故障转移配置指南](./llm-fallback-config.md)。

#### 格式一：简写（按声明顺序尝试）

```toml
fallback_providers = ["deepseek_v4_pro", "local"]
```

#### 格式二：完整（按权重排序，weight 越大越优先）

```toml
fallback_providers = [
    { provider = "deepseek_v4_pro", weight = 10 },
    { provider = "local",           weight = 5  },
]
```

### 4.6 api_format="anthropic" 与 reasoning_effort

- `api_format = "anthropic"`：启用 Anthropic Messages API 原生格式，支持 Claude Extended Thinking
- `reasoning_effort`：控制推理深度，值映射为 `thinking.budget_tokens`（`"low"`=1024、`"medium"`=8192、`"high"`=24576、`"max"`=32000）

详细说明参见 [Anthropic API 配置](./anthropic-config.md)。

---

## 5. 各配置段详解

### 5.1 `[agent]` — Agent 行为配置

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `yolo_mode` | bool | `false` | YOLO 模式：自动批准所有危险操作 |
| `full_yolo_mode` | bool | `false` | 完全自治模式（移除 `ask_user_for_help`，Agent 独立决策） |
| `director_max_steps` | int | `100` | Director 最大步数 |
| `coding_max_steps` | int | `150` | Coding Agent 最大步数 |
| `repo_max_steps` | int | `50` | Repo Agent 最大步数 |
| `chat_max_steps` | int | `50` | Chat Agent 最大步数 |
| `devops_max_steps` | int | `50` | DevOps Agent 最大步数 |
| `browser_max_steps` | int | `200` | Browser Agent 最大步数 |
| `meta_max_steps` | int | `50` | Meta Agent 最大步数 |
| `meta_retry_count` | int | `5` | Meta Agent 重试次数 |
| `lang` | string | `""` | 对话语言（空=默认） |

> **⚠️ YOLO 模式警告**：`yolo_mode = true` 将跳过所有危险操作的用户确认。`full_yolo_mode = true` 将进一步移除 `ask_user_for_help` 工具，Agent 完全自主决策。仅在受控环境中使用。

命令行等效选项：`--yolo` / `--full-yolo`。

### 5.2 `[llm]` — LLM 推理兜底配置

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `timeout` | duration | `"5m"` | 单次 LLM 调用超时，`"0s"` = 不启用 |
| `max_retries` | int | `5` | 底层引擎重试次数 |
| `step_retries` | int | `0` | 步骤重试次数（executor/director/meta），`0` = 不重试 |
| `circuit_breaker_threshold` | int | `0` | 熔断阈值（连续失败次数），`0` = 不启用 |
| `circuit_breaker_reset_timeout` | duration | `"0s"` | 熔断恢复时间（默认 `"0s"`；仅在 `circuit_breaker_threshold > 0` 时生效，此时自动填充为 `60s`） |
| `enable_fallback` | bool | `false` | 启用 Provider 级故障转移 |
| `fallback_max_retries` | int | 使用 `max_retries` 的值 | 每个备选 Provider 的内部重试次数 |

详细说明参见 [LLM 故障转移配置指南](./llm-fallback-config.md)。

### 5.3 `[app]` — 应用设置

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `enable_streaming` | bool | `true` | 启用流式输出 |

### 5.4 `[codeseek]` + `[codeseek.knowledge]` — CodeSeek MCP 引擎

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `binary_path` | string | `""` | codeseek 二进制路径（空 = 不启用 MCP） |
| `mcp_args` | array | `["serve", "--mcp"]` | MCP 启动参数 |
| `request_timeout` | int | `30` | 请求超时秒数 |

**`[codeseek.knowledge]` 子段：**

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `enabled` | bool | `true` | 是否启用知识管理功能 |
| `injection_max_tokens` | int | `1000` | 知识注入最大 token 数 |
| `injection_max_entries` | int | `8` | 知识注入最大条目数 |
| `injection_min_score` | float | `0.3` | 知识检索最低得分阈值 |
| `injection_rerank` | bool | `true` | 是否请求 Cross-Encoder 精排 |

### 5.5 `[browser]` — 浏览器配置

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `headless` | bool | `true` | 无头模式 |
| `browser_path` | string | `""` | 浏览器可执行文件路径（空 = 自动查找） |
| `user_data_dir` | string | `""` | 用户数据目录（空 = 临时目录） |
| `viewport_width` | int | `1280` | 视口宽度 |
| `viewport_height` | int | `720` | 视口高度 |
| `timeout_seconds` | int | `120` | 单页操作超时（秒） |
| `task_timeout_seconds` | int | `300` | 单个浏览器任务超时（秒） |
| `max_concurrent_pages` | int | `4` | 最大并发页面数 |
| `auto_launch` | bool | `true` | 是否自动启动浏览器 |
| `idle_timeout` | string | `"5m"` | 空闲超时 |
| `allow_no_sandbox` | bool | `false` | 允许 `--no-sandbox` 启动（容器环境需要） |
| `enable_browser_agent` | bool | `true` | 总开关：禁用后 Browser-Agent 不可用 |
| `allowed_domains` | array | `[]` | 允许访问的域名白名单（空 = 不限制） |
| `blocked_domains` | array | `[]` | 禁止访问的域名黑名单 |
| `extra_args` | array | `[]` | 附加浏览器启动参数 |

### 5.6 `[keywords]` — 关键词词典配置

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `default_path` | string | `~/.codeactor/keywords.txt` | 默认关键词文件路径 |
| `hot_reload` | bool | `false` | 是否启用热重载 |
| `disable_completion` | bool | `false` | 禁用关键词自动补全 |

**子表 `[[keywords.dict]]`：**

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `name` | string | — | 词典名称 |
| `files` | array | `[]` | 关键词文件路径列表（空 = 使用 DefaultPath） |
| `type` | string | `"prefix"` | 词典类型：`"prefix"`（前缀匹配/补全）或 `"exact"`（精确匹配/扫描） |
| `builtin_type` | string | `"default"` | 内置类型：`"default"` 或 `"none"` |

### 5.7 `[git_checkpoint]` — Git Checkpoint 机制

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `enabled` | bool | `true` | 总开关 |
| `auto_checkpoint` | bool | `false` | **DEPRECATED**：已废弃，checkpoint 由 agent 通过 `git_checkpoint_create` 工具创建 |
| `checkpoint_interval` | int | `0` | **DEPRECATED**：已废弃 |
| `max_checkpoints` | int | `50` | 最大保留的 checkpoint 数量 |
| `squash_on_exit` | bool | `true` | Agent 退出时 squash 合并到主分支 |
| `generate_commit_message` | bool | `true` | 使用 LLM 生成 commit 消息 |
| `agent_branch_prefix` | string | `"agent"` | agent 工作分支前缀 |
| `checkpoint_tag_prefix` | string | `"checkpoint/coding"` | checkpoint 标签前缀 |
| `stash_dirty_worktree` | bool | `true` | 启动时 stash 未提交的变更 |
| `cleanup_agent_branch` | bool | `true` | 退出时清理 agent 分支 |
| `cleanup_checkpoint_tags` | bool | `true` | 退出时清理 checkpoint 标签 |
| `auto_merge_on_exit` | bool | `false` | 退出时自动 squash-merge（`false` 则保留分支供人工审查合并） |

### 5.8 `[enhanced_commander]` — 增强型 Commander

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `enable` | bool | `false` | 总开关 |
| `enable_result_compression` | bool | `false` | 启用结果压缩 |
| `compression_threshold` | int | `4096` | 压缩阈值（字节） |
| `summary_max_length` | int | `2048` | 摘要最大长度（字符） |
| `max_delegation_depth` | int | `3` | 最大委派深度 |

### 5.9 `[memory_jsonl]` — Memory JSONL 实时写入

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `enable` | bool | `false` | 是否在 agent 执行时实时写入 memory JSONL 文件 |
| `output_dir` | string | `""` | 输出目录（空 = 默认 `~/.codeactor/data/memory_jsonl/{projectID}/`） |

### 5.10 `[tui]` — TUI 快捷键配置

**编辑模式快捷键：**

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `submit_task` | string | `"alt+s"` | 提交任务 |
| `command_mode` | string | `"ctrl+e"` | 进入命令模式 |
| `toggle_help` | string | `"ctrl+h"` | 切换帮助 |
| `toggle_timeline` | string | `"ctrl+l"` | 切换全屏时间线 |
| `page_down` | string | `"ctrl+f"` | 向下翻页 |
| `page_up` | string | `"ctrl+b"` | 向上翻页 |
| `quit` | string | `"ctrl+c"` | 退出 |
| `switch_model` | string | `"alt+m"` | 切换模型 |

**命令模式快捷键：**

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `scroll_down` | string | `"j"` | 向下滚动一行 |
| `scroll_up` | string | `"k"` | 向上滚动一行 |
| `page_down` | string | `"f"` | 向下翻页 |
| `page_up` | string | `"b"` | 向上翻页 |
| `edit_mode` | string | `"i"` | 进入编辑模式 |
| `toggle_help` | string | `"?"` | 显示帮助 |
| `toggle_token_panel` | string | `"alt+t"` | 切换 Token 面板 |
| `switch_model` | string | `"alt+m"` | 切换模型 |
| `quit` | string | `"ctrl+c"` | 退出 |

### 5.11 `task_timeout` — 根级字段

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `task_timeout` | duration | `"0s"` | 全局任务超时（`"0s"` = 不启用，正数如 `"30m"` 表示超时时间） |

---

## 6. 场景示例

### 6.1 日常开发：多 Provider + 故障转移

```toml
[global.llm]
use_provider = "aliyun"

[llm]
enable_fallback = true
fallback_max_retries = 2

[global.llm.providers.aliyun]
model = "qwen3-coder-plus"
temperature = 0.0
max_tokens = 28000
api_base_url = "https://dashscope.aliyuncs.com/compatible-mode/v1"
api_key = "sk-your-key"
# 主 Provider 限流时，先尝试 deepseek，再切 local
fallback_providers = ["deepseek_v4_pro", "local"]

[global.llm.providers.deepseek_v4_pro]
model = "deepseek-v4-pro"
temperature = 0.0
max_tokens = 3000
api_base_url = "https://api.deepseek.com/v1"
api_key = "sk-your-key"
reasoning_effort = "max"

[global.llm.providers.local]
model = "Qwen3-30B-A3B"
temperature = 0.0
max_tokens = 3000
api_base_url = "http://127.0.0.1:8000"
api_key = "dummy-key"

# Director 用高质量模型，Coding 用低成本本地模型
[agents.llm.director]
use_provider = "aliyun"

[agents.llm.coding]
use_provider = "local"
```

### 6.2 Anthropic 接入

```toml
[global.llm]
use_provider = "anthropic"

[global.llm.providers.anthropic]
api_format = "anthropic"
model = "claude-sonnet-4-20250514"
temperature = 0.0
max_tokens = 16000
api_base_url = "https://api.anthropic.com/v1"
api_key = "sk-ant-xxxxxxxxxxxx"
reasoning_effort = "high"    # 启用 Extended Thinking
```

详细说明参见 [Anthropic API 配置](./anthropic-config.md)。

### 6.3 本地模型（vLLM/Ollama 兼容端点）

```toml
[global.llm]
use_provider = "local"

[global.llm.providers.local]
model = "Qwen3-30B-A3B"
temperature = 0.0
max_tokens = 3000
api_base_url = "http://127.0.0.1:8000"   # vLLM 服务端点
api_key = "dummy-key"                      # 本地模型通常不需要 API Key
```

> **注意**：确保本地模型服务已启动并监听指定端口，且 API 兼容 OpenAI 格式。

---

## 7. 无效/已废弃配置段

以下配置段在 `config/config.toml` 中已删除，**代码不读取这些段**：

| 配置段 | 原因 |
|--------|------|
| `[messaging]` | 消息总线使用硬编码默认值创建（`BufferSize=1000` 等），配置项未映射 |
| `[codexray]`（及子段） | 已删除，代码分析由 `[codeseek]` 提供 |
| `[director]` | 代码中无对应结构体定义，字段未被读取 |
| `[tools_registry]` | 占位符配置（"Phase 2 使用"），代码中零引用 |

---

## 8. 常见问题 FAQ

### Q: 配置不生效怎么办？

1. **检查加载路径**：运行 `codeactor --config /path/to/config.toml` 确认使用了正确的配置文件
2. **检查优先级**：用户级（`~/.codeactor/config/config.toml`）覆盖项目级（`config/config.toml`），确认修改的是正确的文件
3. **检查语法**：确保 TOML 格式正确，可使用 `python3 -c "import tomllib; tomllib.load(open('config.toml','rb'))"` 验证
4. **查看日志**：启动时打印 `Loading configuration` 日志确认加载路径

### Q: provider 未找到

错误信息形如 `provider 'xxx' not found in configuration`。检查：
- `[global.llm.providers.xxx]` 段是否存在
- `use_provider` 的值与 provider 名称完全一致（区分大小写）

### Q: 如何切换模型？

- **全局切换**：修改 `[global.llm].use_provider`
- **按 Agent 切换**：修改 `[agents.llm.<agent>].use_provider`
- **按工具切换**：修改 `[tools.llm.<tool>].use_provider`

### Q: 配置文件损坏时的恢复？

删除配置文件后重新启动 CodeActor，系统会自动从内置模板生成默认配置：

```bash
rm ~/.codeactor/config/config.toml
codeactor
```

### Q: 如何禁用某个 Agent？

启动时通过 `--disable-agents` 参数：

```bash
codeactor --disable-agents browser,devops
```

### Q: 热重载不生效？

检查：
1. 配置文件路径是否为 `~/.codeactor/config/config.toml` 或项目级 `config/config.toml`
2. 使用 `--config` 指定的自定义路径不支持热重载
3. 编辑器可能使用 swap-and-replace 模式，确保文件确实发生了写入事件
