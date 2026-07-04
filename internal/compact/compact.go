package compact

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"codeactor/internal/llm"
)

// Config 压缩配置（精简版）
type Config struct {
	// MaxContextTokens 最大上下文token数，默认 128000
	MaxContextTokens int `toml:"max_context_tokens"`

	// EnableAutoCompact 是否自动触发压缩
	EnableAutoCompact bool `toml:"enable_auto_compact"`

	// KeepRecentRounds 始终保留的最近对话轮数（默认 3）
	KeepRecentRounds int `toml:"keep_recent_rounds"`

	// SummarizationTimeout 摘要超时时间（默认 60s）
	SummarizationTimeout time.Duration `toml:"summarization_timeout"`

	// SummarizationMaxInputTokens 摘要时单批次最大输入token数（默认 100000）
	SummarizationMaxInputTokens int `toml:"summarization_max_input_tokens"`

	// MaxToolOutputTokens 单条工具输出 token 上限，0 表示不限制
	MaxToolOutputTokens int `toml:"max_tool_output_tokens"`

	// ToolPreviewTokens 工具输出预览 token 数（默认 128）
	ToolPreviewTokens int `toml:"tool_preview_tokens"`

	// MicroCompressEnabled 是否启用微压缩（Layer 3：语义占位符替换）
	MicroCompressEnabled bool `toml:"micro_compress_enabled"`

	// MicroCompressTools 需要微压缩的工具列表（白名单）
	MicroCompressTools []string `toml:"micro_compress_tools"`

	// ── Layer 1: 工具结果预算控制 ──
	// MaxToolOutputTokens > 0 时启用

	// ── Layer 4: 上下文折叠 ──

	// FoldEnabled 是否启用上下文折叠（Layer 4）
	FoldEnabled bool `toml:"fold_enabled"`

	// ── Layer 6: 状态补偿 ──

	// CompensateEnabled 是否启用状态补偿（Layer 6）
	CompensateEnabled bool `toml:"compensate_enabled"`

	// ── Layer 1: 外部存储 ──

	// OffloadEnabled 是否启用外部存储（用于超限工具输出）
	OffloadEnabled bool `toml:"offload_enabled"`

	// OffloadPath 外部存储根路径
	OffloadPath string `toml:"offload_path"`

	// ── 动态阈值 ──

	// CompressionDirection 压缩方向策略："auto" | "recent" | "old"
	CompressionDirection string `toml:"compression_direction"`

	// MinPrunableAge 消息最小年龄（轮数），小于此值的消息不参与修剪
	MinPrunableAge int `toml:"min_prunable_age"`

	// ── Emergency / 应急配置 ──

	// EmergencyMaxTokens 应急压缩的最大 token 预算，0 表示使用 MaxContextTokens
	EmergencyMaxTokens int `toml:"emergency_max_tokens"`

	// EmergencyCBThreshold 熔断器连续失败阈值，0 表示使用默认值 3
	EmergencyCBThreshold int `toml:"emergency_cb_threshold"`

	// EmergencyCBResetDuration 熔断器重置持续时间（Open → HalfOpen），0 表示使用默认值 30s
	EmergencyCBResetDuration time.Duration `toml:"emergency_cb_reset_duration"`
}

// DefaultConfig 默认配置
var DefaultConfig = Config{
	MaxContextTokens:            128000,
	EnableAutoCompact:           true,
	KeepRecentRounds:            3,
	SummarizationTimeout:        60 * time.Second,
	SummarizationMaxInputTokens: 100000,
	MaxToolOutputTokens:         0,    // 0 = 不限制
	ToolPreviewTokens:           128,
	MicroCompressEnabled:        true, // Layer 3: 微压缩默认启用
	MicroCompressTools:          []string{"run_bash", "read_file", "list_files", "grep", "search"},
}

// CompressResult 压缩结果
type CompressResult struct {
	CompressedMessages []llm.Message
	OriginalTokens     int
	CompressedTokens   int
	CompressionRatio   float64 // 压缩比 (0~1)，越小压缩越多
	CompressionStats   string  // 压缩统计信息
	SummaryInfo        string  // 摘要文本（可选）
}

