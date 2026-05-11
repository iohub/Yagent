package diffview

import (
	"fmt"
	"strings"
	"sync"

	"charm.land/lipgloss/v2"
)

// DiffMode 定义 diff 显示模式
type DiffMode int

const (
	UnifiedMode DiffMode = iota // 传统单行 diff
	SplitMode                   // 左右分屏 diff
)

// Styles 定义 diff 样式
type Styles struct {
	DiffPlus    lipgloss.Style
	DiffMinus   lipgloss.Style
	DiffContext lipgloss.Style
	LineNumber  lipgloss.Style
	DiffSign    lipgloss.Style
	Before      lipgloss.Style
	After       lipgloss.Style
}

// DefaultStyles 返回默认样式
func DefaultStyles() Styles {
	return Styles{
		DiffPlus:    lipgloss.NewStyle().Foreground(lipgloss.Color("42")),
		DiffMinus:   lipgloss.NewStyle().Foreground(lipgloss.Color("203")),
		DiffContext: lipgloss.NewStyle().Foreground(lipgloss.Color("245")),
		LineNumber:  lipgloss.NewStyle().Foreground(lipgloss.Color("241")),
		DiffSign:    lipgloss.NewStyle().Foreground(lipgloss.Color("241")),
		Before:      lipgloss.NewStyle().Foreground(lipgloss.Color("246")),
		After:       lipgloss.NewStyle().Foreground(lipgloss.Color("246")),
	}
}

// diffLine 表示 diff 中的一行
type diffLine struct {
	Type     diffType
	Content  string
	LeftNum  int
	RightNum int
}

type diffType int

const (
	diffContext diffType = iota
	diffPlus
	diffMinus
)

// DiffView 代码差异查看组件（Builder 模式）
type DiffView struct {
	beforeFile   string
	beforeContent string
	afterFile    string
	afterContent string
	mode         DiffMode
	lineNumbers  bool
	contextLines int
	diffLines    []diffLine
	styles       Styles
	computed     bool
	mu           sync.RWMutex
}

// New 创建一个新的 DiffView Builder
func New() *DiffView {
	return &DiffView{
		mode:         UnifiedMode,
		lineNumbers:  true,
		contextLines: 3,
		styles:       DefaultStyles(),
	}
}

// Before 设置 before 文件
func (d *DiffView) Before(file, content string) *DiffView {
	d.beforeFile = file
	d.beforeContent = content
	d.computed = false
	return d
}

// After 设置 after 文件
func (d *DiffView) After(file, content string) *DiffView {
	d.afterFile = file
	d.afterContent = content
	d.computed = false
	return d
}

// SetMode 设置 diff 模式
func (d *DiffView) SetMode(mode DiffMode) *DiffView {
	d.mode = mode
	d.computed = false
	return d
}

// LineNumbers 设置是否显示行号
func (d *DiffView) LineNumbers(show bool) *DiffView {
	d.lineNumbers = show
	d.computed = false
	return d
}

// ContextLines 设置上下文字行数
func (d *DiffView) ContextLines(n int) *DiffView {
	d.contextLines = n
	d.computed = false
	return d
}

// Styles 设置自定义样式
func (d *DiffView) Styles(s Styles) *DiffView {
	d.styles = s
	return d
}

// SetContent 设置 diff 内容并立即计算
func (d *DiffView) SetContent(beforeFile, beforeContent, afterFile, afterContent string) {
	d.beforeFile = beforeFile
	d.beforeContent = beforeContent
	d.afterFile = afterFile
	d.afterContent = afterContent
	d.computed = false
}

// Compute 计算 diff 并生成 diffLines
func (d *DiffView) Compute() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.beforeContent == "" || d.afterContent == "" {
		d.diffLines = []diffLine{}
		d.computed = true
		return nil
	}

	beforeLines := strings.Split(d.beforeContent, "\n")
	afterLines := strings.Split(d.afterContent, "\n")

	// LCS diff
	raw := computeDiffRaw(beforeLines, afterLines)
	d.diffLines = applyContext(raw, d.contextLines)
	d.computed = true
	return nil
}

// computeDiffRaw 用 LCS 算法生成原始 diff
func computeDiffRaw(before, after []string) []rawDiffLine {
	m, n := len(before), len(after)
	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}
	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if before[i-1] == after[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else {
				dp[i][j] = max2(dp[i-1][j], dp[i][j-1])
			}
		}
	}

	var result []rawDiffLine
	i, j := m, n
	for i > 0 || j > 0 {
		if i > 0 && j > 0 && before[i-1] == after[j-1] {
			result = append([]rawDiffLine{{Type: diffContext, Before: before[i-1], After: after[j-1], BLine: i, ALine: j}}, result...)
			i--
			j--
		} else if j > 0 && (i == 0 || dp[i][j-1] >= dp[i-1][j]) {
			result = append([]rawDiffLine{{Type: diffPlus, After: after[j-1], ALine: j}}, result...)
			j--
		} else {
			result = append([]rawDiffLine{{Type: diffMinus, Before: before[i-1], BLine: i}}, result...)
			i--
		}
	}
	return result
}

type rawDiffLine struct {
	Type   diffType
	Before string
	After  string
	BLine  int
	ALine  int
}

