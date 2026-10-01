// loop.go — P0 优化 Step 3b：统一装配助手（子 Agent 迁移到 Planner 的基础设施）。
//
// 背景：Planner（internal/agents/director）将成为唯一执行内核，5 个子 Agent
// （repo/coding/chat/devops/browser）+ executeCustomAgent 将从 RunAgentLoop
// （executor.go）迁移过来。本文件提供「ExecutorConfig → PlannerConfig」的
// 装配层与子 Agent 侧的 ToolRunner 适配器，迁移点只需一行
// runSubAgentLoop(ctx, cfg) 调用。
//
// 与 executor.go 同包（agents），可直接使用包级函数：
//   - validateAndRepairToolCallPairs（director.go）— 消息配对修复
//   - EstimateTokens（repo_memory.go）— token 估算
//   - convertToolCalls（director.go）— ToolCall → ToolCallData 转换
//   - InitToolLogger / logToolCall / LogDelegateCall（tool_logger.go）— 工具日志
//   - isInteractiveUserTool（tool_timeout.go）— 交互式工具豁免判定
//
// import 环检查：config 是叶子包；director 包禁止 import agents（其自身硬约束），
// agents → director 单向依赖成立，无环。
package agents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"yagent/internal/agents/director"
	"yagent/internal/config"
	"yagent/internal/llm"
	"yagent/internal/memory"
	"yagent/internal/messaging"
	"yagent/internal/tools"
)

// SubAgentLoopConfig 子 Agent 执行配置（RunAgentLoop ExecutorConfig 的迁移映射）。
// 字段与 executor.go 的 ExecutorConfig 一一对应，差异仅两处：
//   - ToolTimeout 替代 ExecutorConfig 中硬编码的 defaultToolTimeout（180s），
//     0=默认（config.DefaultTimeouts().SubAgentTool，180s）；
//   - hooks 字段（OnAgentStart/OnAgentExit/OnStepEnd）可选注入（P0 补遗：
//     coding.go Git Checkpoint 生命周期恢复使用，等价 executor.go 旧签名）；
//     其余子 Agent 保持 nil。OnToolResult 无对应需求，不提供。
type SubAgentLoopConfig struct {
	SystemPrompt string
	UserInput    string
	Adapters     []*tools.Adapter
	LLM          llm.Engine
	MaxSteps     int
	Publisher    *messaging.MessagePublisher
	AgentName    string
	StopOnFinish bool
	// LLMTimeout 单次 LLM 调用超时；0=默认（config.DefaultTimeouts().LLM，5m，
	// 等价 executor.go:118 的 llmTimeout 硬编码值）。
	LLMTimeout time.Duration
	// SystemAsHuman 系统消息角色由 System 转为 User（等价 executor.go:92-96）。
	SystemAsHuman bool
	// RepoContext 非空时以 "\n\n" 追加到 system prompt 尾部
	// （等价 executor.go:98-101；追加由 Planner 处理）。
	RepoContext string
	// ToolTimeout 普通工具调用超时；0=默认（config.DefaultTimeouts().SubAgentTool，
	// 180s，等价 executor.go:497 的 defaultToolTimeout 硬编码值）。交互式工具
	// （ask_user_for_help）豁免：不加 deadline，无限等待用户响应。
	ToolTimeout time.Duration
	// 上下文压缩开关与预算（等价 executor.go 的 EnableContextCompression/
	// ContextCompressionThreshold/ToolResultKeepTokens）；threshold/keepTokens
	// ≤0 时 Planner 内回退默认值，与 executor.go 行为一致。
	CompressEnable     bool
	CompressThreshold  int
	CompressKeepTokens int
	// Mem 会话 memory；一般 nil（子 Agent 无会话记忆，等价 executor.go 调用点
	// 不传 memory 的现状），非 nil 时透传给 Planner（PlanInput.Mem）。
	Mem *memory.ConversationMemory
	// TaskID 当前任务 ID（Planner 日志用，PlanInput.TaskID）。
	TaskID string

	// ─── 生命周期 hooks（P0 补遗：Git Checkpoint 生命周期恢复）───
	// 以下 3 个 hook 采用 executor.go ExecutorConfig 的旧签名，由
	// runSubAgentLoop 适配/透传到 PlannerConfig（nil 时不注入）。

	// OnAgentStart 主循环开始前调用，返回 error 即终止（等价 executor.go
	// ExecutorConfig.OnAgentStart）。与 PlannerConfig.OnAgentStart 签名一致，
	// 直接透传；nil 时 Planner 内跳过，等价"不注入"。
	OnAgentStart func(ctx context.Context) error
	// OnAgentExit Run 结束（含 panic 路径）时调用一次；返回 error 仅记日志
	//（等价 executor.go 旧签名）。与 PlannerConfig.OnAgentExit 签名一致
	//（func(ctx, error) error），直接透传——Planner defer 内已对返回 error
	// Warn 并统一写入 rollout 结束事件，等价 executor.go 的 defer 内 Warn 行为。
	OnAgentExit func(ctx context.Context, agentErr error) error
	// OnStepEnd 每个执行了工具的步骤结束后调用；错误仅记日志（等价
	// executor.go 旧签名 StepInfo 形式）。PlannerConfig.OnStepEnd 签名为
	// (ctx, step int, runErr error)，由 runSubAgentLoop 内适配闭包重建
	// StepInfo；ToolName 无法填充——Planner 签名无 toolName，gcm.OnStepEnd
	// 已 deprecated 仅日志用途，影响可忽略。
	OnStepEnd func(ctx context.Context, stepInfo StepInfo) error
}

