# 上下文压缩配置指南

> **定位说明**：本文档是面向用户的**配置实战指南**，专注于"如何配置"和"怎么调优"。关于压缩机制的原理、漏斗式分层防御哲学、工程复刻方案等底层设计，请参考 [`compress-impl.md`](./compress-impl.md)。两篇文档互补，建议结合阅读。

---

## 1. 配置总览

### 1.1 配置文件位置与优先级

系统支持**三层覆盖**的配置文件加载机制，优先级从高到低：

| 优先级 | 路径 | 用途 |
|:------:|------|------|
| **高** | `~/.codeactor/config/config.toml` | 用户级配置，覆盖全局默认值 |
| **中** | `config/config.toml` | 项目级配置，适用于当前工作空间 |
| **低** | `internal/config/default_config.toml` | 内置默认配置，应用层兜底 |

**加载规则**：高层级配置会覆盖低层级的同名配置项。如果某个配置段完全缺失，应用层会通过 `applySafeDefaults()` 自动填充所有字段的默认值。

### 1.2 热重载支持

配置文件支持**热重载**。修改 `~/.codeactor/config/config.toml` 或 `config/config.toml` 后，系统会自动检测变化并重新加载配置，无需重启。重载过程带有防抖（debounce）机制，避免频繁触发。

### 1.3 配置段标识

所有上下文压缩相关配置均位于 `[context]` TOML 段内。

---

## 2. 核心配置段详解

以下是 `[context]` 段的所有可配置项，按功能分组说明。

### 2.1 总开关与硬上限

| 配置项 | 类型 | 默认值 | 说明 |
|--------|------|--------|------|
| `enable_auto_compact` | bool | `true` | 是否自动触发压缩 |
| `max_context_tokens` | int | `198000` | 最大上下文 token 数（硬上限） |

#### `enable_auto_compact`

- **作用**：控制是否启用自动上下文压缩。设置为 `false` 后，系统将不会在上下文接近上限时自动触发压缩，但手动压缩仍可能生效。
- **推荐设置范围**：`true`（默认值）
- **注意事项**：
  - 设置为 `false` 适用于调试场景，方便观察原始上下文的增长情况
  - 对于非常长的对话，建议保持开启，否则可能导致 API 调用因 token 超限而失败
- **设置不当的影响**：
  - `false` + 长对话 → 上下文无限增长，可能触发 LLM API 的 token 限制错误
  - `true` + 短对话 → 无明显负面影响，系统仅在接近阈值时才会触发

#### `max_context_tokens`

- **作用**：上下文的硬上限。当上下文 token 数超过此值时，系统会触发压缩机制。这是控制压缩触发时机最核心的参数。
- **推荐设置范围**：
  - 200K 窗口模型：`167000 ~ 198000`
  - 1M 窗口模型：`900000 ~ 980000`
  - 小窗口模型（如 4K/8K）：不建议使用自动压缩功能
- **注意事项**：
  - 该值应小于模型的最大上下文窗口
  - 建议预留至少 20K tokens 给摘要输出和 13K 缓冲带
  - 计算公式：`触发阈值 ≈ max_context_tokens - 20000(摘要预留) - 13000(缓冲带)`
- **设置不当的影响**：
  - 设置过大 → 压缩触发过晚，可能来不及处理就超过模型上限
  - 设置过小 → 压缩触发过频，增加不必要的 LLM 摘要开销

### 2.2 摘要引擎配置

| 配置项 | 类型 | 默认值 | 说明 |
|--------|------|--------|------|
| `summarization_model` | string | `""` | 轻量摘要模型名，空=复用主引擎 |
| `summarization_provider` | string | `""` | 独立摘要 provider，空=复用主引擎 |
| `summarization_timeout` | int | `120` | 摘要超时时间（秒） |
| `summarization_max_input_tokens` | int | `120000` | 单批次最大输入 token 数 |
| `summarization_prompt` | string | `""` | 自定义摘要提示词，空=使用默认 |

#### `summarization_model`

- **作用**：指定用于生成上下文摘要的 LLM 模型。设置为空字符串时，系统会复用当前对话的主引擎模型。
- **推荐设置范围**：
  - 留空（使用主模型）
  - 指定轻量模型（如 `gpt-4o-mini`、`claude-haiku` 等）以降低成本
- **注意事项**：
  - 使用独立摘要模型时，需确保该模型已正确配置在 LLM providers 中
  - 轻量模型可能生成质量稍低的摘要，但成本更低、速度更快
- **设置不当的影响**：
  - 指定了不存在的模型 → 摘要任务失败，系统会回退到主引擎
  - 使用性能过差的模型 → 摘要质量低下，可能导致压缩后丢失关键信息

#### `summarization_provider`

