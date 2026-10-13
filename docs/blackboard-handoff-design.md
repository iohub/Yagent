# 跨 Agent 交接重构：Handoff 协议 + 派生索引 设计方案

> **本文是实施规范**，读者是执行改动的 Coding-Agent。
> 所有"今天如何"的断言都带 `file:line`，核对时间 2026-10-13（分支 `feat-toolspec`）。
> 前置阅读：`AGENTS.md` §6.4（Blackboard/Artifact）、§9.3（新增工具的正确步骤）、`docs/tool-registry-design.md` §8（一致性闸门）。

---

## 0. 决策记录（已定，不再讨论）

| # | 决策点 | 选定方案 |
|---|--------|----------|
| D1 | 方案形态 | **B+C 一次性落地**：B = 索引即派生态（自动注入 + `list_notes`），C = 结构化 Handoff（便签从散文变 typed 交付物）。不做"先修管道"的分期版 |
| D2 | Handoff 采集入口 | **`agent_exit` 增可选 `handoff` 对象**。它是 `RequiresExit` 的强制路径，LLM 无法跳过；不新增 `finalize_handoff` 工具（会被跳过） |
| D3 | boardID 作用域 | **tab/session 级共享**：同一 TUI tab 内开新任务、HTTP 同一 chat session 都复用同一 board；`--prompt` 单发回落 taskID |
| D4 | 索引注入范围 | **Director 每轮也注入**（不只是委派时给子 Agent）。索引每轮从磁盘重扫，因此压缩/resume 都伤不到它 |
| D5 | 预算与 GC | 进 **`[blackboard]` 配置表**（`index_max_notes` / `index_line_chars` / `summary_max_runes` / `ttl` / `max_notes_per_board`），Init 时 GC |
| D6 | 便签正文 | **恒为交付物全文**。artifact 只继续服务 Director 自己的上下文预算，不再是便签正文的来源 |
| D7 | 落地节奏 | **单批次落地**，内部按 §13 的次序执行并逐步验证 |

D1 与 D3 是风险最高的两项，因此 §11 的一致性闸门是**硬约束**，不是可选项。

---

## 1. 现状与根因（逐条带证据）

链路：子 Agent 产出 → `FinalizeResult` 分级 → Director 上下文 + 便签 + artifact → 下一次委派经 `context_refs` 组装 ContextPack → 下一个子 Agent。信息在**六级**上被削掉。

### R1（最致命）`agent_exit` 收尾的 Agent，交付物全文塌缩成退出 JSON

`executor.go:759-761`：
```go
if cfg.StopOnFinish && tc.Function.Name == "agent_exit" {
    return &ExecutorResult{Text: toolResult, History: history}
}
```
`toolResult` 是 `ExecuteAgentExit`（`tools/flow_control.go:25-34`）返回的 `{"finished":true,"reason":...}` 的 JSON 序列化。
设了 `StopOnFinish = true` 的是 **Coding**（`coding.go:346`）、**Browser**（`browser_agent.go:116`）、**DevOps**（`devops.go:82`）、**Chat**（`chat.go:73`）、**Director**（`director.go:356`）——而这四个子 Agent 的 prompt 都要求以 `agent_exit` 收尾。

后果：这四个 Agent 的真实分析（散在中间 assistant 消息里）**既不进 `Text`、也不进 artifact、也不进便签**。
磁盘实证（board `0421af6d-03c5-4d11-a620-7bb952ddc244`）：
- `coding_agent/20261011T003741Z_8cd1.md`：`body_size = 189`，正文 = `{"finished":true,"reason":"任务完成：TUI 模型选择对话框空格=永久切换…"}`
- `devops_agent/…004028Z_61b3.md`（183）、`devops_agent/…004825Z_d57d.md`（183）同形态

对照组：`repo.go` **没有**设 `StopOnFinish` ⇒ Repo 的 `Text` 是最后一条 assistant 散文 ⇒ repo 便签有真实正文（1255/1571 字符）。**同一个 board 里两种 Agent 的便签质量差一个数量级**，这就是"读 notes 只拿到部分信息"的第一位原因。

### R2 便签正文 = artifact 摘要的前缀，不是全文

`director.go:502-505`：
```go
body := result.Text
if result.ArtifactRef != nil {
    body = result.Summary   // ← 只有 Reduce 出来的 500 token 前缀
}
```
`result.Summary` 来自 `result.go:84` 的 `artifact.Reduce(exec.Text, ref.ID, DefaultBudgetTokens)`，而 `artifact/summary.go:43-85` 是**保留前缀、砍掉尾部**（marker 行在 `:83`）。分析的结论、file:line 清单恰恰都在结尾。

实证：`repo_agent/20261011T001842Z_4a24` 正文 1255 字符 ↔ artifact 全文 **4830**；`devops_agent/…004613Z_b58c` 正文 1685 ↔ artifact 全文 **15083**。
全量统计（`~/.yagent/notes`，139 张便签）：**25 张是这种残片正文，平均只保留原文 29.2%**。

### R3 ContextPack 是 2000 字符全有或全无，超出就只剩 200 字符开场白

`resolve.go:9-14`：`BudgetTotalChars=6000` / `BudgetNoteInline=2000` / `BudgetAgentMaxNotes=3` / `BudgetMaxRefs=10`。
`resolve.go:175-179`（`loadNoteEntry`）：正文 > 2000 **或** > 剩余预算 → `Truncated = true`，**不做任何部分内联**。
`resolve.go:55`（`Text()`）truncated 分支只打印 `e.Summary`，而 Summary 在写入时已被 `note.go:63,92-93` 的 `maxSummaryLen = 200` 砍过一刀（`truncateRunes` 取前缀 + `…`）。

实证：上述 25 张残片便签的 `summary` **100% 命中 200 rune 截断**，内容形如：
```
基于我的分析，我现在可以回答您的问题了：\n\n## 分析结果\n\n### 1. config 包中是否有写入/更新 config.toml 的现有 API？\n\n**答案：没有专门的写入…
```
200 字符里装的全是客套前缀——因为 `Reduce` 与 `truncateRunes` 都取前缀。

### R4 `agent:<name>` 名字对不上时静默返回空，连 unresolved 都不报

便签目录名是 `repo_agent` / `coding_agent` / `chat_agent` / `devops_agent` / `browser_agent`（`director_surface.go:36-44` 的 `noteAgent`），而 schema 与 `director.prompt.md:86` 教的格式是 `"agent:<name>"`，模型自然写 `agent:repo`。链路：

1. `resolve.go:128-132` `resolveAgentRef` → `ListNotes(boardID, "repo", 3)`
2. `note.go:140-146` → `listNotesInDir` 对不存在的目录返回 `nil, nil`（`note.go:178-183`）
3. `resolveAgentRef` 返回 nil entries，**且不往 `pack.Unresolved` 追加**
4. `director.go:524-539` `resolveDelegateRefs` 拿到空 pack → `unresolvedCount = 0` → `FormatForDirector`（`result.go:117-119`）连 `| unresolved refs: N` 都不打印

Director 以为带上了上下文，子 Agent 拿到裸任务，**没有任何信号**。
`resolve_test.go:86-92` 的 fixture 用 `Agent: "repo"` 造数据，所以这个不对称一直在测试盲区里。

### R5 压缩后引用归零

`compression/emergency.go:142-157` 只从 **assistant** 消息抽 T&P 块，`:216-223` 重建为 `[system, one user]`。带 `note:` / `artifact:` 的 `[Sub-Agent Result]` 是 **tool** 消息 ⇒ 既不进 LLM 摘要也不进保留区。ultimate 档同理（`thinklink/thinklink.go:131-166` 的原料只有 user 输入 + T&P）。
⇒ **一旦触发第二/三档压缩，Director 手上所有便签引用消失**，只能重新委派一次才能拿回已有结论。
第一档（`truncate_results.go:36-106`）只截正文、保前缀，`note:`/`artifact:` 在 header 首两行 ⇒ 危害小得多。`delegate_*` 未声明 `Trunc`（`agents/specs.go:77-88`）⇒ `TruncNormal`。

### R6 无发现机制 + 作用域断裂 + 便签黑洞

