// 引导向导的排版（docs/config-trim-onboarding-design.md §6.2 / §6.3）。
//
// 视觉与其它 TUI 表面同族：圆角边框 + 面板顶栏标题 + 底边框键帽，
// 令牌全部取自 common/styles.go，不在这里另起一套配色。
package tui

import (
	"fmt"
	"os"
	"strings"

	"yagent/internal/config"
	"yagent/internal/models"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// setupMinWidth  below this we print a one-liner instead of a broken panel
// (the dialog chrome needs border + padding + at least a few content columns).
const setupMinWidth = 40

func (w *setupWizard) View() tea.View {
	if w.width < setupMinWidth {
		return tea.View{AltScreen: true, Content: fmt.Sprintf(
			"yagent setup: please widen the terminal to at least %d columns (now %d).", setupMinWidth, w.width)}
	}
	c := w.colors()
	body := w.body(c)

	// 错误与提示在面板底部统一占行：只在有内容时才占，否则摘要屏会被顶出可视区。
	var extras []string
	for _, line := range wrapPlain(w.errMsg, w.inner()) {
		extras = append(extras, c.err.Render("  ✗ "+line))
	}
	if w.note != "" {
		extras = append(extras, c.ok.Render("  ✓ "+truncate(w.note, w.inner()-4)))
	}
	if w.detail != "" && w.step == stepWelcome {
		extras = append(extras, c.dim.Render("  ! why setup: "+truncate(w.detail, w.inner()-18)))
	}

	title := fmt.Sprintf("YAGENT SETUP · %s", w.step.title())
	meta := fmt.Sprintf("%d/%d", int(w.step)+1, 7)
	frameWidth := minInt(w.width-2, 96)

	lines := append([]string{setupTop(title, meta, frameWidth, c)}, body...)
	lines = append(lines, extras...)
	lines = append(lines, setupBottom(w.hints(), frameWidth, c))

	// 每行都夹进面板宽度：任何一段动态文本（用户填的 URL、provider 的错误原文）
	// 撑破边框，表现就是整屏错位 —— 这比少看几个字严重。
	content := lipgloss.JoinVertical(lipgloss.Left, w.clipToFrame(lines, frameWidth)...)
	return tea.View{AltScreen: true, Content: content}
}

// inner 是面板正文可用列数（边框 2 + 左右内边距 2）。
func (w *setupWizard) inner() int {
	frame := minInt(w.width-2, 96)
	n := frame - 4
	if n < 16 {
		return 16
	}
	return n
}

// clipToFrame 去掉越过面板宽度的行。用 lipgloss.Width（而非字节/可见字符数）
// 是因为样式串里的 ANSI 序列不占列。
func (w *setupWizard) clipToFrame(lines []string, frameWidth int) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if lipgloss.Width(l) > frameWidth {
			l = clipVisible(l, frameWidth-1) + "…"
		}
		out = append(out, l)
	}
	return out
}

// clipVisible 按显示宽度截断，尽量不切断 ANSI 序列：先数可见列，样式串原样带过。
func clipVisible(s string, width int) string {
	if width <= 0 {
		return ""
	}
	var b strings.Builder
	seen := 0
	inEsc := false
	for _, r := range s {
		if inEsc {
			b.WriteRune(r)
			if r == 'm' {
				inEsc = false
			}
			continue
		}
		if r == '\x1b' {
			b.WriteRune(r)
			inEsc = true
			continue
		}
		if seen+lipgloss.Width(string(r)) > width {
			break
		}
		b.WriteRune(r)
		seen += lipgloss.Width(string(r))
	}
	return b.String()
}

type setupPalette struct {
	border lipgloss.Style
	title  lipgloss.Style
	dim    lipgloss.Style
	hint   lipgloss.Style
	cursor lipgloss.Style
	mark   lipgloss.Style
	ok     lipgloss.Style
	err    lipgloss.Style
	field  lipgloss.Style
	value  lipgloss.Style
}

