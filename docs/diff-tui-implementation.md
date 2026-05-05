# TUI Diff 组件实现文档

> 本文档详细描述 Crush TUI 中 diff 显示组件的完整实现，可用于复刻该组件。

---

## 1. 整体架构

```
internal/
├── diff/                          # 核心 diff 生成（调用 go-udiff）
│   └── diff.go                    #   GenerateDiff() → unified diff 字符串
├── diffdetect/                    # diff 检测
│   └── detect.go                  #   IsUnifiedDiff() 判断文本是否为 unified diff
├── ui/
│   ├── common/
│   │   └── diff.go                #   DiffFormatter() 工厂函数（绑定样式+chroma）
│   ├── chat/
│   │   └── unified_diff.go        #   聊天中 unified diff 的渲染入口
│   └── diffview/                  # ★ 核心 diff view 组件
│       ├── diffview.go            #   DiffView 主结构体 + 渲染逻辑（823行）
│       ├── split.go               #   splitHunk/splitLine 结构 + 分栏转换
│       ├── style.go               #   LineStyle/Style + 深浅主题
│       ├── util.go                #   pad/ternary/boti/isOdd 工具函数
│       └── diffview_test.go       #   完整的 golden master 测试
```

### 调用链路

```
AI 工具输出 → unified diff 文本
  → diffdetect.IsUnifiedDiff() 判断
  → parseUnifiedDiff() 解析为 (path, before, after) 列表
  → common.DiffFormatter(sty) → diffview.New()
  → 链式设置 Before/After/FileName/Width/Height/Split 等
  → .String() → 渲染为带 ANSI 转义的富文本
```

---

## 2. 核心依赖

```go
// go.mod 关键依赖
charm.land/lipgloss/v2          // UI 样式渲染（Border/Width/MaxWidth/Background 等）
charm.land/bubbles/v2             // bubbletea 组件基础
github.com/alecthomas/chroma/v2   // 语法高亮引擎
github.com/aymanbagabas/go-udiff  // diff 计算引擎
github.com/charmbracelet/x/ansi   // ANSI 字符串宽度/截断/图元处理
github.com/charmbracelet/x/exp/charmtone  // 配色方案
github.com/zeebo/xxh3             // 语法高亮 cache key hash
```

---

## 3. DiffView 主结构体 (`diffview/diffview.go`)

### 3.1 结构体定义

```go
type DiffView struct {
    // ---- 配置项 ----
    layout          layout        // layoutUnified | layoutSplit
    before          file          // {path, content} 修改前
    after           file          // {path, content} 修改后
    fileName        string        // 文件名字符串头
    contextLines    int           // 上下文行数（默认 udiff.DefaultContextLines = 3）
    lineNumbers     bool          // 是否显示行号（默认 true）
    height          int           // 最大显示高度
    width           int           // 最大显示宽度
    xOffset         int           // 水平滚动偏移（跳过前 N 个图元）
    yOffset         int           // 垂直滚动偏移（跳过前 N 行）
    infiniteYScroll bool          // 是否允许超出最后一行后继续滚动
    style           Style         // 行样式主题
    tabWidth        int           // Tab 展开宽度（默认 8）
    chromaStyle     *chroma.Style // 语法高亮样式（nil = 不高亮）

    // ---- 计算缓存（computeDiff 后填充）----
    isComputed      bool
    err             error
    unified         udiff.UnifiedDiff
    edits           []udiff.Edit
    splitHunks      []splitHunk   // layoutSplit 时填充
    totalLines      int
    codeWidth       int           // 代码区域宽度
    fullCodeWidth   int           // codeWidth + 前导符号区(2)
    extraColOnAfter bool          // 分栏模式下 after 侧是否需要额外 1 列
    beforeNumDigits int           // 前侧行号最大位数
    afterNumDigits  int           // 后侧行号最大位数
    cachedLexer     chroma.Lexer  // 按文件名缓存 lexer
    syntaxCache     map[string]string  // 语法高亮缓存: hash(content+bg) → 高亮结果
}
```

### 3.2 构建器模式（Fluent API）

所有配置方法都返回 `*DiffView`，支持链式调用：

