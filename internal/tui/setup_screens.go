// 引导向导的七屏正文（docs/setup-wizard-ui-design.md §3、§4）。
//
// 排版规则只有一条：所有宽度从 setupLayout 取，所有截断/列宽/滚动走 common 那份内核。
// 屏幕自己算宽度 = 第二份实现 = 迟早漂走的那一份。
package tui

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	"yagent/internal/config"
	"yagent/internal/models"
	"yagent/internal/tui/common"

	"charm.land/lipgloss/v2"
)

func (w *setupWizard) body(c setupColors, L setupLayout) []string {
	switch w.step {
	case stepWelcome:
		return w.welcomeBody(c, L)
	case stepProviders:
		return w.providerListBody(c, L)
	case stepProviderForm:
		return w.formBody(c, L)
	case stepProbe:
		return w.probeBody(c, L)
	case stepDefault:
		return w.defaultBody(c, L)
	case stepFirstRun:
		return w.firstRunBody(c, L)
	case stepSummary:
		return w.summaryBody(c, L)
	}
	return nil
}

// ── 通用小件 ──

// spinnerFrames 只在探测屏用；帧表是自己的一份（决策 DD：不接 internal/tui/anim，
// 那个包全仓零引用）。
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func (w *setupWizard) para(c setupColors, s string, width int, style lipgloss.Style) []string {
	var out []string
	for _, line := range wrapPlain(s, width) {
		out = append(out, style.Render(line))
	}
	return out
}

// screenHead 是一屏的标题行：粗体主张 + 斜体副标题（同一行放不下时副标题下沉）。
// 两侧都过 wrapPlain：让 lipgloss 去折会让渲染行数比计数多，高度预算当场失效。
func (w *setupWizard) screenHead(c setupColors, title, sub string, L setupLayout) []string {
	bold := lipgloss.NewStyle().Foreground(c.TextPrimary).Bold(true)
	muted := lipgloss.NewStyle().Foreground(c.TextMuted).Italic(true)
	if lipgloss.Width(title)+2+lipgloss.Width(sub) <= L.contentW {
		return []string{bold.Render(title) + "  " + muted.Render(sub)}
	}
	out := wrapPlain(title, L.contentW)
	var styled []string
	for _, l := range out {
		styled = append(styled, bold.Render(l))
	}
	for _, l := range wrapPlain(sub, L.contentW) {
		styled = append(styled, muted.Render(l))
	}
	return styled
}

func badge(c setupColors, text string, bg color.Color) string {
	return common.BadgeStyle(c.ColorTokens, bg).Render(text)
}

// rowBadge 只给一枚徽章，优先级"这行现在不能直接用" > draft > default：
// 两枚并排会把列推开，而首启最怕的恰恰是模板里那个 your-key 被当成已经配好了。
func rowBadge(c setupColors, issue string, queued, isDefault bool) string {
	switch {
	case issue != "":
		return badge(c, issue, c.Danger)
	case queued:
		return badge(c, "draft", c.Accent)
	case isDefault:
		return badge(c, "default", c.Success)
	}
	return ""
}

// providerIssue 说的是"填了也不能用"：没有来源，或来源还是模板的占位符。
func providerIssue(d config.ProviderDraft) string {
	switch {
	case d.APIKey == "" && d.APIKeyEnv == "":
		return "no key"
	case d.APIKey != "" && config.IsPlaceholderKey(d.APIKey):
		return "fix key"
	}
	return ""
}

// ── 1 welcome ──

func (w *setupWizard) welcomeBody(c setupColors, L setupLayout) []string {
	muted := lipgloss.NewStyle().Foreground(c.TextMuted)
	dim := lipgloss.NewStyle().Foreground(c.TextMuted).Italic(true)
	num := lipgloss.NewStyle().Foreground(c.Primary).Bold(true)

	out := w.screenHead(c, "Let's point yagent at a model.", "four screens, nothing written until the last one", L)
	out = append(out, "")

	steps := []struct{ n, what string }{
		{"1", "Provider — base_url + key + model. Any OpenAI-compatible endpoint works; ctrl+p picks a known gateway."},
		{"2", "Default — which provider the Director talks to."},
		{"3", "Five choices — language, approvals, routing, browser, per-call timeout."},
		{"4", "Review — the exact edits, key by key. enter writes them."},
	}
	for _, s := range steps {
		head := num.Render(s.n + " ")
		// 预算 = 内容宽 - 前导两空格 - 编号那一列：少算一项就被 fitLines 剪成 "timeo…"。
		lines := wrapPlain(s.what, L.contentW-4)
		if len(lines) == 0 {
			continue
		}
		out = append(out, "  "+head+lines[0])
		for _, extra := range lines[1:] {
			out = append(out, "   "+muted.Render(extra))
		}
	}
	out = append(out, "", sectionRule(c, L.contentW, ""))
	out = append(out, "  "+muted.Render("config  "+truncate(w.path, max(L.contentW-10, 12))))
	out = append(out, "  "+dim.Render("q quits and leaves the file byte-identical"))
	if w.reason == config.SetupBroken {
		out = append(out, "", "  "+lipgloss.NewStyle().Foreground(c.Danger).Render("current config failed to load"))
	}
	return out
}

