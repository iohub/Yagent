# Prompt 缓存优化方案

> 审计日期：2025-07-16
> 审计依据：`docs/Prompt_cache.md` 最佳实践文档
> 决策方法：经过 `deepthinking` 深度分析后确定

---

## 一、审计背景

对照 LLM Prompt Cache 最佳实践文档的五项核心检查清单，对项目中 7 个 Agent（Conductor、Coding、Repo、Chat、DevOps、Meta、ImplPlan）的 prompt 构建方式进行了全面审计。

LLM 缓存采用**严格前缀匹配**（Prefix Matching）机制：从第一个 Token 开始必须完全一致，一旦中途有任何字符不同，该字符之后的所有缓存全部失效。

## 二、审计结论总览

| 编号 | 严重程度 | 问题 | 决策 |
|------|---------|------|------|
| **A** | 🔴 P0 | Conductor 动态项目上下文放在静态 prompt 之前 | **必须修复** |
| **B** | 🟡 P0 | RepoAgent 动态数据插入顺序不当 | **必须修复** |
| **C** | 🟢 P1 | `FunctionDef.Parameters` 使用 `map[string]any` | **建议优化**（仅加防御注释） |
| **D** | 🟡 P0 | `FormatPrompt` 中 Environment 字段条件拼接 | **必须修复** |
| **E** | ⚪ P2 | 未实现路由亲和性 | **暂不修复**（集群部署时再处理） |

---

## 三、P0 必须修复项

### 问题 A：Conductor 动态项目上下文放在静态 prompt 之前

**文件**：`internal/agents/conductor.go`（约第 688 行）

**当前代码**：
```go
// ❌ 动态上下文被 prepend 到静态 prompt 之前
systemPrompt = fmt.Sprintf("### Project Workspace Context\n%s\n\n", loadResult.Content) + systemPrompt
```

**问题分析**：
- `loadProjectContext()` 加载的 `CODEACTOR.md`/`CLAUDE.md`/`AGENTS.md` 每个项目内容不同
- 它被放在 `conductor.prompt.md`（139 行静态 prompt）的**最前面**
- 切换项目时，第一个 token 就不同 → 整个 System Prompt 缓存 100% 失效
- Conductor 的 prompt 是系统中最大、最复杂的，缓存失效代价极高

**修复方案**：将 Project Context 移到 system prompt **末尾**

```go
// ✅ 正确：动态上下文放在末尾
systemPrompt := a.GlobalCtx.FormatPrompt(conductorPrompt)
// ... 追加 Custom Agents 注册信息 ...
if shouldLoadProjectContext {
    systemPrompt += "\n\n### Project Workspace Context\n" + loadResult.Content + "\n"
}
```

**预期收益**：
- 139 行静态模板 + Environment + Language Instructions 成为固定前缀
- 跨项目、跨会话共享缓存
- 缓存命中率预计提升 60%~80%

---

### 问题 B：RepoAgent 动态数据插入顺序不当

**文件**：`internal/agents/repo.go`（约第 195-217 行）

**当前代码**：
```go
// ❌ 动态 investigation 数据插在静态 prompt 和环境信息之间
systemPrompt := repoPrompt       // 54 行静态
systemPrompt += info             // ← 动态数据插在中间
systemPrompt = a.GlobalCtx.FormatPrompt(systemPrompt)  // 追加 Environment
```

**问题分析**：
- `doPreInvestigate()` 返回的 Directory Tree、Core Functions、File Skeletons 每个项目不同，甚至同一项目代码变化后也不同
- 动态数据后的 Environment + Language Instructions 缓存连带失效
- RepoAgent 是高频调用 Agent

**修复方案**：先 `FormatPrompt`（静态 + 环境），最后追加动态调查数据

```go
// ✅ 正确：静态在前，动态在最后
systemPrompt := a.GlobalCtx.FormatPrompt(repoPrompt)
systemPrompt += info  // investigation 数据放在最后
```

**预期收益**：
- 54 行静态指令完全固化于前缀
- 动态数据放尾部符合 LLM 注意力机制的"近因效应"

---

### 问题 D：`FormatPrompt` 中 Environment 字段条件拼接

**文件**：`internal/globalctx/global_context.go`（`FormatPrompt` 方法）

**当前代码**：
```go
// ❌ 条件判断导致同环境下前缀不一致
if g.ProjectPath != "" {
    sb.WriteString(fmt.Sprintf("- **Project Path**: %s\n", g.ProjectPath))
}
if g.OS != "" {
    sb.WriteString(fmt.Sprintf("- **Operating System**: %s\n", g.OS))
}
if g.Arch != "" {
    sb.WriteString(fmt.Sprintf("- **Architecture**: %s\n", g.Arch))
}
```

