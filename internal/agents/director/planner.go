// Package director — planner.go — P0-1 Phase 3-1：Planner 脚手架（类型与依赖接口）。
//
// 目标：从 DirectorAgent 抽取任务规划主循环（run() 的 LLM 对话循环：系统提示
// 构建 → 多步 LLM 调用 → 工具执行 → 上下文压缩 → 收尾）。本阶段仅定义
// per-run 状态、依赖窄接口与配置（脚手架），Run 为桩实现；
// Phase 3-2 填充主循环实现并切换门面调用点。
//
// 依赖推导来源（接口签名以门面实际用法为准，非凭设计稿猜测）：
//   - EventPublisher：*messaging.MessagePublisher 的 Publish/PublishWithMetadata
//     方法形态（messaging 零 yagent 内部依赖、直连可行，仍按依赖倒置取窄接口，
//     与 compressor.go 的 ThinkLinkStore 同策略）；
//   - ThinklinkStore：内嵌 compressor.go 的只读 ThinkLinkStore（Count/RebuildPrompt），
//     补充门面 run() 主循环的实时写入用法 AddUserInput/AddThoughtPlan
//     （*thinklink.Store 天然实现，签名与门面 director.go 中 a.thinkLink 用法一致）；
//   - ToolRunner：门面按名查找 a.Adapters 并调用的形态（tools.Adapter 的
//     Name/Call(ctx, input)/ToToolDef），门面负责包装查找、超时与 JSON 解包；
//   - PromptBuilder：门面 run() 系统提示构建段（GlobalCtx.FormatPrompt +
//     loadProjectContext（仅首次对话）+ metaHandler.List() 自定义 Agent 列举 +
//     KnowledgeInjector.Inject）在 Phase 3-2 由门面包装为闭包；
//   - Rollout：直接复用 *memory.RolloutWriter（nil 容忍，门面 run() 内创建）；
//   - LLM：复用 types.go 已有 LLMClient 接口。
//
// 硬约束：director 包不得 import agents 包——agents 类型不进入本包
// （参照 compressor.go 约束）；本阶段不切换任何门面调用点（run() 原样）。
package director

import (
	"context"
	"time"

	"yagent/internal/llm"
	"yagent/internal/memory"
	"yagent/internal/thinklink"
)

// EventPublisher 事件发布窄接口（*messaging.MessagePublisher 天然实现）。
// 方法集对齐门面实际用法：a.Publisher.Publish(event, payload, source) 与
// a.Publisher.PublishWithMetadata(event, payload, source, metadata)
// （director.go 中 "context_loaded"/"ai_stream_end"/"llm_call_end"/"ai_response" 等调用点）。
// nil 容忍：调用方判空跳过发布（与门面 if a.Publisher != nil 语义一致）。
type EventPublisher interface {
	Publish(event string, payload interface{}, source string) error
	PublishWithMetadata(event string, payload interface{}, source string, metadata map[string]interface{}) error
}

// ThinklinkStore Planner 所需的 thinklink 完整窄接口（*thinklink.Store 天然实现）。
// 内嵌 compressor.go 的只读 ThinkLinkStore（终极压缩重建上下文：Count/RebuildPrompt），
// 补充 run() 主循环的实时写入用法（AddUserInput/AddThoughtPlan），
// 返回的 bool 表示去重后是否新增条目。
type ThinklinkStore interface {
	ThinkLinkStore
	AddUserInput(content string, step int) (thinklink.Entry, bool)
	AddThoughtPlan(content string, step int) (thinklink.Entry, bool)
}

// ToolRunner 工具执行窄接口：门面将包装 a.Adapters（slice 按名查找 + 调用 +
// delegate_* 超时保护与 delegate_repo 结果 JSON 解包），Planner 只面向本接口。
// 方法集对齐门面 run() 的 toolDefs 构建与工具分发用法。
type ToolRunner interface {
	// Specs 返回全部工具定义（LLM 调用的 tools 参数，原 a.Adapters 逐个 ToToolDef）。
	Specs() []llm.ToolDef
	// Call 按工具名调用工具（原 t.Name() == tc.Function.Name 查找后 t.Call(ctx, argsJSON)）。
	Call(ctx context.Context, name string, argsJSON string) (string, error)
}

