package agents

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"yagent/internal/agents/activity"
	director "yagent/internal/agents/director"
	"yagent/internal/artifact"
	"yagent/internal/blackboard"
	"yagent/internal/compression"
	"yagent/internal/config"
	"yagent/internal/knowledge"
	"yagent/internal/llm"
	"yagent/internal/mcp"
	"yagent/internal/memory"
	"yagent/internal/thinklink"
	"yagent/internal/tools"
)

//go:embed director.prompt.md
var directorPrompt string

const maxNonDelegationPrompts = 3

type CustomAgent = director.CustomAgent

type DirectorAgent struct {
	BaseAgent
	RepoAgent    *RepoAgent
	CodingAgent  *CodingAgent
	ChatAgent    *ChatAgent
	MetaAgent    *MetaAgent
	DevOpsAgent  *DevOpsAgent
	BrowserAgent *BrowserAgent
	promptFmt    PromptFormatter
	env          Env
	files        FileToolSet
	search       SearchToolSet
	sys          SysToolSet
	edit         EditToolSet
	flow         FlowToolSet
	thinker      Thinker
	microAgent   MicroAgentRunner
	deepThinker  DeepThinker
	guard        *tools.WorkspaceGuard
	repo         RepoToolSet
	repoCtx      RepoContextStore
	knowledge    KnowledgeProvider
	mcpClient    *mcp.MCPClient
	// Adapters 是 surface 的快照视图，只供仍按字段读取的调用点与测试使用；
	// 工具声明与执行一律走 surface（见 rebuildSurface）。
	Adapters []*tools.Adapter
	// surface 是 Director 的工具面。boardID 按任务变化，而 read_note/read_artifact
	// 的可用性取决于它，所以它按轮重建，不在构造期固化。
	surface *tools.Surface
	// materializedAcrossTurns 记住本会话里已经取回过 schema 的冷工具。
	// Surface 每轮重建，不跨轮保存的话第一轮 load_tool 取回的东西第二轮要再付一次。
	materializedAcrossTurns map[string]bool
	disabledAgents          map[string]bool
	maxSteps                int
	metaRetryCount          int
	metaHandler             *director.MetaAgentHandler
	adapter                 *DirectorAdapter
	llmClient               *llm.Client

	projectCtxLoader *director.ProjectContextLoader

	currentMemory         *memory.ConversationMemory
	pendingSubAgentMemory *AgentResult
	pendingSubAgentMu     sync.Mutex
	compressor            *compression.ContextCompressor

	injectSubAgentMemoryMu sync.Mutex

	llmTimeout time.Duration

	delegateIdleTimeout  time.Duration
	delegateTotalTimeout time.Duration

	maxParallelReadOnlyTools int

	EnhancedCommanderCfg config.EnhancedCommanderConfig
	thinkLink            *thinklink.Store
	taskID               string
	resumedTask          bool
	boardID              string
}

func (a *DirectorAgent) loadProjectContext() *director.ProjectContextLoadResult {
	return a.projectCtxLoader.Load()
}

// DirectorAgentParams 聚合 NewDirectorAgent 所需的全部依赖。
// 用具名字段替代原先 27 个位置参数，让调用点自文档化，且增删依赖不再破坏函数签名。
type DirectorAgentParams struct {
	Formatter   PromptFormatter
	Publisher   EventBus
	Env         Env
	Files       FileToolSet
	Search      SearchToolSet
	Sys         SysToolSet
	Edit        EditToolSet
	Flow        FlowToolSet
	Thinker     Thinker
	MicroAgent  MicroAgentRunner
	DeepThinker DeepThinker
	Repo        RepoToolSet
	Guard       *tools.WorkspaceGuard
	RepoCtx     RepoContextStore
	Knowledge   KnowledgeProvider
	MCPClient   *mcp.MCPClient
	LLM         llm.Engine

	RepoAgent    *RepoAgent
	CodingAgent  *CodingAgent
	ChatAgent    *ChatAgent
	MetaAgent    *MetaAgent
	DevOpsAgent  *DevOpsAgent
	BrowserAgent *BrowserAgent

	MaxSteps       int
	DisabledAgents map[string]bool
	MetaRetryCount int
	Config         config.Config
	LLMClient      *llm.Client
}

