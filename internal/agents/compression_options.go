package agents

import (
	"yagent/internal/compression"
	"yagent/internal/config"
)

// compressionOptionsFromConfig 是 [enhanced_commander] 压缩段到执行层选项的**唯一**转换点。
//
// 它同时服务两侧装配：四个启用压缩的子 Agent（repo / coding / devops / browser，
// 各自填 ExecutorConfig.Compression）与 Director（填 PlannerConfig.Compression）。
// 在此之前这两条路径各自逐字段复制同一组语义、还各用一套名字，新增一个压缩配置项
// 必须记得改两处 —— 漏一处的表现是"配置文件里写了，但那一侧的行为没变"，
// 而且编译与测试都不会响（根 AGENTS.md §11 坑 33、internal/agents/AGENTS.md 硬约束 1）。
//
// 参数是值而不是指针：调用方持有的都是 `config.EnhancedCommanderConfig` 值
// （DirectorAgent.EnhancedCommanderCfg / 各 Agent 的 ecCfg），归一化在 config 层完成，
// 所以这里不需要 nil 分支 —— 与 browserctl.FromAppConfig 同形。
// 新增压缩配置项时只改这一个函数。
func compressionOptionsFromConfig(ec config.EnhancedCommanderConfig) compression.Options {
	return compression.Options{
		Enabled:           ec.EnableContextCompression,
		Threshold:         ec.ContextCompressionThreshold,
		KeepTokens:        ec.ToolResultKeepTokens,
		UltimateEnabled:   ec.EnableUltimateCompression,
		UltimateKeepPlans: ec.UltimateCompressionKeepPlans,
	}
}
