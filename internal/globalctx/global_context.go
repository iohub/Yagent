package globalctx

import (
	"codeactor/internal/browser"
	"codeactor/internal/config"
	"codeactor/internal/tools"
	"codeactor/internal/mcp"
	"codeactor/internal/messaging"
	"fmt"
	"strings"
)

type GlobalCtx struct {
	CustomizePrompt string
	FullYoloMode    bool
	SpeakLang       string
	ProjectPath     string
	OS              string
	Arch            string
	RepoSummary     string
	// Global utility
	Publisher *messaging.MessagePublisher
	// TODO: [Codexray] CodexrayURL field removed — re-add when codexray is re-integrated

	// MaxContextTokens 最大上下文token数
	MaxContextTokens int

	// Tools
	FileOps          *tools.FileOperationsTool
	SearchOps        *tools.SearchOperationsTool
	SysOps           *tools.SystemOperationsTool
	ReplaceTool      *tools.ReplaceBlockTool
	ThinkingTool     *tools.ThinkingTool
	MicroAgentTool   *tools.MicroAgentTool
	FlowOps          *tools.FlowControlTool
	RepoOps          *tools.RepoOperationsTool
	UserConfirmMgr   *tools.UserConfirmManager
	Guard            *tools.WorkspaceGuard
	DeepThinkingTool *tools.DeepThinkingTool
	// BrowserMgr 浏览器管理器（单例，管理 Chromium 浏览器实例生命周期）
	BrowserMgr *browser.Manager

	// CodeSeekMCP MCP 客户端（用于代码分析，nil=未启用）
	CodeSeekMCP *mcp.MCPClient

	// GitCheckpointCfg holds the git checkpoint configuration (may be nil if not configured)
	GitCheckpointCfg *config.GitCheckpointConfig
}

func (g *GlobalCtx) FormatPrompt(prompt string) string {
	var sb strings.Builder
	sb.WriteString(prompt)

	// Environment context
	projectPath := g.ProjectPath
	if projectPath == "" {
		projectPath = "[NOT SET]"
	}
	os := g.OS
	if os == "" {
		os = "[NOT SET]"
	}
	arch := g.Arch
	if arch == "" {
		arch = "[NOT SET]"
	}

	sb.WriteString("\n\n### Environment\n")
	sb.WriteString(fmt.Sprintf("- **Project Path**: %s\n", projectPath))
	sb.WriteString(fmt.Sprintf("- **Operating System**: %s\n", os))
	sb.WriteString(fmt.Sprintf("- **Architecture**: %s\n", arch))

	// Language
	if g.SpeakLang != "" {
		sb.WriteString(fmt.Sprintf("\n### Language Instructions\nYou MUST use **%s** for ALL output, including your internal 'Thought Process', 'Thinking Tool' usage, reasoning steps, and final responses.\n", g.SpeakLang))
	}

	// Custom prompt
	if g.CustomizePrompt != "" {
		sb.WriteString(fmt.Sprintf("\n### Additional Instructions\n%s\n", g.CustomizePrompt))
	}

	// Full-YOLO mode: autonomous decision-making instructions
	if g.FullYoloMode {
		sb.WriteString("\n### Autonomous Mode\n")
		sb.WriteString("你当前处于完全自主模式（FULL-YOLO）。\n")
		sb.WriteString("禁止使用 ask_user_for_help 工具向用户寻求帮助。\n")
		sb.WriteString("遇到模糊、不确定或缺乏关键信息的情况时，必须基于你的最佳判断自主决策并继续执行。\n")
		sb.WriteString("不要暂停等待用户输入，不要询问澄清问题，直接采取你认为最合理的行动。\n")
	}

	return sb.String()
}

func (g *GlobalCtx) SetPublisher(publisher *messaging.MessagePublisher) {
	g.Publisher = publisher
}

func (g *GlobalCtx) SetProjectPath(path string) {
	g.ProjectPath = path
}

func (g *GlobalCtx) SetSpeakLang(lang string) {
	g.SpeakLang = lang
}

func (g *GlobalCtx) SetCustomizePrompt(prompt string) {
	g.CustomizePrompt = prompt
}

// TODO: [Codexray] SetCodexrayURL method removed — re-add when codexray is re-integrated