// SummarizationClient 摘要LLM客户端接口
type SummarizationClient interface {
	GenerateSummary(ctx context.Context, messages []llm.Message) (string, error)
}

// Engine 压缩引擎（7层漏斗式管道编排）
type Engine struct {
	config       *Config
	tokenizer    Tokenizer
	summarizer   SummarizationClient
	frozenSummary string        // 已冻结的历史摘要，用于增量压缩
	offload      *OffloadStorage // 外部存储，用于超限工具输出

	// ── Layer 7: Emergency ──
	emergencyConfig *EmergencyConfig // 应急配置（懒初始化）
	cb              *CircuitBreaker  // 熔断器

	// ── Layer 4: Fold ──
	foldManager *FoldManager // 折叠管理器

	// ── Layer 6: Compensate ──
	extractor *StateExtractor // 状态提取器

	// ── Dynamic Threshold ──
	dynamicEngine *DynamicEngine // 动态阈值引擎
	dynamicConfig *DynamicConfig // 动态阈值配置

	// ── 统计 ──
	statsLock         sync.Mutex
	totalCompressions int
	layerStats        map[string]int // 各层触发次数
}

// SetOffloadStorage 设置外部存储（用于超限工具输出外存）
func (e *Engine) SetOffloadStorage(storage *OffloadStorage) {
	e.offload = storage
}

// NewEngine 创建压缩引擎（初始化 7 层管道组件）
func NewEngine(config *Config, summarizer SummarizationClient) (*Engine, error) {
	if config == nil {
		config = &DefaultConfig
	}
	if config.MaxContextTokens <= 0 {
		config.MaxContextTokens = DefaultConfig.MaxContextTokens
	}
	if config.KeepRecentRounds <= 0 {
		config.KeepRecentRounds = DefaultConfig.KeepRecentRounds
	}
	if config.SummarizationTimeout <= 0 {
		config.SummarizationTimeout = DefaultConfig.SummarizationTimeout
	}
	if config.SummarizationMaxInputTokens <= 0 {
		config.SummarizationMaxInputTokens = DefaultConfig.SummarizationMaxInputTokens
	}
	if config.MaxToolOutputTokens < 0 {
		config.MaxToolOutputTokens = 0
	}
	if config.ToolPreviewTokens <= 0 {
		config.ToolPreviewTokens = DefaultConfig.ToolPreviewTokens
	}
	// Emergency defaults
	if config.EmergencyCBThreshold <= 0 {
		config.EmergencyCBThreshold = 3
	}
	if config.EmergencyCBResetDuration <= 0 {
		config.EmergencyCBResetDuration = 30 * time.Second
	}

	tokenizer := GetGlobalTokenizer()

	// ── Layer 1: 外部存储 ──
	var offload *OffloadStorage
	if config.MaxToolOutputTokens > 0 && config.OffloadEnabled && config.OffloadPath != "" {
		var offloadErr error
		offload, offloadErr = NewOffloadStorage(config.OffloadPath, "default", 100*1024*1024)
		if offloadErr != nil {
			slog.Warn("Offload storage init failed, continuing without it", "error", offloadErr)
		}
	}

	// ── Layer 7: 熔断器 ──
	cb := NewCircuitBreaker(config.EmergencyCBThreshold, config.EmergencyCBResetDuration)

	// ── Dynamic Threshold ──
	dynamicCfg := &DynamicConfig{
		CompressionDirection:    "auto",
		SummaryReservedFraction: 0.165,
		BufferBandTokens:        13000,
		AutoOldRatioThreshold:   0.4,
		AutoLookbackPercent:     0.3,
	}
	if config.CompressionDirection != "" {
		dynamicCfg.CompressionDirection = config.CompressionDirection
	}
	dynamicEngine := NewDynamicEngine(config, tokenizer)
	dynamicEngine.SetConfig(dynamicCfg)

	return &Engine{
		config:        config,
		tokenizer:     tokenizer,
		summarizer:    summarizer,
		offload:       offload,
		cb:            cb,
		foldManager:   NewFoldManager(100, 50),
		extractor:     NewStateExtractor(),
		dynamicEngine: dynamicEngine,
		dynamicConfig: dynamicCfg,
		layerStats:    make(map[string]int),
	}, nil
}

