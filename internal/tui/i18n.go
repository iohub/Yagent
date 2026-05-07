package tui

import "fmt"

type Language string

const (
	LangChinese Language = "zh"
	LangEnglish Language = "en"
)

// Translation strings for all UI text
type translations struct {
	Title                            string
	ProjectDirLabel                  string
	TaskDescLabel                    string
	ProjectDirPlaceholder            string
	TaskDescPlaceholder              string
	InfoMessage                      string
	SubmitButton                     string
	QuitMessage                      string
	ValidationErrorEmptyProjectDir   string
	ValidationErrorInvalidProjectDir string
	ValidationErrorEmptyTaskDesc     string
	ValidationErrorShortTaskDesc     string
	LanguageButton                   string
	LanguageHelp                     string
	// New fields for tips content
	AskTips        string
	BeSpecificTips string
	CreateFileTips string
	HelpTips       string
	// History panel
	HistoryButton            string
	HistoryTitle             string
	HistoryEmpty             string
	HistoryFilterPlaceholder string
	HistoryMoreAbove         string
	HistoryMoreBelow         string
	HistoryKeyContinue       string
	HistoryKeyDelete         string
	HistoryKeyBack           string
	HistoryKeyClearFilter    string
	HistoryConfirmDelete     string
	// Confirmation dialog
	ConfirmDialogHelp    string
	ConfirmQuitTitle     string
	ConfirmQuitMessage   string
	ConfirmCancelTitle   string
	ConfirmCancelMessage string
	ConfirmDialogYes     string
	ConfirmDialogNo      string
	// Command mode (vim-like modal editing)
	CommandModePrompt   string
	CommandModeTips     string
	CommandModeIdleTips string
	EditModeTips        string
	HelpDialogTitle     string
	HelpDialogContent   string
}

