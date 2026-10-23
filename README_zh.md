# 🎻 Yagent — 自进化的多智能体 AI 编程引擎

> **不是 Copilot，而是一支能理解、导航、演进你代码库的自主智能体团队。**

<p align="center">
  <a href="https://youtu.be/hFsy0VnW-3U">
    <img src="https://img.youtube.com/vi/hFsy0VnW-3U/maxresdefault.jpg" alt="观看演示视频" width="800">
  </a>
  <br>
  <em>▶️ 点击上方图片观看演示视频（YouTube）</em>
</p>

<p align="center">English: <a href="README.md">README.md</a> · 中文：<a href="README_zh.md">README_zh.md</a></p>

---

## 💡 为什么是 Yagent？

现有 AI 编程工具共享一个根本缺陷：**它们把代码当作文本，而非结构**；同时它们把上下文当作无限资源，而上下文恰恰是最稀缺的东西。

| 传统工具 | Yagent |
|----------|-----------|
| 基于文本模式匹配 | 🧠 基于 AST + 调用图 + 混合检索的**结构化代码理解** |
| 单智能体，单打独斗 | 🤖 **中枢-辐条多智能体**：Director 统一指挥，六位专家执行 |
| 能力固定，无法进化 | 🧬 **Meta-Agent**：运行时自动设计并注册新 Agent，系统自我演进 |
| 只能搜关键词 | 🔍 自然语言语义搜索——「找到认证逻辑的实现」 |
| 每一步都发送全量工具 schema | 🗂️ **Deferred Tool Schema**：冷工具只给「名字 + 一句话简介」，模型真要用时再去取 schema |
| 长会话静默劣化、结论反复重推 | 🗜️ **三档上下文压缩** + 跨 Agent 便签板，让结论**可寻址**而不是每次重新推导 |
| 一个模型硬扛所有任务 | 🎯 **基准打分的自动路由**：按子任务难度分派 fast / balanced / high |
| 「我改好了，应该能跑」 | ✅ 强制委派、编码前设计闸门、git 快照、活动感知超时、基于 rollout 日志的 `/resume` |

---

## 🤖 智能体团队

| Agent | 定位 | 权限 | 默认 Max Steps |
|-------|------|------|----------------|
| 🎼 **Director** | 指挥家——唯一与你对话的 Agent：分析、分解、委派、评审 | 只读 + 委派 | 100 |
| 🔬 **Repo-Agent** | 代码考古学家 / 审计员：AST、语义搜索、调用图、影响分析 | 只读，可委派 DevOps | 50 |
| ✏️ **Coding-Agent** | 主程工程师：编辑、Shell、设计与反思工具、自我修正 | 完整读写 + Shell | 150 |
| 🌐 **Browser-Agent** | 驱动真实 Chrome 做文档调研、issue 排查、线上页面验证 | 14 个浏览器工具 + 独立工作区（无 Shell、无文件写） | 200 |
| 🔧 **DevOps-Agent** | 运维工程师：Shell 执行、环境诊断、进程管理 | 只读 + Shell | 50 |
| 💬 **Chat-Agent** | 技术顾问：通用问答与技术解释，完全不接触文件系统 | 无 | 50 |
| 🧬 **Meta-Agent** | Agent 工厂：单次 LLM 产出新 Agent 的 JSON 设计并立即执行 | 单次、无工具 | 50 |
| 🎛️ **Custom Agent** | Meta-Agent 的运行时产物（如 `delegate_security_auditor`） | LLM 选择的能力子集 | 15 |

权限不是提示词里的口头约定，而是**派生出来的**：每个 Agent 声明一份**能力画像**（允许的效果位掩码），工具面由中央注册表自动算出。新加工具，它自动出现在所有被授予该能力的 Agent 上；漏了实现，启动校验会直接报错，而不是让工具「静默消失」。

---

## 🏗️ 核心架构

