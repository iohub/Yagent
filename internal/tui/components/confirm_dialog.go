package components

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"codeactor/internal/tui/common"
)

// ConfirmResult represents the user's decision in the authorization confirmation dialog.
type ConfirmResult int

const (
	Allow        ConfirmResult = iota // Allow once
	AllowTool                         // Allow this tool in current session
	AllowSession                      // Allow all tools in current session
	AllowProject                      // Allow this tool for the entire project
	Deny                              // Deny
)

// ConfirmOption represents a single option in the authorization confirmation dialog.
type ConfirmOption struct {
	Key      string // shortcut key
	Label    string // display label
	Value    ConfirmResult
	SafetyLv common.SafetyLevel
}

// ConfirmDialog is the authorization confirmation dialog component.
// It prompts the user to allow/deny a tool execution.
type ConfirmDialog struct {
	requestID     string // 用于匹配用户确认请求
	toolName      string
	command       string
	warning       string
	selectedIndex int
	options       []ConfirmOption
	width         int
	height        int
	lang          Language
	styles        *common.Styles
}

// NewConfirmDialog creates a new authorization confirmation dialog.
func NewConfirmDialog(styles *common.Styles, toolName, command, warning, requestID string, lang Language) *ConfirmDialog {
	// Build options list with safety levels
	options := []ConfirmOption{
		{Key: "a", Label: getConfirmText("ConfirmOptionAllow", lang), Value: Allow, SafetyLv: common.SafetyLow},
		{Key: "t", Label: getConfirmText("ConfirmOptionAllowTool", lang), Value: AllowTool, SafetyLv: common.SafetyMedium},
		{Key: "s", Label: getConfirmText("ConfirmOptionAllowSession", lang), Value: AllowSession, SafetyLv: common.SafetyHigh},
		{Key: "p", Label: getConfirmText("ConfirmOptionAllowProject", lang), Value: AllowProject, SafetyLv: common.SafetyCritical},
		{Key: "d", Label: getConfirmText("ConfirmOptionDeny", lang), Value: Deny, SafetyLv: common.SafetySafe},
	}

	d := &ConfirmDialog{
		requestID:     requestID,
		toolName:      toolName,
		command:       command,
		warning:       warning,
		selectedIndex: 0, // default: Allow
		options:       options,
		lang:          lang,
		styles:        styles,
	}
	return d
}

// ID returns the unique identifier for this dialog.
func (d *ConfirmDialog) ID() string { return "confirm_dialog" }

// Type returns the dialog type.
func (d *ConfirmDialog) Type() DialogType { return DialogModal }

// Init initializes the component. No setup needed.
func (d *ConfirmDialog) Init() tea.Cmd { return nil }

// GetRequestID returns the request ID for matching user confirm requests.
func (d *ConfirmDialog) GetRequestID() string { return d.requestID }

// GetLang returns the language of this dialog.
func (d *ConfirmDialog) GetLang() Language { return d.lang }

// Update processes incoming messages and returns the updated component.
func (d *ConfirmDialog) Update(msg tea.Msg) (Component, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return d, nil
	}

	switch keyMsg.String() {
	case "down", "tab":
		d.selectedIndex = (d.selectedIndex + 1) % len(d.options)
		return d, nil
	case "up":
		d.selectedIndex = (d.selectedIndex + len(d.options) - 1) % len(d.options)
		return d, nil
	case "enter":
		return d, nil // Dialog will be closed by the model
	case "ctrl+c":
		return d, tea.Quit
	case "a", "A":
		return d, nil // respondToAuth will be handled by model
	case "t", "T":
		d.selectedIndex = 1
		return d, nil
	case "s", "S":
		d.selectedIndex = 2
		return d, nil
	case "p", "P":
		d.selectedIndex = 3
		return d, nil
	case "d", "D", "esc":
		d.selectedIndex = 4
		return d, nil
	}

	return d, nil
}