// ── 2 provider 列表 ──

// providerCols 是列表屏的列计划。BASE URL 是第一个被牺牲的列，KEY 是最后一个 ——
// "这家有没有密钥"正是首启要看的那件事（旧版在 inner<64 时整列丢掉它）。
type providerCols struct {
	idx, name, model, url, key int
	badge                      bool
}

// badgeCell 必须容得下徽章本身：BadgeStyle 有 Padding(0,1)，所以 "fix key"（7 列）
// 实际占 9 列。按 8 预留的表现是 Width(8) 把徽章折成两行，整行跟着溢出卡片。
const badgeCell = 10

func providerColumns(cw int) providerCols {
	cols := providerCols{idx: 2, name: clampInt(cw/5, 8, 16), key: 2}
	if cw >= 46 {
		cols.model = clampInt(cw/5, 8, 18)
	}
	if cw >= 66 {
		cols.key = clampInt(cw/6, 10, 20)
	}
	cols.badge = cw >= 46

	// 分隔符数量必须等于真正拼出来的列数：providerRow 用单个空格连接
	// marker / name / model / key / url / badge。按"每列前一个空格"算会少算一个
	// （首列前面没有空格），表现就是整行比卡片内宽多 1~2 列，被 lipgloss 再折一行。
	fixed := cols.idx + cols.name + cols.key
	cells := 3
	if cols.model > 0 {
		fixed += cols.model
		cells++
	}
	if cols.badge {
		fixed += badgeCell
		cells++
	}
	// BASE URL 是唯一可牺牲的列：它最长，而"这家有没有密钥"不能丢。
	// cells 里含它自己那一个分隔符，剩下的宽度正好归它。
	if rest := cw - fixed - cells; rest >= 10 {
		cols.url = rest
	}
	return cols
}

func (w *setupWizard) providerListBody(c setupColors, L setupLayout) []string {
	rows := w.effectiveProviders()
	out := w.screenHead(c, "Providers", "a adds one · t proves it answers", L)
	if len(rows) == 0 {
		card := cardStyle(c, L.contentW).Render(strings.Join([]string{
			lipgloss.NewStyle().Foreground(c.TextSecondary).Bold(true).Render("No provider yet"),
			lipgloss.NewStyle().Foreground(c.TextMuted).Render(
				"press " + keycapHint(c, "a") + " — a name, a URL, a key is all it takes"),
		}, "\n"))
		return append(out, "", card)
	}

	cw := cardInner(L.contentW)
	cols := providerColumns(cw)
	header := w.providerHeader(c, cols)
	visible, _ := common.ListBudget(0, max(L.rows-4, 1), max(L.rows-4, 1), len(rows))
	start, end, moreUp, moreDown := listWindow(len(rows), w.cursor, visible)

	lines := []string{header, sectionRule(c, cw, "")}
	for i := start; i < end; i++ {
		lines = append(lines, w.providerRow(c, cols, rows[i], i))
	}
	if moreUp > 0 {
		lines = append(lines, common.MoreLine(moreUp, true, cw, c.ColorTokens))
	}
	if moreDown > 0 {
		lines = append(lines, common.MoreLine(moreDown, false, cw, c.ColorTokens))
	}
	out = append(out, "", cardStyle(c, L.contentW).Render(strings.Join(lines, "\n")))
	if def := w.currentDefault(); def != "" {
		out = append(out, "  "+lipgloss.NewStyle().Foreground(c.TextMuted).Italic(true).Render(
			"default "+def))
	}
	return out
}

func (w *setupWizard) providerHeader(c setupColors, cols providerCols) string {
	muted := lipgloss.NewStyle().Foreground(c.TextMuted)
	parts := []string{muted.Render(common.PadLeft("", cols.idx))}
	push := func(label string, width int) {
		if width <= 0 {
			return
		}
		parts = append(parts, muted.Render(common.Ellipsize(label, width)))
	}
	push("PROVIDER", cols.name)
	push("MODEL", cols.model)
	push(keyLabel(cols.key), cols.key)
	push("BASE URL", cols.url)
	if cols.badge {
		parts = append(parts, muted.Render(common.Ellipsize("", badgeCell)))
	}
	return strings.Join(parts, " ")
}