// Compress 执行 7 层漏斗式管道压缩
//
// 管道架构（从低成本到高成本）：
//   Layer 1: 工具结果预算控制 — 截断/外存/替换 verbose 工具输出
//   Layer 2: 老消息剪裁       — 基于原子组的旧消息修剪
//   Layer 3: 微压缩           — 语义占位符替换
//   Layer 4: 上下文折叠       — LLM 生成折叠摘要
//   Layer 5: 自动压缩（兜底）  — 原有 LLM 摘要逻辑
//   Layer 6: 状态补偿          — 重新注入关键状态
//   Layer 7: 应急压缩          — 熔断器保护的极限压缩
//
// 每层执行后检查预算，提前退出避免不必要的高成本操作。
func (e *Engine) Compress(ctx context.Context, messages []llm.Message) (*CompressResult, error) {
	if len(messages) == 0 {
		return &CompressResult{
			CompressedMessages: messages,
			OriginalTokens:     0,
			CompressedTokens:   0,
		}, nil
	}

	// ── 步骤 1: 计算原始 token 数 ──
	originalTokens, err := e.CountTokens(messages)
	if err != nil {
		return nil, fmt.Errorf("failed to count tokens: %w", err)
	}

	// ── 步骤 2: 计算动态阈值 ──
	threshold := e.dynamicEngine.CalculateDynamicThreshold(messages)

	// ── 步骤 3: 如果未超限，直接返回 ──
	// 双重检查：先硬阈值（向后兼容），再动态阈值（优化）
	isOverHardLimit := originalTokens > e.config.MaxContextTokens
	isOverThreshold := threshold.IsOverThreshold(originalTokens)
	if !isOverHardLimit && !isOverThreshold {
		return &CompressResult{
			CompressedMessages: messages,
			OriginalTokens:     originalTokens,
			CompressedTokens:   originalTokens,
			CompressionRatio:   1.0,
			CompressionStats:   "No compression needed",
		}, nil
	}

	slog.Info("Context compression triggered (pipeline)",
		"original_tokens", originalTokens,
		"threshold", threshold.TriggerThreshold,
		"effective_window", threshold.EffectiveWindow)

	// ── 步骤 4: 初始化压缩上下文 ──
	cc := &CompressionContext{
		Messages:       append([]llm.Message{}, messages...), // 深拷贝
		OriginalTokens: originalTokens,
		CurrentTokens:  originalTokens,
		Threshold:      threshold,
		HardLimit:      e.config.MaxContextTokens, // 硬阈值，用于管道层判断
	}

	// 压缩前提取状态（用于 Layer 6 补偿）
	if e.extractor != nil && e.config.CompensateEnabled {
		cc.ExtractedState = e.extractor.ExtractState(messages)
	}

	// ───────────────────────────────────────────
	// ── Layer 1: 工具结果预算控制 ──
	// ───────────────────────────────────────────
	if cc.CurrentTokens > cc.HardLimit && e.config.MaxToolOutputTokens > 0 {
		result, newMsgs, err := e.applyToolBudget(cc.Messages)
		if err == nil && result.TokensSaved > 0 {
			cc.Messages = newMsgs
			cc.CurrentTokens = e.countMessagesTokens(cc.Messages)
			cc.LayersApplied = append(cc.LayersApplied, "budget")
			e.recordLayerStat("budget")
			slog.Debug("Layer 1 (budget) applied", "tokens_saved", result.TokensSaved)
		}
	}

	// ───────────────────────────────────────────
	// ── Layer 2: 老消息剪裁 ──
	// ───────────────────────────────────────────
	if cc.CurrentTokens > cc.HardLimit && e.config.KeepRecentRounds > 0 {
		prunerCC := &CompressionContext{
			Threshold:      cc.Threshold,
			CurrentTokens:  cc.CurrentTokens,
			TargetTokens:   threshold.TriggerThreshold,
			MinPrunableAge: e.config.MinPrunableAge,
		}
		result := e.pruneOldMessages(cc.Messages, prunerCC)
		if result.PrunedCount > 0 {
			// pruneOldMessages 返回 PruneResult，但不会修改输入切片
			// 需要从头部裁剪 pruned 条消息
			cc.Messages = cc.Messages[result.PrunedCount:]
			cc.CurrentTokens = e.countMessagesTokens(cc.Messages)
			cc.LayersApplied = append(cc.LayersApplied, "prune")
			e.recordLayerStat("prune")
			slog.Debug("Layer 2 (prune) applied", "pruned", result.PrunedCount)
		}
	}

	// ───────────────────────────────────────────
	// ── Layer 3: 微压缩 ──
	// ───────────────────────────────────────────
	if cc.CurrentTokens > cc.HardLimit && e.config.MicroCompressEnabled {
		keepBoundary := e.findKeepBoundary(cc.Messages)
		result, newMsgs := e.microCompress(cc.Messages, keepBoundary)
		if result.CompressedCount > 0 {
			cc.Messages = newMsgs
			cc.CurrentTokens = e.countMessagesTokens(cc.Messages)
			cc.LayersApplied = append(cc.LayersApplied, "micro")
			e.recordLayerStat("micro")
			slog.Debug("Layer 3 (micro) applied", "compressed", result.CompressedCount, "tokens_saved", result.TokensSaved)
		}
	}

	// ───────────────────────────────────────────
	// ── Layer 4: 上下文折叠 ──
	// ───────────────────────────────────────────
	if cc.CurrentTokens > cc.HardLimit && e.config.FoldEnabled && e.summarizer != nil {
		foldCfg := FoldContextConfig(
			WithKeepRecentRounds(e.config.KeepRecentRounds),
			WithMinFoldTokens(500),
			WithFoldManager(e.foldManager),
		)
		entry, err := e.foldContext(ctx, cc.Messages, foldCfg)
		if err != nil {
			slog.Warn("Layer 4 (fold) failed, skipping", "error", err)
		} else if entry != nil && entry.Phase == FoldPhaseCommitted && entry.SummaryMsg != nil {
			// foldContext 返回 FoldEntry，需要手动应用折叠到消息列表
			newMsgs := make([]llm.Message, 0, len(cc.Messages)-len(entry.SourceMsgs)+1)
			newMsgs = append(newMsgs, cc.Messages[:entry.SourceStart]...)
			newMsgs = append(newMsgs, *entry.SummaryMsg)
			newMsgs = append(newMsgs, cc.Messages[entry.SourceEnd:]...)
			cc.Messages = newMsgs
			cc.CurrentTokens = e.countMessagesTokens(cc.Messages)
			cc.LayersApplied = append(cc.LayersApplied, "fold")
			e.recordLayerStat("fold")
			slog.Debug("Layer 4 (fold) applied", "tokens_saved", entry.TokensSaved)
		}
	}

	// ───────────────────────────────────────────
	// ── Layer 5: 自动压缩（兜底 — 原有 LLM 摘要逻辑） ──
	// ───────────────────────────────────────────
	if cc.CurrentTokens > cc.HardLimit && e.summarizer != nil {
		// 使用原有的分区逻辑
		recent, older := extractRecentMessages(cc.Messages, e.config.KeepRecentRounds)
		if len(older) > 0 {
			// 先尝试工具结果截断
			recentTokens := e.countMessagesTokens(recent)

			truncatedOlder, freedBytes := TruncateToolResults(older, ForceTruncateAll, DefaultTruncationConfig)
			truncatedOlderTokens := e.countMessagesTokens(truncatedOlder)

			totalBudget := threshold.TriggerThreshold
			if sysMsg := extractSystemMessage(messages); sysMsg != nil {
				sysTokens, _ := e.tokenizer.CountMessagesTokens([]llm.Message{*sysMsg})
				totalBudget -= sysTokens
			}

			if freedBytes > 0 && truncatedOlderTokens+recentTokens <= totalBudget {
				// 截断已足够，跳过 LLM 摘要
				cc.Messages = buildTruncatedMessages(messages, truncatedOlder, recent, e.config.KeepRecentRounds)
				cc.LayersApplied = append(cc.LayersApplied, "truncate_only")
				e.recordLayerStat("truncate_only")
				slog.Info("Layer 5 (truncate_only) sufficient", "freed_bytes", freedBytes)
			} else if freedBytes > 0 {
				// 截断释放了部分 token，使用截断后的 older 继续 LLM 摘要
				// (freedBytes == 0 时直接使用原始 older)
				older = truncatedOlder
			} else {
				// 需要 LLM 摘要
				ctxTimeout, cancel := context.WithTimeout(ctx, e.config.SummarizationTimeout)

				var summary string
				var sumErr error
				if e.frozenSummary != "" && len(truncatedOlder) > 0 {
					summary, sumErr = e.incrementalCompress(ctxTimeout, truncatedOlder)
				} else {
					summary, sumErr = e.summarizer.GenerateSummary(ctxTimeout, truncatedOlder)
				}
				cancel()

				if sumErr != nil {
					slog.Warn("Layer 5 auto-compress failed", "error", sumErr)
					// 降级：仅保留 recent
					cc.Messages = recent
				} else {
					e.frozenSummary = summary
					cc.Messages = buildCacheAwareMessages(messages, summary, e.config.KeepRecentRounds)
				}
				cc.LayersApplied = append(cc.LayersApplied, "auto_compress")
				e.recordLayerStat("auto_compress")
			}

			cc.CurrentTokens = e.countMessagesTokens(cc.Messages)
		}
	}

	// ───────────────────────────────────────────
	// ── Layer 6: 状态补偿 ──
	// ───────────────────────────────────────────
	if cc.ExtractedState != nil && e.config.CompensateEnabled {
		compResult := e.extractor.compensateState(
			&CompressionContext{
				Threshold:     cc.Threshold,
				CurrentTokens: cc.CurrentTokens,
				TargetTokens:  threshold.TriggerThreshold,
			},
			cc.Messages,
			cc.ExtractedState,
			e,
		)
		if compResult.Injected {
			cc.LayersApplied = append(cc.LayersApplied, "compensate")
			e.recordLayerStat("compensate")
			slog.Debug("Layer 6 (compensate) applied", "files", compResult.FilesReinjected,
				"plan", compResult.PlanReinjected, "skills", compResult.SkillsReinjected)
		}
	}

	// ── 步骤 5: 计算最终统计 ──
	compressedTokens := e.countMessagesTokens(cc.Messages)
	compressionRatio := float64(compressedTokens) / float64(originalTokens)

	stats := fmt.Sprintf("Pipeline: %s | Original: %d tokens | Compressed: %d tokens | Ratio: %.2f",
		strings.Join(cc.LayersApplied, " → "),
		originalTokens, compressedTokens, compressionRatio)

	// ── 步骤 6: 更新统计 ──
	e.totalCompressions++
	if e.totalCompressions%10 == 0 {
		slog.Info("Compression stats", "total", e.totalCompressions, "layer_stats", e.layerStats)
	}

	return &CompressResult{
		CompressedMessages: cc.Messages,
		OriginalTokens:     originalTokens,
		CompressedTokens:   compressedTokens,
		CompressionRatio:   compressionRatio,
		CompressionStats:   stats,
		SummaryInfo:        e.frozenSummary,
	}, nil
}