var langMap = map[Language]translations{
	LangChinese: {
		Title:                            "CodeActor AI 助手",
		ProjectDirLabel:                  "项目目录",
		TaskDescLabel:                    "任务描述",
		ProjectDirPlaceholder:            "输入项目目录路径",
		TaskDescPlaceholder:              "",
		InfoMessage:                      "按 Tab/Shift+Tab 切换，Enter 确认下一个",
		SubmitButton:                     "提交 (Ctrl+S)",
		QuitMessage:                      "\n再见！\n\n",
		ValidationErrorEmptyProjectDir:   "项目目录不能为空",
		ValidationErrorInvalidProjectDir: "项目目录不存在或不可访问",
		ValidationErrorEmptyTaskDesc:     "任务描述不能为空",
		ValidationErrorShortTaskDesc:     "任务描述太短，尽量更具体",
		LanguageButton:                   "切换语言",
		LanguageHelp:                     "在此按 Enter/L 切换中文/English",
		AskTips:                          "提问、编辑文件或运行命令。",
		BeSpecificTips:                   "尽量具体，效果更佳。",
		CreateFileTips:                   "创建 GEMINI.md 文件以定制你的交互。",
		HelpTips:                         "输入 /help 查看更多信息。",
		HistoryButton:                    "历史任务",
		HistoryTitle:                     "会话历史",
		HistoryEmpty:                     "暂无历史会话",
		HistoryFilterPlaceholder:         "输入关键词过滤...",
		HistoryMoreAbove:                 "▲ 前面还有 %d 条",
		HistoryMoreBelow:                 "▼ 后面还有 %d 条",
		HistoryKeyContinue:               "enter: 继续对话",
		HistoryKeyDelete:                 "ctrl+d: 删除",
		HistoryKeyBack:                   "esc: 返回",
		HistoryKeyClearFilter:            "ctrl+u: 清除过滤",
		HistoryConfirmDelete:             "确认删除此会话？(y = 确认, 其他键 = 取消)",
		ConfirmDialogHelp:                "←/→ 选择  enter 确认  a 允许  s 全部允许  d/esc 拒绝",
		ConfirmQuitTitle:                 "退出程序",
		ConfirmQuitMessage:               "确定要退出程序吗？",
		ConfirmCancelTitle:               "取消任务",
		ConfirmCancelMessage:             "确定要取消当前任务吗？",
		ConfirmDialogYes:                 "确认 (Enter)",
		ConfirmDialogNo:                  "取消 (Esc)",
		CommandModePrompt:                "命令",
		CommandModeTips:                  "gg/G:首/尾  j/k:上下  f/b:翻页  ctrl+d/u:半页  i:编辑  ctrl+e:编辑模式  ZZ:退出",
		CommandModeIdleTips:              "gg/G:首/尾  j/k:上下  f/b:翻页  ctrl+d/u:半页  ::命令  /:搜索  ?:帮助  i:编辑  ZZ:退出",
		EditModeTips:                     "ctrl+s:提交  ctrl+e:命令模式  ctrl+h:历史  ctrl+l:语言  ctrl+c:退出",
		HelpDialogTitle:                  "Vim 快捷键帮助",
		HelpDialogContent: "  导航:\n" +
			"    j / ↓          向下滚动一行\n" +
			"    k / ↑          向上滚动一行\n" +
			"    f / PageDown    向下翻页\n" +
			"    b / PageUp      向上翻页\n" +
			"    ctrl+d          向下半页\n" +
			"    ctrl+u          向上半页\n" +
			"    gg              跳到开头\n" +
			"    G               跳到末尾\n" +
			"  模式:\n" +
			"    i               进入编辑模式\n" +
			"    ctrl+e          进入命令模式\n" +
			"  命令行:\n" +
			"    :q / ZZ         退出程序\n" +
			"    :help           显示命令帮助\n" +
			"    /pattern        搜索日志\n" +
			"  其他:\n" +
			"    ctrl+h          历史会话\n" +
			"    ctrl+l          切换语言\n" +
			"    ?               显示此帮助\n" +
			"    ctrl+c          强制退出",
	},
	LangEnglish: {
		Title:                            "CodeActor AI Assistant",
		ProjectDirLabel:                  "Project Directory",
		TaskDescLabel:                    "Task Description",
		ProjectDirPlaceholder:            "Enter project directory path",
		TaskDescPlaceholder:              "",
		InfoMessage:                      "Tab/Shift+Tab to switch, Enter to confirm next",
		SubmitButton:                     "Submit (Ctrl+S)",
		QuitMessage:                      "\nGoodbye!\n\n",
		ValidationErrorEmptyProjectDir:   "Project directory cannot be empty",
		ValidationErrorInvalidProjectDir: "Project directory does not exist or is inaccessible",
		ValidationErrorEmptyTaskDesc:     "Task description cannot be empty",
		ValidationErrorShortTaskDesc:     "Task description is too short, please be more specific",
		LanguageButton:                   "Switch Language",
		LanguageHelp:                     "Press Enter/L here to toggle English/中文",
		AskTips:                          "Ask questions, edit files, or run commands.",
		BeSpecificTips:                   "Be specific for the best results.",
		CreateFileTips:                   "Create GEMINI.md files to customize interactions.",
		HelpTips:                         "Type /help for more information.",
		HistoryButton:                    "History",
		HistoryTitle:                     "Conversation History",
		HistoryEmpty:                     "No conversations yet",
		HistoryFilterPlaceholder:         "type to filter...",
		HistoryMoreAbove:                 "▲ %d more above",
		HistoryMoreBelow:                 "▼ %d more below",
		HistoryKeyContinue:               "enter: continue",
		HistoryKeyDelete:                 "ctrl+d: delete",
		HistoryKeyBack:                   "esc: back",
		HistoryKeyClearFilter:            "ctrl+u: clear filter",
		HistoryConfirmDelete:             "Delete this conversation? (y = confirm, any other key = cancel)",
		ConfirmDialogHelp:                "←/→ choose  enter confirm  a allow  s all  d/esc deny",
		ConfirmQuitTitle:                 "Quit Program",
		ConfirmQuitMessage:               "Are you sure you want to quit?",
		ConfirmCancelTitle:               "Cancel Task",
		ConfirmCancelMessage:             "Are you sure you want to cancel the current task?",
		ConfirmDialogYes:                 "Confirm (Enter)",
		ConfirmDialogNo:                  "Cancel (Esc)",
		CommandModePrompt:                "COMMAND",
		CommandModeTips:                  "gg/G:top/btm  j/k:scroll  f/b:pgdn/up  ctrl+d/u:half  i:edit  ctrl+e:edit  ZZ:quit",
		CommandModeIdleTips:              "gg/G:top/btm  j/k:scroll  f/b:pgdn/up  ctrl+d/u:half  ::cmd  /:search  ?:help  i:edit  ZZ:quit",
		EditModeTips:                     "ctrl+s:submit  ctrl+e:cmd  ctrl+h:history  ctrl+l:lang  ctrl+c:quit",
		HelpDialogTitle:                  "Vim Keybindings Help",
		HelpDialogContent: "  Navigation:\n" +
			"    j / ↓          scroll down one line\n" +
			"    k / ↑          scroll up one line\n" +
			"    f / PageDown   page down\n" +
			"    b / PageUp     page up\n" +
			"    ctrl+d         half page down\n" +
			"    ctrl+u         half page up\n" +
			"    gg             go to top\n" +
			"    G              go to bottom\n" +
			"  Mode:\n" +
			"    i              enter edit mode\n" +
			"    ctrl+e         enter command mode\n" +
			"  Command line:\n" +
			"    :q / ZZ        quit\n" +
			"    :help          show command help\n" +
			"    /pattern       search log\n" +
			"  Other:\n" +
			"    ctrl+h         history\n" +
			"    ctrl+l         toggle language\n" +
			"    ?              show this help\n" +
			"    ctrl+c         force quit",
	},
}

