# LLM 故障转移（Fallback）配置指南

当主 LLM Provider 因限流（429）、服务器故障（5xx）、网络超时等原因调用失败时，CodeActor 可以自动将请求切换至备选 Provider，保障 Agent 任务不中断。

---

## 快速开始

### 最小配置（3 步启用）

**第 1 步**：打开全局开关

```toml
[llm]
enable_fallback = true
```

**第 2 步**：在主 Provider 中声明备选列表

```toml
[global.llm.providers.aliyun]
model = "qwen3-coder-plus"
api_base_url = "https://dashscope.aliyuncs.com/compatible-mode/v1"
api_key = "your-key"
# ✅ 添加这一行
fallback_providers = ["deepseek_v4_pro", "local"]
```

**第 3 步**：确保备选 Provider 已定义

```toml
[global.llm.providers.deepseek_v4_pro]
model = "deepseek-v4-pro"
api_base_url = "https://api.deepseek.com/v1"
api_key = "your-key"

[global.llm.providers.local]
model = "qwen3-30b-a3b"
api_base_url = "http://127.0.0.1:8000"
api_key = "dummy-key"
```

至此启用完成。当 `aliyun` 调用失败时，系统自动按 `deepseek_v4_pro` → `local` 的顺序尝试。

---

## 配置详解

### 全局开关 `[llm]`

| 字段 | 类型 | 默认值 | 说明 |
|:---|:---|:---|:---|
| `enable_fallback` | `bool` | `false` | 总开关。`false` 时忽略所有 `fallback_providers` |
| `fallback_max_retries` | `int` | 使用 `max_retries` 的值 | 每个备选 Provider 的内部重试次数 |

```toml
[llm]
enable_fallback = true
fallback_max_retries = 2     # 每个备选最多重试 2 次（即最多尝试 3 次）
```

### Provider 级降级列表

在任意 `[global.llm.providers.<name>]` 中添加 `fallback_providers`。

#### 格式一：简写（推荐）

```toml
fallback_providers = ["deepseek_v4_pro", "local", "backup"]
```

按书写顺序逐个尝试。适合大多数场景。

#### 格式二：完整（带权重）

```toml
fallback_providers = [
    { provider = "deepseek_v4_pro", weight = 10 },
    { provider = "local",           weight = 5  },
]
```

权重越大越优先，与书写顺序无关。适合需要精确控制优先级的多 Provider 场景。

> **两种格式可以混用吗？** 可以，TOML 解析器会正确处理。但建议统一使用一种以保持可读性。

---

## 故障转移流程

```
Agent 发起 LLM 调用
        │
        ▼
┌───────────────────┐
│  主 Provider      │  ← 内部重试 max_retries 次
│  (如 aliyun)      │
└──────┬────────────┘
       │ 成功 → ✅ 返回结果
       │ 失败（429/5xx/超时）
       ▼
┌───────────────────┐
│  FallbackEngine   │
│                   │
│  1. 过滤非法条目  │  ← 跳过不存在的、自引用的 provider
│  2. 排序候选列表  │  ← 有非零权重时按 weight 降序，否则保持书写顺序
│  3. 逐个尝试      │
└──────┬────────────┘
       │
       ▼
┌───────────────────┐
│  备选 #1          │  ← 内部重试 fallback_max_retries 次
│  deepseek_v4_pro  │
└──────┬────────────┘
       │ 成功 → ✅ 返回结果（记录日志）
       │ 失败
       ▼
┌───────────────────┐
│  备选 #2          │
│  local            │
└──────┬────────────┘
       │ 成功 → ✅ 返回结果
       │ 失败
       ▼
❌ 全部失败：返回聚合错误
```

---

## 完整配置示例

