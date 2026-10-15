# 上下文压缩触发：阈值口径 + 增量记账 + provider 计量 设计方案

> **本文是给 Coding-Agent 的实施规格书，不是讨论稿。**
> 所有"今天如何"的断言都带 `file:line`，核对时间 2026-10-15（分支 `feat-pause-resume`，基线 commit `a91e02f`）。
> 前置阅读：`AGENTS.md` §6.5（三档压缩）、§11 坑 3（目录与 `context_window` 打架）、§8.3（耗时必须由测试自己负责）。
> **本文不改三档压缩的动作，只改"什么时候触发"与"触发时花多少 CPU"。**
> 三档各自做什么、事件怎么发、`compacted` 记录怎么写，全部照旧。

---

## 0. 决策记录（已定，不再讨论）

| # | 决策点 | 选定方案 |
|---|--------|----------|
| D1 | 阈值口径 | `min(目录窗口×0.75, 配置值)`，**仅当配置值 > 0 时参与取小**；`YAGENT_COMPRESS_THRESHOLD` 仍在最前 |
| D2 | tier-1 记账 | 一次遍历得到逐条 token 数，之后每截一条只减它自己的 delta；不再每截一条全量重算 |
| D3 | 触发信号 | provider 上一步报的 `Usage.TotalInputTokens` 打底 + 此后新增消息的本地估算；无计量时回落到今天的全量估算 |
| D4 | 明确不做 | `non_protected/total > 0.7` 这道门（实测恒真，且瞄错了量，见 §9.1） |
| D5 | 明确不做 | reasoning echo 纳管 / 按类别预算（另立项，见 §9.2） |
| D6 | 明确不做 | 滞回与"这一档能不能达标"的判据（另立项，见 §9.3） |
| D7 | 落地节奏 | **P1 = D1+D2**（纯缺陷修复，可独立合）；**P2 = D3**（换计量口径，依赖 P1 的记账正确性） |
| D8 | 不加 `md.IsDefined` | 与 `[blackboard]`/`[resume]` 不同，这里 `0` 没有独立语义（关压缩是另一个键 `enable_context_compression`），所以 `configured > 0` 就是"用户给了上限"，不引入存在性追踪 |

D1 与 D3 都是**行为变更**（会让压缩在更低的位置触发、让判定数字换来源），所以 §7 的等价性测试与变异测试是硬约束，不是可选项。

---

## 1. 需求与语义定义

1. 用户在 `[enhanced_commander] context_compression_threshold` 写的数字必须生效 —— 今天它被内嵌目录顶掉（R1）。
2. 压缩触发时的本地开销必须与会话长度**线性**相关，不能是"截断条数 × 消息数"（R2）。
3. 触发判定用的 token 数应当是 provider 真正数出来的那个数：含工具声明、用 provider 的分词器，且不需要本地再算一遍全量（R3/R5）。
4. 三档的**动作**、事件、`compacted` 记录、`stats` 字段的语义一律不变 —— TUI 的 ⧉ COMPRESS 区块（`docs/token-compress-panel-design.md`）直接消费这些数字，口径变了面板就是错的。

---

## 2. 现状与根因（逐条带证据）

### R1 配置里的阈值被目录顶掉

`compression/threshold.go:56-67` 的优先级是 `env > 目录×0.75 > 配置 > 120000`。目录命中时配置值**完全不看**。
本机实测（`models.ContextWindow` + 用户的 11 个 provider）：8 个 provider 的模型是 1M 窗口 → 阈值 **750,000**，
而用户配置写的是 `120000`。`config/config.toml:211` 的模板注释把这个行为当成设计写下来了：
"回退值：模型收录于内嵌 models.json 时按「上下文窗口 * 0.75」自动推导，此项被忽略"。

### R2 tier-1 是 O(截断条数 × 消息数)

`truncate_results.go:44` 先全量估算一次；`:86-125` 的循环里，**每截断一条**就在 `:114` 和 `:115` 各做一次全量估算
（这两行还是同一个值算两遍）；`:127` 收尾再一次。
实测（200 条工具结果、129,101 token 的夹具）：单次全量估算 **43ms**，整个 `TruncateToolResultsToBudget` **9.28s**。
今天它不触发（R1 把阈值顶到 750k），所以这 9.28s 一直没被人看见 —— **D1 一旦落地，它就会开始收费**。

### R3 本地估算不含工具声明

`truncate.go:50-62` 只累加 `Content` + `Reasoning` + assistant 的 `ToolCalls[].Arguments`。
工具声明（`defsForStep()` 的产物）从来不进这个数。实测各 Agent 每步真的发出去的工具声明：