```
        TUI (Bubble Tea v2)  ·  HTTP + WebSocket  ·  --prompt
                              │
                   ┌──────────┴──────────┐
                   │    🎼 Director      │  Planner · Thought & Plan 日志
                   │  （统一指挥）        │  · 熔断器 · 结果评审
                   └──────────┬──────────┘
     ┌────────┬────────┬──────┴──────┬─────────┬─────────┐
   🔬 Repo  ✏️ Coding  💬 Chat    🔧 DevOps  🌐 Browser  🧬 Meta
   只读 +    读写 +     无文件     只读 +      真实        设计 →
   调用图    Shell      访问       Shell       Chrome      注册
     └────────┴────────┴──────┬──────┴─────────┴─────────┘
                              │
        每次委派的结果 ────────┤  📝 变成一张便签（结构化 handoff + 派生索引）
                              │  🪨 或直接落盘为 artifact
                   ┌──────────┴──────────┐
                   │     引擎层           │  🧩 工具注册表——50 个工具、能力画像、
                   │                     │    deferred schema（load_tool）
                   │                     │  🎯 模型路由——fast/balanced/high，
                   │                     │    由 478 模型的基准目录打分选出
                   │                     │  🗜️ 三档压缩 ← 🧠 Thinklink 日志
                   │                     │  📼 Rollout JSONL → /resume
                   └──────────┬──────────┘
                              │  MCP stdio（9 个工具：6 个 curated 桥 + 通用发现）
              🦀 codeseek — Tree-sitter AST · Petgraph 调用图
                 · LanceDB 稠密向量 · Tantivy BM25 · RRF · 交叉编码精排
```

> [完整架构文档 →](docs/ARCHITECTURE.md)

---

## ⚡ 七大核心差异

### 🧠 1. Rust 驱动的深度代码智能

Repo-Agent 背后是 **codeseek**——一个 Rust 引擎（git submodule，随 Go 二进制内嵌分发，经 MCP stdio 调用）。它像资深工程师那样理解代码，而不是做正则匹配。

- **25 种 Tree-sitter 语言 ID**；其中函数/类抽取**与调用图边**经过实测验证的有 7 种：Rust · Python · JS · TS · Go · C/C++ · Java
- **调用图分析**（Petgraph）：谁调用了它、改这里会波及什么、深度受限的图遍历、不看全文也能拿到代码骨架
- **六个 curated 桥接工具**（`semantic_search`、`query_code_skeleton`、`query_code_snippet`、`find_function_caller`、`find_function_callee`、`query_call_graph`）外加**通用 `tools/list` 发现**：`codeseek_init` / `status` / `list` 现在可达，`[[mcp.servers]]` 里加的任意 server 也会以 `mcp__<server>__<tool>` 出现
- **自动索引**：文件监听 2 秒防抖，你边改它边同步
- **优雅降级**：没有 embedding 服务、没有 reranker、或索引还在构建——引擎退化为基于图的名字搜索，Agent 退化为文件工具，**绝不会硬失败**

#### 🎯 三阶段级联检索 Pipeline

```
用户查询
    ├─→ Stage 1 · 混合检索（双通道高召回）
    │     ├── 🧠 稠密通道：LanceDB ANN（默认 Qwen3-Embedding-4B，2560 维，可换模型）
    │     ├── 🔤 稀疏通道：Tantivy BM25 + 自定义 CodeTokenizer（识别 snake_case / CamelCase）
    │     └── 🔗 RRF 融合：Reciprocal Rank Fusion 合并双通道排名
    ├─→ Stage 2 · 代码图扩展（结构上下文注入）
    │     └── PetCodeGraph 从种子符号 BFS 展开调用者/被调者，把孤立代码块放回它的架构位置
    └─→ Stage 3 · Cross-Encoder 精排
          └── 可选 Qwen3-Reranker 对每个 (query, code) 对做全交互打分
```

| 维度 | 纯向量搜索 | Yagent 混合检索 + 图扩展 |
|------|-----------|--------------------------|
| 查全率 | ❌ 语义相近但用词不同 → 漏 | ✅ BM25 + 稠密双通道互补 |
| 精确度 | ❌ 短文本 / 噪音常排到前面 | ✅ RRF + 短代码惩罚 + 交叉编码精排 |
| 上下文 | ❌ 返回孤立代码块，看不到调用关系 | ✅ 自动展开调用链，还原架构位置 |
| 代码感知 | ❌ 通用 tokenizer 不懂代码命名 | ✅ CodeTokenizer 专为代码设计 |
| 鲁棒性 | ❌ 单点故障 | ✅ 三条独立降级路径 |

### 🗂️ 2. Deferred Tool Schema：真正的 Token 经济学

把 50 个工具的完整声明发给模型，**每一步**都要付几千 token 的样板开销，而一个任务真正用到的可能只有十来个。Yagent 给每个工具定了 **Hot / Warm / Cold** 三档：