```toml
# ══════════════════════════════════════════════
# LLM 故障转移全局配置
# ══════════════════════════════════════════════
[llm]
timeout = "5m"
max_retries = 5
enable_fallback = true
fallback_max_retries = 2

# ══════════════════════════════════════════════
# Provider 定义
# ══════════════════════════════════════════════
[global.llm]
use_provider = "aliyun"

# --- 主 Provider ---
[global.llm.providers.aliyun]
model = "qwen3-coder-plus"
temperature = 0.0
max_tokens = 28000
api_base_url = "https://dashscope.aliyuncs.com/compatible-mode/v1"
api_key = "sk-your-key"
# 故障转移链：先尝试 deepseek（质量高），失败则切 local（成本低）
fallback_providers = ["deepseek_v4_pro", "local"]

# --- 备选 #1（带 thinking 模式）---
[global.llm.providers.deepseek_v4_pro]
model = "deepseek-v4-pro"
temperature = 0.0
max_tokens = 3000
api_base_url = "https://api.deepseek.com/v1"
api_key = "sk-your-key"
reasoning_effort = "max"

# --- 备选 #2（本地模型，兜底）---
[global.llm.providers.local]
model = "qwen3-30b-a3b"
temperature = 0.0
max_tokens = 3000
api_base_url = "http://127.0.0.1:8000"
api_key = "dummy-key"

# --- 使用权重的高级示例 ---
[global.llm.providers.production]
model = "claude-sonnet-4-20250514"
temperature = 0.0
max_tokens = 8192
api_base_url = "https://api.anthropic.com/v1"
api_key = "sk-ant-your-key"
api_format = "anthropic"
fallback_providers = [
    { provider = "gpt4_fallback",    weight = 100 },
    { provider = "deepseek_v4_pro",  weight = 50  },
    { provider = "local",            weight = 10  },
]
```

---

## 内置安全机制

| 场景 | 系统行为 | 日志级别 |
|:---|:---|:---|
| 引用不存在的 Provider | 跳过，继续尝试下一个 | `WARN` |
| 自引用（A 的 fallback 包含 A） | 跳过自身 | `WARN` |
| 所有备选均失败 | 返回聚合错误（含各 Provider 失败原因） | `ERROR` |
| Context 被取消 | 立即终止转移，返回取消错误 | — |

---

## 日志追踪

启用 fallback 后，可在 `llm-error-YYYY-MM-DD.log` 中追踪完整转移轨迹：

```text
WARN  Primary provider failed, starting fallback
      provider=aliyun model=qwen3-coder-plus
      error="429 Too Many Requests"

INFO  Attempting fallback provider
      primary=aliyun fallback=deepseek_v4_pro model=deepseek-v4-pro weight=0

WARN  Fallback provider also failed
      primary=aliyun fallback=deepseek_v4_pro
      error="context deadline exceeded"

INFO  Attempting fallback provider
      primary=aliyun fallback=local model=qwen3-30b-a3b weight=0

INFO  Fallback provider succeeded
      primary=aliyun fallback=local model=qwen3-30b-a3b
```

同时在 `llm-YYYY-MM-DD.log` 中会记录 `Fallback Success` 事件。

---

## 常见问题

### Q: fallback 会影响正常请求延迟吗？

不会。仅在主 Provider 调用失败后才触发，正常路径零开销。

### Q: 可以给不同 Agent 配置不同的 fallback 链吗？

可以。每个 Agent 可能使用不同的 Provider（通过 `agents.llm.<agent>.use_provider`），而每个 Provider 都有独立的 `fallback_providers`。

```toml
# Director 使用 aliyun，其 fallback 链为 deepseek → local
[agents.llm.director]
use_provider = "aliyun"

# Coding 使用 local，不配置 fallback（失败即报错）
[agents.llm.coding]
use_provider = "local"
```

### Q: 简写格式的权重是多少？

简写格式等价于 `weight = 0`。当所有备选均为简写（weight = 0）时，按书写顺序尝试。

### Q: 如何关闭某个 Provider 的 fallback？

两种方式：
1. 全局关闭：`enable_fallback = false`
2. 单个 Provider 不配置：删除或注释 `fallback_providers` 行

---

## 配置校验清单

- [ ] `[llm]` 中 `enable_fallback = true`
- [ ] 主 Provider 的 `fallback_providers` 不为空
- [ ] 所有备选 Provider 在 `[global.llm.providers]` 中已定义
- [ ] 无自引用（A 的 fallback 不包含 A）
- [ ] `fallback_max_retries` 已设置合理值（建议 1~3）
