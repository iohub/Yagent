# Anthropic API 配置指南

> CodeActor 支持使用 **Anthropic Messages API 原生格式** 进行推理，包括 Claude 模型的 Extended Thinking（扩展思考）功能。本文档说明如何配置 Anthropic 作为 LLM 提供商。

---

## 目录

- [基本配置](#基本配置)
- [字段说明](#字段说明)
- [Extended Thinking（扩展思考）](#extended-thinking扩展思考)
- [配置示例](#配置示例)
  - [基础配置（非流式）](#1-基础配置非流式)
  - [启用 Extended Thinking](#2-启用-extended-thinking)
  - [Agent 级覆盖](#3-agent-级覆盖)
  - [工具级覆盖](#4-工具级覆盖)
- [模型推荐](#模型推荐)
- [验证配置](#验证配置)
- [常见问题](#常见问题)

---

## 基本配置

在 CodeActor 的 `config.toml` 文件中，按以下格式配置 Anthropic 提供商：

```toml
[global.llm.providers.anthropic]
api_format = "anthropic"
model = "claude-sonnet-4-20250514"
temperature = 0.0
max_tokens = 8192
api_base_url = "https://api.anthropic.com/v1"
api_key = "sk-ant-xxxxxxxxxxxx"
```

然后设置默认使用此提供商：

```toml
[global.llm]
use_provider = "anthropic"
```

---

## 字段说明

| 字段 | 必填 | 默认值 | 说明 |
|------|------|--------|------|
| `api_format` | **是** | `"openai"` | **必须设为 `"anthropic"`** 以启用 Anthropic 引擎。留空或设为 `"openai"` 则使用兼容 OpenAI 的引擎。 |
| `model` | **是** | — | Anthropic 模型名称，如 `claude-sonnet-4-20250514`、`claude-3-5-sonnet-20241022` 等。 |
| `api_key` | **是** | — | Anthropic API 密钥，以 `sk-ant-` 开头。 |
| `api_base_url` | **是** | — | API 端点，固定为 `https://api.anthropic.com/v1`（如需代理可自定义）。 |
| `temperature` | 否 | `0.0` | 生成随机性，范围 0.0~1.0。**启用 Extended Thinking 时自动设为 1.0**（Anthropic 强制要求）。 |
| `max_tokens` | 否 | `8192` | 最大输出 token 数。Anthropic 要求此值为必填。 |
| `reasoning_effort` | 否 | `""` | 控制 Extended Thinking 强度，见下文。 |

---

## Extended Thinking（扩展思考）

Extended Thinking 是 Anthropic Claude 模型的推理能力（即"思考"过程），使模型在生成最终回答前进行内部推理。

### 启用方式

设置 `reasoning_effort` 字段即可启用：

```toml
[global.llm.providers.anthropic]
api_format = "anthropic"
model = "claude-sonnet-4-20250514"
reasoning_effort = "high"
```

### 强度等级映射

`reasoning_effort` 的值会映射为 Anthropic 的 `thinking.budget_tokens`：

| 配置值 | Budget Tokens | 适用场景 |
|--------|---------------|----------|
| `"low"` | 1024 | 简单推理，快速响应 |
| `"medium"` | 8192 | 平衡推理深度和响应速度 |
| `"high"` | 24576 | 深度推理，复杂问题 |
| `"max"` | 32000 | 极致推理，最复杂问题 |

> **注意**：启用 Extended Thinking 后，系统会自动确保 `max_tokens > budget_tokens`（Anthropic 的硬性要求），并强制设置 `temperature = 1.0`。

### 工作原理

当启用后：

1. Anthropic API 在返回最终回答前会输出 `thinking` content block
2. 系统会捕获 thinking 内容并存储在 `Response.Reasoning` 字段中
3. **思考过程不会暴露给下游 Agent**，仅用于日志和调试
4. 系统会自动设置 `anthropic-beta: interleaved-thinking-2025-05-14` 请求头，以启用 thinking + tool use 的并行支持

---

## 配置示例

### 1. 基础配置（非流式）

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
```

### 2. 启用 Extended Thinking

```toml
[global.llm]
use_provider = "anthropic"

[global.llm.providers.anthropic]
api_format = "anthropic"
model = "claude-sonnet-4-20250514"
api_base_url = "https://api.anthropic.com/v1"
api_key = "sk-ant-xxxxxxxxxxxx"
reasoning_effort = "high"     # 启用 Extended Thinking
max_tokens = 32000             # 需 > budget_tokens(24576)
```

### 3. Agent 级覆盖

可为不同 Agent 指定不同的 Anthropic 模型：

```toml
[global.llm.providers.anthropic_sonnet]
api_format = "anthropic"
model = "claude-sonnet-4-20250514"
api_base_url = "https://api.anthropic.com/v1"
api_key = "sk-ant-xxxxxxxxxxxx"
reasoning_effort = "high"
max_tokens = 32000

[global.llm.providers.anthropic_haiku]
api_format = "anthropic"
model = "claude-3-5-haiku-20241022"
api_base_url = "https://api.anthropic.com/v1"
api_key = "sk-ant-xxxxxxxxxxxx"
max_tokens = 4096

[agents.llm.director]
use_provider = "anthropic_sonnet"    # Director 用 Sonnet（深度推理）

[agents.llm.coding]
use_provider = "anthropic_haiku"     # Coding 用 Haiku（快速编码）
```

### 4. 工具级覆盖

为 thinking 和 deepthinking 工具指定比主引擎更强大的模型：

```toml
[global.llm.providers.anthropic_sonnet]
api_format = "anthropic"
model = "claude-sonnet-4-20250514"
api_base_url = "https://api.anthropic.com/v1"
api_key = "sk-ant-xxxxxxxxxxxx"
reasoning_effort = "high"
max_tokens = 32000

[tools.llm.thinking]
use_provider = "anthropic_sonnet"

[tools.llm.deepthinking]
use_provider = "anthropic_sonnet"
```

---

## 模型推荐

以下是最适合编码场景的 Anthropic 模型：

| 模型 | 特点 | 推荐场景 |
|------|------|----------|
| `claude-sonnet-4-20250514` | 最新旗舰，编码/推理最强 | Director、DeepThinking（推荐） |
| `claude-3-5-sonnet-20241022` | 上一代旗舰，稳定可靠 | 通用 Agent |
| `claude-3-5-haiku-20241022` | 轻量快速，成本低 | Coding、Repo、Chat（快速任务） |

---

## 验证配置

### 配置检查

修改配置后，启动 CodeActor 时系统会自动验证配置。如果配置正确，启动日志会显示：

```
Creating new LLM client model=claude-sonnet-4-20250514 api_base_url=https://api.anthropic.com/v1
Creating engine for provider provider=anthropic model=claude-sonnet-4-20250514
```

### API 密钥验证

确保 `api_key` 以 `sk-ant-` 开头。如需使用环境变量，可配置：

```toml
api_key = "sk-ant-${ANTHROPIC_API_KEY}"  # 从环境变量读取
```

### 常见错误

| 错误 | 原因 | 解决 |
|------|------|------|
| `anthropic API error (status 401): authentication_error` | API 密钥无效 | 检查 `api_key` 是否正确 |
| `anthropic API error (status 400): ... budget_tokens` | `max_tokens ≤ budget_tokens` | 增大 `max_tokens` 或降低 `reasoning_effort` |
| `anthropic API error (status 529): overloaded_error` | Anthropic 服务过载 | 系统会自动重试（指数退避） |
| 使用 OpenAI 引擎发送请求到 Anthropic | `api_format` 未设置 | 添加 `api_format = "anthropic"` |

---

## 工作原理（实现说明）

CodeActor 的 Anthropic 支持通过以下机制实现：

1. **引擎选择**：通过 `ProviderConfig.ApiFormat = "anthropic"` 触发 `NewAnthropicEngine()` 的创建
2. **请求格式转换**：将内部的 `Message`（OpenAI 兼容格式）转换为 Anthropic Messages API 格式（content blocks、system 顶层字段、tool_use/tool_result 结构）
3. **流式 SSE 解析**：使用 Go 标准库 `bufio.Scanner` 逐行解析 Server-Sent Events，支持 `message_start`、`content_block_start/delta/stop`、`message_delta`、`message_stop` 事件
4. **Extended Thinking**：`thinking_delta` 事件流量被收集到 `Reasoning` 字段，不暴露给下游 Agent
5. **重试与容错**：429（限流）、5xx（服务端错误）、网络超时均自动重试，使用指数退避（10s→20s→40s→80s→160s）

> **注意**：此实现使用 Go 标准库 `net/http` 直接调用 Anthropic Messages API，**无需引入 Anthropic 官方 SDK** 作为外部依赖。