// LoopOutcome 子 Agent 循环结果（等价 ExecutorResult）。
type LoopOutcome struct {
	Text       string
	History    []llm.Message
	Steps      int
	StopReason string
}

// ─────────────────────────────────────────────────────────────────
// adapterToolRunner：子 Agent 侧的 director.ToolRunner 适配器
// ─────────────────────────────────────────────────────────────────

// adapterToolRunner 将 []*tools.Adapter 包装为 director.ToolRunner：
// Specs 负责工具定义构建+排序，Call 负责按名查找、delegate 日志、超时保护
// 与错误转换（等价 executor.go 工具执行段 executor.go:479-536 的职责切分）。
type adapterToolRunner struct {
	adapters  []*tools.Adapter
	agentName string
	// timeout 普通工具调用超时（构造时保证正值：≤0 由 runSubAgentLoop 兜底
	// 回退 config.DefaultTimeouts().SubAgentTool）。交互式工具豁免不加 deadline。
	timeout time.Duration
}

// 编译期断言：*adapterToolRunner 满足 director.ToolRunner。
var _ director.ToolRunner = (*adapterToolRunner)(nil)

// Specs 返回全部工具定义（等价 executor.go:119-126 的 toolDefs 构建+排序：
// 逐个 ToToolDef 后 SortToolDefs，保证 LLM tools 参数确定性、利于 Prompt
// Cache 复用；Planner.Run 内部还会再排一次，幂等无害）。
func (r *adapterToolRunner) Specs() []llm.ToolDef {
	defs := make([]llm.ToolDef, 0, len(r.adapters))
	for _, t := range r.adapters {
		defs = append(defs, t.ToToolDef())
	}
	tools.SortToolDefs(defs)
	return defs
}

// Call 按工具名调用工具（等价 executor.go:479-536 工具执行段）：
//   - 线性查找 adapter（保持现状 O(n)，未找到返回 "Tool %s not found" 防御兜底，
//     等价 executor.go:540-542）；
//   - delegate_ 前缀调用写入专用委派日志（防御性保留，等价 executor.go:489-493）；
//   - WithCancel 剥离父 deadline + WithTimeout 独立超时；交互式工具
//     （isInteractiveUserTool）豁免：仅保留 WithCancel，无限等待用户响应
//     （等价 executor.go:496-510）；
//   - 超时错误（errors.Is DeadlineExceeded）转换为超时提示文本作为 toolResult
//     返回 + nil error（等价 executor.go:529-533，Planner 拿到提示文本直接入
//     历史，不再二次包装）；非超时错误原样上抛（Planner 会截断 1000 + "Error: %s"
//     包装——与 executor.go 的 "Error: %v" 无截断存在微小差异，属统一化改善）；
//   - logToolCall 记录工具日志（等价 executor.go:534，保持工具日志可观测性；
//     agents 包现有函数）。
func (r *adapterToolRunner) Call(ctx context.Context, name string, argsJSON string) (string, error) {
	for _, t := range r.adapters {
		if t.Name() != name {
			continue
		}
		startTime := time.Now()

		// Log delegate tool calls with full arguments to dedicated delegate log
		if strings.HasPrefix(name, "delegate_") {
			LogDelegateCall(name, strings.TrimPrefix(name, "delegate_"), argsJSON)
		}

		// 为工具调用创建独立超时 context，防止非交互工具卡死；
		// 同时 WithCancel 保证了父 context 取消时工具调用也会被取消
		// （剥离父 deadline：父 context 自带的超时不透传给工具调用）。
		cancelCtx, cancelCtxCancel := context.WithCancel(ctx)
		// 交互式等待用户输入的工具（ask_user_for_help）需要无限等待用户响应，
		// 不能加 deadline，否则用户尚未响应调用就会被 context.DeadlineExceeded
		// 自动取消；仅保留 WithCancel，任务中止时仍可经父 context 取消打断等待。
		toolCtx := cancelCtx
		toolCancel := cancelCtxCancel
		if !isInteractiveUserTool(name) {
			toolCtx, toolCancel = context.WithTimeout(cancelCtx, r.timeout)
		}
		toolResult, callErr := t.Call(toolCtx, argsJSON)
		cancelCtxCancel()
		toolCancel()

		if callErr != nil {
			if errors.Is(callErr, context.DeadlineExceeded) {
				// 超时：转换为超时提示文本作为 toolResult 返回（nil error），
				// 等价 executor.go:529-533（日志仍记录原始 DeadlineExceeded）。
				toolResult = fmt.Sprintf("Error: tool execution timed out after %d seconds", int(r.timeout/time.Second))
			} else {
				// 非超时错误原样上抛（Planner 会截断 1000 + "Error: %s" 包装——
				// 与 executor.go 的 "Error: %v" 无截断存在微小差异，属统一化改善）；
				// 日志先留痕（toolResult 文本与 executor.go:532 的覆盖值等价）。
				logToolCall(name, r.agentName, argsJSON, fmt.Sprintf("Error: %v", callErr), callErr, startTime)
				return "", callErr
			}
		}
		logToolCall(name, r.agentName, argsJSON, toolResult, callErr, startTime)
		return toolResult, nil
	}
	// 未找到：防御兜底（等价 executor.go:540-542；nil error——结果文本即入历史）。
	return fmt.Sprintf("Tool %s not found", name), nil
}