- **没有 `list_notes` 工具**（全仓 grep 无命中）。子 Agent 六个画像都 Grant 了 `EffectContext`（`toolspec/profiles.go`），golden 里也都有 `read_note`/`read_artifact`（`testdata/tool_surfaces.golden`），但它**无法枚举便签板上有什么**，只能消费 Director 主动传的 ref。传不传全靠 `director.prompt.md:86` 一句软要求，代码层无强制、无自动附带。
- **prompt 与工具面矛盾**：`repo.prompt.md:103` 写着 "You **cannot** read from or write to the shared blackboard"，而 golden 里 Repo 持有 `read_note`/`read_artifact`。最该复用前序结论的审计型 Agent 被 prompt 劝退。
- **board = taskID**：`director.go:408-411` `boardID = SanitizeBoardID(taskID)`，`app.go:580-595` 每请求重设。断点有三处：HTTP 每请求新 uuid（`http/task_manager.go:44`）、TUI 里 skill 调用走 `submitTaskWithContent`（`tui_update_keys.go:1001`）、tab 内 `m.currentTask == nil` 时开新任务（`tui_tasks.go:33-36,66`）。
  **注意 `/resume` 不断链**：`tui_resume.go:433` 用 `rolloutFirstNonEmptyStr(info.TaskID, info.SessionID)` 复现原 taskID，`rollout_writer.go:153-154` 保证 meta 里有它 ⇒ board 复现。设计时不要把 resume 当成待修项。
- **只有一处写便签**：`writeDelegateNote` 唯一调用点是 `director_surface.go:143`。Coding→Browser 的二级委派（`coding.go:176-192`）直接 `return result.Text`，**既不写便签也不解析 ref**；`runCustomAgent`（`director_surface.go:231`）与 `runMetaDesign` 同样不写。Browser 的产出是最易失的。
- **artifact 条目零信息**：`resolve.go:186-195` `resolveArtifactRef` 的 `Size` 硬编码 0，不 `Load`；`resolve.go:60` 于是打印 `(0 chars)`。`PackEntry`（`resolve.go:26-33`）没有 `ArtifactIDs` 字段 ⇒ 被截断的便签条目把 artifact 线索也丢了，子 Agent 必须先 `read_note` 看到 `artifacts:` 行（`read_note_tool.go:124-126`）才能二跳。
- **notes 无 GC**：`blackboard/` 全包没有 ttl/cleanup（现存 98 个 board / 139 张便签，只增不减）。

---

## 2. 目标与非目标

### 目标

1. **结论可寻址**：交付物的"结论"是一个字段（`Handoff.Conclusion`），不是需要读到第 4700 个字符才碰到的散文尾部。
2. **发现权是系统义务**：任何 Agent 一落地就看到本 board 的索引，不依赖 Director 记得传 `context_refs`。
3. **引用抗压缩**：索引每轮从磁盘重扫、且**瞬态**（不进 `ConversationMemory`、不进 rollout）⇒ 第二/三档压缩无法让它消失。
4. **静默失效 → 可见失效**：ref 解析不到必须上报（`unresolved`），`agent:repo` 与 `agent:repo_agent` 必须同义。
5. **交付物全文不塌缩**：`agent_exit` 收尾的 Agent，其真实产出必须进便签与 artifact。
6. **便签板有生命周期**：TTL + 每 board 条数上限，作用域提到 session 级。

### 非目标

- 不改 `Adapter.Call()` 执行语义、不改 `toolbatch` 调度算法。
- 不改 artifact 的落盘格式（`schemaVersion = 1` 不动）与 ID 形态。
- 不改 `ExecutorResult.Text` / `AgentResult.Text` 的既有语义（`result.go:12-19` 的注释是契约：程序化消费走 Text，LLM 上下文走 Summary）。
- 不新增事件类型 ⇒ **不碰 `protocol/agent-events.yaml` 与 `internal/protocol/agent_events.go`**。
- 不做 `go mod tidy`。
- 不给 Repo 补 `StopOnFinish`（见未决项 U4，属独立变更）。

---

## 3. 数据模型：Handoff 与便签 v2

### 3.1 `internal/blackboard/handoff.go`（新增）

```go
// Handoff 是一次委派的结构化交付物。便签 front-matter 的 canonical 内容，
// 也是 ContextPack / read_note / Director 摘要的共同渲染源。
type Handoff struct {
    Conclusion    string   `json:"conclusion"`               // 一句话结论，≤ MaxConclusionRunes
    Facts         []Fact   `json:"facts,omitempty"`          // 结构化事实，≤ MaxFacts 条
    Artifacts     []string `json:"artifact_ids,omitempty"`
    OpenQuestions []string `json:"open_questions,omitempty"` // ≤ MaxOpenQuestions 条
    Status        string   `json:"status"`                   // ok | error | partial
    Degraded      string   `json:"degraded,omitempty"`       // "" | missing_fields | absent | v1
}

type Fact struct {
    Claim string `json:"claim"`           // ≤ MaxFactClaimRunes
    Where string `json:"where,omitempty"` // file:line / 函数名 / URL，≤ MaxFactWhereRunes
}

const (
    MaxConclusionRunes  = 200
    MaxFacts            = 12
    MaxFactClaimRunes   = 160
    MaxFactWhereRunes   = 120
    MaxOpenQuestions    = 5
    DefaultRenderRunes  = 1200 // Director 侧摘要预算
    IndexConclusionRunes = 80  // 索引行里的结论预算
)

// ParseHandoff 三档回落，永不返回 error（绝不阻断委派）。
//   argsJSON     = agent_exit 的原始 arguments（可能没有 handoff 键）
//   reason       = agent_exit 的 reason 字段
//   fallbackText = 交付物全文（argsJSON/reason 都拿不到结论时取尾部）
func ParseHandoff(argsJSON []byte, reason, fallbackText string) Handoff

// Normalize 裁剪长度与条数、去空、去重（Facts 按 Claim 去重）。ParseHandoff 内部已调用，
// 程序化构造 Handoff 时（如 v1 兼容路径）需显式调用。
func (h Handoff) Normalize() Handoff

// Render 渲染成确定性文本块。maxRunes <= 0 时用 DefaultRenderRunes。
// 超预算时**逐条丢 Fact**（并在末尾标注丢弃条数），绝不截断 Conclusion。
func (h Handoff) Render(maxRunes int) string
```

`ParseHandoff` 的三档（**必须逐档写单测**）：

| 档 | 输入 | 输出 | `Degraded` |
|----|------|------|-----------|
| 1 合规 | `handoff` 对象存在且 `conclusion` 非空 | 原样 `Normalize()` | `""` |
| 2 缺字段 | `handoff` 存在但 `conclusion` 空 | `Conclusion = reason`；其余字段保留 | `"missing_fields"` |
| 3 完全没有 | 无 `handoff` 键 / JSON 解析失败 | `Conclusion = reason`；`reason` 也空则取 `fallbackText` 尾部 `MaxConclusionRunes`；`Facts = nil` | `"absent"` |

`Render` 的输出形态（确定性，禁止依赖 map 迭代顺序）：
```
结论: <Conclusion>
事实:
- <Claim>  @ <Where>
- <Claim>
待确认: <OpenQuestions 用 " / " 连接>
状态: <Status>
```
`Degraded != ""` 时**不渲染该字段**（避免把内部降级标记泄漏给 LLM），但 `read_note` 的 header 行要显示（见 §7.2）。

### 3.2 便签 v2（`note.go`）

```go
const noteSchemaVersion = 2   // 今天是 1（note.go:14）

type Note struct {
    Version     int      `json:"version"`
    // ……既有字段全部保留（NoteRef/BoardID/Agent/Type/CreatedAt/Tool/Task/Summary/ArtifactIDs/BodySize/Status）
    Handoff     Handoff  `json:"handoff"`
    Body        string   `json:"-"`
}
```

- `Summary` 字段**保留并继续写入**，值 = `Handoff.Conclusion`。它是 v1 读者的兼容位，也是 `NoteMeta` 的一行摘要来源。
- `maxSummaryLen`（`note.go:63`）从 200 提到 `MaxConclusionRunes`（同为 200，改成引用常量，消灭魔数重复）。
- `NoteInput` 增 `Handoff Handoff` 字段；`Body` 语义改为**调用方保证是交付物全文**，`WriteNote` 内部不再用 `Summary` 兜底覆盖（删除 `note.go:79-82` 的 `if body == "" { body = input.Summary }`，改为 `if body == "" { body = input.Handoff.Render(0) }`）。
- **`decodeNote` 的 v1 兼容**（`note.go:241-261`）：`Version < 2` 时构造
  `Handoff{Conclusion: n.Summary, Artifacts: n.ArtifactIDs, Status: n.Status, Degraded: "v1"}`。
  存量 139 张便签不作废、不迁移、不重写。
- `toMeta()`（`note.go:203-216`）增 `Conclusion string`（= `Handoff.Conclusion`）与 `Degraded string`。

---

## 4. 修复交付物全文（R1）

### 4.1 `agents/result.go`：`deliverableText`

```go
// deliverableText 返回本次委派的真实交付文本。
// agent_exit 收尾时 ExecutorResult.Text 只是退出 JSON（executor.go:759-761），
// 真实答案在最后一条有内容的 assistant 消息里。
func deliverableText(exec ExecutorResult) string
```

