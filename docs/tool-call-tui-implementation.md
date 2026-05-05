# Tool Call & Tool Result TUI 渲染实现文档

> 本文档详细描述 Crush TUI 中 tool call 和 tool result 的完整渲染系统，可用于复刻该组件。

---

## 1. 整体架构

### 1.1 核心文件

```
internal/
├── message/
│   ├── message.go              # ToolCall / ToolResult 数据结构定义
│   └── content.go              # ContentPart 接口 + 工具方法
├── ui/
│   ├── chat/
│   │   ├── messages.go         # MessageItem 接口、ExtractMessageItems、BuildToolResultMap
│   │   ├── tools.go            # baseToolMessageItem、NewToolMessageItem 工厂、通用渲染函数
│   │   ├── file.go             # View/Write/Edit/MultiEdit/Download 工具
│   │   ├── search.go           # Glob/Grep/LS/Sourcegraph 工具
│   │   ├── fetch.go            # Fetch/WebFetch/WebSearch 工具
│   │   ├── bash.go             # Bash/JobOutput/JobKill 工具
│   │   ├── agent.go            # Agent/AgenticFetch 工具（支持嵌套）
│   │   ├── todos.go            # Todos 工具
│   │   ├── mcp.go              # MCP 工具
│   │   ├── docker_mcp.go       # Docker MCP 工具
│   │   ├── diagnostics.go      # Diagnostics 工具
│   │   ├── lsp_restart.go      # LSP Restart 工具
│   │   ├── references.go       # Find References 工具
│   │   ├── unified_diff.go     # Unified Diff 解析与渲染
│   │   ├── tool_result_content.go # 工具结果内容智能判断
│   │   ├── generic.go          # 未知工具默认渲染
│   │   ├── assistant.go        # 助手消息渲染（含 thinking）
│   │   └── user.go             # 用户消息渲染
│   ├── anim/
│   │   └── anim.go             # 渐变旋转动画引擎
│   ├── common/
│   │   ├── diff.go             # DiffFormatter 工厂
│   │   ├── highlight.go        # SyntaxHighlight 语法高亮
│   │   ├── markdown.go         # MarkdownRenderer / QuietMarkdownRenderer
│   │   └── elements.go         # PrettyPath、Section 等通用元素
│   └── styles/
│       ├── styles.go           # Styles 结构体定义（Tool 相关字段）
│       └── quickstyle.go       # 默认主题初始化（工具样式赋值）
```

### 1.2 调用链路

```
AI 工具调用
  → message.Message (assistant) 中携带 ToolCall[]
  → AI 工具执行后返回 message.Message (tool) 中携带 ToolResult[]
  → BuildToolResultMap()      → 将 ToolResult 按 tool_call_id 映射为 map
  → ExtractMessageItems()     → 将 assistant message 拆分为 MessageItem 列表
  → list.List.Render()        → 遍历 MessageItem，调用 Render() 渲染
  → baseToolMessageItem.Render()
        → toolRenderer.RenderTool()  → 具体工具的渲染逻辑
```

### 1.3 消息提取流程

```go
// Step 1: 构建 ToolResult 映射
toolResults := BuildToolResultMap(allMessages)

// Step 2: 对每个 assistant message 提取 MessageItem
for _, msg := range assistantMessages {
    items := ExtractMessageItems(sty, msg, toolResults)
    // items = [AssistantMessageItem, BashToolMessageItem, EditToolMessageItem, ...]
}
```

`ExtractMessageItems` 逻辑：

```go
func ExtractMessageItems(sty *styles.Styles, msg *message.Message, toolResults map[string]message.ToolResult) []MessageItem {
    switch msg.Role {
    case message.Assistant:
        var items []MessageItem
        // 1. 助手文本（如果非空）
        if ShouldRenderAssistantMessage(msg) {
            items = append(items, NewAssistantMessageItem(sty, msg))
        }
        // 2. 每个 ToolCall 对应一个 ToolMessageItem
        for _, tc := range msg.ToolCalls() {
            if tr, ok := toolResults[tc.ID]; ok {
                result := &tr
                items = append(items, NewToolMessageItem(sty, msg.ID, tc, result, msg.FinishReason() == message.FinishReasonCanceled))
            }
        }
        return items
    }
}

func BuildToolResultMap(messages []*message.Message) map[string]message.ToolResult {
    resultMap := make(map[string]message.ToolResult)
    for _, msg := range messages {
        if msg.Role == message.Tool {
            for _, result := range msg.ToolResults() {
                resultMap[result.ToolCallID] = result
            }
        }
    }
    return resultMap
}
```

---

## 2. 数据模型

### 2.1 ToolCall (`message.ToolCall`)

```go
type ToolCall struct {
    ID               string `json:"id"`               // 唯一标识符，关联 ToolResult
    Name             string `json:"name"`             // 工具名，如 "bash", "edit", "view"
    Input            string `json:"input"`            // JSON 字符串形式的参数
    ProviderExecuted bool   `json:"provider_executed"`
    Finished         bool   `json:"finished"`         // 工具调用是否已完成
}
```

### 2.2 ToolResult (`message.ToolResult`)

```go
type ToolResult struct {
    ToolCallID string `json:"tool_call_id"`    // 关联的 ToolCall.ID
    Name       string `json:"name"`            // 工具名（冗余）
    Content    string `json:"content"`         // 文本结果
    Data       string `json:"data"`            // 二进制数据（base64 编码的图片等）
    MIMEType   string `json:"mime_type"`       // 如 "image/png", "text/plain"
    Metadata   string `json:"metadata"`        // JSON 元数据（如 diff 信息、任务列表）
    IsError    bool   `json:"is_error"`        // 是否错误
}
```