| Agent | 广播的工具数 | 声明体积 | 约合 token（deferred 开） |
|-------|------|------|------|
| coding | 30（deferred 后 20） | 23,575B | **3,781** |
| director | 17 | 16,275B | 3,606 |
| repo | 19（15） | 16,821B | 3,088 |
| browser | 19（15） | 13,025B | 2,569 |
| devops | 12 | 12,231B | 2,645 |
| chat | 8 | 8,514B | 1,819 |

即本地口径系统性少算 1.8k–5k token（120k 阈值下约 1.5%–4%）。
（表内数字用 `toolspec.BuiltinProfiles()` + `Profile.ProbeDeps()` 构造探针面、
`json.Marshal(Surface.Defs())` + `Surface.Catalog()` 测得；生产装配会因 deps 缺失跳过若干工具
（如未开 git_checkpoint、browser-control 不在场），实际值略小于此。）

### R4 触发判定有四个副本，外加一个没人调用的 helper

同一句"总 token 超过阈值吗"写在四个地方：`executor.go:504`（tier-2 入口）、`executor.go:542`（tier-3 入口）、
`planner_run.go:273`、`planner_run.go:301`。而 `compression/compressor.go:76-78` 的
`ContextCompressor.ShouldCompress` 是同一逻辑的第五份实现，**非测试代码里没有任何调用方**。
改判据必须同时改四处，漏一处的表现是"子 Agent 与 Director 的触发时机不一致"。

### R5 每步两次全量 tiktoken

即使什么都不截断，一个步也要付 `truncate_results.go:44` + 判定点各一次全量估算。
129k token 的上下文 ≈ **86ms/步**，且随上下文线性增长；这两次都发生在**重试循环内部**
（`executor.go:426` 的 `for attempt`），所以一次重试就再付一遍。

### R6 `ResolveThreshold` 有两个语义不同的调用方

| 调用方 | 传参 | 它要的语义 |
|--------|------|-----------|
| `executor.go:458` / `planner_run.go:235` | `(model, 配置的阈值)` | "我愿意在多长的上下文上继续跑" |
| `resume_delegation.go:187` | `(model, 0)` | "重放最多能塞多少进去"（`[resume] replay_token_budget = 0` 时） |

D1 的取小**只能**影响第一类。第二类传的是 `0`，按 D1 的规则（`configured > 0` 才参与取小）自然不受影响 ——
但这只是"恰好没事"，必须有测试把它钉住（§7 T2），否则下一个人给重放预算传个非零配置值就会连带改掉重放行为。

---

## 3. 实测基线：压缩不是延迟主因（先读这一节再动手）

从本机 `~/.yagent/data/rollout/*/​*.jsonl` 的 `event_msg.token_count.info.last_token_usage`
与记录时间戳统计，样本 = 2026-10-13 至 10-15 的 **610 个真实步 / 120 个会话**（已剔除 mock 与 `>600s` 的暂停 artifact）：

| 事实 | 数值 |
|------|------|
| 输入超过 120k 的步 | **6 / 610** |
| 输入超过 750k（当前实际阈值）的步 | **0 / 610** |
| 至少一步越过 120k 的会话 | **1 / 120**（峰值 131,458） |
| 缓存命中率中位（`cached_input_tokens / input_tokens`） | siliconflow GLM **93.2%**、aliyun DS 90.3%、V3.2 90.7%、autodl DS 87.8%、autodl GLM 68.6%、**Qwen3.5-35B-A3B 0%** |
| 输出 <1500 token 的步，按输入分桶的中位耗时 | 10–30k: 7.5s｜30–60k: 5.9s｜60–100k: 7.9s｜**>100k: 22.1s** |
| 每 1k **输出** token 的耗时（最小二乘） | 7–59s（大输出步中位 36.5s，输出中位 2,668） |
| 单步最慢 | coding / GLM-5.3-flash：输入 7,285、输出 **39,259**、**7 分 44 秒** |

三条结论，写在这里是为了防止后来者把压缩当性能杠杆：

1. **输出长度是延迟的全部**，输入 token 在 100k 以下几乎免费（前缀缓存 87–93% 命中在兜着）。
   输出无上限的根因已由 commit `a91e02f` 修掉（provider 的 `max_tokens` 此前从未上线）。