// ─────────────────────────────────────────────────────────────────
// runSubAgentLoop：统一装配助手
// ─────────────────────────────────────────────────────────────────

// runSubAgentLoop 子 Agent 统一装配助手：将 SubAgentLoopConfig 装配为
// director.PlannerConfig 并执行 Planner.Run，是子 Agent 从 RunAgentLoop
// 迁移到 Planner 的唯一入口（迁移点只需一行调用）。
//
// 装配等价性（对照 executor.go RunAgentLoop）：
//   - LLM/Publisher：llm.Engine 天然满足 director.LLMClient，
//     *messaging.MessagePublisher 天然实现 director.EventPublisher；
//   - Journal: nil——子 Agent 不用 thinklink（executor.go 无对应物）；
//   - Rollout: memory.GetRolloutWriter(ctx)——等价 executor.go 的 rollout
//     writer 获取方式（nil 容忍）；
//   - Prompts: 静态闭包直接返回 cfg.SystemPrompt——等价 executor.go 的
//     systemPrompt 直接使用；RepoContext 的追加由 Planner 处理（P0-Step 2b）；
//   - Compressor: nil——子 Agent 无紧急/终极压缩，等价 executor.go（仅有
//     常规截断，经 CompressEnable/CompressThreshold/CompressKeepTokens 配置）；
//   - Recovery/Metrics: nil——子 Agent 无熔断/指标；
//   - NormalizeMessages/EstimateTokensFn/ConvertToolCallsFn：agents 包级函数
//     闭包注入（director 禁止 import agents，经 cfg 传递）；
//   - hooks（P0 补遗）：OnAgentStart/OnAgentExit 与 PlannerConfig 签名一致
//     直接透传，OnStepEnd 经适配闭包重建 StepInfo（见各字段注释）；cfg 中为
//     nil 时 Planner 内跳过，等价"不注入"。OnAgentExit 注入时 Planner 的
//     散布 rollout 结束事件写入跳过、由 defer 统一写入（与 executor.go 一致），
//     OnAgentExit==nil 时散布写入生效（现状，属改善）；panic 语义：hook 注入
//     时 Planner defer recover 后调 OnAgentExit（等价 executor.go），未注入时
//     panic 直接上抛。
func runSubAgentLoop(ctx context.Context, cfg SubAgentLoopConfig) (LoopOutcome, error) {
	// 等价 executor.go:91：初始化工具日志器（TUI 模式）。
	_ = InitToolLogger()

	// 超时兜底：≤0 时回退 config 统一默认值（LLM 5m / 普通工具 180s，
	// 与 executor.go:118/497 的硬编码值一致）。
	llmTimeout := cfg.LLMTimeout
	if llmTimeout <= 0 {
		llmTimeout = config.DefaultTimeouts().LLM
	}
	toolTimeout := cfg.ToolTimeout
	if toolTimeout <= 0 {
		toolTimeout = config.DefaultTimeouts().SubAgentTool
	}

	// hooks 适配（P0 补遗）：OnStepEnd 需闭包重建 StepInfo（Planner 签名为
	// (ctx, step int, runErr error)，Planner 调用时 runErr 恒为 nil →
	// Success=true，等价 executor.go 旧行为——OnStepEnd 仅成功路径调用且
	// Success 恒 true）；OnAgentStart/OnAgentExit 签名一致，字面量内直接
	// 透传。nil 时保持不注入（Planner 内跳过）。
	var stepEndHook func(ctx context.Context, step int, runErr error) error
	if cfg.OnStepEnd != nil {
		stepEndHook = func(ctx context.Context, step int, runErr error) error {
			return cfg.OnStepEnd(ctx, StepInfo{StepNumber: step, Success: runErr == nil})
		}
	}

	planner := director.NewPlanner(director.PlannerConfig{
		LLM:       cfg.LLM,       // llm.Engine 天然满足 director.LLMClient
		Publisher: cfg.Publisher, // *messaging.MessagePublisher 天然实现 EventPublisher
		Journal:   nil,           // 子 Agent 不用 thinklink
		// Rollout writer（等价 executor.go 的 rollout writer 获取；nil 容忍）。
		Rollout: memory.GetRolloutWriter(ctx),
		// Tools：子 Agent 侧 ToolRunner 适配器（timeout 与下方 ToolTimeout 一致）。
		Tools: &adapterToolRunner{
			adapters:  cfg.Adapters,
			agentName: cfg.AgentName,
			timeout:   toolTimeout,
		},
		// Prompts：静态闭包——等价 executor.go 的 systemPrompt 直接使用；
		// RepoContext 的追加由 Planner 处理（P0-Step 2b，在系统消息文本装配处）。
		Prompts: func(_ context.Context, _ director.PromptInput) (string, error) {
			return cfg.SystemPrompt, nil
		},
		Compressor: nil, // 子 Agent 无紧急/终极压缩，等价 executor.go
		MaxSteps:   cfg.MaxSteps,
		LLMTimeout: llmTimeout,

		Recovery: nil, // 子 Agent 无熔断/步骤级重试
		Metrics:  nil, // 子 Agent 无 LLM 耗时指标
		// 包级函数闭包注入（director 禁止 import agents，经 cfg 传递）。
		NormalizeMessages:  validateAndRepairToolCallPairs,
		EstimateTokensFn:   EstimateTokens,
		ConvertToolCallsFn: convertToolCalls,

		// 上下文压缩：threshold/keepTokens ≤0 时 Planner 内回退默认值，
		// 与 executor.go 行为一致。
		CompressEnable:         cfg.CompressEnable,
		CompressThreshold:      cfg.CompressThreshold,
		CompressKeepTokens:     cfg.CompressKeepTokens,
		UltimateCompressEnable: false,
		// ToolTimeout：与 adapterToolRunner 实际超时一致（Planner 仅用于
		// DeadlineExceeded 错误提示的秒数格式化；实际超时保护在 Tools.Call 内）。
		ToolTimeout: toolTimeout,

		StopOnFinish:  cfg.StopOnFinish,
		SystemAsHuman: cfg.SystemAsHuman,
		RepoContext:   cfg.RepoContext,
		// hooks 透传（P0 补遗）：OnAgentStart/OnAgentExit 与 PlannerConfig
		// 签名一致直接赋值（nil 时 Planner 内跳过，等价"不注入"；OnAgentExit
		// 返回 error 由 Planner defer 内 Warn，等价 executor.go 行为）；
		// OnStepEnd 经 stepEndHook 适配闭包重建 StepInfo（见上方声明）。
		OnAgentStart: cfg.OnAgentStart,
		OnAgentExit:  cfg.OnAgentExit,
		OnStepEnd:    stepEndHook,

		AgentName:         cfg.AgentName,
		RolloutCollabMode: "single", // 等价 executor.go WriteTurnContext 中 CollaborationMode: "single"
	}, &director.RunState{MaxSteps: cfg.MaxSteps})

	result, err := planner.Run(ctx, director.PlanInput{
		Input:  cfg.UserInput,
		Mem:    cfg.Mem,
		TaskID: cfg.TaskID,
	})
	// 结果转换：LoopOutcome 等价 ExecutorResult（出错时亦返回部分结果——
	// Planner.Run 的 context cancel 等退出路径携带 Steps/StopReason/History）。
	return LoopOutcome{
		Text:       result.Text,
		History:    result.History,
		Steps:      result.Steps,
		StopReason: result.StopReason,
	}, err
}