func keyLabel(width int) string {
	if width > 2 {
		return "KEY"
	}
	return "·"
}

func (w *setupWizard) providerRow(c setupColors, cols providerCols, r providerRow, i int) string {
	focused := i == w.cursor
	isDefault := r.draft.Name == w.currentDefault()
	_, queued := w.pendingFor(r.draft.Name)
	issue := providerIssue(r.draft)

	marker := strings.Repeat(" ", 2)
	if focused {
		marker = common.CursorIndicatorStyle(c.ColorTokens).Render("▶ ")
	}
	nameColor := c.TextSecondary
	switch {
	case focused:
		nameColor = c.TextPrimary
	case isDefault:
		nameColor = c.Success
	}
	nameStyle := lipgloss.NewStyle().Foreground(nameColor).Bold(focused || isDefault)

	parts := []string{marker, nameStyle.Render(common.Ellipsize(r.draft.Name, cols.name))}
	if cols.model > 0 {
		style := lipgloss.NewStyle().Foreground(c.TextMuted)
		if focused {
			style = style.Foreground(c.TextSecondary)
		}
		parts = append(parts, style.Render(common.Ellipsize(r.draft.Model, cols.model)))
	}
	parts = append(parts, lipgloss.NewStyle().Foreground(keyColor(c, issue)).
		Render(common.Ellipsize(w.keyCell(r, cols.key), cols.key)))
	if cols.url > 0 {
		parts = append(parts, lipgloss.NewStyle().Foreground(c.TextMuted).
			Render(common.Ellipsize(r.draft.APIBaseURL, cols.url)))
	}
	if cols.badge {
		parts = append(parts, lipgloss.NewStyle().Width(badgeCell).
			Render(rowBadge(c, issue, queued, isDefault)))
	}
	return strings.Join(parts, " ")
}

func keyColor(c setupColors, issue string) color.Color {
	if issue != "" {
		return c.Danger
	}
	return c.Success
}

func (w *setupWizard) keyCell(r providerRow, width int) string {
	if width <= 2 {
		switch {
		case r.draft.APIKeyEnv != "":
			return "$"
		case r.draft.APIKey != "" && !config.IsPlaceholderKey(r.draft.APIKey):
			return "✓"
		default:
			return "✗"
		}
	}
	return maskKey(r.draft)
}

// ── 3 provider 表单 ──

func (w *setupWizard) labelColWidth() int {
	n := 0
	for _, r := range formRows {
		if v := lipgloss.Width(w.rowLabel(formRowIndexByKey(r.key))); v > n {
			n = v
		}
	}
	return clampInt(n, 10, 16)
}

// fieldWidth 给 textarea 一个随面板收缩的宽度。骨架（标签列 + 1 空格 + `❯ ` 前缀 2 列）
// 都要占位：少算一项的表现是整行越过面板，被 clipToFrame 剪出半个省略号。
func (w *setupWizard) fieldWidth() int {
	L := w.layout(0)
	n := L.contentW - w.labelColWidth() - 3
	return clampInt(n, 12, 52)
}

// showHints 的阈值：提示是"这一格改了会怎样"，宁可整列不见也不能截成半句。
func (w *setupWizard) formBody(c setupColors, L setupLayout) []string {
	if w.presetOpen {
		return w.presetBody(c, L)
	}
	out := w.screenHead(c, w.formTitle(), w.formSub(), L)
	out = append(out, "")

	firstOptional := formRowIndexByKey("api_format")
	for _, i := range w.visibleFormRows() {
		if i == firstOptional && firstOptional >= 0 {
			out = append(out, "", sectionRule(c, L.contentW, "optional"))
		}
		out = append(out, w.formRowView(c, i, L)...)
	}
	if !w.optionalOpen {
		hidden := len(formRows) - len(w.visibleFormRows())
		out = append(out, "", lipgloss.NewStyle().Foreground(c.TextMuted).Italic(true).Render(
			fmt.Sprintf("  %d optional field%s hidden — %s opens them", hidden, plural(hidden), keycapHint(c, "ctrl+o"))))
	}
	return out
}

func (w *setupWizard) formTitle() string {
	if w.editing == "" {
		return "New provider"
	}
	return "Editing " + w.editing
}

func (w *setupWizard) formSub() string {
	req := 0
	opt := 0
	for _, r := range formRows {
		if r.group == groupRequired {
			req++
		} else {
			opt++
		}
	}
	return fmt.Sprintf("%d required · %d optional", req, opt)
}