| 方法 | 作用 | 默认值 |
|------|------|--------|
| `New()` | 创建，默认 dark style | — |
| `Unified()` / `Split()` | 布局模式 | Unified |
| `Before(path, content)` | 设置修改前 | — |
| `After(path, content)` | 设置修改后 | — |
| `FileName(name)` | 文件名字符串头 | 空（不显示） |
| `ContextLines(n)` | 上下文行数 | 3 |
| `Style(style)` | 主题样式 | DefaultDarkStyle() |
| `LineNumbers(bool)` | 显示行号 | true |
| `Height(n)` | 最大高度 | 0（不限） |
| `Width(n)` | 最大宽度 | 0（自动检测） |
| `XOffset(n)` | 水平偏移 | 0 |
| `YOffset(n)` | 垂直偏移 | 0 |
| `InfiniteYScroll(bool)` | 无限垂直滚动 | false |
| `TabWidth(n)` | Tab 宽度 | 8 |
| `ChromaStyle(*chroma.Style)` | 语法高亮样式 | nil |

### 3.3 `String()` 渲染流程

这是核心渲染入口，按顺序执行以下步骤：

```
String()
 ├─ normalizeLineEndings()     // \r\n → \n
 ├─ replaceTabs()              // \t → spaces (tabWidth)
 ├─ computeDiff()              // 调用 go-udiff 计算 diff
 ├─ convertDiffToSplit()       // unified → splitHunks（仅 Split 模式）
 ├─ adjustStyles()             // 行号区域添加 padding + right align
 ├─ detectNumDigits()          // 计算 before/after 行号最大位数
 ├─ detectTotalLines()         // 计算总行数（用于高度限制）
 ├─ preventInfiniteYScroll()   // clamp yOffset 到有效范围
 ├─ resizeCodeWidth()          // 根据 width 计算 codeWidth
 │   └─ (若 width==0) detectCodeWidth() // 自动检测最大行宽
 └─ lipgloss.NewStyle()
      ├─ MaxWidth(dv.width)
      ├─ MaxHeight(dv.height)
      └─ Render( renderUnified() | renderSplit() )
```

---

## 4. Diff 计算 (`computeDiff`)

```go
func (dv *DiffView) computeDiff() error {
    dv.edits = udiff.Lines(dv.before.content, dv.after.content)
    dv.unified, dv.err = udiff.ToUnifiedDiff(
        dv.before.path,
        dv.after.path,
        dv.before.content,
        dv.edits,
        dv.contextLines,
    )
    return dv.err
}
```

`go-udiff` 返回 `UnifiedDiff` 结构：

```go
type UnifiedDiff struct {
    FileName1 string
    FileName2 string
    FromLine  int          // 前侧起始行号
    ToLine    int          // 后侧起始行号
    Hunks     []*Hunk      // 差异块列表
}

type Hunk struct {
    FromLine int           // 前侧起始行
    ToLine   int           // 后侧起始行
    Lines    []Line        // 行列表
}

type Line struct {
    Kind    OpKind   // Equal | Insert | Delete
    Content string   // 行内容（含末尾 \n）
}
```

---

## 5. 统一布局渲染 (`renderUnified`)

### 5.1 数据结构

每行输出包含三个部分：
1. **Before 行号列** — `beforeNumDigits` 宽度，右对齐
2. **After 行号列** — `afterNumDigits` 宽度，右对齐
3. **代码列** — `fullCodeWidth` 宽度（= codeWidth + 2 前导符号）

### 5.2 渲染逻辑

```go
func (dv *DiffView) renderUnified() string {
    // 1. 计算实际要渲染的行范围（受 yOffset 和 height 限制）
    printedLines := -dv.yOffset
    shouldWrite := func() bool { return printedLines >= 0 }

    // 2. 遍历所有 hunk
    for i, h := range dv.unified.Hunks {
        // 第一个 hunk 前渲染文件名字符串头
        if i == 0 && dv.fileName != "" { ... }

        // 渲染 hunk 分隔行: "  @@ -1,5 +1,6 @@ "
        if shouldWrite() { render divider line }
        printedLines++

        // 3. 遍历 hunk 中的每一行
        for j, l := range h.Lines {
            // 高度溢出检测：如果到达 height 且不是最后一行，渲染 "…" 截断符
            if hasReachedHeight && (!isLastHunk || !isLastLine) {
                render "…" truncation line; break outer
            }

            switch l.Kind {
            case udiff.Equal:
                // 两行号都显示，内容两侧留空格 "  content"
            case udiff.Insert:
                // before 行号留空，after 行号显示
                // 前导符号: "+ " 或 "+…"（被截断时）
            case udiff.Delete:
                // before 行号显示，after 行号留空
                // 前导符号: "- " 或 "-…"（被截断时）
            }
            printedLines++
        }
    }
}
```

### 5.3 内容处理管线

每条代码行的内容经过以下处理：

