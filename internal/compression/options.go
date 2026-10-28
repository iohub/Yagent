package compression

// Options 是上下文压缩在执行层的**唯一**选项载体。
//
// 存在的理由：同一组语义曾经在 ExecutorConfig 与 PlannerConfig 里各写一份、还各用一套名字
// （EnableContextCompression / CompressEnable、ContextCompressionThreshold / CompressThreshold、
// ToolResultKeepTokens / CompressKeepTokens、EnableUltimateCompression / UltimateCompressEnable、
// UltimateKeepPlans / UltimateCompressKeepPlans），装配层因此要复制两遍。两份同形配置必然漂移
// （根 AGENTS.md §11 坑 33），所以这里只留一份，命名保持中性、不偏向任何一侧循环。
//
// 字段与 [enhanced_commander] 的对应关系只由 internal/agents 的
// compressionOptionsFromConfig 一处维护；新增压缩配置项时只改那个函数。
//
// 零值语义：**全部零值 = 不覆盖，走 compression 包既有的兜底**。
//   - Threshold == 0 → ResolveThreshold 回落到模型目录窗口或 DefaultContextCompressionThreshold；
//   - KeepTokens == 0 → 调用方回落到 DefaultToolResultKeepTokens；
//   - UltimateKeepPlans == 0 → ApplyUltimate 全部保留。
//
// 因此这里刻意**不**重复一套默认值：默认值在 threshold.go / truncate_results.go 已经各有一份，
// 在这里再抄一份就是第二个真相。
type Options struct {
	// Enabled 是第一/二档（工具结果截断 + 紧急摘要）的总开关。
	Enabled bool
	// Threshold 是触发压缩的配置值（token 数）；0 = 交给 ResolveThreshold 决定。
	Threshold int
	// KeepTokens 是每条被截断工具结果保留的 token 数；0 = 用 DefaultToolResultKeepTokens。
	KeepTokens int

	// UltimateEnabled 是第三档（从 ThinkLink/timeline 重建上下文）的开关。
	// 第三档还需要第二个条件：重建原料非 nil（子 Agent 侧 ThinkLinkJournal、
	// Director 侧 Journal），两侧一致。
	UltimateEnabled bool
	// UltimateKeepPlans 是第三档保留的 Thought & Plan 块数上限；0 = 全部保留。
	UltimateKeepPlans int
}

// DefaultOptions 返回"什么都不覆盖"的选项：压缩关闭、其余走 compression 包既有兜底。
// 调用方（不启用压缩的 Agent）用它表达"这里没有配置"，而不是手写四个零值字段。
func DefaultOptions() Options {
	return Options{}
}