// formRowView 一行两态：字段本身，加上（仅焦点行）一句"这一格改了会怎样"。
// 八行都写着就成了说明书，而用户此刻只关心手里这格。
func (w *setupWizard) formRowView(c setupColors, i int, L setupLayout) []string {
	active := i == w.fieldIdx
	labelStyle := lipgloss.NewStyle().Foreground(c.TextMuted)
	if active {
		labelStyle = lipgloss.NewStyle().Foreground(c.TextPrimary).Bold(true)
	}
	label := labelStyle.Render(padTo(w.rowLabel(i), w.labelColWidth()))

	var field string
	switch {
	case formRows[i].secret && !active:
		field = w.staticField(c, maskSecret(w.inputs[i].Value()))
	default:
		w.inputs[i].SetWidth(w.fieldWidth())
		field = w.inputs[i].View()
	}
	out := []string{"  " + label + " " + field}
	if !active {
		return out
	}
	hint := w.rowHint(i)
	if hint == "" {
		return out
	}
	// 焦点行的说明：缩进到字段那一列，读起来像这格的旁注而不是新的一行内容。
	indent := strings.Repeat(" ", 2+w.labelColWidth()+1)
	return append(out, indent+lipgloss.NewStyle().Foreground(c.TextMuted).Italic(true).
		Render(truncate(hint, max(L.contentW-lipgloss.Width(indent), 8))))
}

// staticField 用与 textarea 相同的底色画一个"不可编辑的那一格"，
// 这样遮罩态与焦点态之间只有文字变化，没有第二套视觉。
func (w *setupWizard) staticField(c setupColors, text string) string {
	return lipgloss.NewStyle().
		Background(lipgloss.Color("235")).
		Foreground(c.TextMuted).
		Width(w.fieldWidth()).
		Render("  " + text)
}

func maskSecret(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if len(s) <= 6 {
		return strings.Repeat("•", len(s))
	}
	return s[:3] + strings.Repeat("•", 6) + s[len(s)-2:]
}

func (w *setupWizard) presetBody(c setupColors, L setupLayout) []string {
	out := w.screenHead(c, "Endpoint presets", "only empty fields get filled", L)
	cw := cardInner(L.contentW)
	visible, _ := common.ListBudget(0, max(L.rows-4, 1), max(L.rows-4, 1), len(endpointPresets))
	start, end, moreUp, moreDown := listWindow(len(endpointPresets), w.cursor, visible)

	var lines []string
	for i := start; i < end; i++ {
		p := endpointPresets[i]
		marker := "  "
		nameStyle := lipgloss.NewStyle().Foreground(c.TextSecondary)
		if i == w.cursor {
			marker = common.CursorIndicatorStyle(c.ColorTokens).Render("▶ ")
			nameStyle = nameStyle.Foreground(c.TextPrimary).Bold(true)
		}
		idx := " "
		if i < 9 {
			idx = fmt.Sprintf("%d", i+1)
		}
		// 游标 2 + 序号 2 + 名称 + 1 + URL = cw，一列都不能多：
		// 超出卡片内宽的行会被 lipgloss 再折一次，行数预算当场失效。
		nameW := clampInt(cw*2/5, 14, 30)
		urlW := cw - 2 - 2 - nameW - 1
		row := marker + lipgloss.NewStyle().Foreground(c.TextMuted).Render(idx+" ") +
			nameStyle.Render(common.Ellipsize(p.Name, nameW)) + " " +
			lipgloss.NewStyle().Foreground(c.PrimaryDim).Render(common.Ellipsize(p.BaseURL, max(urlW, 8)))
		lines = append(lines, row)
		if p.Note != "" && cw >= 56 {
			lines = append(lines, "      "+lipgloss.NewStyle().Foreground(c.TextMuted).Italic(true).
				Render(truncate(p.Note, cw-6)))
		}
	}
	if moreUp > 0 {
		lines = append(lines, common.MoreLine(moreUp, true, cw, c.ColorTokens))
	}
	if moreDown > 0 {
		lines = append(lines, common.MoreLine(moreDown, false, cw, c.ColorTokens))
	}
	return append(out, "", cardStyle(c, L.contentW).Render(strings.Join(lines, "\n")))
}

