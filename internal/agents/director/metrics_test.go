package director

import (
	"testing"
	"time"
)

func TestMetricsCollector_RecordAndSnapshot(t *testing.T) {
	mc := NewMetricsCollector(true)

	mc.RecordLLMDuration(1500 * time.Millisecond)
	mc.RecordLLMDuration(250 * time.Millisecond)

	snap := mc.Snapshot()
	if got := snap.DurationMs["llm_total"]; got != 1750 {
		t.Errorf("DurationMs[llm_total] = %v, want 1750", got)
	}
}

func TestMetricsCollector_Disabled(t *testing.T) {
	mc := NewMetricsCollector(false)

	mc.RecordLLMDuration(1500 * time.Millisecond)

	if got := mc.Snapshot().DurationMs["llm_total"]; got != 0 {
		t.Errorf("disabled: DurationMs[llm_total] = %v, want 0", got)
	}
}

func TestMetricsCollector_Reset(t *testing.T) {
	mc := NewMetricsCollector(true)

	mc.RecordLLMDuration(100 * time.Millisecond)
	mc.Reset()

	if got := mc.Snapshot().DurationMs["llm_total"]; got != 0 {
		t.Errorf("after reset: DurationMs[llm_total] = %v, want 0", got)
	}
}

func TestMetricsCollector_Concurrent(t *testing.T) {
	mc := NewMetricsCollector(true)

	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func() {
			mc.RecordLLMDuration(10 * time.Millisecond)
			done <- true
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}

	if got := mc.Snapshot().DurationMs["llm_total"]; got != 100 {
		t.Errorf("concurrent DurationMs[llm_total] = %v, want 100", got)
	}
}