// recordLayerStat 记录某层被触发的次数（线程安全）
func (e *Engine) recordLayerStat(layer string) {
	e.statsLock.Lock()
	defer e.statsLock.Unlock()
	e.layerStats[layer]++
}

// CountTokens 计算 messages 的总 token 数
func (e *Engine) CountTokens(messages []llm.Message) (int, error) {
	return e.tokenizer.CountMessagesTokens(messages)
}

// countMessagesTokens 内部计数，不返回错误（降级估算）
func (e *Engine) countMessagesTokens(messages []llm.Message) int {
	tokens, err := e.tokenizer.CountMessagesTokens(messages)
	if err != nil {
		// 降级估算：约4个字符=1个token
		total := 0
		for _, msg := range messages {
			total += len([]rune(msg.Content)) / 4
		}
		return total
	}
	return tokens
}

// ─────────────────────────────────────────────────────────
// extractRecentMessages — 消息分区（含 Tool-Call Atomicity Repair）
// ─────────────────────────────────────────────────────────

// extractRecentMessages splits messages into (recent, older) based on keepRounds.
// Ensures tool_call/tool_response pairs are never split across the boundary.
func extractRecentMessages(messages []llm.Message, keepRounds int) (recent, older []llm.Message) {
	if len(messages) == 0 {
		return nil, nil
	}

	// 从后往前数 keepRounds*2 条消息作为保留区（每轮 user+assistant 约2条）
	keepCount := keepRounds * 2
	if keepCount <= 0 {
		keepCount = 6 // 默认保留3轮
	}
	if keepCount > len(messages) {
		keepCount = len(messages)
	}

	recentStart := len(messages) - keepCount

	// ─── Tool-Call Atomicity Repair ───
	// Ensure the boundary does not split tool_call/tool_response pairs.
	if recentStart > 0 {
		toolCallIDsInRecent := make(map[string]bool)
		for i := recentStart; i < len(messages); i++ {
			msg := messages[i]
			if msg.Role == llm.RoleTool && msg.ToolCallID != "" {
				toolCallIDsInRecent[msg.ToolCallID] = true
			}
		}
		if len(toolCallIDsInRecent) > 0 {
			for i := recentStart - 1; i >= 0; i-- {
				msg := messages[i]
				if msg.Role == llm.RoleAssistant && len(msg.ToolCalls) > 0 {
					needsAdjustment := false
					for _, tc := range msg.ToolCalls {
						if toolCallIDsInRecent[tc.ID] {
							needsAdjustment = true
							break
						}
					}
					if needsAdjustment {
						recentStart = i
						break
					}
				}
				if msg.Role == llm.RoleUser {
					break
				}
			}
		}
	}

	// 保留区：最后 keepRounds 轮的消息
	// Capacity must be len(messages)-recentStart so that copy() does not
	// silently truncate when atomicity repair moved recentStart backwards.
	recent = make([]llm.Message, len(messages)-recentStart)
	copy(recent, messages[recentStart:])

	// 待压缩区：保留区之前且非 System 的消息
	older = make([]llm.Message, 0, recentStart)
	for i := 0; i < recentStart; i++ {
		msg := messages[i]
		// 跳过 System 消息（它们不应再被压缩）
		if msg.Role == llm.RoleSystem {
			continue
		}
		older = append(older, msg)
	}

	// 锚定消息保护：将 older 中的锚定消息移到 recent
	var anchoredMsgs []llm.Message
	var nonAnchored []llm.Message
	for _, msg := range older {
		if msg.IsAnchored {
			anchoredMsgs = append(anchoredMsgs, msg)
		} else {
			nonAnchored = append(nonAnchored, msg)
		}
	}
	older = nonAnchored
	// 锚定消息插入到 recent 最前面
	recent = append(anchoredMsgs, recent...)

	// End-boundary atomicity: trim incomplete trailing tool-call groups
	recent = trimIncompleteEndGroup(recent)

	return recent, older
}

