# CodeActor Agent

基于 Go 语言开发的 **Hub-and-Spoke（中枢-辐条）多智能体架构** AI 自主编程助手。

CodeActor Agent 协调多个专用智能体——指挥家（Conductor）、仓库分析员（Repo-Analyst）、编码工程师（Coding-Engineer）和对话助手（Chat-Assistant）——自主完成复杂的软件工程任务，具备自我纠错能力。

## 特性

- **多智能体架构** — 中央 Conductor 将任务委派给专用子智能体（仓库分析、代码编辑、通用对话）
- **丰富的工具系统** — 14 个内置工具，涵盖文件操作、代码搜索、语义分析、Shell 执行和认知反思
- **自我修正** — `thinking` 工具使 Agent 能够在出错时分析原因并恢复，避免盲目重试
- **双交互模式** — TUI 终端界面用于本地使用；HTTP + WebSocket 服务用于 IDE/Web 集成
- **多提供商 LLM 支持** — 小米 MiMo、阿里 Qwen、DeepSeek、Mistral、AWS Bedrock 等，通过 OpenAI 兼容 API
- **流式输出** — AI 回复、工具调用和结果实时流式传输
- **对话记忆** — 完整对话上下文（含工具调用历史），跨会话持久化
- **仓库分析** — 自动代码库调查，包含目录树、调用图和语义搜索

## 快速开始

### 环境要求

- Go 1.23+
- `ripgrep` (`rg`) — 全文正则搜索
- `fzf` — 模糊文件搜索（可选）
- 运行中的 `codeactor-codebase` 服务（或设置 `CODEBASE_URL`）

### 安装

```bash
git clone https://github.com/your-org/codeactor-agent.git
cd codeactor-agent
go build -o codeactor .
```

### 配置

创建 `$HOME/.codeactor/config/config.toml`：

```toml
[http]
server_port = 9080

[llm]
use_provider = "siliconflow"

[llm.providers.siliconflow]
model = "deepseek-ai/DeepSeek-V3.2"
temperature = 0.0
max_tokens = 23000
api_base_url = "https://api.siliconflow.cn/v1"
api_key = "your-api-key-here"

[app]
enable_streaming = true

[agent]
conductor_max_steps = 30
coding_max_steps = 50
repo_max_steps = 30
lang = "Chinese"
```

### 运行

**TUI 模式**（终端界面）：
```bash
./codeactor tui
# 或携带任务文件：
./codeactor tui --taskfile TASK.md
```

**HTTP 服务模式**（API + WebSocket）：
```bash
./codeactor http
# 服务启动在 http://localhost:9080
```

## 架构

<p align="center">
  <img src="docs/architecture.svg" alt="CodeActor Agent 架构图" width="900">
</p>

[完整架构文档 →](docs/ARCHITECTURE.md)

## API 概览

### REST 接口

| 方法 | 路径 | 说明 |
|--------|------|------|
| `POST` | `/api/start_task` | 启动或恢复编码任务 |
| `GET` | `/api/task_status?task_id=` | 查询任务状态和记忆 |
| `POST` | `/api/cancel_task` | 取消运行中的任务 |
| `GET` | `/api/history` | 历史任务列表 |
| `POST` | `/api/load_task` | 从持久化恢复任务 |
| `GET` | `/api/memory?task_id=` | 获取对话记忆 |
| `DELETE` | `/api/memory?task_id=` | 清空对话记忆 |

### WebSocket

连接到 `ws://localhost:9080/ws`

| 客户端事件 | 说明 |
|-------------|------|
| `start_task` | 创建并启动新编码任务 |
| `chat_message` | 发送后续对话消息 |
| `get_memory` | 获取对话记忆 |
| `clear_memory` | 清空对话记忆 |

详细 API 文档见 [docs/Agent_Reference.md](docs/Agent_Reference.md)。

## 支持的 LLM 提供商

| 提供商 | 配置键 | 模型示例 |
|----------|-----------|------|
| 小米 MiMo | `xiaomi` | `mimo-v2-flash` |
| 阿里云百炼 | `aliyun` | `qwen3-coder-plus` |
| 硅基流动 | `siliconflow` | `deepseek-ai/DeepSeek-V3.2` |
| DeepSeek | `deepseek` | `deepseek-ai/DeepSeek-V3` |
| Moonshot | `moonshot` | `moonshotai/Kimi-K2-Instruct` |
| Mistral | `mistral` | `mistralai/devstral-small` |
| 智谱 Z.ai | `zai` | `zai-org/GLM-4.5-Air` |
| OpenRouter | `openrouter` | `qwen3-coder-plus` |
| StreamLake | `streamlake` | 自定义端点 |
| AWS Bedrock | `bedrock` | `us.anthropic.claude-3-7-sonnet-*` |
| 本地 | `local` | 任意 OpenAI 兼容服务 |


## 文档

- [ARCHITECTURE.md](docs/ARCHITECTURE.md) — 系统架构、模块、数据流、协议
- [Agent_Reference.md](docs/Agent_Reference.md) — API 参考和配置指南
- [Agent_Design.md](docs/Agent_Design.md) — 多智能体设计理念

## 许可证

[Apache License 2.0](LICENSE)
