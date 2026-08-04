package globalctx

import (
	"codeactor/internal/browser"
	"codeactor/internal/config"
	"codeactor/internal/knowledge"
	"codeactor/internal/mcp"
	"codeactor/internal/messaging"
	"codeactor/internal/tools"
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

	// KnowledgeInjector 对话前知识注入器（nil=未启用）
	KnowledgeInjector *knowledge.KnowledgeInjector

	// GitCheckpointCfg holds the git checkpoint configuration (may be nil if not configured)
	GitCheckpointCfg *config.GitCheckpointConfig

	// EnhancedCommander 增强型 Commander 配置（含上下文压缩开关，零值=全部关闭）
	EnhancedCommander config.EnhancedCommanderConfig
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
		sb.WriteString("You are currently in FULL-YOLO autonomous mode.\n")
		sb.WriteString("You MUST NOT use the ask_user_for_help tool to seek user assistance.\n")
		sb.WriteString("When encountering ambiguity, uncertainty, or missing critical information, you MUST make the best independent decision based on your judgment and continue executing.\n")
		sb.WriteString("Do NOT pause for user input, do NOT ask clarifying questions — directly take what you consider the most reasonable course of action.\n")
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
