package compact

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"codeactor/internal/llm"
)

// ─────────────────────────────────────────────────────────
// FoldPhase — 折叠阶段状态机
// ─────────────────────────────────────────────────────────

// FoldPhase 表示折叠操作的生命周期阶段
type FoldPhase int

const (
	// FoldPhaseStaging 暂存阶段：已创建 FoldEntry，等待提交
	FoldPhaseStaging FoldPhase = iota
	// FoldPhaseCommitted 已提交：折叠已生效，替换了原始消息
	FoldPhaseCommitted
	// FoldPhaseRolledBack 已回滚：折叠被撤销
	FoldPhaseRolledBack
)

func (p FoldPhase) String() string {
	switch p {
	case FoldPhaseStaging:
		return "staging"
	case FoldPhaseCommitted:
		return "committed"
	case FoldPhaseRolledBack:
		return "rolled_back"
	default:
		return fmt.Sprintf("unknown(%d)", int(p))
	}
}

// ─────────────────────────────────────────────────────────
// FoldEntry — 单次折叠操作的元数据
// ─────────────────────────────────────────────────────────

// FoldEntry 记录一次折叠操作的完整信息
type FoldEntry struct {
	// ID 折叠操作唯一标识
	ID string

	// Phase 当前阶段
	Phase FoldPhase

	// SourceMsgs 被折叠的原始消息（快照）
	SourceMsgs []llm.Message

	// SourceStart 在原始消息列表中的起始索引
	SourceStart int

	// SourceEnd 在原始消息列表中的结束索引（不含）
	SourceEnd int

	// SourceTokens 原始消息的 token 总数
	SourceTokens int

	// SummaryMsg 生成的摘要消息，用于替换 SourceMsgs
	SummaryMsg *llm.Message

	// SummaryTokens 摘要消息的 token 数
	SummaryTokens int

	// TokensSaved 节省的 token 数
	TokensSaved int

	// CreatedAt 创建时间
	CreatedAt time.Time

	// CommittedAt 提交时间（零值表示尚未提交）
	CommittedAt time.Time

	// FoldError 折叠过程中的错误（零值表示成功）
	FoldError error
}

// isIncremental 判断是否为增量折叠（SourceMsgs 中已包含摘要）
func (e *FoldEntry) isIncremental() bool {
	for _, msg := range e.SourceMsgs {
		if msg.Role == llm.RoleSystem && strings.HasPrefix(msg.Content, "[CONTEXT SUMMARY]") {
			return true
		}
	}
	return false
}

// ─────────────────────────────────────────────────────────
// SessionFork — Prompt Cache 优化跟踪
// ─────────────────────────────────────────────────────────

// SessionFork 记录会话分叉点，用于 Prompt Cache 命中率分析
type SessionFork struct {
	// ForkID 分叉点唯一标识
	ForkID string

	// FoldEntryID 关联的折叠条目
	FoldEntryID string

	// ForkAt 分叉时间
	ForkAt time.Time

	// BeforeTokens 分叉前的上下文 token 数
	BeforeTokens int

	// AfterTokens 折叠后的上下文 token 数
	AfterTokens int

	// CacheHitRate 缓存命中率估计值（0.0~1.0）
	CacheHitRate float64

	// Description 分叉原因描述
	Description string
}

// ─────────────────────────────────────────────────────────
// FoldManager — 折叠操作管理器
// ─────────────────────────────────────────────────────────

// FoldManager 管理会话中的折叠操作及其生命周期
type FoldManager struct {
	// entries 所有折叠条目，按时间顺序排列
	entries []*FoldEntry

	// forks 分叉点记录
	forks []*SessionFork

	// maxEntries 保留的最大折叠条目数（防止内存泄漏）
	maxEntries int

	// maxForks 保留的最大分叉记录数
	maxForks int
}

// NewFoldManager 创建折叠管理器
func NewFoldManager(maxEntries, maxForks int) *FoldManager {
	if maxEntries <= 0 {
		maxEntries = 10
	}
	if maxForks <= 0 {
		maxForks = 20
	}
	return &FoldManager{
		entries:    make([]*FoldEntry, 0, maxEntries),
		forks:      make([]*SessionFork, 0, maxForks),
		maxEntries: maxEntries,
		maxForks:   maxForks,
	}
}

// AddEntry 添加折叠条目并清理超历史记录
func (fm *FoldManager) AddEntry(entry *FoldEntry) {
	fm.entries = append(fm.entries, entry)
	if len(fm.entries) > fm.maxEntries {
		fm.entries = fm.entries[len(fm.entries)-fm.maxEntries:]
	}
}

