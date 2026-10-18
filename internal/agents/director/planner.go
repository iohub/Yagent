package director

import (
	"context"
	"sync"
	"time"

	"yagent/internal/compression"
	"yagent/internal/llm"
	"yagent/internal/memory"
	"yagent/internal/thinklink"
)

// EventPublisher 是 planner 需要的最小面。刻意**不含** ForDelegation：
// Director 每任务只有一个，自己的流不会与自己混；它的 tool_call_* 事件本来就带 tool_call_id。
// 需要绑定委派坐标的是子 Agent 那一侧（见 agents.EventBus.ForDelegation）。
type EventPublisher interface {
	Publish(event string, payload interface{}, source string) error
	PublishWithMetadata(event string, payload interface{}, source string, metadata map[string]interface{}) error
}

type ThinklinkStore interface {
	compression.ThinkLinkStore
	AddUserInput(content string, step int) (thinklink.Entry, bool)
	AddThoughtPlan(content string, step int) (thinklink.Entry, bool)
}

type ToolRunner interface {
	Specs() []llm.ToolDef
	// Call 执行一次工具调用。callID 是这次调用的 tool_call_id（模型给的那个），
	// 委派类工具用它把父子 rollout 钉在一起（docs/pause-resume-design.md §5.1）。
	// 显式加参数而不是塞进 ctx：接口漂移在编译期就炸，而 ctx 里悄悄传东西的后果是
	// 恢复侧拿到空 delegation_id、把一份本来能救回来的会话判成不可重放。
	Call(ctx context.Context, name string, argsJSON string, callID string) (string, error)
}

type PromptInput struct {
	Input               string
	FirstConversation   bool
	ForceProjectContext bool
}

type PromptBuilder func(ctx context.Context, in PromptInput) (string, error)

type RunState struct {
	Step                  int
	MaxSteps              int
	HasDelegated          bool
	NonDelegationPrompts  int
	DelegationAttempts    int
	PendingSubAgentMemory *memory.SubAgentMemory

	mu sync.Mutex
}

func (s *RunState) RecordDelegation(ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.DelegationAttempts++
	if ok {
		s.HasDelegated = true
	}
}

func (s *RunState) SetPendingSubAgentMemory(m *memory.SubAgentMemory) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.PendingSubAgentMemory = m
}

type PlannerConfig struct {
	LLM        LLMClient
	Publisher  EventPublisher
	Journal    ThinklinkStore
	Rollout    *memory.RolloutWriter
	Tools      ToolRunner
	Prompts    PromptBuilder
	Compressor *compression.ContextCompressor
	MaxSteps   int
	LLMTimeout time.Duration

	Recovery                  *RecoveryHandler
	Metrics                   *MetricsCollector
	NormalizeMessages         func([]llm.Message) []llm.Message
	EstimateTokensFn          func(string) int
	ConvertToolCallsFn        func([]llm.ToolCall) []memory.ToolCallData
	CompressEnable            bool
	CompressThreshold         int
	CompressKeepTokens        int
	UltimateCompressEnable    bool
	UltimateCompressKeepPlans int
	ToolTimeout               time.Duration

	MaxParallelReadOnlyTools int
	IsParallelizableTool     func(name string) bool

	// BoardID 是本次任务的便签板；空串表示便签板不可用
	// （YAGENT_BLACKBOARD=0，或 Director 还没装配 board）。见 board_index.go 的不变量。
	BoardID string
}

// Resumption 是"这一轮不是新任务，而是把上一次停在委派里的活儿接着做完"的凭据。
//
// 由门面层（internal/agents）负责把它凑齐并重跑那次委派 —— 委派机器只在门面层有，
// planner 手上只有 Tools.Call。这里带的是**结果**，planner 只负责把它按 §4.3 的
// 收尾语义落进 memory 与 rollout。
//
// 恢复决策完全在代码里（D7）：模型只看到"那次委派成功返回了"，不需要理解暂停/恢复
// 这个概念。让模型来决定要不要恢复，结果是同样的停点有时重做、有时接着跑。
type Resumption struct {
	// DelegationID 是父 Director 那条 `delegate_*` 的 tool_call_id，
	// 也是这条结果要挂回去的 call_id。
	DelegationID string
	ToolName     string // "delegate_coding"
	AgentName    string // "coding"
	RolloutPath  string // 子 Agent 那份 rollout
	// Result 是重放（或降级报告）的成品文本，planner 原样提交。
	Result string
	// Degraded 非空表示这次没有精确重放，交回去的是一份交接报告。
	// 它必须一路显示到 UI —— 静默降级是本仓库最难查的那类问题。
	Degraded []string
}

type PlanInput struct {
	Input   string
	Mem     *memory.ConversationMemory
	TaskID  string
	Resumed bool
	// PendingResumption 在进 LLM 循环**之前**处理，所以恢复本身不花 token、且确定。
	PendingResumption *Resumption
}

type PlanResult struct {
	Text       string
	Steps      int
	StopReason string
}

type Planner struct {
	cfg   PlannerConfig
	state *RunState
}

func NewPlanner(cfg PlannerConfig, state *RunState) *Planner {
	return &Planner{cfg: cfg, state: state}
}

var _ ThinklinkStore = (*thinklink.Store)(nil)