// catalogHint 用内嵌模型目录给一行实况反馈：窗口多大、没收录会怎样。
// 目录没收录就直说没收录 —— 进度条与压缩阈值都依赖这个数，猜不如说明。
func (w *setupWizard) catalogHint(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return "type a model id — catalog hits prefill context_window"
	}
	if cw := models.ContextWindow(model); cw > 0 {
		return fmt.Sprintf("catalog ✓ %s · context %s", model, common.ContextLabel(cw))
	}
	return fmt.Sprintf("catalog ✗ %s is not in the embedded catalog: context_window stays unset "+
		"and the compression threshold falls back to 120000", model)
}

// ── 4 连通性探测 ──

func (w *setupWizard) probeBody(c setupColors, L setupLayout) []string {
	if w.probing {
		frame := spinnerFrames[w.spinFrame%len(spinnerFrames)]
		head := lipgloss.NewStyle().Foreground(c.Success).Bold(true).Render(frame+" contacting ") +
			lipgloss.NewStyle().Foreground(c.TextPrimary).Render(truncate(probeHost(w.probeTarget), 40))
		muted := lipgloss.NewStyle().Foreground(c.TextMuted)
		out := []string{head, "",
			muted.Render(fmt.Sprintf("model %s · %s elapsed · %s budget · max_tokens 16, no tools",
				truncate(w.probeModel, 28), w.elapsed(), probeTimeout))}
		out = append(out, "", lipgloss.NewStyle().Foreground(c.TextMuted).Italic(true).Render(
			"waiting for a real round-trip — nothing here is simulated"))
		return out
	}
	if w.probeErr != "" {
		// 原文逐字（§6.2 要求"不做美化"），归类行只是追加的猜测，认不出就不给。
		head := lipgloss.NewStyle().Foreground(c.Danger).Bold(true).Render("× no answer")
		body := wrapPlain(w.probeErr, L.contentW-2)
		lines := append([]string{head, ""}, body...)
		if advice := probeAdvice(w.probeErr); advice != "" {
			// 归类行同样折进宽度：截成 "with…" 的建议没法照着做。
			for _, a := range wrapPlain(advice, max(L.contentW-3, 12)) {
				lines = append(lines, lipgloss.NewStyle().Foreground(c.Accent).Italic(true).Render("→ "+a))
			}
		}
		return lines
	}
	if w.probeText != "" {
		head := lipgloss.NewStyle().Foreground(c.Success).Bold(true).Render("✓ answered")
		meta := lipgloss.NewStyle().Foreground(c.TextMuted).Render(
			fmt.Sprintf("%s · %s · %s", w.probeLatency.Round(time.Millisecond), truncate(w.probeModel, 24), probeHost(w.probeTarget)))
		card := cardStyle(c, L.contentW).Render(
			lipgloss.NewStyle().Foreground(c.TextMuted).Italic(true).Render("reply") + "\n" +
				lipgloss.NewStyle().Foreground(c.TextPrimary).Render(truncate(w.probeText, cardInner(L.contentW))))
		return []string{head + "  " + meta, "", card}
	}
	return []string{"", lipgloss.NewStyle().Foreground(c.TextMuted).Render("nothing to show yet — press " + keycapHint(c, "p"))}
}

func (w *setupWizard) elapsed() string {
	if w.probeStart.IsZero() {
		return "0s"
	}
	return time.Since(w.probeStart).Round(time.Second).String()
}

// probeAdvice 只做四件事：把最常见的三种失败翻译成一个下一步。
// 认不出来就闭嘴 —— 错误的建议比没有建议更贵。
func probeAdvice(errText string) string {
	low := strings.ToLower(errText)
	switch {
	case strings.Contains(low, "401"), strings.Contains(low, "403"),
		strings.Contains(low, "invalid api key"), strings.Contains(low, "unauthorized"):
		return "401/403 is almost always the key, not the URL — re-check this row with f2"
	case strings.Contains(low, "404"), strings.Contains(low, "not found"):
		return "404 usually means the base_url path is wrong (many gateways want a trailing /v1)"
	case strings.Contains(low, "deadline"), strings.Contains(low, "timeout"), strings.Contains(low, "timed out"):
		return "no answer in time — check the endpoint is reachable from this machine"
	case strings.Contains(low, "no such host"), strings.Contains(low, "dial tcp"):
		return "the host did not answer — check base_url and your network"
	}
	return ""
}

// ── 5 默认档 ──