**问题分析**：
- 条件分支导致相同环境下的请求前缀长度/内容不同
- 若某字段为空被跳过，Environment 块的结构发生变化
- 虽然 Environment 在最末尾，不会破坏前面的静态缓存，但会影响完整前缀一致性

**修复方案**：移除条件判断，始终输出完整字段结构

```go
// ✅ 正确：始终输出完整字段，空值用占位符
projectPath := g.ProjectPath
if projectPath == "" {
    projectPath = "[NOT SET]"
}
os := g.OS
if os == "" {
    os = "[NOT SET]"
}
arch := g.Arch
if arch == "" {
    arch = "[NOT SET]"
}

sb.WriteString("\n\n### Environment\n")
sb.WriteString(fmt.Sprintf("- **Project Path**: %s\n", projectPath))
sb.WriteString(fmt.Sprintf("- **Operating System**: %s\n", os))
sb.WriteString(fmt.Sprintf("- **Architecture**: %s\n", arch))
```

**预期收益**：
- 保证了 Environment 块的结构和前缀长度绝对一致
- 最大化缓存命中率

---

## 四、P1 建议优化项

### 问题 C：`FunctionDef.Parameters` 使用 `map[string]any`

**文件**：`internal/llm/engine.go`（`FunctionDef` 结构体）

**现状**：
```go
type FunctionDef struct {
    Name        string         `json:"name"`
    Description string         `json:"description,omitempty"`
    Parameters  map[string]any `json:"parameters,omitempty"`
}
```

**分析**：
- Go 标准库 `encoding/json` 对 `map` 序列化时按 key **字母排序**，行为是确定性的 ✅
- 但如果未来切换 JSON 库（如 `sonic`、`jsoniter`），需确保启用 `SortMapKeys` 配置
- 当前改为 struct 的工程成本高、收益低，不建议重构

**建议方案**：仅添加防御性注释

```go
// ⚠️ IMPORTANT: The current implementation relies on encoding/json's deterministic
// sorting of map keys (alphabetical order). If migrating to sonic, jsoniter, or
// another JSON library in the future, ensure SortMapKeys is enabled to maintain
// deterministic key ordering and prevent prompt cache fragmentation.
type FunctionDef struct {
    Name        string         `json:"name"`
    Description string         `json:"description,omitempty"`
    Parameters  map[string]any `json:"parameters,omitempty"`
}
```

---

## 五、P2 暂不修复项

### 问题 E：未实现路由亲和性

**分析**：
- 当前开发环境为单节点运行，路由亲和性无实际影响
- 引入 `prompt_cache_key` 或 Session Router 需改造 LLM Client 层，增加状态管理复杂度

**规划**：
- 集群部署时再引入一致性哈希路由或共享 Redis 缓存层
- 可在 `llm.CallOptions` 中预留 `PromptCacheKey` 字段供未来使用

---

## 六、其他发现：可接受的架构代价

### Compact 压缩导致的缓存 Miss

压缩引擎的 L3（丢弃早期消息）和 L2（截断工具输出）会改变消息结构，导致后续 LLM 调用的前缀变化。这是**可接受的架构代价**：缓存 Miss 是换取 Token 超限安全的必要手段，不应为保缓存而限制压缩。

### 动态 Agent 注册导致的工具定义变化

Meta-Agent 运行时注册自定义 Agent 会改变 `tool_defs` 列表。由于 `tool_defs` 作为 API 请求参数参与前缀匹配，动态注册天然导致 Cache Miss。这属于业务特性，无法避免。

---

## 七、实施优先级

| 优先级 | 问题 | 预计改动量 | 风险 |
|--------|------|-----------|------|
| **1** | D — FormatPrompt 条件拼接 | ~10 行 | 极低 |
| **2** | B — RepoAgent 顺序调整 | ~3 行 | 低 |
| **3** | A — Conductor 上下文移至末尾 | ~5 行 | 低（需验证 LLM 指令遵循度） |
| **4** | C — 添加防御注释 | ~5 行 | 零风险 |

---

## 八、验证策略

1. **单元测试**：验证 `FormatPrompt` 在不同参数下输出前缀一致
2. **集成测试**：使用 Mock LLM 拦截请求，统计前缀命中率
3. **LLM 行为回归**：选取典型编码任务，验证修复后指令遵循率、工具调用准确率无退化
4. **回滚方案**：所有变更通过独立 Git Commit 隔离，异常时一键 Revert