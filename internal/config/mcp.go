// 本文件维护 `[mcp]` 与 `[[mcp.servers]]`（通用 MCP server 的配置面）。
//
// 与 blueprint.go / resume.go / blackboard.go 同构：Manager 的生命周期、工具发现、
// 状态行、配置校验四处都要问同一件事，而逐层穿参数会让装配点到 TUI 都多一个入参 ——
// 所以走"启动初始化 + 全局唯一事实源"。
package config

import (
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// MCPServerConfig 对应一个 `[[mcp.servers]]` 块。
type MCPServerConfig struct {
	// Name 唯一，并进入工具名前缀（mcp__<name>__<tool>）。
	Name string `toml:"name"`
	// Command 是可执行文件；Args 原样传给它。
	Command string   `toml:"command"`
	Args    []string `toml:"args"`
	// Env 追加给子进程（覆盖同名项）。第三方 server 靠它拿 token。
	Env map[string]string `toml:"env"`
	// WorkingDir 是子进程的工作目录（"" = 继承当前进程）。
	WorkingDir string `toml:"working_dir"`
	// RequestTimeout 是单条 JSON-RPC 请求的 deadline（秒，<=0 → 30）。
	// 兜底只在 mcp.NewMCPClient 一处，这里不再抄一个 30。
	RequestTimeout int `toml:"request_timeout"`

	// Enabled 默认 true，所以用指针：`[[mcp.servers]]` 是数组元素，
	// toml.MetaData 的 IsDefined 拿不到"这一条写没写 enabled"，
	// 只有指针能区分"没写"（nil）与"写了 false"（坑 26 的数组版）。
	Enabled *bool `toml:"enabled"`

	// AllowWrite 默认 false：写类外部工具不进任何画像。放宽必须是显式意图。
	AllowWrite bool `toml:"allow_write"`
	// ReadOnly 是"虽然没有 annotations 但确实只读"的显式名单（免授权、可并发）。
	ReadOnly []string `toml:"read_only"`
	// Tools 非空时是白名单；Exclude 是黑名单；两者都按服务端工具名比对。
	Tools   []string `toml:"tools"`
	Exclude []string `toml:"exclude"`
}

// IsEnabled 报告该 server 是否启用（缺省 true）。
func (s MCPServerConfig) IsEnabled() bool {
	return s.Enabled == nil || *s.Enabled
}

// MCPConfig 对应 `[mcp]`。
type MCPConfig struct {
	// Enable 是总开关。关掉就是这套机制整体退出：不起进程、不发现工具、不注入状态行。
	Enable bool `toml:"enable"`

	// StartupTimeout 是"起进程 + initialize + tools/list"的有界等待。
	StartupTimeout time.Duration `toml:"startup_timeout"`
	// ReconnectBackoff 是重连退避起点（之后指数翻倍，上限 5m）。
	ReconnectBackoff time.Duration `toml:"reconnect_backoff"`
	// MaxReconnects 是重连次数；用尽即终态 offline，状态行写 gave up。
	MaxReconnects int `toml:"max_reconnects"`
	// MaxToolsPerServer 是单 server 的工具上限（按名字排序截断并记账）。
	MaxToolsPerServer int `toml:"max_tools_per_server"`

	// StatusLine 决定是否把 MCP 状态行注入 Director 的 system prompt。
	// 默认 true ⇒ 必须看键的存在性（Presence.StatusLine）。
	StatusLine bool `toml:"status_line"`
	// StatusMaxLines 是状态行的行数上限（超出合并成 +N more）。
	StatusMaxLines int `toml:"status_max_lines"`

	// Servers 来自 `[[mcp.servers]]`。
	Servers []MCPServerConfig `toml:"servers"`
}

// DefaultMCPConfig 是整段缺省时的取值。
func DefaultMCPConfig() MCPConfig {
	return MCPConfig{
		Enable:            true,
		StartupTimeout:    30 * time.Second,
		ReconnectBackoff:  5 * time.Second,
		MaxReconnects:     3,
		MaxToolsPerServer: 40,
		StatusLine:        true,
		StatusMaxLines:    8,
	}
}

// MCPPresence 记录 [mcp] 里哪些值被**显式**写出。
// enable 与 status_line 的缺省都是 true，而 Go 零值是 false —— 只能看键的存在性（坑 26）。
type MCPPresence struct {
	Section    bool
	Enable     bool
	StatusLine bool
}

const mcpKeyHint = "enable / startup_timeout / reconnect_backoff / max_reconnects / max_tools_per_server / status_line / status_max_lines / servers"

// mcpServerNamePattern：名字要进工具名前缀，字符集与长度都受限。
// 32 是刻意留的余量：mcp__<name>__<tool> 总长上限 64（provider 的 function name 上限）。
var mcpServerNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// reservedServerName 是内建 codeseek server 的名字。
const reservedServerName = "codeseek"

var (
	mcpMu     sync.RWMutex
	mcpConfig = DefaultMCPConfig()
)

// SetGlobalMCP 写入全局配置并归一化缺省值。
func SetGlobalMCP(c MCPConfig, p MCPPresence) {
	defaults := DefaultMCPConfig()

	if !p.Section {
		c = defaults
	} else {
		if !p.Enable {
			c.Enable = defaults.Enable
		}
		if !p.StatusLine {
			c.StatusLine = defaults.StatusLine
		}
		if c.StartupTimeout <= 0 {
			c.StartupTimeout = defaults.StartupTimeout
		}
		if c.ReconnectBackoff <= 0 {
			c.ReconnectBackoff = defaults.ReconnectBackoff
		}
		if c.MaxReconnects <= 0 {
			c.MaxReconnects = defaults.MaxReconnects
		}
		if c.MaxToolsPerServer <= 0 {
			c.MaxToolsPerServer = defaults.MaxToolsPerServer
		}
		if c.StatusMaxLines <= 0 {
			c.StatusMaxLines = defaults.StatusMaxLines
		}
	}

	mcpMu.Lock()
	mcpConfig = c
	mcpMu.Unlock()

	attrs := []any{
		"enable", c.Enable,
		"servers", len(c.Servers),
		"startup_timeout", c.StartupTimeout,
		"reconnect_backoff", c.ReconnectBackoff,
		"max_reconnects", c.MaxReconnects,
		"max_tools_per_server", c.MaxToolsPerServer,
		"status_line", c.StatusLine,
		"status_max_lines", c.StatusMaxLines,
	}
	if !MCPEnabled() {
		attrs = append(attrs, "kill_switch", "YAGENT_MCP=0")
	}
	slog.Info("mcp: 外部 server 配置", attrs...)
}

// 每个键的消费者（写在这里是为了让"写了没人读"无处藏身；AGENTS.md §11 坑 25）：
//
//	enable              → app.Init 是否构造 Manager（唯一判据 MCPEnabled）
//	startup_timeout     → Manager.WaitForReady 与单次 connect 的上界
//	reconnect_backoff   → Manager 重连退避起点（测试改 ManagerOptions 里的 seam）
//	max_reconnects      → Manager 的终态判定
//	max_tools_per_server→ tools.BuildExternalSpecs 的每 server 上限
//	status_line         → agents 的 Director prompt 是否注入状态行
//	status_max_lines    → 状态行渲染的行数上限
//	servers[...]        → app.Init 构造 []mcp.ServerConfig
//	servers[].enabled   → 该 server 是否进 Manager（指针区分"没写"与 false）
//	servers[].allow_write / read_only / tools / exclude → tools.BuildExternalSpecs 的筛选策略
//	servers[].request_timeout → mcp.NewMCPClient（<=0 → 30 只在那里兜底）
//	servers[].env / working_dir / args / command → 子进程启动参数

// GlobalMCP 返回当前生效的 [mcp] 配置。
func GlobalMCP() MCPConfig {
	mcpMu.RLock()
	defer mcpMu.RUnlock()
	return mcpConfig
}

// MCPEnabled 是这套机制的**唯一**启用判据。
// 环境变量 YAGENT_MCP=0 是 kill-switch（只认精确的 "0"），与 YAGENT_BLUEPRINT 同构。
func MCPEnabled() bool {
	if os.Getenv("YAGENT_MCP") == "0" {
		return false
	}
	return GlobalMCP().Enable
}

// validateMCPServers 校验 server 名的唯一性/字符集与 command 非空。
// 名字要进工具名前缀，拼错一个字符的后果是"工具名对不上"而不是报错 —— 所以在这里拒收。
func validateMCPServers(c MCPConfig) error {
	seen := make(map[string]bool, len(c.Servers))
	for i, s := range c.Servers {
		name := strings.TrimSpace(s.Name)
		if !mcpServerNamePattern.MatchString(name) {
			return fmt.Errorf("mcp.servers[%d].name = %q: must match ^[a-z0-9][a-z0-9_-]{0,31}$", i, s.Name)
		}
		if name == reservedServerName {
			return fmt.Errorf("mcp.servers[%d].name = %q: reserved for the built-in codeseek server", i, name)
		}
		if seen[name] {
			return fmt.Errorf("mcp.servers[%d].name = %q: duplicate server name", i, name)
		}
		seen[name] = true
		if s.IsEnabled() && strings.TrimSpace(s.Command) == "" {
			return fmt.Errorf("mcp.servers[%q].command is required when the server is enabled", name)
		}
	}
	return nil
}
