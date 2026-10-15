// 本文件维护 deferred tool schema（docs/tool-registry-design.md §7）的运行时配置。
//
// 与各 Agent 局部配置的关系：工具面是"每次 Run 重建一次"的东西，而注册表与画像
// 都是全局的，所以这里与 llm_timeout.go 一样采用"启动时初始化 + 全局唯一事实源"。
// 不放进 ExecutorConfig 是为了避免 6 个 Agent 的构造函数与 app.go 各多穿一个参数。
package config

import (
	"log/slog"
	"sync"
)

// DeferredToolsConfig 对应 [tools.deferred]。
type DeferredToolsConfig struct {
	// Disable=true 时整体退回历史行为：所有工具每步都带完整参数声明，
	// 不生成 deferred 目录，也不注册 load_tool。留给排障与 A/B 对照。
	Disable bool `toml:"disable"`

	// BudgetTokens 是每步工具声明的预算。超出时把 Warm 工具按声明体积从大到小
	// 降级成 Cold。0 表示不设预算（只按 Spec 声明的档位）。
	BudgetTokens int `toml:"budget_tokens"`
}

// DefaultDeferredToolsConfig 是缺省值：deferred 开启，预算 2500 token
// （按 Coding 画像的 Hot 集 2224 token 留约 12% 余量定的）。
func DefaultDeferredToolsConfig() DeferredToolsConfig {
	return DeferredToolsConfig{BudgetTokens: 2500}
}

var (
	deferredMu     sync.RWMutex
	deferredConfig = DefaultDeferredToolsConfig()
)

// SetGlobalDeferredTools 写入全局配置。BudgetTokens=0 表示"配置文件里没写这一段"，
// 归一化成缺省预算；负值是显式意图（不设预算），原样保留。
func SetGlobalDeferredTools(c DeferredToolsConfig) {
	if c.BudgetTokens == 0 {
		c.BudgetTokens = DefaultDeferredToolsConfig().BudgetTokens
	}
	deferredMu.Lock()
	deferredMu.Unlock()
	deferredConfig = c
	if !c.Disable {
		slog.Info("deferred tool schema enabled", "budget_tokens", c.BudgetTokens)
	} else {
		slog.Info("deferred tool schema disabled")
	}
}

// GlobalDeferredTools 返回当前生效的 deferred 配置。
func GlobalDeferredTools() DeferredToolsConfig {
	deferredMu.RLock()
	defer deferredMu.RUnlock()
	out := deferredConfig
	return out
}