// AddFork 添加分叉点记录并清理超历史记录
func (fm *FoldManager) AddFork(fork *SessionFork) {
	fm.forks = append(fm.forks, fork)
	if len(fm.forks) > fm.maxForks {
		fm.forks = fm.forks[len(fm.forks)-fm.maxForks:]
	}
}

// GetEntries 获取所有折叠条目
func (fm *FoldManager) GetEntries() []*FoldEntry {
	return fm.entries
}

// GetLatestEntry 获取最新的折叠条目
func (fm *FoldManager) GetLatestEntry() *FoldEntry {
	if len(fm.entries) == 0 {
		return nil
	}
	return fm.entries[len(fm.entries)-1]
}

// GetCommittedSummaries 获取所有已提交的摘要消息
func (fm *FoldManager) GetCommittedSummaries() []*llm.Message {
	var summaries []*llm.Message
	for _, e := range fm.entries {
		if e.Phase == FoldPhaseCommitted && e.SummaryMsg != nil {
			summaries = append(summaries, e.SummaryMsg)
		}
	}
	return summaries
}

// TotalSavedTokens 计算所有已提交折叠节省的 token 总数
func (fm *FoldManager) TotalSavedTokens() int {
	total := 0
	for _, e := range fm.entries {
		if e.Phase == FoldPhaseCommitted {
			total += e.TokensSaved
		}
	}
	return total
}

// ─────────────────────────────────────────────────────────
// SummarizationClient 扩展接口
// ─────────────────────────────────────────────────────────

// ExtendedSummarizationClient 扩展的摘要接口，支持全量/增量摘要
type ExtendedSummarizationClient interface {
	// GenerateSummary 生成摘要（兼容旧接口）
	GenerateSummary(ctx context.Context, messages []llm.Message) (string, error)

	// FullSummarize 全量摘要（指定系统提示词）
	FullSummarize(ctx context.Context, systemPrompt string, messages []llm.Message) (string, error)

	// IncrementalSummarize 增量摘要（合并已有摘要和新消息）
	IncrementalSummarize(ctx context.Context, existingSummary string, newMessages []llm.Message) (string, error)
}

// ─────────────────────────────────────────────────────────
// Summary 提示词
// ─────────────────────────────────────────────────────────

// foldSummaryPrompt 折叠摘要提示词
// 与 defaultSummarizationPrompt 类似，但强调"折叠"场景
var foldSummaryPrompt = `# Role
You are a **Conversation Folder** for an AI coding assistant. Your task is to compress a conversation fragment into a concise yet complete summary.

# Task
Produce a compact summary preserving ALL critical context:

1. **Task Progress**: What has been done? What remains?
2. **Key Decisions**: Architecture/design choices and rationale.
3. **Code Changes**: Modified files, key patterns, imports.
4. **Errors & Fixes**: Concrete errors and solutions.
5. **Critical Discoveries**: Codebase facts — structure, deps, conventions.

# Rules
- **Preserve Identifiers**: ALL file names, function names, class names, variable names, paths MUST be retained.
- **Preserve Error Details**: Error messages and fix strategies verbatim.
- **Ignore Tool Noise**: Skip verbose tool output; keep only meaningful results.
- **Be Complete**: Nothing critical may be omitted.
- **Be Concise**: Bullet points preferred over prose.

# Output Format
- Structured Markdown in **English**.
- Organize under the 5 categories above.
- No meta-commentary, no markdown fences.`

// ─────────────────────────────────────────────────────────
// Engine 折叠配置
// ─────────────────────────────────────────────────────────

// foldContextConfig 折叠上下文配置（内部结构体）
type foldContextConfig struct {
	// KeepRecentRounds 始终保留的最近轮数（非锚定消息的边界）
	KeepRecentRounds int

	// MinFoldTokens 最小折叠 token 数，低于此值不触发折叠
	MinFoldTokens int

	// UseIncremental 是否优先使用增量折叠
	UseIncremental bool

	// Manager 折叠管理器（可选）
	Manager *FoldManager
}