// ─────────────────────────────────────────────────────────
// buildCacheAwareMessages — 缓存友好布局
// ─────────────────────────────────────────────────────────

// buildCacheAwareMessages 构建缓存友好的消息布局
// 布局顺序：[System] + [Summary] + [Recent]
func buildCacheAwareMessages(messages []llm.Message, summary string, keepRounds int) []llm.Message {
	result := make([]llm.Message, 0, 3+len(messages))

	// 1. 原始 System 消息（绝对稳定前缀）
	systemMsg := extractSystemMessage(messages)
	if systemMsg != nil {
		result = append(result, *systemMsg)
	}

	// 2. 摘要消息
	if summary != "" {
		result = append(result, llm.Message{
			Role:    llm.RoleSystem,
			Content: "[CONTEXT SUMMARY]\n" + CleanSummaryOutput(summary),
		})
	}

	// 3. 保留区消息（recent）
	recent, _ := extractRecentMessages(messages, keepRounds)
	result = append(result, recent...)

	return result
}

// buildTruncatedMessages 构建截断后的消息布局
// 布局顺序：[System] + [Truncated Older] + [Recent]
// 当截断释放了足够的 token 时使用，跳过 LLM 摘要
func buildTruncatedMessages(originalMessages []llm.Message, truncatedOlder, recent []llm.Message, keepRounds int) []llm.Message {
	result := make([]llm.Message, 0, 2+len(truncatedOlder)+len(recent))

	// 1. 原始 System 消息（绝对稳定前缀）
	systemMsg := extractSystemMessage(originalMessages)
	if systemMsg != nil {
		result = append(result, *systemMsg)
	}

	// 2. 已截断的历史消息
	result = append(result, truncatedOlder...)

	// 3. 保留区消息（recent）
	result = append(result, recent...)

	return result
}