- **Hot**：每步都带完整 JSON Schema（流程控制工具，以及该 Agent 几乎每步都要用的工具）
- **Cold**：只出现在 system prompt 的 *Deferred tools* 目录里——`名字：一句话简介`
- 模型想用某个冷工具，就调 `load_tool`（一次最多批量取回 8 个）拿到真实参数 schema，然后再调用。若它跳过这一步直接调，系统**不判它幻觉**，而是把完整 schema 回吐一次作为教学兜底
- 每步的**声明预算**（默认 2500 token，`[tools.deferred.budget_tokens]`）会把体积过大的工具自动降级为 Cold；`[tools.deferred.hot] / [cold]` 可以按名字强制调档，不必改代码重编
- `/resume` 会恢复上一轮已取回的 schema，续跑不用重新付一次往返
- 一个开关（`disable = true`）即可整体退回「全部声明每步都发」的旧行为，留作排障与 A/B 对照

### 🗜️ 3. 三档上下文压缩，且省了多少一眼可见

每一步都会对照预算做检查，阈值来自**内嵌的 478 模型目录**（`context × 0.75`；目录未命中才回落到你配置的值）。

1. **第一档 Rule**：按价值优先级截断工具结果（`deepthinking` / `design` 这类认知结果受保护），最近的结果保持完整。
2. **第二档 Emergency**：把 Thought & Plan 块抽进 thinklink 日志，最近 3 条保留原文，其余交给 LLM 摘要，记忆整体重写为 `[system, one user]`。
3. **第三档 Ultimate**：仍超预算时，用思维日志**重建**工作上下文，保留最近的规划块，最后硬截断到预算。

三档都会发事件并写 rollout 记录，TUI 里能直接看到每一档省下了多少：分档节省 token 与百分比、上下文变化趋势曲线、以及「哪些工具被截得最狠」排行榜。侧栏、底部面板、折叠行**共用同一个渲染器和同一套记账口径**，因此同一个标签绝不会显示出两个数字。

### 📝 4. Blackboard 便签交接：结论可以被指认，而不是重推

子 Agent 一交付，结果就落成一张**便签**，带结构化 handoff——`conclusion`、可验证的 `facts[]`（每条都带 `where`：file:line、符号、URL）、`open_questions`、`status`；超长交付物则**落盘为 artifact**，上下文里只留摘要 + ID。

便签索引会注入 Director 的**每一轮**上下文和**每一次委派**的任务前缀，`context_refs` 让 Director 可以直接说「接着 Repo 已经查明的结论干」：

```
delegate_coding(task = "实现这个修复",
                context_refs = ["agent:repo", "artifact:20261014-093012-a1b2c3d4"])
```

索引是**派生态：每轮从磁盘重扫，绝不写进会话记忆**——这正是它能扛住上下文压缩的原因（压缩动的是会话历史里的 tool 消息，旧写法里的 `note:` / `artifact:` 引用就是这么蒸发的）。读回工具三个：`read_note`（handoff 块 + 分页正文）、`read_artifact`（分页全文）、`list_notes`（整块板的目录）。便签板是 **session 级**的，所以 `/resume` 回来接的是同一批结论，而不是把仓库重新探索一遍。

### 🎯 5. 基于公开基准的模型自动路由

你把几个 provider 交给它，它替你排好座次。内嵌模型目录带基准成绩，路由对每个候选打分（`ai_score` 取**候选集合的基准交集**求和，DeepSWE / SWE-Bench Pro / GPQA Diamond 权重 1.5×），再从你自己的模型池里选出三档——最高分、中位数、最低分。若可打分的候选不足三个、或基准交集为空，路由**带着原因自动停用**，而不是瞎猜。

随后由 fast 模型给每个子任务判难度：跑命令看日志 → fast；常规编码、检索、分析 → balanced；架构设计、产出方案、疑难调试 → high。可以按角色设基线（`[routing.baselines] coding = "high"`），分类失败回落到基线而不阻塞任务，TUI 里 `alt+r` / `:route` 实时切换并显示选中的模型与分数。

Provider 选择是**三级覆盖：工具 → Agent → 全局**，所以可以让 `micro_agent` 跑本地模型、Director 跑前沿模型。两种协议都是第一公民：`api_format = "openai"` 与 `"anthropic"`（原生 Messages、Extended Thinking 预算映射、流式 `tool_use` 重组），之上还有**带权重的故障转移**和**熔断器**。

### 🧬 6. Meta-Agent：运行时自我进化

当任务超出内置团队的能力，Director 会请出 Meta-Agent，走**设计 → 执行 → 注册**三步：