func (w *setupWizard) colors() setupPalette {
	t := setupBorderStyle()
	return setupPalette{
		border: t,
		title:  lipgloss.NewStyle().Foreground(lipgloss.Color("141")).Bold(true),
		dim:    lipgloss.NewStyle().Foreground(lipgloss.Color("243")),
		hint:   lipgloss.NewStyle().Foreground(lipgloss.Color("250")).Background(lipgloss.Color("235")),
		cursor: lipgloss.NewStyle().Foreground(lipgloss.Color("141")).Bold(true),
		mark:   lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true),
		ok:     lipgloss.NewStyle().Foreground(lipgloss.Color("76")),
		err:    lipgloss.NewStyle().Foreground(lipgloss.Color("196")),
		field:  lipgloss.NewStyle().Foreground(lipgloss.Color("250")),
		value:  lipgloss.NewStyle().Foreground(lipgloss.Color("39")),
	}
}

func (w *setupWizard) body(c setupPalette) []string {
	switch w.step {
	case stepWelcome:
		return w.welcomeBody(c)
	case stepProviders:
		return w.providerListBody(c)
	case stepProviderForm:
		return w.formBody(c)
	case stepProbe:
		return w.probeBody(c)
	case stepDefault:
		return w.defaultBody(c)
	case stepFirstRun:
		return w.firstRunBody(c)
	case stepSummary:
		return w.summaryBody(c)
	}
	return nil
}

func (w *setupWizard) welcomeBody(c setupPalette) []string {
	out := []string{
		"",
		"  Let's point yagent at a model. Three things, then you're in:",
		"",
		"    1. a provider  — base_url + api_key + model (any OpenAI-compatible endpoint)",
		"    2. a default   — which provider the Director uses",
		"    3. five choices — language, approvals, routing, browser, per-call timeout",
		"",
		c.dim.Render(fmt.Sprintf("  config file: %s", w.path)),
		c.dim.Render("  nothing is written until you press enter on the last screen."),
		"",
	}
	if w.reason == config.SetupBroken {
		out = append(out,
			c.err.Render("  your current config failed to load:"),
			c.err.Render("    "+w.detail),
			"",
		)
	}
	return out
}