```
getContent(content, bgColor)
 ├─ strings.TrimSuffix(content, "\n")    // 去掉行尾换行
 ├─ hightlightCode(content, bgColor)     // 语法高亮（含缓存）
 ├─ ansi.GraphemeWidth.Cut(content, xOffset, len)  // 水平滚动裁剪
 ├─ ansi.Truncate(content, codeWidth, "…")         // 宽度截断
 └─ return content, leadingEllipsis
```

---

## 6. 分栏布局渲染 (`renderSplit`)

### 6.1 splitHunk 转换

`hunkToSplit()` 将 unified diff 的 `[]Line` 转换为 `splitLine` 配对：

```go
type splitLine struct {
    before *udiff.Line   // 前侧行（nil = 纯新增行）
    after  *udiff.Line   // 后侧行（nil = 纯删除行）
}
```

**转换规则**：
- `Equal` → before 和 after 指向同一个 Line 对象
- `Insert` → before=nil, after 指向该 Line
- `Delete` → before 指向该 Line，然后扫描后续行匹配一个 Insert 配对（匹配到 Equal 则停止扫描）

### 6.2 渲染逻辑

分栏布局左右独立渲染：

```
for each splitLine {
    // ---- 左侧（Before） ----
    switch {
    case l.before == nil:        // 左侧空白 "  "
    case l.before.Kind == Equal: // 行号 + "  content"
    case l.before.Kind == Delete:// before行号 + "- " + content
    }

    // ---- 右侧（After） ----
    switch {
    case l.after == nil:         // 右侧空白 "  "
    case l.after.Kind == Equal:  // 行号 + "  content"
    case l.after.Kind == Insert: // after行号 + "+ " + content
    }

    b.WriteRune('\n')
}
```

**额外列处理**：当剩余宽度为奇数时，`extraColOnAfter = true`，after 侧多 1 个字符宽度。

---

## 7. 样式系统 (`diffview/style.go`)

### 7.1 行样式结构

```go
type LineStyle struct {
    LineNumber lipgloss.Style  // 行号区域样式
    Symbol     lipgloss.Style  // 前导符号 (+ / -) 样式
    Code       lipgloss.Style  // 代码内容样式
}

type Style struct {
    DividerLine LineStyle   // hunk 分隔行 (@@ ... @@)
    MissingLine LineStyle   // 空白行（分栏模式一侧无内容）
    EqualLine   LineStyle   // 未修改行
    InsertLine  LineStyle   // 新增行
    DeleteLine  LineStyle   // 删除行
    Filename    LineStyle   // 文件名字符串头
}
```

### 7.2 深色主题 (DefaultDarkStyle) 配色

| 行类型 | 行号 FG | 行号 BG | 代码 FG | 代码 BG |
|--------|---------|---------|---------|---------|
| DividerLine | charmtone.Smoke | charmtone.Sapphire | charmtone.Smoke | charmtone.Ox |
| MissingLine | — | charmtone.Charcoal | — | charmtone.Charcoal |
| EqualLine | charmtone.Ash | charmtone.Charcoal | charmtone.Salt | charmtone.Pepper |
| InsertLine | charmtone.Turtle | #293229 | charmtone.Salt | #303a30 |
| InsertLine.Symbol | charmtone.Turtle | #303a30 | — | — |
| DeleteLine | charmtone.Cherry | #332929 | charmtone.Salt | #3a3030 |
| DeleteLine.Symbol | charmtone.Cherry | #3a3030 | — | — |
| Filename | charmtone.Smoke | charmtone.Sapphire | charmtone.Smoke | charmtone.Sapphire |

### 7.3 浅色主题 (DefaultLightStyle) 配色

| 行类型 | 行号 FG | 行号 BG | 代码 FG | 代码 BG |
|--------|---------|---------|---------|---------|
| DividerLine | charmtone.Iron | charmtone.Thunder | charmtone.Oyster | charmtone.Anchovy |
| MissingLine | — | charmtone.Ash | — | charmtone.Ash |
| EqualLine | charmtone.Charcoal | charmtone.Ash | charmtone.Pepper | charmtone.Salt |
| InsertLine | charmtone.Turtle | #c8e6c9 | charmtone.Pepper | #e8f5e9 |
| InsertLine.Symbol | charmtone.Turtle | #e8f5e9 | — | — |
| DeleteLine | charmtone.Cherry | #ffcdd2 | charmtone.Pepper | #ffebee |
| DeleteLine.Symbol | charmtone.Cherry | #ffebee | — | — |

