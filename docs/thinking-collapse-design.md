# 正文思考折叠（方案 A）设计稿

> 2026-11-05 拍板：四份视觉稿（A 折叠块 / B 默认全隐藏 / C step 卡片 / D 原地修）中选 **A**。
> 范围：主 transcript 里两类"思考"的默认形态与展开形态。**不改** ctrl+l 时间线、ctrl+g thinklink
> 全屏、`<thought>` 的解析与落库（thinklink store）。

## 1. 现状判据（为什么乱）

> 本节行号是**改前**坐标（§8 记的是落地后的行号）；`formatLogEntry` 的 `"thinking"` 分支、
> `renderThinkingContinuation` 与 `bodyEmitState.lastThinking` 在本次实施中已整体删除。

- `thinking` 事件 = 每步 LLM 响应的 reasoning 全文，一步一条：
  `internal/agents/executor.go:587`、`internal/agents/director/planner_llm.go:137`。
- TUI 落条目 `internal/tui/tui_update_tasks.go:660-728`：**只有最后一条**是同 Agent 的 thinking
  才合并（`:680`），中间插进任何条目（工具调用、空 ai_stream 占位）就断成新段。
- 渲染 `formatLogEntry` 的 `"thinking"` 分支（改前 `tui_render.go:1028-1039`）：换行**拍平**、
  截到 `maxWidth-8` rune → 一段思考在屏幕上 = 一行被腰斩的斜体；合并过的段只露第一句，其余永久不可见。
- 续行是第二套算法：`bodyBlockFor`（改前 `tui_body.go:196-201`）+
  `renderThinkingContinuation`（改前 `tui_render.go:1082`），导轨 `"      │ "`、预算 `maxWidth-10`，
  与段首的 `💭 ` + `"  │ "` / `maxWidth-8` 不同源 → 段首行比续行短 2 列。
- `<thought>` 块：`renderAssistantContent`（`tui_render.go:494`）与 `renderStreamEntry`（`:454`）
  每步画一整块 9–10 行圆角面板，无折叠、无去重；Director/Coding/Browser 每步都发 → 40 步 = 40 块。
- 全文的唯一读路：ctrl+l（`findFullThinkingContent`，`tui_timeline_fullscreen.go:885`）与 ctrl+g。
- `/resume` 不重建 thinking 条目（`tui_resume.go` 对 "thinking" 零命中）→ 续跑会话没有斜体墙。
  本设计**不改变**这条事实，只保证 ai_response 里的 thought 块恢复后与实时同形态。
- 附带形状债：`RenderAgentSeparator` 拿的是 viewport 外宽（`tui_body.go:188`），而 viewport 带
  `Padding(0,1)`（`tui_model.go:643`）→ 分隔行恒比内宽多 2 列，把整块顶宽。

## 2. 视觉契约

**折叠态（默认）**

- thinking 条目 = 恰好 1 行：`💭 ×N │ <首段首行 preview>…`
  - `N` = 段数（content 按 `\n` 切、去空段）；`💭` 141，`×N` dim 241，`│` 238，preview 沿用
    `thinkTextStyle`（251 + italic + faint）。
  - preview 取首个非空行（复用 `firstThinklinkLine` 的去标题/去标记规则），截断补 `…`，
    整行 ≤ 视口内宽。
- thought 块 = 恰好 1 行：`◆ THOUGHT │ <goal>… │ ▸ <当前 plan 项> (done/total)`
  - 无结构化内容（`ParseThoughtPlan` 空）退化成 `◆ THOUGHT │ <首行>…`。
  - 窄宽度让步序：先丢 plan 段，再截 goal（与 `thinklinkPanelTop` 的 meta 让步同规矩）。

**展开态（`alt+h` 全局切换）**

- thinking 条目 = 逐段编号 + 折行：首行 `  1 │ `、续行 `    │ `（编号列 4 + 导轨 2 = 6 列对齐），
  折行走 `wrapWords`（`tui_thinklink_fullscreen.go:251`），预算 = 内宽 − 6；**不截断**。
- thought 块 = 现有圆角面板，`renderThinklinkPanel` 逐字不动。

