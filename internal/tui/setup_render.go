package tui

import (
	"fmt"
	"image/color"
	"os"
	"strings"

	"yagent/internal/config"
	"yagent/internal/tui/common"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

const (
	setupMinWidth      = 40
	setupMinHeight     = 18
	setupMaxPanelWidth = 104
	setupRailWidth     = 18
	setupRailMinInner  = 54
	setupMinInner      = 30
	bodyChrome         = 4
)

type setupColors struct{ common.ColorTokens }

func setupPaletteColors() setupColors {
	return setupColors{common.DarkModeColors()}
}

type setupLayout struct {
	panelW   int
	inner    int
	railW    int
	contentW int
	rows     int
}

func (w *setupWizard) layout(extras int) setupLayout {
	panelW, _ := common.PanelWidths(setupMaxPanelWidth, setupMinInner, w.width)
	inner := max(panelW-bodyChrome, setupMinInner)
	L := setupLayout{panelW: panelW, inner: inner, contentW: inner}
	if inner >= setupRailMinInner {
		L.railW = setupRailWidth
		L.contentW = inner - setupRailWidth - 1
	}
	L.rows = max(w.height-2-extras, 1)
	return L
}

func (w *setupWizard) View() tea.View {
	if w.width < setupMinWidth || w.height < setupMinHeight {
		return tea.View{AltScreen: true, Content: fmt.Sprintf(
			"yagent setup: needs at least %d×%d columns×rows (now %d×%d).",
			setupMinWidth, setupMinHeight, w.width, w.height)}
	}
	c := setupPaletteColors()
	status := fitLines(w.capStatus(fitLines(w.statusLines(c), w.statusInner()), c), w.statusInner())
	L := w.layout(len(status))

	body := fitLines(w.body(c, L), L.contentW)
	var joined string
	if L.railW > 0 {
		joined = lipgloss.JoinHorizontal(lipgloss.Top, w.rail(c, L),
			lipgloss.JoinVertical(lipgloss.Left, body...))
	} else {
		joined = lipgloss.JoinVertical(lipgloss.Left, body...)
	}
	content := strings.Join(w.trimToHeight(strings.Split(joined, "\n"), c, L), "\n")

	rail := w.borderFor(c)
	frame := []string{renderPanelTop(w.panelLabel(c, L), w.panelMeta(), L.panelW, rail)}
	frame = append(frame, editorBodyStyle(rail, L.panelW).Render(content))
	if strip := lipgloss.JoinVertical(lipgloss.Left, status...); strip != "" {
		frame = append(frame, editorBodyStyle(rail, L.panelW).Render(strip))
	}
	frame = append(frame, renderPanelBottom(w.hints(), L.panelW, rail))

	packed := lipgloss.JoinVertical(lipgloss.Left, fitLines(frame, L.panelW)...)
	return tea.View{AltScreen: true, Content: lipgloss.Place(
		w.width, w.height, lipgloss.Center, lipgloss.Center, packed)}
}

func (w *setupWizard) borderFor(c setupColors) color.Color {
	switch {
	case w.errMsg != "" || w.probeErr != "":
		return c.Danger
	case w.probing:
		return c.Success
	case w.step == stepProviderForm:
		return lipgloss.Color("62")
	case w.step == stepSummary:
		return c.Accent
	default:
		return c.BorderActive
	}
}

func (w *setupWizard) panelLabel(c setupColors, L setupLayout) string {
	name := w.step.title()
	switch {
	case L.panelW >= 46:
		if L.railW > 0 {
			return "YAGENT SETUP · " + name
		}
		return fmt.Sprintf("YAGENT SETUP · %s · %d of %d", name, w.stage()+1, setupStageCount)
	case L.panelW >= 32:
		return "SETUP · " + name
	default:
		return name
	}
}

func (w *setupWizard) panelMeta() string {
	if len(w.ops) == 0 {
		return "nothing written"
	}
	return fmt.Sprintf("%d change%s queued", len(w.ops), plural(len(w.ops)))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func (w *setupWizard) statusLines(c setupColors) []string {
	inner := w.statusInner()
	add := func(prefix, text string, style lipgloss.Style) []string {
		if text == "" {
			return nil
		}
		budget := max(inner-lipgloss.Width(prefix), 8)
		var out []string
		for _, line := range wrapPlain(text, budget) {
			out = append(out, prefix+style.Render(line))
		}
		return out
	}

	var out []string
	out = append(out, add("× ", w.errMsg, lipgloss.NewStyle().Foreground(c.Danger))...)
	if w.step != stepProbe {
		out = append(out, add("× ", w.probeErr, lipgloss.NewStyle().Foreground(c.Danger))...)
	}
	out = append(out, add("✓ ", w.note, lipgloss.NewStyle().Foreground(c.Success))...)
	if w.detail != "" && w.step == stepWelcome {
		out = append(out, add("! why setup: ", w.detail,
			lipgloss.NewStyle().Foreground(c.TextMuted).Italic(true))...)
	}
	return out
}

func (w *setupWizard) capStatus(status []string, c setupColors) []string {
	budget := w.height - 2 - 3
	if budget < 2 || len(status) <= budget {
		return status
	}
	hidden := len(status) - (budget - 1)
	out := append([]string{}, status[:budget-1]...)
	marker := fmt.Sprintf("… %d more (terminal too short)", hidden)
	return append(out, lipgloss.NewStyle().Foreground(c.DangerDim).Italic(true).Render(marker))
}

func (w *setupWizard) statusInner() int {
	panelW, _ := common.PanelWidths(setupMaxPanelWidth, setupMinInner, w.width)
	return max(panelW-bodyChrome, setupMinInner)
}

const setupStageCount = 4

func (w *setupWizard) stage() int {
	switch w.step {
	case stepWelcome, stepProviders, stepProviderForm, stepProbe:
		return 0
	case stepDefault:
		return 1
	case stepFirstRun:
		return 2
	case stepSummary:
		return 3
	}
	return 0
}

func (w *setupWizard) stageDone(stage int) bool {
	switch stage {
	case 0:
		return len(w.effectiveProviders()) > 0
	case 1:
		def := w.currentDefault()
		if def == "" {
			return false
		}
		for _, r := range w.effectiveProviders() {
			if r.draft.Name == def {
				return true
			}
		}
		return false
	case 2:
		for _, op := range w.ops {
			if op.kind == "firstrun" {
				return true
			}
		}
		return false
	case 3:
		return w.saved
	}
	return false
}

func (w *setupWizard) currentDefault() string {
	def := w.snap.useProvider
	for _, op := range w.ops {
		if op.kind == "default" {
			def = op.name
		}
	}
	return def
}

func (w *setupWizard) rail(c setupColors, L setupLayout) string {
	names := []string{"PROVIDER", "DEFAULT", "CHOICES", "REVIEW"}
	cur := w.stage()
	var out []string
	for i, name := range names {
		node, style := "○", lipgloss.NewStyle().Foreground(c.TextMuted)
		switch {
		case i == cur:
			node, style = "●", lipgloss.NewStyle().Foreground(c.Primary).Bold(true)
		case w.stageDone(i):
			node, style = "✓", lipgloss.NewStyle().Foreground(c.Success)
		}
		out = append(out, node+" "+style.Render(common.Ellipsize(name, L.railW-2)))
		if i == cur {
			if detail := w.stageDetail(i, c, L.railW-4); detail != "" {
				out = append(out, "  "+detail)
			}
		}
		if i < len(names)-1 {
			out = append(out, lipgloss.NewStyle().Foreground(c.Border).Render("│"))
		}
	}
	return lipgloss.NewStyle().Width(L.railW).PaddingRight(1).Render(strings.Join(out, "\n"))
}

func (w *setupWizard) stageDetail(stage int, c setupColors, width int) string {
	muted := lipgloss.NewStyle().Foreground(c.TextMuted).Italic(true)
	width = max(width, 8)
	switch stage {
	case 0:
		rows := w.effectiveProviders()
		if len(rows) == 0 {
			return muted.Render("none yet")
		}
		for _, r := range rows {
			if _, pending := w.pendingFor(r.draft.Name); pending {
				return muted.Render("draft " + truncate(r.draft.Name, width-6))
			}
		}
		return muted.Render(fmt.Sprintf("%d provider%s", len(rows), plural(len(rows))))
	case 1:
		if def := w.currentDefault(); def != "" {
			return muted.Render(truncate(def, width))
		}
		return muted.Render("not set")
	case 2:
		for _, op := range w.ops {
			if op.kind == "firstrun" {
				return muted.Render("answered")
			}
		}
		return muted.Render("5 questions")
	case 3:
		if w.saved {
			return muted.Render("written")
		}
		if n := len(w.ops); n > 0 {
			return muted.Render(fmt.Sprintf("%d to write", n))
		}
		return muted.Render("nothing queued")
	}
	return ""
}

func fitLines(lines []string, width int) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if lipgloss.Width(l) > width {
			l = clipVisible(l, max(width-1, 0)) + "…"
		}
		out = append(out, l)
	}
	return out
}

