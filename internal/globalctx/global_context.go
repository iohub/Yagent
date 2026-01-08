package globalctx

import (
	"codeactor/internal/assistant/tools"
	"codeactor/pkg/messaging"
	"fmt"
)

type GlobalCtx struct {
	CustomizePrompt string
	SpeakLang       string
	ProjectPath     string
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
	extra := ""
	if g.ProjectPath != "" {
		extra += fmt.Sprintf("\nProject Path: %s\n", g.ProjectPath)
	}
	if g.SpeakLang != "" {
		extra += fmt.Sprintf("\nYou must speak in %s.\n", g.SpeakLang)
	}
	if g.CustomizePrompt != "" {
		extra += fmt.Sprintf("\n%s\n", g.CustomizePrompt)
	}
	return prompt + extra
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
