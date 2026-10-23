package director

import (
	"sync"
	"time"
)

// MetricsCollector 负责收集和聚合 Director 的可观测性指标。
// 生产侧只有 LLM 耗时这一路真的在记账（planner_run.go 每步调一次 RecordLLMDuration）。
type MetricsCollector struct {
	mu         sync.Mutex
	durationMs map[string]float64
	enabled    bool
}

// NewMetricsCollector 创建指标收集器。
func NewMetricsCollector(enabled bool) *MetricsCollector {
	return &MetricsCollector{
		durationMs: make(map[string]float64),
		enabled:    enabled,
	}
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

	durCopy := make(map[string]float64, len(m.durationMs))
	for k, v := range m.durationMs {
		durCopy[k] = v
	}

	return MetricsSnapshot{
		DurationMs: durCopy,
		Timestamp:  time.Now(),
	}
}

// Reset 重置所有指标。
func (m *MetricsCollector) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.durationMs = make(map[string]float64)
}
