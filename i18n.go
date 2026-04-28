package main

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
	// New fields for history modal
	HistoryButton       string
	HistoryTitle        string
	HistoryEmpty        string
	HistorySearchHint   string
	HistoryUseSelected  string
	HistoryClose        string
}

var langMap = map[Language]translations{
	LangChinese: {
		Title:                            "DeepCoder 助手",
		ProjectDirLabel:                  "项目目录",
		TaskDescLabel:                    "任务描述",
		ProjectDirPlaceholder:            "输入项目目录路径",
		TaskDescPlaceholder:              "输入任务描述，如：重构模块、修复 Bug、实现功能…",
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
		HistoryTitle:                     "选择历史任务",
		HistoryEmpty:                     "暂无历史任务",
		HistorySearchHint:                "上下移动选择，输入过滤，Enter 使用，Esc 关闭",
		HistoryUseSelected:               "使用所选",
		HistoryClose:                     "关闭",
	},
	LangEnglish: {
		Title:                            "DeepCoder Assistant",
		ProjectDirLabel:                  "Project Directory",
		TaskDescLabel:                    "Task Description",
		ProjectDirPlaceholder:            "Enter project directory path",
		TaskDescPlaceholder:              "Enter task description, e.g.: refactor module, fix bug, implement feature...",
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
		HistoryTitle:                     "Select a Past Task",
		HistoryEmpty:                     "No history yet",
		HistorySearchHint:                "Move to select, type to filter, Enter to use, Esc to close",
		HistoryUseSelected:               "Use Selected",
		HistoryClose:                     "Close",
	},
}

// LanguageManager handles language selection and translation
type LanguageManager struct {
	currentLang Language
}

func NewLanguageManager() *LanguageManager {
	return &LanguageManager{currentLang: LangEnglish} // Default to English
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
	case "HistorySearchHint":
		return translations.HistorySearchHint
	case "HistoryUseSelected":
		return translations.HistoryUseSelected
	case "HistoryClose":
		return translations.HistoryClose
	default:
		return fmt.Sprintf("[Missing translation: %s]", key)
	}
}
