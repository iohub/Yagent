package director

import (
	"testing"
	"time"
)

func TestMetricsCollector_RecordAndSnapshot(t *testing.T) {
	mc := NewMetricsCollector(true)

	mc.RecordTaskStart()
	mc.RecordToolCall("read_file", 100*time.Millisecond)
	mc.RecordToolCall("write_file", 200*time.Millisecond)
	mc.RecordError("repo_agent")
	mc.RecordLLMDuration(1500 * time.Millisecond)

	snap := mc.Snapshot()

	if snap.TaskCount != 1 {
		t.Errorf("TaskCount = %d, want 1", snap.TaskCount)
	}
	if snap.ToolCallCount != 2 {
		t.Errorf("ToolCallCount = %d, want 2", snap.ToolCallCount)
	}
	if snap.ErrorCount["repo_agent"] != 1 {
		t.Errorf("ErrorCount[repo_agent] = %d, want 1", snap.ErrorCount["repo_agent"])
	}
}

func TestMetricsCollector_Disabled(t *testing.T) {
	mc := NewMetricsCollector(false)

	mc.RecordTaskStart()
	mc.RecordToolCall("read_file", 100*time.Millisecond)
	mc.RecordError("test")

	snap := mc.Snapshot()
	if snap.TaskCount != 0 {
		t.Errorf("disabled: TaskCount = %d, want 0", snap.TaskCount)
	}
	if snap.ToolCallCount != 0 {
		t.Errorf("disabled: ToolCallCount = %d, want 0", snap.ToolCallCount)
	}
}

func TestMetricsCollector_Reset(t *testing.T) {
	mc := NewMetricsCollector(true)

	mc.RecordTaskStart()
	mc.RecordError("err1")
	mc.Reset()

	snap := mc.Snapshot()
	if snap.TaskCount != 0 {
		t.Errorf("after reset: TaskCount = %d, want 0", snap.TaskCount)
	}
	if len(snap.ErrorCount) != 0 {
		t.Errorf("after reset: ErrorCount should be empty, got %v", snap.ErrorCount)
	}
}

func TestMetricsCollector_MultipleErrors(t *testing.T) {
	mc := NewMetricsCollector(true)

	mc.RecordError("agent_a")
	mc.RecordError("agent_a")
	mc.RecordError("agent_b")

	snap := mc.Snapshot()
	if snap.ErrorCount["agent_a"] != 2 {
		t.Errorf("ErrorCount[agent_a] = %d, want 2", snap.ErrorCount["agent_a"])
	}
	if snap.ErrorCount["agent_b"] != 1 {
		t.Errorf("ErrorCount[agent_b] = %d, want 1", snap.ErrorCount["agent_b"])
	}
}

func TestMetricsCollector_Concurrent(t *testing.T) {
	mc := NewMetricsCollector(true)

	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func() {
			mc.RecordTaskStart()
			mc.RecordToolCall("tool", 10*time.Millisecond)
			done <- true
		}()
	}

	for i := 0; i < 10; i++ {
		<-done
	}

	snap := mc.Snapshot()
	if snap.TaskCount != 10 {
		t.Errorf("concurrent TaskCount = %d, want 10", snap.TaskCount)
	}
}