// NewDirectorAgent 构造 DirectorAgent：先填充结构体字段，再装配工具适配器
// （基础/JSON 工具 → 委派工具 → 知识工具），最后完成 blackboard、压缩器等后置初始化。
// 委派闭包在运行时读取接收者字段，因此必须先构造 self 再调用两个 build* 方法。
func NewDirectorAgent(p DirectorAgentParams) *DirectorAgent {
	adapterCfg := director.DefaultRecoveryConfig()
	adapterCfg.MaxRetries = p.MaxSteps
	adapterCfg.CircuitBreakerThreshold = p.Config.LLM.CircuitBreakerThreshold
	adapterCfg.CircuitBreakerResetTimeout = p.Config.LLM.CircuitBreakerResetTimeout
	adapterCfg.LLMRetries = p.Config.LLM.StepRetries

	self := &DirectorAgent{
		BaseAgent:        BaseAgent{LLM: p.LLM, Publisher: p.Publisher},
		RepoAgent:        p.RepoAgent,
		CodingAgent:      p.CodingAgent,
		ChatAgent:        p.ChatAgent,
		MetaAgent:        p.MetaAgent,
		DevOpsAgent:      p.DevOpsAgent,
		BrowserAgent:     p.BrowserAgent,
		promptFmt:        p.Formatter,
		env:              p.Env,
		files:            p.Files,
		search:           p.Search,
		sys:              p.Sys,
		edit:             p.Edit,
		flow:             p.Flow,
		thinker:          p.Thinker,
		microAgent:       p.MicroAgent,
		deepThinker:      p.DeepThinker,
		guard:            p.Guard,
		repoCtx:          p.RepoCtx,
		knowledge:        p.Knowledge,
		mcpClient:        p.MCPClient,
		maxSteps:         p.MaxSteps,
		metaRetryCount:   p.MetaRetryCount,
		metaHandler:      director.NewMetaAgentHandler(),
		adapter:          NewDirectorAdapter(true, adapterCfg),
		llmClient:        p.LLMClient,
		projectCtxLoader: director.NewProjectContextLoader(func() string { return p.Env.ProjectPath() }),

		llmTimeout: func() time.Duration {
			// 三级优先：全局运行时值（TUI 模型设置面板可动态修改）> 配置文件 > 程序默认值。
			// 构造期快照仅作兜底；运行期真实值见 currentLLMTimeout()。
			if t := config.GlobalLLMTimeout(); t > 0 {
				return t
			}
			if p.Config.LLM.Timeout > 0 {
				return p.Config.LLM.Timeout
			}
			return config.DefaultLLMCallTimeout
		}(),

		delegateIdleTimeout: func() time.Duration {
			if p.Config.Agent.DelegateIdleTimeout > 0 {
				return p.Config.Agent.DelegateIdleTimeout
			}
			return config.DeriveDelegateIdleTimeout(p.Config.LLM.Timeout)
		}(),
		delegateTotalTimeout: p.Config.Agent.DelegateTotalTimeout,

		maxParallelReadOnlyTools: NormalizeMaxParallelReadOnlyTools(p.Config.Agent.MaxParallelReadOnlyTools),

		EnhancedCommanderCfg: p.Config.EnhancedCommander,

		thinkLink: thinklink.NewStore(0),
	}

	self.disabledAgents = p.DisabledAgents

	if self.boardID == "" {
		self.boardID = blackboard.GenerateAdhocID()
	}

	self.compressor = compression.NewContextCompressor(p.LLM, self.Name(), self.thinkLink, func() { self.setPendingSubAgentMemory(nil) })

	if self.env != nil {
		SetProjectPathProvider(self.env.ProjectPath)
	}

	// 构造期先建一次工具面：让启动日志说清 Director 到底有哪些工具，并把"依赖没装配
	// 所以被跳过"的项汇总出来 —— 历史上这类信息散在 6 处日志里，没人汇总。
	self.rebuildSurface()
	defs := self.surface.Defs()
	names := make([]string, 0, len(defs))
	for _, td := range defs {
		names = append(names, td.Function.Name)
	}
	slog.Info("Director tool definitions initialized",
		"hash", tools.ComputeToolDefsHash(defs),
		"tool_count", len(defs),
		"tool_names", names)

	return self
}

