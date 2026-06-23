package compact

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ─────────────────────────────────────────────────────────
// MetricsCollector 压缩指标收集器
// ─────────────────────────────────────────────────────────

// MetricsCollector 收集压缩过程的关键指标
// 所有操作线程安全，适合高并发场景
type MetricsCollector struct {
	mu sync.RWMutex

	// --- 计数器 ---
	compressTotal       atomic.Int64 // 总压缩次数
	compressSuccess     atomic.Int64 // 成功压缩次数
	compressFailure     atomic.Int64 // 失败压缩次数
	incrementalTotal    atomic.Int64 // 增量压缩次数
	fullTotal           atomic.Int64 // 全量压缩次数
	mergeTotal          atomic.Int64 // 摘要合并次数
	degradationTotal    atomic.Int64 // 降级总次数
	truncatedTotal      atomic.Int64 // 截断降级次数
	asyncSubmittedTotal atomic.Int64 // 异步提交总数
	asyncCompletedTotal atomic.Int64 // 异步完成总数
	asyncDroppedTotal   atomic.Int64 // 异步丢弃总数
	tokenizerCacheHit   atomic.Int64 // Tokenizer 缓存命中
	tokenizerCacheMiss  atomic.Int64 // Tokenizer 缓存未命中

	// --- 直方图（简化版：滑动窗口采样） ---
	// 使用指数加权移动平均（EWMA）而非完整直方图，节省内存
	compressionRatioEWMA   ewma // 压缩比 EWMA
	compressionLatencyEWMA ewma // 压缩延迟 EWMA（毫秒）
	qualityScoreEWMA       ewma // 质量评分 EWMA (0~1)
	tokenReductionEWMA     ewma // token 缩减率 EWMA

	// --- 原始数据采样（最近的 N 条记录，用于调试） ---
	sampleEnabled bool
	samples       []CompressionSample
	maxSamples    int
}

// CompressionSample 单次压缩的采样数据
type CompressionSample struct {
	Timestamp        time.Time       `json:"timestamp"`
	Action           string          `json:"action"`           // "full" | "incremental" | "merge"
	Duration         time.Duration   `json:"duration"`
	OriginalTokens   int             `json:"original_tokens"`
	CompressedTokens int             `json:"compressed_tokens"`
	ReductionRatio   float64         `json:"reduction_ratio"`
	DegradationTier  DegradationTier `json:"degradation_tier"`
	QualityScore     float64         `json:"quality_score"`
	MessageCount     int             `json:"message_count"`
	SummaryDepth     int             `json:"summary_depth"`
	Error            string          `json:"error,omitempty"`
}

// ewma 指数加权移动平均
type ewma struct {
	mu         sync.RWMutex
	value      float64   // 当前值
	alpha      float64   // 平滑因子（0~1），越大越看重近期数据
	count      int64     // 更新次数
	lastUpdate time.Time // 最近更新时间
}

// newEWMA 创建 EWMA
func newEWMA(alpha float64) ewma {
	if alpha <= 0 || alpha > 1 {
		alpha = 0.3 // 默认
	}
	return ewma{
		alpha: alpha,
	}
}

// Update 更新 EWMA 值
func (e *ewma) Update(value float64) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.count++
	e.lastUpdate = time.Now()

	if e.count == 1 {
		e.value = value
		return
	}

	// EWMA: new_value = alpha * current + (1 - alpha) * old_value
	e.value = e.alpha*value + (1-e.alpha)*e.value
}

// Value 返回当前 EWMA 值
func (e *ewma) Value() float64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.value
}

// Count 返回更新次数
func (e *ewma) Count() int64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.count
}

// ─────────────────────────────────────────────────────────
// 构造函数
// ─────────────────────────────────────────────────────────

