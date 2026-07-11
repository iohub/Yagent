# 🎻 CodeActor — A Self-Evolving AI Coding Engine

> **Not a copilot. A crew of autonomous agents that understand, navigate, and evolve your codebase — together.**

<p align="center">
  <a href="https://youtu.be/hFsy0VnW-3U">
    <img src="https://img.youtube.com/vi/hFsy0VnW-3U/maxresdefault.jpg" alt="Watch Demo Video" width="800">
  </a>
  <br>
  <em>▶️ Click the image above to watch the demo video on YouTube</em>
</p>

---

## 💡 Why CodeActor?

Most AI coding tools share a fundamental flaw: **they treat code as text, not structure**.

| Traditional Tools | CodeActor |
|-------------------|-----------|
| Flat text pattern matching | 🧠 **Structural code understanding** via AST + call graphs + semantic vectors |
| Single agent, working alone | 🤖 **Hub-and-Spoke multi-agent**: Director orchestrates, six specialized agents execute |
| Static capabilities | 🧬 **Meta-Agent**: designs & registers new agents at runtime — the system evolves |
| Keyword-only search | 🔍 Natural-language semantic search — "find where auth logic is implemented" |
| No project memory | — |

---

## 🤖 The Agent Team

| Agent | Role | Core Capability |
|-------|------|-----------------|
| 🎼 **Director** | Orchestrator | Task decomposition, dynamic planning, delegation, review |
| 🔬 **Repo-Agent** | Code Archaeologist | AST parsing, semantic search, call graphs, code skeletons |
| ✏️ **Coding-Agent** | Staff Engineer | 22+ tools, autonomous coding, self-correction |
| 🌐 **Browser-Agent** | Web Researcher | Headless Chrome, page navigation, data extraction |
| 🔧 **DevOps-Agent** | SRE | Shell execution, environment diagnostics, process management |
| 💬 **Chat-Agent** | Technical Advisor | General Q&A, technical explanations |
| 🧬 **Meta-Agent** | Agent Factory | Runtime agent design & registration |

---

## 🏗️ Architecture

```
User Interface (TUI / HTTP+WebSocket)
            │
     🎼 Director
     Task Decomposition · Dynamic Planning · Review
            │
   ┌────────┼────────┬────────┬────────┬────────┐
   │        │        │        │        │        │
🔬Repo   ✏️Coding  💬Chat   🔧DevOps 🌐Browser 🧬Meta
Code Intel  Editing  Q&A     Shell    Web     Agent
(Rust)    (22 tools)         Ops     Research Factory
```

> [Full architecture docs →](docs/ARCHITECTURE.md)

---

## ⚡ Four Core Differentiators

### 🧠 1. Rust-Powered Deep Code Intelligence

The **Repo-Agent** is backed by a Rust engine with Tree-sitter AST parsing, LanceDB vector embeddings, and Petgraph call-graph analysis. It understands code like a senior engineer — cross-file impact analysis, cycle detection, semantic search.

- **7 language ASTs**: Rust · Python · JavaScript · TypeScript · Java · C++ · Go
- **Semantic search**: find code by *meaning*, not keywords
- **Call graph analysis**: real-time "who calls this function?" and "what will this change break?"
- **Auto-indexing**: file watcher with 20s debounce keeps the model in sync

### 🧬 2. Meta-Agent: Self-Evolving at Runtime

This is CodeActor's most unique capability. When the Director encounters a task beyond built-in agents, the **Meta-Agent**:

1. 🎨 **Designs** — auto-generates a new agent's system prompt and toolset
2. ⚡ **Executes** — immediately runs the new agent to complete the task
3. 🔧 **Registers** — permanently adds it to the available tool pool

> *Example: auto-creates `delegate_security_auditor` for full-repo security audits, or `delegate_performance_profiler` for bottleneck analysis.*

### 🌐 3. Browser-Agent: Autonomous Web Research

Built-in headless Chrome (go-rod) navigates the web autonomously — documentation, GitHub issues, Stack Overflow. When local context is insufficient, the Director delegates web research automatically.

> *"Find the latest FastAPI middleware docs and summarize CORS setup" — without leaving the terminal.*

### 📚 4. Git Commit Learning: Project Memory