// routeCtx 对 delegate 子任务做难度分类，把选中的档位写入 ctx（供子 agent 的
// RunAgentLoop 解析实际引擎），并发布 model_routed 事件。自动路由未启用时原样返回。
// 分类失败会安全回退到该 agent 的角色基线档位，绝不阻塞委派。
func (a *DirectorAgent) routeCtx(ctx context.Context, agentName, task string) context.Context {
	if a.llmClient == nil {
		return ctx
	}
	r := a.llmClient.GetRouter()
	if !r.Enabled() {
		return ctx
	}
	baseline := r.BaselineOf(agentName)
	tier := r.Classify(ctx, task, baseline)
	ctx = llm.WithTier(ctx, tier)
	if a.Publisher != nil {
		a.Publisher.Publish("model_routed", map[string]interface{}{
			"agent":    agentName,
			"tier":     tier.String(),
			"model":    r.ModelFor(tier),
			"score":    r.ScoreFor(tier),
			"baseline": baseline.String(),
		}, a.Name())
	}
	return ctx
}

func (a *DirectorAgent) Name() string {
	return "Director"
}

// routingEngine 为指定 agent 重建路由引擎包装。必须保留 RoutingEngine 而非裸
// provider 引擎：RunAgentLoop 起始处的 ResolveTierEngine 靠类型识别把档位换成具体
// 引擎，包装一丢档位就被丢弃（model_routed 事件照发、实际模型不变）。
// RoutingEngine 在路由关闭时透明回落到 Client.GetAgentEngine，因此按轮重建同样
// 能拾取运行时的 provider 覆盖与 auto/fixed 切换。
func (a *DirectorAgent) routingEngine(agentName string) llm.Engine {
	if a.llmClient == nil {
		return nil
	}
	return llm.NewRoutingEngine(a.llmClient, agentName)
}

func (a *DirectorAgent) refreshSubAgentEngines() {
	if a.RepoAgent != nil {
		if e := a.routingEngine("repo"); e != nil {
			a.RepoAgent.LLM = e
		}
	}
	if a.CodingAgent != nil {
		if e := a.routingEngine("coding"); e != nil {
			a.CodingAgent.LLM = e
		}
	}
	if a.ChatAgent != nil {
		if e := a.routingEngine("chat"); e != nil {
			a.ChatAgent.LLM = e
		}
	}
	if a.MetaAgent != nil {
		if e := a.routingEngine("meta"); e != nil {
			a.MetaAgent.LLM = e
		}
	}
	if a.DevOpsAgent != nil {
		if e := a.routingEngine("devops"); e != nil {
			a.DevOpsAgent.LLM = e
		}
	}
	if a.BrowserAgent != nil {
		if e := a.routingEngine("browser"); e != nil {
			a.BrowserAgent.LLM = e
		}
	}
}

func toSnakeCase(name string) string {
	var result strings.Builder
	for i, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			result.WriteRune(r)
		} else if r == ' ' || r == '-' || r == '_' {
			if i > 0 {
				result.WriteRune('_')
			}
		} else {
			result.WriteRune('_')
		}
	}
	raw := result.String()
	for strings.Contains(raw, "__") {
		raw = strings.ReplaceAll(raw, "__", "_")
	}
	raw = strings.Trim(raw, "_")
	if raw == "" {
		raw = "custom_agent"
	}
	return raw
}

// currentLLMTimeout 返回当前生效的 LLM 调用超时：全局运行时值（TUI 模型设置面板
// 可动态修改）优先，未初始化时回落构造期快照 a.llmTimeout。
// 每次构造 ExecutorConfig / PlannerConfig 时求值（而非使用构造期快照），
// 确保 TUI 调整后新发起的 LLM 调用立即使用新值。
func (a *DirectorAgent) currentLLMTimeout() time.Duration {
	if t := config.GlobalLLMTimeout(); t > 0 {
		return t
	}
	if a.llmTimeout > 0 {
		return a.llmTimeout
	}
	return config.DefaultLLMCallTimeout
}