func applyContext(lines []rawDiffLine, ctx int) []diffLine {
	if len(lines) <= ctx*2+1 {
		return rawToDiffLines(lines)
	}
	result := make([]diffLine, 0, ctx*2+3)
	result = append(result, rawToDiffLines(lines[:ctx])...)
	result = append(result, diffLine{Type: diffContext, Content: "..."})
	skipEnd := len(lines) - ctx
	if skipEnd < ctx+1 {
		skipEnd = ctx + 1
	}
	result = append(result, diffLine{Type: diffContext, Content: "..."})
	result = append(result, rawToDiffLines(lines[skipEnd:])...)
	return result
}

func rawToDiffLines(lines []rawDiffLine) []diffLine {
	result := make([]diffLine, 0, len(lines))
	for _, l := range lines {
		switch l.Type {
		case diffContext:
			result = append(result, diffLine{Type: diffContext, Content: l.Before, LeftNum: l.BLine, RightNum: l.ALine})
		case diffPlus:
			result = append(result, diffLine{Type: diffPlus, Content: l.After, LeftNum: 0, RightNum: l.ALine})
		case diffMinus:
			result = append(result, diffLine{Type: diffMinus, Content: l.Before, LeftNum: l.BLine, RightNum: 0})
		}
	}
	return result
}

func max2(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// String 输出 diff 渲染字符串
func (d *DiffView) String() string {
	if !d.computed {
		d.Compute()
	}

	d.mu.RLock()
	defer d.mu.RUnlock()

	if len(d.diffLines) == 0 {
		return "(无差异)\n"
	}

	// 计算行号宽度
	numDigits := 0
	for _, line := range d.diffLines {
		if line.LeftNum > 0 {
			d := countDigits(line.LeftNum)
			if d > numDigits {
				numDigits = d
			}
		}
		if line.RightNum > 0 {
			d := countDigits(line.RightNum)
			if d > numDigits {
				numDigits = d
			}
		}
	}

	var b strings.Builder
	for _, line := range d.diffLines {
		b.WriteString(d.formatLine(line, numDigits))
		b.WriteString("\n")
	}

	return b.String()
}

func (d *DiffView) formatLine(line diffLine, numDigits int) string {
	switch line.Type {
	case diffContext:
		return d.formatContext(line, numDigits)
	case diffPlus:
		return d.formatPlus(line, numDigits)
	case diffMinus:
		return d.formatMinus(line, numDigits)
	}
	return ""
}

func (d *DiffView) formatContext(line diffLine, numDigits int) string {
	leftPad := strings.Repeat(" ", numDigits-countDigits(line.LeftNum))
	rightPad := strings.Repeat(" ", numDigits-countDigits(line.RightNum))

	if d.lineNumbers {
		return fmt.Sprintf("%s%s%s %s %s%s",
			d.styles.LineNumber.Render(leftPad+fmt.Sprintf("%d", line.LeftNum)),
			d.styles.DiffContext.Render("│"),
			d.styles.DiffContext.Render(line.Content),
			d.styles.DiffContext.Render(line.Content),
			d.styles.DiffContext.Render("│"),
			d.styles.LineNumber.Render(rightPad+fmt.Sprintf("%d", line.RightNum)),
		)
	}
	return d.styles.DiffContext.Render(line.Content)
}

func (d *DiffView) formatPlus(line diffLine, numDigits int) string {
	rightPad := strings.Repeat(" ", numDigits-countDigits(line.RightNum))
	signPad := strings.Repeat(" ", numDigits)

	if d.lineNumbers {
		return fmt.Sprintf("%s%s%s %s%s%s",
			d.styles.DiffSign.Render(signPad),
			d.styles.DiffPlus.Render("+"),
			d.styles.DiffPlus.Render(line.Content),
			d.styles.DiffPlus.Render(" "),
			d.styles.DiffContext.Render(rightPad+fmt.Sprintf("%d", line.RightNum)),
			d.styles.LineNumber.Render(""),
		)
	}
	return d.styles.DiffPlus.Render("+" + line.Content)
}

func (d *DiffView) formatMinus(line diffLine, numDigits int) string {
	leftPad := strings.Repeat(" ", numDigits-countDigits(line.LeftNum))
	signPad := strings.Repeat(" ", numDigits)

	if d.lineNumbers {
		return fmt.Sprintf("%s%s%s %s%s%s",
			d.styles.LineNumber.Render(leftPad+fmt.Sprintf("%d", line.LeftNum)),
			d.styles.DiffMinus.Render(" "),
			d.styles.DiffMinus.Render(line.Content),
			d.styles.DiffMinus.Render(" "),
			d.styles.DiffSign.Render(signPad),
			d.styles.DiffSign.Render(""),
		)
	}
	return d.styles.DiffMinus.Render("-" + line.Content)
}

func countDigits(n int) int {
	if n <= 0 {
		return 1
	}
	count := 0
	for n > 0 {
		count++
		n /= 10
	}
	return count
}

// View 返回渲染字符串（Component 接口兼容）
func (d *DiffView) View() string {
	return d.String()
}

// IsEmpty 检查是否有 diff 内容
func (d *DiffView) IsEmpty() bool {
	return d.beforeContent == "" && d.afterContent == ""
}

// ToggleMode 切换 Split/Unified 模式
func (d *DiffView) ToggleMode() {
	if d.mode == UnifiedMode {
		d.mode = SplitMode
	} else {
		d.mode = UnifiedMode
	}
	d.computed = false
}

// GetMode 返回当前模式
func (d *DiffView) GetMode() DiffMode {
	return d.mode
}

// GetDiffLines 返回 diff 行（用于测试）
func (d *DiffView) GetDiffLines() []diffLine {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.diffLines
}
