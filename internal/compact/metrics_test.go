package compact

import (
	"sync"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────
// 基础计数功能测试
// ─────────────────────────────────────────────────────────

func TestMetricsCollector_BasicCounters(t *testing.T) {
	m := NewMetricsCollector(false, 0)

	// 记录全量压缩
	m.RecordCompression(CompressionSample{
		Action:           "full",
		OriginalTokens:   1000,
		CompressedTokens: 500,
		Duration:         100 * time.Millisecond,
		QualityScore:     0.9,
		DegradationTier:  TierFull,
	})

	snapshot := m.Snapshot()
	if snapshot.CompressTotal != 1 {
		t.Errorf("CompressTotal = %d, want 1", snapshot.CompressTotal)
	}
	if snapshot.CompressSuccess != 1 {
		t.Errorf("CompressSuccess = %d, want 1", snapshot.CompressSuccess)
	}
	if snapshot.FullTotal != 1 {
		t.Errorf("FullTotal = %d, want 1", snapshot.FullTotal)
	}
	if snapshot.SuccessRate != 1.0 {
		t.Errorf("SuccessRate = %f, want 1.0", snapshot.SuccessRate)
	}
}

func TestMetricsCollector_FailureCounting(t *testing.T) {
	m := NewMetricsCollector(false, 0)

	m.RecordCompression(CompressionSample{
		Action:    "full",
		Duration:  50 * time.Millisecond,
		Error:     "timeout",
	})

	snapshot := m.Snapshot()
	if snapshot.CompressFailure != 1 {
		t.Errorf("CompressFailure = %d, want 1", snapshot.CompressFailure)
	}
	if snapshot.SuccessRate != 0.0 {
		t.Errorf("SuccessRate = %f, want 0.0", snapshot.SuccessRate)
	}
}

func TestMetricsCollector_IncrementalAndMerge(t *testing.T) {
	m := NewMetricsCollector(false, 0)

	m.RecordCompression(CompressionSample{
		Action:           "incremental",
		OriginalTokens:   800,
		CompressedTokens: 400,
		Duration:         50 * time.Millisecond,
		DegradationTier:  TierFull,
	})

	m.RecordCompression(CompressionSample{
		Action:           "merge",
		OriginalTokens:   1200,
		CompressedTokens: 600,
		Duration:         150 * time.Millisecond,
		DegradationTier:  TierFull,
	})

	snapshot := m.Snapshot()
	if snapshot.IncrementalTotal != 1 {
		t.Errorf("IncrementalTotal = %d, want 1", snapshot.IncrementalTotal)
	}
	if snapshot.MergeTotal != 1 {
		t.Errorf("MergeTotal = %d, want 1", snapshot.MergeTotal)
	}
	if snapshot.FullTotal != 0 {
		t.Errorf("FullTotal = %d, want 0", snapshot.FullTotal)
	}
}

// ─────────────────────────────────────────────────────────
// EWMA 计算正确性测试
// ─────────────────────────────────────────────────────────

func TestEWMA_SingleValue(t *testing.T) {
	e := newEWMA(0.5)
	e.Update(100.0)

	got := e.Value()
	if got != 100.0 {
		t.Errorf("EWMA after single update = %f, want 100.0", got)
	}
	if e.Count() != 1 {
		t.Errorf("Count = %d, want 1", e.Count())
	}
}

func TestEWMA_MultipleValues(t *testing.T) {
	// alpha=0.5
	// 第 1 次: value = 10
	// 第 2 次: value = 0.5*20 + 0.5*10 = 15
	// 第 3 次: value = 0.5*30 + 0.5*15 = 22.5
	e := newEWMA(0.5)
	e.Update(10.0)
	e.Update(20.0)
	e.Update(30.0)

	got := e.Value()
	expected := 22.5
	if got != expected {
		t.Errorf("EWMA after 3 updates = %f, want %f", got, expected)
	}
	if e.Count() != 3 {
		t.Errorf("Count = %d, want 3", e.Count())
	}
}

func TestEWMA_Weighting(t *testing.T) {
	// alpha 越大，越看重近期数据
	e := newEWMA(0.9)

	// 先给一个较小的值
	e.Update(10.0)

	// 再给一个较大的值，alpha=0.9 应该让新值占主导
	e.Update(100.0)

	got := e.Value()
	// 预期: 0.9*100 + 0.1*10 = 91.0
	expected := 91.0
	if got != expected {
		t.Errorf("EWMA alpha=0.9 = %f, want %f", got, expected)
	}

	// 对比 alpha=0.1（更看重历史）
	e2 := newEWMA(0.1)
	e2.Update(10.0)
	e2.Update(100.0)
	got2 := e2.Value()
	// 预期: 0.1*100 + 0.9*10 = 19.0
	expected2 := 19.0
	if got2 != expected2 {
		t.Errorf("EWMA alpha=0.1 = %f, want %f", got2, expected2)
	}
}

func TestEWMA_DefaultAlpha(t *testing.T) {
	// alpha <= 0 或 alpha > 1 应该使用默认值 0.3
	e := newEWMA(-0.1)
	if e.alpha != 0.3 {
		t.Errorf("alpha for invalid input = %f, want 0.3", e.alpha)
	}

	e2 := newEWMA(1.5)
	if e2.alpha != 0.3 {
		t.Errorf("alpha for alpha > 1 = %f, want 0.3", e2.alpha)
	}

	// alpha = 1.0 应该允许
	e3 := newEWMA(1.0)
	if e3.alpha != 1.0 {
		t.Errorf("alpha for 1.0 = %f, want 1.0", e3.alpha)
	}
}

// ─────────────────────────────────────────────────────────
// RecordCompression 采样测试
// ─────────────────────────────────────────────────────────

func TestRecordCompression_Sampling(t *testing.T) {
	m := NewMetricsCollector(true, 50) // 最多保留 50 条采样

	// 记录 100 次
	for i := 0; i < 100; i++ {
		m.RecordCompression(CompressionSample{
			Action:           "full",
			OriginalTokens:   1000,
			CompressedTokens: 500,
			Duration:         time.Duration(i) * time.Millisecond,
			DegradationTier:  TierFull,
		})
	}

	samples := m.RecentSamples(0)
	// 移除最早的 10%，所以 100 条 -> 最多保留 50 条
	maxExpected := 50
	if len(samples) > maxExpected {
		t.Errorf("RecentSamples count = %d, want <= %d", len(samples), maxExpected)
	}
	if len(samples) == 0 {
		t.Error("RecentSamples is empty, want at least some samples")
	}
}

func TestRecordCompression_SamplingDisabled(t *testing.T) {
	m := NewMetricsCollector(false, 100)

	for i := 0; i < 50; i++ {
		m.RecordCompression(CompressionSample{
			Action:           "full",
			OriginalTokens:   1000,
			CompressedTokens: 500,
			Duration:         time.Duration(i) * time.Millisecond,
			DegradationTier:  TierFull,
		})
	}

	samples := m.RecentSamples(0)
	if len(samples) != 0 {
		t.Errorf("RecentSamples count = %d, want 0 (sampling disabled)", len(samples))
	}
}

func TestRecordCompression_DegradationTracking(t *testing.T) {
	m := NewMetricsCollector(false, 0)

	// 正常压缩
	m.RecordCompression(CompressionSample{
		Action:           "full",
		DegradationTier:  TierFull,
		OriginalTokens:   1000,
		CompressedTokens: 500,
		Duration:         100 * time.Millisecond,
	})

	// 缓存降级
	m.RecordCompression(CompressionSample{
		Action:           "incremental",
		DegradationTier:  TierCache,
		OriginalTokens:   800,
		CompressedTokens: 400,
		Duration:         50 * time.Millisecond,
	})

	// 截断降级
	m.RecordCompression(CompressionSample{
		Action:           "full",
		DegradationTier:  TierTruncated,
		OriginalTokens:   1200,
		CompressedTokens: 600,
		Duration:         200 * time.Millisecond,
	})

	snapshot := m.Snapshot()
	if snapshot.DegradationTotal != 2 {
		t.Errorf("DegradationTotal = %d, want 2", snapshot.DegradationTotal)
	}
	if snapshot.TruncatedTotal != 1 {
		t.Errorf("TruncatedTotal = %d, want 1", snapshot.TruncatedTotal)
	}
}

func TestRecordDegradation(t *testing.T) {
	m := NewMetricsCollector(false, 0)

	m.RecordDegradation(TierTruncated)
	m.RecordDegradation(TierCache)

	snapshot := m.Snapshot()
	if snapshot.DegradationTotal != 2 {
		t.Errorf("DegradationTotal = %d, want 2", snapshot.DegradationTotal)
	}
	if snapshot.TruncatedTotal != 1 {
		t.Errorf("TruncatedTotal = %d, want 1", snapshot.TruncatedTotal)
	}
}

// ─────────────────────────────────────────────────────────
// Snapshot 快照完整性测试
// ─────────────────────────────────────────────────────────

func TestSnapshot_Completeness(t *testing.T) {
	m := NewMetricsCollector(false, 0)

	// 填充各种计数器
	m.RecordCompression(CompressionSample{
		Action:           "full",
		OriginalTokens:   1000,
		CompressedTokens: 500,
		Duration:         100 * time.Millisecond,
		QualityScore:     0.8,
		DegradationTier:  TierFull,
	})
	m.RecordAsyncEvent("submitted")
	m.RecordAsyncEvent("completed")
	m.RecordTokenizerCache(true)
	m.RecordTokenizerCache(false)

	snapshot := m.Snapshot()

	// 验证所有字段都有值
	if snapshot.CompressTotal != 1 {
		t.Error("CompressTotal should be 1")
	}
	if snapshot.AsyncSubmitted != 1 {
		t.Error("AsyncSubmitted should be 1")
	}
	if snapshot.AsyncCompleted != 1 {
		t.Error("AsyncCompleted should be 1")
	}
	if snapshot.TokenizerCacheHit != 1 {
		t.Error("TokenizerCacheHit should be 1")
	}
	if snapshot.TokenizerCacheMiss != 1 {
		t.Error("TokenizerCacheMiss should be 1")
	}
	if snapshot.TokenizerCacheRate != 0.5 {
		t.Errorf("TokenizerCacheRate = %f, want 0.5", snapshot.TokenizerCacheRate)
	}
}

func TestSnapshot_EmptyCollector(t *testing.T) {
	m := NewMetricsCollector(false, 0)
	snapshot := m.Snapshot()

	if snapshot.CompressTotal != 0 {
		t.Errorf("Empty CompressTotal = %d, want 0", snapshot.CompressTotal)
	}
	if snapshot.SuccessRate != 0.0 {
		t.Errorf("Empty SuccessRate = %f, want 0.0", snapshot.SuccessRate)
	}
	if snapshot.DegradationRate != 0.0 {
		t.Errorf("Empty DegradationRate = %f, want 0.0", snapshot.DegradationRate)
	}
	if snapshot.AsyncDropRate != 0.0 {
		t.Errorf("Empty AsyncDropRate = %f, want 0.0", snapshot.AsyncDropRate)
	}
}

func TestSnapshot_String(t *testing.T) {
	m := NewMetricsCollector(false, 0)
	m.RecordCompression(CompressionSample{
		Action:           "full",
		OriginalTokens:   1000,
		CompressedTokens: 500,
		Duration:         100 * time.Millisecond,
		QualityScore:     0.9,
		DegradationTier:  TierFull,
	})

	snapshot := m.Snapshot()
	str := snapshot.String()

	if len(str) == 0 {
		t.Error("Snapshot.String() is empty")
	}
	if !contains(str, "Compression Metrics") {
		t.Error("Snapshot.String() should contain 'Compression Metrics'")
	}
	if !contains(str, "Total: 1") {
		t.Error("Snapshot.String() should contain 'Total: 1'")
	}
}

// ─────────────────────────────────────────────────────────
// Reset 重置测试
// ─────────────────────────────────────────────────────────

func TestReset(t *testing.T) {
	m := NewMetricsCollector(true, 100)

	// 记录一些数据
	m.RecordCompression(CompressionSample{
		Action:           "full",
		OriginalTokens:   1000,
		CompressedTokens: 500,
		Duration:         100 * time.Millisecond,
		QualityScore:     0.9,
		DegradationTier:  TierFull,
	})
	m.RecordAsyncEvent("submitted")
	m.RecordTokenizerCache(true)

	snapshot := m.Snapshot()
	if snapshot.CompressTotal != 1 {
		t.Errorf("Before reset: CompressTotal = %d, want 1", snapshot.CompressTotal)
	}

	// 执行重置
	m.Reset()

	snapshot = m.Snapshot()
	if snapshot.CompressTotal != 0 {
		t.Errorf("After reset: CompressTotal = %d, want 0", snapshot.CompressTotal)
	}
	if snapshot.CompressSuccess != 0 {
		t.Errorf("After reset: CompressSuccess = %d, want 0", snapshot.CompressSuccess)
	}
	if snapshot.AsyncSubmitted != 0 {
		t.Errorf("After reset: AsyncSubmitted = %d, want 0", snapshot.AsyncSubmitted)
	}
	if snapshot.TokenizerCacheHit != 0 {
		t.Errorf("After reset: TokenizerCacheHit = %d, want 0", snapshot.TokenizerCacheHit)
	}

	// 采样也应该被清除
	samples := m.RecentSamples(0)
	if len(samples) != 0 {
		t.Errorf("After reset: samples count = %d, want 0", len(samples))
	}
}

// ─────────────────────────────────────────────────────────
// CalculateQualityScore 质量评分计算测试
// ─────────────────────────────────────────────────────────

func TestCalculateQualityScore_Success(t *testing.T) {
	// 理想情况：正常压缩，无降级，合理压缩比
	sample := CompressionSample{
		Action:           "full",
		OriginalTokens:   1000,
		CompressedTokens: 500,
		Duration:         100 * time.Millisecond,
		DegradationTier:  TierFull,
	}

	score := CalculateQualityScore(sample)
	if score <= 0 || score > 1 {
		t.Errorf("Score out of range [0,1]: %f", score)
	}
	if score < 0.9 {
		t.Errorf("Good compression should have high score, got %f", score)
	}
}

func TestCalculateQualityScore_Failure(t *testing.T) {
	sample := CompressionSample{
		Action:    "full",
		Error:     "llm timeout",
		Duration:  50 * time.Millisecond,
	}

	score := CalculateQualityScore(sample)
	if score != 0.0 {
		t.Errorf("Failed compression score = %f, want 0.0", score)
	}
}

func TestCalculateQualityScore_DegradationTruncated(t *testing.T) {
	sample := CompressionSample{
		Action:           "full",
		OriginalTokens:   1000,
		CompressedTokens: 500,
		Duration:         100 * time.Millisecond,
		DegradationTier:  TierTruncated,
	}

	score := CalculateQualityScore(sample)
	// 截断降级应该有明显扣分（score = 0.5）
	if score <= 0 || score > 0.5 {
		t.Errorf("Truncated degradation score = %f, want <= 0.5", score)
	}
}

func TestCalculateQualityScore_DegradationCache(t *testing.T) {
	sample := CompressionSample{
		Action:           "incremental",
		OriginalTokens:   1000,
		CompressedTokens: 500,
		Duration:         50 * time.Millisecond,
		DegradationTier:  TierCache,
	}

	score := CalculateQualityScore(sample)
	// 缓存降级应该得分为 0.7
	if score < 0.6 || score > 0.75 {
		t.Errorf("Cache degradation score = %f, want in range [0.6, 0.75]", score)
	}
}

func TestCalculateQualityScore_BadCompressionRatio(t *testing.T) {
	// 压缩太多：ratio = 0.95 > 0.9
	sample := CompressionSample{
		Action:           "full",
		OriginalTokens:   1000,
		CompressedTokens: 950,
		Duration:         100 * time.Millisecond,
		DegradationTier:  TierFull,
	}

	score := CalculateQualityScore(sample)
	if score >= 1.0 {
		t.Errorf("Bad compression ratio should reduce score, got %f", score)
	}

	// 压缩太少：ratio = 0.05 < 0.1
	sample2 := CompressionSample{
		Action:           "full",
		OriginalTokens:   1000,
		CompressedTokens: 50,
		Duration:         100 * time.Millisecond,
		DegradationTier:  TierFull,
	}

	score2 := CalculateQualityScore(sample2)
	if score2 >= 1.0 {
		t.Errorf("Too little compression should reduce score, got %f", score2)
	}
}

func TestCalculateQualityScore_LongLatency(t *testing.T) {
	// 超过 10s
	sample := CompressionSample{
		Action:           "full",
		OriginalTokens:   1000,
		CompressedTokens: 500,
		Duration:         11 * time.Second,
		DegradationTier:  TierFull,
	}

	score := CalculateQualityScore(sample)
	if score >= 1.0 {
		t.Errorf("Long latency should reduce score, got %f", score)
	}

	// 超过 30s
	sample2 := CompressionSample{
		Action:           "full",
		OriginalTokens:   1000,
		CompressedTokens: 500,
		Duration:         31 * time.Second,
		DegradationTier:  TierFull,
	}

	score2 := CalculateQualityScore(sample2)
	if score2 >= score {
		t.Errorf("Very long latency should further reduce score: %f >= %f", score2, score)
	}
}

func TestCalculateQualityScore_Bounds(t *testing.T) {
	// 测试极端情况不会超出 [0, 1]
	samples := []CompressionSample{
		{Error: "any error"},                          // 失败
		{DegradationTier: TierTruncated},              // 截断
		{OriginalTokens: 0},                           // 零 token
		{Duration: 60 * time.Second, DegradationTier: TierTruncated}, // 超长 + 截断
	}

	for i, sample := range samples {
		score := CalculateQualityScore(sample)
		if score < 0 || score > 1 {
			t.Errorf("Sample %d: score %f out of [0,1] range", i, score)
		}
	}
}

// ─────────────────────────────────────────────────────────
// FormatMetricSummary 格式化测试
// ─────────────────────────────────────────────────────────

func TestFormatMetricSummary(t *testing.T) {
	snapshot := MetricsSnapshot{
		CompressTotal:       100,
		CompressSuccess:     95,
		CompressFailure:     5,
		SuccessRate:         0.95,
		DegradationTotal:    10,
		DegradationRate:     0.10,
		CompressionRatioAvg: 0.5,
		CompressionLatencyAvg: 150.0,
		QualityScoreAvg:     0.85,
		TokenReductionAvg:   0.5,
	}

	result := FormatMetricSummary(snapshot)

	if result == "" {
		t.Error("FormatMetricSummary returned empty string")
	}

	expectedSubstrings := []string{
		"Compression Metrics",
		"Success: 95/100 (95.0%)",
		"Degradation: 10",
		"Avg Ratio: 0.50",
		"Latency: 150ms",
		"Quality: 0.85",
		"Reduction: 50.0%",
	}

	for _, sub := range expectedSubstrings {
		if !contains(result, sub) {
			t.Errorf("FormatMetricSummary missing '%s'\nGot:\n%s", sub, result)
		}
	}
}

// ─────────────────────────────────────────────────────────
// 辅助函数
// ─────────────────────────────────────────────────────────

// calcRate 边界测试
func TestCalcRate(t *testing.T) {
	tests := []struct {
		numerator   int64
		denominator int64
		want        float64
	}{
		{0, 10, 0.0},
		{10, 10, 1.0},
		{5, 10, 0.5},
		{0, 0, 0.0}, // 除零保护
		{1, 0, 0.0}, // 除零保护
	}

	for _, tt := range tests {
		got := calcRate(tt.numerator, tt.denominator)
		if got != tt.want {
			t.Errorf("calcRate(%d, %d) = %f, want %f",
				tt.numerator, tt.denominator, got, tt.want)
		}
	}
}

// ─────────────────────────────────────────────────────────
// 并发安全测试（-race 无报警）
// ─────────────────────────────────────────────────────────

func TestConcurrentAccess(t *testing.T) {
	m := NewMetricsCollector(true, 1000)
	var wg sync.WaitGroup

	// 10 个并发 goroutine，每个记录 100 次
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				m.RecordCompression(CompressionSample{
					Action:           "full",
					OriginalTokens:   1000,
					CompressedTokens: 500,
					Duration:         time.Duration(j) * time.Millisecond,
					DegradationTier:  DegradationTier(id % 4),
				})
			}
		}(i)
	}

	// 同时读取快照
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = m.Snapshot()
			}
		}()
	}

	// 同时读取采样
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = m.RecentSamples(10)
			}
		}()
	}

	wg.Wait()

	// 验证最终结果
	snapshot := m.Snapshot()
	if snapshot.CompressTotal != 1000 {
		t.Errorf("CompressTotal = %d, want 1000", snapshot.CompressTotal)
	}
}