func (a *DirectorAgent) executeCustomAgent(ctx context.Context, ca *CustomAgent, surface *tools.Surface, task string) (string, error) {
	systemPrompt := a.promptFmt.FormatPrompt(ca.SystemPrompt)

	cfg := DefaultExecutorConfig()
	cfg.MaxParallelReadOnlyTools = a.maxParallelReadOnlyTools
	cfg.SystemPrompt = systemPrompt
	cfg.UserInput = task
	cfg.Surface = surface
	cfg.LLM = a.LLM
	cfg.MaxSteps = 15
	cfg.Publisher = a.Publisher
	cfg.AgentName = ca.DisplayName
	cfg.StopOnFinish = true
	cfg.LLMTimeout = a.currentLLMTimeout()

	if rolloutWriter := a.createRolloutWriter(ca.DisplayName, task); rolloutWriter != nil {
		defer rolloutWriter.Close()
		ctx = memory.WithRolloutWriter(ctx, rolloutWriter)
	}

	result, err := RunAgentLoop(ctx, cfg)
	if err != nil {
		return "", err
	}
	// 统一走委派结果分级构造基础设施：Text=完整子代理输出（delegate 闭包与
	// note 程序化消费），Summary=分级摘要（预算内=全文；超预算时落盘 artifact
	// 并生成带 ID 的摘要）。此前手工构造漏填 Summary，导致 FormatForDirector
	// 注入上下文时子代理输出丢失（回归测试 TestCustomAgentDelegateTool_* 钉住）。
	agentResult := FinalizeResultFull(ca.DisplayName, task, result)
	a.setPendingSubAgentMemory(&agentResult)
	return result.Text, nil
}

func (a *DirectorAgent) injectSubAgentMemory(result AgentResult, toolCallID string, toolName string) {
	a.injectSubAgentMemoryMu.Lock()
	defer a.injectSubAgentMemoryMu.Unlock()

	if a.currentMemory == nil {
		return
	}

	if result.Text != "" {
		metadata := map[string]interface{}{
			"type":      "sub_agent_summary",
			"tool":      toolName,
			"msg_count": len(result.Memory),
		}
		if result.ArtifactRef != nil {
			metadata["artifact_id"] = result.ArtifactRef.ID
		}
		summaryMsg := memory.ChatMessage{
			Type:       memory.MessageTypeAssistant,
			Content:    FormatForDirector(toolName, result, "", 0),
			Timestamp:  time.Now(),
			GroupID:    fmt.Sprintf("%s_summary_%d", toolName, time.Now().UnixNano()),
			ParentID:   toolCallID,
			IsSubAgent: true,
			Metadata:   metadata,
		}
		a.currentMemory.Messages = append(a.currentMemory.Messages, summaryMsg)
	}

}

func (a *DirectorAgent) SetTaskID(taskID string) {
	a.taskID = taskID
	a.boardID = blackboard.SanitizeBoardID(taskID)
}

func (a *DirectorAgent) SetResumedTask(resumed bool) {
	a.resumedTask = resumed
}

func (a *DirectorAgent) createRolloutWriter(agentName, task string) *memory.RolloutWriter {
	projectID := a.computeProjectID()

	writer, err := memory.NewRolloutWriter(agentName, a.taskID, projectID)
	if err != nil {
		slog.Warn("Rollout: failed to create writer, continuing without rollout logging",
			"agent", agentName,
			"error", err,
		)
		return nil
	}

	slog.Debug("Rollout: writer created for delegate agent",
		"agent", agentName,
		"file", writer.FilePath(),
	)

	return writer
}

func (a *DirectorAgent) computeProjectID() string {
	return director.ComputeProjectID(a.env.ProjectPath())
}

func (a *DirectorAgent) setPendingSubAgentMemory(r *AgentResult) {
	a.pendingSubAgentMu.Lock()
	defer a.pendingSubAgentMu.Unlock()
	a.pendingSubAgentMemory = r
}

