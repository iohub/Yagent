// Package compression — compressor.go — P0-1 Phase 3-0：上下文压缩编排组件。
//
// 从 DirectorAgent 抽取紧急压缩（第二级）与终极压缩（第三级，thinklink 重建）的
// 编排逻辑。依赖以窄接口/回调/参数注入。
//
// 方案C（TodoWrite 重构）批次3：终极压缩重建数据源从「Thought & Plan 块回放」
// 改为「全部用户输入 + 当前任务清单快照 + 完成台账」（设计文档 5.5）——
// 信息形态从"轨迹"转为"状态+事实"，重置横幅指令模型以任务清单为权威状态、
// 缺失细节用工具重新核实。
//
// 硬约束：compression 包不得 import agents 包——agents 类型（如 AgentResult）不进入
// 本包，涉及 agents 侧状态的清理经回调由门面适配。
package compression

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"yagent/internal/llm"
	"yagent/internal/memory"
	"yagent/internal/thinklink"
	"yagent/internal/tokenutil"
)

// ThinkLinkStore 终极压缩所需的 thinklink 窄接口（*thinklink.Store 天然实现）。
// 只暴露重建上下文所需的最小只读方法集，避免 compression 包耦合具体存储实现。
type ThinkLinkStore interface {
	// Snapshot 返回全部条目的深拷贝（用于过滤用户原始输入渲染）。
	Snapshot() []thinklink.Entry
	// CurrentTodos 返回当前任务清单条目的深拷贝；尚无任何 TodoWrite 快照时返回 nil。
	CurrentTodos() []thinklink.TodoItem
	// TodoHistory 返回最近 n 条任务清单快照（按时间序，旧→新）；n<=0 时返回全部。
	TodoHistory(n int) []thinklink.TodoSnapshot
	// CompletedLedger 返回完成台账的深拷贝。
	CompletedLedger() []thinklink.CompletedLedgerEntry
}

// ThinkLinkJournal 终极压缩的 thinklink 读写窄接口 (*thinklink.Store 天然实现):
// 只读部分 (Snapshot/CurrentTodos/TodoHistory/CompletedLedger) 供 ApplyUltimate
// 重建上下文；AddUserInput 供 Agent 主循环实时记录。
type ThinkLinkJournal interface {
	ThinkLinkStore
	AddUserInput(content string, step int) (thinklink.Entry, bool)
}

// UltimateRebuildOptions 终极压缩重建选项（每次调用传入，保持配置的动态语义）。
type UltimateRebuildOptions struct {
	// KeepTodoLedgerLimit 完成台账在重建中保留的条数上限，0=全部保留。
	// 预算不足时台账还会按降级链进一步逐条递减。
	KeepTodoLedgerLimit int
	// KeepRecentTodoSnapshots 可选历史快照段渲染的最近 N 条任务清单快照
	// 进度轨迹，0=不渲染该段（默认）。
	KeepRecentTodoSnapshots int
	// EnableLegacyThoughtPlanFallback 兼容期静默兜底（5.10-f）：true 时扫描
	// 消息历史中残留的旧格式 Thought & Plan 块，追加为可选段落（不提示、不依赖）；
	// false 时完全忽略。
	EnableLegacyThoughtPlanFallback bool
}

