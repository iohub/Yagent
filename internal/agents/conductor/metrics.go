package conductor

import (
	"sync"
	"time"
)

// MetricsCollector 负责收集和聚合 Conductor 的可观测性指标。
// 职责包括：任务计数、工具调用统计、错误统计、耗时统计。
type MetricsCollector struct {
	mu          sync.Mutex
	taskCount   int
	toolCallCnt int
	errorCount  map[string]int
	durationMs  map[string]float64
	enabled     bool
}

// NewMetricsCollector 创建指标收集器。
func NewMetricsCollector(enabled bool) *MetricsCollector {
	return &MetricsCollector{
		errorCount: make(map[string]int),
		durationMs: make(map[string]float64),
		enabled:    enabled,
	}
}

// RecordTaskStart 记录任务开始。
func (m *MetricsCollector) RecordTaskStart() {
	if !m.enabled {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.taskCount++
}

// RecordToolCall 记录工具调用。
func (m *MetricsCollector) RecordToolCall(toolName string, duration time.Duration) {
	if !m.enabled {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.toolCallCnt++
	m.durationMs[toolName] += float64(duration.Milliseconds())
}

// RecordError 记录错误。
func (m *MetricsCollector) RecordError(source string) {
	if !m.enabled {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.errorCount[source]++
}

// RecordLLMDuration 记录 LLM 调用耗时。
func (m *MetricsCollector) RecordLLMDuration(d time.Duration) {
	if !m.enabled {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.durationMs["llm_total"] += float64(d.Milliseconds())
}

// Snapshot 返回当前指标的快照。
func (m *MetricsCollector) Snapshot() MetricsSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()

	errCopy := make(map[string]int, len(m.errorCount))
	for k, v := range m.errorCount {
		errCopy[k] = v
	}
	durCopy := make(map[string]float64, len(m.durationMs))
	for k, v := range m.durationMs {
		durCopy[k] = v
	}

	return MetricsSnapshot{
		TaskCount:     m.taskCount,
		ToolCallCount: m.toolCallCnt,
		ErrorCount:    errCopy,
		DurationMs:    durCopy,
		Timestamp:     time.Now(),
	}
}

// Reset 重置所有指标。
func (m *MetricsCollector) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.taskCount = 0
	m.toolCallCnt = 0
	m.errorCount = make(map[string]int)
	m.durationMs = make(map[string]float64)
}