Automatically fetches recent commits → LLM generates structured summaries → LanceDB vector storage → semantic matching on user queries → relevant history auto-injected into context. The AI always knows your project's latest evolution.

---

### 🔬 5. Hybrid Retrieval + Code Graph Expansion: From "Found" to "Understood"

> **Traditional code search tells you *where* keywords match. CodeActor finds the code, then automatically analyzes the structural world around it.**

#### 🎯 Three-Stage Cascading Retrieval Pipeline

```
User Query
    │
    ├─→ Stage 1: Hybrid Search (Dual-Channel High Recall)
    │   ├── 🧠 Dense: LanceDB Vector Search (Qwen3-Embedding-4B, 2560-dim)
    │   └── 🔤 Sparse: Tantivy BM25 Full-Text Search (CodeTokenizer for snake_case/CamelCase)
    │   └── 🔗 RRF Fusion: Reciprocal Rank Fusion merges both channels
    │
    ├─→ Stage 2: Code Graph Expansion (Structural Context Injection)
    │   └── PetCodeGraph BFS Traversal: from seed functions, auto-expand callers/callees
    │   └── Cross-file context: place isolated code blocks back into their architectural position
    │
    └─→ Stage 3: Cross-Encoder Rerank (Precision Refinement)
        └── Optional Reranker API for Query-Document cross-encoding rerank
```

#### Why This Matters

**Pure vector search treats code blocks as isolated islands** — it computes semantic similarity but has no idea what the function calls, who calls it, or what module it belongs to.

**CodeActor's breakthrough**: Hybrid retrieval + code graph expansion = **a leap from "found" to "understood"**.

| Aspect | Pure Vector Search | CodeActor Hybrid + Graph Expansion |
|--------|-------------------|-------------------------------------|
| Recall | ❌ Semantic matches with different keywords → missed | ✅ BM25 + Vector dual-channel covers both semantics and exact match |
| Precision | ❌ Short text / noise often ranks high | ✅ RRF fusion + short-code penalty + Cross-Encoder triple filtering |
| Context | ❌ Returns isolated code blocks with no call relationships | ✅ PetCodeGraph auto-expands call chains, restores architectural context |
| Code-Aware | ❌ Generic tokenizers don't understand code naming | ✅ Custom CodeTokenizer designed for snake_case & CamelCase |
| Robustness | ❌ Single point of failure | ✅ Triple degradation: BM25 fails→dense-only, Reranker fails→RRF, one channel→other |

---

## 🚀 Quick Start

### Download Pre-built Binary (Recommended)

Download the latest all-in-one release for your platform from the [GitHub Releases page](https://github.com/iohub/codeactor-agent/releases). The binary bundles the **codexray intelligence engine** (Rust), **fzf** (fuzzy finder), and **ripgrep** (regex search) — everything you need is included. Just extract and run `./codeactor` — zero dependencies, zero configuration.

### Prerequisites (for building from source)
- Go 1.24+
- `ripgrep` (full-text regex search)

### Build from Source

```bash
git clone https://github.com/iohub/codeactor-agent.git
cd codeactor-agent
go build -o codeactor .
```

### Configure

Create `~/.codeactor/config/config.toml`:

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

### Run

```bash
# TUI mode
./codeactor tui

# With a task file
./codeactor tui --taskfile TASK.md

# HTTP server mode (default :9080)
./codeactor http
```

---

## 📖 Documentation

| Document | Description |
|----------|-------------|
| [ARCHITECTURE.md](docs/ARCHITECTURE.md) | System architecture, modules, data flow |
| [Agent_Design.md](docs/Agent_Design.md) | Multi-agent design rationale |
| [Agent_Reference.md](docs/Agent_Reference.md) | API reference & configuration |
| [Browser_Agent_Design.md](docs/Browser_Agent_Design.md) | Browser agent architecture |

---

## 🤝 Community & Contributing

We welcome all contributions — bug reports, feature requests, docs, and code.

- 🐛 [Open an Issue](https://github.com/iohub/codeactor-agent/issues)
- 🔀 [Submit a PR](https://github.com/iohub/codeactor-agent/pulls)
- 💬 [Join the Discussion](https://github.com/iohub/codeactor-agent/discussions)

---

## 📄 License

[Apache License 2.0](LICENSE)