### 2.3 ToolStatus 枚举

```go
type ToolStatus int

const (
    ToolStatusAwaitingPermission ToolStatus = iota  // 等待权限审批
    ToolStatusRunning                               // 正在执行
    ToolStatusSuccess                               // 执行成功
    ToolStatusError                                 // 执行出错
    ToolStatusCanceled                              // 已取消
)
```

---

## 3. ToolMessageItem 工厂

`NewToolMessageItem` 根据工具名创建对应的渲染器，工厂模式：

```go
func NewToolMessageItem(sty, messageID, toolCall, result, canceled) ToolMessageItem {
    switch toolCall.Name {
    case "bash":          return NewBashToolMessageItem(...)
    case "job_output":    return NewJobOutputToolMessageItem(...)
    case "job_kill":      return NewJobKillToolMessageItem(...)
    case "view":          return NewViewToolMessageItem(...)
    case "write":         return NewWriteToolMessageItem(...)
    case "edit":          return NewEditToolMessageItem(...)         // full width
    case "multi_edit":    return NewMultiEditToolMessageItem(...)     // full width
    case "glob":          return NewGlobToolMessageItem(...)
    case "grep":          return NewGrepToolMessageItem(...)
    case "ls":            return NewLSToolMessageItem(...)
    case "download":      return NewDownloadToolMessageItem(...)
    case "fetch":         return NewFetchToolMessageItem(...)
    case "sourcegraph":   return NewSourcegraphToolMessageItem(...)
    case "diagnostics":   return NewDiagnosticsToolMessageItem(...)
    case "agent":         return NewAgentToolMessageItem(...)         // 嵌套
    case "agentic_fetch": return NewAgenticFetchToolMessageItem(...)  // 嵌套
    case "web_fetch":     return NewWebFetchToolMessageItem(...)
    case "web_search":    return NewWebSearchToolMessageItem(...)
    case "todos":         return NewTodosToolMessageItem(...)
    case "references":    return NewReferencesToolMessageItem(...)
    case "lsp_restart":   return NewLSPRestartToolMessageItem(...)
    default:
        if strings.HasPrefix(name, "mcp_docker_"): return NewDockerMCPToolMessageItem(...)
        if strings.HasPrefix(name, "mcp_"):        return NewMCPToolMessageItem(...)
        return NewGenericToolMessageItem(...)
    }
}
```

---

## 4. baseToolMessageItem 核心结构

所有工具消息项都组合了 `baseToolMessageItem`：

```go
type baseToolMessageItem struct {
    *highlightableMessageItem  // 文本选择高亮
    *cachedMessageItem         // 渲染结果缓存
    *focusableMessageItem      // 焦点状态

    toolRenderer ToolRenderer    // 具体工具的渲染逻辑 (ToolRenderer 接口)
    toolCall     message.ToolCall
    result       *message.ToolResult
    messageID    string
    status       ToolStatus

    hasCappedWidth bool        // 是否限制最大宽度(120) — Edit/MultiEdit 为 false
    isCompact      bool        // 紧凑模式（嵌套工具标记）
    spinningFunc   SpinningFunc  // 自定义旋转逻辑 — nil 时使用默认
    expandedContent bool        // 是否展开内容

    sty *styles.Styles
    anim *anim.Anim
}
```

### 4.1 渲染管线

```
Render(width int) string              // 添加行前缀（focused/blurred/compact）
  ├── 判断 isCompact / focused / blurred
  ├── 获取对应 prefix style
  ├── 调用 RawRender(width)
  └── 将 prefix 逐行添加到每行开头

RawRender(width int) string           // 实际内容渲染（含缓存逻辑）
  ├── 计算实际渲染宽度
  │   ├── hasCappedWidth → cappedMessageWidth(width)
  │   └── 否则 → width - MessageLeftPaddingTotal
  ├── 检查缓存 getCachedRender()
  │   ├── cache hit → 返回
  │   └── cache miss / spinning → 执行渲染
  ├── 调用 toolRenderer.RenderTool(sty, toolItemWidth, &ToolRenderOpts{...})
  ├── 检查 hook indicator
  │   └── 如果有 hook 元数据 → 在顶部添加 hook 行
  ├── lipgloss.Height(content) → 缓存
  └── renderHighlighted(content, ...) // 应用文本选择高亮
```

### 4.2 渲染选项 `ToolRenderOpts`

```go
type ToolRenderOpts struct {
    ToolCall        message.ToolCall
    Result          *message.ToolResult
    Anim            *anim.Anim
    ExpandedContent bool
    Compact         bool
    IsSpinning      bool
    Status          ToolStatus
}

// 辅助方法
func (o *ToolRenderOpts) IsPending() bool  → !o.ToolCall.Finished && !o.IsCanceled()
func (o *ToolRenderOpts) HasResult() bool  → o.Result != nil
func (o *ToolRenderOpts) HasEmptyResult() bool → o.Result == nil || o.Result.Content == ""
func (o *ToolRenderOpts) IsCanceled() bool → o.Status == ToolStatusCanceled
```

### 4.3 computeStatus 状态计算

```go
func (t *baseToolMessageItem) computeStatus() ToolStatus {
    if t.result != nil {
        if t.result.IsError {
            return ToolStatusError
        }
        return ToolStatusSuccess  // 有结果 → Success
    }
    return t.status  // 无结果 → 返回当前状态
}
```

### 4.4 isSpinning 旋转判断