// NewMetricsCollector 创建指标收集器
func NewMetricsCollector(sampleEnabled bool, maxSamples int) *MetricsCollector {
	if maxSamples <= 0 {
		maxSamples = 100
	}

	return &MetricsCollector{
		sampleEnabled:       sampleEnabled,
		samples:             make([]CompressionSample, 0, maxSamples),
		maxSamples:          maxSamples,
		compressionRatioEWMA:   newEWMA(0.3),
		compressionLatencyEWMA: newEWMA(0.3),
		qualityScoreEWMA:       newEWMA(0.3),
		tokenReductionEWMA:     newEWMA(0.3),
	}
}

// ─────────────────────────────────────────────────────────
// 记录方法
// ─────────────────────────────────────────────────────────

// RecordCompression 记录一次压缩操作的指标
func (m *MetricsCollector) RecordCompression(sample CompressionSample) {
	m.compressTotal.Add(1)

	if sample.Error != "" {
		m.compressFailure.Add(1)
	} else {
		m.compressSuccess.Add(1)
	}

	switch sample.Action {
	case "incremental":
		m.incrementalTotal.Add(1)
	case "full":
		m.fullTotal.Add(1)
	case "merge":
		m.mergeTotal.Add(1)
	}

	// 记录降级
	if sample.DegradationTier > TierFull {
		m.degradationTotal.Add(1)
		if sample.DegradationTier == TierTruncated {
			m.truncatedTotal.Add(1)
		}
	}

	// 更新 EWMA
	if sample.ReductionRatio > 0 {
		m.compressionRatioEWMA.Update(sample.ReductionRatio)
	}
	m.compressionLatencyEWMA.Update(float64(sample.Duration.Milliseconds()))
	if sample.QualityScore > 0 {
		m.qualityScoreEWMA.Update(sample.QualityScore)
	}
	if sample.OriginalTokens > 0 {
		reduction := 1.0 - float64(sample.CompressedTokens)/float64(sample.OriginalTokens)
		m.tokenReductionEWMA.Update(reduction)
	}

	// 采样
	if m.sampleEnabled {
		m.mu.Lock()
		m.samples = append(m.samples, sample)
		if len(m.samples) > m.maxSamples {
			// 移除最早的 10%
			excess := len(m.samples) - m.maxSamples
			m.samples = m.samples[excess:]
		}
		m.mu.Unlock()
	}
}

// RecordDegradation 记录降级事件
func (m *MetricsCollector) RecordDegradation(tier DegradationTier) {
	m.degradationTotal.Add(1)
	if tier == TierTruncated {
		m.truncatedTotal.Add(1)
	}
}

// RecordAsyncEvent 记录异步压缩事件
func (m *MetricsCollector) RecordAsyncEvent(eventType string) {
	switch eventType {
	case "submitted":
		m.asyncSubmittedTotal.Add(1)
	case "completed":
		m.asyncCompletedTotal.Add(1)
	case "dropped":
		m.asyncDroppedTotal.Add(1)
	}
}

// RecordTokenizerCache 记录 tokenizer 缓存事件
func (m *MetricsCollector) RecordTokenizerCache(hit bool) {
	if hit {
		m.tokenizerCacheHit.Add(1)
	} else {
		m.tokenizerCacheMiss.Add(1)
	}
}

// ─────────────────────────────────────────────────────────
// 查询方法
// ─────────────────────────────────────────────────────────

// Snapshot 返回当前指标的完整快照
func (m *MetricsCollector) Snapshot() MetricsSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	snapshot := MetricsSnapshot{
		Timestamp: time.Now(),

		CompressTotal:       m.compressTotal.Load(),
		CompressSuccess:     m.compressSuccess.Load(),
		CompressFailure:     m.compressFailure.Load(),
		SuccessRate:         calcRate(m.compressSuccess.Load(), m.compressTotal.Load()),
		IncrementalTotal:    m.incrementalTotal.Load(),
		FullTotal:           m.fullTotal.Load(),
		MergeTotal:          m.mergeTotal.Load(),
		DegradationTotal:    m.degradationTotal.Load(),
		TruncatedTotal:      m.truncatedTotal.Load(),
		DegradationRate:     calcRate(m.degradationTotal.Load(), m.compressTotal.Load()),

		AsyncSubmitted:  m.asyncSubmittedTotal.Load(),
		AsyncCompleted:  m.asyncCompletedTotal.Load(),
		AsyncDropped:    m.asyncDroppedTotal.Load(),
		AsyncDropRate:   calcRate(m.asyncDroppedTotal.Load(), m.asyncSubmittedTotal.Load()),

		TokenizerCacheHit:   m.tokenizerCacheHit.Load(),
		TokenizerCacheMiss:  m.tokenizerCacheMiss.Load(),
		TokenizerCacheRate:  calcRate(m.tokenizerCacheHit.Load(), m.tokenizerCacheHit.Load()+m.tokenizerCacheMiss.Load()),

		CompressionRatioAvg:   m.compressionRatioEWMA.Value(),
		CompressionLatencyAvg: m.compressionLatencyEWMA.Value(), // 毫秒
		QualityScoreAvg:       m.qualityScoreEWMA.Value(),
		TokenReductionAvg:     m.tokenReductionEWMA.Value(),
	}

	return snapshot
}