func (a *DirectorAgent) takePendingSubAgentMemory() *AgentResult {
	a.pendingSubAgentMu.Lock()
	defer a.pendingSubAgentMu.Unlock()
	r := a.pendingSubAgentMemory
	a.pendingSubAgentMemory = nil
	return r
}

func (a *DirectorAgent) applyEnhancedCommander(
	agentType string,
	task string,
	result AgentResult,
	err error,
	noteRef string,
	unresolvedCount int,
) (string, error) {
	a.setPendingSubAgentMemory(&result)

	if err != nil {
		return "", err
	}

	return FormatForDirector(agentType, result, noteRef, unresolvedCount), nil
}

func blackboardEnabled() bool {
	return os.Getenv("YAGENT_BLACKBOARD") != "0"
}

func parseContextRefs(raw interface{}) []string {
	arr, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	refs := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			refs = append(refs, s)
		}
	}
	return refs
}

func (a *DirectorAgent) writeDelegateNote(agentName, toolName, task string, result AgentResult, runErr error) string {
	if !blackboardEnabled() || a.boardID == "" {
		return ""
	}
	status := "ok"
	if runErr != nil {
		status = "error"
	}
	var artifactIDs []string
	if result.ArtifactRef != nil {
		artifactIDs = []string{result.ArtifactRef.ID}
	}
	body := result.Text
	if result.ArtifactRef != nil {
		body = result.Summary
	}
	noteRef, err := blackboard.WriteNote(blackboard.NoteInput{
		BoardID:     a.boardID,
		Agent:       agentName,
		Tool:        toolName,
		Task:        task,
		Summary:     result.Summary,
		Body:        body,
		ArtifactIDs: artifactIDs,
		Status:      status,
	})
	if err != nil {
		slog.Warn("blackboard: failed to write note", "agent", agentName, "error", err)
		return ""
	}
	slog.Debug("blackboard: note written", "agent", agentName, "noteRef", noteRef, "boardID", a.boardID)
	return noteRef
}

func (a *DirectorAgent) resolveDelegateRefs(ctx context.Context, task string, params map[string]interface{}) (packedTask string, unresolvedCount int) {
	if !blackboardEnabled() {
		return task, 0
	}
	refs := parseContextRefs(params["context_refs"])
	if len(refs) == 0 {
		return task, 0
	}
	pack := blackboard.ResolveRefs(a.boardID, refs, a.artifactExists)
	unresolvedCount = len(pack.Unresolved)
	packText := pack.Text()
	if packText == "" {
		return task, unresolvedCount
	}
	slog.Debug("blackboard: context pack assembled", "refs", len(refs), "entries", len(pack.Entries), "unresolved", unresolvedCount, "packChars", len(packText))
	return packText + "\n---\n" + task, unresolvedCount
}

func (a *DirectorAgent) artifactExists(id string) bool {
	projectID := a.computeProjectID()
	_, err := artifact.DefaultStore().Load(projectID, id)
	return err == nil
}

func convertToolCalls(tcs []llm.ToolCall) []memory.ToolCallData {
	var res []memory.ToolCallData
	for _, tc := range tcs {
		res = append(res, memory.ToolCallData{
			ID:   tc.ID,
			Type: tc.Type,
			Function: memory.ToolCallFunction{
				Name:      tc.Function.Name,
				Arguments: json.RawMessage(tc.Function.Arguments),
			},
		})
	}
	return res
}

var _ Agent = (*DirectorAgent)(nil)

var _ = director.NewPlanner

func (a *DirectorAgent) Run(ctx context.Context, input string) (AgentResult, error) {
	mem, _ := memory.GetConversationMemory(ctx)

	text, err := a.run(ctx, input, mem)

	var memHistory []memory.ChatMessage
	if mem != nil {
		memHistory = mem.GetMessages()
	}
	return AgentResult{Text: text, Memory: memHistory}, err
}

func toSubAgentMemory(result *AgentResult) *memory.SubAgentMemory {
	if result == nil {
		return nil
	}
	return &memory.SubAgentMemory{Text: result.Text, Memory: result.Memory}
}

type directorToolRunner struct {
	agent *DirectorAgent
	state *director.RunState
}