// View renders the dialog with the new design system.
func (d *ConfirmDialog) View() string {
	if d.width < 40 || d.height < 8 {
		return ""
	}

	// ── Size calculations ──
	const maxDialogWidth = 70
	dialogWidth := maxDialogWidth
	if d.width-4 < dialogWidth {
		dialogWidth = d.width - 4
	}
	innerWidth := dialogWidth - 6
	if innerWidth < 24 {
		innerWidth = 24
	}

	c := common.DarkModeColors()

	// ── Styles ──
	borderStyle := common.DialogBorderStyle(c)
	titleStyle := common.SectionHeaderStyle(c)
	commandStyle := common.CodeBlockStyle(c, innerWidth)
	helpStyle := common.HelpTextStyle(c)
	cursorStyle := common.CursorIndicatorStyle(c)

	// ── Title line ──
	titlePrefix := getConfirmText("ConfirmAuthTitle", d.lang)
	rawTitle := fmt.Sprintf("%s — %s", titlePrefix, d.toolName)
	if lipgloss.Width(rawTitle) > innerWidth-4 {
		runes := []rune(rawTitle)
		if len(runes) > innerWidth-6 {
			rawTitle = string(runes[:innerWidth-6]) + "…"
		}
	}
	titleLine := titleStyle.Render(rawTitle)

	// ── Command code block ──
	var commandLine string
	if d.command != "" {
		commandLine = commandStyle.Render(d.command)
	}

	// ── Warning text ──
	var warningLine string
	if d.warning != "" {
		warnText := fmt.Sprintf("⚠ %s", d.warning)
		if lipgloss.Width(warnText) > innerWidth-2 {
			runes := []rune(warnText)
			if len(runes) > innerWidth-4 {
				warnText = string(runes[:innerWidth-4]) + "…"
			}
		}
		warningLine = lipgloss.NewStyle().
			Foreground(c.Warning).
			Render(warnText)
	}

	// ── Option list ──
	var optionLines []string
	for i, opt := range d.options {
		if i == d.selectedIndex {
			// Focused option: full button style
			styled := lipgloss.JoinHorizontal(lipgloss.Center,
				opt.SafetyLv.Icon()+" "+opt.Label,
			)
			button := common.FocusedButtonStyle(c, opt.SafetyLv).
				Width(innerWidth - 2).
				Render(styled)
			optionLines = append(optionLines, button)
		} else {
			// Normal option: icon + label + key
			optText := fmt.Sprintf("  %s %s  (%s)", opt.SafetyLv.Icon(), opt.Label, opt.Key)
			if lipgloss.Width(optText) > innerWidth-2 {
				runes := []rune(optText)
				if len(runes) > innerWidth-4 {
					optText = string(runes[:innerWidth-4]) + "…"
				}
			}
			optionLines = append(optionLines, cursorStyle.Render("▶ ")+" "+cursorStyle.Render("•")+" "+optText)
		}
	}
	optionsBlock := lipgloss.JoinVertical(lipgloss.Left, optionLines...)

	// ── Help text ──
	help := helpStyle.Render(getConfirmText("ConfirmDialogHelp", d.lang))

	// ── Assemble content ──
	var contentParts []string
	contentParts = append(contentParts, titleLine)
	contentParts = append(contentParts, "")
	if commandLine != "" {
		contentParts = append(contentParts, commandLine)
		contentParts = append(contentParts, "")
	}
	if warningLine != "" {
		contentParts = append(contentParts, warningLine)
		contentParts = append(contentParts, "")
	}
	contentParts = append(contentParts, optionsBlock)
	contentParts = append(contentParts, "")
	contentParts = append(contentParts, help)

	content := lipgloss.JoinVertical(lipgloss.Left, contentParts...)

	dialog := borderStyle.Width(dialogWidth).Render(content)

	return lipgloss.Place(d.width, d.height,
		lipgloss.Center, lipgloss.Center,
		dialog,
	)
}

// IsFocused reports whether this dialog has keyboard focus.
func (d *ConfirmDialog) IsFocused() bool { return true }

// Focus sets the component as focused.
func (d *ConfirmDialog) Focus() tea.Cmd { return nil }

// Blur removes focus from this component.
func (d *ConfirmDialog) Blur() {}

// SetBounds sets the component's dimensions.
func (d *ConfirmDialog) SetBounds(w, h int) { d.width = w; d.height = h }

// Bounds returns the component's current width and height.
func (d *ConfirmDialog) Bounds() (int, int) { return d.width, d.height }

// IsVisible reports whether this dialog is visible.
func (d *ConfirmDialog) IsVisible() bool { return true }

// SetVisible sets the visibility of this dialog.
func (d *ConfirmDialog) SetVisible(bool) {}

// GetSelectedResult returns the currently selected ConfirmResult.
func (d *ConfirmDialog) GetSelectedResult() ConfirmResult {
	if d.selectedIndex < 0 || d.selectedIndex >= len(d.options) {
		return Allow
	}
	return d.options[d.selectedIndex].Value
}

// GetResponseAction returns the string action to send to the publisher.
func (d *ConfirmDialog) GetResponseAction() string {
	switch d.GetSelectedResult() {
	case Allow:
		return "allow"
	case AllowTool:
		return "allow_session"
	case AllowSession:
		return "allow_all_session"
	case AllowProject:
		return "allow_all_project"
	case Deny:
		return "deny"
	default:
		return "allow"
	}
}

// ── Helper functions ──

// getConfirmText retrieves a translation string for the confirm dialog.
func getConfirmText(key string, lang Language) string {
	switch key {
	case "ConfirmDialogHelp":
		if lang == LanguageZh {
			return "↑↓ 切换 · Enter 确认 · 字母键快捷选择"
		}
		return "↑↓ navigate  ·  Enter confirm  ·  letter shortcuts"
	case "ConfirmAuthTitle":
		if lang == LanguageZh {
			return "⚠️ 授权请求"
		}
		return "⚠️ Authorization Request"
	case "ConfirmAuthWarning":
		if lang == LanguageZh {
			return "此操作可能影响工作空间外的文件或系统环境。是否允许执行？"
		}
		return "This operation may affect files or the system environment outside the workspace. Allow?"
	case "ConfirmOptionAllow":
		if lang == LanguageZh {
			return "允许 (本次)"
		}
		return "Allow (Once)"
	case "ConfirmOptionAllowTool":
		if lang == LanguageZh {
			return "允许 (本工具)"
		}
		return "Allow (Tool)"
	case "ConfirmOptionAllowSession":
		if lang == LanguageZh {
			return "允许 (本次会话全部)"
		}
		return "Allow (All Session)"
	case "ConfirmOptionAllowProject":
		if lang == LanguageZh {
			return "允许 (本项目全部)"
		}
		return "Allow (Project)"
	case "ConfirmOptionDeny":
		if lang == LanguageZh {
			return "拒绝"
		}
		return "Deny"
	default:
		return key
	}
}

// wrapText wraps text to fit within maxWidth columns.
func wrapText(text string, maxWidth int) string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return text
	}

	var lines []string
	var currentLine string
	var currentWidth int

	for _, word := range words {
		wordWidth := lipgloss.Width(word)
		if currentLine == "" {
			currentLine = word
			currentWidth = wordWidth
		} else if currentWidth+1+wordWidth <= maxWidth {
			currentLine += " " + word
			currentWidth += 1 + wordWidth
		} else {
			lines = append(lines, currentLine)
			currentLine = word
			currentWidth = wordWidth
		}
	}
	if currentLine != "" {
		lines = append(lines, currentLine)
	}

	return strings.Join(lines, "\n")
}