2. **750k 这个阈值从来没被撞到过**，所以 R1 是"配置骗人"的正确性缺陷，不是本次慢的原因。
3. 把阈值降到 120k 的**延迟收益接近零**（只影响那 6 步），代价是：一次 emergency 摘要（LLM 调用，
   `emergency.go:104` 上限 90s）+ 摘要重写前缀导致下一步 **0% 缓存命中的冷 prefill**。
   → 所以 D1 的价值是"让配置说真话"，不是"让系统变快"；**不要**为了性能把阈值再往下调。

复现方法（一次性脚本，不入库）：遍历 rollout，`turn_context.model` 取模型名，
`token_count` 取 `last_token_usage`，用相邻 `function_call_output → token_count` 的时间差近似单次 LLM 调用耗时。
会话末尾构成按 `response_item` 的 `function_call.name` 反查 `function_call_output.call_id` 归类。

**会话末尾的上下文构成**（chars 口径，取总量 >20k 的 12 个会话）：

| 形态 | 典型构成 |
|------|----------|
| repo / devops / browser | 工具结果 **71%–97%**，其余是 assistant 文本 |
| coding（GLM 系） | **reasoning echo 49%–70%**、工具结果 10%–39%、system 3%–10% |
| director | 工具结果 71%、system 10%、`call_args` 若干 |
| protected（`deepthinking`/`design`/`load_tool` 的结果） | **0.0%–3.5%**，多数会话是 0.0% |

最后一行是 D4 的依据；第二行是 §9.2（未做的 C）的依据 —— tier-1 只动 `Role=tool`
（`truncate_results.go:60-78`），reasoning echo 三档里没人管，而它现在是 coding 会话最大的一块。

---

## 4. 目标与非目标

### 目标

- G1 配置值参与阈值决定，且**取小**（两个来源都是上限）。
- G2 tier-1 的本地开销从 `O(截断条数 × 消息数)` 降到 `O(消息数 + 截断条数)`，结果与全量重算**逐字段相同**。
- G3 触发判定优先用 provider 计量，本地 tiktoken 只为"新增的那几条"付费。
- G4 四个判定点收敛到一个实现（R4）。

### 非目标

- 不改三档的动作、事件名、`compacted` 记录、`stats` 字段语义。
- 不改 `[enhanced_commander]` 的键集合，不加新配置项（D8）。
- 不碰 TUI（`compress_panel.go` / `tokenSnapshot` 的口径不变）。
- 不碰 emergency / ultimate **内部**的估算（`emergency.go:136,227,241,249`、`compressor.go:122,130,180`）：
  那些发生在压缩**之后**，`messages` 已经被重写，没有对应的 provider 计量值可用。
- 不做滞回、不做"这一档能不能达标"的判据（§9.3）。

---

## 5. 设计

### 5.1 D1 阈值：四象限

`compression/threshold.go:56` 的 `ResolveThreshold(model string, configured int) int` **签名不变**，
优先级改为：

| 目录命中（`window > 0`） | `configured > 0` | 结果 |
|---|---|---|
| 是 | 是 | **`min(window×0.75, configured)`** |
| 是 | 否 | `window×0.75` |
| 否 | 是 | `configured` |
| 否 | 否 | `DefaultContextCompressionThreshold`（120000） |

`envThreshold()`（`threshold.go:57`）仍在最前，语义不变（调试用，压过一切，取值不缓存）。

**影响面**：两个模板（`config/config.toml:211`、`internal/config/default_config.toml:176`）里这个键都是**注释掉的**
→ 新装用户 `configured = 0` → 走第二行，**行为不变**。只有显式取消注释的用户会拿到取小后的值。
本机用户写了 `120000` 且模型是 1M 窗口 → 750k 变 120k，这就是他要的。

**必须同步改的文档**：`config/config.toml:211` 那句"此项被忽略"是错的，改成"与目录推导值取小"；
`AGENTS.md` §6.5 第一段与 §11 坑 3（坑 3 描述的"两个消费者取法相反"要更新为"压缩侧取小，TUI 进度条口径不变"）。

### 5.2 D2 tier-1 增量记账

`TruncateToolResultsToBudget` 在 **P1 阶段签名与返回值不变**（`stats` 的每个字段语义不变），内部改为：
（P2 会按 §5.3 给它加一个 `prefix` 入参，那是唯一一次签名变更；`stats` 仍然只用本地口径，见 §5.3 末尾。）