// extractSystemMessage 从消息列表中提取第一条 System 消息
func extractSystemMessage(messages []llm.Message) *llm.Message {
	for _, msg := range messages {
		if msg.Role == llm.RoleSystem {
			return &msg
		}
	}
	return nil
}

// ─────────────────────────────────────────────────────────
// CleanSummaryOutput — 清洗 LLM 摘要输出
// ─────────────────────────────────────────────────────────

// CleanSummaryOutput 清洗 LLM 摘要输出
func CleanSummaryOutput(raw string) string {
	if raw == "" {
		return ""
	}

	cleaned := raw

	cleaned = removeMarkdownFence(cleaned)
	cleaned = removeCourtesyPrefix(cleaned)
	cleaned = removeDuplicateLines(cleaned)
	cleaned = compactWhitespace(cleaned)

	return strings.TrimSpace(cleaned)
}

func removeMarkdownFence(text string) string {
	if !strings.HasPrefix(text, "```") {
		return text
	}
	first := strings.Index(text, "\n")
	last := strings.LastIndex(text, "```")
	if first > 0 && last > first {
		text = text[first:last]
	}
	return strings.TrimSpace(text)
}

func removeDuplicateLines(text string) string {
	lines := strings.Split(text, "\n")
	if len(lines) < 3 {
		return text
	}

	var result []string
	repeatCount := 1
	for i := 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		prevTrimmed := strings.TrimSpace(lines[i-1])

		if trimmed == prevTrimmed && trimmed != "" {
			repeatCount++
			if repeatCount > 2 {
				continue
			}
		} else {
			repeatCount = 1
		}
		result = append(result, lines[i-1])
	}
	result = append(result, lines[len(lines)-1])

	return strings.Join(result, "\n")
}