判定规则（**只看形态，不看 Agent 名字**）：
1. `trimmed := strings.TrimSpace(exec.Text)`；若 `trimmed` 以 `{"finished"` 开头且能 `json.Unmarshal` 成 `map[string]interface{}` 且含 `finished` 键 ⇒ 认定为退出 JSON。
2. 认定成立 ⇒ 从 `exec.History` 尾部向前找第一条 `Role == assistant` 且 `strings.TrimSpace(Content) != ""` 的消息，返回其 `Content`。
3. 找不到 ⇒ 返回 `exec.Text`（今天的行为，安全退化）。
4. 不认定 ⇒ 返回 `exec.Text`。

**不改 `ExecutorResult.Text` / `AgentResult.Text`**（§2 非目标）。只有便签正文与 artifact 全文走 `deliverableText`。

### 4.2 `FinalizeResult` / `FinalizeResultFull` 改造（`result.go:50-104`）

签名增一个 handoff 入参（`AgentResult` 增 `Handoff *blackboard.Handoff` 字段）：

```go
func FinalizeResult(agentName, task string, exec ExecutorResult, h *blackboard.Handoff) AgentResult
```

分级逻辑（**kill-switch 与空文本分支保持原样**）：

| 条件 | `Text` | `Summary` | `ArtifactRef` |
|------|--------|-----------|---------------|
| `artifact.Disabled()` 或 `full == ""` | `exec.Text` | `exec.Text` | nil |
| `EstTokens(full) <= 500` | `exec.Text` | `full` | nil |
| 超预算，`h != nil && h.Degraded == ""` | `exec.Text` | `h.Render(0)` | 落盘 `full` |
| 超预算，handoff 缺失/降级 | `exec.Text` | `artifact.Reduce(full, id, 500)`（今天的行为） | 落盘 `full` |

其中 `full := deliverableText(exec)`。⇒ **artifact 里存的从此是真实交付文本**，不再是退出 JSON。

`FinalizeResultFull` 同步增参，`r.Text = exec.Text` 的语义不变（`result.go:100-104`）。

### 4.3 `writeDelegateNote`（`director.go:490-522`）

```go
body := deliverableText(result)          // 删除 :502-505 的 artifact 分支
noteRef, err := blackboard.WriteNote(blackboard.NoteInput{
    BoardID:     a.boardID,
    Agent:       agentName,
    Tool:        toolName,
    Task:        task,
    Summary:     result.Handoff.Conclusion,  // 兼容位
    Body:        body,
    ArtifactIDs: artifactIDs,
    Status:      status,
    Handoff:     *result.Handoff,            // 新增
})
```
`result.Handoff` 为 nil 时用 `blackboard.Handoff{Status: status}`（`WriteNote` 内部会 `Normalize`）。

### 4.4 Coding→Browser 的二级委派补便签（`coding.go:176-192`）

`browserDelegate` 今天直接 `return result.Text`。改为与 `delegateTo` 同构的最小三步：
1. `packedTask` = `resolveRefs`（复用 Director 传下来的能力：给 `CodingAgent` 注入一个 `RefResolver` 窄接口，见下）+ 索引块；
2. `result, err := a.BrowserAgent.Run(ctx, packedTask)`；
3. 写便签（`noteAgent = "browser_agent"`，`tool = "delegate_browser"`），返回值改为 `FormatForDirector` 同格式的文本。

**分层约束**：`coding.go` 不得 import `director/` 子包（`scripts/check_no_agents_import.sh` 守的是反向，但 `agents` → `director` 已有依赖，注意别造环）。窄接口加在 `agents/ports.go`：
```go
// BoardWriter 让子 Agent 的二级委派也能落便签（Coding→Browser）。
type BoardWriter interface {
    WriteDelegateNote(agentName, toolName, task string, r AgentResult, runErr error) string
    ResolveRefs(ctx context.Context, task string, refs []string) (packedTask string, unresolved int)
}
```
由 `DirectorAgent` 实现并在装配时注入 `CodingAgentParams`（`app.go` 的 Coding 构造处）。

---

## 5. Handoff 采集（D2）

### 5.1 schema（`internal/toolspec/schemas.json`）

`agent_exit` 增**可选** `handoff` 对象（`required` 仍只有 `reason`，旧调用零影响）：

```json
"handoff": {
  "type": "object",
  "description": "Structured handoff for the next agent. STRONGLY recommended: conclusion is the only part guaranteed to survive context compression. facts must be verifiable claims (file:line / symbol / URL), never guesses.",
  "properties": {
    "conclusion":     {"type": "string",  "description": "One-sentence answer to the delegated task (<=200 chars)."},
    "facts":          {"type": "array", "maxItems": 12, "items": {"type": "object", "properties": {
                          "claim": {"type": "string", "description": "One verifiable fact (<=160 chars)."},
                          "where": {"type": "string", "description": "file:line, symbol name, or URL backing the claim."}},
                          "required": ["claim"]}},
    "open_questions": {"type": "array", "maxItems": 5, "items": {"type": "string"}},
    "status":         {"type": "string", "enum": ["ok", "partial", "error"]}
  }
}
```

**`agent_exit` 的 Spec 不动**（`tools/specs_flow.go:13-26`：`EffectFlow` / `Hot` / `Serial`）。schema 改完 `TestRegistryMatchesSchemas` 仍应 1:1 通过——它对齐的是"名字集合"，不是参数逐字。

### 5.2 `ExecuteAgentExit`（`tools/flow_control.go:25-34`）

只做**透传与可观测**，不做解析：
```go
result := map[string]interface{}{"finished": true, "reason": reason}
if h, ok := params["handoff"]; ok {
    result["handoff"] = h
}
return result, nil
```
（`reason` 的类型校验保持原样：非 string 仍返回 `util.WrapError`。）

### 5.3 executor 采集点（`executor.go:750-763`）

唯一采集处，在 `execOneToolCall` 的 stop 分支：
```go
if cfg.StopOnFinish && tc.Function.Name == "agent_exit" {
    h := blackboard.ParseHandoff([]byte(tc.Function.Arguments), exitReason(toolResult), lastAssistantText(history))
    return &ExecutorResult{Text: toolResult, History: history, Handoff: &h}
}
```
`ExecutorResult`（`executor.go:70-73`）增 `Handoff *blackboard.Handoff`。
**注意**：`tc.Function.Arguments` 是 `json.RawMessage`（`convertToolCalls`，`director.go:548-561`），此处直接取原始字节，不要二次序列化。

`exitReason` / `lastAssistantText` 是本文件内的私有小工具函数（各 ≤15 行）。

各 Agent 的 `Run()` 把 `exec.Handoff` 透传进 `FinalizeResult`（§4.2）。**没有走 agent_exit 的路径**（循环因纯文本回复结束、步数耗尽、panic recover）⇒ `Handoff` 为 nil ⇒ `FinalizeResult` 走"handoff 缺失"档 ⇒ 今天的行为。

---

## 6. 索引即派生态（D4）

### 6.1 `internal/blackboard/index.go`（新增）

```go
type IndexOptions struct {
    MaxNotes  int // <=0 → DefaultIndexMaxNotes (12)
    LineChars int // <=0 → DefaultIndexLineChars (160)
}

const (
    DefaultIndexMaxNotes  = 12
    DefaultIndexLineChars = 160
)

// BuildIndex 从磁盘扫描 board，渲染 newest-first 的便签索引。
// 返回 (索引文本, board 内便签总数, error)。board 不存在 → ("", 0, nil)。
func BuildIndex(boardID string, opts IndexOptions) (string, int, error)
```

**实现要点（性能相关，必须照做）**：
- 文件名是 `20060102T150405Z_<rand4>.md`（`board.go:118-125`）⇒ **字典序 = 时间序**。先按目录列举 + 文件名字典序倒排，**只 decode 前 `MaxNotes` 个**，不要像 `ListNotes`（`note.go:134-201`）那样把所有文件读进来再排序。
- 总数用目录列举计数（不 decode）。
- 单个文件 decode 失败 ⇒ 跳过并计入 `skipped`，不中断（与 `listNotesInDir:194-197` 的容错一致）。

行格式（每条 note 一行，`|` 分隔，逐段裁剪到 `LineChars` 总预算）：
```
repo_agent/20261011T002445Z_be45 | delegate_repo | 第二轮定向调查(TUI 模型持久化) | TOML 库为 BurntSushi v1.5.0，解码走 hot_reload… | 1571c | artifact:20261011-002445-8cb469c1
```
段位：`note_ref | tool | task(≤40 rune) | conclusion(≤IndexConclusionRunes) | body_size c | artifact:<id>`（无 artifact 则省略末段）。
`Degraded == "v1"` 的便签，conclusion 段用 v1 兼容值（`Summary`）。