### 7.4 adjustStyles() 统一处理

所有 `LineNumber` 样式统一添加：
- `Padding(0, lineNumPadding)` → 左右各 1 空格 padding
- `Align(lipgloss.Right)` → 行号右对齐

---

## 8. 语法高亮 (`hightlightCode`)

### 8.1 Lexer 选择策略

```go
func (dv *DiffView) getChromaLexer() chroma.Lexer {
    // 1. 用文件名匹配 (如 "main.go" → Go lexer)
    l := lexers.Match(dv.before.path)
    if l == nil {
        // 2. 用文件内容分析 (content-based analysis)
        l = lexers.Analyse(dv.before.content)
    }
    if l == nil {
        // 3. 回退到默认 lexer
        l = lexers.Fallback
    }
    return chroma.Coalesce(l)  // 包装为合并 lexer
}
```

### 8.2 缓存机制

使用 `xxh3` 对 `content + bgColor` 生成 hash 作为 cache key：

```go
func (dv *DiffView) createSyntaxCacheKey(source string, bgColor color.Color) string {
    r, g, b, a := bgColor.RGBA()
    colorStr := fmt.Sprintf("%d,%d,%d,%d", r, g, b, a)
    h := xxh3.New()
    h.Write([]byte(source))
    h.Write([]byte(colorStr))
    return fmt.Sprintf("%x", h.Sum(nil))
}
```

### 8.3 Chroma Formatter

自定义 `xchroma.Formatter` 使用 lipgloss 渲染每个 token：

```go
func Formatter(bgColor color.Color, processValue func(string) string) chroma.Formatter {
    return chroma.FormatterFunc(func(w io.Writer, style *chroma.Style, it chroma.Iterator) error {
        for token := it(); token != chroma.EOF; token = it() {
            entry := style.Get(token.Type)
            s := lipgloss.NewStyle().Background(bgColor)
            if entry.Bold == chroma.Yes { s = s.Bold(true) }
            if entry.Underline == chroma.Yes { s = s.Underline(true) }
            if entry.Italic == chroma.Yes { s = s.Italic(true) }
            if entry.Colour.IsSet() { s = s.Foreground(lipgloss.Color(entry.Colour.String())) }
            fmt.Fprint(w, s.Render(processValue(token.Value)))
        }
        return nil
    })
}
```

### 8.4 控制字符转义 (`ansiext.Escape`)

将控制字符 (0x00-0x1F, DEL) 转为 Unicode Control Picture 表示（如 `\x00` → `␀`），防止破坏 UI 渲染。

---

## 9. 聊天集成 (`chat/unified_diff.go`)

### 9.1 Diff 检测

```go
func looksLikeDiff(content string) bool {
    return diffdetect.IsUnifiedDiff(content)
}

// IsUnifiedDiff 判断逻辑:
// 1. 有 "diff --git " + ("--- " 或 "+++ ") → true
// 2. 有 "@@" + ("--- " 或 "+++ ") → true
```

### 9.2 Unified Diff 解析 (`parseUnifiedDiff`)

将 unified diff 文本解析为 `[]parsedDiffFile{path, before, after}`：

**解析规则**：
- `diff --git a/foo.go b/foo.go` → 提取文件名 `foo.go`（去掉 `b/` 前缀）
- `--- a/foo.go` / `+++ b/foo.go` → 提取文件名（去掉 `a/` 或 `b/`，截断 `\t` 后的部分）
- `--- /dev/null` → 新文件，用 `+++` 中的路径
- `-` 开头的行 → 去掉 `-` 写入 before
- `+` 开头的行 → 去掉 `+` 写入 after
- ` ` 开头的行（空格）→ 同时写入 before 和 after（上下文行）

### 9.3 多文件 diff 处理

```go
func toolOutputDiffContentFromUnified(sty *styles.Styles, content string, width int, expanded bool) string {
    files := parseUnifiedDiff(content)
    if len(files) == 0 {
        // 不是 diff，当作普通代码渲染
        return sty.Tool.Body.Render(toolOutputCodeContent(sty, "result.diff", content, ...))
    }

    var blocks []string
    for i, f := range files {
        formatter := common.DiffFormatter(sty).
            Before(f.path, f.before).
            After(f.path, f.after).
            Width(bodyWidth)
        if len(files) > 1 {
            formatter = formatter.FileName(f.path)  // 多文件时显示文件名头
        }
        if width > maxTextWidth {
            formatter = formatter.Split()  // 宽屏自动切换分栏
        }
        formatted := formatter.String()
        // ...
    }

    // 行数截断处理
    maxLines := responseContextHeight  // 默认 10
    if expanded {
        maxLines = len(lines)  // 展开后显示全部
    }
    if len(lines) > maxLines && !expanded {
        combined = lines[:maxLines] + truncationMessage
    }
}
```