```go
// 一次遍历：逐条 token 数 + 总和 + 可截断清单（按 prio 0 → 1 排序，与今天的顺序一致）
tokens := make([]int, len(messages))
total := 0
for i := range messages { tokens[i] = msgTokens(&messages[i]); total += tokens[i] }
if total <= maxTokens { return messages, nil }
// 逐条截断：只重算被截断那一条
for _, e := range toTruncate {
    // ... 截断 e.msg（与今天逐字相同的标记与文案，truncate_results.go:96-106）
    newTokens := msgTokens(e.msg)
    total -= tokens[e.index] - newTokens
    tokens[e.index] = newTokens
    if total <= maxTokens { break }
}
```

`msgTokens` 就是把 `truncate.go:50-62` 里"单条消息"的那三行抽出来（Content + Reasoning + assistant 的 args），
`EstimateMessagesTokens` 改为对 `msgTokens` 求和 —— 这样"逐条"与"全量"永远同源，不会出现两套口径。

**不变量**（每条都要有测试）：
- `stats.OriginalTokens` / `CompressedTokens` / `SavedTokens` / `SavedPercent` / `TruncatedCount` /
  `TruncatedTools[]` 与"每截一条就全量重算"的参考实现**逐字段相同**。
- 截断顺序不变：prio 0 全部先于 prio 1，组内保持消息原顺序（`:80-83` 的现有语义）。
- 已带 `TruncationMarker` 的消息仍然跳过（`:69-71`），`TruncationPass` 的自增语义不变（`:96-98`）。
- `total` 单调不增；循环最多遍历 `len(toTruncate)` 次。

`:115` 那次重复估算顺带消失（它和 `:114` 算的是同一个值）。

### 5.3 D3 provider 计量：`Prefix`

新增（放在 `compression/truncate.go`，与 `EstimateMessagesTokens` 同文件）：

```go
// Prefix 是"provider 已经替我们数过的那一段"。
// Tokens 来自上一步响应的 Usage.TotalInputTokens —— 这个字段已经是跨 provider 的统一口径
// （OpenAI 的 prompt_tokens 含缓存：engine_openai.go:253 流式 / :416 非流式；
//   Anthropic 的 input_tokens 不含，所以是三者之和：engine_anthropic.go:490）。
type Prefix struct {
    Tokens int64 // 0 = 没有可用计量
    Len    int   // 该计量覆盖 messages 的前 Len 条
}

// Total 返回本次请求的 prompt token 数：计量打底 + 新增部分本地估算。
func (p Prefix) Total(messages []llm.Message) int {
    if p.Tokens <= 0 || p.Len <= 0 || p.Len > len(messages) {
        return EstimateMessagesTokens(messages) // 回落 = 今天的行为
    }
    return int(p.Tokens) + EstimateMessagesTokens(messages[p.Len:])
}
```

**为什么必须加 delta 项**：只用 `Tokens` 会漏掉本步新增的消息，而一次 `read_file` 就能塞进几万 token ——
晚一步触发在极端情况下就是直接撞窗口。加上 delta 后既不滞后，本地计算又只花在新增的几条上（G3）。

**四个判定点 + tier-1 入口一起换**（R4 的收敛）：

| 位置 | 今天 | 改成 |
|------|------|------|
| `executor.go:504` | `EstimateMessagesTokens(messages) > threshold` | `prefix.Total(messages) > threshold` |
| `executor.go:542` | 同上 | 同上 |
| `planner_run.go:273` | 同上 | 同上 |
| `planner_run.go:301` | 同上 | 同上 |
| `truncate_results.go:44`（tier-1 入口，调用方在 `executor.go:464` 与 `planner_run.go:241`） | 全量估算 | 由调用方把 `prefix` 传进来，入口判定用 `prefix.Total` |

tier-1 需要 `prefix` 是因为**两个口径必须一致**：如果 tier-1 用本地口径判定"没超"、而 tier-2 用 provider 口径判定"超了"，
结果就是 tier-1 一条都不截、直接掉进 emergency（一次 90s 上限的 LLM 调用）。
所以 tier-1 的签名要带上 `prefix`（新参数或并入一个小的入参结构，二选一，**不要**保留两个入口）。
它的增量记账仍然用本地 delta：`total = providerBase + Σ本地(未计量部分) − Σ本地(被截断部分)`。

`ShouldCompress`（`compressor.go:76-78`）：改成 `Prefix.Total` 的薄封装并**真的被四个判定点调用**，
或者删掉。二者择一 —— 留着第五份没人调用的实现是 R4 的病灶。

**计量的生命周期**（这是本方案最容易写错的部分）：