func (w *setupWizard) providerListBody(c setupPalette) []string {
	inner := w.inner()
	// 列宽按面板分配：名字与 model 优先，base_url 其次，密钥列在太窄时整列消失。
	colName := clampInt(inner/6, 8, 18)
	colModel := clampInt(inner/4, 8, 26)
	colURL := clampInt(inner/5, 6, 22)
	showKey := inner >= 64

	head := fmt.Sprintf("  %-*s %-*s %-*s", colName, "PROVIDER", colModel, "MODEL", colURL, "BASE URL")
	if showKey {
		head += " KEY"
	}
	out := []string{
		"",
		c.dim.Render(head),
		c.dim.Render("  " + strings.Repeat("─", clampInt(inner-2, 8, 88))),
	}
	rows := w.effectiveProviders()
	if len(rows) == 0 {
		return append(out, "", c.dim.Render("  no providers yet — press a to add one"), "")
	}
	for i, r := range rows {
		mark := "  "
		if i == w.cursor {
			mark = c.cursor.Render("> ")
		}
		var flags []string
		if r.draft.Name == w.snap.useProvider {
			flags = append(flags, "default")
		}
		if _, pending := w.pendingFor(r.draft.Name); pending {
			flags = append(flags, "queued")
		}
		flag := ""
		if len(flags) > 0 {
			flag = c.mark.Render(" [" + strings.Join(flags, "|") + "]")
		}
		line := fmt.Sprintf("%s%-*s %-*s %-*s", mark,
			colName, truncate(r.draft.Name, colName),
			colModel, truncate(r.draft.Model, colModel),
			colURL, truncate(r.draft.APIBaseURL, colURL))
		if showKey {
			line += " " + maskKey(r.draft)
		}
		out = append(out, line+flag)
	}
	return append(out, "")
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// formBody 的排版预算：行内提示（"这个字段影响什么"）只在够宽时给，
// 宁可少看一行说明也不能撑破面板 —— 撑破的表现是整屏错位。
func (w *setupWizard) formBody(c setupPalette) []string {
	inner := w.inner()
	showHints := inner >= 72
	labelAt := clampInt(inner/5, 10, 16)

	out := []string{}
	label := "new provider"
	if w.editing != "" {
		label = w.editing
	}
	out = append(out, "", c.dim.Render("  editing: "+truncate(label, inner-12)), "")
	out = append(out, w.formRow(c, 0, "name", w.inputs[0].View(), "A-Z a-z 0-9 _ -", labelAt, showHints, w.fieldIdx == 0))
	hints := []string{
		"",
		"any OpenAI-compatible endpoint",
		"leave empty to use api_key_env",
		"env var name — wins over the literal key",
		"catalog models get a context window automatically",
		"openai | anthropic",
		"low | medium | high | max",
		"engine-level default",
		"TUI context bar only",
	}
	for i, f := range setupFields {
		out = append(out, w.formRow(c, i+1, f.label, w.inputs[i+1].View(), hints[i+1], labelAt, showHints, w.fieldIdx == i+1))
	}
	if cat := w.catalogHint(w.inputs[4].Value()); cat != "" {
		if inner >= 60 {
			out = append(out, "", c.dim.Render("  catalog: "+truncate(cat, inner-12)))
		}
	}
	out = append(out, "")
	return out
}

func (w *setupWizard) formRow(c setupPalette, idx int, label, value, hint string, labelAt int, showHint, active bool) string {
	pointer := "  "
	if active {
		pointer = c.cursor.Render("> ")
	}
	lab := c.field.Render(fmt.Sprintf("%-*s", labelAt, label))
	line := pointer + lab + " " + value
	if showHint && hint != "" {
		line += c.dim.Render("   · " + hint)
	}
	return line
}

// catalogHint 用内嵌模型目录给一行实况反馈：窗口多大、auto-routing 会把它归到哪一档。
// 目录没收录就直说没收录 —— 进度条与压缩阈值都依赖这个数，猜不如说明。
func (w *setupWizard) catalogHint(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	if cw := models.ContextWindow(model); cw > 0 {
		return fmt.Sprintf("%s is known, context = %d", model, cw)
	}
	return fmt.Sprintf("%s is not in the embedded catalog: context_window stays unset and the compression threshold falls back to 120000", model)
}

func (w *setupWizard) probeBody(c setupPalette) []string {
	if w.probing {
		return []string{"", "  contacting the endpoint...", ""}
	}
	out := []string{""}
	// 错误原文可能很长：折行进面板，而不是截成看不懂的半句（校验类错误由 View 统一渲染）。
	for _, line := range wrapPlain(w.probeErr, w.inner()-4) {
		out = append(out, c.err.Render("  ✗ "+line))
	}
	if w.probeErr == "" && w.probeText != "" {
		out = append(out, c.ok.Render("  reply: "+truncate(w.probeText, 60)))
	}
	if w.probeErr == "" && w.probeText == "" && w.errMsg == "" {
		out = append(out, c.dim.Render("  nothing to show yet"))
	}
	out = append(out, "")
	return out
}

func (w *setupWizard) defaultBody(c setupPalette) []string {
	rows := w.effectiveProviders()
	out := []string{"", "  Which provider does the Director use by default?", ""}
	for i, r := range rows {
		mark := "  "
		if i == w.cursor {
			mark = c.cursor.Render("> ")
		}
		now := ""
		if r.draft.Name == w.snap.useProvider {
			now = c.mark.Render(" (current)")
		}
		out = append(out, fmt.Sprintf("%s%s%s", mark, padTo(r.draft.Name, 24), now))
	}
	out = append(out, "")
	return out
}

func (w *setupWizard) firstRunBody(c setupPalette) []string {
	out := []string{"", "  Use ←/→ (or enter) on a row to change it, then c to continue.", ""}
	descriptions := [5]string{
		"language the model answers in",
		"auto-approve dangerous operations",
		"pick fast/balanced/high per task difficulty",
		"Browser-Agent (needs the browser-control CLI + Chrome)",
		"per-call LLM timeout",
	}
	for i, label := range firstRunLabels {
		mark := "  "
		if i == w.cursor {
			mark = c.cursor.Render("> ")
		}
		value := firstRunValueSets[i][w.firstRunIdx[i]]
		out = append(out, fmt.Sprintf("%s%-14s %s", mark, padTo(label, 14), c.value.Render(padTo(value, 22))))
		out = append(out, "  "+c.dim.Render("  "+descriptions[i]))
	}
	out = append(out, "")
	return out
}

func (w *setupWizard) summaryBody(c setupPalette) []string {
	out := []string{"", c.dim.Render("  file: " + w.path), ""}
	if len(w.ops) == 0 {
		out = append(out, c.dim.Render("  no changes queued — enter leaves the file untouched"), "")
		return out
	}
	for _, op := range w.ops {
		out = append(out, "  "+c.value.Render("·")+" "+op.note)
	}
	out = append(out, "")
	return out
}

func (w *setupWizard) hints() string {
	switch w.step {
	case stepWelcome:
		return "enter start · q quit (writes nothing)"
	case stepProviders:
		return "a add · e edit · d delete · t test · c continue · q quit"
	case stepProviderForm:
		return "tab next field · ctrl+s save draft · esc back"
	case stepProbe:
		return "p retry · enter edit provider · any other key back to list"
	case stepDefault:
		return "j/k pick · enter confirm · esc quit"
	case stepFirstRun:
		return "j/k row · h/l change · c continue"
	case stepSummary:
		return "enter write file · esc back · q quit without writing"
	}
	return ""
}

// fieldWidth 给 textarea 一个随面板收缩的宽度：骨架（指针 2 + 标签 + 空格）要占位，
// 下限必须能容一个 URL 的前缀，否则用户看着截断的地方打字。
func (w *setupWizard) fieldWidth() int {
	labelAt := clampInt(w.inner()/5, 10, 16)
	n := w.inner() - 4 - labelAt
	if n < 14 {
		return 14
	}
	if n > 48 {
		return 48
	}
	return n
}

func setupTop(title, meta string, width int, c setupPalette) string {
	inner := width - 4
	label := " " + title
	pad := inner - lipgloss.Width(label) - lipgloss.Width(meta)
	if pad < 1 {
		pad = 1
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color("141")).Render(
		"╭─" + label + strings.Repeat(" ", pad) + meta + "─╮")
}

func setupBottom(hint string, width int, c setupPalette) string {
	inner := width - 4
	if hint != "" {
		hint = " " + hint
	}
	pad := inner - lipgloss.Width(hint)
	if pad < 1 {
		pad = 1
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color("141")).Render(
		"╰─" + hint + strings.Repeat(" ", pad) + "─╯")
}

func setupBorderStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color("62"))
}