1. 🎨 一次 LLM 调用产出新 Agent 的系统提示词、工具集与目标（JSON）
2. ⚡ 立即以新 Agent 身份跑起来（15 步）完成当前子任务
3. 🔧 并把它永久注册为 `delegate_<name>`，本任务内后续可直接调用，清单会写进 Director 的 system prompt

> *例如：自动创建 `delegate_security_auditor` 做全仓库安全审计，或 `delegate_performance_profiler` 定位性能瓶颈。*

它能拿到的工具调色板同样受注册表约束——**生成的 Agent 无法自己发明权限**。

### 🌐 7. Browser-Agent：驱动真实 Chrome

已重建在外部 `browser-control` CLI 之上，操作的是一个真实的 Chrome 实例——14 个工具（`open` / `inspect` / `snapshot` / `click` / `fill` / `type` / `select` / `tabs` / `wait` / `screenshot` / `eval` 等），读的是渲染后的 DOM，而不只是抓回来的 HTML。

- **`mode = "attach"`**：通过 CDP 连上你自己正在用的浏览器，复用已登录的会话
- **按委派隔离的工作区**——截图、trace 与上游状态都落在 `~/.yagent/data/browser/<delegation>/`，**绝不会在你的仓库里长目录**
- **URL 策略**（`allowed_domains` / `denied_domains`）、弹窗策略，以及单次输出上限，防止一个大页面淹没上下文
- **敏感 JS 需要授权**——访问 cookie / 存储 / 网络请求、提交表单都要批准；截图路径由工具层收敛到会话工作区
- **诚实的能力降级**：缺 binary 或缺 Chrome 时，`delegate_browser` 自身的描述会变成「不可用 + 原因 + 修复方式」，模型自然不会再尝试

---

## 🧠 认知工具与工程纪律

- **`design`——编码前的设计闸门。** Coding-Agent 的提示词对复杂任务**强制**先出设计：目标与范围、受影响文件、分步计划、风险、验收标准；实现中途范围变了，就重新跑一遍。
- **`deepthinking`**（被动型，常规手段失败后再用）· **`micro_agent`**（隔离子 LLM 推理，独立 provider 槽位）· **`thinking`**（轻量自我反思）——每个都能单独指定 provider。
- **结构化 Thought & Plan 块**——`goal / step / reason / plan`，机器校验；解析结果进入滚动日志，驱动第三档压缩、喂给 TUI 时间线，并在上下文重置后存活。
- **只读工具并行执行**——连续的只读调用（读文件、搜索、调用图查询、读便签、只读委派）自动分批并发（默认 4，上限 8）。并发判定来自工具的**效果**而非名字表，任何扩大并发面的改动都由契约测试把关。
- **git 快照**——每个编码子任务可快照、可回滚。
- **强制委派**——Director 不能写文件，仓库探索必须走 Repo-Agent；不委派就作答会被提醒，最多三次。

## 🛡️ 安全护栏、容错与可恢复

| 关注点 | Yagent 的做法 |
|--------|----------------|
| **爆炸半径** | `WorkspaceGuard`：写类工具与 `run_bash` 的路径必须落在工作区内，指向外部必须授权（按 session / 项目持久化）。`run_bash` **没有命令黑名单**——它把模型自报的 `is_dangerous` 与「任何指向工作区外的绝对路径 token 都要先问」这两条叠在一起用。 |
| **人在回路** | `ask_user_for_help` 支持 confirm / select / input 三种交互；阻塞式授权走**关键旁路队列**，永不丢弃，按 `request_id` 配对。 |
| **自治程度可调** | `-y / --yolo` 自动批准工作区内操作；`-Y / --full-yolo` 连 `ask_user_for_help` 一起移除，适合完全无人值守。 |
| **LLM 抖动** | 每步指数退避重试（≤30s）→ 带权重的 provider 故障转移 → 熔断器 + 复位超时。 |
| **任务卡死** | 用**活动感知超时**替代固定 deadline：每个在跑的工具发心跳，区分「闲着」和「很慢」（`delegate_idle_timeout` / `delegate_total_timeout`），取消信号会传播进子 Agent。 |
| **会话丢失** | **同步写入的 rollout JSONL 是事实来源**：`/resume` 重建会话记忆（并修复工具调用配对）、thinklink 条目、各 Agent token 统计、压缩历史、模型与上下文窗口，并重新接管便签板。被打断的任务会弹窗列出未完成的 plan 项与悬空的工具调用，并询问是否续跑。 |
| **体积炸弹** | 编辑与读取 10MB 上限、单次读 1000 行、2MB 软警告、工具结果预算、超大交付物落盘卸载。 |