| 时机 | 动作 |
|------|------|
| 一次 LLM 调用成功返回且 `resp.Usage != nil` | `prefix = Prefix{Tokens: resp.Usage.TotalInputTokens, Len: len(messages)}` |
| `resp.Usage == nil`（部分网关流式不返回 usage） | `prefix = Prefix{}` → 本步之后回落全量估算 |
| emergency / ultimate **重写了 messages** | `prefix = Prefix{}`（条数骤减，`Len` 已无意义），下一次响应再重建 |
| 重放进入循环（`ResumeState`） | `prefix = Prefix{}`，第一步必然全量估算 |
| 同一步的重试（`attempt > 0`） | **不重置**。messages 可能已被 tier-1 截短而 base 还是上一步的数 → base 偏大 → 更早触发（保守方向，可接受） |

已知的口径偏差，全部是保守方向（偏大 → 更早触发），**不要**试图修正，但要写在注释里：
- Director 的 `messagesForLLMCall`（`director/board_index.go:22-46`）在**送出去的那份**末尾追加便签索引，
  外层 `messages` 里没有它 → provider 数含索引、`Len` 不含 → base 偏大。
- 引擎内部的 `NormalizeMessages`（`llm/messages.go:13-38`）会丢空消息、合并连续 assistant →
  provider 数的是归一化后的列表，与外层 `messages[:Len]` 不是一一对应。
- tier-1 截断过的消息，其 provider 计数是截断前的 → base 偏大。
- 分词器不同（本地 cl100k_base vs provider 自己的）→ 百分之几量级的双向误差。

结论：`Prefix.Total` 只用于"是否超过阈值"这一个判断，**不得**被当成精确预算用于别处
（例如不要拿它去算 `stats.SavedTokens`；`stats` 一律用本地口径，否则 TUI 面板的百分比会自相矛盾）。

### 5.4 与重放预算的关系

`resume_delegation.go:187` 传 `configured = 0` → 按 §5.1 的第二行 → 仍是 `window×0.75`，行为不变。
这不是巧合而是 D1 规则的设计约束：**取小只在用户显式给了上限时发生**。§7 T2 钉住它。

---

## 6. 缓存交互：为什么触发要"少而准"

前缀缓存是本机 87%–93% 命中率的来源（§3）。任何一次压缩都会改写前缀 → 下一步是冷 prefill。
所以：

- 阈值取小是**用户显式要求**的结果，不是系统自作主张（D1 只在 `configured > 0` 时生效）。
- 触发之后要一次压够，避免"每步都压一点"（那是 D6/§9.3 的滞回要解决的事，本次不做）。
- Qwen3.5-35B-A3B 那条 provider 实测 **0% 命中** —— 对它来说输入 token 是全价的，
  压缩收益最大。但这是 provider 侧的事，代码不为单个 provider 开特例。

---

## 7. 测试与闸门

现有闸门全部照跑：`bash scripts/check_all.sh`（build + vet + 五把守卫 + 全量单测）。新增：

| # | 用例 | 断言 |
|---|------|------|
| T1 | `ResolveThreshold` 四象限 | §5.1 表格四行各一条；`env` 覆盖仍压过一切；`configured = 0` **不**参与取小 |
| T2 | 重放预算不受 D1 影响 | 以 `resume_delegation.go:187` 的传参形态（`configured = 0`）调用，断言结果 == `window×0.75`，且与 D1 之前逐字节相同 |
| T3 | 增量记账等价性 | 同一夹具下，新实现的 `stats` 与一个 test-only 的"每截一条全量重算"参考实现**逐字段相同**；截断顺序相同 |
| T4 | tier-1 耗时上界 | 200 条工具结果 / 129k token 的夹具，全程 **< 2s**（实测 ~45ms，留 40 倍余量避免 flaky）。夹具构造遵守 `AGENTS.md` §8.3：不靠 sleep |
| T5 | `Prefix.Total` | 有计量时结果 == `Tokens + 本地(新增)`；`Tokens<=0` / `Len<=0` / `Len>len(messages)` 三种情况都回落全量；**前缀里有超长内容时不触碰它**（用"巨大前缀 + 短 delta"的夹具，断言结果只随 delta 变化） |
| T6 | e2e 接线 | 脚本引擎在第一步响应里报 `TotalInputTokens = X`，把本地估算造得远小于 `X`，断言第二步**触发了**压缩。这是"四个判定点真的接上了"的唯一证据（参考 `AGENTS.md` §8.3 对 e2e 的要求：只 mock 模型，不 mock 被测组件） |
| T7 | 计量失效 | emergency 触发后 `prefix` 被清空（下一步回落全量估算）；`Usage == nil` 时同样回落 |