func (w *setupWizard) defaultBody(c setupColors, L setupLayout) []string {
	rows := w.effectiveProviders()
	out := w.screenHead(c, "Which provider does the Director use by default?",
		"[global.llm] use_provider", L)
	if len(rows) == 0 {
		return append(out, "", lipgloss.NewStyle().Foreground(c.TextMuted).Render(
			"no provider yet — go back and press "+keycapHint(c, "a")))
	}
	cw := cardInner(L.contentW)
	nameW := clampInt(cw/3, 10, 24)
	visible, _ := common.ListBudget(0, max(L.rows-4, 1), max(L.rows-4, 1), len(rows))
	start, end, moreUp, moreDown := listWindow(len(rows), w.cursor, visible)

	def := w.currentDefault()
	var lines []string
	for i := start; i < end; i++ {
		r := rows[i]
		focused := i == w.cursor
		marker, style := "○ ", lipgloss.NewStyle().Foreground(c.TextMuted)
		switch {
		case focused:
			marker, style = common.CursorIndicatorStyle(c.ColorTokens).Render("▶ "),
				lipgloss.NewStyle().Foreground(c.TextPrimary).Bold(true)
		case r.draft.Name == def:
			marker = lipgloss.NewStyle().Foreground(c.Success).Render("● ")
			style = lipgloss.NewStyle().Foreground(c.Success)
		}
		line := "  " + marker + style.Render(common.Ellipsize(r.draft.Name, nameW)) + " " +
			lipgloss.NewStyle().Foreground(c.TextMuted).Render(common.Ellipsize(r.draft.Model, max(cw-nameW-4, 8)))
		if r.draft.Name == def {
			line += " " + lipgloss.NewStyle().Foreground(c.TextMuted).Italic(true).Render("now")
		}
		lines = append(lines, line)
	}
	if moreUp > 0 {
		lines = append(lines, common.MoreLine(moreUp, true, cw, c.ColorTokens))
	}
	if moreDown > 0 {
		lines = append(lines, common.MoreLine(moreDown, false, cw, c.ColorTokens))
	}
	return append(out, "", cardStyle(c, L.contentW).Render(strings.Join(lines, "\n")))
}

// ── 6 首启五项 ──

func (w *setupWizard) firstRunBody(c setupColors, L setupLayout) []string {
	out := w.screenHead(c, "First-run choices", "h/l cycles a row · nothing is written yet", L)
	cw := cardInner(L.contentW)
	labelW := clampInt(cw/5, 10, 16)
	valueW := clampInt(cw/4, 10, 22)
	showDesc := cw >= 72

	currents := w.firstRunCurrents()
	var lines []string
	for i, label := range firstRunLabels {
		focused := i == w.cursor
		value := firstRunValueSets[i][w.firstRunIdx[i]]
		changed := value != currents[i]

		marker := "  "
		if focused {
			marker = common.CursorIndicatorStyle(c.ColorTokens).Render("▶ ")
		} else if changed {
			marker = lipgloss.NewStyle().Foreground(c.Accent).Render("~ ")
		}
		labelStyle := lipgloss.NewStyle().Foreground(c.TextSecondary)
		if focused {
			labelStyle = labelStyle.Foreground(c.TextPrimary).Bold(true)
		}
		stepper := lipgloss.NewStyle().Foreground(c.PrimaryDim).Render("‹ ") +
			lipgloss.NewStyle().Foreground(valueColor(c, focused, changed)).Bold(focused).
				Render(common.Ellipsize(value, valueW)) +
			lipgloss.NewStyle().Foreground(c.PrimaryDim).Render(" ›")
		line := marker + labelStyle.Render(common.Ellipsize(label, labelW)) + " " + stepper
		if showDesc {
			line += "   " + lipgloss.NewStyle().Foreground(c.TextMuted).Italic(true).
				Render(truncate(w.firstRunDescription(i), max(cw-labelW-valueW-10, 8)))
		}
		lines = append(lines, line)
		if !showDesc {
			lines = append(lines, "      "+lipgloss.NewStyle().Foreground(c.TextMuted).Italic(true).
				Render(truncate(w.firstRunDescription(i), max(cw-6, 8))))
		}
	}
	return append(out, "", cardStyle(c, L.contentW).Render(strings.Join(lines, "\n")))
}

func valueColor(c setupColors, focused, changed bool) color.Color {
	switch {
	case changed:
		return c.Accent
	case focused:
		return c.TextPrimary
	default:
		return c.TextSecondary
	}
}

// firstRunDescription 里 autonomy 那一行按当前档位换措辞：
// full yolo 会让工作区之外的一切确认消失，这句话不该只在提示栏里写一次。
func (w *setupWizard) firstRunDescription(i int) string {
	if i == 1 && w.firstRunIdx[1] == 2 {
		return "⚠ every prompt is removed, including paths outside the workspace"
	}
	return firstRunDescriptions[i]
}

// ── 7 摘要 ──

type summaryLine struct {
	sign   string
	color  color.Color
	what   string
	detail string
	note   string // op.note 原文：写失败时报的就是它，人话行不能替代它
	// group 为真表示这是一节的小标题（PROVIDERS / DEFAULT / CHOICES），不带条目语义。
	group bool
}

