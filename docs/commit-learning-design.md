# Git Commit Learning System - 系统设计文档

## 1. 概述

### 1.1 问题陈述
Agent 系统在处理用户任务时，对项目近期演化历史缺乏感知。这导致：
- Agent 不了解项目最近修改了什么
- 可能重复实现刚刚完成的功能
- 与近期重构产生冲突
- 无法利用已有的代码模式和经验

### 1.2 解决方案
设计并实现一个自动学习 git commit 能力的系统，使 Agent 能够：
1. 解读最近 30 个 commit，总结需求摘要、变更文件、思路和实现
2. 与用户输入任务进行相似度匹配
3. 高相似度的 commit 作为上下文注入 Agent

## 2. 系统架构

```
┌─────────────────────────────────────────────────────────────────┐
│                        Go 主应用                                 │
│                                                                 │
│  ┌──────────────┐    ┌──────────────┐    ┌──────────────────┐  │
│  │ CommitManager│───▶│CommitLearner │───▶│  Tool Adapters   │  │
│  │  (管理器)    │    │  (学习者)    │    │  (工具适配器)    │  │
│  └──────────────┘    └──────────────┘    └──────────────────┘  │
│            │                  │                                    │
│            │                  │  HTTP API                       │
│            │                  ▼                                    │
│            │    ┌────────────────────────┐                       │
│            │    │   Rust Codebase Service│                       │
│            │    │                        │                       │
│  ┌────────▼──┐ │  ┌──────────────────┐  │                       │
│  │ Conductor │ │  │ CommitEmbedding  │  │                       │
│  │  Agent    │ │  │ Service          │  │                       │
│  │ (指挥家)  │ │  │                  │  │                       │
│  │            │ │  │  ┌───────────┐   │  │                       │
│  └────────────┘ │  │  │ LanceDB   │   │  │                       │
│                  │  │  │Table:     │   │  │                       │
│  ┌──────────────┐│  │  │commit_    │   │  │                       │
│  │ Context      ││  │  │embeddings│   │  │                       │
│  │ Compressor   ││  │  └───────────┘   │  │                       │
│  │ (压缩引擎)   ││  └──────────────────┘  │                       │
│  └──────────────┘│                          │                       │
└──────────────────┼──────────────────────────────────────────────────┘
                   │
                   ▼
           ┌─────────────────┐
           │  Git Repository  │
           │                  │
           │  git log         │
           │  git diff        │
           └─────────────────┘
```

## 3. 组件详解

### 3.1 Rust 侧组件

#### CommitEmbeddingService
- **位置**: `codebase/src/services/commit_embedding_service.rs`
- **职责**: 
  - 管理 LanceDB 中的 commit 向量数据
  - 提供向量生成和搜索功能
  - 支持 SQLite 缓存
- **核心方法**:
  - `init_table()` - 初始化 commit 向量表
  - `add_commit(hash, summary)` - 添加 commit 向量
  - `search_similar(query, top_k)` - 搜索相似 commit
  - `clear_all()` - 清空数据

#### HTTP API
- **位置**: `codebase/src/http/handlers/commit.rs`
- **端点**:
  - `POST /commit/embed` - 添加 commit 向量
  - `POST /commit/search` - 搜索相似 commit
  - `POST /commit/clear` - 清空 commit 向量

### 3.2 Go 侧组件

#### CommitLearner
- **位置**: `internal/agents/commit_learner.go`
- **职责**:
  - 通过 git log 获取 commit 信息
  - 调用 LLM 生成结构化摘要
  - 调用 Rust API 存储向量
  - 搜索相似 commit
- **核心方法**:
  - `FetchRecentCommits()` - 获取最近 N 个 commit
  - `SummarizeCommits()` - LLM 生成摘要（并发处理）
  - `StoreEmbeddings()` - 存储到向量数据库
  - `SearchSimilar()` - 搜索相似 commit
  - `EnsureLatest()` - 缓存机制

#### CommitManager
- **位置**: `internal/agents/commit_manager.go`
- **职责**:
  - 初始化和提供 CommitLearner 实例
  - 支持异步初始化
  - 线程安全

#### Tools
- **位置**: `internal/agents/commit_tools.go`
- **工具**:
  - `learn_commits` - 触发 commit 学习
  - `search_similar_commits` - 搜索相似 commit