```go
func (t *baseToolMessageItem) isSpinning() bool {
    if t.spinningFunc != nil {
        // Agent 和 AgenticFetch 使用自定义逻辑
        return t.spinningFunc(SpinningState{ToolCall, Result, Status})
    }
    // 默认：未完成且未取消 → 旋转
    return !t.toolCall.Finished && t.status != ToolStatusCanceled
}
```

---

## 5. 通用渲染组件

### 5.1 宽度常量与函数

```go
const (
    MessageLeftPaddingTotal  = 2  // 消息行前缀（左边框 + padding）占 2 格
    maxTextWidth             = 120 // 内容最大宽度（保证可读性）
    toolBodyLeftPaddingTotal = 2  // Body 内容左侧内边距
    responseContextHeight    = 10 // 折叠状态下最大显示行数
    maxCollapsedThinkingHeight = 10 // 思考区域最大高度
)

func cappedMessageWidth(availableWidth int) int {
    return min(availableWidth - MessageLeftPaddingTotal, maxTextWidth)
}
```

### 5.2 pendingTool — 运行中状态

**渲染输出**:

```
● Bash ████████~!@....
```

**代码**:

```go
func pendingTool(sty *styles.Styles, name string, anim *anim.Anim, nested bool) string {
    icon := sty.Tool.IconPending.Render()  // "●" 绿色
    nameStyle := sty.Tool.NameNormal       // 蓝色
    if nested {
        nameStyle = sty.Tool.NameNested    // 蓝色（略浅）
    }
    toolName := nameStyle.Render(name)
    animView := anim.Render()              // 渐变 cycling + 省略号
    return fmt.Sprintf("%s %s %s", icon, toolName, animView)
}
```

### 5.3 toolHeader — 工具头部

**渲染输出**:

```
✓ Bash ls -la --color=auto
✓ Edit /path/to/file
● View src/main.go (limit 100)
× Fetch https://example.com
```

**代码**:

```go
func toolHeader(sty *styles.Styles, status ToolStatus, name string, width int, nested bool, params ...string) string {
    icon := toolIcon(sty, status)          // 根据 status 选择图标
    nameStyle := sty.Tool.NameNormal       // 蓝色
    if nested {
        nameStyle = sty.Tool.NameNested
    }
    toolName := nameStyle.Render(name)
    prefix := fmt.Sprintf("%s %s ", icon, toolName)
    prefixWidth := lipgloss.Width(prefix)
    remainingWidth := width - prefixWidth
    paramsStr := toolParamList(sty, params, remainingWidth)
    return prefix + paramsStr
}
```

**参数渲染规则** (`toolParamList`):

```go
func toolParamList(sty *styles.Styles, params []string, width int) string {
    const minSpaceForMainParam = 30

    mainParam := params[0]  // 第一个参数始终显示

    // 成对 key=value
    var kvPairs []string
    for i := 1; i+1 < len(params); i += 2 {
        if params[i+1] != "" {
            kvPairs = append(kvPairs, fmt.Sprintf("%s=%s", params[i], params[i+1]))
        }
    }

    output := mainParam
    if len(kvPairs) > 0 {
        partsStr := strings.Join(kvPairs, ", ")
        if remaining := width - lipgloss.Width(partsStr) - 3; remaining >= minSpaceForMainParam {
            output = fmt.Sprintf("%s (%s)", mainParam, partsStr)
        }
    }

    if width >= 0 {
        output = ansi.Truncate(output, width, "…")
    }
    return sty.Tool.ParamMain.Render(output)
}
```

### 5.4 toolEarlyStateContent — 早期状态消息

**渲染输出**:

| Status | 显示内容 |
|---|---|
| `ToolStatusError` | `× Edit /path/to/file ERROR <错误内容>` |
| `ToolStatusCanceled` | `● Edit /path/to/file Canceled.` |
| `ToolStatusAwaitingPermission` | `● Edit /path/to/file Requesting permission...` |
| `ToolStatusRunning` | `● Edit /path/to/file Waiting for tool response...` |

```go
func toolEarlyStateContent(sty *styles.Styles, opts *ToolRenderOpts, width int) (string, bool) {
    var msg string
    switch opts.Status {
    case ToolStatusError:
        msg = toolErrorContent(sty, opts.Result, width)
    case ToolStatusCanceled:
        msg = sty.Tool.StateCancelled.Render("Canceled.")
    case ToolStatusAwaitingPermission:
        msg = sty.Tool.StateWaiting.Render("Requesting permission...")
    case ToolStatusRunning:
        msg = sty.Tool.StateWaiting.Render("Waiting for tool response...")
    default:
        return "", false  // 非早期状态，继续渲染
    }
    return msg, true
}

func toolErrorContent(sty *styles.Styles, result *message.ToolResult, width int) string {
    errContent := strings.ReplaceAll(result.Content, "\n", " ")
    errTag := sty.Tool.ErrorTag.Render("ERROR")
    tagWidth := lipgloss.Width(errTag)
    errContent = ansi.Truncate(errContent, width-tagWidth-3, "...")
    return fmt.Sprintf("%s %s", errTag, sty.Tool.ErrorMessage.Render(errContent))
}
```

### 5.5 toolIcon — 状态图标映射

| Status | 图标 | 样式 |
|---|---|---|
| `ToolStatusSuccess` | `✓` | 绿色 `IconSuccess` |
| `ToolStatusError` | `×` | 红色 `IconError` |
| `ToolStatusCanceled` | `●` | 灰色 `IconCancelled` |
| `ToolStatusRunning` | `●` | 绿色暗色 `IconPending` |
| `ToolStatusAwaitingPermission` | `●` | 绿色暗色 `IconPending` |