func (w *setupWizard) trimToHeight(lines []string, c setupColors, L setupLayout) []string {
	if len(lines) <= L.rows {
		return lines
	}
	keep := max(L.rows-1, 1)
	out := append([]string{}, lines[:keep]...)
	return append(out, common.MoreLine(len(lines)-keep, false, L.contentW, c.ColorTokens))
}

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

func wrapPlain(s string, width int) []string {
	if s == "" {
		return nil
	}
	if width <= 0 {
		return []string{s}
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		line := ""
		for _, word := range strings.Fields(para) {
			for lipgloss.Width(word) > width {
				if line != "" {
					out = append(out, line)
					line = ""
				}
				var head string
				head, word = cutWidth(word, width)
				out = append(out, head)
			}
			if lipgloss.Width(line)+lipgloss.Width(word)+1 > width && line != "" {
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

func cutWidth(s string, w int) (head, rest string) {
	used := 0
	for i, r := range s {
		rw := lipgloss.Width(string(r))
		if rw == 0 {
			continue
		}
		if used+rw > w {
			return s[:i], s[i:]
		}
		used += rw
	}
	return s, ""
}

func listWindow(n, focus, rows int) (start, end, moreUp, moreDown int) {
	visible, _ := common.ListBudget(0, max(rows, 1), max(rows, 1), n)
	start, end = common.WindowRange(n, focus, visible)
	return start, end, start, n - end
}

func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
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

func cardStyle(c setupColors, width int) lipgloss.Style {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(c.Border).
		Background(c.SurfaceRaised).
		BorderBackground(c.SurfaceRaised).
		Padding(0, 1).
		Width(width)
}

func cardInner(width int) int { return max(width-4, 8) }

func sectionRule(c setupColors, width int, label string) string {
	if label == "" {
		return lipgloss.NewStyle().Foreground(c.Border).Render(strings.Repeat("─", width))
	}
	label = " " + label + " "
	dashes := max(width-lipgloss.Width(label), 0)
	left := dashes / 2
	return lipgloss.NewStyle().Foreground(c.Border).Render(strings.Repeat("─", left)) +
		lipgloss.NewStyle().Foreground(c.TextMuted).Italic(true).Render(label) +
		lipgloss.NewStyle().Foreground(c.Border).Render(strings.Repeat("─", dashes-left))
}

func keycapHint(c setupColors, key string) string {
	return common.KeyHintStyle(c.ColorTokens).Render(key)
}

func RunSetup(path string, reason config.SetupReason, detail string) (saved bool, err error) {
	w := newSetupWizard(path, reason, detail)
	w.standalone = true
	termWidth, termHeight := 80, 24
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