func (w *setupWizard) summaryBody(c setupColors, L setupLayout) []string {
	out := w.screenHead(c, "Review", "enter writes the file · esc goes back", L)
	if len(w.ops) == 0 {
		card := cardStyle(c, L.contentW).Render(strings.Join([]string{
			lipgloss.NewStyle().Foreground(c.TextSecondary).Render("No changes queued"),
			lipgloss.NewStyle().Foreground(c.TextMuted).Render(
				"enter leaves " + truncate(w.path, cardInner(L.contentW)-24) + " untouched"),
		}, "\n"))
		return append(out, "", card)
	}

	lines := w.summaryLines(c)
	cw := cardInner(L.contentW)
	visible, _ := common.ListBudget(0, max(L.rows-4, 1), max(L.rows-4, 1), len(lines))
	if w.summaryTop > len(lines)-visible {
		w.summaryTop = max(len(lines)-visible, 0)
	}
	var rendered []string
	for i := w.summaryTop; i < min(w.summaryTop+visible, len(lines)); i++ {
		rendered = append(rendered, lines[i].render(c, cw))
	}
	if w.summaryTop > 0 {
		rendered = append(rendered, common.MoreLine(w.summaryTop, true, cw, c.ColorTokens))
	}
	if rest := len(lines) - (w.summaryTop + visible); rest > 0 {
		rendered = append(rendered, common.MoreLine(rest, false, cw, c.ColorTokens))
	}
	out = append(out, "", cardStyle(c, L.contentW).Render(strings.Join(rendered, "\n")))
	out = append(out, "  "+lipgloss.NewStyle().Foreground(c.TextMuted).Italic(true).Render(
		fmt.Sprintf("%d change%s · restart yagent to load them", len(w.ops), plural(len(w.ops)))))
	return out
}

func (s summaryLine) render(c setupColors, cw int) string {
	if s.group {
		return sectionRule(c, cw, s.what)
	}
	sign := lipgloss.NewStyle().Foreground(s.color).Bold(true).Render(s.sign)
	what := lipgloss.NewStyle().Foreground(c.TextPrimary).Render(common.Ellipsize(s.what, clampInt(cw/4, 10, 18)))
	detail := s.detail
	if lipgloss.Width(what)+lipgloss.Width(detail)+4 > cw {
		detail = truncate(detail, max(cw-lipgloss.Width(what)-4, 8))
	}
	line := sign + " " + what + " " + lipgloss.NewStyle().Foreground(c.TextSecondary).Render(detail)
	if s.note == "" {
		return line
	}
	// 人话行给用户看，note 行给用户和日志对账 —— 两者不可互相替代。
	return line + "\n      " + lipgloss.NewStyle().Foreground(c.TextMuted).Italic(true).
		Render(truncate(s.note, cw-6))
}

// summaryLines 把 op 队列翻译成人话 + old → new。判据全部来自账本，
// 与 applyOps 真正会写的东西一一对应（多写一行、少写一行都是骗）。
func (w *setupWizard) summaryLines(c setupColors) []summaryLine {
	var out []summaryLine
	add := func(group string) {
		out = append(out, summaryLine{group: true, color: c.Primary, what: group})
	}
	grouped := map[string]bool{}
	for _, op := range w.ops {
		switch op.kind {
		case "write":
			if !grouped["providers"] {
				add("PROVIDERS")
				grouped["providers"] = true
			}
			d := op.provider
			out = append(out, summaryLine{
				sign:  "+",
				color: c.Success,
				what:  d.Name,
				detail: strings.TrimSpace(fmt.Sprintf("%s · %s · %s", d.Model, probeHost(d.APIBaseURL),
					secretSummary(d))),
				note: op.note,
			})
		case "delete":
			if !grouped["providers"] {
				add("PROVIDERS")
				grouped["providers"] = true
			}
			old := ""
			for _, r := range w.snap.providers {
				if r.draft.Name == op.name {
					old = r.draft.Model
				}
			}
			out = append(out, summaryLine{sign: "−", color: c.Danger, what: op.name,
				detail: "(was: " + old + ")", note: op.note})
		case "default":
			if !grouped["default"] {
				add("DEFAULT")
				grouped["default"] = true
			}
			detail, sign, tint := orNone(w.snap.useProvider)+" → "+op.name, "→", c.Primary
			if op.name == w.snap.useProvider {
				detail, sign, tint = op.name+" (unchanged)", "=", c.TextMuted
			}
			out = append(out, summaryLine{sign: sign, color: tint, what: "use_provider",
				detail: detail, note: op.note})
		case "firstrun":
			if !grouped["choices"] {
				add("CHOICES")
				grouped["choices"] = true
			}
			out = append(out, w.firstRunSummaryLines(c, op.firstRun, op.note)...)
		}
	}
	return out
}