func (r *directorToolRunner) Specs() []llm.ToolDef {
	if r.agent.surface == nil {
		// 兼容仍手工注入 Adapters 的调用点与测试（生产路径总是有 Surface）。
		defs := make([]llm.ToolDef, len(r.agent.Adapters))
		for i, ad := range r.agent.Adapters {
			defs[i] = ad.ToToolDef()
		}
		return defs
	}
	// 每次调用都重新向 Surface 取：deferred 的取回（load_tool）与 Meta 运行期注册的
	// Custom Agent 都要在下一步就对模型可见。过去这里返回构造期快照、planner 每轮再
	// 冻结一次，结果新注册的委派工具本回合只有代码能调到。
	return r.agent.surfaceOrDefault().Defs()
}

func (r *directorToolRunner) Call(ctx context.Context, name string, argsJSON string) (string, error) {
	a := r.agent

	ad, state := a.resolveTool(name)
	if state == tools.StateUnknown || ad == nil {
		return fmt.Sprintf("Tool %s not found", name), nil
	}
	// 模型跳过 load_tool 直接调冷工具：不判它幻觉，把完整 schema 回吐一次，
	// 同一步就能自己纠正过来。
	if state == tools.StateColdKnown {
		schema, err := a.surfaceOrDefault().Materialize([]string{name})
		if err != nil {
			return "", fmt.Errorf("cannot load schema for %s: %w", name, err)
		}
		return fmt.Sprintf("Error: schema for %s was not loaded. Full schema:\n%s\nRetry the call now with these parameters.", name, schema), nil
	}

	if strings.HasPrefix(ad.Name(), "delegate_") {
		agentName := strings.TrimPrefix(ad.Name(), "delegate_")
		LogDelegateCall(ad.Name(), agentName, argsJSON)
	}

	toolTimeout := 120 * time.Second
	isDelegate := strings.HasPrefix(name, "delegate_")
	cancelCtx, cancelCtxCancel := context.WithCancel(ctx)
	toolCtx := cancelCtx
	toolCancel := cancelCtxCancel
	switch {
	case isInteractiveUserTool(name):
	case isDelegate:
		toolCtx, toolCancel = activity.WithIdleTimeout(cancelCtx, activity.Options{
			IdleTimeout:  a.delegateIdleTimeout,
			TotalTimeout: a.delegateTotalTimeout,
		})
	default:
		toolCtx, toolCancel = context.WithTimeout(cancelCtx, toolTimeout)
	}
	toolResult, err := ad.Call(toolCtx, argsJSON)
	cancelCtxCancel()
	toolCancel()

	if psm := a.takePendingSubAgentMemory(); psm != nil {
		a.injectSubAgentMemory(*psm, "", name)
		r.state.SetPendingSubAgentMemory(toSubAgentMemory(psm))
	}

	if strings.HasPrefix(ad.Name(), "delegate_") {
		r.state.RecordDelegation(err == nil)
	}

	if err != nil {
		cause := context.Cause(toolCtx)
		var idleErr *activity.IdleTimeoutError
		var totalErr *activity.TotalTimeoutError
		switch {
		case errors.As(cause, &idleErr):
			return "", fmt.Errorf("tool execution timed out (idle): %w", idleErr)
		case errors.As(cause, &totalErr):
			return "", fmt.Errorf("tool execution timed out (total): %w", totalErr)
		case errors.Is(err, context.DeadlineExceeded):
			return "", fmt.Errorf("tool execution timed out after %d seconds", int(toolTimeout.Seconds()))
		}
		return "", err
	} else if ad.Name() == "delegate_repo" {
		if psm := r.state.PendingSubAgentMemory; psm != nil {
			a.repoCtx.Set(psm.Text)
		} else {
			var summary string
			if uerr := json.Unmarshal([]byte(toolResult), &summary); uerr == nil {
				a.repoCtx.Set(summary)
			} else {
				a.repoCtx.Set(toolResult)
			}
		}
	}
	return toolResult, nil
}