func compactWhitespace(text string) string {
	re := regexp.MustCompile(`\n{3,}`)
	text = re.ReplaceAllString(text, "\n\n")
	re = regexp.MustCompile(`[ \t]+$`)
	text = re.ReplaceAllString(text, "")
	return text
}

func removeCourtesyPrefix(text string) string {
	// 常见的礼貌前缀列表
	prefixes := []string{
		"Sure, here is the summary.",
		"Here is the summary.",
		"Certainly! Here is the summary.",
		"Certainly, here is the summary.",
		"Here's a summary of the conversation",
		"Here's the summary",
		"Here is a summary",
		"Of course. Here is the summary",
		"Of course, here is the summary",
	}

	for _, prefix := range prefixes {
		if strings.HasPrefix(strings.TrimSpace(text), prefix) {
			after := strings.TrimPrefix(strings.TrimSpace(text), prefix)
			after = strings.TrimSpace(after)
			if after != "" {
				return after
			}
		}
	}

	return text
}

// incrementalCompress 执行增量压缩
// 从 older 中提取已有的 [CONTEXT SUMMARY] 作为基准，仅压缩后续的新消息
func (e *Engine) incrementalCompress(ctx context.Context, older []llm.Message) (string, error) {
	// 1. 查找已有的 [CONTEXT SUMMARY] 消息
	existingSummary := ""
	newStartIdx := 0
	for i, msg := range older {
		if msg.Role == llm.RoleSystem && strings.HasPrefix(msg.Content, "[CONTEXT SUMMARY]") {
			// 提取摘要文本（去掉前缀）
			raw := msg.Content
			raw = strings.TrimPrefix(raw, "[CONTEXT SUMMARY]")
			raw = strings.TrimPrefix(raw, "\n")
			existingSummary = raw
			newStartIdx = i + 1
			break
		}
	}

	// 2. 如果没有找到已有摘要（理论上不会发生，因为 frozenSummary != ""）
	if existingSummary == "" {
		return e.summarizer.GenerateSummary(ctx, older)
	}

	// 3. 提取新增消息
	newMsgs := older[newStartIdx:]
	if len(newMsgs) == 0 {
		// 没有新消息，直接返回已有摘要
		return e.frozenSummary, nil
	}

	// 4. 构建增量压缩输入
	var sb strings.Builder
	sb.WriteString("[EXISTING SUMMARY]\n")
	sb.WriteString(existingSummary)
	sb.WriteString("\n\n[NEW MESSAGES TO INCORPORATE]\n")
	for _, msg := range newMsgs {
		sb.WriteString(fmt.Sprintf("[%s] %s\n", msg.Role, msg.Content))
		if len(msg.ToolCalls) > 0 {
			for _, tc := range msg.ToolCalls {
				sb.WriteString(fmt.Sprintf("  → ToolCall: %s(%s)\n", tc.Function.Name, tc.Function.Arguments))
			}
		}
	}

	userContent := sb.String()

	// 5. 调用 LLM 做增量摘要（复用现有接口）
	// 将已有摘要 + 新消息打包成一条 User 消息
	incMsgs := []llm.Message{
		{Role: llm.RoleUser, Content: userContent},
	}
	return e.summarizer.GenerateSummary(ctx, incMsgs)
}

