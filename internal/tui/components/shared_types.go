package components

// Language represents the UI language.
// This type is defined here to avoid import cycles between tui and components packages.
type Language string

const (
	LanguageZh Language = "zh"
	LanguageEn Language = "en"
)

// DialogType defines the type of dialog.
// (Note: also defined in dialog.go - this is for cross-reference only)

// Dialog is a dialog component interface.
// (Note: also defined in dialog.go - this is for cross-reference only)
