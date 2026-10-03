// Package compression — compressor.go — P0-1 Phase 3-0：上下文压缩编排组件。
//
// 从 DirectorAgent 抽取紧急压缩（第二级）与终极压缩（第三级，thinklink 重建）的
// 编排逻辑。依赖以窄接口/回调/参数注入，压缩逻辑逐字搬迁自原 DirectorAgent 方法，
// 控制流/数字/字符串/日志文案不变，压缩行为与拆分前完全一致
// （回归防线：director_characterization_test.go）。
//
// 硬约束：compression 包不得 import agents 包——agents 类型（如 AgentResult）不进入
// 本包，涉及 agents 侧状态的清理经回调由门面适配。
package compression

import (
	"context"
	"log/slog"

	"yagent/internal/llm"
	"yagent/internal/memory"
	"yagent/internal/thinklink"
	"yagent/internal/tokenutil"
)

// ThinkLinkStore 终极压缩所需的 thinklink 窄接口（*thinklink.Store 天然实现）。
// 只暴露重建上下文所需的最小只读方法集，避免 compression 包耦合具体存储实现。
type ThinkLinkStore interface {
	// Count 返回指定类型的条目数量。
	Count(kind thinklink.Kind) int
	// RebuildPrompt 用保留的最近 keepPlans 个 Thought & Plan 块重建上下文提示。
	RebuildPrompt(keepPlans int) string
}

// ThinkLinkJournal 终极压缩的 thinklink 读写窄接口 (*thinklink.Store 天然实现):
// 只读部分 (Count/RebuildPrompt) 供 ApplyUltimate 重建上下文;
// 写入部分 (AddUserInput/AddThoughtPlan) 供 Agent 主循环实时记录。
type ThinkLinkJournal interface {
	ThinkLinkStore
	AddUserInput(content string, step int) (thinklink.Entry, bool)
	AddThoughtPlan(content string, step int) (thinklink.Entry, bool)
}

// UltimateCompressionStats 记录终极压缩的统计信息。
type UltimateCompressionStats struct {
	OriginalTokens   int  `json:"original_tokens"`   // 压缩前总 token
	CompressedTokens int  `json:"compressed_tokens"` // 重建后总 token
	SavedTokens      int  `json:"saved_tokens"`      // 节省的 token
	TotalPlans       int  `json:"total_plans"`       // thinklink 中 T&P 块总数
	KeptPlans        int  `json:"kept_plans"`        // 重建时保留的 T&P 块数
	UserInputs       int  `json:"user_inputs"`       // 重建时包含的用户输入条数
	Truncated        bool `json:"truncated"`         // 重建内容是否被硬截断（极端兜底）
}

// ContextCompressor 上下文压缩编排组件（P0-1 Phase 3-0 从 DirectorAgent 抽取）。
// 依赖注入：
//   - engine/agentName：LLM 摘要引擎与 Agent 名（原 a.LLM / a.Name()）；
//   - thinkLink：终极压缩重建上下文的 thinklink 窄接口（原 a.thinkLink，须与
//     DirectorAgent 持有同一实例，保证跨任务累积的条目可见）；
//   - clearPendingSubAgent：清理 pendingSubAgentMemory 残留的回调
//     （原 a.pendingSubAgentMemory = nil；agents 类型不得进入 compression 包）。
type ContextCompressor struct {
	engine               llm.Engine // LLM 摘要引擎（紧急压缩超块数时总结用）
	agentName            string     // Agent 名（LLM 摘要与日志用）
	thinkLink            ThinkLinkStore
	clearPendingSubAgent func()
}

// NewContextCompressor 构造上下文压缩编排组件。
func NewContextCompressor(engine llm.Engine, agentName string, thinkLink ThinkLinkStore, clearPendingSubAgent func()) *ContextCompressor {
	return &ContextCompressor{
		engine:               engine,
		agentName:            agentName,
		thinkLink:            thinkLink,
		clearPendingSubAgent: clearPendingSubAgent,
	}
}

// ShouldCompress 触发判断辅助：消息总 token 估算超过 threshold 时需要压缩。
func (c *ContextCompressor) ShouldCompress(messages []llm.Message, threshold int) bool {
	return EstimateMessagesTokens(messages) > threshold
}