- **作用**：为摘要任务指定独立的 LLM provider。这样可以实现与主对话不同的 provider（例如使用更便宜的 provider 进行摘要）。
- **推荐设置范围**：留空（使用主 provider）
- **注意事项**：
  - 与 `summarization_model` 配合使用，可分别指定模型和 provider
  - 需确保指定的 provider 已在配置中注册
- **设置不当的影响**：
  - 指定了不存在的 provider → 摘要任务失败

#### `summarization_timeout`

- **作用**：单次摘要任务的最大等待时间（秒）。如果摘要生成超过此时间，任务将被中断并触发回退机制。
- **推荐设置范围**：
  - 默认 `120` 秒（适合大多数场景）
  - 大上下文摘要：`180 ~ 300` 秒
  - 低延迟优先：`60 ~ 90` 秒
- **注意事项**：
  - 摘要任务的耗时与输入 token 数量和模型性能直接相关
  - 建议在测试环境中观察典型摘要耗时，然后适当放大 50% 作为阈值
- **设置不当的影响**：
  - 设置过小 → 摘要频繁超时，影响用户体验
  - 设置过大 → 摘要失败后阻塞时间过长，拖慢整体响应

#### `summarization_max_input_tokens`

- **作用**：单次摘要任务允许输入的最大 token 数。当待摘要的上下文超过此值时，系统会分批次处理。
- **推荐设置范围**：
  - 默认 `120000`（适合 200K 窗口）
  - 1M 窗口模型：`200000 ~ 400000`
  - 小窗口模型：`80000 ~ 100000`
- **注意事项**：
  - 该值不应超过所用模型的上下文窗口
  - 分批次处理会增加摘要的总耗时，建议合理设置以减少批次数量
- **设置不当的影响**：
  - 设置过小 → 分批次过多，摘要耗时增加
  - 设置过大 → 单次摘要请求过大，可能触发 provider 的输入限制

#### `summarization_prompt`

- **作用**：自定义摘要任务的系统提示词。设置为空字符串时，系统使用内置的默认提示词模板。
- **推荐设置范围**：留空（使用默认）
- **注意事项**：
  - 自定义提示词可用于调整摘要风格，例如"保留技术决策和架构变更"或"精简代码示例，保留关键逻辑"
  - 提示词过长可能影响摘要效率，建议控制在 500 tokens 以内
- **示例配置**：
  ```toml
  summarization_prompt = "请详细保留对话中的技术细节和决策理由"
  ```
- **设置不当的影响**：
  - 提示词与模型不兼容 → 摘要质量下降
  - 提示词过于复杂 → 增加摘要延迟

### 2.3 保留策略

| 配置项 | 类型 | 默认值 | 说明 |
|--------|------|--------|------|
| `keep_recent_rounds` | int | `2` | 始终保留的最近对话轮数 |

#### `keep_recent_rounds`

- **作用**：无论上下文多长，系统始终保留最近 N 轮对话不做压缩。这确保了用户最新的输入和 AI 最近的回复不会被摘要化。
- **推荐设置范围**：
  - 日常开发：`2`（默认值）
  - 长对话/文档阅读：`3 ~ 5`
  - 低延迟优先：`1`
- **注意事项**：
  - 每轮对话包含 user 消息和 assistant 消息（可能包含多条工具结果）
  - 设置过大可能影响压缩效果，导致压缩触发过晚
  - 设置过小可能导致最近的上下文被误压缩
- **设置不当的影响**：
  - 设置过大 → 上下文膨胀，压缩效果减弱
  - 设置过小 → 最近的对话被摘要化，可能丢失即时上下文

---

## 3. 内部引擎配置（高级调优）

以下配置项目前尚未开放为 TOML 可配置项，但它们在 `internal/compact/compact.go` 的 `Config` 结构体中定义，了解它们有助于理解系统行为和未来可能的配置开放方向。

| 配置项 | 类型 | 默认值 | 说明 |
|--------|------|--------|------|
| `MicroCompressEnabled` | bool | `true` | 微压缩开关 |
| `MicroCompressTools` | []string | 白名单 | 需要微压缩的工具列表 |
| `ToolPreviewTokens` | int | `128` | 工具输出预览长度（token） |
| `FoldEnabled` | bool | `true` | 上下文折叠开关 |
| `CompressionDirection` | string | `"auto"` | 压缩方向策略 |
| `MinPrunableAge` | int | `5` | 消息最小年龄（轮数） |
| `EmergencyMaxTokens` | int | `0` | 应急压缩最大 token 预算 |
| `EmergencyCBThreshold` | int | `3` | 熔断器连续失败阈值 |
| `EmergencyCBResetDuration` | int | `30` | 熔断器重置持续时间（秒） |
| `OffloadEnabled` | bool | `true` | 外部存储开关 |
| `OffloadPath` | string | — | 外部存储根路径 |
| `CompensateEnabled` | bool | `true` | 状态补偿开关 |

