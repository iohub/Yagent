// 本文件维护"停止/继续"（docs/pause-resume-design.md §8.4）的运行时配置。
//
// 与 blackboard.go 同构：这些预算被装配层与循环深处读取（executor 的检查点写入、
// planner 的重放预执行、TUI 的恢复入口），逐层穿参数会让六个子 Agent 的构造函数
// 各多一个入参，所以走"启动初始化 + 全局唯一事实源"。
package config

import (
	"log/slog"
	"os"
	"strings"
	"sync"
)

// 工作区变更时的两种策略。常量而不是裸字符串：判据 5 的分支只能认这两个值，
// 写错一个字母的表现是"默默按另一种策略走"，而那正是本仓库最常见的那类病。
const (
	DegradeWorkspaceWarn  = "warn"  // 工作区变了：记一条降级说明，仍然重放
	DegradeWorkspaceBlock = "block" // 工作区变了：拒绝重放，走交接报告
)

// ResumeConfig 对应 [resume]。
type ResumeConfig struct {
	// Enable 是总开关。关掉之后仍然写检查点吗？不写 —— 关掉就是"这套机制整体退出"，
	// 留半个开关会让"为什么这份 rollout 没有检查点"变成一个新问题。
	Enable bool `toml:"enable"`

	// ReplayTokenBudget 是重放上下文的 token 上限；0 = 用模型目录的阈值。
	// 超预算不是失败，是降级：调用方拿 ErrReplayTooLarge 去生成交接报告。
	ReplayTokenBudget int `toml:"replay_token_budget"`

	// MaxReplayMessages 是重建消息的条数上限。**显式写 0 = 不限**，所以它需要
	// 存在性标记来区分"没写"（默认 2000）与"写了 0 要不限"——与 [blackboard]
	// 那三个阀门同一个坑，方案的 §8.4 说"这里没有 0 有语义的键"是不准确的。
	MaxReplayMessages int `toml:"max_replay_messages"`

	// DegradeOnWorkspaceChange 取 DegradeWorkspaceWarn / DegradeWorkspaceBlock。
	DegradeOnWorkspaceChange string `toml:"degrade_on_workspace_change"`

	// CheckpointWorkspace 控制停止时跑不跑 git status。
	// 大仓库上这条命令可能很慢（虽然有 2s 预算兜着），留一个能关掉的口子。
	CheckpointWorkspace bool `toml:"checkpoint_workspace"`
}

// DefaultResumeConfig 是整段缺省时的取值。
func DefaultResumeConfig() ResumeConfig {
	return ResumeConfig{
		Enable:                   true,
		ReplayTokenBudget:        0,
		MaxReplayMessages:        2000,
		DegradeOnWorkspaceChange: DegradeWorkspaceWarn,
		CheckpointWorkspace:      true,
	}
}

// ResumePresence 记录 [resume] 里哪些值被**显式**写出（由 toml.MetaData 得出）。
type ResumePresence struct {
	Section            bool
	MaxReplayMessages  bool
	DegradeOnWorkspace bool
}

const resumeKeyHint = "enable / replay_token_budget / max_replay_messages / degrade_on_workspace_change / checkpoint_workspace"

var (
	resumeMu     sync.RWMutex
	resumeConfig = DefaultResumeConfig()
)

// SetGlobalResume 写入全局配置并归一化缺省值。
func SetGlobalResume(c ResumeConfig, p ResumePresence) {
	if !p.Section {
		c = DefaultResumeConfig()
	} else {
		if !p.MaxReplayMessages || c.MaxReplayMessages < 0 {
			c.MaxReplayMessages = DefaultResumeConfig().MaxReplayMessages
		}
		if c.ReplayTokenBudget < 0 {
			c.ReplayTokenBudget = 0
		}
		if !p.DegradeOnWorkspace {
			c.DegradeOnWorkspaceChange = DefaultResumeConfig().DegradeOnWorkspaceChange
		}
		switch strings.ToLower(strings.TrimSpace(c.DegradeOnWorkspaceChange)) {
		case DegradeWorkspaceBlock:
			c.DegradeOnWorkspaceChange = DegradeWorkspaceBlock
		case DegradeWorkspaceWarn:
			c.DegradeOnWorkspaceChange = DegradeWorkspaceWarn
		default:
			// 认不出来的策略当作"没写"处理并播报 —— 把它静默当成 warn 的话，
			// 用户写错了 block 却以为挡住了危险的重放。
			slog.Warn("resume: degrade_on_workspace_change 取值不认识，按默认处理",
				"value", c.DegradeOnWorkspaceChange, "expected", resumeKeyHint)
			c.DegradeOnWorkspaceChange = DefaultResumeConfig().DegradeOnWorkspaceChange
		}
	}

	resumeMu.Lock()
	resumeConfig = c
	resumeMu.Unlock()

	attrs := []any{
		"enable", c.Enable,
		"replay_token_budget", c.ReplayTokenBudget,
		"max_replay_messages", c.MaxReplayMessages,
		"degrade_on_workspace_change", c.DegradeOnWorkspaceChange,
		"checkpoint_workspace", c.CheckpointWorkspace,
	}
	if !ResumeEnabled() {
		attrs = append(attrs, "kill_switch", "YAGENT_RESUME=0")
	}
	slog.Info("resume: 停止/继续配置", attrs...)
}

// GlobalResume 返回当前生效的 [resume] 配置。
func GlobalResume() ResumeConfig {
	resumeMu.RLock()
	defer resumeMu.RUnlock()
	return resumeConfig
}

// ResumeEnabled 是唯一的启用判据（写检查点、读侧重放、UI 入口三处共用）。
// 环境变量 YAGENT_RESUME=0 是 kill-switch，与 YAGENT_BLACKBOARD=0 同构：
// 出问题时先关掉整套机制再排查，而不是去改配置文件再重启。
func ResumeEnabled() bool {
	if os.Getenv("YAGENT_RESUME") == "0" {
		return false
	}
	return GlobalResume().Enable
}