func (a *DirectorAgent) run(ctx context.Context, input string, mem *memory.ConversationMemory) (string, error) {
	resumedTask := a.resumedTask
	a.resumedTask = false

	// boardID 是按任务写入的（SetTaskID），而 read_note/read_artifact 的可用性取决于它，
	// 所以每轮开始重建工具面。
	a.rebuildSurface()

	a.currentMemory = mem
	defer func() { a.currentMemory = nil }()

	if a.llmClient != nil {
		a.LLM = a.routingEngine("director")
		a.refreshSubAgentEngines()
	}

	state := &director.RunState{
		MaxSteps:              a.maxSteps,
		HasDelegated:          false,
		NonDelegationPrompts:  0,
		DelegationAttempts:    0,
		PendingSubAgentMemory: nil,
	}

	prompts := func(ctx context.Context, in director.PromptInput) (string, error) {
		systemPrompt := a.promptFmt.FormatPrompt(directorPrompt)
		if a.surface != nil {
			if catalog := a.surface.Catalog(); catalog != "" {
				systemPrompt += "\n\n" + catalog
			}
		}
		var projectContext string
		if in.FirstConversation || in.ForceProjectContext {
			if loadResult := a.loadProjectContext(); loadResult != nil && loadResult.Content != "" {
				if a.Publisher != nil {
					a.Publisher.Publish("context_loaded", loadResult, a.Name())
				}
				projectContext = fmt.Sprintf("\n\n### Project Workspace Context\n%s\n", loadResult.Content)
			}
		}

		systemPrompt += a.customAgentPromptSection()

		if projectContext != "" {
			systemPrompt += projectContext
		}

		if a.knowledge != nil {
			injCtx := knowledge.InjectionContext{
				UserMessage: in.Input,
				TargetFiles: nil,
				AgentName:   a.Name(),
			}
			if knowledgeBlock, err := a.knowledge.Inject(ctx, injCtx); err == nil && knowledgeBlock != "" {
				systemPrompt += knowledgeBlock
			}
		}
		return systemPrompt, nil
	}

	var directorRolloutWriter *memory.RolloutWriter
	if rw := a.createRolloutWriter("director", input); rw != nil {
		directorRolloutWriter = rw
		defer func() {
			if directorRolloutWriter != nil {
				directorRolloutWriter.Close()
			}
		}()
	}

	toolRunner := &directorToolRunner{agent: a, state: state}

	var recovery *director.RecoveryHandler
	var metrics *director.MetricsCollector
	if a.adapter != nil {
		recovery = a.adapter.recovery
		metrics = a.adapter.metrics
	}

	cfg := director.PlannerConfig{
		LLM:                       a.LLM,
		Publisher:                 a.Publisher,
		Journal:                   a.thinkLink,
		Rollout:                   directorRolloutWriter,
		Tools:                     toolRunner,
		Prompts:                   prompts,
		Compressor:                a.compressor,
		MaxSteps:                  a.maxSteps,
		LLMTimeout:                a.currentLLMTimeout(),
		Recovery:                  recovery,
		Metrics:                   metrics,
		NormalizeMessages:         validateAndRepairToolCallPairs,
		EstimateTokensFn:          EstimateTokens,
		ConvertToolCallsFn:        convertToolCalls,
		CompressEnable:            a.EnhancedCommanderCfg.Enable && a.EnhancedCommanderCfg.EnableContextCompression,
		CompressThreshold:         a.EnhancedCommanderCfg.ContextCompressionThreshold,
		CompressKeepTokens:        a.EnhancedCommanderCfg.ToolResultKeepTokens,
		UltimateCompressEnable:    a.EnhancedCommanderCfg.EnableUltimateCompression,
		UltimateCompressKeepPlans: a.EnhancedCommanderCfg.UltimateCompressionKeepPlans,
		ToolTimeout:               120 * time.Second,

		MaxParallelReadOnlyTools: a.maxParallelReadOnlyTools,
		IsParallelizableTool:     a.isToolParallelizable,
	}

	planner := director.NewPlanner(cfg, state)
	result, err := planner.Run(ctx, director.PlanInput{
		Input:   input,
		Mem:     mem,
		TaskID:  a.taskID,
		Resumed: resumedTask,
	})
	a.carryOverMaterialized()
	return result.Text, err
}

