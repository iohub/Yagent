package agents

import (
	"strings"

	"yagent/internal/config"
	"yagent/internal/plasma"
)

// PlasmaDefaults 从配置派生 Elaborate 的默认值。
//
// **不许硬编码**（设计稿 §4.2）：生成出来的 Agent 必须继承这台机器上真实生效的超时、
// 压缩、并发与语言设置，否则它一出生就与内建 Agent 不同形。每一项都带"配置没写就用
// plasma 的兜底"守卫 —— DefaultDefaults() 是兜底常数。
//
// 这里的调用方有两个：`internal/app` 装配 trampoline（在线生成）与 `yagent plasma`
// 六个子命令（离线编译）。放在 agents 而不是 cmd 是为了让两处读**同一份**派生逻辑：
// 分成两份的后果是"离线编译通过的 bundle，在线上跑出来的步数/超时不一样"。
func PlasmaDefaults(cfg *config.Config) plasma.Defaults {
	d := plasma.DefaultDefaults()
	if cfg == nil {
		return d
	}
	if v := strings.TrimSpace(cfg.Agent.SpeakLang); v != "" {
		d.Lang = v
	}
	// 生成式 Agent 的步数预算沿用 Repo 的基线：两者同为"只读 + 收尾"的形状，
	// 而 Coding 的 150 步词表里有一半是写类工具。
	if v := cfg.Agent.RepoMaxSteps; v > 0 {
		d.MaxSteps = v
	}
	if v := cfg.LLM.Timeout; v > 0 {
		d.LLMTimeout = v
	}
	// 生成器本身跑在 meta 档（[routing] 的角色基线），生成出来的 Agent 从同一档起步。
	if v := strings.TrimSpace(cfg.Routing.Baselines["meta"]); v != "" {
		d.Tier = v
	}
	d.CompressionEnabled = cfg.EnhancedCommander.EnableContextCompression
	if v := cfg.EnhancedCommander.ToolResultKeepTokens; v > 0 {
		d.KeepToolResultTokens = v
	}
	if v := cfg.Agent.MaxParallelReadOnlyTools; v > 0 {
		d.MaxParallelReadOnlyTools = v
	}
	// Deny 保留 plasma 的安全基线（fs_write / shell），不从配置读：
	// 它是 ADR-4 的默认拒绝面，而不是一个可调预算。
	return d
}

// ElaborateWithDefaults 把"从原始字节到 bundle"这一步的一次性装配收敛成一个函数，
// 让 trampoline（在线）与 CLI（离线）只有一份形状。
func ElaborateWithDefaults(cfg *config.Config) func(raw []byte) (*plasma.Bundle, error) {
	d := PlasmaDefaults(cfg)
	return func(raw []byte) (*plasma.Bundle, error) { return plasma.Elaborate(raw, d) }
}