**不做逐块展开**：正文没有行光标（j/k 只滚不选），逐块展开要先立"行光标"这个新交互，见 §6。

## 3. 状态与键位

- 全局一个旗标 `model.thinkDetailOpen`（默认 false）。
- NORMAL 模式 `alt+h` 切换（与 `alt+m` / `alt+r` 同层，`tui_update_keys.go`；`alt+t` 已被 token
  面板折叠占用，`alt+d/alt+s/alt+w/alt+c` 也各有其主，所以取 `alt+h` = tHoughts）。切换动作：
  所有条目 `clearRenderCache` + 流式面板 memo（`streamPanels/streamPanelSrc`）清零 +
  `needFullRebuild = true` + `rebuildViewportScrollLock()`（贴底的继续贴底）。
- 提示条 `CmdlineHintIdle`（`i18n.go:147` 中 / `:238` 英）尾部追加 `alt+h think` / `alt+h 思考`；
  窄终端由 `common.HintBar` 自尾丢弃，不新增预算逻辑。

## 4. 渲染落点（函数级）

- 新增 `(m *model) renderThinkingBlock(entry *logEntry, width int) string`，取代
  `formatLogEntry` 的 `"thinking"` 分支与 `renderThinkingContinuation`（两者删除）；
  `renderSingleEntry` 的 thinking 分支改调它。折叠/展开两形态是它的两个私有辅助。
- `renderStreamEntry` 改方法（要读 `m.thinkDetailOpen`）；thought 块的单行/面板两形态由
  新增的 `(m *model) renderThinklinkBlock(block string, panelW int) string` 二选一，
  `renderThinklinkPanels` 改走它。流式 memo 的失效在切换时统一清零（§3），
  `sameThoughtBlockPrefix` 不动。
- `bodyBlockFor` 删掉 `state.lastThinking` 续行分支（`bodyEmitState.lastThinking` 字段一并删）；
  分隔行改按内宽：`RenderAgentSeparator(entry.from, m.viewport.Width()-2)`。
- 宽度账统一：正文一切排版按**视口内宽** = `viewport.Width() - 2`（Padding(0,1)）。

## 5. 与既有契约的结算

- **bodyIndex**：折叠/展开只改变 `block.text` 的行数；行数统计与取词仍同源（`bodyBlockFor`），
  切换走 `needFullRebuild` + `bodyRepaint(0)`，索引结构不动（硬约束 3.2 ①③④ 全保留）。
- **流式合并**：thinking 条目不是 `ai_stream`，切换产生的脏不属于 `streamDirtyHeld` 的
  "可推迟"集合，立刻落地 —— 判据无需改。
- **流式 parity**：`TestTUI_StreamPanelsMatchOneShot` 的"增量 == 一次性"在两种形态下都要成立
  （用例对 `thinkDetailOpen` 两值各跑一遍）。
- **merge 用例**：`tui_thinking_merge_test.go` 六条按新契约改写 —— 💭 次数 == thinking 条目数
  （折叠与展开都成立）、折叠态每条目恰一行、空白 ai_stream 占位不产生行。
- **恢复链路**：`TestApplyRestoredMemory_RenderParityWithLivePath` 继续钉"恢复 == 实时"；
  恢复出的 ai_response 含 thought 块时按折叠态渲染。

## 6. 不做（P2 及以后）

- 逐块展开 / 行光标（j/k 语义变更，单独立项）。
- 替模型总结或去重 thinking 内容。
- 时间线、thinklink 全屏、`<thought>` 解析的任何改动。
- `/resume` 重建 thinking 条目。

## 7. 闸门清单

新增（`tui_think_collapse_test.go`）：

- `TestThinkingCollapse_OneLinePerEntry`：折叠态每个 thinking 条目恰一行、行宽 ≤ 内宽，
  宽度矩阵 {30,60,100,240}。
- `TestThinkingCollapse_ExpandedShowsFullText`：展开态全文在、无 `…` 截断、折行 ≤ 内宽、段号连续。
- `TestThinkToggle_RoundTripKeepsScroll`：切换前后 `bodyTotalLines` 对称回到原值、贴底保持。
- `TestThoughtOneLine_ShapeAndYield`：单行含 goal 与 `(done/total)`；窄宽度先丢 plan 段；
  展开态与 `renderThinklinkPanel` 逐字相同。