---

## 10. DiffFormatter 工厂 (`common/diff.go`)

```go
func DiffFormatter(s *styles.Styles) *diffview.DiffView {
    formatDiff := diffview.New()
    style := chroma.MustNewStyle("crush", s.ChromaTheme())  // 从 markdown 样式构建 chroma 样式
    diff := formatDiff.
        ChromaStyle(style).   // 绑定语法高亮
        Style(s.Diff).        // 绑定 diff 主题样式
        TabWidth(4)           // Tab 宽度固定为 4
    return diff
}
```

### ChromaTheme 构建

从 `Styles.Markdown.CodeBlock.Chroma` 映射到 `chroma.StyleEntries`，覆盖所有标准 token 类型（Text, Keyword, Comment, String, Number 等）。

---

## 11. 工具函数 (`diffview/util.go`)

| 函数 | 作用 |
|------|------|
| `pad(v any, width int)` | 将值格式化为右对齐的固定宽度字符串（用空格填充） |
| `isEven(n int)` / `isOdd(n int)` | 奇偶判断 |
| `btoi(b bool)` | bool → int (true=1, false=0) |
| `ternary[T any](cond bool, t, f T)` | 泛型三元运算符 |

---

## 12. 使用示例

### 12.1 基本用法

```go
output := diffview.New().
    Before("main.go", oldContent).
    After("main.go", newContent).
    String()
```

### 12.2 带样式和语法高亮

```go
dy := diffview.New()
chromaStyle := chroma.MustNewStyle("crush", chromaTheme)
output := diffview.New().
    Before("main.go", beforeContent).
    After("main.go", afterContent).
    FileName("main.go").
    Style(diffview.DefaultDarkStyle()).
    ChromaStyle(chromaStyle).
    TabWidth(4).
    Width(120).
    Height(20).
    LineNumbers(true).
    String()
```

### 12.3 分栏模式

```go
output := diffview.New().
    Before("main.go", beforeContent).
    After("main.go", afterContent).
    Split().
    Style(diffview.DefaultLightStyle()).
    ChromaStyle(chromaStyle).
    Width(160).
    String()
```

### 12.4 带滚动

```go
output := diffview.New().
    Before("main.go", beforeContent).
    After("main.go", afterContent).
    Height(10).
    YOffset(5).     // 向下滚动 5 行
    XOffset(10).    // 向右滚动 10 个图元
    String()
```

---

## 13. 测试策略

项目使用 **golden master** 测试策略：

```go
//go:embed testdata/TestDefault.before
var TestDefaultBefore string

//go:embed testdata/TestDefault.after
var TestDefaultAfter string

func TestDiffView(t *testing.T) {
    dv := diffview.New().
        Before("main.go", TestDefaultBefore).
        After("main.go", TestDefaultAfter).
        Style(diffview.DefaultLightStyle()).
        ChromaStyle(styles.Get("catppuccin-latte"))

    output := dv.String()
    golden.RequireEqual(t, []byte(output))  // 与 golden file 比对
}
```

测试覆盖维度：
- **布局**: Unified / Split
- **行为**: 默认行号、无行号、多 hunk、自定义 contextLines、窄屏、宽屏、无语法高亮
- **主题**: LightMode / DarkMode
- **参数扫描**: Width 1~110、Height 1~20、XOffset 0~20、YOffset 0~16
- **边界**: Tab 替换、行尾换行符问题

---

## 14. 关键设计决策

1. **Builder 模式**：所有配置通过链式方法，灵活且类型安全
2. **Lazy computation**：`String()` 时才真正计算 diff，中间状态可随意修改配置
3. **语法高亮缓存**：`xxh3(content + bgColor)` 避免重复高亮相同内容
4. **Lexer 缓存**：按文件名缓存 lexer 结果，避免每次高亮都匹配
5. **ANSI 感知**：使用 `ansi.GraphemeWidth.Cut` 处理水平滚动，支持 UTF-8 多字节字符
6. **宽度自动检测**：当 `width=0` 时自动计算所有行的最大宽度
7. **高度截断**：到达 height 时渲染 `…` 截断符，保持输出不超过指定高度
8. **Control character 转义**：防止控制字符破坏 ANSI 输出