### 5.6 joinToolParts — 拼接头部和主体

```go
func joinToolParts(header, body string) string {
    return strings.Join([]string{header, "", body}, "\n")
}
```

头部和主体之间用空行分隔。

---

## 6. 工具结果内容渲染

### 6.1 renderToolResultTextContent — 智能内容判断

```go
func renderToolResultTextContent(sty *styles.Styles, content string, widths toolResultContentWidths, expanded bool) string {
    // 1. JSON — 美化格式化为代码
    if err := json.Unmarshal([]byte(content), &result); err == nil {
        prettyResult, _ := json.MarshalIndent(result, "", "  ")
        return sty.Tool.Body.Render(toolOutputCodeContent(sty, "result.json", string(prettyResult), 0, widths.Body, expanded))
    }

    // 2. Unified Diff — 解析并渲染为 diff view
    if diffdetect.IsUnifiedDiff(content) {
        return toolOutputDiffContentFromUnified(sty, content, widths.Diff, expanded)
    }

    // 3. Markdown — 带语法高亮
    if looksLikeMarkdown(content) {
        return sty.Tool.Body.Render(toolOutputCodeContent(sty, "result.md", content, 0, widths.Body, expanded))
    }

    // 4. Plain text
    return sty.Tool.Body.Render(toolOutputPlainContent(sty, content, widths.Body, expanded))
}
```

**Markdown 检测模式** (`looksLikeMarkdown`):

检测以下模式是否存在：`# `, `## `, `**`, ``` ` `, `- `, `1. `, `> `, `---`, `***`

### 6.2 toolOutputPlainContent — 纯文本

**渲染输出** (折叠):

```
 command output line 1
 command output line 2
 ...
 command output line 10
   ... (25 lines hidden) [click or space to expand]
```

**渲染输出** (展开):

```
 command output line 1
 command output line 2
 ...
 command output line 35
```

**逻辑**:
- 每行前加 1 个空格
- 样式：`ContentLine`（灰色 `fgMoreSubtle` 文本 + `bgLeastVisible` 背景 + 固定宽度）
- 最大显示 10 行（折叠）/ 全部（展开）
- 超出时使用截断提示

### 6.3 toolOutputCodeContent — 代码 + 语法高亮

**渲染输出**:

```
 ┃ 1  package main
 ┃ 2
 ┃ 3  import (
 ┃ 4      "fmt"
 ┃ 5  )
 ┃    ... (5 lines hidden) [click or space to expand]
```

**逻辑**:
1. 使用 Chroma 语法高亮（`common.SyntaxHighlight`），基于文件扩展名选择 lexer
2. 行号宽度 = `digits(len(lines) + offset)`（如 3 行 → `%3d` 格式）
3. 代码宽度 = `bodyWidth - maxDigits`
4. 每行左侧 2 格 padding（`ContentCodeLine` 样式）
5. 背景色 = `ContentCodeBg`
6. 最大 10 行（折叠）

### 6.4 toolOutputMarkdownContent — Markdown 渲染

使用 glamour 的 `QuietMarkdownRenderer`（低对比度 Markdown 样式）渲染，限制最大 10 行。

### 6.5 toolOutputDiffContent — 差异对比

**渲染输出** (Unified):

```
──── before: main.go ────
-    old line
+    new line
 ═  unchanged line
──── after: main.go ────
```

**渲染输出** (Split, 宽屏):

```
──── before: main.go ──────── │ ────── after: main.go ────────
-    old line                 │ +    new line
 ═  unchanged line            │ ═  unchanged line
