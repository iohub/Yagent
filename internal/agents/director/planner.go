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
	Call(ctx context.Context, name string, argsJSON string) (string, error)
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

type PlanInput struct {
	Input   string
	Mem     *memory.ConversationMemory
	TaskID  string
	Resumed bool
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
