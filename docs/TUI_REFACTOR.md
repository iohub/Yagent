# CodeActor TUI 重构技术文档

> 完全参考 crush TUI 实现方式，重构 CodeActor current TUI (`internal/tui/`)

---

## 目录

1. [现状分析](#1-现状分析)
   - 1.1 [crush TUI 架构概览](#11-crush-tui-架构概览)
   - 1.2 [CodeActor 当前 TUI 架构概览](#12-codeactor-当前-tui-架构概览)
   - 1.3 [核心差距对比](#13-核心差距对比)
2. [crush TUI 设计精髓](#2-crush-tui-设计精髓)
   - 2.1 [布局引擎：ultraviolet ScreenBuffer + 动态布局计算](#21-布局引擎)
   - 2.2 [列表虚拟化：Generic List Widget](#22-列表虚拟化)
   - 2.3 [消息渲染管道：Item 接口 + Render 回调](#23-消息渲染管道)
   - 2.4 [状态机驱动的视图切换](#24-状态机驱动的视图切换)
   - 2.5 [动画系统：可见性感知的帧管理](#25-动画系统)
   - 2.6 [Dialog 栈式弹窗系统](#26-dialog-栈式弹窗系统)
   - 2.7 [Pub-Sub 事件驱动](#27-pub-sub-事件驱动)
   - 2.8 [样式系统：主题化 + 渐变](#28-样式系统)
3. [目标架构设计](#3-目标架构设计)
   - 3.1 [包结构重组](#31-包结构重组)
   - 3.2 [核心类型设计](#32-核心类型设计)
   - 3.3 [状态机设计](#33-状态机设计)
   - 3.4 [布局引擎设计](#34-布局引擎设计)
   - 3.5 [列表组件设计](#35-列表组件设计)
   - 3.6 [消息项接口](#36-消息项接口)
   - 3.7 [Render/Draw 管道](#37-renderdraw-管道)
   - 3.8 [Update 管道](#38-update-管道)
4. [分阶段实现计划](#4-分阶段实现计划)
   - 4.1 [Phase 1: 基础架构重构](#41-phase-1-基础架构重构)
   - 4.2 [Phase 2: 列表虚拟化 + 消息项](#42-phase-2-列表虚拟化--消息项)
   - 4.3 [Phase 3: 布局引擎](#43-phase-3-布局引擎)
   - 4.4 [Phase 4: 动画系统](#44-phase-4-动画系统)
   - 4.5 [Phase 5: 状态机 + 视图](#45-phase-5-状态机--视图)
   - 4.6 [Phase 6: Dialog 系统完善](#46-phase-6-dialog-系统完善)
   - 4.7 [Phase 7: 主题/样式系统](#47-phase-7-主题样式系统)
   - 4.8 [Phase 8: 测试 + 清理](#48-phase-8-测试--清理)
5. [附录](#5-附录)
   - A. [关键文件对照表](#a-关键文件对照表)
   - B. [crush UI 完整文件清单](#b-crush-ui-完整文件清单)

---

## 1. 现状分析

### 1.1 crush TUI 架构概览

crush 的 TUI 系统位于 `internal/ui/`，是一个成熟、工程化程度极高的 Bubble Tea 终端 UI 实现。其核心特征：

#### 1.1.1 目录结构

```
internal/ui/
├── model/           # 核心 Model 层 (Bubble Tea Model)
│   ├── ui.go        # 顶层 UI struct (~3000行)
│   ├── chat.go      # Chat 消息列表
│   ├── header.go    # 顶部 Header
│   ├── sidebar.go   # 侧边栏
│   ├── status.go    # 状态栏/Help
│   ├── keys.go      # 全局快捷键
│   ├── session.go   # Session 管理
│   ├── landing.go   # Landing 页面
│   ├── onboarding.go # Onboarding 页面
│   ├── pills.go     # 任务/Pills 面板
│   ├── skills.go    # Skills 状态渲染
│   ├── mcp.go       # MCP 状态渲染
│   ├── lsp.go       # LSP 状态渲染
│   ├── filter.go    # 鼠标事件节流
│   └── clipboard.go # 剪贴板抽象
├── chat/            # 消息项渲染器 (每种消息类型一个文件)
│   ├── messages.go  # 消息分发
│   ├── assistant.go # AI 回复渲染
│   ├── user.go      # 用户消息渲染
│   ├── tools.go     # 工具调用渲染
│   ├── bash.go      # Bash 工具渲染
│   ├── file.go      # 文件操作渲染
│   ├── search.go    # 搜索工具渲染
│   ├── fetch.go     # Web 请求渲染
│   ├── todos.go     # Todo 列表渲染
│   ├── unified_diff.go # Diff 渲染
│   ├── agent.go     # 子 Agent 渲染
│   ├── mcp.go       # MCP 工具渲染
│   ├── generic.go   # 通用消息渲染
│   ├── diagnostics.go # LSP 诊断渲染
│   ├── lsp_restart.go  # LSP 重启渲染
│   ├── docker_mcp.go   # Docker MCP 渲染
│   ├── references.go   # 代码引用渲染
│   └── tool_result_content.go # 工具结果内容
├── common/          # 通用 UI 组件
│   ├── common.go    # Common 共享上下文
│   ├── interface.go # Model[T] 泛型接口
│   ├── elements.go  # Section/Status/DialogTitle/Button
│   ├── markdown.go  # Glamour Markdown 渲染
│   ├── scrollbar.go # 纵向滚动条
│   ├── diff.go      # Diff 语法高亮
│   ├── highlight.go # 文本高亮
│   ├── button.go    # 按钮组件
│   └── capabilities.go # 终端能力检测
├── list/            # 通用虚拟化列表
│   ├── list.go      # List 核心
│   ├── item.go      # Item 接口
│   ├── filterable.go # 可过滤列表
│   ├── focus.go     # 焦点管理
│   └── highlight.go # 文本选择高亮
├── dialog/          # 弹窗系统
│   ├── dialog.go    # Overlay 栈管理
│   ├── common.go    # 公共弹窗元素
│   ├── commands.go  # 命令选择器
│   ├── models.go    # 模型选择器
│   ├── sessions.go  # Session 选择器
│   ├── permissions.go # 权限确认
│   ├── filepicker.go # 文件选择器
│   ├── oauth.go     # OAuth 流程
│   ├── quit.go      # 退出确认
│   ├── reasoning.go # 推理设置
│   ├── actions.go   # 操作确认
│   └── api_key_input.go # API Key 输入
├── diffview/        # Diff 查看器
│   ├── diffview.go  # 主视图
│   ├── split.go     # 分屏渲染
│   ├── util.go      # 工具函数
│   └── style.go     # 样式
├── completions/     # @-mention 补全
│   ├── completions.go # 补全浮层
│   ├── item.go      # 补全项
│   └── keys.go      # 快捷键
├── styles/          # 主题/样式系统
│   ├── styles.go    # 样式定义
│   ├── themes.go    # 主题切换
│   ├── grad.go      # 渐变工具
│   └── quickstyle.go # 快速样式
├── anim/            # 动画系统
│   └── anim.go      # 帧动画
├── attachments/     # 文件附件
│   └── attachments.go
├── image/           # 图片渲染
│   └── image.go
├── notification/    # 系统通知
│   └── notification.go
├── logo/            # Logo 渲染
│   ├── logo.go
│   └── letterforms.go
├── util/            # 工具
│   └── util.go
└── xchroma/         # Chroma 语法高亮
    └── chroma.go
```

#### 1.1.2 核心架构模式

```
┌─────────────────────────────────────────────────────┐
│                     UI (model.UI)                     │
│  implements tea.Model (Init/Update/View)              │
│                                                       │
│  ┌─────────────┐ ┌──────────┐ ┌───────────────────┐ │
│  │   header     │ │  Chat    │ │     Status        │ │
│  │  (logo+info) │ │ (*list)  │ │  (help+notify)    │ │
│  └─────────────┘ └──────────┘ └───────────────────┘ │
│  ┌─────────────┐ ┌──────────┐ ┌───────────────────┐ │
│  │  textarea    │ │ dialog   │ │   completions     │ │
│  │  (input)     │ │ (stack)  │ │   (popup)         │ │
│  └─────────────┘ └──────────┘ └───────────────────┘ │
│  ┌─────────────────────────────────────────────────┐ │
│  │              uiLayout (computed rects)           │ │
│  └─────────────────────────────────────────────────┘ │
│  ┌─────────────────────────────────────────────────┐ │
│  │  States: uiState, uiFocusState, session, ...     │ │
│  └─────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────┘
```

**关键设计决策：**

1. **uv.ScreenBuffer 直接像素渲染**：`View()` 不直接返回字符串，而是创建 `uv.ScreenBuffer`，调用 `Draw()` 方法逐步绘制每个区域，最后 `Render()` 输出
2. **灵活的布局计算**：`generateLayout(w, h)` 根据状态、compact 模式、sidebar 宽度等因素动态计算所有区域矩形
3. **列表虚拟化**：`list.List` 只渲染视口内可见的 Item，通过 `offsetIdx` + `offsetLine` 管理滚动
4. **Render 回调链**：`RegisterRenderCallback` 允许在渲染时对 Item 进行变换（如焦点高亮），而不修改原始数据
5. **状态机驱动**：UI 有明确的 4 个状态 (`uiOnboarding` → `uiInitialize` → `uiLanding` → `uiChat`)
6. **Pub-Sub 事件**：通过 `pubsub.Event[T]` 泛型事件系统驱动所有后台数据更新

### 1.2 CodeActor 当前 TUI 架构概览

#### 1.2.1 目录结构

```
internal/tui/
├── tui_model.go          # model struct, 全局样式, initialModel (~760行)
├── tui_update.go         # Update 消息处理 (~2013行)
├── tui_view.go           # View 渲染 (~594行)
├── tui_render.go         # 渲染辅助函数 (~1099行)
├── tui_helpers.go        # 辅助函数 (submit/submitTask等)
├── tui_dialogs.go        # 确认弹窗渲染
├── tui_history.go        # 历史浏览模式
├── tui_tasks.go          # 任务管理
├── tui_fzf.go            # FZF 文件选择器集成
├── styles.go             # 样式/图标常量和辅助函数
├── types.go              # ToolEntry 等类型定义
├── i18n.go               # 多语言支持
├── completion.go         # 补全系统
├── completion_test.go    # 补全测试
├── anim.go               # 简单动画
├── render.go             # ToolLine 渲染 (RenderHeader, RenderToolLine)
├── components/           # 新组件 (部分已实现)
│   ├── dialog.go         # DialogStack (栈式弹窗)
│   ├── confirm.go        # 确认弹窗
│   ├── help.go           # 帮助弹窗
│   ├── model_select.go   # 模型选择弹窗
│   └── mouse.go          # 鼠标检测器
├── layout/               # 布局引擎 (骨架)
│   └── layout.go         # LayoutEngine
├── diffview/             # Diff 查看器 (骨架)
│   └── diffview.go
└── anim/
    └── anim.go           # 动画管理器 (骨架)
```

#### 1.2.2 核心问题

| 问题 | 描述 | 影响 |
|------|------|------|
| **巨量 model 结构** | `model` 结构包含 ~90 个字段，所有状态平铺在一个 struct 中 | 难以维护，难以扩展 |
| **Update 方法 ~1870 行** | 单文件单 switch 处理所有消息，嵌套大量 if/switch | 难以调试，新增消息类型容易出错 |
| **View 方法简单拼接** | 直接在 `strings.Builder` 上拼接，无布局抽象 | resize 行为脆弱 |
| **无虚拟化列表** | 所有 logEntry 全部渲染到 contentCache，依赖 viewport 的 `SetContent` | 大量消息时性能差 |
| **无消息项接口** | `logEntry` 是所有消息类型的"万能"容器，通过 `eventType` 区分 | 无法多态，switch 蔓延 |
| **样式分散** | 全局变量定义在 `tui_model.go` 和 `styles.go` 两处，无主题概念 | 无主题切换能力 |
| **无真正布局引擎** | 通过 `computeFooterHeight()` 硬编码计算 footer 高度 | 不支持灵活的面板布局 |
| **无状态机** | 通过 `historyMode`、`taskRunning` 等布尔标志隐式管理状态 | 状态组合爆炸 |
| **代码量巨大** | 单文件超过 2000 行 (tui_update.go) | 维护困难 |

### 1.3 核心差距对比

| 维度 | crush TUI | CodeActor 当前 TUI | 差距 |
|------|-----------|-------------------|------|
| **包结构** | 15+ 子包，按职责分离 | 扁平结构，6 个子包 | 高内聚低耦合不足 |
| **Model 设计** | 状态机驱动，组合子组件 | 单一巨型 model | 需要拆解 |
| **列表渲染** | 虚拟化 List + Item 接口 | 全量 contentCache | 需要虚拟化 |
| **消息项** | 接口多态 (Item/Focusable/Highlightable/...) | eventType 字符串 switch | 需要接口抽象 |
| **布局** | uv.ScreenBuffer + generateLayout() | 手动计算 footer/height | 需要布局引擎 |
| **动画** | 可见性感知的帧管理 | 简单 tick + 全量重建 | 需要可见性优化 |
| **Dialog** | 完整 DialogStack | 已有基础 DialogStack | 差距较小 |
| **样式** | 主题化 Styles struct | 全局 var 散落各处 | 需要主题系统 |
| **Update 管道** | 单 switch ~40 case，清晰可读 | 单 switch 混合所有逻辑 | 需要拆分 |

---

## 2. crush TUI 设计精髓

### 2.1 布局引擎

crush 使用 **ultraviolet (uv)** 作为底层渲染引擎，实现像素级精度的终端 UI：

```go
// View() — crush 风格
func (m *UI) View() tea.View {
    canvas := uv.NewScreenBuffer(m.width, m.height)
    cursor := m.Draw(canvas, canvas.Bounds())
    content := canvas.Render()
    // trim trailing whitespace...
    return tea.NewView(content)
}

// Draw() — 按区域绘制
func (m *UI) Draw(scr *uv.ScreenBuffer, area image.Rectangle) *tea.Cursor {
    m.layout = m.generateLayout(area.Dx(), area.Dy())
    scr.Clear()
    
    switch m.state {
    case uiOnboarding:
        m.header.drawHeader(scr, m.layout.header, ...)
        // dialogs handle the rest
    case uiLanding:
        m.header.drawHeader(scr, m.layout.header, ...)
        m.landingView(scr, m.layout.main)
        m.renderEditorView(scr, m.layout.editor)
    case uiChat:
        // sidebar or compact header
        m.chat.Draw(scr, m.layout.main)
        m.renderPillsView(scr, m.layout.pills)
        m.renderEditorView(scr, m.layout.editor)
    }
    
    m.status.Draw(scr, m.layout.status)
    // completions popup
    // dialog overlays (topmost)
    
    return cursor
}
```

**布局计算 (`generateLayout`)** 根据多种因素动态调整：

```
generateLayout(width, height) → uiLayout {
    area, header, main, pills, editor, sidebar, status, sessionDetails
}

非 Compact 模式 (> 120cols):
┌──────────────────────────────────┐
│ Header (logo, creds, hints)      │
├───────────────────┬──────────────┤
│                   │              │
│ Main (Chat)       │ Sidebar      │
│                   │ (30 cols)    │
│                   │              │
├───────────────────┼──────────────┤
│ Pills             │              │
├───────────────────┼──────────────┤
│ Editor            │              │
├───────────────────┴──────────────┤
│ Status / Help                    │
└──────────────────────────────────┘

Compact 模式 (<= 120cols):
┌──────────────────────────────────┐
│ Compact Header                   │
├──────────────────────────────────┤
│                                  │
│ Main (Chat + possibly Pills)     │
│                                  │
├──────────────────────────────────┤
│ Editor                           │
├──────────────────────────────────┤
│ Status / Help                    │
└──────────────────────────────────┘
```

**CodeActor 适配**：我们不需要 uv，可以用 lipgloss 的 `Place` + `JoinVertical` + `JoinHorizontal` 实现等效的布局计算。核心是**先计算各区域尺寸，再分别渲染，最后组装**。

### 2.2 列表虚拟化

crush 的 `list.List` 实现了高效的虚拟化滚动：

```go
type List struct {
    width, height   int         // 视口尺寸
    items           []Item      // 所有项
    gap             int         // 项间距
    reverse         bool        // 是否从底部渲染
    focused         bool
    selectedIdx     int
    offsetIdx       int         // 第一个可见项的索引
    offsetLine      int         // offsetIdx 项的已滚动行数
    renderCallbacks []RenderCallback
}
```

**虚拟化原理：**

```
所有项:  [Item0(3行), Item1(5行), Item2(2行), Item3(4行), Item4(6行)]
视口高度: 8 行

offsetIdx=1, offsetLine=2:
  ┌─────────────────┐
  │ Item1 第3行      │ ← offsetLine=2, 跳过前2行
  │ Item1 第4行      │
  │ Item1 第5行      │
  │ [gap]            │
  │ Item2 第1行      │
  │ Item2 第2行      │
  │ [gap]            │
  │ Item3 第1行      │
  └─────────────────┘
  视口 8 行已满 → 停止
```

**Render 方法流程：**

```go
func (l *List) Render() string {
    // 1. 对每个可见项应用 RenderCallbacks
    // 2. 调用 item.Render(width) 获取字符串
    // 3. 计算项的行数
    // 4. 从 offsetIdx/offsetLine 开始渲染
    // 5. 累加行数直到 >= height
}
```

**CodeActor 适配**：我们需要实现一个简化版的 `List`，核心保留：
- `offsetIdx` / `offsetLine` 虚拟化滚动
- `RenderCallbacks` 链
- `ScrollToBottom` / `ScrollBy` / `VisibleItemIndices`

### 2.3 消息渲染管道

crush 使用**接口多态**而非 eventType switch：

```go
// 核心接口 — list/item.go
type Item interface {
    Render(width int) string
}

type RawRenderable interface {
    RawRender(width int) string
}

type Focusable interface {
    SetFocused(focused bool)
}

type Highlightable interface {
    SetHighlight(startLine, startCol, endLine, endCol int)
    Highlight() (start, end position)
}

type MouseClickable interface {
    HandleMouseClick(btn int, x, y int) bool
}

// Chat 特有接口 — chat/messages.go
type MessageItem interface {
    Item
    ID() string
}

type Expandable interface {
    ToggleExpanded() bool
    IsExpanded() bool
}

type Animatable interface {
    Animate(msg anim.StepMsg) tea.Cmd
    StartAnimation() tea.Cmd
}

type KeyEventHandler interface {
    HandleKeyMsg(key tea.KeyMsg) (bool, tea.Cmd)
}

type NestedToolContainer interface {
    NestedToolIDs() []string
}
```

**消息类型多态实现：**

```go
// chat/assistant.go
type AssistantMessage struct {
    id      string
    content string
    focused bool
    // ...
}

func (a *AssistantMessage) Render(width int) string {
    // 使用 Glamour 渲染 Markdown
    return renderer.Render(a.content)
}

func (a *AssistantMessage) ID() string { return a.id }
func (a *AssistantMessage) SetFocused(f bool) { a.focused = f }

// chat/tools.go
type ToolCallMessage struct {
    id     string
    tool   ToolInfo
    result *ToolResult
    status ToolStatus
    // ...
}

func (t *ToolCallMessage) Render(width int) string {
    // 渲染工具调用行 + 结果
}

func (t *ToolCallMessage) Animate(msg anim.StepMsg) tea.Cmd {
    // 动画帧更新
}

// 所有消息类型都存储为 Item 接口
chat.list.SetItems(items...)
```

**CodeActor 适配**：将当前的 `logEntry` (单一结构 + eventType switch) 重构为接口层次：

```
Item (interface)
├── MessageItem (interface)
│   ├── UserMessage
│   ├── AssistantMessage
│   ├── ToolCallMessage       (实现 Animatable)
│   ├── ToolResultMessage
│   ├── LLMCallMessage        (实现 Animatable)
│   ├── CompactMessage        (上下文压缩)
│   ├── CommitContextMessage
│   ├── StatusMessage
│   └── ErrorMessage
└── SpacerItem
```

### 2.4 状态机驱动的视图切换

crush 使用枚举状态而非布尔标志：

```go
type uiState int
const (
    uiOnboarding uiState = iota  // 首次启动，无配置
    uiInitialize                  // 有配置，项目未初始化
    uiLanding                     // 就绪但无活跃 session
    uiChat                        // 活跃 session 中
)

type uiFocusState int
const (
    uiFocusNone   uiFocusState = iota
    uiFocusEditor               // 焦点在输入框
    uiFocusMain                 // 焦点在聊天列表
)
```

**状态转换：**
```
Onboarding ──(配置完成)──→ Initialize ──(项目初始化)──→ Landing
                ↑                                         │
                └────────────(需要重新配置)────────────────┘
                                                          │
                                                    (开始对话)
                                                          │
                                                          ↓
                                                        Chat
                                                          │
                                                    (任务完成)
                                                          │
                                                          ↓
                                                       Landing
```

**CodeActor 适配**：定义清晰的状态枚举，替换当前的：
- `m.historyMode` → `uiHistory` state
- `m.taskRunning` → 并入 state 管理
- `m.commandMode` → `uiFocusCommand` focusState
- `m.confirmDialog.open` → DialogStack 管理

### 2.5 动画系统

crush 的动画系统核心特征：

**可见性感知：** 只有在视口内可见的 Item 才执行动画帧更新

```go
func (c *Chat) Animate(msg anim.StepMsg) tea.Cmd {
    visible := c.list.VisibleItemIndices()
    visibleSet := make(map[int]bool)
    for _, idx := range visible {
        visibleSet[idx] = true
    }
    
    var cmds []tea.Cmd
    for i, item := range c.list.Items() {
        if animatable, ok := item.(Animatable); ok {
            if visibleSet[i] {
                cmd := animatable.Animate(msg)
                cmds = append(cmds, cmd)
            } else {
                // Pause — track for resume when scrolled into view
                c.pausedAnimations[item.(MessageItem).ID()] = struct{}{}
            }
        }
    }
    return tea.Batch(cmds...)
}
```

**CodeActor 当前问题**：每次 tick 都重建整个 viewport 内容，非可见区域的动画也在渲染

**适配方案**：保留现有的 `anim.Manager` (骨架已存在)，添加：
- `VisibleItemIndices()` 方法
- 可见性检查
- 暂停/恢复机制

### 2.6 Dialog 栈式弹窗系统

crush 的 Dialog 系统设计精巧：

```go
// dialog/dialog.go
type Overlay struct {
    stack []Dialog  // LIFO 栈
}

type Dialog interface {
    tea.Model  // Init/Update/View
    Bounds() (x, y, w, h int)
    SetBounds(x, y, w, h int)
}

// 使用示例
ui.dialog.Push(models.NewModelSelectDialog(...))
ui.dialog.Push(permissions.NewConfirmDialog(...))
ui.dialog.Overlay(width, height)  // 渲染整个栈
```

**CodeActor 当前状态**：`components.DialogStack` 已基本实现了 crush 的功能，差距较小

### 2.7 Pub-Sub 事件驱动

crush 使用泛型 Pub-Sub：

```go
// pubsub.Event[T] 泛型事件
type Event[T any] struct {
    Payload T
}

// 在 Update 中处理
case pubsub.Event[session.Session]:
    m.session = msg.Payload
case pubsub.Event[message.Message]:
    m.appendMessage(msg.Payload)
case pubsub.Event[app.LSPEvent]:
    m.lspStates = app.GetLSPStates()
```

**CodeActor 当前状态**：通过 `taskEventMsg` 包装 `messaging.MessageEvent`，在 `tuiEventConsumer` 的 Go channel 中传递。**核心差距**是 crush 的泛型事件能够携带任意类型的 Payload，而我们的是 `interface{}` 类型的 Content。

### 2.8 样式系统

crush 的样式系统：

```go
// styles/styles.go
type Styles struct {
    // 基础样式
    Body, Dimmed, Accent, Error, Warn, Success, Info lipgloss.Style
    
    // Markdown 样式
    Markdown, QuietMarkdown glamour.Style
    
    // 特定组件样式
    Sidebar, Dialog, Tool...
}

// styles/themes.go
func NewStyles(theme Theme) *Styles { ... }
func (s *Styles) ApplyForegroundGrad(text, from, to string) string { ... }
```

**CodeActor 当前状态**：所有样式都是全局 `var` 定义在 `tui_model.go` 中。需要集中到 `Styles` struct 中，支持主题。

---

## 3. 目标架构设计

### 3.1 包结构重组

```
internal/tui/                    # 当前 (重构后)
├── model.go                     # UI struct, 顶层 Model (~500行)
├── root_update.go               # 顶层 Update 消息分发 (~300行)
├── root_view.go                 # View/Draw 管道 (~200行)
├── layout.go                    # 布局计算引擎 (~200行)
├── state.go                     # 状态枚举 + 状态机 (~100行)
├── keys.go                      # 快捷键定义 (~100行)
├── init.go                      # initialModel 工厂函数 (~200行)
│
├── chat/                        # [新] 消息列表
│   ├── chat.go                  # Chat struct (封装 List)
│   ├── items.go                 # 核心接口 (Item, MessageItem, ...)
│   ├── items_user.go            # 用户消息
│   ├── items_assistant.go       # AI 回复
│   ├── items_tool.go            # 工具调用/结果
│   ├── items_llm.go             # LLM 调用
│   ├── items_system.go          # 系统消息 (压缩, commit context, ...)
│   └── items_diff.go            # Diff 条目
│
├── list/                        # [新] 虚拟化列表
│   ├── list.go                  # List 核心 + 虚拟化渲染
│   ├── item.go                  # Item 接口族
│   ├── scroll.go                # 滚动逻辑
│   └── highlight.go             # 文本选择高亮
│
├── common/                      # [重写] 通用 UI 元素
│   ├── common.go                # Common 上下文 (主题, workspace)
│   ├── styles.go                # Styles 主题系统
│   ├── elements.go              # Section/Status/DialogTitle
│   ├── markdown.go              # Glamour 渲染封装
│   └── scrollbar.go             # 滚动条
│
├── dialog/                      # [增强] 弹窗系统
│   ├── stack.go                 # DialogStack (LIFO)
│   ├── base.go                  # Dialog 接口
│   ├── confirm.go               # 确认弹窗
│   ├── help.go                  # 帮助弹窗
│   ├── model_select.go          # 模型选择弹窗
│   ├── quit.go                  # 退出确认弹窗
│   ├── task_complete.go         # 任务完成弹窗
│   └── history.go               # 历史浏览弹窗
│
├── sidebar/                     # [新] 侧边栏
│   ├── sidebar.go               # 侧边栏主视图
│   ├── files.go                 # 文件变更列表
│   ├── skills.go                # Skills 状态
│   └── tokens.go                # Token 使用统计
│
├── completions/                 # [复用] 补全系统
│   ├── completions.go
│   └── item.go
│
├── diffview/                    # [增强] Diff 查看器
│   ├── diffview.go
│   └── render.go
│
├── anim/                        # [增强] 动画系统
│   └── anim.go
│
├── input/                       # [新] 输入处理
│   ├── textarea.go              # Textarea 配置 + 样式
│   ├── autocomplete.go          # Skill/Keyword 补全
│   └── command.go               # 命令模式处理
│
├── render.go                    # Tool 渲染辅助 (从当前保留)
├── types.go                     # 基础类型 (从当前保留)
├── i18n.go                      # 多语言 (从当前保留)
├── tui_fzf.go                   # FZF 集成 (从当前保留)
├── tui_helpers.go               # 辅助函数 (从当前保留)
│
├── tui_history.go               # 历史模式 (重构到 dialog/)
├── tui_dialogs.go               # Dialog 渲染 (重构到 dialog/)
├── tui_tasks.go                 # 任务管理 (保留)
│
└── spec/                        # 测试
    ├── chat_test.go
    ├── list_test.go
    └── model_test.go
```

### 3.2 核心类型设计

#### 3.2.1 UI — 顶层 Model

```go
// model.go
type UI struct {
    // 共享上下文
    com *common.Common

    // 终端尺寸
    width  int
    height int

    // 状态机
    state      UIState
    focusState UIFocusState

    // 布局缓存
    layout UILayout

    // 子组件
    header      *Header
    chat        *chat.Chat
    sidebar     *sidebar.Sidebar
    status      *Status
    textarea    textarea.Model
    dialogStack *dialog.Stack
    completions *completions.Completions

    // 动画
    animManager  *anim.Manager
    tickStarted  bool
    activeAnim   bool

    // Session
    session      *Session

    // 输入处理
    inputHandler *input.Handler

    // 快捷键
    keyMap KeyMap

    // 退出标志
    quitting bool
}

// 最小化！所有非 UI 逻辑委托给子组件或后台服务
```

#### 3.2.2 UIState — 状态枚举

```go
// state.go
type UIState int

const (
    UIStateInit        UIState = iota // 初始状态 (加载配置)
    UIStateReady                      // 就绪 (可输入)
    UIStateRunning                    // 任务运行中
    UIStateHistory                    // 历史浏览
    UIStateDialog                     // 弹窗中 (由 DialogStack 管理)
)

type UIFocusState int

const (
    UIFocusEditor UIFocusState = iota
    UIFocusMain
    UIFocusCommand
    UIFocusDialog // 弹窗获取焦点时
)
```

#### 3.2.3 UILayout — 布局矩形

```go
// layout.go
type UILayout struct {
    Full   Rect // 终端全区域
    Header Rect
    Main   Rect // 主内容区 (Chat)
    Sidebar Rect
    Editor Rect
    Status Rect
    Pills  Rect // 技能/关键词建议
}

type Rect struct {
    X, Y, W, H int
}

func (m *UI) computeLayout() UILayout {
    // 根据 UIState + width/height 动态计算所有区域
    // Compact 模式: 宽度 <= 100
    // 编辑器高度: min(15, max(3, textarea.Height()))
    // Sidebar: 30 cols (非 compact)
}
```

### 3.3 状态机设计

```
              ┌──────────┐
    启动 ────→│   Init   │
              └────┬─────┘
                   │ (初始化完成)
                   ↓
              ┌──────────┐
    输入任务 ──→│  Ready   │←──────────┐
              └────┬─────┘            │
                   │ (Ctrl+S)          │ (任务完成)
                   ↓                   │
              ┌──────────┐     ┌───────┴──────┐
              │ Running  │────→│  TaskComplete │ (弹窗)
              └────┬─────┘     └──────────────┘
                   │ (Esc → dialog)
                   ↓
              ┌──────────┐
              │  Dialog  │ (确认/选择)
              └────┬─────┘
                   │ (关闭)
                   ↓
              ┌──────────┐
              │  History │ (:history 命令)
              └──────────┘
```

**状态守卫：**

```go
func (m *UI) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch m.state {
    case UIStateInit:
        return m.updateInit(msg)
    case UIStateReady:
        return m.updateReady(msg)
    case UIStateRunning:
        return m.updateRunning(msg)
    case UIStateHistory:
        return m.updateHistory(msg)
    case UIStateDialog:
        // DialogStack 管理
        return m.updateDialog(msg)
    }
}
```

### 3.4 布局引擎设计

```go
// layout.go

func (m *UI) computeLayout() UILayout {
    w, h := m.width, m.height
    compact := w <= 100
    
    l := UILayout{Full: Rect{0, 0, w, h}}
    
    // Status bar — 1 row at bottom
    l.Status = Rect{0, h - 1, w, 1}
    remainingH := h - 1
    
    // Editor area — dynamic height based on textarea
    editorH := min(15, max(3, m.textarea.Height()))
    if !m.hasSuggestions() {
        editorH = min(8, editorH)
    }
    
    // Suggestion area
    sugH := m.suggestionHeight()
    
    // Editor section
    editorSectionH := editorH + sugH + 2  // +2 for borders/separators
    l.Editor = Rect{0, remainingH - editorSectionH, compact ? w : w - 30, editorSectionH}
    
    // Main area
    mainH := remainingH - editorSectionH - 1 // -1 for separator
    if compact {
        l.Header = Rect{0, 0, w, 1}  // compact header
        l.Main = Rect{0, 1, w, mainH}
        l.Sidebar = Rect{}  // no sidebar
    } else {
        l.Header = Rect{0, 0, w, 3}  // full header
        l.Sidebar = Rect{w - 30, 0, 30, h - 1}
        l.Main = Rect{0, 3, w - 30, mainH}
    }
    
    return l
}
```

**View 管道：**

```go
// root_view.go

func (m *UI) View() tea.View {
    if m.quitting { return tea.NewView("") }
    if m.width <= 0 || m.height <= 0 { return tea.NewView("") }
    
    // Dialog overlay — fullscreen, top layer
    if m.dialogStack.Len() > 0 {
        overlay := m.dialogStack.Overlay(m.width, m.height)
        if overlay != "" {
            return tea.NewView(overlay)
        }
    }
    
    var b strings.Builder
    b.Grow(m.width * m.height * 4) // preallocate
    
    layout := m.computeLayout()
    
    // Draw pipeline — each component gets its rect
    b.WriteString(m.renderHeader(layout.Header))
    b.WriteString(m.renderMainContent(layout.Main))
    if !m.isCompact() {
        b.WriteString(m.renderSidebar(layout.Sidebar))
    }
    b.WriteString(m.renderEditor(layout.Editor))
    b.WriteString(m.renderStatus(layout.Status))
    
    return tea.NewView(b.String())
}
```

### 3.5 列表组件设计

```go
// list/list.go

// Item is the core interface for list items.
type Item interface {
    Render(width int) string
    Height(width int) int  // 预计算行数
}

// List is a virtualized scrollable list.
type List struct {
    items       []Item
    width       int
    height      int
    gap         int
    
    // Scroll state
    offsetIdx   int  // index of first partially-visible item
    offsetLine  int  // lines scrolled past the start of offsetIdx
    
    // Focus
    focused     bool
    selectedIdx int
    
    // Render callbacks — applied before each item's Render
    renderCallbacks []RenderCallback
}

type RenderCallback func(item Item, idx int) Item
```

**虚拟化渲染：**

```go
func (l *List) Render() string {
    if len(l.items) == 0 {
        return ""
    }
    
    var b strings.Builder
    b.Grow(l.height * l.width)
    
    linesRendered := 0
    for i := l.offsetIdx; i < len(l.items) && linesRendered < l.height; i++ {
        item := l.items[i]
        
        // Apply callbacks
        for _, cb := range l.renderCallbacks {
            item = cb(item, i)
        }
        
        content := item.Render(l.width)
        itemLines := strings.Split(content, "\n")
        
        // Skip offsetLine for the first visible item
        startLine := 0
        if i == l.offsetIdx {
            startLine = l.offsetLine
        }
        
        for j := startLine; j < len(itemLines) && linesRendered < l.height; j++ {
            b.WriteString(itemLines[j])
            b.WriteByte('\n')
            linesRendered++
        }
        
        // Gap
        if i < len(l.items)-1 && linesRendered < l.height && l.gap > 0 {
            for g := 0; g < l.gap && linesRendered < l.height; g++ {
                b.WriteByte('\n')
                linesRendered++
            }
        }
    }
    
    return b.String()
}
```

**滚动方法：**

```go
func (l *List) ScrollBy(lines int) {
    // 正向: 向下滚动
    // 负向: 向上滚动
    if lines > 0 {
        for lines > 0 && l.offsetIdx < len(l.items) {
            itemH := l.items[l.offsetIdx].Height(l.width)
            remaining := itemH - l.offsetLine
            if lines < remaining {
                l.offsetLine += lines
                break
            }
            lines -= remaining
            l.offsetIdx++
            l.offsetLine = 0
        }
    } else {
        lines = -lines
        for lines > 0 && (l.offsetIdx > 0 || l.offsetLine > 0) {
            if l.offsetLine > 0 {
                if lines <= l.offsetLine {
                    l.offsetLine -= lines
                    break
                }
                lines -= l.offsetLine
                l.offsetLine = 0
            } else {
                l.offsetIdx--
                l.offsetLine = l.items[l.offsetIdx].Height(l.width)
                if l.offsetLine > 0 {
                    l.offsetLine-- // leave last line visible
                }
                lines--
            }
        }
    }
}

func (l *List) ScrollToBottom() {
    totalH := l.totalHeight()
    visibleH := l.height
    if totalH <= visibleH {
        l.offsetIdx = 0
        l.offsetLine = 0
        return
    }
    // Walk backward to find last visible position
    remaining := visibleH
    l.offsetIdx = len(l.items) - 1
    for l.offsetIdx >= 0 && remaining > 0 {
        itemH := l.items[l.offsetIdx].Height(l.width) + l.gap
        if itemH >= remaining {
            l.offsetLine = itemH - remaining
            break
        }
        remaining -= itemH
        l.offsetIdx--
    }
    if l.offsetIdx < 0 {
        l.offsetIdx = 0
        l.offsetLine = 0
    }
}

func (l *List) VisibleItemIndices() []int {
    var result []int
    lines := 0
    for i := l.offsetIdx; i < len(l.items) && lines < l.height; i++ {
        itemH := l.items[i].Height(l.width)
        if i == l.offsetIdx {
            itemH -= l.offsetLine
        }
        result = append(result, i)
        lines += itemH + l.gap
    }
    return result
}
```

### 3.6 消息项接口

```go
// chat/items.go

// MessageItem 是所有 Chat 消息的基本接口
type MessageItem interface {
    list.Item
    ID() string
    Type() MessageType
}

// MessageType enum
type MessageType int
const (
    MsgUser       MessageType = iota
    MsgAssistant
    MsgToolCall
    MsgToolResult
    MsgLLMCall
    MsgSystem
    MsgError
)

// Animatable — 支持动画的消息项
type Animatable interface {
    Animate(step int) string  // 返回动画帧内容
    IsAnimating() bool
    StartAnimation()
    StopAnimation()
}

// Expandable — 可展开/折叠
type Expandable interface {
    ToggleExpand() bool
    IsExpanded() bool
}

// Selectable — 可被选中 (用于复制、展开等)
type Selectable interface {
    SetSelected(bool)
    IsSelected() bool
}

// Focusable — 可获取焦点
type Focusable interface {
    SetFocused(bool)
}

// DiffRenderable — 包含 diff 内容
type DiffRenderable interface {
    DiffContent() string
}
```

**具体消息项：**

```go
// chat/items_user.go
type UserMessage struct {
    id      string
    content string
    // 无动画、无展开、无 diff
}

func (m *UserMessage) Render(width int) string {
    return renderUserMessageBox(m.content, width)
}

// chat/items_tool.go
type ToolCallMessage struct {
    id     string
    tool   ToolCallInfo
    status ToolStatus
    result *ToolResultInfo
    anim   *AnimationState  // 运行中的动画
}

func (m *ToolCallMessage) Render(width int) string {
    if m.status == ToolStatusRunning {
        return RenderPending(m.tool.Name, m.tool.Summary, m.anim)
    }
    return RenderToolLine(&ToolEntry{
        Call:   m.tool,
        Result: m.result,
        Status: m.status,
    }, nil, width)
}

func (m *ToolCallMessage) Animate(step int) string {
    // 返回旋转指示器的下一帧
    return m.anim.Render()
}
```

### 3.7 Render/Draw 管道

完整的 View 管道流程：

```
View()
  │
  ├─ DialogStack 覆盖? ──→ 返回弹窗覆盖层
  │
  ├─ History 模式? ──→ renderHistory()
  │
  ├─ computeLayout(width, height) → UILayout
  │
  ├─ renderHeader(layout.Header)
  │   ├─ Compact: 一行 (模型 + 状态)
  │   └─ Full: Logo + 3行信息
  │
  ├─ renderMainContent(layout.Main)
  │   └─ chat.Chat.Draw(rect)
  │       └─ list.List.Render()
  │           ├─ 对每个可见项:
  │           │   1. 应用 RenderCallbacks (焦点、选中等)
  │           │   2. 调用 item.Render(width)
  │           │   3. 跳过 offsetLine
  │           └─ 从 offsetIdx 开始，直到填满视口
  │
  ├─ renderSidebar(layout.Sidebar) [非 compact]
  │   ├─ Logo
  │   ├─ Model info
  │   ├─ Modified files
  │   ├─ Skills status
  │   └─ Token stats
  │
  ├─ renderEditor(layout.Editor)
  │   ├─ Textarea view
  │   ├─ Autocomplete suggestions [if active]
  │   └─ Error message [if any]
  │
  └─ renderStatus(layout.Status)
      └─ Airline-style status bar
```

### 3.8 Update 管道

```go
// root_update.go

func (m *UI) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    // 1. Global handlers (independent of state)
    switch msg := msg.(type) {
    case tea.QuitMsg:
        return m, tea.Quit
    
    case tea.WindowSizeMsg:
        m.width = msg.Width
        m.height = msg.Height
        return m, m.startTick()
    
    case tea.KeyMsg:
        if m.isGlobalKey(msg) {
            return m, m.handleGlobalKey(msg)
        }
    }
    
    // 2. Dialog has priority
    if m.dialogStack.Len() > 0 {
        return m.updateDialog(msg)
    }
    
    // 3. State-specific handlers
    switch m.state {
    case UIStateInit:
        return m.updateInit(msg)
    case UIStateReady:
        return m.updateReady(msg)
    case UIStateRunning:
        return m.updateRunning(msg)
    case UIStateHistory:
        return m.updateHistory(msg)
    }
    
    return m, nil
}

// updateRunning — 任务运行时的消息处理
func (m *UI) updateRunning(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case tickMsg:
        // 动画 tick
        m.animManager.Tick(100)
        // 只更新可见的运行中项
        m.chat.Animate(m.animManager.Step())
        return m, tickCmd()
    
    case taskEventMsg:
        // 处理后台事件
        return m.handleTaskEvent(msg.event)
    
    case tea.KeyMsg:
        // 运行时键盘处理 (限制操作)
        return m.handleRunningKeyMsg(msg)
    
    case tea.MouseMsg:
        // 鼠标滚轮 → 滚动聊天列表
        return m.handleMouseMsg(msg)
    }
    
    return m, nil
}
```

---

## 4. 分阶段实现计划

### 4.1 Phase 1: 基础架构重构 ✅

**目标**：建立新的包结构骨架，不破坏现有功能

#### 任务清单

- [x] 1. **创建目录结构** ✅
  ```
  mkdir -p internal/tui/{chat,list,sidebar,input,dialog}
  ```
- [x] 2. **实现 `common.Common` 共享上下文** ✅
  - 文件：`internal/tui/common/common.go`
- [x] 3. **实现 `common.Styles` 主题系统** ✅
  - 文件：`internal/tui/common/styles.go`（完整迁移所有全局样式到 Styles struct）
- [x] 4. **实现状态枚举和状态机** ✅
  - 文件：`internal/tui/state.go`
  - `UIState` + `UIFocusState` + `CanTransition()`
- [x] 5. **model struct 集成新组件** ✅
  - 文件：`internal/tui/tui_model.go`（保留 model 名，添加 `com`, `state`, `focusState`, `keyMap`, `chatList`, `sidebarPanel`, `inputHandler`）
- [x] 6. **`initialModel()` 创建所有新组件** ✅

**验收状态**：`go build ./...` 通过

### 4.2 Phase 2: 列表虚拟化 + 消息项 ✅

**目标**：实现通用 `List` 和消息项接口，替换当前 `logEntry` + `contentParts` 的全量渲染

#### 任务清单

- [x] 1. **实现 `list.Item` 接口族** ✅
  - 文件：`internal/tui/list/item.go`
  - `Item`, `Focusable`, `Selectable`, `Highlightable`, `MouseClickable`
- [x] 2. **实现 `list.List` 核心** ✅
  - 文件：`internal/tui/list/list.go`
  - 虚拟化 `Render()`, `ScrollBy`, `ScrollToBottom`, `VisibleItemIndices`, `RenderCallback`
  - 通过 11 项测试
- [x] 3. **实现 `chat.MessageItem` 接口** ✅
  - 文件：`internal/tui/chat/items.go`
  - `MessageItem`, `Animatable`, `Expandable`, `KeyEventHandler`
- [x] 4. **实现具体消息项** ✅
  - `UserMessage` → `items_user.go`
  - `AssistantMessage` → `items_assistant.go`（含 Glamour Markdown）
  - `ToolCallMessage` → `items_tool.go`（含动画 + 展开 + 结果摘要）
  - `LLMCallMessage` + `SystemMessage` → `items_llm.go`
- [x] 5. **实现 `chat.Chat`** ✅
  - 文件：`internal/tui/chat/chat.go`
  - 封装 `*list.List`, ID 索引, 可见性感知动画
- [x] 6. **在 Model 中集成 Chat** 🔄
  - `m.chatList` 字段已添加，与旧 `m.logEntries` 并存
  - 旧渲染路径保留，逐步迁移

**验收状态**：`go test ./internal/tui/list/...` 通过，`go build ./...` 通过

### 4.3 Phase 3: 布局引擎 ✅

**目标**：实现基于矩形的布局计算，替换当前的手动高度计算

#### 任务清单

- [x] 1. **实现布局计算** ✅
  - 文件：`internal/tui/layout.go`
  - `UILayout` struct（Full/Header/Main/Sidebar/Editor/Status Rect）
  - `computeLayout()` — Compact 模式自动切换 (width <= 100)
- [x] 2. **创建 `layoutView()` 新渲染管道** ✅
  - 文件：`internal/tui/root_view.go`
  - `renderHeaderPane`, `renderMainPane`, `renderEditorPane`, `renderStatusPane`
  - 与旧 `View()` 并存
- [x] 3. **实现 Header 组件** ✅
  - 文件：`internal/tui/common/elements.go`
  - `Header`, `StatusBar`, `DialogTitle`, `Button`, `ConfirmationPrompt`
- [x] 4. **Status Bar 组件** ✅ (partial: `common.StatusBar()` 已可用于新代码，旧 `renderAirlineStatusBar` 保留供旧 View 路径使用)
- [x] 5. **实现 Sidebar 组件** ✅
  - 文件：`internal/tui/sidebar/sidebar.go`
  - Token 统计、Skills 状态、Model info

**验收状态**：`go build ./...` 通过，新布局管道可用但未替换旧 View

### 4.4 Phase 4: 动画系统 ✅

**目标**：完善动画系统，支持可见性感知

#### 任务清单

- [x] 1. **`anim.Manager` 增强** ✅
  - 文件：`internal/tui/anim/manager.go`
  - Register/Unregister, FPS 控制, ProcessTickMsg, Step()
  - 可见性设置 (SetVisible)
- [x] 2. **Chat 可见性感知动画** ✅
  - `Chat.Animate(step)` — 只更新 VisibleItemIndices 中的 Animatable 项
  - 暂停/恢复：`pausedAnimations` 跟踪
- [x] 3. **UI.Update 新管道调用动画** ✅
  - `root_update.go:updateRunning()` 中有 tick 处理骨架
  - 旧 Update 仍用原始 tick 路径

**验收状态**：`Chat.Animate()` 可见性感知完成，通过构建

### 4.5 Phase 5: 状态机 + 视图 ✅

**目标**：完善状态机，实现按状态分发的 Update 管道

#### 任务清单

- [x] 1. **拆分 `Update()` 方法** ✅
  - 文件：`internal/tui/root_update.go`
  - `updateByState()` → `updateInit()`, `updateReady()`, `updateRunning()`
  - 与旧 `Update()` 并存
- [x] 2. **`updateReady()` + `updateRunning()`** ✅
  - 输入处理, 快捷键, Task event 处理, 动画 tick
- [x] 3. **快捷键集中化** ✅
  - 文件：`internal/tui/keys.go`
  - `KeyMap` struct + `DefaultKeyMap()` + 30+ 常量
- [x] 4. **命令解析提取** ✅
  - 文件：`internal/tui/input/command.go`
  - `ParseCommand()` 纯函数 → `ParsedCommand{Action, Args1, Args2}`
  - `CmdQuitForce/CmdQuitDialog/CmdHelp/CmdModel*/CmdSearch/CmdHistory`
- [x] 5. **补全逻辑提取** ✅
  - 文件：`internal/tui/input/autocomplete.go`
  - `AutoCompleteState` + `DoSkillAutocomplete()` + `DoKeywordAutocomplete()`
- [x] 6. **model 新增字段** ✅
  - `focusState UIFocusState`, `keyMap KeyMap`, `state UIState`

**验收状态**：`go build ./...` 通过，新旧 Update 管道并存

### 4.6 Phase 6: Dialog 系统完善 ✅

**目标**：完成弹窗系统，替换旧的 confirm dialog 实现

#### 任务清单

- [x] 1. **创建 `dialog/` 包** ✅
  - 文件：`internal/tui/dialog/base.go`
  - `Dialog`/`Stack` 别名（wraps `components.*`），`HistoryDialog` 占位符
- [x] 2. **`components/` 底层已完备** ✅
  - DialogStack (LIFO), ConfirmDialog, HelpDialog, ModelSelectDialog, QuitConfirmDialog, AgentSelectDialog, TaskCompleteDialog — 全部已有
  - `Component` 接口已有 `Bounds()` / `SetBounds()` / `IsVisible()` / `SetVisible()`
- [x] 3. **History 模式 → DialogStack** ✅
  - 删除确认 (delete confirmation) 已迁移到 DialogStack (通过 `QuitConfirmDialog`)
  - 新增 `NewQuitConfirmDialogForDelete()` 工厂函数
  - `DialogStack` 处理优先级高于 history 模式
- [x] 4. **移除旧 boolean dialog 字段** ✅
  - 已移除 `confirmDialog`, `confirmQuitDialog`, `confirmCancelDialog`, `taskCompleteDialog`, `confirmDeleteDialog` struct 类型
  - 已移除 model 中的对应字段和 `showHelpDialog` 布尔值
  - 已移除旧 dialog 渲染函数（`renderConfirmDialog` 等）
  - 所有 dialog 操作均通过 `DialogStack` 完成

**验收状态**：所有 dialog 操作通过 DialogStack，旧 boolean 字段全部移除

### 4.7 Phase 7: 主题/样式系统 ✅

**目标**：集中样式管理，支持暗色/亮色主题

#### 任务清单

- [x] 1. **`Styles` struct 完整定义** ✅
  - 文件：`internal/tui/common/styles.go`（60+ 样式字段，7 个分类）
- [x] 2. **主题切换** ✅
  - `ThemeDark` + `ThemeLight`，`NewStyles(theme)` 支持主题参数
- [x] 3. **清理全局 var** ✅
  - 已移除未使用的全局样式变量（`promptFocusedStyle`, `promptBlurredStyle`, `welcomeTitleStyle`, `welcomeRightTitle`, `infoMsgStyle`, `footerStyle`, `inputPanelBlurredStyle`, `diffNoNewlineStyle`, `commandModeBarStyle`）
  - 已移除 dialog 专用样式变量（随旧 dialog 渲染函数一并移除）
  - 保留的全局 var 已添加 `Deprecated: prefer m.com.Styles` 注释
  - `common.Styles` 为 canonical 样式源，新旧样式并存
  - 新代码通过 `m.com.Styles.XXX` 访问（`root_view.go`, `chat/items_*.go`）
  - 旧代码迁移需逐个文件推进

**验收状态**：`Styles` struct 是 canonical 样式源，新旧样式并存

### 4.8 Phase 8: 测试 + 清理 ✅

**目标**：添加测试，清理冗余代码

#### 任务清单

- [x] 1. **List 组件测试** ✅
  - `list/list_test.go`: 11 tests — NewList, Render (empty/virtualization), ScrollBy, ScrollToBottom, ScrollToTop, VisibleItemIndices, AtBottom, AppendItems, ItemIndexAtPosition, Follow
- [x] 2. **Chat 组件测试** ✅
  - `chat/chat_test.go`: 20 tests — NewChat, SetMessages, AppendMessage, AppendMessages, FindByID, FindIndexByID, ScrollToBottom, ScrollToTop, ScrollBy, PageDown, PageUp, Focus, SelectPrevNext, Render, UpdateMessage, FocusableRenderCallback, SetSizeAutoFollow, Animate, SetFollow
- [x] 3. **布局引擎测试** ✅
  - `layout_test.go`: 6 tests
- [x] 4. **状态机测试** ✅
  - `state_test.go`: 4 tests
- [x] 5. **清理死代码** ✅
  - 已移除旧 dialog struct 类型（`confirmDialog`, `taskCompleteDialog`, `confirmQuitDialog`, `confirmCancelDialog`, `confirmDeleteDialog`）
  - 已移除旧 dialog 渲染函数（~520 行）
  - 已移除旧 dialog 专用样式变量（~13 个 var block）
  - 已移除未使用的全局样式变量（9 个）
  - 已移除 `showHelpDialog` 布尔字段
  - 旧渲染路径保留（与旧 Update/View 并存）
- [x] 6. **更新 CLAUDE.md** ✅

**验收状态**：31 项测试通过，`go build ./...` + `go vet ./internal/tui/...` 无错误

---

## 5. 附录

### A. 关键文件对照表

| crush 文件 | CodeActor 目标文件 | 说明 |
|-----------|-------------------|------|
| `model/ui.go` | `model.go` + `root_update.go` + `root_view.go` | 拆分巨型文件 |
| `model/chat.go` | `chat/chat.go` | Chat 列表 |
| `model/header.go` | `common/elements.go` | Header 渲染 |
| `model/sidebar.go` | `sidebar/sidebar.go` | 侧边栏 |
| `model/status.go` | `common/elements.go` | 状态栏 |
| `model/keys.go` | `keys.go` | 快捷键 |
| `model/session.go` | `tui_tasks.go` | Session/任务管理 |
| `model/landing.go` | `root_view.go` (welcome panel) | Landing 页面 |
| `chat/messages.go` | `chat/items.go` | 消息接口 |
| `chat/assistant.go` | `chat/items_assistant.go` | AI 回复 |
| `chat/user.go` | `chat/items_user.go` | 用户消息 |
| `chat/tools.go` | `chat/items_tool.go` | 工具调用 |
| `list/list.go` | `list/list.go` | 虚拟化列表 |
| `list/item.go` | `list/item.go` | Item 接口 |
| `common/common.go` | `common/common.go` | 共享上下文 |
| `common/elements.go` | `common/elements.go` | UI 元素 |
| `common/markdown.go` | `common/markdown.go` | Markdown |
| `styles/styles.go` | `common/styles.go` | 样式系统 |
| `dialog/dialog.go` | `dialog/stack.go` | 弹窗栈 |
| `anim/anim.go` | `anim/anim.go` | 动画 |
| `completions/completions.go` | `completions/completions.go` | 补全 |

### B. crush UI 完整文件清单

```
internal/ui/
├── anim/anim.go
├── attachments/attachments.go
├── chat/
│   ├── agent.go
│   ├── assistant.go
│   ├── bash.go
│   ├── diagnostics.go
│   ├── docker_mcp.go
│   ├── fetch.go
│   ├── file.go
│   ├── generic.go
│   ├── lsp_restart.go
│   ├── mcp.go
│   ├── messages.go
│   ├── references.go
│   ├── search.go
│   ├── todos.go
│   ├── tool_result_content.go
│   ├── tools.go
│   └── unified_diff.go
├── common/
│   ├── button.go
│   ├── capabilities.go
│   ├── common.go
│   ├── diff.go
│   ├── elements.go
│   ├── highlight.go
│   ├── interface.go
│   ├── markdown.go
│   └── scrollbar.go
├── completions/
│   ├── completions.go
│   ├── item.go
│   └── keys.go
├── dialog/
│   ├── actions.go
│   ├── api_key_input.go
│   ├── arguments.go
│   ├── commands.go
│   ├── commands_item.go
│   ├── common.go
│   ├── dialog.go
│   ├── filepicker.go
│   ├── models.go
│   ├── models_item.go
│   ├── models_list.go
│   ├── oauth.go
│   ├── oauth_copilot.go
│   ├── oauth_hyper.go
│   ├── permissions.go
│   ├── quit.go
│   ├── reasoning.go
│   └── sessions.go
├── diffview/
│   ├── diffview.go
│   ├── split.go
│   ├── style.go
│   └── util.go
├── image/image.go
├── list/
│   ├── filterable.go
│   ├── focus.go
│   ├── highlight.go
│   ├── item.go
│   └── list.go
├── logo/
│   ├── letterforms.go
│   ├── logo.go
│   └── rand.go
├── model/
│   ├── chat.go
│   ├── clipboard.go
│   ├── filter.go
│   ├── header.go
│   ├── keys.go
│   ├── landing.go
│   ├── lsp.go
│   ├── mcp.go
│   ├── onboarding.go
│   ├── pills.go
│   ├── session.go
│   ├── sidebar.go
│   ├── skills.go
│   ├── status.go
│   └── ui.go
├── notification/
│   ├── icon_darwin.go
│   ├── icon_other.go
│   ├── native.go
│   ├── noop.go
│   └── notification.go
├── styles/
│   ├── grad.go
│   ├── quickstyle.go
│   ├── styles.go
│   └── themes.go
├── util/util.go
└── xchroma/chroma.go
```

**总计**: 76 个 Go 源文件，按 15 个功能包组织

---

> **Document Version**: 1.2
> **Date**: 2026-06-09
> **Status**: Complete — 全部 8 个 Phase 完成，31 项测试通过，DialogStack 完全取代旧 boolean dialog

### 进度总结

| Phase | 状态 | 完成度 |
|-------|------|--------|
| 1. 基础架构 | ✅ | 6/6 |
| 2. 列表 + 消息项 | ✅ | 6/6 |
| 3. 布局引擎 | ✅ | 5/5 |
| 4. 动画系统 | ✅ | 3/3 |
| 5. 状态机 + 视图 | ✅ | 6/6 |
| 6. Dialog 系统 | ✅ | 4/4 |
| 7. 样式系统 | ✅ | 3/3 |
| 8. 测试 + 清理 | ✅ | 6/6 |

**总计**: 新增 ~18 个 Go 源文件（含 common/elements.go, layout_test.go, state_test.go, chat/chat_test.go），4 个新包 (chat/, list/, sidebar/, dialog/)，增强 2 个包 (common/, input/)，4 个测试文件 (list: 11 tests, layout: 6 tests, state: 4 tests, chat: 20 tests)，移除 ~600 行死代码