- `TestAgentSeparatorFitsInnerWidth`：分隔行显示宽 == 视口内宽。
- `TestStreamPanelsMatchOneShot_BothThinkModes`：parity 两形态各跑一遍。

改写：`tui_thinking_merge_test.go` 六条 → 新契约（见 §5）。

## 8. 落地坐标（改后行号，逐条对过磁盘）

| 落点 | 位置 |
|---|---|
| 折叠/展开渲染 | `tui_render.go:639` `renderThinkingBlock`（+ `renderThinkingCollapsed` / `renderThinkingExpanded` / `thinkingParagraphs`） |
| 切换动作 | `tui_render.go:702` `toggleThinkDetail`；旗标 `tui_model.go:488` `thinkDetailOpen` |
| thought 两形态 | `tui_render.go:534` `renderThinklinkBlocks` / `:542` `renderThinklinkBlock` / `:551` `renderThinklinkPanelOneLine`；面板本体 `:608` |
| 排版块 | `tui_body.go:167` `bodyBlockFor`（分隔行内宽 `:188`；`bodyEmitState.lastThinking` 已删） |
| 键位 | `tui_update_keys.go:618` `alt+h`；提示条 `i18n.go:147`（中）/ `:238`（英） |
| 折行度量 | `tui_thinklink_fullscreen.go:251` `wrapWords`（显示列口径）+ `cutCells` |

## 9. 实施记录（2026-11-05）

落地范围 = P1 全部；`bash scripts/check_all.sh` 全绿（编译 / vet / 十一把守卫 / 26 个测试包）。

**与本文档的偏差（都是实施中发现文档判据写错了，按事实改）**：

1. **切换键 `alt+t` → `alt+h`**：`alt+t` 早被 token 面板折叠占用（`tui_update_keys.go:601`），
   `alt+d/s/w/c` 也各有其主。`alt+h` 落在 `handleNormalGlobalKey` 的 `alt+m`/`alt+r` 同层
   （`tui_update_keys.go:618`）。
2. **`wrapWords` 的度量口径从 rune 改成显示列**（`tui_thinklink_fullscreen.go:248`）：
   文档 §2 写"折行走 wrapWords"时没验它的度量，实测它按 `len([]rune)` 折行 ——
   中文一列两格，展开态的中文思考行会溢出约两倍，把 thought 面板的圆角边框撕开。
   现在按 `uniseg.StringWidth` 折，超长单词的硬切点落在 grapheme 簇边界（`cutCells`）。
   这条同时修好了全屏 thinklink 详情面板里中文值的同一个溢出。
3. **`TestTUI_StreamPanelsMatchOneShot` 现在对折叠/展开两形态各跑一遍**（文档 §5 只说"用例跑两遍"，
   没点名它）；parity 辅助改名 `verifyStreamPanelsMatchOneShot` 才过 `check_test_hygiene`
   —— 它按名字认断言，`test*` 前缀不算。
4. **事件路径的测试必须先 `rebuildContentCache`**：thinking 合并只标脏、正文由 `View()` 落地，
   直接读 `bodyText` 会拿到合并前那一帧的旧 `contentParts`（新加的 `thinkingTestBody` 辅助钉住这个顺序）。

**实测数字**（120 列视口，10 步 × 两段思考 + 一次工具调用，末尾一条带 thought 块的回复）：
折叠态 24 行，展开态 63 行 —— 同一份内容，默认屏占比降到 38%。

**闸门落地情况**：§7 列的六条全部存在（`tui_think_collapse_test.go` 四条 +
`tui_render_test.go` 的折叠/展开两条），`tui_thinking_merge_test.go` 七条按新契约重写
（旧契约的"💭 每段一次 + 缩进续行"已随 `renderThinkingContinuation` 一起删除）。

**仍未做**：§6 的 P2 逐块展开（要先行光标）；`/resume` 不重建 thinking 条目这条事实没变。