// LanguageManager handles language selection and translation
type LanguageManager struct {
	currentLang Language
}

func NewLanguageManager() *LanguageManager {
	return &LanguageManager{currentLang: LangEnglish} // Default to English
}

// InitLangManager creates and assigns a new LanguageManager to the global langManager.
// Called by main.go's init() to initialize the TUI language manager.
func InitLangManager() {
	if langManager == nil {
		langManager = NewLanguageManager()
	}
}

func (lm *LanguageManager) SetLanguage(lang Language) {
	if _, exists := langMap[lang]; exists {
		lm.currentLang = lang
	}
}

func (lm *LanguageManager) GetText(key string) string {
	translations := langMap[lm.currentLang]
	switch key {
	case "Title":
		return translations.Title
	case "ProjectDirLabel":
		return translations.ProjectDirLabel
	case "TaskDescLabel":
		return translations.TaskDescLabel
	case "ProjectDirPlaceholder":
		return translations.ProjectDirPlaceholder
	case "TaskDescPlaceholder":
		return translations.TaskDescPlaceholder
	case "InfoMessage":
		return translations.InfoMessage
	case "SubmitButton":
		return translations.SubmitButton
	case "QuitMessage":
		return translations.QuitMessage
	case "ValidationErrorEmptyProjectDir":
		return translations.ValidationErrorEmptyProjectDir
	case "ValidationErrorInvalidProjectDir":
		return translations.ValidationErrorInvalidProjectDir
	case "ValidationErrorEmptyTaskDesc":
		return translations.ValidationErrorEmptyTaskDesc
	case "ValidationErrorShortTaskDesc":
		return translations.ValidationErrorShortTaskDesc
	case "LanguageButton":
		return translations.LanguageButton
	case "LanguageHelp":
		return translations.LanguageHelp
	case "AskTips":
		return translations.AskTips
	case "BeSpecificTips":
		return translations.BeSpecificTips
	case "CreateFileTips":
		return translations.CreateFileTips
	case "HelpTips":
		return translations.HelpTips
	case "HistoryButton":
		return translations.HistoryButton
	case "HistoryTitle":
		return translations.HistoryTitle
	case "HistoryEmpty":
		return translations.HistoryEmpty
	case "HistoryFilterPlaceholder":
		return translations.HistoryFilterPlaceholder
	case "HistoryMoreAbove":
		return translations.HistoryMoreAbove
	case "HistoryMoreBelow":
		return translations.HistoryMoreBelow
	case "HistoryKeyContinue":
		return translations.HistoryKeyContinue
	case "HistoryKeyDelete":
		return translations.HistoryKeyDelete
	case "HistoryKeyBack":
		return translations.HistoryKeyBack
	case "HistoryKeyClearFilter":
		return translations.HistoryKeyClearFilter
	case "HistoryConfirmDelete":
		return translations.HistoryConfirmDelete
	case "ConfirmDialogHelp":
		return translations.ConfirmDialogHelp
	case "ConfirmQuitTitle":
		return translations.ConfirmQuitTitle
	case "ConfirmQuitMessage":
		return translations.ConfirmQuitMessage
	case "ConfirmCancelTitle":
		return translations.ConfirmCancelTitle
	case "ConfirmCancelMessage":
		return translations.ConfirmCancelMessage
	case "ConfirmDialogYes":
		return translations.ConfirmDialogYes
	case "ConfirmDialogNo":
		return translations.ConfirmDialogNo
	case "CommandModePrompt":
		return translations.CommandModePrompt
	case "CommandModeTips":
		return translations.CommandModeTips
	case "CommandModeIdleTips":
		return translations.CommandModeIdleTips
	case "EditModeTips":
		return translations.EditModeTips
	case "HelpDialogTitle":
		return translations.HelpDialogTitle
	case "HelpDialogContent":
		return translations.HelpDialogContent
	default:
		return fmt.Sprintf("[Missing translation: %s]", key)
	}
}