// PromptInput 系统提示构建输入。
// 字段推导自门面 run() 系统提示构建段：
//   - Input：用户原始输入（KnowledgeInjector.InjectionContext.UserMessage 所需）；
//   - FirstConversation：是否首次对话（原 mem == nil || len(mem.GetMessages()) == 0，
//     用于仅首次加载项目上下文文件，避免重复注入浪费 token）。
type PromptInput struct {
	Input             string
	FirstConversation bool
}

// PromptBuilder 系统提示构建函数类型。门面负责包装
// GlobalCtx.FormatPrompt/loadProjectContext/KnowledgeInjector/customAgents 列举，
// 返回完整 system prompt（Phase 3-2 接线；注入失败由门面忽略错误继续，与现行为一致）。
type PromptBuilder func(ctx context.Context, in PromptInput) (string, error)

// RunState per-run 规划状态（门面每次任务创建、闭包共享指针）。
// 字段收编自 DirectorAgent 的本次任务状态：执行检测机制字段
// （hasDelegated/nonDelegationPrompts/delegationAttempts）与主循环步数；
// PendingSubAgentMemory 复用 types.go 的 SubAgentMemory（原 *AgentResult
// 的等价载体，Phase 3-2 迁移时门面负责转换）。
type RunState struct {
	Step                  int             // 当前主循环步数
	MaxSteps              int             // 最大步数（原 a.maxSteps）
	HasDelegated          bool            // 本次任务是否已委派过 agent（执行检测机制）
	NonDelegationPrompts  int             // "未委派强制提醒"已注入次数（上限 maxNonDelegationPrompts=3）
	DelegationAttempts    int             // 委派尝试次数统计（成功失败均计）
	PendingSubAgentMemory *SubAgentMemory // 最近一次 delegate 调用的完整结果（nil=无）
}

// PlannerConfig Planner 配置（首版允许略胖：主循环依赖一次注入，Phase 3-2 起逐步收窄）。
// Publisher/Journal/Rollout 均 nil 容忍（调用点判空，行为与门面 nil 语义一致）。
type PlannerConfig struct {
	LLM        LLMClient             // LLM 客户端（复用 types.go 接口）
	Publisher  EventPublisher        // 事件发布（nil 容忍）
	Journal    ThinklinkStore        // thinklink 存储（nil 容忍）
	Rollout    *memory.RolloutWriter // Rollout 写入器（nil 容忍，门面 run() 内创建）
	Tools      ToolRunner            // 工具执行器
	Prompts    PromptBuilder         // 系统提示构建闭包
	Compressor *ContextCompressor    // 上下文压缩编排组件（Phase 3-0 已抽取，同包直接注入）
	MaxSteps   int                   // 主循环最大步数
	LLMTimeout time.Duration         // LLM 调用超时（原 a.llmTimeout，默认 5 分钟）

	// ── Phase 3-2a-1 扩展（Planner.Run 搬迁 run() 主循环所需；除压缩配置外均
	// nil 容忍/零值安全，与门面 nil 语义一致）──
	// Recovery：熔断检查与步骤级重试（原 a.adapter 的
	// IsCircuitBreakerOpen/LLMRetries/RecordLLMSuccess/RecordLLMFailure/
	// RecordLLMFailureStats/ConsecutiveLLMFailures——run() 主循环内联逻辑，
	// RecoveryHandler 为同包类型（recovery.go）故直接注入而非闭包）。
	Recovery *RecoveryHandler
	// Metrics：LLM 耗时指标记录（原 a.adapter.RecordLLMDuration，MetricsCollector
	// 为同包类型（metrics.go）；nil 跳过记录）。
	Metrics *MetricsCollector
	// NormalizeMessages：tool_call/tool_response 配对修复闭包（原 agents 包包级
	// validateAndRepairToolCallPairs；director 禁止 import agents，经门面闭包注入，
	// nil 时跳过修复）。
	NormalizeMessages func([]llm.Message) []llm.Message
	// EstimateTokensFn：单条文本 token 估算闭包（原 agents 包 EstimateTokens，仅
	// usage 缺失时估算用；director 禁止 import agents，经门面闭包注入，nil 时计 0）。
	EstimateTokensFn func(string) int
	// ConvertToolCallsFn：LLM ToolCall → memory.ToolCallData 转换闭包（原 agents
	// 包包级 convertToolCalls；director 禁止 import agents，经门面闭包注入，
	// fn 为 nil 时传 nil ToolCallData）。
	ConvertToolCallsFn func([]llm.ToolCall) []memory.ToolCallData
	// 上下文压缩开关与预算（原 a.EnhancedCommanderCfg 为 config 包配置，门面提取
	// 注入；threshold/keepTokens ≤0 时 Planner 内回退默认值，行为与 run() 一致）。
	CompressEnable            bool // 原 Enable && EnableContextCompression
	CompressThreshold         int  // 原 ContextCompressionThreshold
	CompressKeepTokens        int  // 原 ToolResultKeepTokens
	UltimateCompressEnable    bool // 原 EnableUltimateCompression
	UltimateCompressKeepPlans int  // 原 UltimateCompressionKeepPlans
	// ToolTimeout：普通工具调用超时（原 120s；超时保护本身由门面 Tools.Call 包装
	// 执行，Planner 仅用其做 DeadlineExceeded 错误提示的秒数格式化；delegate_*
	// 专用 10min 超时由门面感知，Planner 不感知）。
	ToolTimeout time.Duration

	// ── P0-Step 2a 扩展（等价基准 executor.go RunAgentLoop；纯加法未接线，
	// 零值/nil=旧行为，后续步骤接线消费）──
	// StopOnFinish：true 且当前工具为 agent_exit 时立即结束循环返回
	// （等价基准 executor.go:553-556，该步不调 OnStepEnd，由 OnAgentExit 收尾）。
	StopOnFinish bool
	// SystemAsHuman：系统消息角色由 System 转为 User（等价基准 executor.go:92-96）。
	SystemAsHuman bool
	// RepoContext：非空时以 "\n\n" 追加到 system prompt 尾部
	// （等价基准 executor.go:98-101）。
	RepoContext string
	// OnAgentStart：主循环开始前调用，返回 error 即终止整个 Run
	// （失败包装 "OnAgentStart hook failed: %w"）；nil 跳过。
	OnAgentStart func(ctx context.Context) error
	// OnAgentExit：Run 最外层 defer 调用一次（含 panic 路径，panic → runErr
	// 包装后传入；hook 返回的 error 仅 slog.Warn、不影响 Run 返回值——
	// 等价基准 executor.go:178-197）；nil 跳过。
	OnAgentExit func(ctx context.Context, runErr error) error
	// OnStepEnd：每步结束调用（hook 返回的 error 仅日志、不终止——等价基准
	// executor.go:559-584）；StopOnFinish 提前返回的那一步不调用；nil 跳过。
	OnStepEnd func(ctx context.Context, step int, runErr error) error
	// OnToolResult：每次工具返回后、结果入历史前调用；nil 跳过。
	OnToolResult func(ctx context.Context, toolName string, result string, callErr error)
}