---

## 🖥️ 终端 UI

基于 Bubble Tea **v2**，全套统一的面板语言（圆角手绘边框、边框内嵌模式标签、键帽式提示），并带响应式布局引擎，窄终端也能用。

- **最多 10 个并行 session 标签页**（`ctrl+t`、`alt+1..9`、`alt+w`），每个 tab 独立快照自己的会话、任务、时间线、token 计数与压缩统计，各自绑定一个 session ID
- **真正的 vim 模态编辑**——`Esc`→NORMAL、`i`→INSERT、数字前缀（`10j`）、`/` 视口内搜索配合 `n`/`N`，以及完整的 `:` 命令行（`:q :h :mode :language :route :model :timeout :clear :hist :resume`）
- **`/技能名`** 自动补全（来自 `SKILL.md` 注册表），**`@`** 直接调用内嵌 fzf 选文件
- **Token 面板，单一渲染器**：Σ 总量、按 Agent 明细、`⊕` prompt 缓存命中率（OpenAI 与 Anthropic 口径已归一化）、`⛁` 上下文占窗口比例的绿/黄/红渐变计量，外加 `⧉` 压缩区块显示每一档的节省量（`alt+t` 折叠成一行）
- **`alt+m` 模型选择器**——运行时改绑全局默认、7 个 Agent 中的任意一个、`deepthinking` / `design`，或 LLM 超时；按 `space` 额外持久化选择
- **`ctrl+g` thinklink 全屏**并按 Agent 过滤（`f`）、`ctrl+l` 时间线全屏，以及按项目和日期分组的 resume/历史浏览器
- **`ctrl+z`** 挂起回 shell；`:language` 一键在中英之间切换整个界面

---

## 🚀 快速开始

### 下载预编译包（推荐）