首尾固定：
```
## Board Index (<boardID>, <total> notes, newest first — call read_note(ref) for full text, list_notes(agent) to page)
<lines>
[<total - shown> older notes not listed — call list_notes() to page]
```
`total <= shown` 时省略末行。空 board ⇒ 返回 `""`（调用方不得注入空块）。

### 6.2 注入点一：委派时（`director_surface.go:110-145`）

`delegateTo` 里 `resolveDelegateRefs` 之前先建索引，顺序固定为 **索引 → 显式 refs 的 ContextPack → `---` → task**：

```go
packedTask, unresolved := a.resolveDelegateRefs(ctx, task, params)  // 既有
if idx, _, _ := blackboard.BuildIndex(a.boardID, a.indexOpts()); idx != "" {
    packedTask = idx + "\n\n" + packedTask
}
```
`resolveDelegateRefs`（`director.go:524-539`）内部的 `packText + "\n---\n" + task` 组装顺序保持不变。
`a.indexOpts()` 从 `[blackboard]` 配置读（§9）。

`context_refs` 语义**降级为强调**：传了就在 ContextPack 里内联全文摘录，不传也能从索引发现。`director.prompt.md:86` 同步改写（§10）。

### 6.3 注入点二：Director 每轮（`director/planner_run.go`）

**不变量（本方案的核心，写进代码注释）**：索引是**派生态**，每轮从磁盘重扫，**只进 LLM 调用的 messages，绝不写进 `ConversationMemory`、绝不写进 rollout**。因此第二/三档压缩（R5）无法让它消失，`/resume` 也不会累积历史索引。

实现位置约束：
- `director/planner_run.go` **今天已 652 行**，超 `scripts/check_file_sizes.sh` 的 300 行上限（`docs/tool-registry-design.md` §10.5 记录的存量违规）。**只允许在这里加 1-3 行 hook**，逻辑本体放新文件 `internal/agents/director/board_index.go`（≤300 行）。
- hook 形态：在每轮构造 LLM messages 处（`planner_run.go` 里 messages 组装完成、调用 engine 之前）插入
  ```go
  msgs = p.withBoardIndex(ctx, msgs)   // board_index.go
  ```
- `withBoardIndex` 的规则：把索引块作为**一条独立的 `RoleUser` 消息**插在"最后一条 user 消息之前"；每次调用重新 `BuildIndex`（不缓存跨轮）；同一步内重复调用必须幂等（先剔除上一条自己插入的索引消息——用固定前缀 `## Board Index (` 识别）。
- `director/` 子包**禁止 import `internal/agents`**（`scripts/check_no_agents_import.sh`）。`blackboard` 是 leaf 包，可以直接 import。
- 索引为空（board 无便签）⇒ 原样返回 msgs，不插空消息。

### 6.4 子 Agent 侧

子 Agent **不注入索引**：它拿到的 `packedTask` 已经带索引（§6.2），且它有 `list_notes` 可自主发现（§7.1）。给子 Agent 再注入一份等于双倍 token。

---

## 7. 读取侧

### 7.1 新增 `list_notes` 工具

按 `AGENTS.md` §9.3 的六步走，**一步都不能省**：

1. 实现 `internal/agents/list_notes_tool.go`：
   ```go
   func executeListNotes(ctx context.Context, params map[string]interface{}, getBoardID func() string) (interface{}, error)
   ```
   参数：`agent`（可选，别名归一见 §7.4）、`limit`（默认 20，硬上限 50）、`offset`（默认 0）。
   输出：每条一行 `note_ref | tool | created_at | status | body_size c | conclusion(≤80 rune)`，尾部 `[more: N left; continue with offset=M]`。
   **只读 front-matter + Handoff.Conclusion，绝不返回 Body**（这是它便宜的原因）。
   错误一律返回友好文本（对齐 `readNoteErrorText`，`read_note_tool.go:136-138`），不返回 Go error。
2. `internal/toolspec/schemas.json` 加条目（name/description/parameters）。
3. Spec 注册在 `agents/specs.go` 的 `registerContextSpecs()`（`:116-146`）里加一行 —— `Effect: EffectContext` / `Frequency: Warm` / `Available: d.HasBoard()` / `Build` 闭包捕获 `d.BoardID`，与 `read_note` 完全同构。**不要新建按名索引的表**。
4. 工具面：六个画像已 Grant `EffectContext`（`toolspec/profiles.go`）⇒ **自动出现在六个 Agent 上**，无需改画像。`tool_surfaces.golden` 必然变化 ⇒ 按 §11 显式改写。
5. 工具级引擎：不需要（无 LLM 调用）。
6. 测试：`internal/agents/list_notes_tool_test.go`（分页、agent 过滤、空 board、无 boardID）。

`toolspec.Spec` 的 `Mutates=false`、`Serial` 不设 ⇒ 派生为**可并发**（`AGENTS.md` §5.1）。纯本地文件读，无 `mcp sendJSON` 那类共享写端，安全。

### 7.2 `read_note` 渲染 handoff（`read_note_tool.go`）

header（今天 `:122-126`）改为：
```
note <ref> | agent: <agent> | status: <status> | <total> lines | showing <a>-<b>[ | degraded: <d>]
artifacts: <ids>
--- handoff ---
<Handoff.Render(0)>
--- body ---
<分页正文>
```
`degraded` 段仅在 `Degraded != ""` 时出现（让 LLM 知道这条是 v1 存量或降级产物，结论可能不完整）。
分页常量（`:13-17`）不动。

### 7.3 `resolve.go` 四项修复

1. **`PackEntry`（`:26-33`）增字段**：`ArtifactIDs []string`、`ArtifactSummary string`。
2. **`resolveArtifactRef`（`:186-195`）真正取值**：签名改为接收一个 `ArtifactInfoFn func(id string) (summary string, charCount int, ok bool)`（替换今天的 `ArtifactExistsFn`，`:69-70`），由 `director.go:542-546` 的 `artifactExists` 升级实现（`Load` 一次拿 `Summary` + `CharCount`）。`Size` 不再硬编码 0。
   **分层约束**：`blackboard` 不得 import `artifact`（保持今天的回调解耦风格）。
3. **`loadNoteEntry`（`:153-184`）改为渲染 handoff，不再砍成 200 字符前缀**：
   - 预算充足（`body <= BudgetNoteInline` 且 `<= remaining`）⇒ 内联全文（今天的行为）。
   - 预算不足 ⇒ 内联 `Handoff.Render(min(remaining, DefaultRenderRunes))`，`Truncated = true`，并在 `Text()` 里保留 `read_note(ref)` / `read_artifact(id)` 提示 + `ArtifactIDs`。
   - `Handoff.Degraded != ""`（v1 存量或降级）⇒ 回落到**头尾摘录**：前 60% 预算 + `\n…[middle omitted]…\n` + 后 40% 预算。这是 v1 便签唯一能给出结论的机会。
4. **`Text()`（`:45-67`）**：artifact 条目打印真实 `Size` 与 `ArtifactSummary` 首行；note 条目在 truncated 分支额外打印 `artifacts: <ids>`。

预算常量（`:9-14`）保持数值不变，但改为可被 `[blackboard]` 覆盖（§9）——用 `IndexOptions` 同风格的 `ResolveOptions` 传入，常量降级为默认值。

### 7.4 agent 名别名归一（R4）

`resolveAgentRef`（`:128-142`）改为按候选名依次尝试，**全部落空才计入 `Unresolved`**：
```go
func agentNameCandidates(name string) []string // 去重、保序
// "repo"       → ["repo", "repo_agent"]
// "repo_agent" → ["repo_agent", "repo"]
```
规则：原名 → 加 `_agent` 后缀 → 去 `_agent` 后缀。**不在 blackboard 里硬编码 Agent 名单**（`delegateTargets()` 是那份名单的唯一来源，`director_surface.go:36-44`；blackboard 是 leaf 包，不能反向依赖）。

`ResolveRefs`（`:94-114`）的 `case "agent"` 分支必须补上 unresolved 处理：
```go
entries := resolveAgentRef(boardID, value, &totalChars)
if len(entries) == 0 {
    pack.Unresolved = append(pack.Unresolved, ref)
    continue
}
```
⇒ `FormatForDirector`（`result.go:117-119`）的 `| unresolved refs: N` 从此真的会亮。

**测试 fixture 必须改**：`resolve_test.go:86-92` 的 `Agent: "repo"` 改为 `"repo_agent"`，并新增一条断言 `agent:repo` 与 `agent:repo_agent` 命中同一批便签、`agent:nonexistent` 进 `Unresolved`。