// RecentSamples 返回最近的采样记录
func (m *MetricsCollector) RecentSamples(n int) []CompressionSample {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if n <= 0 || n > len(m.samples) {
		n = len(m.samples)
	}

	result := make([]CompressionSample, n)
	copy(result, m.samples[len(m.samples)-n:])
	return result
}

// Reset 重置所有指标
func (m *MetricsCollector) Reset() {
	m.compressTotal.Store(0)
	m.compressSuccess.Store(0)
	m.compressFailure.Store(0)
	m.incrementalTotal.Store(0)
	m.fullTotal.Store(0)
	m.mergeTotal.Store(0)
	m.degradationTotal.Store(0)
	m.truncatedTotal.Store(0)
	m.asyncSubmittedTotal.Store(0)
	m.asyncCompletedTotal.Store(0)
	m.asyncDroppedTotal.Store(0)
	m.tokenizerCacheHit.Store(0)
	m.tokenizerCacheMiss.Store(0)

	m.mu.Lock()
	m.samples = make([]CompressionSample, 0, m.maxSamples)
	m.mu.Unlock()
}

// ─────────────────────────────────────────────────────────
// MetricsSnapshot 指标快照
// ─────────────────────────────────────────────────────────

// MetricsSnapshot 压缩指标快照
type MetricsSnapshot struct {
	Timestamp time.Time `json:"timestamp"`

	// 计数
	CompressTotal       int64   `json:"compress_total"`
	CompressSuccess     int64   `json:"compress_success"`
	CompressFailure     int64   `json:"compress_failure"`
	SuccessRate         float64 `json:"success_rate"`
	IncrementalTotal    int64   `json:"incremental_total"`
	FullTotal           int64   `json:"full_total"`
	MergeTotal          int64   `json:"merge_total"`
	DegradationTotal    int64   `json:"degradation_total"`
	TruncatedTotal      int64   `json:"truncated_total"`
	DegradationRate     float64 `json:"degradation_rate"`

	// 异步
	AsyncSubmitted int64   `json:"async_submitted"`
	AsyncCompleted int64   `json:"async_completed"`
	AsyncDropped   int64   `json:"async_dropped"`
	AsyncDropRate  float64 `json:"async_drop_rate"`

	// Cache
	TokenizerCacheHit  int64   `json:"tokenizer_cache_hit"`
	TokenizerCacheMiss int64   `json:"tokenizer_cache_miss"`
	TokenizerCacheRate float64 `json:"tokenizer_cache_rate"`

	// EWMA 平均值
	CompressionRatioAvg   float64 `json:"compression_ratio_avg"`   // 压缩后/压缩前
	CompressionLatencyAvg float64 `json:"compression_latency_avg"` // 毫秒
	QualityScoreAvg       float64 `json:"quality_score_avg"`
	TokenReductionAvg     float64 `json:"token_reduction_avg"` // 0~1，越大压缩越多
}