// FoldContextConfig 公开的折叠配置构建器
func FoldContextConfig(opts ...FoldContextConfigOption) foldContextConfig {
	cfg := foldContextConfig{
		KeepRecentRounds: 3,
		MinFoldTokens:    1000, // 至少节省 1000 token 才折叠
		UseIncremental:   true,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return cfg
}

// FoldContextConfigOption 折叠配置选项
type FoldContextConfigOption func(*foldContextConfig)

// WithKeepRecentRounds 设置保留轮数
func WithKeepRecentRounds(n int) FoldContextConfigOption {
	return func(c *foldContextConfig) { c.KeepRecentRounds = n }
}

// WithMinFoldTokens 设置最小折叠 token 数
func WithMinFoldTokens(n int) FoldContextConfigOption {
	return func(c *foldContextConfig) { c.MinFoldTokens = n }
}

// WithoutIncremental 禁用增量折叠
func WithoutIncremental() FoldContextConfigOption {
	return func(c *foldContextConfig) { c.UseIncremental = false }
}

// WithFoldManager 设置折叠管理器
func WithFoldManager(m *FoldManager) FoldContextConfigOption {
	return func(c *foldContextConfig) { c.Manager = m }
}

// ─────────────────────────────────────────────────────────
// Engine 折叠方法
// ─────────────────────────────────────────────────────────

// foldContext 主入口：将旧消息分组后通过 LLM 生成摘要替换原始消息
//
// 参数：
//   - ctx: 上下文（含超时控制）
//   - msgs: 完整的消息列表
//   - cc: 折叠配置
//
// 流程：
//   1. 找到可折叠区域（keepBoundary 之前的非锚定消息）
//   2. 检查是否有已有摘要（增量折叠）
//   3. 调用 LLM 生成摘要
//   4. 返回 FoldEntry（包含摘要消息，由调用者替换 msgs）
//   5. 记录到 FoldManager
//
// 返回值：
//   - *FoldEntry: 折叠条目（包含摘要和统计信息）
//   - error: 折叠失败时的错误
func (e *Engine) foldContext(ctx context.Context, msgs []llm.Message, cc foldContextConfig) (*FoldEntry, error) {
	// 尝试将 summarizer 断言为 ExtendedSummarizationClient
	var es ExtendedSummarizationClient
	var ok bool
	if es, ok = e.summarizer.(ExtendedSummarizationClient); !ok {
		return nil, fmt.Errorf("summarizer does not support extended operations (need FullSummarize/IncrementalSummarize)")
	}
	return e.foldContextWithClient(ctx, msgs, cc, es)
}

// foldContextWithClient 使用指定的摘要客户端执行折叠
func (e *Engine) foldContextWithClient(ctx context.Context, msgs []llm.Message, cc foldContextConfig, summarizer ExtendedSummarizationClient) (*FoldEntry, error) {
	if summarizer == nil {
		return nil, fmt.Errorf("summarizer is nil")
	}
	if len(msgs) == 0 {
		return nil, fmt.Errorf("no messages to fold")
	}

	// 1. 创建 FoldEntry（Staging 阶段）
	entry := &FoldEntry{
		ID:        fmt.Sprintf("fold-%d", time.Now().UnixNano()),
		Phase:     FoldPhaseStaging,
		CreatedAt: time.Now(),
	}

	if cc.Manager != nil {
		cc.Manager.AddEntry(entry)
	}

	// 2. 找到可折叠区域
	foldable, sourceStart, sourceEnd := findFoldableRegion(msgs, cc.KeepRecentRounds)
	entry.SourceMsgs = foldable
	entry.SourceStart = sourceStart
	entry.SourceEnd = sourceEnd

	if len(foldable) == 0 {
		entry.FoldError = fmt.Errorf("no foldable messages found")
		entry.Phase = FoldPhaseRolledBack
		return entry, nil
	}

	// 3. 计算原始 token 数
	sourceTokens := e.countMessagesTokens(foldable)
	entry.SourceTokens = sourceTokens

	// 4. 检查是否满足最小折叠 token 数
	if sourceTokens < cc.MinFoldTokens {
		entry.FoldError = fmt.Errorf("foldable region too small: %d tokens < %d minimum", sourceTokens, cc.MinFoldTokens)
		entry.Phase = FoldPhaseRolledBack
		slog.Debug("foldContext skipped: foldable region too small",
			"entry_id", entry.ID,
			"tokens", sourceTokens,
			"min_tokens", cc.MinFoldTokens)
		return entry, nil
	}

	// 5. 检查是否有已有摘要（增量折叠）
	existingSummary := ""
	newMessages := foldable
	if cc.UseIncremental {
		existingSummary = findExistingSummaryInMessages(foldable)
		if existingSummary != "" {
			// 找到已有摘要，提取新消息
			for i, msg := range foldable {
				if msg.Role == llm.RoleSystem && strings.HasPrefix(msg.Content, "[CONTEXT SUMMARY]") {
					newMessages = foldable[i+1:]
					break
				}
			}
		}
	}

	// 6. 调用 LLM 生成摘要
	var summary string
	var err error

	if cc.UseIncremental && existingSummary != "" && len(newMessages) > 0 {
		// 增量折叠
		summary, err = e.doIncrementalFold(ctx, summarizer, existingSummary, newMessages)
	} else {
		// 全量折叠
		summary, err = e.doFullFold(ctx, summarizer, foldable)
	}

	if err != nil {
		entry.FoldError = fmt.Errorf("summarization failed: %w", err)
		entry.Phase = FoldPhaseRolledBack
		slog.Error("foldContext failed", "entry_id", entry.ID, "error", err)
		return entry, fmt.Errorf("foldContext failed for %s: %w", entry.ID, err)
	}

	// 7. 构建摘要消息
	cleanedSummary := CleanSummaryOutput(summary)
	summaryMsg := &llm.Message{
		Role:    llm.RoleSystem,
		Content: "[CONTEXT SUMMARY (FOLDED)]\n" + cleanedSummary,
	}

	// 8. 计算摘要 token 数
	summaryTokens := e.countMessagesTokens([]llm.Message{*summaryMsg})
	entry.SummaryMsg = summaryMsg
	entry.SummaryTokens = summaryTokens
	entry.TokensSaved = sourceTokens - summaryTokens

	slog.Info("foldContext completed",
		"entry_id", entry.ID,
		"source_tokens", sourceTokens,
		"summary_tokens", summaryTokens,
		"tokens_saved", entry.TokensSaved,
		"is_incremental", cc.UseIncremental && existingSummary != "",
		"new_messages_count", len(newMessages),
	)

	return entry, nil
}

// doFullFold 全量折叠：对一组消息生成完整摘要
func (e *Engine) doFullFold(ctx context.Context, summarizer ExtendedSummarizationClient, messages []llm.Message) (string, error) {
	// 尝试使用扩展接口
	if es, ok := summarizer.(interface {
		FullSummarize(ctx context.Context, systemPrompt string, messages []llm.Message) (string, error)
	}); ok {
		return es.FullSummarize(ctx, foldSummaryPrompt, messages)
	}

	// 回退到 GenerateSummary
	return summarizer.GenerateSummary(ctx, messages)
}

// doIncrementalFold 增量折叠：合并已有摘要和新消息
func (e *Engine) doIncrementalFold(ctx context.Context, summarizer ExtendedSummarizationClient, existingSummary string, newMessages []llm.Message) (string, error) {
	// 尝试使用扩展接口
	if es, ok := summarizer.(interface {
		IncrementalSummarize(ctx context.Context, existingSummary string, newMessages []llm.Message) (string, error)
	}); ok {
		return es.IncrementalSummarize(ctx, existingSummary, newMessages)
	}

	// 回退：手动构建增量输入，使用 GenerateSummary
	var sb strings.Builder
	sb.WriteString("[EXISTING SUMMARY]\n")
	sb.WriteString(existingSummary)
	sb.WriteString("\n\n[NEW MESSAGES TO INCORPORATE]\n")
	for _, msg := range newMessages {
		sb.WriteString(fmt.Sprintf("[%s] %s\n", msg.Role, msg.Content))
		if len(msg.ToolCalls) > 0 {
			for _, tc := range msg.ToolCalls {
				sb.WriteString(fmt.Sprintf("  → ToolCall: %s(%s)\n", tc.Function.Name, tc.Function.Arguments))
			}
		}
	}

	incMsgs := []llm.Message{
		{Role: llm.RoleUser, Content: sb.String()},
	}
	return summarizer.GenerateSummary(ctx, incMsgs)
}

// findFoldableRegion 找到可折叠的消息区域
//
// 返回：(可折叠消息, 起始索引, 结束索引（不含）)
//
// 可折叠区域 = 非 System 消息 + 非锚定消息 + keepBoundary 之前
//
// 逻辑与 compact.go 中的 extractRecentMessages 对齐：
// 1. 从后往前数 keepRounds*2 条消息作为保留区
// 2. 确保 tool_call/tool_response 不被拆分
// 3. 锚定消息被移到保留区
// 4. 剩余的即为可折叠区域
func findFoldableRegion(msgs []llm.Message, keepRounds int) ([]llm.Message, int, int) {
	if len(msgs) == 0 {
		return nil, 0, 0
	}

	// 复用 extractRecentMessages 获取 recent 和 older 分区
	_, older := extractRecentMessages(msgs, keepRounds)

	// 计算 older 在原始 msgs 中的索引范围
	var sourceStart, sourceEnd int
	foundStart := false

	for i, msg := range msgs {
		// 检查此消息是否在 older 中
		for _, om := range older {
			// 通过身份比较（同一切片元素）
			if &msg == &om {
				if !foundStart {
					sourceStart = i
					foundStart = true
				}
				sourceEnd = i + 1
			}
		}
	}

	// 如果没找到（older 是深拷贝导致引用不同），尝试内容匹配
	if !foundStart && len(older) > 0 {
		// 找到非 System、非锚定且在 recent 之前的消息
		_, recent := extractRecentMessages(msgs, keepRounds)
		recentSet := make(map[string]bool)
		for _, rm := range recent {
			key := string(rm.Role) + rm.Content
			recentSet[key] = true
		}

		for i, msg := range msgs {
			key := string(msg.Role) + msg.Content
			if recentSet[key] || msg.Role == llm.RoleSystem || msg.IsAnchored {
				continue
			}
			if !foundStart {
				sourceStart = i
				foundStart = true
			}
			sourceEnd = i + 1
		}
	}

	return older, sourceStart, sourceEnd
}

// findExistingSummaryInMessages 在消息列表中查找已有的 [CONTEXT SUMMARY] 摘要
// 返回摘要内容（不含前缀），如果不存在则返回空字符串
func findExistingSummaryInMessages(msgs []llm.Message) string {
	for _, msg := range msgs {
		if msg.Role == llm.RoleSystem && strings.HasPrefix(msg.Content, "[CONTEXT SUMMARY]") {
			// 提取摘要文本（去掉前缀）
			content := msg.Content
			content = strings.TrimPrefix(content, "[CONTEXT SUMMARY (FOLDED)]")
			content = strings.TrimPrefix(content, "[CONTEXT SUMMARY (EMERGENCY FOLD)]")
			content = strings.TrimPrefix(content, "[CONTEXT SUMMARY]")
			content = strings.TrimPrefix(content, "\n")
			return content
		}
	}
	return ""
}

// findExistingSummary 是 Engine 上的便捷方法，委托给包级函数
func (e *Engine) findExistingSummary(msgs []llm.Message) string {
	return findExistingSummaryInMessages(msgs)
}

// emergencyFold 简化版折叠（无两阶段，直接压缩）
// 适用于紧急情况：快速折叠指定消息范围，不经过 Staging → Committed 流程
//
// 参数：
//   - ctx: 上下文（含超时控制）
//   - msgs: 完整的消息列表
//
// 返回值：
//   - []llm.Message: 折叠后的完整消息列表（包含替换后的摘要）
//   - error: 折叠失败时的错误（nil 表示成功或无需折叠）
//
// 流程：
//   1. 提取 System 消息
//   2. 提取可折叠区域（非 System，非最近 keepRounds 轮，非锚定）
//   3. 查找已有摘要（增量折叠优先）
//   4. 调用 LLM 生成摘要
//   5. 构建 [System] + [Summary] + [Recent] 布局
func (e *Engine) emergencyFold(ctx context.Context, msgs []llm.Message) ([]llm.Message, error) {
	if len(msgs) == 0 {
		return msgs, nil
	}

	if e.summarizer == nil {
		return nil, fmt.Errorf("emergencyFold: no summarizer available")
	}

	// 断言为扩展接口
	extSummarizer, ok := e.summarizer.(ExtendedSummarizationClient)
	if !ok {
		return nil, fmt.Errorf("emergencyFold: summarizer does not support extended operations")
	}

	ctx, cancel := context.WithTimeout(ctx, e.config.SummarizationTimeout)
	defer cancel()

	// 1. 提取 System 消息
	systemMsg := extractSystemMessage(msgs)
	systemContent := ""
	if systemMsg != nil {
		systemContent = systemMsg.Content
	}

	// 2. 提取可折叠区域（非 System，非最近 keepRounds 轮）
	_, older := extractRecentMessages(msgs, e.config.KeepRecentRounds)

	if len(older) == 0 {
		return msgs, nil // 无可折叠区域
	}

	// 3. 查找已有摘要（增量）
	existingSummary := e.findExistingSummary(older)
	var summary string

	if existingSummary != "" {
		// 增量折叠
		var newMessages []llm.Message
		for _, msg := range older {
			if msg.Role == llm.RoleSystem && strings.HasPrefix(msg.Content, "[CONTEXT SUMMARY]") {
				continue
			}
			newMessages = append(newMessages, msg)
		}
		if len(newMessages) > 0 {
			summary, _ = e.doIncrementalFold(ctx, extSummarizer, existingSummary, newMessages)
		} else {
			summary = existingSummary
		}
	} else {
		// 全量折叠
		summary, _ = e.doFullFold(ctx, extSummarizer, older)
	}

	if summary == "" {
		// 摘要生成失败，返回原始消息
		return msgs, nil
	}

	// 4. 构建折叠后的消息列表
	result := make([]llm.Message, 0, 2+len(msgs)-len(older))

	// System 消息
	if systemContent != "" {
		result = append(result, llm.Message{
			Role:    llm.RoleSystem,
			Content: systemContent,
		})
	}

	// 摘要消息
	result = append(result, llm.Message{
		Role:    llm.RoleSystem,
		Content: "[CONTEXT SUMMARY (EMERGENCY FOLD)]\n" + CleanSummaryOutput(summary),
	})

	// Recent 消息（older 在 extractRecentMessages 中是 recent，这里需要重新获取）
	recent, _ := extractRecentMessages(msgs, e.config.KeepRecentRounds)
	result = append(result, recent...)

	slog.Info("emergencyFold completed",
		"source_tokens", e.countMessagesTokens(older),
		"kept_messages", len(result),
	)

	return result, nil
}

// ─────────────────────────────────────────────────────────
// SummaryAdapter 扩展：实现 ExtendedSummarizationClient 接口
// ─────────────────────────────────────────────────────────

// FullSummarize 实现扩展接口的全量摘要
func (a *SummaryAdapter) FullSummarize(ctx context.Context, systemPrompt string, messages []llm.Message) (string, error) {
	systemMsg := llm.Message{
		Role:    llm.RoleSystem,
		Content: systemPrompt,
	}
	allMessages := append([]llm.Message{systemMsg}, messages...)

	opts := &llm.CallOptions{
		MaxTokens:   a.MaxTokens,
		Temperature: a.Temperature,
	}

	resp, err := a.LLM.GenerateContent(ctx, allMessages, nil, opts)
	if err != nil {
		return "", fmt.Errorf("full summarization failed: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("full summarization returned empty response")
	}

	return resp.Choices[0].Content, nil
}

// IncrementalSummarize 实现扩展接口的增量摘要
func (a *SummaryAdapter) IncrementalSummarize(ctx context.Context, existingSummary string, newMessages []llm.Message) (string, error) {
	// 构建增量摘要提示词
	incrementalPrompt := `You are a conversation summarizer for an AI coding assistant.

Your task is to MERGE new conversation messages into an existing summary, producing an updated comprehensive summary.

## Rules
- PRESERVE all information from the existing summary - do not lose any key facts
- INCORPORATE new information from the new messages
- DEDUPLICATE overlapping content
- Keep identifiers (file names, function names, paths) intact
- Keep error messages verbatim
- Be concise but complete

## Output
Output ONLY the updated summary text. No meta-commentary, no markdown fences.`

	incrementalMsg := llm.Message{
		Role:    llm.RoleSystem,
		Content: incrementalPrompt,
	}

	// 将已有摘要 + 新消息打包
	var sb strings.Builder
	sb.WriteString("[EXISTING SUMMARY]\n")
	sb.WriteString(existingSummary)
	sb.WriteString("\n\n[NEW MESSAGES TO INCORPORATE]\n")
	for _, msg := range newMessages {
		sb.WriteString(fmt.Sprintf("[%s] %s\n", msg.Role, msg.Content))
		if len(msg.ToolCalls) > 0 {
			for _, tc := range msg.ToolCalls {
				sb.WriteString(fmt.Sprintf("  → ToolCall: %s(%s)\n", tc.Function.Name, tc.Function.Arguments))
			}
		}
	}

	userMsg := llm.Message{
		Role:    llm.RoleUser,
		Content: sb.String(),
	}

	allMessages := []llm.Message{incrementalMsg, userMsg}

	opts := &llm.CallOptions{
		MaxTokens:   a.MaxTokens,
		Temperature: a.Temperature,
	}

	resp, err := a.LLM.GenerateContent(ctx, allMessages, nil, opts)
	if err != nil {
		return "", fmt.Errorf("incremental summarization failed: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("incremental summarization returned empty response")
	}

	return resp.Choices[0].Content, nil
}