**变异测试要求**（每条都必须亲手做过一次并看到红，写进 PR 描述）：
- 把 §5.1 的 `min` 写成 `max` → T1 红。
- 让 `configured = 0` 也参与取小 → T2 红（重放预算变成 0/默认值）。
- 增量记账漏减 delta（`total` 不更新）→ T3 红。
- 把 `:114-115` 的全量重算加回去 → T4 红。
- `Prefix.Total` 的回落条件写反（有计量时仍全量估算）→ T5 红。
- 只改 `executor.go` 的判定点、漏改 `planner_run.go` → T6 红（Director 侧那条要单独跑一遍）。

**耗时归因**：新增用例若让 `internal/compression` 或 `internal/agents` 的墙钟明显上涨，
按 `AGENTS.md` §8.3 用 `-skip` 归因，不要猜；tiktoken 夹具一律 1–2k payload 起步（历史上 28s 的教训）。

---

## 8. 落地节奏

**P1（可独立合）**：D1 阈值 + D2 增量记账 + T1/T2/T3/T4 + 两个模板注释 + `AGENTS.md` §6.5/§11 坑 3。
理由：纯缺陷修复，自带等价性测试，不引入新口径。

**P2（依赖 P1）**：D3 `Prefix` + 四个判定点收敛 + T5/T6/T7。
理由：P2 换的是"判定数字从哪来"，出错的表现是"压缩早触发/晚触发"；只有 P1 的记账正确性先落地，
P2 的偏差才可归因。P2 落地后 §3 的实测应当重跑一次（同样 610 步口径），确认触发次数与冷 prefill 次数符合预期。

---

## 9. 未做（附实测依据，别当成遗漏）

### 9.1 明确否决：`non_protected / total > 0.7` 这道门

protected 只有三个工具：`deepthinking`、`design`（`tools/specs_cog.go:59,75`）与 `load_tool`（`tools/surface.go:551`）。
实测它们在会话末尾上下文里占 **0.0%–3.5%**，多数会话是 0.0% → 这道门恒真，等于没有。
更根本的问题是它瞄错了量："非保护"≠"可回收"。tier-1 能回收的只有 `Role=tool && prio>=0 && 未截断`
（`truncate_results.go:60-78`），而 Director 的 system prompt（10%）与 coding 的 reasoning echo（49%–70%）
都是"非保护但不可回收"——门会放行，tier-1 白跑，然后掉进 emergency。

### 9.2 未做 C：按类别预算 / 把 reasoning echo 纳管

实测最大的一块无人管：coding 会话 49%–70% 是 `reasoning_content` 回灌。但删除它跨 provider 语义不同：
DeepSeek 思考模式要求原样回灌（`engine_openai.go:303-317`），Anthropic 的 thinking 块与 `tool_use`
的连续性绑定（`engine_anthropic.go:205-235`）—— 删错就是 400。要做必须按 provider 开关，另立项。

### 9.3 未做 B：可回收量判据 + 滞回

D1 把阈值降到 120k 之后，"触发了但 tier-1 压不下去 → 掉进 emergency（LLM 调用，上限 90s，`emergency.go:104`）"
会从罕见变常见（§3 那 6 步里，coding 会话的可回收比例只有 10%–39%）。正确形态是：
每步一次遍历同时算出 total / tier-1 可回收量 / tier-2、3 可用性，选**第一个能压到达标**的档；
压完加滞回（回到 `0.6×threshold` 才允许再次触发）。它改的是决策逻辑而非口径，单独立项。

### 9.4 未做 E：延迟三件事（与本方案无关，但实测时间在那里）

- 两个引擎的自写 transport 没有 `ForceAttemptHTTP2`，而设了 `DialContext` → 实测协商成 **HTTP/1.1**
  （同址 `http.DefaultTransport` 拿到 h2）；也没有 `Proxy: http.ProxyFromEnvironment` → 忽略 `HTTPS_PROXY`。
  `engine_openai.go:45-57`、`engine_anthropic.go:113-124`。
- 退避 `baseDelay = 10s`、无 jitter、不读 `Retry-After`（`engine_openai.go:498`、`engine_anthropic.go:392`）。
- `reasoning_effort = "max"` 在 openai 侧原样透传 + 强制 `thinking:{type:enabled}`（`engine_openai.go:162-167`）：
  §3 那 39,259 token / 7m44s 的一步就是这么来的。降到 `medium` 是配置一行的事。
