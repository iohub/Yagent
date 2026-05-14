# Git Commit Learning System

## 概述

Git Commit Learning 系统让 Agent 能够自动学习项目的 git 提交历史，理解最近的代码变更，并在处理用户任务时利用这些信息作为上下文。

## 功能

1. **自动学习**: 获取最近 N 个 commit，使用 LLM 生成结构化摘要
2. **向量存储**: 将摘要存储到向量数据库（LanceDB）
3. **相似度搜索**: 根据用户输入搜索最相关的 commit
4. **上下文注入**: 将匹配的 commit 摘要作为上下文注入 Agent

## 配置

在 `config/config.toml` 中添加：

```toml
[commit_learner]
enabled = true                          # 启用/禁用
max_commits = 30                        # 学习最近 N 个 commit
similarity_threshold = 0.75             # 相似度阈值
top_k = 3                               # 返回的最多匹配数
trigger = "both"                        # 触发模式: "on_demand" | "on_session_start" | "both"
cache_ttl = 3600                        # 缓存有效期（秒）
rust_service_url = "http://127.0.0.1:12800"  # Rust 服务地址
```

## 触发模式

### on_demand
仅在用户显式调用工具时触发。

### on_session_start
每次新会话启动时自动触发。

### both
两种模式都启用。

## 使用方式

### 1. 自动触发（推荐）

在 `trigger = "both"` 配置下，Agent 会在会话启动时自动学习最近的 commit，并在处理任务时自动注入相关上下文。

### 2. 手动触发工具

#### learn_commits
手动触发 commit 学习：

```
learn_commits(max_commits=20, repo_path="/path/to/repo")
```

#### search_similar_commits
搜索与任务相关的 commit：

```
search_similar_commits(query="user authentication", top_k=5)
```

## 工作流程

```
1. 获取 commit 信息 (git log)
   ↓
2. LLM 生成结构化摘要
   ↓
3. 生成向量嵌入 (Rust EmbeddingService)
   ↓
4. 存储到 LanceDB
   ↓
5. 用户输入 → 向量搜索 → Top-K 匹配
   ↓
6. 注入 Agent 上下文
```

## 数据结构

### CommitMeta
- `Hash`: commit hash
- `Subject`: commit message
- `Author`: 作者
- `Date`: 提交时间
- `Files`: 变更文件列表
- `Diff`: diff 文本（截断）

### CommitSummary
- `Hash`: commit hash
- `Requirement`: 需求摘要
- `Files`: 变更文件描述
- `Approach`: 技术思路
- `Implementation`: 实现细节

## 注意事项

1. **性能**: 首次学习可能需要几秒到几十秒（取决于 commit 数量和 LLM 响应时间）
2. **Token 成本**: 30 个 commit 的摘要生成会消耗数千 token
3. **缓存**: 使用 HEAD hash 作为缓存键，避免重复处理
4. **降级**: 如果 Rust 服务不可用，系统会优雅降级，不注入 commit 上下文
5. **分支切换**: 切换分支时，缓存会自动失效（HEAD 变化）

## 技术架构

- **Go 侧**: CommitLearner 组件负责 git 操作、LLM 调用、业务逻辑
- **Rust 侧**: CommitEmbeddingService 负责向量生成和存储
- **通信**: HTTP API（/commit/embed, /commit/search, /commit/clear）