func orNone(s string) string {
	if s == "" {
		return "(unset)"
	}
	return s
}

func secretSummary(d config.ProviderDraft) string {
	if d.APIKeyEnv != "" {
		return "$" + d.APIKeyEnv
	}
	if d.APIKey == "" {
		return "no key"
	}
	if config.IsPlaceholderKey(d.APIKey) {
		return "placeholder key"
	}
	return maskSecret(d.APIKey)
}

// firstRunSummaryLines 五行各一条。三件事必须说清：改了什么（→ old → new）、
// 没改什么（= x (unchanged)），以及 Lang 为空时 WriteFirstRun 根本不写那一行
// （config/setup.go:282）—— 那里显示"不变"而不是假装归零。
func (w *setupWizard) firstRunSummaryLines(c setupColors, d config.FirstRunDraft, note string) []summaryLine {
	currents := w.firstRunCurrents()
	// note 只在组内第一条"真的有账"的行上出现一次：五行共用一条 "write first-run choices"，
	// 重复五遍只是把对账行变成噪音（摘要那头的测试仍然要求它出现）。
	taken := false
	pair := func(what, old, fresh string) summaryLine {
		n := ""
		if !taken {
			n, taken = note, true
		}
		if old == fresh {
			return summaryLine{sign: "=", color: c.TextMuted, what: what,
				detail: old + " (unchanged)", note: n}
		}
		return summaryLine{sign: "→", color: c.Primary, what: what,
			detail: old + " → " + fresh, note: n}
	}
	autonomy := "ask for approval"
	switch {
	case d.FullYoloMode:
		autonomy = "full yolo (-Y)"
	case d.YoloMode:
		autonomy = "yolo (-y)"
	}
	browser := "disabled"
	if d.BrowserEnabled {
		browser = "enabled"
	}
	// language 先建，才能决定那条 note 归谁 —— 五行共用一条 "write first-run choices"，
	// 出现两遍就又不是对账行了（摘要那头的测试钉住"恰好一次"）。
	var lang summaryLine
	if d.Lang == "" {
		lang, taken = summaryLine{sign: "=", color: c.TextMuted, what: "language",
			detail: "stays " + orNone(currents[0]) + " — no-instruction writes nothing",
			note:   note}, true
	} else {
		lang = pair("language", currents[0], d.Lang)
	}
	out := []summaryLine{
		pair("autonomy", currents[1], autonomy),
		pair("routing", currents[2], d.RoutingMode),
		pair("browser", currents[3], browser),
		pair("llm timeout", currents[4], d.LLMTimeout.String()),
	}
	return append([]summaryLine{lang}, out...)
}

// ── 键帽提示（底栏） ──

// hints 必须是 "key desc · key desc" 形状：renderHintBar 按 " · " 切段、
// 取首词做键帽。写成整句的地方，键帽就没了 —— 这条由 TestWizardHintsMatchHandledKeys 钉。
func (w *setupWizard) hints() string {
	switch w.step {
	case stepWelcome:
		return "enter start · a add provider · q quit (writes nothing)"
	case stepProviders:
		return "a add · e edit · 1-9 jump · s default · d delete · t test · c continue"
	case stepProviderForm:
		if w.presetOpen {
			return "j/k pick · 1-9 apply · enter apply · esc close"
		}
		return "tab next · ctrl+s save · ctrl+p preset · f2 key · esc back"
	case stepProbe:
		if w.probing {
			return "esc cancel view · wait for the round-trip"
		}
		return "p retry · enter edit provider · esc back to list"
	case stepDefault:
		return "j/k move · 1-9 pick · enter confirm · q quit (writes nothing)"
	case stepFirstRun:
		return "j/k row · h/l change · c continue · q quit (writes nothing)"
	case stepSummary:
		return "enter write file · j/k scroll · esc back · q quit without writing"
	}
	return ""
}

func clampInt(v, lo, hi int) int {
	if hi < lo {
		hi = lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func maskKey(d config.ProviderDraft) string {
	if d.APIKeyEnv != "" {
		return "$" + truncate(d.APIKeyEnv, 24)
	}
	if d.APIKey == "" {
		return "—"
	}
	if config.IsPlaceholderKey(d.APIKey) {
		return "your-key"
	}
	return maskSecret(d.APIKey)
}