---

## 8. board 作用域（D3）+ GC（D5）

### 8.1 `SetBoardScope`

```go
// director.go，取代 SetTaskID 里的 boardID 赋值（:408-411）
func (a *DirectorAgent) SetBoardScope(sessionID, taskID string) {
    a.taskID = taskID                                   // rollout 命名仍用 taskID
    a.boardID = blackboard.SanitizeBoardID(firstNonEmpty(sessionID, taskID))
}
```
保留 `SetTaskID` 没有必要：已核实它的调用点只有 `app.go:582` 与 `app.go:591`，**无测试依赖** ⇒ 直接替换为 `SetBoardScope`，两处调用点一起改（`director_surface.go:48` 与 `director.go:688` 的注释里提到 `SetTaskID`，同步改文案）。
构造函数里的 adhoc 兜底（`director.go:205-207`）不动。

### 8.2 sessionID 的三个来源

| 入口 | sessionID | 改动点 |
|------|-----------|--------|
| TUI | **per-tab**，tab 创建时生成，跨 `submitTaskWithContent` 稳定 | `tui_model.go` 增 `sessionID string` 字段；`tui_tabs.go:143/242` 的 `tabState` 必须一起存取（切 tab 不能串 board）；`tui_tasks.go:64-77` 建新 task 时**不重置** sessionID；tab 关闭/新建 tab 才重新生成 |
| HTTP | 请求里带的 session 标识；缺省 = taskID | `http/task_manager.go:44` 与 `http/websocket.go:218`：`TaskRequest` 增 `WithSessionID`；`websocket.go` 从 `chatData` 取（字段缺失则空） |
| `--prompt` | 空 ⇒ 回落 taskID | `main.go:371-372` 不改 |

`app.TaskRequest` 增 `sessionID` 字段 + `WithSessionID(string)`；`ProcessCodingTaskWithCallback` / `ProcessConversation`（`app.go:580-595`）把 `SetTaskID(req.taskID)` 换成 `SetBoardScope(req.sessionID, req.taskID)`。

### 8.3 `/resume` 与 rollout

今天 `tui_resume.go:433` 复现的是 **taskID** ⇒ 引入 sessionID 后，resume 需要复现的是 **sessionID**，否则同一会话恢复出来会落到旧 task 的 board（比今天更窄）。

方案：`memory.SessionMeta`（`rollout_types.go:23` 附近，**手写结构，不是生成物**）增 `BoardID string \`json:"board_id,omitempty"\``；`RolloutWriter` 写入时回填（对齐 `rollout_writer.go:153-154` 的 `TaskID` 回填模式）；`tui_resume.go` 优先用 `info.BoardID`。
`NewRolloutWriter` 需要多一个 boardID 入参 ⇒ `director.go:417-435` 的 `createRolloutWriter` 同步改。

**禁止**：不要为此新增事件类型，不要碰 `internal/protocol/agent_events.go`（文件头写着 DO NOT EDIT）。

### 8.4 GC（`internal/blackboard/gc.go`，新增）

```go
type GCOptions struct {
    TTL              time.Duration // <=0 → 不清理
    MaxNotesPerBoard int           // <=0 → 不限
}

// GC 清理 notes 根目录。activeBoardIDs 内的 board 一律跳过。
// 返回删除的便签数与清空的 board 目录数。
func GC(opts GCOptions, activeBoardIDs []string) (notesRemoved, boardsRemoved int, err error)
```

规则（**破坏性操作，必须保守**）：
- TTL 判据是 **board 目录的 mtime**（不是单文件），`time.Since(mtime) > TTL` 才整目录删。
- 每 board 条数上限：按文件名字典序（= 时间序）保留最新 `MaxNotesPerBoard` 个，删更旧的。
- 只删 `*.md` 与 `*.md.tmp`（`board.go:18-19`）；遇到不认识的条目跳过并 `slog.Warn`，**不递归删未知内容**。
- 删空后 `os.Remove` board 目录（`Remove` 而非 `RemoveAll`，非空则失败并忽略）。
- 调用点：`app.go` 的 `Init`（`sync.Once` 内），`activeBoardIDs = [当前 boardID]`。失败只 `slog.Warn`，**绝不阻断启动**。
- 日志：`slog.Info("blackboard: gc", "notes_removed", n, "boards_removed", m, "ttl", ttl)`。

---

## 9. `[blackboard]` 配置表（D5）

```toml
[blackboard]
index_max_notes = 12          # 索引最多列几条
index_line_chars = 160        # 索引单行 rune 预算
summary_max_runes = 1200      # Director 侧 handoff 摘要预算
ttl = "168h"                  # board 目录 mtime 超过则整目录清理；"0" = 不清理
max_notes_per_board = 200     # 每 board 保留的最新便签数
```

- `config.go` 增 `BlackboardConfig` 结构 + `DefaultBlackboardConfig()`，解析模式**照抄 `[browser]`**：整表缺省时按默认值填，判据是"段内是否存在至少一个被新 schema 认识的键"（`browserSectionConfigured` 的同构函数），**不能用某个 bool 的零值判断**（`AGENTS.md` §11 坑 / `tool-registry-design.md` §11.1 记过这个坑）。
- **不设 `enabled` 键**：kill-switch 继续是 `YAGENT_BLACKBOARD=0`（`director.go:472-474`）。加一个 bool 开关会重蹈 `[browser] enabled` 的覆辙。
- 未知键 `slog.Warn` 点名（对齐 `[browser]` 失效键的处理）。
- 同步改：`config/config.toml`（注释模板）、`docs/config-guide.md`、`AGENTS.md` §7 顶层表清单。
- `duration` 解析沿用仓库既有写法（`[browser] workspace_ttl` / `idle_ttl` 怎么解析就怎么解析，先 grep 再写）。

---

## 10. Prompt 改动（7 份）

| 文件 | 改什么 |
|------|--------|
| `director.prompt.md:86` | 改写 Blackboard 段：从"必须传 context_refs"改为"**索引已自动注入**，`context_refs` 用于强调/内联全文；引用格式不变；`agent:repo` 与 `agent:repo_agent` 同义"。补一句：`| unresolved refs: N` 出现时必须换格式重试一次，不得当作已送达 |
| `repo.prompt.md:103` | **删除** "You cannot read from or write to the shared blackboard"（与 golden 工具面直接矛盾）。替换为：你有 `read_note` / `read_artifact` / `list_notes`，任务里出现 Board Index 时**先查再自己扫**，禁止重复前序 Agent 已给出的结论 |
| `coding.prompt.md` | 增 Handoff 收尾契约段（见下）+ "任务里出现 Board Index 时，先按索引取回已有结论，不要重新调查" |
| `browser.prompt.md` | 同上；另注明截图路径属于 `facts[].where` |
| `chat.prompt.md` / `devops.prompt.md` | 同上（DevOps 强调命令输出原文进 `facts[].claim`，不进 conclusion） |
| `meta.prompt.md` | Custom Agent 的工具池由注册表生成（`deps.go:75-96`），Palette 不含 `EffectContext` ⇒ **不会**自动获得 `list_notes`，无需改；但要在 handoff 契约里说明 Custom Agent 也走 `agent_exit` |

**Handoff 收尾契约段**（六份子 Agent prompt 用同一段措辞，便于维护；放在各自 `agent_exit` 说明附近）：

```markdown
## Handoff Contract

Before calling `agent_exit`, fill its optional `handoff` object. It is the ONLY part of
your work guaranteed to reach the next agent verbatim — everything else may be summarized
or dropped by context compression.

- `conclusion`: one sentence answering the delegated task. No preamble, no restating the task.
- `facts`: verifiable claims only, each with `where` (file:line, symbol, URL, or command).
  Never include a guess. An empty list is better than a fabricated one.
- `open_questions`: what the next agent must still resolve.
- `status`: `ok` | `partial` | `error`.

Your full text output is still preserved (note + artifact), so `handoff` is an index into
your work, not a replacement for it.
```

**注意**：`facts` 明确允许为空。把 handoff 变成必填会逼 LLM 编造 fact（§14 禁止事项 9）。

---

## 11. 一致性闸门（D1/D3 的补偿控制，硬约束）

1. **Golden surface**（`agents/registry_gate_test.go` + `testdata/tool_surfaces.golden`）
   `list_notes` 会出现在**六个画像**上（`EffectContext` 已 Grant）。golden 必须**显式改写**，改写前逐行确认：只有 `list_notes` 一行新增，六个 Agent 的其余工具、tier、`ro`/`auth`/`serial` 标记**一律不变**。这个 diff 就是评审物。