从 [GitHub Releases 页面](https://github.com/iohub/yagent/releases) 下载对应平台的 all-in-one 二进制。包内已内嵌 **codeseek 代码智能引擎**（Rust）、**fzf**（模糊搜索）与 **ripgrep**（正则搜索）——解压后直接运行 `./yagent`，零依赖、零配置。

### 前置要求（从源码编译）

- **Go 1.25+**
- `ripgrep` 在 `PATH` 上（`search_by_regex` 使用；发布包已内嵌）
- 只有需要重新构建 codeseek 时才需要 Rust + Cargo

### 编译

```bash
git clone --recurse-submodules https://github.com/iohub/yagent.git
cd yagent
./build.sh              # Rust(codeseek) + Go → dist/bin/codeseek 与 ./yagent
go build -o yagent .    # 只构建 Go 侧，复用已编译好的 dist/bin
```

### 配置

首次运行会用内嵌模板生成 `~/.yagent/config/config.toml`（也可以用 `-c` 指定你自己的路径）：

```toml
[global.llm]
use_provider = "siliconflow"

[global.llm.providers.siliconflow]
model           = "deepseek-ai/DeepSeek-V3.2"
api_base_url    = "https://api.siliconflow.cn/v1"
api_key         = "your-api-key"
temperature     = 0.0
max_tokens      = 23000
# context_window = 262144        # 只影响 TUI 上下文进度条（压缩阈值走模型目录）
# api_format     = "anthropic"   # 原生 Messages + Extended Thinking
# reasoning_effort = "high"      # openai 透传 · anthropic 映射为 thinking budget
# fallback_providers = ["mimo", { provider = "local", weight = 2 }]

[agents.llm.coding]              # Agent 级 / 工具级 provider 覆盖
use_provider = "local"
[tools.llm.micro_agent]
use_provider = "local"

[routing]
mode = "auto"                    # 基准打分的 fast/balanced/high 自动路由
[routing.baselines]
coding  = "high"
repo    = "balanced"

[codeseek]                       # Rust 代码智能（MCP）
binary_path = ""                 # 留空 → 自动从二进制内嵌资源解出

[browser]                        # Browser-Agent（需要 browser-control + Chrome）
enabled = true
mode    = "headless"             # headless | headful | attach
```

### 运行

```bash
./yagent                        # TUI（默认）
./yagent tui --taskfile TASK.md
./yagent -p "解释一下构建系统"     # 非交互单轮
./yagent http --port 0          # HTTP + WebSocket，端口从 9800 起自动探测
```

通用 flag：`-d/--disable-agents` · `-c/--config` · `-y/--yolo` · `-Y/--full-yolo` · `-Q/--force-quit`。
当前工作目录即项目根。技能放在 `.yagent/skills/<name>/SKILL.md`（或 `~/.yagent/skills/`，也兼容扁平的 `<name>.md`）：既是 `/斜杠命令`，也进模型可见的技能目录（模型可自行用 `use_skill` 取回正文）。想接第三方能力就在 `[[mcp.servers]]` 里加一个 stdio MCP server，`/mcp` 能看到每个 server 的状态。

---

## 🧪 构建与质量

```bash
bash scripts/check_all.sh            # 一条命令的本地闸门：build + vet + 五把守卫 + 全量单测
RACE=true bash scripts/check_all.sh  # 再对并发包加 -race
go test ./internal/... -count=1      # 约 970 个用例 / 129 个测试文件，秒级跑完
```

- **链路级 E2E 只 mock 模型，不 mock 被测组件。** 授权回路、跨委派交接（便签 + artifact + deferred schema 重试）、委派生命周期（idle/total 超时与取消传播）、`/resume` 四条核心链路，跑的是**真实** dispatcher、真实 `WorkspaceGuard`、真实便签盘、真实 rollout 文件；只有 LLM 是脚本，而且脚本**按 Agent 身份分派**，不按全局调用序号。
- **协议层用 `httptest` 钉住字节形状**：断言发出去的 JSON 与读回来的用量账目（两家 provider 的缓存 token 口径正好相反，这一点被显式断言）。
- **测试必须能够失败。** 一条卫生棘轮会拒掉「永远不会失败」的用例（无断言、无条件 skip、重复的辅助函数）；夹具没触发到被测分支本身就当 bug 处理。计时相关测试一律走 seam 而不是 `sleep`——这样整套测试才快到有人会真的去跑它。
- **架构棘轮**：`director/` 不许 import `agents/`（防循环依赖）、`toolspec` 必须保持叶子包、不许重新出现按名字查工具的硬编码表、文件行数守卫在数字失步时会主动报出来，而不是一条常红的空门。
- **事件类型只有一个事实来源**：`protocol/agent-events.yaml` 生成 Go / TS / JSON-Schema。

---

## 📖 文档

| 文档 | 说明 |
|------|------|
| [ARCHITECTURE.md](docs/ARCHITECTURE.md) · [ARCHITECTURE.en.md](docs/ARCHITECTURE.en.md) | 系统架构、模块设计、数据流 |
| [AGENTS.md](AGENTS.md) | Agent 自己进仓库前先读的知识库：模块地图、架构不变量、已知坑 |
| [tool-registry-design.md](docs/tool-registry-design.md) | 能力注册表、画像与 deferred tool schema 设计方案 |
| [blackboard-handoff-design.md](docs/blackboard-handoff-design.md) | 跨 Agent 便签、结构化 handoff 与派生索引 |
| [browser-agent-design.md](docs/browser-agent-design.md) | 基于 browser-control CLI 的 Browser-Agent 重建方案 |
| [token-dashboard-design.md](docs/token-dashboard-design.md) · [token-compress-panel-design.md](docs/token-compress-panel-design.md) | Token 记账口径与压缩计量面板 |
| [config-guide.md](docs/config-guide.md) | 完整配置参考手册 |
| [anthropic-config.md](docs/anthropic-config.md) | Anthropic 供应商配置 |
| [llm-fallback-config.md](docs/llm-fallback-config.md) | 熔断与故障转移配置 |

> **仓库级知识只有一条通道**：蓝图（`internal/blueprint`、`[blueprint]` 配置、`read_blueprint`）是
> 唯一的仓库级持久知识。更早的那套知识机制——`internal/knowledge/` 注入、带后台整理 worker 的仓库级
> KV 记忆、以及 `consolidate_knowledge` / `prune_history` 两个工具——已于 2026-10-22 整体删除；
> 注入器当时是硬关闭状态，也就是说那些写入从来没有读者。

---

## 🤝 社区与贡献

我们欢迎任何形式的贡献——Bug 报告、功能建议、文档完善、代码贡献。

- 🐛 [提交 Issue](https://github.com/iohub/yagent/issues)
- 🔀 [提交 Pull Request](https://github.com/iohub/yagent/pulls)
- 💬 [参与讨论](https://github.com/iohub/yagent/discussions)

---

## 📄 许可证

[Apache License 2.0](LICENSE)
