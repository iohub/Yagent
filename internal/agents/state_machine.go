package agents

import (
	"fmt"
	"sync"
	"time"
)

// Phase 定义 Director 运行阶段
type Phase string

const (
	PhaseIdle      Phase = "idle"
	PhasePlanning  Phase = "planning"
	PhaseExecuting Phase = "executing"
	PhaseReviewing Phase = "reviewing"
	PhaseDone      Phase = "done"
	PhaseError     Phase = "error"
)

// PhaseTransition 记录一次状态转换
type PhaseTransition struct {
	From      Phase     `json:"from"`
	To        Phase     `json:"to"`
	Timestamp time.Time `json:"timestamp"`
	Reason    string    `json:"reason,omitempty"`
}

// StateMachine 线程安全的状态机
type StateMachine struct {
	current     Phase
	transitions map[Phase][]Phase // 允许的转换
	history     []PhaseTransition
	onEnter     map[Phase]func(from Phase) // 进入状态回调
	onExit      map[Phase]func(to Phase)   // 离开状态回调
	mu          sync.Mutex
}

// NewStateMachine 创建默认状态机
func NewStateMachine() *StateMachine {
	sm := &StateMachine{
		current: PhaseIdle,
		transitions: map[Phase][]Phase{
			PhaseIdle:      {PhasePlanning},
			PhasePlanning:  {PhaseExecuting, PhaseError, PhaseIdle},
			PhaseExecuting: {PhaseReviewing, PhaseError, PhasePlanning},
			PhaseReviewing: {PhaseDone, PhasePlanning, PhaseError, PhaseIdle},
			PhaseDone:      {PhaseIdle},
			PhaseError:     {PhasePlanning, PhaseIdle},
		},
		history: make([]PhaseTransition, 0),
		onEnter: make(map[Phase]func(from Phase)),
		onExit:  make(map[Phase]func(to Phase)),
	}
	return sm
}

// Current 返回当前阶段
func (sm *StateMachine) Current() Phase {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.current
}

// Transition 执行状态转换
func (sm *StateMachine) Transition(to Phase, reason string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// 检查是否合法转换
	allowed, ok := sm.transitions[sm.current]
	if !ok {
		return fmt.Errorf("unknown current phase: %s", sm.current)
	}

	valid := false
	for _, p := range allowed {
		if p == to {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Errorf("invalid transition: %s → %s (allowed from %s: %v)",
			sm.current, to, sm.current, allowed)
	}

	// 执行 exit 回调
	if fn, ok := sm.onExit[sm.current]; ok {
		fn(to)
	}

	from := sm.current
	sm.current = to

	// 记录历史
	sm.history = append(sm.history, PhaseTransition{
		From:      from,
		To:        to,
		Timestamp: time.Now(),
		Reason:    reason,
	})

	// 执行 enter 回调
	if fn, ok := sm.onEnter[to]; ok {
		fn(from)
	}

	return nil
}

// MustTransition 执行状态转换，失败时 panic
func (sm *StateMachine) MustTransition(to Phase, reason string) {
	if err := sm.Transition(to, reason); err != nil {
		panic(err)
	}
}

// CanTransitionTo 检查是否可以转换到指定状态
func (sm *StateMachine) CanTransitionTo(to Phase) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	allowed, ok := sm.transitions[sm.current]
	if !ok {
		return false
	}
	for _, p := range allowed {
		if p == to {
			return true
		}
	}
	return false
}

// OnEnter 注册进入状态回调
func (sm *StateMachine) OnEnter(phase Phase, fn func(from Phase)) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.onEnter[phase] = fn
}

// OnExit 注册离开状态回调
func (sm *StateMachine) OnExit(phase Phase, fn func(to Phase)) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.onExit[phase] = fn
}

// History 返回转换历史
func (sm *StateMachine) History() []PhaseTransition {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	result := make([]PhaseTransition, len(sm.history))
	copy(result, sm.history)
	return result
}

// IsIn 检查是否在指定阶段
func (sm *StateMachine) IsIn(phase Phase) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.current == phase
}

// Reset 重置状态机到初始状态
func (sm *StateMachine) Reset() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.current = PhaseIdle
	sm.history = make([]PhaseTransition, 0)
}

// String 返回状态描述
func (sm *StateMachine) String() string {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return fmt.Sprintf("StateMachine[current=%s, history=%d]", sm.current, len(sm.history))
}
