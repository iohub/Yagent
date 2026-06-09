package common

import (
	"codeactor/internal/app"
	"codeactor/internal/config"
)

// Common holds shared state and configuration for all UI components.
// Inspired by crush's common.Common pattern — a single struct passed
// to every UI sub-component to avoid parameter explosion.
type Common struct {
	// Styles is the centralized theme. All visual styling goes through this.
	Styles *Styles

	// Config is the application configuration.
	Config *config.Config

	// Assistant is the CodeActor instance for agent operations.
	Assistant *app.CodeActor

	// ProjectDir is the current working directory.
	ProjectDir string

	// UseDarkStyle controls dark/light theme.
	UseDarkStyle bool
}

// NewCommon creates a Common with all fields initialized.
func NewCommon(styles *Styles, cfg *config.Config, assistant *app.CodeActor, projectDir string, useDarkStyle bool) *Common {
	return &Common{
		Styles:       styles,
		Config:       cfg,
		Assistant:    assistant,
		ProjectDir:   projectDir,
		UseDarkStyle: useDarkStyle,
	}
}
