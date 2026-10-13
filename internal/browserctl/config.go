package browserctl

import (
	"time"

	"yagent/internal/config"
)

const (
	ModeHeadless = "headless"
	ModeHeadful  = "headful"
	ModeAttach   = "attach"

	DialogAccept  = "accept"
	DialogDismiss = "dismiss"
	DialogAsk     = "ask"

	// BinaryName 是上游安装出的可执行文件名（crates.io 上的包名是 browser-control-cli）。
	BinaryName = "browser-control"

	// MaxWaitSeconds 是本系统允许的最长等待秒数（browser_wait 的上限）。
	// 上游内部 CDP 超时默认只有 15s，必须由我们用 BROWSER_CONTROL_TIMEOUT 抬到 ≥ 它。
	MaxWaitSeconds = 60
)

// Config 是执行层配置。字段含义与 [browser] 段一一对应，零值兜底在 config 层完成。
type Config struct {
	Enabled        bool
	BinaryPath     string
	ChromePath     string
	Mode           string
	CDPURL         string
	CDPPortBase    int
	CDPTimeout     time.Duration
	CommandTimeout time.Duration
	IdleTTL        time.Duration
	MaxChrome      int
	WorkspaceRoot  string
	WorkspaceTTL   time.Duration
	MaxOutputBytes int
	DialogPolicy   string
	AllowedDomains []string
	DeniedDomains  []string
	ProfileDir     string
	EnableSkills   bool
}

// DefaultConfig 与 config.DefaultBrowserConfig 保持一致，供不经过配置文件的调用方使用。
func DefaultConfig() Config {
	return FromAppConfig(config.DefaultBrowserConfig())
}

// FromAppConfig 把 [browser] 段映射成执行层配置。
func FromAppConfig(c config.BrowserConfig) Config {
	return Config{
		Enabled:        c.Enabled,
		BinaryPath:     c.BinaryPath,
		ChromePath:     c.ChromePath,
		Mode:           c.Mode,
		CDPURL:         c.CDPURL,
		CDPPortBase:    c.CDPPortBase,
		CDPTimeout:     c.CDPTimeout,
		CommandTimeout: c.CommandTimeout,
		IdleTTL:        c.IdleTTL,
		MaxChrome:      c.MaxChrome,
		WorkspaceRoot:  c.WorkspaceRoot,
		WorkspaceTTL:   c.WorkspaceTTL,
		MaxOutputBytes: c.MaxOutputBytes,
		DialogPolicy:   c.DialogPolicy,
		AllowedDomains: c.AllowedDomains,
		DeniedDomains:  c.DeniedDomains,
		ProfileDir:     c.ProfileDir,
		EnableSkills:   c.EnableSkills,
	}
}
