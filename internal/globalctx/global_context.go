package globalctx

import (
	"codeactor/internal/assistant/tools"
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

	// Tools
	FileOps      *tools.FileOperationsTool
	SearchOps    *tools.SearchOperationsTool
	SysOps       *tools.SystemOperationsTool
	ReplaceTool  *tools.ReplaceBlockTool
	ThinkingTool *tools.ThinkingTool
	FlowOps      *tools.FlowControlTool
}

func NewGlobalCtx() *GlobalCtx {
	return &GlobalCtx{
		SpeakLang: "Chinese",
	}
}

func (g *GlobalCtx) FormatPrompt(prompt string) string {
	var sb strings.Builder
	sb.WriteString(prompt)

	// Environment context
	sb.WriteString("\n\n<env>\n")
	if g.ProjectPath != "" {
		sb.WriteString(fmt.Sprintf("Project Path: %s\n", g.ProjectPath))
	}
	if g.OS != "" {
		sb.WriteString(fmt.Sprintf("Operating System: %s\n", g.OS))
	}
	if g.Arch != "" {
		sb.WriteString(fmt.Sprintf("Architecture: %s\n", g.Arch))
	}
	sb.WriteString("</env>\n")

	// Language
	if g.SpeakLang != "" {
		sb.WriteString(fmt.Sprintf("\n<critical_instructions>\nYou must speak in %s.\n</critical_instructions>\n", g.SpeakLang))
	}

	// Custom prompt
	if g.CustomizePrompt != "" {
		sb.WriteString(fmt.Sprintf("\n<additional_instructions>\n%s\n</additional_instructions>\n", g.CustomizePrompt))
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