2. **注册表自检**：`Registry.Validate()` 的 schemas.json ↔ Spec 双向 1:1 必须继续通过（`list_notes` 两边都要有）。`agent_exit` 只改参数不改 Spec 集合 ⇒ 不应触发失败；若触发，说明对账比的是参数逐字，需要先看清再改。
3. **架构守卫脚本**：
   - `scripts/check_no_manual_lookup.sh`：PENDING 名单**不许加条目**。`list_notes` 的执行器只能在 `registerContextSpecs()` 里出现。
   - `scripts/check_no_agents_import.sh`：`director/board_index.go` 不得 import `internal/agents`。
   - `scripts/check_file_sizes.sh`：新增的 `director/board_index.go` ≤300 行；`planner_run.go` 只加 hook（存量 652 行的违规不许扩大）。
4. **测试基线**（新增/改写）：
   - `blackboard`：`ParseHandoff` 三档、`Normalize` 裁剪、`Render` 预算（逐条丢 Fact 而非截 Conclusion）、v1↔v2 兼容读、`BuildIndex` golden（固定 board 快照 → 期望文本，含空 board / 超 MaxNotes / decode 失败跳过）、别名归一、agent ref 缺失进 Unresolved、GC（TTL 边界 / 每 board 上限 / active board 跳过 / 未知文件不删）。
   - `agents`：`deliverableText` 的四种输入（退出 JSON + 有 assistant / 退出 JSON + 无 assistant / 普通文本 / 空）、`FinalizeResult` 的四档矩阵、`writeDelegateNote` 正文是全文、`delegateTo` 注入索引、`browserDelegate` 写便签、`list_notes` 分页。
   - `compression` 联动（**本方案最关键的回归**）：构造一个带便签的临时 board（`t.Setenv("YAGENT_NOTES_ROOT", …)`）→ 跑 `EmergencyCompressMessages` → 断言重建后的 messages 里**不含**索引块 → 再断言 `withBoardIndex` 重扫后索引**回来了**。这条测试钉住 D4 的"派生态"不变量。
   - `tui`：sessionID 跨 `submitTaskWithContent` 稳定、切 tab 不串、新建 tab 才变；渲染/尺寸矩阵测试（`AGENTS.md` §6.8）全绿。
   - `config`：`[blackboard]` 整表缺省 → 默认值；未知键 → warn。
   - `memory`：`SessionMeta.BoardID` 写入与 resume 复现。
5. **验证命令**（每步都跑，**不启动任何长驻进程**——TUI/http/Chrome 都不行）：
   ```bash
   go build ./... && go vet ./...
   go test ./internal/blackboard/... ./internal/agents/... ./internal/compression/... ./internal/config/... ./internal/memory/... ./internal/tui/... -count=1 -timeout 600s
   bash scripts/check_file_sizes.sh && bash scripts/check_no_agents_import.sh && bash scripts/check_no_manual_lookup.sh
   ```
   `internal/agents`（~39s）与 `internal/compression`（~28s）是最慢的两档，别在快循环里跑全量。
   **不要跑 cargo / codeseek 构建**（Go-only 测试范围）。

---

## 12. 行为变更清单（一次性重构会顺带改变的，逐条签字）

| # | 变更 | 依据 | 风险与处置 |
|---|------|------|-----------|
| 12.1 | **Coding/Browser/DevOps/Chat 的 artifact 全文从退出 JSON 变成真实交付文本** | R1 + §4.1 | **高价值、中风险**。artifact 体积上升（`deliverableText` 取的是最后一条 assistant 全文）。Director 上下文**不变**（Summary 走 handoff，§4.2）。`AgentResult.Text` 语义不动 ⇒ 输出解析/consolidation 不受影响。必须有 §11 的 `deliverableText` 四输入单测 |
| 12.2 | **Director 看到的 Summary 从"正文前缀 500 token"变成"conclusion + facts 表"** | §4.2 | 中。形态变化，`result_test.go:214` / `result_wiring_test.go:53-65` 的断言要跟着改。handoff 缺失时**逐字回落今天的行为**（`Reduce` 前缀），所以最坏情况 = 现状 |
| 12.3 | **便签正文恒全文** | D6 + R2 | 中。notes 磁盘占用上升（今天 139 张；单张从 ~1.5KB 升到交付文本量级）。靠 §8.4 的 TTL + 每 board 上限兜。`read_note` 已有 2000 行/24000 rune 分页 ⇒ 读侧无爆量风险 |
| 12.4 | **六个子 Agent 工具面 +`list_notes`** | §7.1 | 低。`EffectContext` 已 Grant，纯增量。golden 显式改写 |
| 12.5 | **每次委派 + Director 每轮 注入索引块** | D4 + §6 | 中。每轮 token 成本 ≤ `index_max_notes × index_line_chars`（默认 12×160 = 1920 rune，中文最坏约 1900 token）。这是 B 的主要代价，用配置收口。**必须瞬态**（§6.3 不变量），否则会累积 |
| 12.6 | **`agent:repo` / `agent:repo_agent` 同义；ref 解析不到从静默变 `unresolved` 上报** | R4 + §7.4 | 低，且是修复。Director 会开始看到 `| unresolved refs: N`——这是**期望的新信号**，prompt 里要教它怎么反应（§10） |
| 12.7 | **ContextPack 的 artifact 条目从 `(0 chars)` 变成真实 CharCount + Summary 首行** | §7.3 | 低。`ArtifactExistsFn` → `ArtifactInfoFn` 是**破坏性签名变更**，`resolve.go:69-70`、`director.go:532,542-546` 与全部调用点/测试一起改 |
| 12.8 | **boardID 从 taskID 变 sessionID** | D3 + §8 | **中，本方案第二大风险**。同一 tab 内跨任务共享便签 ⇒ 不相关话题的旧结论会出现在索引里。处置：索引行带 `task` 段（≤40 rune）标出话题边界；TTL 默认 168h；`max_notes_per_board` 默认 200；新建 tab 一定换新 board。**HTTP 侧默认 sessionID = taskID**（除非请求显式带），避免把不同用户的请求并到一个 board |
| 12.9 | **Coding→Browser 的二级委派开始写便签、开始解析 ref** | R6 + §4.4 | 中。今天 Browser 的产出只活在 Coding 的上下文里。改后 board 会多出 `browser_agent` 目录。`BoardWriter` 窄接口注入缺失时必须安全降级（nil ⇒ 保持今天的行为，不 panic） |
| 12.10 | **`repo.prompt.md` 删除"cannot read blackboard"** | R6 + §10 | 低。Repo 本来就持有 `read_note`/`read_artifact`（golden 可证），这是修 prompt 与实现的矛盾 |
| 12.11 | **`agent_exit` 增可选 `handoff` 参数** | D2 + §5 | 低。`required` 仍只有 `reason`，旧调用零影响。`ExecuteAgentExit` 透传 ⇒ 退出 JSON 变长（`Text` 仍被 `deliverableText` 识别，判据是 `{"finished"` 前缀 + `finished` 键，不受影响） |
| 12.12 | **`SessionMeta` 增 `board_id`** | §8.3 | 低。`omitempty` ⇒ 旧 rollout 读出来是空串，resume 回落到 `TaskID`（今天的行为） |
| 12.13 | **notes 开始被 GC 删除** | D5 + §8.4 | **破坏性**。默认 TTL 168h + 每 board 200 条。处置：active board 永不删；只删 `*.md`/`*.md.tmp`；`os.Remove` 而非 `RemoveAll`；首次上线可在 `slog.Info` 里打印将要删除的数量便于观察 |

---

## 13. 实施次序（单批次落地，内部按此顺序并逐步验证）

每步的验收都是 `go build ./... && go vet ./...` + 该步涉及的包测试。