// String 返回可读的快照文本
func (s MetricsSnapshot) String() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("=== Compression Metrics [%s] ===\n", s.Timestamp.Format("15:04:05")))
	sb.WriteString(fmt.Sprintf("Total: %d | Success: %d (%.1f%%) | Failure: %d\n",
		s.CompressTotal, s.CompressSuccess, s.SuccessRate*100, s.CompressFailure))
	sb.WriteString(fmt.Sprintf("Full: %d | Incremental: %d | Merge: %d\n",
		s.FullTotal, s.IncrementalTotal, s.MergeTotal))
	sb.WriteString(fmt.Sprintf("Degradation: %d (%.1f%%) | Truncated: %d\n",
		s.DegradationTotal, s.DegradationRate*100, s.TruncatedTotal))
	sb.WriteString(fmt.Sprintf("Async: submitted=%d completed=%d dropped=%d (%.1f%%)\n",
		s.AsyncSubmitted, s.AsyncCompleted, s.AsyncDropped, s.AsyncDropRate*100))
	sb.WriteString(fmt.Sprintf("Token cache: hit=%d miss=%d rate=%.1f%%\n",
		s.TokenizerCacheHit, s.TokenizerCacheMiss, s.TokenizerCacheRate*100))
	sb.WriteString(fmt.Sprintf("Avg compression ratio: %.2f | Latency: %.0fms\n",
		s.CompressionRatioAvg, s.CompressionLatencyAvg))
	sb.WriteString(fmt.Sprintf("Avg quality score: %.2f | Token reduction: %.1f%%",
		s.QualityScoreAvg, s.TokenReductionAvg*100))
	return sb.String()
}

// ─────────────────────────────────────────────────────────
// 辅助函数
// ─────────────────────────────────────────────────────────

// calcRate 计算比率，避免除零
func calcRate(numerator, denominator int64) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

// ─────────────────────────────────────────────────────────
// 便捷函数：为样本计算质量评分
// ─────────────────────────────────────────────────────────

// CalculateQualityScore 计算单次压缩的质量评分（0~1）
// 基于压缩比、延迟、是否降级等因素综合评估
func CalculateQualityScore(sample CompressionSample) float64 {
	score := 1.0

	// 失败扣分
	if sample.Error != "" {
		return 0.0
	}

	// 降级扣分
	switch sample.DegradationTier {
	case TierTruncated:
		score *= 0.5
	case TierCache:
		score *= 0.7
	}

	// 压缩比评分：理想范围 0.3~0.7
	if sample.OriginalTokens > 0 {
		ratio := float64(sample.CompressedTokens) / float64(sample.OriginalTokens)
		if ratio < 0.1 {
			score *= 0.8 // 压缩太少，可能丢失信息
		} else if ratio > 0.9 {
			score *= 0.7 // 压缩太多，效果差
		} else if ratio >= 0.2 && ratio <= 0.5 {
			score *= 1.0 // 理想范围
		} else {
			score *= 0.9 // 可接受
		}
	}

	// 延迟评分：超过 10s 扣分
	if sample.Duration > 10*time.Second {
		score *= 0.9
	}
	if sample.Duration > 30*time.Second {
		score *= 0.8
	}

	// 确保范围 [0, 1]
	if score < 0 {
		score = 0
	}
	if score > 1 {
		score = 1
	}

	return score
}

// FormatMetricSummary 格式化指标汇总（供日志/UI使用）
func FormatMetricSummary(snapshot MetricsSnapshot) string {
	lines := []string{
		"📊 Compression Metrics:",
		fmt.Sprintf("   Success: %d/%d (%.1f%%)", snapshot.CompressSuccess, snapshot.CompressTotal, snapshot.SuccessRate*100),
		fmt.Sprintf("   Degradation: %d (%.1f%%)", snapshot.DegradationTotal, snapshot.DegradationRate*100),
		fmt.Sprintf("   Avg Ratio: %.2f | Latency: %.0fms", snapshot.CompressionRatioAvg, snapshot.CompressionLatencyAvg),
		fmt.Sprintf("   Quality: %.2f | Reduction: %.1f%%", snapshot.QualityScoreAvg, snapshot.TokenReductionAvg*100),
	}
	return strings.Join(lines, "\n")
}