// UltimateCompressionStats 记录终极压缩的统计信息。
type UltimateCompressionStats struct {
	OriginalTokens   int  `json:"original_tokens"`   // 压缩前总 token
	CompressedTokens int  `json:"compressed_tokens"` // 重建后总 token
	SavedTokens      int  `json:"saved_tokens"`      // 节省的 token
	TotalTodos       int  `json:"total_todos"`       // 重建时当前任务清单条目数
	KeptLedger       int  `json:"kept_ledger"`       // 重建时保留的完成台账条数
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

// ApplyEmergency 执行紧急压缩：提取用户原始任务 + 总结/保留执行过程块，
// 覆盖 memory 为单条输入消息后返回压缩后的 messages。
// 逻辑搬迁自 DirectorAgent.applyEmergencyCompression（依赖改为注入形式）。
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
	// 强行覆盖 memory：只保留一条输入（原始任务 + 总结 + 保留的最近过程块）
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

// ─── 终极压缩重建（方案C 5.5）────────────────────────────────────────────────

// ultimateBanner 终极压缩重建横幅：重置说明（保留原有语义）+ 新增指令——
// 任务清单为权威状态；缺失细节需用工具重新核实，不得臆断。
const ultimateBanner = "Your previous conversation context has been RESET because it exceeded the token limit. " +
	"Below are the original requirements of the task, the current task list, and the completed-task ledger. " +
	"Seamlessly continue the task from where it left off: do NOT ask the user any questions, do NOT apologize, and do NOT repeat work that has already been completed.\n" +
	"IMPORTANT: The current task list is the authoritative source of task state. " +
	"If any details are missing, re-verify them with tools instead of guessing.\n"

// legacyFragmentsSection legacy 静默兜底段落标题（5.10-f：不提示、不依赖，纯防御性捕获）。
const legacyFragmentsSection = "\n=== Legacy Thought & Plan fragments ===\n"

// ApplyUltimate 终极压缩（第三级）：两级常规压缩后仍超限时，
// 用 thinklink 中保存的用户原始输入 + TodoWrite 任务清单状态
// （当前快照 + 完成台账）重建上下文。
//
//   - messages 重置为 [system..., 单条 user 重建消息]；
//   - 同步覆盖 mem（保持 memory 与 messages 一致，参照 ApplyEmergency）；
//   - 经 clearPendingSubAgent 回调清理 pendingSubAgentMemory 等可能导致
//     tool_call/tool_response 配对校验失败的残留；
//   - 预算降级链（5.5）：重建文本超 userBudget 时按序裁剪——
//     ① 可选段落（历史快照轨迹段 + legacy Thought & Plan 残留段）→
//     ② 完成台账逐条递减（keepLedger=0 时台账区省略为占位行）→
//     ③ TruncateToTokenBudget 硬截断兜底，确保重建后必然低于阈值，绝不进入死循环；
//   - opts：重建选项（台账保留上限/历史快照条数/legacy 兜底开关），
//     每次调用时传入，保持配置的动态语义。
func (c *ContextCompressor) ApplyUltimate(messages []llm.Message, threshold int, mem *memory.ConversationMemory, opts UltimateRebuildOptions) ([]llm.Message, *UltimateCompressionStats) {
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

	// 数据源（方案C 5.5）：任务状态来自 TodoWrite 快照（当前清单）与完成台账
	userInputs := filterUserInputs(c.thinkLink.Snapshot())
	currentTodos := c.thinkLink.CurrentTodos()
	ledger := c.thinkLink.CompletedLedger()

	// 台账初始保留数：配置上限生效时取配置值与全部数量的较小值（0=全部保留）
	keepLedger := len(ledger)
	if cfgKeep := opts.KeepTodoLedgerLimit; cfgKeep > 0 && cfgKeep < keepLedger {
		keepLedger = cfgKeep
	}

	// 可选段落内容：历史快照轨迹（KeepRecentTodoSnapshots>0 时）与
	// legacy Thought & Plan 残留块（兼容期静默兜底开关开启时）
	var historySnaps []thinklink.TodoSnapshot
	if opts.KeepRecentTodoSnapshots > 0 {
		historySnaps = c.thinkLink.TodoHistory(opts.KeepRecentTodoSnapshots)
	}
	var legacyBlocks []string
	if opts.EnableLegacyThoughtPlanFallback {
		legacyBlocks = collectLegacyThoughtPlanBlocks(messages)
	}

	// build 组装重建文本；withOptional=false 时省略可选段落（预算降级链 ①）
	build := func(keepLedger int, withOptional bool) string {
		var sb strings.Builder
		sb.WriteString(ultimateBanner)
		renderUserInputs(&sb, userInputs)
		renderCurrentTaskList(&sb, currentTodos)
		renderCompletedLedger(&sb, ledger, keepLedger)
		if withOptional {
			if len(historySnaps) > 0 {
				renderTodoHistory(&sb, historySnaps)
			}
			if len(legacyBlocks) > 0 {
				renderLegacyFragments(&sb, legacyBlocks)
			}
		}
		return sb.String()
	}

	prompt := build(keepLedger, true)

	// 预算降级链 ①：超预算时先裁可选段落（历史快照轨迹 + legacy 残留段）
	if tokenutil.EstimateTokens(prompt) > userBudget && (len(historySnaps) > 0 || len(legacyBlocks) > 0) {
		prompt = build(keepLedger, false)
	}

	// 预算降级链 ②：仍超预算时逐条递减台账保留数（keepLedger=0 时台账区省略为占位行）
	truncated := false
	for tokenutil.EstimateTokens(prompt) > userBudget {
		if keepLedger <= 0 {
			// 预算降级链 ③：极端兜底，硬截断确保重建后必然低于阈值
			prompt = TruncateToTokenBudget(prompt, userBudget)
			truncated = true
			break
		}
		keepLedger--
		prompt = build(keepLedger, false)
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
		TotalTodos:       len(currentTodos),
		KeptLedger:       keptLedgerCount(len(ledger), keepLedger),
		UserInputs:       len(userInputs),
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
		"total_todos", stats.TotalTodos,
		"kept_ledger", stats.KeptLedger,
		"user_inputs", stats.UserInputs,
		"truncated", stats.Truncated)

	return newMessages, stats
}

// keptLedgerCount 计算实际渲染的台账条数（total 为台账总量，keepN 为保留数；
// keepN=0 表示台账区省略，保留数为 0）。
func keptLedgerCount(total, keepN int) int {
	if keepN >= 0 && keepN < total {
		return keepN
	}
	return total
}

// filterUserInputs 从全部条目中过滤用户原始输入（保持时间序）。
func filterUserInputs(entries []thinklink.Entry) []thinklink.Entry {
	var userInputs []thinklink.Entry
	for _, e := range entries {
		if e.Kind == thinklink.KindUserInput {
			userInputs = append(userInputs, e)
		}
	}
	return userInputs
}

// renderUserInputs 渲染"Original user input(s)"区：全部用户原始输入按时间顺序
// 列出，最后一条前标注 [CURRENT TASK]（格式与终极压缩重建渲染保持一致）。
func renderUserInputs(sb *strings.Builder, userInputs []thinklink.Entry) {
	sb.WriteString("\n=== Original user input(s) ===\n")
	if len(userInputs) == 0 {
		sb.WriteString("(no user input recorded)\n")
	}
	for i, e := range userInputs {
		marker := ""
		if i == len(userInputs)-1 {
			marker = "[CURRENT TASK] "
		}
		fmt.Fprintf(sb, "\n%s[USER INPUT %d] (%s, step %d)\n%s\n",
			marker, i+1, e.Timestamp.Format(time.RFC3339), e.Step, e.Content)
	}
}

// renderCurrentTaskList 渲染"Current task list"区：当前任务清单快照按
// in_progress 置顶（标注进行中，渲染 activeForm）→ pending 次之（渲染 content）→
// completed 最后（带完成标记，渲染 activeForm 或 content）排序。
// 无任何 TodoWrite 快照时渲染"(no task list)"占位行。
func renderCurrentTaskList(sb *strings.Builder, items []thinklink.TodoItem) {
	sb.WriteString("\n=== Current task list ===\n")
	if len(items) == 0 {
		sb.WriteString("(no task list)\n")
		return
	}
	var inProgress, pending, completed []thinklink.TodoItem
	for _, it := range items {
		switch it.Status {
		case thinklink.StatusInProgress:
			inProgress = append(inProgress, it)
		case thinklink.StatusPending:
			pending = append(pending, it)
		case thinklink.StatusCompleted:
			completed = append(completed, it)
		default: // 防御：未知状态按 pending 处理
			pending = append(pending, it)
		}
	}
	for _, it := range inProgress {
		fmt.Fprintf(sb, "[IN PROGRESS] %s\n", todoActiveFormOrContent(it))
	}
	for _, it := range pending {
		fmt.Fprintf(sb, "[ ] %s\n", it.Content)
	}
	for _, it := range completed {
		fmt.Fprintf(sb, "[x] %s\n", todoActiveFormOrContent(it))
	}
}

// todoActiveFormOrContent 优先返回 activeForm（进行时态描述），为空时回退 content。
func todoActiveFormOrContent(it thinklink.TodoItem) string {
	if it.ActiveForm != "" {
		return it.ActiveForm
	}
	return it.Content
}

// renderCompletedLedger 渲染"Completed tasks"区：完成台账（content + 完成时间/step）。
// keepN>0 时仅保留最近 keepN 条；keepN=0 且台账非空表示预算降级后省略为占位行。
func renderCompletedLedger(sb *strings.Builder, ledger []thinklink.CompletedLedgerEntry, keepN int) {
	sb.WriteString("\n=== Completed tasks ===\n")
	if len(ledger) == 0 {
		sb.WriteString("(no completed tasks recorded)\n")
		return
	}
	if keepN == 0 {
		sb.WriteString("(completed tasks omitted)\n")
		return
	}
	entries := ledger
	if keepN > 0 && len(entries) > keepN {
		entries = entries[len(entries)-keepN:]
	}
	for _, le := range entries {
		fmt.Fprintf(sb, "[x] %s (completed at step %d, %s)\n",
			le.Content, le.CompletedAtStep, le.CompletedAtTime.Format(time.RFC3339))
	}
}

// renderTodoHistory 渲染可选的历史快照进度轨迹段（最近 N 条快照）。
// 仅 KeepRecentTodoSnapshots>0 时调用（默认 0 不渲染该段）。
func renderTodoHistory(sb *strings.Builder, snaps []thinklink.TodoSnapshot) {
	sb.WriteString("\n=== Todo history (recent progress snapshots) ===\n")
	for _, snap := range snaps {
		fmt.Fprintf(sb, "\n[snapshot rev %d] (%s, step %d)\n",
			snap.Revision, snap.UpdatedAt.Format(time.RFC3339), snap.Step)
		for _, it := range snap.Items {
			switch it.Status {
			case thinklink.StatusInProgress:
				fmt.Fprintf(sb, "[IN PROGRESS] %s\n", todoActiveFormOrContent(it))
			case thinklink.StatusCompleted:
				fmt.Fprintf(sb, "[x] %s\n", todoActiveFormOrContent(it))
			default:
				fmt.Fprintf(sb, "[ ] %s\n", it.Content)
			}
		}
	}
}

// renderLegacyFragments 渲染消息历史中残留的旧格式 Thought & Plan 块
// （legacy 静默兜底段：不提示、不依赖，纯防御性捕获，5.10-f）。
// 仅 EnableLegacyThoughtPlanFallback=true 且存在残留时调用。
func renderLegacyFragments(sb *strings.Builder, blocks []string) {
	sb.WriteString(legacyFragmentsSection)
	for i, b := range blocks {
		fmt.Fprintf(sb, "\n[LEGACY TP-%d]\n%s\n", i+1, b)
	}
}

// collectLegacyThoughtPlanBlocks 扫描消息历史，提取残留的旧格式 Thought & Plan 块
// （复用 emergency.go 私有化后的提取函数；5.10-f 兼容期静默兜底，不提示、不依赖）。
// 批次5 清理时随 legacy 分支一并删除。
func collectLegacyThoughtPlanBlocks(messages []llm.Message) []string {
	var blocks []string
	for _, msg := range messages {
		if msg.Role != llm.RoleAssistant || msg.Content == "" {
			continue
		}
		blocks = append(blocks, extractThoughtAndPlanBlocks(msg.Content)...)
	}
	return blocks
}