| 步 | 内容 | 验收 |
|----|------|------|
| 0 | **前置核实**（写代码前做完，结论写回本文 §15）。已替你核实两条：① `SetTaskID` 的调用点**只有** `app.go:582` 与 `app.go:591`，无测试依赖 ⇒ 可直接改名，不必留别名；② duration 的配置写法照 `config.go:738`（`WorkspaceTTL time.Duration \`toml:"workspace_ttl"\``）+ `:771-772` 的默认值回填 + `:798` 的默认表。仍需你核实：③ `SessionMeta` 的读写三端（`rollout_writer.go` / `rollout_reader.go` / `rollout_sessions.go`）各自怎么取 `TaskID`；④ `result_test.go` / `result_wiring_test.go` 里会被 §4.2 打破的断言清单（已知两处：`result_test.go:214`、`result_wiring_test.go:53-65`）；⑤ 三道闸门测试的确切名字已确认为 `TestRegistryMatchesSchemas`(`registry_gate_test.go:24`) / `TestAgentSurfacesGolden`(`:48`，`-update` 见 `:46`) / `TestDerivedMetadataMatchesLegacyTables`(`:110`) | 一份"实际调用点清单"，与本文假设不符处**先停下来报告**，不要自行改设计 |
| 1 | `blackboard/handoff.go`：`Handoff` / `Fact` / 常量 / `ParseHandoff` / `Normalize` / `Render` + 单测（三档回落、裁剪、预算逐条丢 Fact） | `go test ./internal/blackboard/... -count=1` |
| 2 | `blackboard/note.go`：`noteSchemaVersion = 2`、`Note.Handoff`、`NoteInput.Handoff`、`toMeta` 增 `Conclusion`/`Degraded`、`decodeNote` 的 v1 兼容、删除 `WriteNote` 的 `body == "" → Summary` 兜底 + 兼容读单测 | 同上；用一张真实 v1 便签（`~/.yagent/notes/0421af6d-…/repo_agent/…4a24.md`，**复制进 testdata，不要读用户目录**）做 fixture |
| 3 | `blackboard/index.go`：`IndexOptions` / `BuildIndex` + golden 单测（文件名字典序倒排、只 decode 前 N 个、decode 失败跳过、空 board 返回 ""） | 同上 |
| 4 | `blackboard/resolve.go`：`PackEntry` 增字段、`ArtifactExistsFn` → `ArtifactInfoFn`、`loadNoteEntry` 的 handoff 渲染 + v1 头尾摘录、`agentNameCandidates` 别名、`case "agent"` 补 Unresolved、`Text()` 改写；`resolve_test.go` fixture 改 `repo_agent` + 新增别名/Unresolved 断言 | 同上 |
| 5 | `blackboard/gc.go`：`GCOptions` / `GC` + 单测（TTL 边界、每 board 上限、active 跳过、未知文件不删、`os.Remove` 非空失败忽略） | 同上 |
| 6 | `config`：`BlackboardConfig` + `DefaultBlackboardConfig()` + `[browser]` 同风格的整表缺省判定 + 未知键 warn；`config/config.toml` 模板与 `docs/config-guide.md` | `go test ./internal/config/... -count=1` |
| 7 | `agents/result.go`：`deliverableText` + `AgentResult.Handoff` + `FinalizeResult`/`FinalizeResultFull` 增参与四档矩阵；改 `result_test.go` / `result_wiring_test.go` 的既有断言 | `go test ./internal/agents/... -run "Result" -count=1` |
| 8 | `tools/flow_control.go` + `toolspec/schemas.json`：`agent_exit` 的 `handoff` 透传与 schema | `go build ./...`；`TestRegistryMatchesSchemas` 通过 |
| 9 | `agents/executor.go`：`ExecutorResult.Handoff` + `execOneToolCall` 采集点 + `exitReason`/`lastAssistantText`；六个子 Agent 的 `Run()` 透传 handoff 进 `FinalizeResult` | `go test ./internal/agents/... -count=1 -timeout 300s` |
| 10 | `agents/director.go` + `director_surface.go`：`SetBoardScope`、`writeDelegateNote` 用全文 + handoff、`artifactExists` → `artifactInfo`、`delegateTo` 注入索引、`indexOpts()` 读配置 | 同上 |
| 11 | `agents/list_notes_tool.go` + `agents/specs.go` 的 `registerContextSpecs()` 加一行 + `read_note_tool.go` 的 handoff 渲染 + 单测 | `go test ./internal/agents/... -run "Note" -count=1` |
| 12 | **golden 显式改写**：`go test ./internal/agents -run TestAgentSurfacesGolden -update`，然后**逐行看 diff**，确认只有 `list_notes` 六行新增（§11.1） | diff 里出现任何其它变化 ⇒ 停下排查，不许直接提交 |
| 13 | `agents/director/board_index.go`（≤300 行）+ `planner_run.go` 的 1-3 行 hook + 幂等/瞬态单测 | `bash scripts/check_file_sizes.sh && bash scripts/check_no_agents_import.sh`；`go test ./internal/agents/director/... -count=1` |
| 14 | **压缩联动回归**（§11.4 那条）：`internal/compression` 或 `internal/agents` 里加"紧急压缩后重扫仍有索引"的测试 | 该测试必须先红后绿（先写测试确认它抓得住回归） |
| 15 | `agents/coding.go` 的 `browserDelegate` + `agents/ports.go` 的 `BoardWriter` + `app.go` 注入 | `go test ./internal/agents/... -count=1 -timeout 300s` |
| 16 | sessionID 管道：`app.TaskRequest.WithSessionID` → `SetBoardScope`；TUI per-tab sessionID（含 `tui_tabs.go` 的 tabState 存取）；HTTP；`SessionMeta.BoardID` + resume 优先用它 | `go test ./internal/tui/... ./internal/memory/... ./internal/http/... -count=1`；TUI 渲染/尺寸矩阵全绿 |
| 17 | GC 接入 `app.go` 的 `Init`（`sync.Once` 内，active board 跳过，失败只 warn） | `go test ./internal/app/... ./internal/blackboard/... -count=1` |
| 18 | Prompt 七份（§10）+ Handoff 契约段 | 人工通读；`TestMetaPromptToolPoolMatchesPalette` 之类的 prompt 一致性测试全绿 |
| 19 | 文档：`AGENTS.md` §5.2（工具表 +`list_notes`）、§6.4（Blackboard 段落整体重写：Handoff/索引/session 作用域/GC）、§7（`[blackboard]` 表）、§11（新增坑：索引必须瞬态、`deliverableText` 的判据）；`docs/ARCHITECTURE.md` 的 Blackboard 段；本文 §15 落地状态 | 人工通读 |
| 20 | 全量闸门：`go build ./... && go vet ./...` + 全仓测试 + 三个守卫脚本 | `go test ./... -count=1 -timeout 600s` 全绿（约 80s） |

**回滚**：整个批次在一个分支上。三处可独立回退的开关——
① `YAGENT_BLACKBOARD=0` 关掉便签与索引注入（既有 kill-switch，`director.go:472-474`）；
② `YAGENT_ARTIFACT_DISABLE=1` 关掉 artifact 落盘（`artifact/summary.go:89-91`）；
③ `[blackboard] index_max_notes = 0` 关掉索引注入（**必须实现这个语义**：0 = 不注入，作为应急阀门）。

---

## 14. 禁止事项

1. **不要把索引写进 `ConversationMemory` 或 rollout**。它是派生态，每轮重扫（§6.3）。写进去会让 `/resume` 累积历史索引，且被压缩层当成普通历史处理——正好复现 R5。
2. **不要让 `ParseHandoff` 返回 error 或阻断委派**。三档回落必须穷尽，最坏情况等于今天的行为。
3. **不要在 `internal/blackboard` 里 import `agents` / `artifact` / `tools`**。跨包信息一律走回调（今天的 `ArtifactExistsFn` 就是这个模式，升级成 `ArtifactInfoFn` 也不要破坏它）。
4. **不要在 blackboard 里硬编码 Agent 名单**。别名规则只能是"加/去 `_agent` 后缀"，名单的唯一来源是 `delegateTargets()`。
5. **不要手改 `internal/protocol/agent_events.go`**（本方案不新增事件类型）。
6. **不要新建任何按工具名索引的表**。`list_notes` 的声明只能进 `registerContextSpecs()`。
7. **不要为了通过 golden 测试直接改写 golden 文件**——先确认变化是 §12 里签过字的。
8. **不要在 GC 里删 active board**，不要用 `RemoveAll`，不要删 `*.md`/`*.md.tmp` 以外的东西。
9. **不要把 `handoff` 变成必填**，也不要在 prompt 里暗示"facts 越多越好"。空 facts 优于编造的 facts——`Where` 字段存在的意义就是让编造变得显眼。
10. **不要改 `ExecutorResult.Text` / `AgentResult.Text` 的语义**（`result.go:12-19` 是契约）。`deliverableText` 只在便签正文与 artifact 全文两处消费。
11. **不要在 `planner_run.go` 里写索引逻辑本体**（存量已超行数上限），只加 hook。
12. **不要 `go mod tidy`**，不要跑 cargo/codeseek 构建。
13. **不要启动长驻进程验证**（TUI/http/Chrome 一律不行），用 `go build` + 测试。

---

## 15. 未决项与落地状态

### 未决项（实施中遇到请先报告，不要自行决定）

