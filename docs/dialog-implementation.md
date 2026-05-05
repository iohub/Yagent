# TUI 确认弹窗（Dialog）实现文档

## 概述

Crush 的 TUI 确认弹窗系统基于 [Bubble Tea](https://github.com/charmbracelet/bubbletea) 框架和 [Ultraviolet](https://github.com/charmbracelet/ultraviolet) 终端渲染引擎构建，使用 [Lipgloss](https://github.com/charmbracelet/lipgloss) 进行样式编排。弹窗采用栈式管理（Overlay），支持多层嵌套。

---

## 核心架构

### 技术栈

| 组件 | 库 | 用途 |
|------|------|------|
| 事件模型 | `charm.land/bubbletea/v2` (tea) | TEA 架构的消息循环 |
| 渲染引擎 | `github.com/charmbracelet/ultraviolet` (uv) | 终端屏幕绘制 |
| 样式系统 | `charm.land/lipgloss/v2` | 样式编排与计算 |
| 按键管理 | `charm.land/bubbles/v2/key` | 按键绑定与匹配 |
| 帮助系统 | `charm.land/bubbles/v2/help` | 快捷键提示展示 |
| 视口滚动 | `charm.land/bubbles/v2/viewport` | 长内容滚动区域 |
| 加载动画 | `charm.land/bubbles/v2/spinner` | 加载状态指示器 |
| 文本输入 | `charm.land/bubbles/v2/textinput` | 多行文本输入 |
| 差异比较 | `github.com/aymanbagabas/go-udiff` + `github.com/alecthomas/chroma/v2` | Diff 生成与语法高亮 |

### 目录结构

```
internal/ui/dialog/
├── dialog.go          # Dialog 接口定义 + Overlay 栈管理器
├── common.go          # RenderContext 渲染上下文 + 光标调整工具
├── actions.go         # Action 消息类型定义
├── permissions.go     # 权限确认弹窗（核心）
├── quit.go            # 退出确认弹窗
├── arguments.go       # 参数输入弹窗
├── api_key_input.go   # API Key 输入弹窗
├── models.go / models_list.go / models_item.go  # 模型选择弹窗
├── commands.go / commands_item.go  # 命令选择弹窗
├── sessions.go / sessions_item.go  # 会话管理弹窗
├── filepicker.go      # 文件选择器弹窗
├── oauth.go / oauth_copilot.go / oauth_hyper.go  # OAuth 认证弹窗
├── reasoning.go       # 推理力度选择弹窗
└── oauth.go           # OAuth 认证弹窗

internal/ui/common/
├── button.go          # 按钮组件（支持选中状态 + 下划线字符）
├── elements.go        # DialogTitle、Section、Status 等通用元素
├── scrollbar.go       # 垂直滚动条组件
├── diff.go            # Diff 格式化器
├── common.go          # CenterRect、BottomLeftRect、IsFileTooBig 等工具函数
└── highlight.go       # 语法高亮封装

internal/ui/styles/
├── styles.go          # Styles 结构体定义（含 Dialog 子结构）
├── quickstyle.go      # quickStyle() 构建默认主题样式
├── themes.go          # CharmtonePantera() / HypercrushObsidiana() 主题
└── grad.go            # 渐变色工具函数
```

---

## 1. Dialog 接口定义

所有弹窗必须实现 `Dialog` 接口（位于 `dialog.go`）：

```go
// dialog.go

// Action represents an action taken in a dialog after handling a message.
type Action any

// Dialog is a component that can be displayed on top of the UI.
type Dialog interface {
    // ID returns the unique identifier of the dialog.
    ID() string

    // HandleMsg processes a tea.Msg and returns an Action.
    // The caller is responsible for handling the Action.
    HandleMsg(msg tea.Msg) Action

    // Draw draws the dialog onto the provided screen within the specified area.
    // Returns the desired cursor position.
    Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor
}
```

### 可选接口：LoadingDialog

需要加载状态的弹窗可选实现 `LoadingDialog`：

```go
// dialog.go

// LoadingDialog is a dialog that can show a loading state.
type LoadingDialog interface {
    StartLoading() tea.Cmd
    StopLoading()
}
```

### 关闭按键

```go
// dialog.go

// CloseKey is the default key binding to close dialogs.
var CloseKey = key.NewBinding(
    key.WithKeys("esc", "alt+esc"),
    key.WithHelp("esc", "exit"),
)
```

---

## 2. Overlay 栈管理器

`Overlay` 结构体管理多个 Dialog 的堆栈，最新打开的 Dialog 在最上层（切片末尾）。

```go
// dialog.go

type Overlay struct {
    dialogs []Dialog
}
```

### 核心方法

| 方法 | 功能 |
|------|------|
| `NewOverlay(dialogs ...Dialog)` | 创建 Overlay 实例 |
| `HasDialogs() bool` | 检查是否有弹窗 |
| `ContainsDialog(id string) bool` | 检查指定 ID 的弹窗是否存在 |
| `OpenDialog(dialog Dialog)` | 将弹窗压入栈顶 |
| `CloseDialog(dialogID string)` | 按 ID 关闭指定弹窗 |
| `CloseFrontDialog()` | 关闭栈顶弹窗 |
| `Dialog(dialogID string) Dialog` | 按 ID 获取弹窗 |
| `DialogLast() Dialog` | 获取栈顶弹窗 |
| `BringToFront(dialogID string)` | 将指定弹窗移到栈顶 |
| `Update(msg tea.Msg) tea.Msg` | 处理消息（转发给栈顶弹窗） |
| `Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor` | 依次绘制所有弹窗 |
| `StartLoading() tea.Cmd` | 启动栈顶弹窗的加载状态 |
| `StopLoading()` | 停止栈顶弹窗的加载状态 |

### 消息处理流程

```
用户按键 → UI.Update(msg)
            → m.dialog.Update(msg)  // 调用 overlay.Update
                → dialog.HandleMsg(msg)  // 栈顶弹窗处理
                → 返回 Action
            → m.handleDialogMsg(action)  // UI 层处理 Action
```

---

## 3. 渲染工具函数

### DrawCenter / DrawCenterCursor

将内容居中绘制在指定区域内：

```go
// dialog.go

func DrawCenter(scr uv.Screen, area uv.Rectangle, view string) {
    DrawCenterCursor(scr, area, view, nil)
}

func DrawCenterCursor(scr uv.Screen, area uv.Rectangle, view string, cur *tea.Cursor) {
    width, height := lipgloss.Size(view)
    center := common.CenterRect(area, width, height)
    if cur != nil {
        cur.X += center.Min.X
        cur.Y += center.Min.Y
    }
    uv.NewStyledString(view).Draw(scr, center)
}
```

### DrawOnboarding / DrawOnboardingCursor

将内容绘制在屏幕左下区域（用于 onboarding 流程）：

```go
func DrawOnboarding(scr uv.Screen, area uv.Rectangle, view string) {
    DrawOnboardingCursor(scr, area, view, nil)
}

func DrawOnboardingCursor(scr uv.Screen, area uv.Rectangle, view string, cur *tea.Cursor) {
    width, height := lipgloss.Size(view)
    bottomLeft := common.BottomLeftRect(area, width, height)
    if cur != nil {
        cur.X += bottomLeft.Min.X
        cur.Y += bottomLeft.Min.Y
    }
    uv.NewStyledString(view).Draw(scr, bottomLeft)
}
```

### 光标调整

弹窗内的输入控件需要相对于弹窗调整光标位置：

```go
// common.go

func InputCursor(t *styles.Styles, cur *tea.Cursor) *tea.Cursor {
    if cur != nil {
        titleStyle := t.Dialog.Title
        dialogStyle := t.Dialog.View
        inputStyle := t.Dialog.InputPrompt

        // 累加弹窗边框/内边距/外边距的偏移
        cur.X += inputStyle.GetBorderLeftSize() +
            inputStyle.GetMarginLeft() +
            inputStyle.GetPaddingLeft() +
            dialogStyle.GetBorderLeftSize() +
            dialogStyle.GetPaddingLeft() +
            dialogStyle.GetMarginLeft()
        cur.Y += titleStyle.GetVerticalFrameSize() +
            inputStyle.GetBorderTopSize() +
            inputStyle.GetMarginTop() +
            // ... 更多累加
    }
    return cur
}
```

---

## 4. RenderContext — 通用弹窗渲染器

`RenderContext` 提供了一种声明式的弹窗布局方式，将标题、内容块、帮助文本按标准格式组合：

```go
// common.go

type RenderContext struct {
    Styles                 *styles.Styles
    TitleStyle             lipgloss.Style   // 默认为 t.Dialog.Title
    ViewStyle              lipgloss.Style   // 默认为 t.Dialog.View
    TitleGradientFromColor color.Color      // 标题渐变起始色
    TitleGradientToColor   color.Color      // 标题渐变结束色
    Width                  int              // 弹窗总宽度
    Gap                    int              // 内容块之间的间距
    Title                  string           // 弹窗标题
    TitleInfo              string           // 标题旁附加信息（原始字符串）
    Parts                  []string         // 内容块列表
    Help                   string           // 底部帮助文本
    IsOnboarding           bool             // 是否在 onboarding 模式下渲染
}

func NewRenderContext(t *styles.Styles, width int) *RenderContext
func (rc *RenderContext) AddPart(part string)
func (rc *RenderContext) Render() string
```

### 渲染顺序

1. 标题（带渐变装饰线 `╱╱╱`）— 如果有
2. 内容块（按顺序，间隔 `Gap` 行）
3. 帮助文本

### 使用示例

```go
rc := NewRenderContext(com.Styles, contentWidth)
rc.Title = "Confirm"
rc.AddPart("Do you want to proceed?")
rc.Help = "enter: confirm  esc: cancel"
view := rc.Render()
// 然后用 lipgloss 包裹 dialog frame
dialogStyle := t.Dialog.View.Width(rc.Width).Padding(0, 1)
output := dialogStyle.Render(view)
```

---

## 5. 权限确认弹窗（Permissions）— 核心实现

这是最复杂的弹窗，支持多种工具类型的权限请求，包括 Bash 命令执行确认、文件编辑 diff 确认、下载确认等。

### 5.1 数据结构

```go
// permissions.go

type Permissions struct {
    com          *common.Common
    windowWidth  int
    windowHeight int
    fullscreen   bool

    permission     permission.PermissionRequest
    selectedOption int         // 0: Allow, 1: Allow for Session, 2: Deny

    viewport       viewport.Model
    viewportDirty  bool
    viewportWidth  int

    // Diff view state.
    diffSplitMode        *bool    // nil = 默认
    defaultDiffSplitMode bool
    diffXOffset          int      // 水平滚动偏移
    unifiedDiffContent   string   // 统一模式 diff 缓存
    splitDiffContent     string   // 分列模式 diff 缓存

    help   help.Model
    keyMap permissionsKeyMap
}
```

### 5.2 按键绑定

```go
func defaultPermissionsKeyMap() permissionsKeyMap {
    return permissionsKeyMap{
        Left:            key.WithKeys("left", "h"),              // 上一个选项
        Right:           key.WithKeys("right", "l"),             // 下一个选项
        Tab:             key.WithKeys("tab"),                    // 下一个选项
        Select:          key.WithKeys("enter", "ctrl+y"),        // 确认
        Allow:           key.WithKeys("a", "A", "ctrl+a"),       // 直接允许
        AllowSession:    key.WithKeys("s", "S", "ctrl+s"),       // 允许本次会话
        Deny:            key.WithKeys("d", "D"),                 // 拒绝
        Close:           CloseKey,                               // ESC 关闭（= 拒绝）
        ToggleDiffMode:  key.WithKeys("t"),                      // 切换 diff 模式
        ToggleFullscreen:key.WithKeys("f"),                      // 切换全屏
        ScrollUp:        key.WithKeys("shift+up", "K"),          // 向上滚动
        ScrollDown:      key.WithKeys("shift+down", "J"),        // 向下滚动
        ScrollLeft:      key.WithKeys("shift+left", "H"),        // 向左滚动
        ScrollRight:     key.WithKeys("shift+right", "L"),       // 向右滚动
        Choose:          key.WithKeys("left", "right"),          // 选择（帮助用）
        Scroll:          key.WithKeys("shift+left", "shift+down", "shift+up", "shift+right"), // 滚动（帮助用）
    }
}
```

### 5.3 消息处理（HandleMsg）

```go
func (p *Permissions) HandleMsg(msg tea.Msg) Action {
    switch msg := msg.(type) {
    case tea.KeyPressMsg:
        switch {
        case key.Matches(msg, p.keyMap.Close):
            return p.respond(PermissionDeny)           // ESC → 拒绝
        case key.Matches(msg, p.keyMap.Right), key.Matches(msg, p.keyMap.Tab):
            p.selectedOption = (p.selectedOption + 1) % 3  // 循环切换
        case key.Matches(msg, p.keyMap.Left):
            p.selectedOption = (p.selectedOption + 2) % 3  // 循环切换（+2 避免负数）
        case key.Matches(msg, p.keyMap.Select):
            return p.selectCurrentOption()              // 确认当前选项
        case key.Matches(msg, p.keyMap.Allow):
            return p.respond(PermissionAllow)
        case key.Matches(msg, p.keyMap.AllowSession):
            return p.respond(PermissionAllowForSession)
        case key.Matches(msg, p.keyMap.Deny):
            return p.respond(PermissionDeny)
        case key.Matches(msg, p.keyMap.ToggleDiffMode):
            if p.hasDiffView() {
                p.diffSplitMode = ptr(!p.isSplitMode())
                p.viewportDirty = true
            }
        case key.Matches(msg, p.keyMap.ToggleFullscreen):
            if p.hasDiffView() {
                p.fullscreen = !p.fullscreen
            }
        // ... 滚动处理
        }
    case tea.MouseWheelMsg:
        // 鼠标滚轮滚动
    }
    return nil
}
```

### 5.4 响应消息类型

```go
// actions.go

type ActionPermissionResponse struct {
    Permission permission.PermissionRequest
    Action     PermissionAction
}

// permissions.go

type PermissionAction string

const (
    PermissionAllow           PermissionAction = "allow"
    PermissionAllowForSession PermissionAction = "allow_session"
    PermissionDeny            PermissionAction = "deny"
)
```

### 5.5 尺寸计算逻辑

弹窗尺寸根据内容类型动态调整：

```go
func (p *Permissions) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
    // 窗口太小时强制全屏
    forceFullscreen := area.Dx() <= minWindowWidth || area.Dy() <= minWindowHeight

    var width, maxHeight int
    if forceFullscreen || (p.fullscreen && p.hasDiffView()) {
        // 全屏模式
        width = area.Dx()
        maxHeight = area.Dy()
    } else if p.hasDiffView() {
        // 有 diff 内容：宽窗口，80% 比例，最大 180 列
        width = min(int(float64(area.Dx())*0.8), 180)
        maxHeight = int(float64(area.Dy()) * 0.8)
    } else {
        // 简单内容：窄窗口，60% 比例，最大 100 列
        width = min(int(float64(area.Dx())*0.6), 100)
        maxHeight = int(float64(area.Dy()) * 0.5)
    }
    // ...
}
```

常量定义：

| 常量 | 值 | 说明 |
|------|-----|------|
| `diffMaxWidth` | 180 | diff 视图最大宽度 |
| `diffSizeRatio` | 0.8 | diff 视图相对窗口比例 |
| `simpleMaxWidth` | 100 | 简单内容弹窗最大宽度 |
| `simpleSizeRatio` | 0.6 | 简单内容弹窗相对窗口比例 |
| `simpleHeightRatio` | 0.5 | 简单内容弹窗相对窗口高度比例 |
| `splitModeMinWidth` | 140 | 分列 diff 模式最小宽度 |
| `minWindowWidth` | 77 | 强制全屏最小窗口宽度 |
| `minWindowHeight` | 20 | 强制全屏最小窗口高度 |
| `layoutSpacingLines` | 4 | 布局间距行数 |

### 5.6 头部渲染（renderHeader）

头部包含：
1. 标题 `Permission Required`（带渐变装饰线）
2. 工具名称行
3. 路径行
4. 工具特定的信息行

```go
func (p *Permissions) renderHeader(contentWidth int) string {
    title := common.DialogTitle(t, "Permission Required", ...)
    lines := []string{title, "", toolLine, pathLine}

    switch p.permission.ToolName {
    case tools.BashToolName:
        // 显示命令描述
    case tools.DownloadToolName:
        // 显示 URL + 文件名 + 超时
    case tools.EditToolName, tools.WriteToolName, tools.MultiEditToolName, tools.ViewToolName:
        // 显示文件路径
    case tools.LSToolName:
        // 显示目录
    }
    return lipgloss.JoinVertical(lipgloss.Left, lines...)
}
```

### 5.7 内容渲染（renderContent）

根据工具类型选择对应的渲染函数：

| 工具类型 | 渲染函数 | 内容 |
|----------|----------|------|
| `bash` | `renderBashContent` | 命令文本 |
| `edit` | `renderEditContent` | 文件 diff |
| `write` | `renderWriteContent` | 文件 diff |
| `multiedit` | `renderMultiEditContent` | 文件 diff |
| `download` | `renderDownloadContent` | URL + 文件 + 超时 |
| `fetch` | `renderFetchContent` | URL |
| `agentic_fetch` | `renderAgenticFetchContent` | URL + Prompt |
| `view` | `renderViewContent` | 文件路径 + 行数信息 |
| `ls` | `renderLSContent` | 目录 + 忽略模式 |
| 其他 | `renderDefaultContent` | 描述 + JSON params（语法高亮） |

### 5.8 Diff 渲染

对于 edit/write/multiedit 工具，弹窗使用 diff 视图展示文件变更：

```go
func (p *Permissions) renderDiff(filePath, oldContent, newContent string, contentWidth int) string {
    if !p.viewportDirty {
        if p.isSplitMode() {
            return p.splitDiffContent
        }
        return p.unifiedDiffContent
    }

    formatter := common.DiffFormatter(p.com.Styles).
        Before(fsext.PrettyPath(filePath), oldContent).
        After(fsext.PrettyPath(filePath), newContent).
        XOffset(p.diffXOffset).
        Width(contentWidth)

    if isSplitMode {
        formatter = formatter.Split()
        p.splitDiffContent = formatter.String()
        return p.splitDiffContent
    }
    formatter = formatter.Unified()
    p.unifiedDiffContent = formatter.String()
    return p.unifiedDiffContent
}
```

### 5.9 按钮渲染（renderButtons）

底部显示三个按钮，支持水平排列和垂直回退：

```go
func (p *Permissions) renderButtons(contentWidth int) string {
    buttons := []common.ButtonOpts{
        {Text: "Allow", UnderlineIndex: 0, Selected: p.selectedOption == 0},
        {Text: "Allow for Session", UnderlineIndex: 10, Selected: p.selectedOption == 1},
        {Text: "Deny", UnderlineIndex: 0, Selected: p.selectedOption == 2},
    }

    content := common.ButtonGroup(p.com.Styles, buttons, "  ")

    // 如果按钮太宽，则垂直堆叠
    if lipgloss.Width(content) > contentWidth {
        content = common.ButtonGroup(p.com.Styles, buttons, "\n")
        return lipgloss.NewStyle().Width(contentWidth).Align(lipgloss.Center).Render(content)
    }

    return lipgloss.NewStyle().Width(contentWidth).Align(lipgloss.Right).Render(content)
}
```

### 5.10 帮助系统（help.KeyMap）

弹窗通过实现 `help.KeyMap` 接口提供快捷键提示：

```go
func (p *Permissions) ShortHelp() []key.Binding {
    bindings := []key.Binding{
        p.keyMap.Choose,
        p.keyMap.Select,
        p.keyMap.Close,
    }
    if p.canScroll() {
        bindings = append(bindings, p.keyMap.Scroll)
    }
    if p.hasDiffView() {
        bindings = append(bindings, p.keyMap.ToggleDiffMode, p.keyMap.ToggleFullscreen)
    }
    return bindings
}

func (p *Permissions) FullHelp() [][]key.Binding {
    return [][]key.Binding{p.ShortHelp()}
}
```

---

## 6. 退出确认弹窗（Quit）— 简化实现

这是一个更简单的确认弹窗，只有两个选项：Yes / No。

```go
// quit.go

type Quit struct {
    com        *common.Common
    selectedNo bool         // true = 选中 "Nope"
    keyMap     struct {
        LeftRight, EnterSpace, Yes, No, Tab, Close, Quit key.Binding
    }
}
```

### 关键行为

| 按键 | 行为 |
|------|------|
| `←/→` / `Tab` | 切换 Yes/No 选项 |
| `Enter` / `Space` | 确认选中的选项 |
| `y/Y` / `Ctrl+C` | 直接确认（退出） |
| `n/N` / `Esc` | 直接取消 |
| `Ctrl+C` | 退出应用 |

### 渲染

```go
func (q *Quit) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
    const question = "Are you sure you want to quit?"
    buttonOpts := []common.ButtonOpts{
        {Text: "Yep!", Selected: !q.selectedNo, Padding: 3},
        {Text: "Nope", Selected: q.selectedNo, Padding: 3},
    }
    buttons := common.ButtonGroup(q.com.Styles, buttonOpts, " ")
    content := baseStyle.Render(
        lipgloss.JoinVertical(lipgloss.Center, question, "", buttons),
    )
    view := q.com.Styles.Dialog.Quit.Frame.Render(content)
    DrawCenter(scr, area, view)
    return nil
}
```

---

## 7. 参数输入弹窗（Arguments）

用于在执行自定义命令前收集参数：

```go
type Arguments struct {
    com          *common.Common
    title        string
    arguments    []commands.Argument
    inputs       []textinput.Model  // 每个参数一个输入框
    focused      int                // 当前聚焦的输入框索引
    spinner      spinner.Model      // 加载动画
    loading      bool
    description  string
    resultAction Action
    viewport     viewport.Model
}
```

### 输入框聚焦循环

```go
func (a *Arguments) focusInput(newIndex int) {
    a.inputs[a.focused].Blur()
    n := len(a.inputs)
    a.focused = ((newIndex % n) + n) % n  // 循环回绕
    a.inputs[a.focused].Focus()
    a.ensureFieldVisible(a.focused)         // 确保可见
}
```

---

## 8. 通用按钮组件

位于 `common/button.go`：

```go
type ButtonOpts struct {
    Text         string  // 按钮文本
    UnderlineIndex int   // 0-based 下划线字符索引（-1 无下划线）
    Selected     bool    // 是否被选中
    Padding      int     // 左右内边距（0 时默认为 2）
}

func Button(t *styles.Styles, opts ButtonOpts) string {
    style := t.Button.Blurred
    if opts.Selected {
        style = t.Button.Focused
    }
    text := style.Padding(0, opts.Padding).Render(text)
    // 如果指定了下划线索引，对字符添加下划线样式
    if opts.UnderlineIndex != -1 {
        text = lipgloss.StyleRanges(text, lipgloss.NewRange(start, end, style.Underline(true)))
    }
    return text
}

func ButtonGroup(t *styles.Styles, buttons []ButtonOpts, spacing string) string {
    parts := make([]string, len(buttons))
    for i, b := range buttons {
        parts[i] = Button(t, b)
    }
    return strings.Join(parts, spacing)
}
```

### 样式

```go
// quickstyle.go

s.Button.Focused = lipgloss.NewStyle().Foreground(o.onPrimary).Background(o.secondary)
s.Button.Blurred = lipgloss.NewStyle().Foreground(o.fgBase).Background(o.bgLessVisible)
```

---

## 9. DialogTitle — 标题装饰线

```go
// elements.go

func DialogTitle(t *styles.Styles, title string, width int, fromColor, toColor color.Color) string {
    char := "╱"
    length := lipgloss.Width(title) + 1
    remainingWidth := width - length
    if remainingWidth > 0 {
        lines := strings.Repeat(char, remainingWidth)
        lines = styles.ApplyForegroundGrad(t.Dialog.TitleLineBase, lines, fromColor, toColor)
        title = title + " " + lines
    }
    return title
}
```

---

## 10. 滚动条组件

```go
// scrollbar.go

func Scrollbar(s *styles.Styles, height, contentSize, viewportSize, offset int) string {
    if height <= 0 || contentSize <= viewportSize {
        return ""
    }

    // 滑块大小（最小 1）
    thumbSize := max(1, height*viewportSize/contentSize)
    // 滑块位置
    maxOffset := contentSize - viewportSize
    trackSpace := height - thumbSize
    thumbPos := 0
    if trackSpace > 0 && maxOffset > 0 {
        thumbPos = min(trackSpace, offset*trackSpace/maxOffset)
    }

    var sb strings.Builder
    for i := range height {
        if i > 0 { sb.WriteString("\n") }
        if i >= thumbPos && i < thumbPos+thumbSize {
            sb.WriteString(s.Dialog.ScrollbarThumb.Render(styles.ScrollbarThumb))  // ┃
        } else {
            sb.WriteString(s.Dialog.ScrollbarTrack.Render(styles.ScrollbarTrack))  // │
        }
    }
    return sb.String()
}
```

---

## 11. 样式系统

### Styles 结构中的 Dialog 子结构

```go
// styles.go

type Styles struct {
    Dialog struct {
        Title              lipgloss.Style        // 标题
        TitleText          lipgloss.Style        // 标题文本
        TitleError         lipgloss.Style        // 错误标题
        TitleAccent        lipgloss.Style        // 强调标题
        TitleLineBase      lipgloss.Style        // 装饰线基础样式
        TitleGradFromColor color.Color           // 渐变起始色
        TitleGradToColor   color.Color           // 渐变结束色
        View               lipgloss.Style        // 弹窗主体边框
        PrimaryText        lipgloss.Style        // 主文本
        SecondaryText      lipgloss.Style        // 次文本
        HelpView           lipgloss.Style        // 帮助文本区域
        Help               struct{ ... }          // 帮助子样式
        NormalItem         lipgloss.Style        // 普通列表项
        SelectedItem       lipgloss.Style        // 选中列表项
        InputPrompt        lipgloss.Style        // 输入框提示
        List               lipgloss.Style        // 列表
        Spinner            lipgloss.Style        // 加载动画
        ContentPanel       lipgloss.Style        // 内容面板（背景色块）
        ScrollbarThumb     lipgloss.Style        // 滚动条滑块
        ScrollbarTrack     lipgloss.Style        // 滚动条轨道
        Arguments          struct{ ... }          // 参数输入子样式
        ListItem           struct{ ... }          // 列表项子样式
        Models             struct{ ... }          // 模型选择子样式
        Permissions        struct{ KeyText, ValueText, ParamsBg }  // 权限子样式
        Quit               struct{ Content, Frame }  // 退出确认子样式
        APIKey             struct{ Spinner }      // API Key 子样式
        OAuth              struct{ ... }          // OAuth 子样式
        ImagePreview       lipgloss.Style        // 图片预览
        Sessions           struct{ ... }          // 会话管理子样式
    }
    Button struct { Focused, Blurred lipgloss.Style }
    // ...
}
```

### 默认样式构建（CharmtonePantera）

```go
// quickstyle.go

// Dialog
s.Dialog.Title = base.Padding(0, 1).Foreground(o.primary)
s.Dialog.View = base.Border(lipgloss.RoundedBorder()).BorderForeground(o.primary)
s.Dialog.ContentPanel = base.Background(o.bgLessVisible).Foreground(o.fgBase).Padding(1, 2)
s.Dialog.ScrollbarThumb = // ...
s.Dialog.ScrollbarTrack = // ...

// Permissions
s.Dialog.Permissions.KeyText = lipgloss.NewStyle().Foreground(o.fgMoreSubtle)
s.Dialog.Permissions.ValueText = lipgloss.NewStyle().Foreground(o.fgBase)
s.Dialog.Permissions.ParamsBg = o.bgLessVisible

// Quit
s.Dialog.Quit.Content = lipgloss.NewStyle().Foreground(o.fgBase)
s.Dialog.Quit.Frame = lipgloss.NewStyle().
    BorderForeground(o.primary).
    Border(lipgloss.RoundedBorder()).
    Padding(1, 2)
```

---

## 12. 权限请求数据模型

```go
// permission/permission.go

type PermissionRequest struct {
    ID          string  // UUID
    SessionID   string
    ToolCallID  string
    ToolName    string  // "bash", "edit", "write", "multiedit", "download", "fetch", "agentic_fetch", "view", "ls"
    Description string
    Action      string
    Params      any     // 工具特定的参数
    Path        string
}
```

### 参数类型映射

| ToolName | Params 类型 |
|----------|-------------|
| `bash` | `BashPermissionsParams` |
| `download` | `DownloadPermissionsParams` |
| `edit` | `EditPermissionsParams` |
| `write` | `WritePermissionsParams` |
| `multiedit` | `MultiEditPermissionsParams` |
| `view` | `ViewPermissionsParams` |
| `ls` | `LSPermissionsParams` |
| `fetch` | `FetchPermissionsParams` |
| `agentic_fetch` | `AgenticFetchPermissionsParams` |

---

## 13. UI 层集成

### 弹窗打开流程

```
权限服务发布 PermissionRequest
    → pubsub.Event[permission.PermissionRequest]
    → m.openPermissionsDialog(perm)
        → m.dialog.CloseDialog(dialog.PermissionsID)  // 关闭旧的
        → dialog.NewPermissions(m.com, perm, opts...)
        → m.dialog.OpenDialog(permDialog)
```

### Action 处理流程

```go
// ui.go: handleDialogMsg

case dialog.ActionPermissionResponse:
    m.dialog.CloseDialog(dialog.PermissionsID)
    switch msg.Action {
    case dialog.PermissionAllow:
        m.com.Workspace.PermissionGrant(msg.Permission)
    case dialog.PermissionAllowForSession:
        m.com.Workspace.PermissionGrantPersistent(msg.Permission)
    case dialog.PermissionDeny:
        m.com.Workspace.PermissionDeny(msg.Permission)
    }

case dialog.ActionClose:
    m.dialog.CloseFrontDialog()

case dialog.ActionQuit:
    // 退出应用
```

---

## 14. 自定义弹窗实现指南

如需实现一个新的确认弹窗，按以下步骤操作：

### 步骤 1：定义弹窗结构体

```go
type MyDialog struct {
    com        *common.Common
    selected   int
    keyMap     struct{ ... }
}
```

### 步骤 2：实现 Dialog 接口

```go
const MyDialogID = "my_dialog"

func (d *MyDialog) ID() string { return MyDialogID }

func (d *MyDialog) HandleMsg(msg tea.Msg) Action {
    switch msg := msg.(type) {
    case tea.KeyPressMsg:
        switch {
        case key.Matches(msg, d.keyMap.Close):
            return dialog.ActionClose{}
        case key.Matches(msg, d.keyMap.Confirm):
            return MyAction{}
        }
    }
    return nil
}

func (d *MyDialog) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
    view := d.com.Styles.Dialog.View.Render(...)
    DrawCenter(scr, area, view)
    return nil
}
```

### 步骤 3：定义 Action 消息类型

```go
type MyAction struct {
    Value string
}
```

在 `actions.go` 中定义或在弹窗文件本地定义。

### 步骤 4：在 UI 层注册

```go
// ui.go: openDialog()
case MyDialogID:
    dialog := NewMyDialog(m.com)
    m.dialog.OpenDialog(dialog)
    return nil
```

### 步骤 5：处理 Action

```go
// ui.go: handleDialogMsg()
case MyAction:
    // 处理用户选择
    m.dialog.CloseDialog(dialog.MyDialogID)
```

---

## 15. 布局计算总结

弹窗的垂直布局通常如下：

```
┌─ Dialog.Frame ──────────────────────────┐
│  ┌─ Dialog.View ───────────────────────┐ │
│  │  [Title] "Permission Required ╱╱╱"  │ │
│  │                                      │ │
│  │  [Content]                           │ │
│  │  ┌─ ContentPanel ─────────────────┐  │ │
│  │  │ Tool: bash                     │  │ │
│  │  │ Command: rm -rf /tmp           │  │ │
│  │  └────────────────────────────────┘  │ │
│  │                                      │ │
│  │  [Buttons]  Allow  Allow Session  Deny│ │
│  │                                      │ │
│  │  [Help]  ←/→ choose  enter confirm  │ │
│  └──────────────────────────────────────┘ │
└──────────────────────────────────────────┘
```

各部分高度计算：

```go
headerHeight := lipgloss.Height(header)
buttonsHeight := lipgloss.Height(buttons)
helpHeight := lipgloss.Height(helpView)
frameHeight := dialogStyle.GetVerticalFrameSize() + layoutSpacingLines // 4行

contentHeight := lipgloss.Height(renderedContent)
neededHeight := fixedHeight + contentHeight

if neededHeight < maxHeight {
    // 收缩到内容大小
    availableHeight = contentHeight
} else {
    // 使用最大高度 - 其他部分
    availableHeight = maxHeight - fixedHeight - headerHeight - buttonsHeight - helpHeight
}
```

---

## 附录：关键文件索引

| 文件 | 职责 |
|------|------|
| `dialog/dialog.go` | `Dialog` 接口、`Overlay` 栈、`DrawCenter` 工具 |
| `dialog/common.go` | `RenderContext`、`InputCursor` |
| `dialog/actions.go` | 所有 `Action` 消息类型 |
| `dialog/permissions.go` | 权限确认弹窗（最完整实现） |
| `dialog/quit.go` | 退出确认弹窗（最简单实现） |
| `dialog/arguments.go` | 参数输入弹窗 |
| `dialog/filepicker.go` | 文件选择器弹窗 |
| `dialog/oauth.go` | OAuth 认证弹窗 |
| `common/button.go` | 按钮组件 |
| `common/elements.go` | `DialogTitle`、`Section`、`Status`、`CenterRect` |
| `common/scrollbar.go` | 垂直滚动条 |
| `common/diff.go` | Diff 格式化器 |
| `common/common.go` | 通用工具函数 |
| `styles/styles.go` | `Styles` 结构体 |
| `styles/quickstyle.go` | `quickStyle()` 默认主题构建 |
| `styles/themes.go` | 主题函数 |