## 4. 工作流程

### 4.1 自动触发流程

```
用户请求
    │
    ▼
ConductorAgent 接收请求
    │
    ▼
CommitManager.EnsureLatest()
    │
    ├── HEAD 未变 & 缓存未过期 → 跳过
    │
    └── HEAD 变化或缓存过期 → 继续
            │
            ▼
        git log --max-count=30
            │
            ▼
        LLM 生成摘要 (并发 3)
            │
            ▼
        调用 Rust API 存储向量
            │
            ▼
        更新缓存
```

### 4.2 相似度搜索流程

```
用户输入
    │
    ▼
SearchSimilar(userInput, topK)
    │
    ▼
调用 Rust /commit/search API
    │
    ▼
LanceDB 向量搜索
    │
    ▼
过滤低于阈值的匹配 (similarity < 0.75)
    │
    ▼
返回 Top-K 摘要
    │
    ▼
格式化为文本
    │
    ▼
注入 Agent 上下文
```

## 5. 数据模型

### 5.1 CommitMeta (Go)
```go
type CommitMeta struct {
    Hash    string
    Subject string
    Author  string
    Date    time.Time
    Files   []string
    Diff    string
}
```

### 5.2 CommitSummary (Go)
```go
type CommitSummary struct {
    Hash            string
    Requirement     string  // 需求摘要
    Files           string  // 变更文件
    Approach        string  // 思路
    Implementation  string  // 实现
}
```

### 5.3 LanceDB 表 Schema (Rust)
```
Table: commit_embeddings
- commit_hash: string (PK)
- summary_text: string
- embedding: vector<float32>[1536]
- timestamp: i64
```

## 6. 配置

### 6.1 TOML 配置
```toml
[commit_learner]
enabled = true                          # 启用/禁用
max_commits = 30                        # 学习最近 N 个 commit
similarity_threshold = 0.75             # 相似度阈值
top_k = 3                               # 返回的最多匹配数
trigger = "both"                        # 触发模式
cache_ttl = 3600                        # 缓存有效期（秒）
rust_service_url = "http://127.0.0.1:12800"
```

### 6.2 触发模式
| 模式 | 说明 |
|------|------|
| `on_demand` | 仅手动触发 |
| `on_session_start` | 会话启动时自动触发 |
| `both` | 两种模式都启用 |

## 7. 验证结果

### 7.1 编译验证
- ✅ Go 编译: `go build ./...`
- ✅ Rust 编译: `cargo check`
- ✅ Go 测试: 13/13 测试通过

### 7.2 功能验证
- ✅ Commit 获取和解析
- ✅ LLM 摘要生成
- ✅ 向量存储和搜索
- ✅ 缓存机制
- ✅ 工具注册和调用

## 8. 文件清单

### Rust 代码
| 文件 | 说明 |
|------|------|
| `codebase/src/services/commit_embedding_service.rs` | Commit 向量服务 |
| `codebase/src/http/handlers/commit.rs` | HTTP Handlers |
| `codebase/src/http/models/commit.rs` | HTTP 模型 |
| `codebase/src/storage/mod.rs` | StorageManager 扩展 |
| `codebase/src/services/mod.rs` | 服务导出 |

### Go 代码
| 文件 | 说明 |
|------|------|
| `internal/agents/commit_learner.go` | CommitLearner 核心 |
| `internal/agents/commit_manager.go` | CommitManager |
| `internal/agents/commit_tools.go` | 工具定义 |
| `internal/config/config.go` | 配置集成 |
| `internal/agents/conductor.go` | Agent 集成 |

### 测试和文档
| 文件 | 说明 |
|------|------|
| `internal/agents/commit_learner_test.go` | 单元测试 |
| `docs/commit-learning.md` | 使用文档 |
| `docs/commit-learning-design.md` | 系统设计文档 |

## 9. 扩展方向

1. **多仓库支持**: 支持多个 git 仓库的 commit 学习
2. **增量更新**: 仅处理新增的 commit，不重复处理
3. **跨分支搜索**: 支持跨分支搜索 commit
4. **Commit 关系分析**: 分析 commit 之间的依赖关系
5. **自动学习触发**: 基于 commit message 的关键字自动触发
6. **Commit 分组**: 将相关的 commit 分组学习