| # | 问题 | 建议 |
|---|------|------|
| U1 | Repo **没有** `StopOnFinish`（§1 对照组），所以它今天不塌缩。是否统一？ | **本次不动**。统一后 Repo 也会塌缩成退出 JSON，必须同时接 handoff——属独立变更，混进来会让 §12.1 的验证面翻倍 |
| U2 | 索引是否按 agent 分组渲染（`[repo_agent]` / `[coding_agent]` 小节）？ | 先平铺（newest-first），跑 3-5 个真实任务看 LLM 是否混淆归属，再决定 |
| U3 | `facts[].where` 是否校验 file:line 真实存在？ | **不校验**。校验会把 FS 依赖拉进 blackboard（违反禁止事项 3），且失败处置无解。靠 prompt 的"Never include a guess" + `Where` 字段的显眼性约束 |
| U4 | Custom Agent（Meta 产物）是否发 `list_notes`？ | **不发**。`ProfileDirector.Palette`（`profiles.go:32-33`）不含 `EffectContext`，今天也没有 `read_note`；扩它属于行为变更，需单独签字 |
| U5 | Director 自己的结论要不要也写便签？ | **本次不写**。Director 的结论活在它的上下文 + thinklink 里，而索引已经让子 Agent 结论可达。若实测发现 Director 的分析仍然丢失，再单开一条 |
| U6 | `index_max_notes = 12` / `index_line_chars = 160` / `summary_max_runes = 1200` 的默认值 | 初值。需跑真实任务统计索引 token 占比后调整（对齐 `tool-registry-design.md` §12 未决项 3 的做法） |

### 落地状态（2026-10-14）

**已全部落地**：§13 的步 0-20。`go build ./... && go vet ./...` 干净，`go test ./... -count=1` 23 个包全绿，
三个守卫脚本（`check_file_sizes` / `check_no_agents_import` / `check_no_manual_lookup`）通过。
golden diff 与 §11.1 的预判完全一致：六个 Agent 各 +1 行 `list_notes tier=warm ro` + 六行工具计数，**没有其它变化**。

#### 相对本方案的偏离（都写进了代码注释，后续维护按这里说的做）

| 方案里写的 | 实际做的 | 原因 |
|---|---|---|
| §5.3 executor 在 stop 分支采集 handoff，`ExecutorResult` 增 `Handoff` | `ExecuteAgentExit` 把 `handoff` 回显进返回值，采集点在 `agents/result.go` 的 `collectHandoff()`；`ExecutorResult` **没有**新字段 | 返回值 JSON 与工具参数同构，解析器一处两用；executor 少一个字段、少一处生产写入点，且 `ExecutorResult` 的构造点在测试里有 10 个 |
| §4.2 `FinalizeResult(agentName, task, exec, h)` 加第四入参 | 签名不动，handoff 从 `exec` 派生 | 9 个生产调用点 + 33 处测试引用零改动；六个子 Agent 的 `Run()` 因此不需要透传（步骤 9 的"透传"变成零工作） |
| §4.2 表格里 `Text` 列恒为 `exec.Text` | `Text` 保持现状（超预算时仍是摘要），交付全文另存 `AgentResult.Deliverable` | `Text` 的既有语义被 TUI/consolidation/`FinalizeResultFull` 依赖，动它是无关的连带风险；便签正文需要全文时读 `Deliverable` |
| §6.1 `IndexOptions.MaxNotes <=0 → 默认` | `==0` 是**关掉索引**（§13 回滚阀门），`<0` 才取默认 | 方案自身在 §6.1 与 §13 之间矛盾；阀门语义优先，因为它是有意的应急出口 |
| §6.1 配置 `index_line_chars` 无特殊 0 语义 | 显式 `0` = **不裁剪行**，经 `blackboard.IndexOptionsFromConfig()` 换算成 `MaxRenderRunes` | `blackboard` 把 `<=0` 读成"取默认"，而配置层的 0 是"不裁剪" —— 换算必须有一处，放在 owning 该哨兵的包里，配置层与特性层不必互相认识。索引行仍有 `maxTaskRunes=40` / `IndexConclusionRunes=80` 的分段上限，所以"不裁剪整行"不会退化成无限长 |
| §6.3 索引插在"最后一条 user 消息之前" | **追加在末尾** | 多步循环里那条 user 消息通常在列表头部，插到它前面等于把目录塞到几百步历史之前；末尾永远是安全插入点（assistant 的 tool_calls 与其响应总是成段出现） |
| §6.3 `withBoardIndex` 改 `messages` | 函数叫 `messagesForLLMCall`，返回新切片，**外层 messages 不回写** | 回写会让下一步的压缩把目录算进预算、紧急压缩还会把它当用户输入折叠掉 —— 正是它要治的病。两条测试钉住（§11.4 的压缩联动） |
| §7.3 `PackEntry` 只增 `ArtifactIDs`/`ArtifactSummary` | 还直接带了 `Handoff` | 截断分支要渲染的就是 handoff，不带它就得再读一次便签 |
| §8.3 `NewRolloutWriter` 多一个 boardID 入参 | `SetBoardID()` + `WriteSessionMeta` 兜底回填（对齐 `TaskID` 的既有模式） | 同一个原因：构造点有 10 个测试调用点 |
| §8.4 "不递归删未知内容，用 `os.Remove` 回收空目录" | **过期整板 `os.RemoveAll`**；条数淘汰只删 `*.md` 且不回收目录；notes 根里的 symlink 用 `os.Lstat` 显式拒绝 | 原方案下 agent 目录里长出的嵌套目录永不出清（每条 `.md` 都删了，目录还在，`os.Remove` 又非空失败）。递归安全靠三点：只处理根目录一层、symlink 绝不跟、active board 永不动 |
| §9 `max_notes_per_board` 显式 `0` = 不限条数（带 presence 标记） | 0 与负数都归默认 200，**去掉**该键的 presence 标记 | 少一个标记、少一种特殊语义；"不限"写个大数字即可。最终只有 `index_max_notes` / `index_line_chars` / `ttl` 三个键需要键存在性判定 |
| §12.8 HTTP 每请求新 uuid ⇒ 断链，需要 `WithSessionID` | **HTTP 不改**：`start_task` 带 `task_id` 时 `task.ID` 被复用 ⇒ board 本来就连续；不带就是新会话 | 落地时核实的结论，与 §1 R6 的初判不同。`/resume` 也不是断点（`tui_resume.go` 早就是 taskID 优先，后来才加 board_id 优先） |
| §12.11 之外 | `blackboard.Enabled()` 成为 `YAGENT_BLACKBOARD` 的**唯一**判据，`agents.blackboardEnabled` 变成它的一行代理 | 写便签、读便签、注入索引、启动清理四处共用同一个开关，规则不该有四份 |

#### 闸门抓出来的问题（都已修，证明它们有用）

1. `TestDerivedMetadataMatchesLegacyTables` 直接拒绝新的可并发只读工具 `list_notes` —— 按 §11.4 的规矩加进
   `newTools` 并写明"纯本地文件读、无共享写端"的理由后才放行。
2. `resolve_test.go` 的 fixture 用 `Agent:"repo"`（生产写的是 `repo_agent`），这正是 §1 R4 的盲区来源；
   改成真实目录名 + 新增"别名命中同一批便签"与"未知 Agent 必须进 Unresolved"两条断言。
3. `blackboard.truncateRunes` 历史上返回 `max+1` 个 rune（省略号额外算），而 handoff 的预算契约要求"输出 ≤ max"。
   改成省略号计入预算；`tailRunes` 的前置省略号同样会挤掉真正要保住的尾部，也计入。受益面是便签的
   `Task`(300) / `Summary`(200) 与索引的分段裁剪 —— 这些位置的"≤ N"承诺此前系统性地超一个 rune。
4. `[blackboard]` 刻意**没有** `enabled` 键（§9 的决定），落地过程中也没有出现过草稿残留：
   kill-switch 只有 `YAGENT_BLACKBOARD=0` 一条路，判据收在 `blackboard.Enabled()`。
   写这张表时容易顺手加一个 bool，而它的零值会把新能力静默关掉 —— 那是 `[browser] enabled` 踩过的坑。
5. `gc.go` 的 `listBoardNotes` 排序键最初用整条路径，导致"最旧"按 agent 目录分组而非按时间；
   改成按文件名排序，并补了跨 agent 目录的条数淘汰测试。

#### 未做（签字范围之外，已确认留给后续）

- U1 Repo 仍不设 `StopOnFinish`（因此它今天的交付物恰好不塌缩；统一它需要 handoff 同时到位，属独立变更）。
- U4 Custom Agent 的 Palette 仍不含 `EffectContext`，所以它们**不能**主动读便签。
  但 `runCustomAgent` 现在会为它们的产出写一张便签（§1 R6 里同类黑洞），
  结论因此仍会出现在下一次委派与 Director 每轮的 Board Index 里 —— 不需要它们自己知道 `read_note` 的存在。
- U5 Director 自己的分析仍不落便签（只有子 Agent 的产物落）。
- U6 三个默认预算值是初值，需要跑真实任务看索引 token 占比再调。