### 3.1 微压缩相关

- **`MicroCompressEnabled`**：控制是否启用微压缩（Layer 3）。微压缩每轮必做，无额外成本，建议保持开启。
- **`MicroCompressTools`**：微压缩白名单。只有白名单内的工具输出会被替换为占位符。默认包含 `Read`、`Bash`、`Grep`、`Glob`、`WebSearch`、`Edit` 等高频工具。
- **`ToolPreviewTokens`**：工具输出被压缩后，占位符中保留的原始内容预览长度。默认 128 tokens，用于让用户知道原始内容是什么。

### 3.2 压缩方向

- **`CompressionDirection`**：控制压缩时的保留策略
  - `"auto"`（默认）：根据上下文特征自动判断保留最近还是保留最早
  - `"recent"`：保前策略，保留最新的消息（成本优先）
  - `"old"`：保后策略，保留最旧的消息（信息优先）
- **`MinPrunableAge`**：消息的最小年龄（轮数）。小于此值的消息不参与修剪，用于保护近期消息不被过早压缩。默认 5 轮。

### 3.3 应急与容错

- **`EmergencyMaxTokens`**：应急压缩的最大 token 预算。设为 0 表示使用 `MaxContextTokens`。当上下文急剧膨胀时触发。
- **`EmergencyCBThreshold`**：熔断器连续失败阈值。当压缩连续失败次数达到此值时，熔断器打开，跳过后续压缩尝试。默认 3 次。
- **`EmergencyCBResetDuration`**：熔断器从 `Open` 状态切换到 `HalfOpen` 状态的等待时间。默认 30 秒。

---

## 4. 场景化配置模板

以下提供 4 种典型场景的推荐配置，可直接复制到你的 `config.toml` 中。

### 场景 A：日常代码开发（默认推荐）

适合大多数开发场景，平衡性能和上下文保留。

```toml
[context]
enable_auto_compact = true
max_context_tokens = 198000
keep_recent_rounds = 2
summarization_timeout = 120
summarization_max_input_tokens = 120000
```

**特点**：
- 标准的 200K 窗口配置
- 保留 2 轮最近对话
- 默认的摘要超时和输入限制

### 场景 B：大型代码库分析

适合需要深入理解大型项目、多文件关联分析的场景。

```toml
[context]
enable_auto_compact = true
max_context_tokens = 320000
keep_recent_rounds = 3
summarization_timeout = 180
summarization_max_input_tokens = 200000
```

**特点**：
- 更大的上下文窗口，保留更多信息
- 更长的摘要超时，适应大代码库的摘要需求
- 保留 3 轮最近对话，增强即时上下文

### 场景 C：长对话/文档阅读

适合多轮技术讨论、长文档分析、代码审查等场景。

```toml
[context]
enable_auto_compact = true
max_context_tokens = 128000
keep_recent_rounds = 4
summarization_prompt = "请详细保留对话中的技术细节和决策理由"
```

**特点**：
- 更保守的压缩上限，减少压缩频率
- 保留 4 轮最近对话，确保讨论连续性
- 自定义摘要提示词，强调保留技术细节

### 场景 D：低延迟优先

适合对响应速度要求高、对话轮次较少的场景。

```toml
[context]
enable_auto_compact = true
max_context_tokens = 96000
keep_recent_rounds = 1
summarization_timeout = 60
summarization_max_input_tokens = 60000
```

**特点**：
- 较小的上下文窗口，减少 LLM 处理时间
- 仅保留 1 轮最近对话，最大化压缩
- 更短的超时设置，快速失败快速重试

---

## 5. 压缩效果监控

配置调整后，需要观察压缩效果以验证配置是否合理。

### 5.1 通过 Agent 事件观察

每次压缩完成后，系统会发送 `ContextCompressedData` 事件，包含以下字段：

```json
{
  "event_type": "context_compressed",
  "content": {
    "original_tokens": 150000,
    "compressed_tokens": 90000,
    "ratio": "60.00%"
  }
}
```

| 字段 | 含义 |
|------|------|
| `original_tokens` | 压缩前的 token 数 |
| `compressed_tokens` | 压缩后的 token 数 |
| `ratio` | 压缩后占压缩前的百分比（格式为百分比字符串） |

**在 TUI 界面中**，压缩事件会显示为类似以下内容：

```
📦 上下文已压缩  150,000 → 90,000  tokens  (60.00%)
```

**在 VSCode 扩展中**，压缩提示会显示在状态栏或聊天消息中：

```
📦 上下文已压缩 (90000 tokens)
```

### 5.2 通过日志观察