func TestConcurrentReset(t *testing.T) {
	m := NewMetricsCollector(true, 100)
	var wg sync.WaitGroup

	// 一边写入，一边重置
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				m.RecordCompression(CompressionSample{
					Action:           "full",
					OriginalTokens:   1000,
					CompressedTokens: 500,
					Duration:         10 * time.Millisecond,
					DegradationTier:  TierFull,
				})
			}
		}()
	}

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				m.Reset()
			}
		}()
	}

	wg.Wait()
	// 只要不 panic 就算通过
	_ = m.Snapshot()
}

func TestConcurrentEWMA(t *testing.T) {
	m := NewMetricsCollector(false, 0)
	var wg sync.WaitGroup

	// 多个 goroutine 同时更新 EWMA
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				m.RecordCompression(CompressionSample{
					Action:           "full",
					OriginalTokens:   1000,
					CompressedTokens: 500,
					ReductionRatio:   0.5, // 设置 ReductionRatio > 0 以触发 EWMA 更新
					Duration:         10 * time.Millisecond,
					QualityScore:     0.9,
					DegradationTier:  TierFull,
				})
			}
		}()
	}

	wg.Wait()

	// 读取 EWMA 值
	snapshot := m.Snapshot()
	if snapshot.CompressionRatioAvg <= 0 {
		t.Error("CompressionRatioAvg should be > 0 after updates")
	}
	if snapshot.QualityScoreAvg <= 0 {
		t.Error("QualityScoreAvg should be > 0 after updates")
	}
}

// ─────────────────────────────────────────────────────────
// 辅助函数
// ─────────────────────────────────────────────────────────

// contains 检查字符串是否包含子串
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