```

**逻辑**:
- 使用 `common.DiffFormatter(sty)` 创建 diff view
- 绑定 Chroma 语法高亮 + diff 主题样式
- 终端宽 > 120 时自动切换 split view
- 最大 10 行

### 6.6 toolOutputDiffContentFromUnified — Unified Diff 解析

解析 unified diff 格式：

```go
func parseUnifiedDiff(content string) []parsedDiffFile
```

**解析规则**:

| 行前缀 | 处理 |
|---|---|
| `diff --git a/foo.go b/foo.go` | 提取文件名 `foo.go`（去掉 `b/`），重置 hunk 状态 |
| `@@ ... @@` | 进入 hunk 模式 |
| `--- a/foo.go` | 提取文件名（去掉 `a/`，截断 `\t` 后部分）|
| `+++ b/foo.go` | 提取文件名（去掉 `b/`）|
| `-` 开头 | 去掉 `-` 写入 before |
| `+` 开头 | 去掉 `+` 写入 after |
| ` ` 开头（空格） | 同时写入 before 和 after（上下文行）|

### 6.7 toolOutputImageContent — 图片指示器

**渲染输出**:

```
Loaded Image → image/png 1.2 KB
```

### 6.8 toolOutputSkillContent — Skill 加载指示器

**渲染输出**:

```
Loaded Skill → my-skill This skill does X
```

### 6.9 toolOutputHookIndicator — Hook 指示器

**渲染输出**:

```
Hook path/to/format.sh     *\.go$  → OK
Hook pre-commit.sh              → Denied too many changes
```

**逻辑**:
- 从 `result.Metadata` 中解析 JSON 格式的 hooks
- 每行格式：`Hook ` + name(padded) + ` ` + matcher(padded) + ` → ` + detail
- 名称列最大 30 字符，路径左截断（`.../format.sh`），其他右截断
- 结果：OK / OK Rewrote Output / Denied reason

---

## 7. 特殊工具渲染

### 7.1 Agent 工具（支持嵌套工具调用）

**渲染输出**:

```
● Agent ████████~!@....
```

(pending，无嵌套时)

```
╭── Task Write a new feature
│
├── ─── Bash npm test
│
├── ─── View src/main.go
│     ┃ 1  package main
│     ┃ 2
│     ┃ 3  func main() {
│     ┃    ... (5 lines hidden) [click or space to expand]
├── ─── Edit src/main.go
│     ┃ 1  - old code
│     ┃ 2  + new code
```

**渲染逻辑**:

1. **Pending 判断**: 如果 `!Finished && !canceled && len(nestedTools) == 0` → 显示 `pendingTool`
2. **参数解析**: JSON 解包 `AgentParams` → 获取 `Prompt`，替换换行为空格
3. **头部构建**: `Task` 标签（蓝色背景 + 粗体）+ prompt 文本
4. **树形嵌套**: `tree.Root(header)` + 每个嵌套工具 `tree.Child(childView)`
5. **分支线**: `roundedEnumerator(2, tagWidth-5)` → `├── ` / `╰── `
6. **嵌套工具**: 自动标记为 compact 模式
7. **Body**: 已完成且有结果 → 显示 Markdown 渲染的内容

**compact 模式**: 只显示一行头部，无 body。

### 7.2 AgenticFetch 工具

与 Agent 类似，差异点：
- 显示 `Prompt` 标签（绿色背景 + 粗体）
- 参数中包含 URL 和 Prompt

### 7.3 Todos 工具

**Header 显示**:

| 场景 | Header |
|---|---|
| 新建 | `created N todos` |
| 刚开始 | `created N todos, starting first` |
| 完成中 | `N/M · completed K, starting next` |
| 完成中(仅完成) | `N/M · completed K` |
| 完成中(仅开始) | `N/M · starting task` |
| 正常进度 | `N/M` |

**Body 显示**（仅新建/全部完成/刚开始时）:

```
✓ Task 1 - completed task
→ Task 2 - in progress task
• Task 3 - pending task
```

- 状态排序：completed → in_progress → pending
- 图标：`✓` (绿色) / `→` (绿色暗色) / `•` (灰色)
- 每行截断到可用宽度

### 7.4 Edit / MultiEdit 工具

- 使用 **full width**（`hasCappedWidth = false`，不限制到 120）
- 从 `result.Metadata` 解析 diff 信息（`EditResponseMetadata` / `MultiEditResponseMetadata`）
- 渲染为 diff view（split view 如果宽度 > 120）
- MultiEdit 额外显示失败编辑提示：`Note 2 of 5 edits succeeded`

### 7.5 Bash 工具

**普通命令**:

```
● Bash ls -la --color=auto
 output text line 1
 output text line 2
```

**后台任务**:

```
● Job (Start) PID 12345 command description...
Command: ls -la --color=auto
output text
```

**Job header**: `● Job (Action) PID <shellID> description...`

### 7.6 MCP 工具

**名称格式**: `mcp_provider_tool` → `Provider → Tool`

**渲染输出**:

```
GitHub → List Issues
```

**参数**: 原始 JSON 字符串

### 7.7 Docker MCP 工具

**名称格式**: `Docker MCP → Exec`

**特殊渲染**:
- `mcp-find`: 渲染 MCP 服务器表格（名称 + 描述），最多显示 10 个，多余的显示 `... and N more`
- `mcp-add`: 使用 `ActionCreate` 绿色样式
- `mcp-remove`: 使用 `ActionDestroy` 红色样式

### 7.8 其他工具

| 工具 | 头部参数 | Body 渲染 |
|---|---|---|
| View | `PrettyPath(filePath)` | 代码高亮（带 offset 行号）|
| Write | `PrettyPath(filePath)` | 代码高亮 |
| Glob | `pattern` + `path` | 纯文本 |
| Grep | `pattern` + `path` + `include` + `literal` | 纯文本 |
| LS | `PrettyPath(path)` | 纯文本 |
| Download | `url` + `file_path` + `timeout` | 纯文本 |
| Fetch | `url` + `format` + `timeout` | 代码高亮（根据 format 选扩展名）|
| WebFetch | `url` | Markdown 渲染 |
| WebSearch | `query` | Markdown 渲染 |
| Sourcegraph | `query` + `count` + `context` | 纯文本 |
| Diagnostics | `project` / `PrettyPath(filePath)` | 纯文本 |
| Find References | `symbol` + `PrettyPath(path)` | 纯文本 |
| Restart LSP | `name` | 纯文本 |
| Generic | `name` + 原始 JSON | JSON/Diff/Markdown/Plain 智能判断 |

---

## 8. 动画系统 (`anim.Anim`)

### 8.1 配置

```go
anim.New(anim.Settings{
    ID:          toolCall.ID,             // 唯一标识，用于匹配 StepMsg
    Size:        15,                      // cycling 字符数
    GradColorA:  sty.WorkingGradFromColor, // 渐变起始色
    GradColorB:  sty.WorkingGradToColor,   // 渐变结束色
    LabelColor:  sty.WorkingLabelColor,
    CycleColors: true,
})
```

### 8.2 动画阶段

```
时间线:  T0 ──→ T1 ──→ T2 ──→ T3 ──→ T4 ──→ T5
         │      │      │      │      │      │
         ▼      ▼      ▼      ▼      ▼      ▼
       入场   全部    标签    省略号
       动画   可见    出现    循环
```

1. **入场 (0 ~ 1s)**: 每个字符随机延迟 0~1 秒出现（staggered entrance），初始字符为 `.`
2. **Cycling (持续)**: 15 个字符循环显示随机符号
3. **渐变 (持续)**: 字符颜色在 GradColorA 和 GradColorB 之间平滑过渡
4. **标签 (入场后)**: 显示 Label 文本（如 "Thinking"、"Summarizing"）
5. **省略号 (持续)**: `.  ..  ...  ` 循环动画

### 8.3 动画帧生成

```
可用符号集: "0123456789abcdefABCDEF~!@#$£€%^&*()+=_"

帧结构: [cycling chars] [gap] [label] [ellipsis]
         ████████~!@     " "   Think   ...
```

### 8.4 帧率与缓存

- **帧率**: 20 FPS（50ms 间隔）
- **预渲染帧数**: `width * 2` 帧（CycleColors=true）或 10 帧
- **缓存 key**: `settingsHash(opts)` = `Size-Label-ColorA-ColorB-CycleColors` 的 xxh3 hash
- 相同配置的动画共享帧数据

### 8.5 Bubbletea 集成

```go
func (a *Anim) Start() tea.Cmd {
    return a.Step()  // tea.Tick(50ms, ...) → 返回 StepMsg
}

func (a *Anim) Animate(msg anim.StepMsg) tea.Cmd {
    if msg.ID != a.id { return nil }
    a.step.Add(1)
    // ... 更新 ellipsis step
    return a.Step()  // 继续定时触发
}
```

---

## 9. 样式系统 (`styles.Styles`)

### 9.1 工具相关样式完整列表

**图标**:

| 字段 | 渲染 | 默认色 |
|---|---|---|
| `Tool.IconPending` | `●` | 绿色暗色 |
| `Tool.IconSuccess` | `✓` | 绿色 |
| `Tool.IconError` | `×` | 红色 |
| `Tool.IconCancelled` | `●` | 灰色 |

**名称**:

| 字段 | 用途 | 默认色 |
|---|---|---|
| `Tool.NameNormal` | 顶层工具名 | 蓝色 (`info`) |
| `Tool.NameNested` | 嵌套工具名 | 蓝色 (`info`) |

**参数**:

| 字段 | 用途 | 默认色 |
|---|---|---|
| `Tool.ParamMain` | 主参数 | 浅灰色 (`fgMostSubtle`) |
| `Tool.ParamKey` | key=value 中的 key | 浅灰色 |

**内容渲染**:

| 字段 | 用途 | 默认样式 |
|---|---|---|
| `Tool.ContentLine` | 纯文本行 | 灰色 + 浅灰背景 + 固定宽度 |
| `Tool.ContentTruncation` | 截断提示 | 灰色 + 浅灰背景 |
| `Tool.ContentCodeLine` | 代码行 | 前景 + 背景 + 左 padding 2 |
| `Tool.ContentCodeTruncation` | 代码截断 | 灰色 + 背景 + 左 padding 2 |
| `Tool.ContentCodeBg` | 代码背景色 | `bgBase` |
| `Tool.Body` | 内容容器 | 前景 + 左 padding 2 |

**状态消息**:

| 字段 | 用途 | 默认样式 |
|---|---|---|
| `Tool.StateWaiting` | "Waiting..." / "Requesting..." | 前景 |
| `Tool.StateCancelled` | "Canceled." | 前景 |

**错误**:

| 字段 | 用途 | 默认样式 |
|---|---|---|
| `Tool.ErrorTag` | `ERROR` 标签 | 红色背景 + 反色文字 |
| `Tool.ErrorMessage` | 错误信息 | 前景 |

**Diff**:

| 字段 | 用途 | 默认样式 |
|---|---|---|
| `Tool.DiffTruncation` | Diff 截断 | 灰色 + 浅灰背景 + 左 padding 2 |
| `Tool.NoteTag` | `Note` 标签 | 蓝色背景 + 反色文字 |
| `Tool.NoteMessage` | Note 消息 | 前景 |

**Job**:

| 字段 | 用途 | 默认色 |
|---|---|---|
| `Tool.JobIconPending` | Job 运行中图标 | 绿色暗色 |
| `Tool.JobIconError` | Job 错误图标 | 红色 |
| `Tool.JobIconSuccess` | Job 成功图标 | 绿色 |
| `Tool.JobToolName` | "Job" 名称 | 蓝色 |
| `Tool.JobAction` | 动作 (Start/Output/Kill) | 蓝色暗色 |
| `Tool.JobPID` | PID 文本 | 灰色 |
| `Tool.JobDescription` | 描述文本 | 浅灰 |

**Agent**:

| 字段 | 用途 | 默认样式 |
|---|---|---|
| `Tool.AgentTaskTag` | `Task` 标签 | 蓝色背景 + 白色粗体 + 左 margin 2 |
| `Tool.AgentPrompt` | Prompt 文本 | 灰色 |

**AgenticFetch**:

| 字段 | 用途 | 默认样式 |
|---|---|---|
| `Tool.AgenticFetchPromptTag` | `Prompt` 标签 | 绿色背景 + 白色粗体 + 左 margin 2 |

**Todos**:

| 字段 | 用途 | 默认色 |
|---|---|---|
| `Tool.TodoRatio` | 进度比 (2/5) | 蓝色暗色 |
| `Tool.TodoCompletedIcon` | `✓` | 绿色 |
| `Tool.TodoInProgressIcon` | `→` | 绿色暗色 |
| `Tool.TodoPendingIcon` | `•` | 灰色 |
| `Tool.TodoStatusNote` | 状态备注 | 浅灰 |
| `Tool.TodoItem` | 任务项文本 | 前景 |
| `Tool.TodoJustStarted` | 刚开始的任务 | 前景 |

**MCP**:

| 字段 | 用途 | 默认色 |
|---|---|---|
| `Tool.MCPName` | MCP 提供商名 | 蓝色 |
| `Tool.MCPToolName` | MCP 工具名 | 蓝色暗色 |
| `Tool.MCPArrow` | `→` | 蓝色 |

**Hooks**:

| 字段 | 用途 | 默认色 |
|---|---|---|
| `Tool.HookLabel` | `Hook` 标签 | 绿色暗色 |
| `Tool.HookName` | Hook 脚本名 | 前景 |
| `Tool.HookMatcher` | 匹配器 | 灰色 |
| `Tool.HookArrow` | `→` | 绿色暗色 |
| `Tool.HookDetail` | 详情 | 灰色 |
| `Tool.HookOK` | `OK` | 绿色暗色 |
| `Tool.HookDenied` | `Denied` | 红色 |
| `Tool.HookDeniedLabel` | Hook(拒绝) | 破坏色 |
| `Tool.HookDeniedReason` | 拒绝原因 | 浅灰背景 |
| `Tool.HookRewrote` | `Rewrote Output` | 浅灰背景 |

**其他**:

| 字段 | 用途 | 默认样式 |
|---|---|---|
| `Tool.ActionCreate` | 创建操作 | 绿色暗色 |
| `Tool.ActionDestroy` | 删除操作 | 破坏色 |
| `Tool.ResultEmpty` | "No results" | 浅灰 |
| `Tool.ResultTruncation` | "... and N more" | 浅灰 |
| `Tool.ResultItemName` | 列表项名称 | 前景 |
| `Tool.ResultItemDesc` | 列表项描述 | 浅灰 |

### 9.2 消息项样式

| 字段 | 用途 | 默认样式 |
|---|---|---|
| `Messages.ToolCallFocused` | 聚焦工具行前缀 | 灰色 + 左边框 + 绿色 + 窄边框 |
| `Messages.ToolCallBlurred` | 失焦工具行前缀 | 灰色 + 左 padding 2 |
| `Messages.ToolCallCompact` | 紧凑模式行前缀 | 灰色（无边框无 padding）|

### 9.3 样式初始化

主题基于 `quickStyleOpts` 颜色调色板（`charmtone.Pantera`）：

```go
quickStyle(quickStyleOpts{
    primary:        color1,
    secondary:      color2,
    fgBase:         foreground,
    bgBase:         background,
    fgMoreSubtle:   gray1,
    fgMostSubtle:   gray2,
    error:          red,
    success:        green,
    successMoreSubtle: green2,
    successMostSubtle: green3,
    info:           blue,
    infoMoreSubtle: blue2,
    infoMostSubtle: blue3,
    destructive:    darkRed,
    warning:        yellow,
    warningSubtle:  amber,
    separator:      separatorGray,
    bgMostVisible:  bgLight,
    bgLessVisible:  bgLighter,
    bgLeastVisible: bgLightest,
    onPrimary:      onPrimaryColor,
    busy:           orange,
})
```

---

## 10. 树形嵌套渲染

### 10.1 roundedEnumerator — 分支线生成器

```go
func roundedEnumerator(lPadding, width int) tree.Enumerator {
    if width == 0 { width = 2 }
    if lPadding == 0 { lPadding = 1 }

    return func(children tree.Children, index int) string {
        line := strings.Repeat("─", width)
        padding := strings.Repeat(" ", lPadding)
        if children.Length()-1 == index {
            // 最后一项: ╰───
            return padding + "╰" + line
        }
        // 中间项: ├───
        return padding + "├" + line
    }
}
```

**使用**: `tree.Root(header).Enumerator(roundedEnumerator(2, tagWidth-5)).String()`

### 10.2 树形结构构建

```go
// Agent 工具中
childTools := tree.Root(header)
for _, nestedTool := range agent.nestedTools {
    childView := nestedTool.Render(remainingWidth)
    childTools.Child(childView)
}
parts = append(parts, childTools.Enumerator(roundedEnumerator(2, taskTagWidth-5)).String())
```

---

## 11. 完整渲染示例

### 11.1 Bash — 成功

```
  ● Bash ls -la --color=auto
   command output text line 1
   command output text line 2
   command output text line 3
```

### 11.2 Bash — 运行中

```
  ● Bash ls -la  ████████~!@....
```

### 11.3 Edit — 成功

```
  ● Edit /path/to/file
   ┃ 1  - old line 1
   ┃ 2  - old line 2
   ┃ 3  - old line 3
   ┃ 4  + new line 1
   ┃ 5  + new line 2
   ┃ 6  + new line 3
   ┃ 7    unchanged line
   ┃ 8    unchanged line
   ┃ 9    unchanged line
   ┃ 10   unchanged line
     ┃ ... (15 lines hidden) [click or space to expand]
```

### 11.4 Agent — 有嵌套

```
  ╭── Task Write a new feature
  │
  ├── ─── Bash npm test
  │
  ├── ─── View src/main.go
  │     ┃ 1  package main
  │     ┃ 2
  │     ┃ 3  func main() {
  │     ┃    ... (5 lines hidden) [click or space to expand]
  ├── ─── Edit src/main.go
  │     ┃ 1  - old code
  │     ┃ 2  + new code
```

### 11.5 Agent — 运行中（无嵌套）

```
  ● Agent ████████~!@....
```

### 11.6 Agent — 有嵌套且运行中

```
  ╭── Task Write a new feature
  │
  ├── ─── Bash npm test
  │
  ├── ─── Edit src/main.go  ████████~!@....
```

### 11.7 Todos — 正常进度

```
  ● To-Do 2/5 · completed 1
   → Task 2 - starting next
```

### 11.8 紧凑模式

```
    ● Bash ls -la
    ● Edit /path/to/file
    ● View src/main.go
```

### 11.9 错误状态

```
  × Edit /path/to/file ERROR failed to apply edit: invalid replacement
```

### 11.10 取消状态

```
  ● Edit /path/to/file Canceled.
```

### 11.11 Waiting 状态

```
  ● Edit /path/to/file Waiting for tool response...
```

### 11.12 Permission 状态

```
  ● Edit /path/to/file Requesting permission...
```

### 11.13 MCP 工具

```
  GitHub → List Issues
```

### 11.14 Docker MCP — Find

```
  Docker MCP → Find
   github-cli  Manage GitHub repositories and pull requests
   kubernetes  Kubernetes cluster management
   docker      Docker container management
   ... and 5 more
```

---

## 12. 交互行为

### 12.1 展开/折叠

- `ToggleExpanded()` 切换 `expandedContent` 状态
- 折叠: 显示 10 行 + 截断提示
- 展开: 显示全部内容
- 快捷键: `c` 或 `y` 复制内容到剪贴板

### 12.2 文本选择

- 通过 `highlightableMessageItem` 支持
- 设置 `startLine/startCol/endLine/endCol`
- 使用 `list.Highlight(content, area, ...)` 进行区域高亮
- 鼠标拖拽选择文本

### 12.3 鼠标点击

- `HandleMouseClick(btn, x, y)`: 左键点击触发展开/折叠（针对 Agent 工具）
- Agent 工具点击 thinking box 区域可展开

### 12.4 焦点管理

- `SetFocused(focused bool)` 设置焦点状态
- 焦点行显示左边框 + 绿色边框色
- 非焦点行仅左 padding

### 12.5 缓存失效

- `clearCache()` 清除缓存
- 状态变更时调用：`SetToolCall`, `SetResult`, `SetStatus`, `ToggleExpanded`, `SetCompact`

---

## 13. 关键常量

```go
const (
    MessageLeftPaddingTotal       = 2   // 消息行前缀宽度
    maxTextWidth                  = 120 // 内容最大宽度
    toolBodyLeftPaddingTotal      = 2   // Body 左侧 padding
    responseContextHeight         = 10  // 内容最大显示行数（折叠）
    maxCollapsedThinkingHeight    = 10  // 思考区域最大高度

    ToolPending   = "●"
    ToolSuccess   = "✓"
    ToolError     = "×"
    ArrowRightIcon = "→"
)

const assistantMessageTruncateFormat = "… (%d lines hidden) [click or space to expand]"
```

---

## 14. 复刻检查清单

复刻时应确保实现以下组件：

- [ ] **消息数据模型**: `ToolCall`, `ToolResult` 结构
- [ ] **MessageItem 接口**: `ID()`, `Render()`, `RawRender()`
- [ ] **ToolMessageItem 接口**: 扩展 `MessageItem`，增加 `ToolCall()`, `SetResult()`, `Status()` 等
- [ ] **ToolStatus 枚举**: 5 种状态
- [ ] **ToolRenderOpts 结构体**: 渲染选项
- [ ] **NewToolMessageItem 工厂**: 按工具名路由到具体渲染器
- [ ] **baseToolMessageItem**: 组合 `highlightableMessageItem`, `cachedMessageItem`, `focusableMessageItem`
- [ ] **渲染管线**: `Render → RawRender → toolRenderer.RenderTool`
- [ ] **缓存机制**: `getCachedRender / setCachedRender / clearCache`
- [ ] **pendingTool**: 运行中动画显示
- [ ] **toolHeader**: 工具头部（图标 + 名称 + 参数）
- [ ] **toolParamList**: 参数格式化（主参数 + key=value 对）
- [ ] **toolEarlyStateContent**: 早期状态消息
- [ ] **toolIcon**: 状态图标映射
- [ ] **joinToolParts**: 头部和主体拼接
- [ ] **toolOutputPlainContent**: 纯文本渲染
- [ ] **toolOutputCodeContent**: 代码 + 语法高亮 + 行号
- [ ] **toolOutputMarkdownContent**: Markdown 渲染
- [ ] **toolOutputDiffContent**: Diff 渲染
- [ ] **toolOutputDiffContentFromUnified**: Unified Diff 解析
- [ ] **renderToolResultTextContent**: 智能内容判断（JSON/Diff/Markdown/Plain）
- [ ] **工具结果辅助**: `toolOutputImageContent`, `toolOutputSkillContent`, `toolOutputHookIndicator`
- [ ] **动画系统**: gradient cycling + staggered entrance + label + ellipsis
- [ ] **树形嵌套**: Agent 工具的 `tree.Root/Child/Enumerator` + `roundedEnumerator`
- [ ] **紧凑模式**: `isCompact` + `ToolCallCompact` 样式
- [ ] **宽度截断**: capped at 120 + ansi.Truncate
- [ ] **文本高亮**: `highlightableMessageItem`
- [ ] **焦点管理**: `focusableMessageItem`
- [ ] **每种工具的 RenderTool**: Bash, Edit, View, Write, Agent 等至少 10+ 种
- [ ] **样式系统**: `styles.Styles.Tool` 和 `styles.Styles.Messages` 相关字段