系统日志会记录各层压缩的触发情况。在日志中搜索以下关键词：

- `micro_compress` — 微压缩触发
- `fold` — 上下文折叠触发
- `compact` — 自动压缩触发
- `emergency` — 应急压缩触发
- `completion_ratio` — 压缩比例

**示例日志**：

```
[INFO] micro_compress: replaced 3 tool results with placeholders
[INFO] fold: summarizing 2 conversation groups
[INFO] compact: triggered, original=150000 compressed=90000 ratio=60.0%
```

### 5.3 判断压缩配置是否合理

| 指标 | 正常范围 | 异常信号 | 调优建议 |
|------|----------|----------|----------|
| **压缩率** | 20%~50%（压缩后占原比例） | < 20% 或 > 80% | 调整 `max_context_tokens` |
| **压缩频率** | 每 10~30 轮压缩 1 次 | 每轮都压缩 或 始终不压缩 | 调整 `max_context_tokens` 或 `keep_recent_rounds` |
| **摘要超时** | 极少发生 | 频繁超时 | 增大 `summarization_timeout` 或减小 `summarization_max_input_tokens` |
| **应急压缩** | 极少发生 | 频繁触发 | 增大 `max_context_tokens` |

---

## 6. 常见问题与排障

### Q1: 压缩后丢失关键信息

**症状**：压缩后 AI 忘记了之前讨论过的技术决策或代码逻辑。

**可能原因**：
- `keep_recent_rounds` 设置过小，导致近期关键对话被摘要化
- `summarization_prompt` 未指定，默认提示词过于精简
- `summarization_model` 性能不足，摘要质量差

**解决方案**：
1. 增加 `keep_recent_rounds` 到 3~5
2. 设置更详细的 `summarization_prompt`
3. 更换为更强的摘要模型

```toml
keep_recent_rounds = 4
summarization_prompt = "请保留所有技术决策、代码片段和架构讨论"
```

### Q2: 摘要超时频繁

**症状**：日志中出现 `summarization timeout` 或 `context fold timed out`。

**可能原因**：
- `summarization_timeout` 设置过小
- `summarization_max_input_tokens` 设置过小，导致分批次过多
- 模型响应速度慢

**解决方案**：
1. 增大 `summarization_timeout`
2. 增大 `summarization_max_input_tokens` 减少批次

```toml
summarization_timeout = 180
summarization_max_input_tokens = 200000
```

### Q3: 压缩率过高（> 80%）或过低（< 20%）

**症状**：
- 压缩率过高：上下文被过度压缩，信息损失大
- 压缩率过低：压缩效果不明显，上下文仍然很大

**解决方案**：
- 压缩率过高 → 增大 `max_context_tokens`，让压缩在更高阈值触发
- 压缩率过低 → 减小 `max_context_tokens`，让压缩更早触发

```toml
# 压缩率过高时
max_context_tokens = 250000

# 压缩率过低时
max_context_tokens = 150000
```

### Q4: 微压缩不生效

**症状**：日志中没有 `micro_compress` 记录，工具输出仍然占用大量 token。

**可能原因**：
- `MicroCompressEnabled` 被禁用
- 工具名不在微压缩白名单中

**解决方案**：
1. 确认 `MicroCompressEnabled` 为 `true`
2. 检查工具名是否在白名单中（可查阅 `internal/compact/compact.go` 中的 `MicroCompressTools`）
3. 如需新增工具到白名单，需在代码中添加

### Q5: 压缩后对话质量下降

**症状**：压缩后 AI 的回答质量明显变差，出现幻觉或答非所问。

**可能原因**：
- 压缩方向设置不当（保前 vs 保后）
- 状态补偿未启用

**解决方案**：
1. 尝试调整 `CompressionDirection`
2. 确认 `CompensateEnabled` 为 `true`

```toml
# 尝试保后策略，保留更多信息
compression_direction = "old"

# 确保状态补偿开启
compensate_enabled = true
```

---

## 7. 快速参考速查表

| 想做什么 | 调整哪个参数 | 建议值 |
|----------|-------------|--------|
| 减少压缩频率 | `max_context_tokens` | 增大 |
| 增加压缩频率 | `max_context_tokens` | 减小 |
| 保留更多近期对话 | `keep_recent_rounds` | 增大（3~5） |
| 加快摘要速度 | `summarization_timeout` | 减小 |
| 提高摘要质量 | `summarization_model` | 使用更强的模型 |
| 降低摘要成本 | `summarization_provider` | 使用更便宜的 provider |
| 自定义摘要风格 | `summarization_prompt` | 设置详细提示词 |
| 减少压缩后的信息丢失 | `keep_recent_rounds` + `summarization_prompt` | 组合调整 |

---

*文档版本：v1.0 | 最后更新：2025*