// PlanInput Planner 单次任务输入。
type PlanInput struct {
	Input  string                     // 用户任务输入
	Mem    *memory.ConversationMemory // 会话 memory（nil=新会话，沿用门面既有语义）
	TaskID string                     // 当前任务 taskID（Rollout 日志用）
}

// PlanResult Planner 单次任务输出。
type PlanResult struct {
	Text       string // 最终回复文本（原 run() 返回值）
	Steps      int    // 实际执行步数
	StopReason string // 停止原因（"agent_exit"/"plain_text"/"max_steps" 等）
	// History 完整内部消息历史（system/user/assistant/tool 全量，循环结束时
	// 赋值，等价基准 executor.go 的 history 切片；供后续子 Agent 迁移时
	// 构造 Memory 用；本步未接线，循环结束前为零值 nil）。
	History []llm.Message
}

// Planner 任务规划器：P0-1 Phase 3-1 脚手架，Phase 3-2 填充主循环实现。
type Planner struct {
	cfg   PlannerConfig
	state *RunState
}

// NewPlanner 构造 Planner。state 由门面每次任务创建（闭包共享指针，
// delegate 适配器经其更新 HasDelegated/DelegationAttempts/PendingSubAgentMemory）。
func NewPlanner(cfg PlannerConfig, state *RunState) *Planner {
	return &Planner{cfg: cfg, state: state}
}

// Run 执行任务规划主循环。
// Phase 3-2a-1 完整实现见 planner_run.go（lift-and-shift 自 DirectorAgent.run）。

// 编译期断言：*thinklink.Store 满足 Planner 所需的完整窄接口（含 compressor 只读部分）。
var _ ThinklinkStore = (*thinklink.Store)(nil)