func maskKey(d config.ProviderDraft) string {
	if d.APIKeyEnv != "" {
		return "$" + truncate(d.APIKeyEnv, 18)
	}
	if d.APIKey == "" {
		return "—"
	}
	if config.IsPlaceholderKey(d.APIKey) {
		return "your-key (not filled in)"
	}
	if len(d.APIKey) <= 8 {
		return "••••"
	}
	return d.APIKey[:4] + "…" + d.APIKey[len(d.APIKey)-2:]
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

func padTo(s string, n int) string {
	if lipgloss.Width(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-lipgloss.Width(s))
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// wrapPlain 按列宽折行（只折空白，不做样式），用于把 provider 的错误原文放进面板。
func wrapPlain(s string, width int) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		line := ""
		for _, word := range strings.Fields(para) {
			if lipgloss.Width(line)+len(word)+1 > width && line != "" {
				out = append(out, line)
				line = word
				continue
			}
			if line == "" {
				line = word
			} else {
				line += " " + word
			}
		}
		out = append(out, line)
	}
	return out
}

// RunSetup 独立跑一次引导（坏配置启动、`yagent setup` 用）。
// 返回 saved：调用方据此决定要不要重载配置继续正常启动。
func RunSetup(path string, reason config.SetupReason, detail string) (saved bool, err error) {
	w := newSetupWizard(path, reason, detail)
	termWidth, termHeight := 80, 24 // fallback
	if ws, sizeErr := getWindowSize(int(os.Stdout.Fd())); sizeErr == nil {
		termWidth, termHeight = int(ws.Width), int(ws.Height)
	}
	w.width, w.height = termWidth, termHeight

	p := tea.NewProgram(w)
	final, runErr := p.Run()
	if runErr != nil {
		return false, runErr
	}
	if done, ok := final.(*setupWizard); ok {
		return done.Saved(), nil
	}
	return false, nil
}