// resolveTool 按名解析执行器。有 Surface 时以注册表为准（含冷工具状态）；
// 没有 Surface 时回退到 Adapters 快照 —— 生产路径恒有 Surface，这个回退只为
// 仍手工注入适配器表的测试与调用点保留。
func (a *DirectorAgent) resolveTool(name string) (*tools.Adapter, tools.ToolState) {
	if a.surface != nil {
		return a.surface.Lookup(name)
	}
	for _, ad := range a.Adapters {
		if ad.Name() == name {
			return ad, tools.StateReady
		}
	}
	return nil, tools.StateUnknown
}

func (a *DirectorAgent) isToolParallelizable(name string) bool {
	if isInteractiveUserTool(name) || isSerialTool(name) {
		return false
	}
	ad, state := a.resolveTool(name)
	return state == tools.StateReady && ad != nil && ad.IsReadOnly()
}

func (a *DirectorAgent) applyEmergencyCompression(ctx context.Context, messages []llm.Message, threshold int) ([]llm.Message, *compression.EmergencyCompressionStats) {
	return a.compressor.ApplyEmergency(ctx, messages, threshold, a.currentMemory)
}

type UltimateCompressionStats = compression.UltimateCompressionStats

func (a *DirectorAgent) applyUltimateCompression(messages []llm.Message, threshold int) ([]llm.Message, *UltimateCompressionStats) {
	return a.compressor.ApplyUltimate(messages, threshold, a.currentMemory, a.EnhancedCommanderCfg.UltimateCompressionKeepPlans)
}

func validateAndRepairToolCallPairs(messages []llm.Message) []llm.Message {
	result := make([]llm.Message, 0, len(messages))

	for i := 0; i < len(messages); i++ {
		msg := messages[i]

		if msg.Role == llm.RoleAssistant && len(msg.ToolCalls) > 0 {
			expectedIDs := make(map[string]bool, len(msg.ToolCalls))
			for _, tc := range msg.ToolCalls {
				expectedIDs[tc.ID] = true
			}

			matchedResponses := make(map[string]llm.Message)
			j := i + 1
			for j < len(messages) {
				next := messages[j]
				if next.Role == llm.RoleTool && next.ToolCallID != "" {
					if expectedIDs[next.ToolCallID] {
						matchedResponses[next.ToolCallID] = next
					}
					j++
				} else if next.Role == llm.RoleAssistant {
					break
				} else {
					break
				}
			}

			allResponsesPresent := len(matchedResponses) == len(msg.ToolCalls)

			if allResponsesPresent {
				result = append(result, msg)
				for _, tc := range msg.ToolCalls {
					if resp, ok := matchedResponses[tc.ID]; ok {
						result = append(result, resp)
					}
				}
			} else {
				missingIDs := make([]string, 0)
				for _, tc := range msg.ToolCalls {
					if _, ok := matchedResponses[tc.ID]; !ok {
						missingIDs = append(missingIDs, tc.ID)
					}
				}
				slog.Warn("Removing incomplete tool_call group due to context compression",
					"expected", len(msg.ToolCalls),
					"found", len(matchedResponses),
					"missing_ids", missingIDs,
				)
				if msg.Content != "" {
					preserved := msg
					preserved.ToolCalls = nil
					result = append(result, preserved)
				}
			}

			i = j - 1
			continue
		}

		if msg.Role == llm.RoleTool && msg.ToolCallID != "" {
			hasMatchingAssistant := false
			for k := len(result) - 1; k >= 0; k-- {
				if result[k].Role == llm.RoleAssistant && len(result[k].ToolCalls) > 0 {
					for _, tc := range result[k].ToolCalls {
						if tc.ID == msg.ToolCallID {
							hasMatchingAssistant = true
							break
						}
					}
					break
				}
				if result[k].Role == llm.RoleUser || result[k].Role == llm.RoleAssistant {
					break
				}
			}
			if !hasMatchingAssistant {
				slog.Warn("Removing orphan tool message (no matching assistant)",
					"tool_call_id", msg.ToolCallID,
				)
				continue
			}
		}

		result = append(result, msg)
	}

	result = llm.MergeConsecutiveAssistants(result)

	return result
}