// ApplyEmergency 执行紧急压缩：提取用户原始任务 + 总结/保留 Thought & Plan 历史，
// 覆盖 memory 为单条输入消息后返回压缩后的 messages。
// 逻辑逐字搬迁自 DirectorAgent.applyEmergencyCompression（依赖改为注入形式）。
func (c *ContextCompressor) ApplyEmergency(ctx context.Context, messages []llm.Message, threshold int, mem *memory.ConversationMemory) ([]llm.Message, *EmergencyCompressionStats) {
	originalInput := ""
	if mem != nil {
		for _, m := range mem.GetMessages() {
			if m.Type == memory.MessageTypeHuman {
				originalInput = m.Content
				break
			}
		}
	}
	newMessages, stats := EmergencyCompressMessages(ctx, messages, originalInput, threshold, c.engine, c.agentName, DefaultEmergencyCompressKeepLastN)
	// 强行覆盖 memory：只保留一条输入（原始任务 + 总结 + 最后 N 个 Thought & Plan）
	if mem != nil {
		if err := mem.Clear(); err != nil {
			slog.Warn("emergency compression: failed to clear memory", "error", err)
		}
		last := newMessages[len(newMessages)-1]
		if last.Role == llm.RoleUser {
			mem.AddHumanMessage(last.Content)
		}
	}
	return newMessages, stats
}

// ApplyUltimate 终极压缩（第三级）：两级常规压缩后仍超限时，
// 用 thinklink 中保存的用户原始输入 + Thought & Plan 块重建上下文。
//
//   - messages 重置为 [system..., 单条 user 重建消息]；
//   - 同步覆盖 mem（保持 memory 与 messages 一致，参照 ApplyEmergency）；
//   - 经 clearPendingSubAgent 回调清理 pendingSubAgentMemory 等可能导致
//     tool_call/tool_response 配对校验失败的残留；
//   - 循环保护：若重建后的输入自身仍超预算，逐步减少保留的 T&P 块数量
//     （保留最近 N-1、N-2……直至只留用户原始输入），仍超限则对重建内容做硬截断，
//     确保重建后必然低于阈值，绝不进入死循环；
//   - keepPlansLimit：配置指定的 T&P 块保留数量上限（0=全部保留），
//     每次调用时传入，保持配置的动态语义。
//
// 逻辑逐字搬迁自 DirectorAgent.applyUltimateCompression（依赖改为注入形式）。
func (c *ContextCompressor) ApplyUltimate(messages []llm.Message, threshold int, mem *memory.ConversationMemory, keepPlansLimit int) ([]llm.Message, *UltimateCompressionStats) {
	originalTokens := EstimateMessagesTokens(messages)

	// system（非 user）消息原样保留，不计入 user 内容预算
	nonUserTokens := 0
	newMessages := make([]llm.Message, 0, 2)
	for _, msg := range messages {
		if msg.Role == llm.RoleSystem {
			newMessages = append(newMessages, msg)
			nonUserTokens += EstimateMessagesTokens([]llm.Message{msg})
		}
	}
	userBudget := threshold - nonUserTokens
	if userBudget < 0 {
		userBudget = 0
	}

	totalPlans := c.thinkLink.Count(thinklink.KindThoughtPlan)
	keep := totalPlans
	// 配置指定保留数量上限时，取配置值与全部数量的较小值（0=全部保留）
	if cfgKeep := keepPlansLimit; cfgKeep > 0 && cfgKeep < keep {
		keep = cfgKeep
	}

	// 循环保护：从 keep 开始逐步递减；keep==0（只留用户原始输入）仍超限时硬截断兜底
	var prompt string
	truncated := false
	for {
		prompt = c.thinkLink.RebuildPrompt(keep)
		if tokenutil.EstimateTokens(prompt) <= userBudget {
			break
		}
		if keep <= 0 {
			prompt = TruncateToTokenBudget(prompt, userBudget)
			truncated = true
			break
		}
		keep--
	}

	newMessages = append(newMessages, llm.Message{
		Role:    llm.RoleUser,
		Content: prompt,
	})

	// 同步覆盖 memory：对话历史重置为该单条 user 消息，保持 memory 与 messages 一致
	if mem != nil {
		if err := mem.Clear(); err != nil {
			slog.Warn("ultimate compression: failed to clear memory", "error", err)
		}
		mem.AddHumanMessage(prompt)
	}
	// 清理可能导致配对校验失败的残留
	if c.clearPendingSubAgent != nil {
		c.clearPendingSubAgent()
	}

	stats := &UltimateCompressionStats{
		OriginalTokens:   originalTokens,
		CompressedTokens: EstimateMessagesTokens(newMessages),
		TotalPlans:       totalPlans,
		KeptPlans:        keep,
		UserInputs:       c.thinkLink.Count(thinklink.KindUserInput),
		Truncated:        truncated,
	}
	stats.SavedTokens = stats.OriginalTokens - stats.CompressedTokens
	if stats.SavedTokens < 0 {
		stats.SavedTokens = 0
	}

	slog.Warn("ultimate context compression applied: context rebuilt from thinklink",
		"original_tokens", stats.OriginalTokens,
		"compressed_tokens", stats.CompressedTokens,
		"saved_tokens", stats.SavedTokens,
		"threshold", threshold,
		"total_plans", stats.TotalPlans,
		"kept_plans", stats.KeptPlans,
		"user_inputs", stats.UserInputs,
		"truncated", stats.Truncated)

	return newMessages, stats
}