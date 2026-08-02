# 🎻 CodeActor — 自进化的多智能体 AI 编程引擎

> **不是 Copilot，而是一支能理解、导航、演进你代码库的自主智能体团队。**

<p align="center">
  <a href="https://youtu.be/hFsy0VnW-3U">
    <img src="https://img.youtube.com/vi/hFsy0VnW-3U/maxresdefault.jpg" alt="观看演示视频" width="800">
  </a>
  <br>
  <em>▶️ 点击上方图片观看演示视频（YouTube）</em>
</p>

---

## 💡 为什么是 CodeActor？

现有 AI 编程工具共享一个根本缺陷：**它们把代码当作文本，而非结构**。

| 传统工具 | CodeActor |
|----------|-----------|
| 基于文本模式匹配 | 🧠 基于 AST + 调用图 + 语义向量的**结构化代码理解** |
| 单智能体，单打独斗 | 🤖 **中枢-辐条多智能体**：Director 统一指挥，六大脑各司其职 |
| 能力固定，无法进化 | 🧬 **Meta-Agent**：运行时自动设计并注册新 Agent，越用越强 |
| 只能搜关键词 | 🔍 自然语言语义搜索——「找到认证逻辑的实现」 |
| 不了解项目历史 | 📚 **Git Commit Learning**：自动学习提交历史，注入上下文 |

---

## 🤖 智能体团队

| Agent | 角色 | 核心能力 |
|-------|------|----------|
| 🎼 **Director** | 指挥家 | 任务分解、动态规划、代理委派、结果评审 |
| 🔬 **Repo-Agent** | 代码考古学家 | AST 解析、语义搜索、调用图、代码骨架 |
| ✏️ **Coding-Agent** | 主程工程师 | 22+ 工具、自主编码、自我修正 |
| 🌐 **Browser-Agent** | 网络研究员 | 无头 Chrome、网页导航、数据提取 |
| 🔧 **DevOps-Agent** | 运维工程师 | Shell 执行、环境诊断、进程管理 |
| 💬 **Chat-Agent** | 技术顾问 | 通用问答、技术解释 |
| 🧬 **Meta-Agent** | Agent 工厂 | 运行时设计并注册新 Agent |

---

## 🏗️ 核心架构

```
用户交互层 (TUI / HTTP+WebSocket)
            │
     🎼 Director（指挥家）
       任务分解 · 动态规划 · 结果评审
            │
   ┌────────┼────────┬────────┬────────┬────────┐
   │        │        │        │        │        │
🔬Repo   ✏️Coding  💬Chat   🔧DevOps 🌐Browser 🧬Meta
代码智能  代码编辑  通用对话  运维执行  网页调研  自进化
(Rust)   (22工具)          (Shell)  (无头Chrome) (Agent工厂)
```

> [完整架构文档 →](docs/ARCHITECTURE.md)

---

## ⚡ 四大核心差异

### 🧠 1. Rust 驱动的深度代码智能

不是简单的正则搜索。CodeActor 的 **Repo-Agent** 由 Rust 引擎驱动，集成 Tree-sitter AST 解析、LanceDB 向量嵌入和 Petgraph 调用图分析。它能像资深工程师一样理解代码——跨文件影响分析、环路检测、语义搜索。

- **7 种语言 AST**：Rust · Python · JavaScript · TypeScript · Java · C++ · Go
- **语义搜索**：按代码*含义*搜索，而非关键词
- **调用图分析**：实时追踪「谁调用了这个函数」「改动会影响哪些模块」
- **自动索引**：文件变更自动感知，20 秒增量更新

### 🧬 2. Meta-Agent：运行时的自我进化

这是 CodeActor 最独特的能力。当 Director 遇到内置 Agent 无法胜任的任务时，**Meta-Agent** 会：

1. 🎨 **设计**——自动生成新 Agent 的系统提示词和工具组合
2. ⚡ **执行**——立即运行新 Agent 完成任务
3. 🔧 **注册**——将其永久注册为可用工具，供后续调用

> *例如：自动创建 `delegate_security_auditor` 进行全代码库安全审计，或 `delegate_performance_profiler` 分析性能瓶颈。*

### 🌐 3. Browser-Agent：让 AI 替你上网

内置无头 Chrome 浏览器（go-rod），自主导航网页、提取文档、填写表单。当本地上下文不足时，Director 自动委派网络调研任务。

> *「查找最新的 FastAPI 中间件文档，总结 CORS 配置」——全程无需离开终端。*

### 📚 4. Git Commit Learning：项目记忆

自动拉取最近的 Git 提交 → LLM 生成结构化摘要 → LanceDB 向量存储 → 用户提问时语义匹配，自动注入相关历史上下文。AI 始终了解项目的最新演进。

