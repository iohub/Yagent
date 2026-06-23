package agents

import (
	"encoding/json"
	"sync"
	"testing"
	"time"
)

func TestStateMachine_BasicTransitions(t *testing.T) {
	sm := NewStateMachine()

	// 初始状态
	if sm.Current() != PhaseIdle {
		t.Errorf("expected initial phase %s, got %s", PhaseIdle, sm.Current())
	}

	// idle → planning
	if err := sm.Transition(PhasePlanning, "start planning"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sm.Current() != PhasePlanning {
		t.Errorf("expected phase %s, got %s", PhasePlanning, sm.Current())
	}

	// planning → executing
	sm.MustTransition(PhaseExecuting, "start executing")
	if sm.Current() != PhaseExecuting {
		t.Errorf("expected phase %s, got %s", PhaseExecuting, sm.Current())
	}

	// executing → reviewing
	sm.MustTransition(PhaseReviewing, "start reviewing")
	if sm.Current() != PhaseReviewing {
		t.Errorf("expected phase %s, got %s", PhaseReviewing, sm.Current())
	}

	// reviewing → done
	sm.MustTransition(PhaseDone, "review complete")
	if sm.Current() != PhaseDone {
		t.Errorf("expected phase %s, got %s", PhaseDone, sm.Current())
	}
}

func TestStateMachine_InvalidTransition(t *testing.T) {
	sm := NewStateMachine()

	// idle → done should fail
	err := sm.Transition(PhaseDone, "jump to done")
	if err == nil {
		t.Fatal("expected error for invalid transition, got nil")
	}

	// verify state unchanged
	if sm.Current() != PhaseIdle {
		t.Errorf("state should be unchanged, got %s", sm.Current())
	}
}

func TestStateMachine_CanTransitionTo(t *testing.T) {
	sm := NewStateMachine()

	if !sm.CanTransitionTo(PhasePlanning) {
		t.Error("should be able to transition to planning from idle")
	}
	if sm.CanTransitionTo(PhaseDone) {
		t.Error("should NOT be able to transition to done from idle")
	}

	sm.MustTransition(PhasePlanning, "test")
	if !sm.CanTransitionTo(PhaseExecuting) {
		t.Error("should be able to transition to executing from planning")
	}
	if !sm.CanTransitionTo(PhaseError) {
		t.Error("should be able to transition to error from planning")
	}
}

func TestStateMachine_History(t *testing.T) {
	sm := NewStateMachine()

	sm.MustTransition(PhasePlanning, "planning")
	sm.MustTransition(PhaseExecuting, "executing")
	sm.MustTransition(PhaseReviewing, "reviewing")
	sm.MustTransition(PhaseDone, "done")

	history := sm.History()
	if len(history) != 4 {
		t.Fatalf("expected 4 transitions, got %d", len(history))
	}

	// verify history order
	if history[0].From != PhaseIdle || history[0].To != PhasePlanning {
		t.Errorf("first transition wrong: got %s→%s", history[0].From, history[0].To)
	}
	if history[1].From != PhasePlanning || history[1].To != PhaseExecuting {
		t.Errorf("second transition wrong: got %s→%s", history[1].From, history[1].To)
	}
	if history[2].From != PhaseExecuting || history[2].To != PhaseReviewing {
		t.Errorf("third transition wrong: got %s→%s", history[2].From, history[2].To)
	}
	if history[3].From != PhaseReviewing || history[3].To != PhaseDone {
		t.Errorf("fourth transition wrong: got %s→%s", history[3].From, history[3].To)
	}

	// verify timestamps are ordered
	if !history[1].Timestamp.After(history[0].Timestamp) && !history[1].Timestamp.Equal(history[0].Timestamp) {
		t.Error("history timestamps should be non-decreasing")
	}

	// verify returned history is a copy
	history[0].To = PhaseDone
	if sm.History()[0].To != PhasePlanning {
		t.Error("History() should return a copy, not the original slice")
	}
}

func TestStateMachine_Callbacks(t *testing.T) {
	sm := NewStateMachine()

	var entered string
	var exited string

	sm.OnEnter(PhasePlanning, func(from Phase) {
		entered = string(from)
	})
	sm.OnExit(PhaseIdle, func(to Phase) {
		exited = string(to)
	})

	sm.MustTransition(PhasePlanning, "test callbacks")

	if entered != "idle" {
		t.Errorf("expected enter callback with from=idle, got %s", entered)
	}
	if exited != "planning" {
		t.Errorf("expected exit callback with to=planning, got %s", exited)
	}
}

func TestStateMachine_IsIn(t *testing.T) {
	sm := NewStateMachine()

	if !sm.IsIn(PhaseIdle) {
		t.Error("should be in idle initially")
	}

	sm.MustTransition(PhasePlanning, "test")
	if !sm.IsIn(PhasePlanning) {
		t.Error("should be in planning")
	}
	if sm.IsIn(PhaseIdle) {
		t.Error("should NOT be in idle after transition")
	}
}

func TestStateMachine_Reset(t *testing.T) {
	sm := NewStateMachine()

	sm.MustTransition(PhasePlanning, "test")
	sm.MustTransition(PhaseExecuting, "test")
	sm.MustTransition(PhaseReviewing, "test")
	sm.MustTransition(PhaseDone, "test")

	sm.Reset()

	if sm.Current() != PhaseIdle {
		t.Errorf("expected reset to idle, got %s", sm.Current())
	}
	if len(sm.History()) != 0 {
		t.Errorf("expected empty history after reset, got %d", len(sm.History()))
	}
}

func TestStateMachine_String(t *testing.T) {
	sm := NewStateMachine()
	sm.MustTransition(PhasePlanning, "test")

	str := sm.String()
	if str == "" {
		t.Error("String() should not return empty string")
	}
}

func TestStateMachine_ConcurrentAccess(t *testing.T) {
	sm := NewStateMachine()
	var wg sync.WaitGroup

	// 并发读取
	wg.Add(10)
	for i := 0; i < 10; i++ {
		go func() {
			defer wg.Done()
			_ = sm.Current()
			_ = sm.IsIn(PhaseIdle)
			_ = sm.History()
		}()
	}

	// 并发写入
	wg.Add(5)
	for i := 0; i < 5; i++ {
		go func(n int) {
			defer wg.Done()
			if n%2 == 0 {
				sm.Transition(PhasePlanning, "concurrent")
			} else {
				sm.CanTransitionTo(PhasePlanning)
			}
		}(i)
	}

	wg.Wait()
}

func TestStateMachine_MustTransitionPanic(t *testing.T) {
	sm := NewStateMachine()

	defer func() {
		if r := recover(); r == nil {
			t.Error("MustTransition should panic on invalid transition")
		}
	}()

	sm.MustTransition(PhaseDone, "should panic")
}

// --- ConductorState tests ---

func TestConductorState_ComputeChecksum(t *testing.T) {
	state := &ConductorState{
		SessionID: "test-session",
		Phase:     PhasePlanning,
		Iteration: 5,
		StepCount: 10,
		Version:   1,
		Context:   map[string]interface{}{"key": "value"},
	}

	checksum1 := state.ComputeChecksum()
	checksum2 := state.ComputeChecksum()
	if checksum1 != checksum2 {
		t.Error("checksum should be deterministic for same data")
	}
	if len(checksum1) == 0 {
		t.Error("checksum should not be empty")
	}
}

func TestConductorState_ComputeChecksum_ExcludesSavedAt(t *testing.T) {
	state := &ConductorState{
		SessionID: "test-session",
		Phase:     PhasePlanning,
		Version:   1,
	}
	state.SavedAt = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	checksum1 := state.ComputeChecksum()

	// 修改 SavedAt 不应影响校验和
	state.SavedAt = time.Date(2099, 12, 31, 23, 59, 59, 0, time.UTC)
	checksum2 := state.ComputeChecksum()

	if checksum1 != checksum2 {
		t.Error("checksum should not depend on SavedAt field")
	}
}

func TestConductorState_Validate(t *testing.T) {
	// 缺少 session_id
	state := &ConductorState{Phase: PhasePlanning, Version: 1}
	state.Checksum = state.ComputeChecksum()
	err := state.Validate()
	if err == nil {
		t.Error("expected error for missing session_id")
	}

	// 缺少 phase
	state = &ConductorState{SessionID: "test", Version: 1}
	state.Checksum = state.ComputeChecksum()
	err = state.Validate()
	if err == nil {
		t.Error("expected error for missing phase")
	}

	// 校验和不匹配
	state = &ConductorState{
		SessionID: "test",
		Phase:     PhasePlanning,
		Version:   1,
		Checksum:  "bad-checksum",
	}
	err = state.Validate()
	if err == nil {
		t.Error("expected error for checksum mismatch")
	}

	// 合法状态
	state = &ConductorState{
		SessionID: "test",
		Phase:     PhasePlanning,
		Version:   1,
	}
	state.Checksum = state.ComputeChecksum()
	err = state.Validate()
	if err != nil {
		t.Errorf("expected no error for valid state: %v", err)
	}
}

func TestConductorState_TaskState(t *testing.T) {
	state := &ConductorState{
		SessionID:    "test",
		Phase:        PhaseExecuting,
		Iteration:    1,
		StepCount:    3,
		Version:      1,
		ActiveTasks:  make(map[string]*TaskState),
		CompletedIDs: []string{"task-1"},
	}
	state.Checksum = state.ComputeChecksum()

	if err := state.Validate(); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}

	// 添加任务
	state.ActiveTasks["task-2"] = &TaskState{
		ID:     "task-2",
		Type:   "coding",
		Status: "running",
	}

	// 序列化/反序列化
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var restored ConductorState
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if restored.SessionID != "test" {
		t.Errorf("expected session_id 'test', got %q", restored.SessionID)
	}
	if restored.Phase != PhaseExecuting {
		t.Errorf("expected phase %s, got %s", PhaseExecuting, restored.Phase)
	}
	if len(restored.ActiveTasks) != 1 {
		t.Errorf("expected 1 active task, got %d", len(restored.ActiveTasks))
	}
	if restored.ActiveTasks["task-2"].Status != "running" {
		t.Errorf("expected task-2 status 'running', got %q", restored.ActiveTasks["task-2"].Status)
	}
}