// ─────────────────────────────────────────────────────────
// trimIncompleteEndGroup — 工具调用原子性保障
// ─────────────────────────────────────────────────────────

// trimIncompleteEndGroup 移除尾部不完整的 tool_call 组。
// 如果一个 assistant 有 N 个 tool_calls 但后面跟随的 tool 响应少于 N 个，
// 则认为该组不完整。这可以防止在 recent 切片末尾出现孤立的 tool 消息。
func trimIncompleteEndGroup(msgs []llm.Message) []llm.Message {
	if len(msgs) == 0 {
		return msgs
	}

	// Walk backward from the end to find the last assistant with tool_calls
	lastIdx := len(msgs) - 1

	// If the last message is not a tool response, there's no incomplete end group
	if msgs[lastIdx].Role != llm.RoleTool {
		return msgs
	}

	// Find the assistant that initiated the last tool_call group
	assistantIdx := -1
	for i := lastIdx; i >= 0; i-- {
		if msgs[i].Role == llm.RoleAssistant && len(msgs[i].ToolCalls) > 0 {
			assistantIdx = i
			break
		}
		// If we hit a non-tool, non-assistant message, the trailing
		// tool messages are orphans — trim them
		if msgs[i].Role != llm.RoleTool && msgs[i].Role != llm.RoleAssistant {
			return msgs[:i+1]
		}
	}

	if assistantIdx == -1 {
		// No assistant found — trailing tool messages are orphans
		return msgs[:0]
	}

	// Count tool responses after the assistant
	expectedCount := len(msgs[assistantIdx].ToolCalls)
	actualCount := 0
	for i := assistantIdx + 1; i < len(msgs); i++ {
		if msgs[i].Role == llm.RoleTool {
			actualCount++
		} else {
			break
		}
	}

	if actualCount >= expectedCount {
		return msgs // Complete group — no trimming needed
	}

	// Incomplete group — trim from the assistant onward
	return msgs[:assistantIdx]
}