---

### 🔬 5. 混合检索 + 代码图扩展：从"搜到"到"理解上下文"

> **传统代码搜索只告诉你「哪里匹配了关键词」。CodeActor 不仅找到代码，还自动分析它周围的结构世界。**

#### 🎯 三阶段级联检索 Pipeline

```
用户查询
    │
    ├─→ Stage 1: 混合检索（双通道高召回）
    │   ├── 🧠 稠密通道：LanceDB 向量搜索（Qwen3-Embedding-4B，2560维语义嵌入）
    │   └── 🔤 稀疏通道：Tantivy BM25 全文搜索（自定义 CodeTokenizer 识别 snake_case/CamelCase）
    │   └── 🔗 RRF 融合：Reciprocal Rank Fusion 合并双通道排名
    │
    ├─→ Stage 2: 代码图扩展（结构上下文注入）
    │   └── PetCodeGraph BFS 遍历：从种子函数出发，自动获取调用者/被调者链
    │   └── 跨文件上下文：将孤立代码块还原到其架构位置
    │
    └─→ Stage 3: Cross-Encoder 重排（精排提纯）
        └── 可选 Reranker API 对候选结果做 Query-Document 交叉编码精排
```

#### 为什么这很重要？

**传统向量搜索的局限**：纯向量搜索把代码块当孤岛，只计算语义相似度，却不理解它和谁一起工作、被谁调用、调用了什么。

**CodeActor 的突破**：混合检索 + 代码图扩展 = **从「搜到」到「理解」的质变**。

| 维度 | 纯向量搜索 | CodeActor 混合检索 + 图扩展 |
|------|-----------|--------------------------|
| 查全率 | ❌ 语义近但关键词不同 → 可能漏掉 | ✅ BM25 + Vector 双通道互补，覆盖语义 + 精确匹配 |
| 精确度 | ❌ 短文本/噪音常误中 | ✅ RRF 融合 + 短文本惩罚 + Cross-Encoder 精排三重过滤 |
| 上下文 | ❌ 返回孤立代码块，看不到调用关系 | ✅ PetCodeGraph 自动展开调用链，还原架构上下文 |
| 代码感知 | ❌ 通用 Tokenizer 不懂代码命名 | ✅ 自定义 CodeTokenizer 专为 snake_case/CamelCase 设计 |
| 鲁棒性 | ❌ 单点故障 | ✅ 三重降级：BM25 失败→纯向量，Reranker 失败→RRF，单通道→另一通道 |

---

## 🚀 快速开始

### 下载预编译包（推荐）

从 [GitHub Releases 页面](https://github.com/iohub/codeactor-agent/releases) 下载最新的 all-in-one 二进制包。包内已集成 **codeseek 代码智能引擎**（Rust）、**fzf**（模糊搜索）和 **ripgrep**（正则搜索）——所有依赖一应俱全。解压后直接运行 `./codeactor`，零依赖、零配置，开箱即用。

### 前置要求（从源码编译）
- Go 1.24+
- `ripgrep`（全文正则搜索）

### 从源码编译

```bash
git clone https://github.com/iohub/codeactor-agent.git
cd codeactor-agent
go build -o codeactor .
```

### 配置

创建 `~/.codeactor/config/config.toml`：

```toml
[global.llm]
use_provider = "siliconflow"

[global.llm.providers.siliconflow]
model = "deepseek-ai/DeepSeek-V3.2"
api_base_url = "https://api.siliconflow.cn/v1"
api_key = "your-api-key"
temperature = 0.0
max_tokens = 23000
```

### 运行

```bash
# TUI 模式
./codeactor tui

# 指定任务文件
./codeactor tui --taskfile TASK.md

# HTTP 服务器模式（默认 :9080）
./codeactor http
```

---

## 📖 文档

| 文档 | 说明 |
|------|------|
| [ARCHITECTURE.md](docs/ARCHITECTURE.md) | 系统架构、模块设计、数据流 |
| [Agent_Design.md](docs/Agent_Design.md) | 多智能体设计理念 |
| [Agent_Reference.md](docs/Agent_Reference.md) | API 参考与配置指南 |
| [Browser_Agent_Design.md](docs/Browser_Agent_Design.md) | 浏览器智能体架构 |

---

## 🤝 社区与贡献

我们欢迎任何形式的贡献——Bug 报告、功能建议、文档完善、代码贡献。

- 🐛 [提交 Issue](https://github.com/iohub/codeactor-agent/issues)
- 🔀 [提交 Pull Request](https://github.com/iohub/codeactor-agent/pulls)
- 💬 [参与讨论](https://github.com/iohub/codeactor-agent/discussions)

---

## 📄 许可证

[Apache License 2.0](LICENSE)
