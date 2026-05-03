package globalctx

import (
	"codeactor/internal/tools"
	"codeactor/pkg/messaging"
	"fmt"
	"strings"
)

type GlobalCtx struct {
	CustomizePrompt string
	SpeakLang       string
	ProjectPath     string
	OS              string
	Arch            string
	RepoSummary     string
	// Global utility
	Publisher *messaging.MessagePublisher
	// Codebase baseurl
	CodebaseURL string

	// Tools
	FileOps         *tools.FileOperationsTool
	SearchOps       *tools.SearchOperationsTool
	SysOps          *tools.SystemOperationsTool
	ReplaceTool     *tools.ReplaceBlockTool
	ThinkingTool    *tools.ThinkingTool
	MicroAgentTool  *tools.MicroAgentTool
	ImplPlanTool    *tools.ImplPlanTool
	FlowOps         *tools.FlowControlTool
	RepoOps         *tools.RepoOperationsTool
	UserConfirmMgr  *tools.UserConfirmManager
	Guard           *tools.WorkspaceGuard
}

func (g *GlobalCtx) FormatPrompt(prompt string) string {
	var sb strings.Builder
	sb.WriteString(prompt)

	// Environment context
	sb.WriteString("\n\n### Environment\n")
	if g.ProjectPath != "" {
		sb.WriteString(fmt.Sprintf("- **Project Path**: %s\n", g.ProjectPath))
	}
	if g.OS != "" {
		sb.WriteString(fmt.Sprintf("- **Operating System**: %s\n", g.OS))
	}
	if g.Arch != "" {
		sb.WriteString(fmt.Sprintf("- **Architecture**: %s\n", g.Arch))
	}

	// Language
	if g.SpeakLang != "" {
		sb.WriteString(fmt.Sprintf("\n### Language Instructions\nYou MUST use **%s** for ALL output, including your internal 'Thought Process', 'Thinking Tool' usage, reasoning steps, and final responses.\n", g.SpeakLang))
	}

	// Custom prompt
	if g.CustomizePrompt != "" {
		sb.WriteString(fmt.Sprintf("\n### Additional Instructions\n%s\n", g.CustomizePrompt))
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

func (g *GlobalCtx) SetCodebaseURL(url string) {
	g.CodebaseURL = url
}
