package agents

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"yagent/internal/agents/activity"
	director "yagent/internal/agents/director"
	"yagent/internal/artifact"
	"yagent/internal/blackboard"
	"yagent/internal/compression"
	"yagent/internal/config"
	"yagent/internal/llm"
	"yagent/internal/memory"
	"yagent/internal/pause"
	"yagent/internal/thinklink"
	"yagent/internal/tokenutil"
	"yagent/internal/tools"
)

//go:embed director.prompt.md
var directorPrompt string

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
	// surface 是 Director 唯一的工具面：工具声明、按名解析、并发准入都问它。
	// 这里不再有 Adapters 快照视图 —— 两份同形来源正是子 Agent 那边并行失效的成因
	// （设计稿 R1），Director 侧没踩中只是因为谓词恰好走了 surface。
	// boardID 按任务变化，而 read_note/read_artifact 的可用性取决于它，
	// 所以它按轮重建，不在构造期固化。
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

	// blueprint 是常驻骨架的注入视图（构造期从 params 拷一份，见 NewDirectorAgent）。
	blueprint *BlueprintView

	// external 是外部工具规格与技能通道（构造期从 params 拷一份）。
	external External

	currentMemory *memory.ConversationMemory
	compressor    *compression.ContextCompressor

	llmTimeout time.Duration

	delegateIdleTimeout  time.Duration
	delegateTotalTimeout time.Duration

	maxParallelReadOnlyTools int

	// maxParallelDelegations 是一次 Director 运行里同时可跑几个子 Agent
	// （[agent] max_parallel_delegations；池子建在每次 Run 的 toolRunner 上）。
	maxParallelDelegations int

	EnhancedCommanderCfg config.EnhancedCommanderConfig
	thinkLink            *thinklink.Store
	taskID               string
	resumedTask          bool
	// pendingResumption 是 TUI/HTTP 的"从子 Agent 继续"入口交进来的落点。
	// nil = 正常新任务。取用于 run()，用完即清。
	pendingResumption *DelegationStop
	boardID           string
}

func (a *DirectorAgent) loadProjectContext() *director.ProjectContextLoadResult {
	return a.projectCtxLoader.Load()
}

// skillCatalog 是 Director 的 system prompt 里那段技能目录（"" = 不注入）。
// 判据收在 skillCatalogFor：五个子 Agent 也调同一个函数。
func (a *DirectorAgent) skillCatalog() string {
	if s := skillCatalogFor("director", a.external.Skills); s != "" {
		return "\n\n" + s
	}
	return ""
}

// 只注入 Director：子 Agent 不需要知道拓扑，它们的工具面里没有就是没有
// （Skipped 的日志足够排障）。
func (a *DirectorAgent) mcpStatusLine() string {
	if a.external.Status == nil || !config.MCPEnabled() || !config.GlobalMCP().StatusLine {
		return ""
	}
	line := strings.TrimSpace(a.external.Status())
	if line == "" {
		return ""
	}
	return "\n\n## MCP servers\n" + line
}

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

	// Blueprint 是仓库蓝图的注入视图（nil = 这个项目没有蓝图）。
	// Director 拿到的只有形状②的骨架（目录→目录），符号级问题仍然归 codeseek ——
	// 这条分工写进 prompt，不写进代码，是因为它约束的是"该委派谁"。
	Blueprint *BlueprintView

	// External 是外部工具规格与技能通道（见 external.go）。
	External External
}

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
		maxSteps:         p.MaxSteps,
		metaRetryCount:   p.MetaRetryCount,
		metaHandler:      director.NewMetaAgentHandler(),
		adapter:          NewDirectorAdapter(true, adapterCfg),
		llmClient:        p.LLMClient,
		blueprint:        p.Blueprint,
		external:         p.External,
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

		maxParallelDelegations: NormalizeMaxParallelDelegations(p.Config.Agent.MaxParallelDelegations),

		EnhancedCommanderCfg: p.Config.EnhancedCommander,

		thinkLink: thinklink.NewStore(0),
	}

	self.disabledAgents = p.DisabledAgents

	if self.boardID == "" {
		self.boardID = blackboard.GenerateAdhocID()
	}

	self.compressor = compression.NewContextCompressor(p.LLM, self.Name(), self.thinkLink)

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
	// 手动选过 provider 的 agent 不走档位（RoutingEngine.resolve 里覆盖优先），
	// 所以既不必花一次分类调用，也不能发 model_routed 去描述一个不会被用到的模型。
	if a.llmClient.HasAgentOverride(agentName) {
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

	if rolloutWriter := a.createRolloutWriter(ctx, ca.DisplayName, task); rolloutWriter != nil {
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
	recordDelegationOutcome(ctx, agentResult)
	// 落一张便签。以前 custom agent 的产物只活在 Director 的上下文里，一次压缩就没了，
	// 与 Coding→Browser 是同一类黑洞。不返回 noteRef 也无妨：Board Index 每轮从磁盘重扫，
	// 下一次委派与 Director 自己的上下文都会自动看到这条。
	a.writeDelegateNote(ca.Name, "delegate_"+ca.Name, task, agentResult, nil)
	return result.Text, nil
}

func (a *DirectorAgent) injectSubAgentMemory(result AgentResult, toolCallID string, toolName string) {
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
		// Append 而不是裸 append 到字段：锁在类型内部，且这条摘要现在也会参与
		// MaxSize 溢出截断与配对修复（以前绕过它，摘要攒够了内存就无上限增长）。
		// 也不用 AddMessage —— 它按 Type 分派重建消息，会丢掉 GroupID/ParentID/IsSubAgent。
		a.currentMemory.Append(summaryMsg)
	}

}

// SetBoardScope 绑定本次任务的身份与便签板作用域。
//
// boardID 优先取 sessionID：同一会话（TUI 的一个 tab / HTTP 的一个 chat session）里开新任务
// 应该接着看同一块板，否则前一次委派写下的结论对新任务完全不可见，只能重做一遍。
// taskID 仍单独保留 —— rollout 文件名与 tasks/{id}.jsonl 都以它为键，不能跟着换。
// 两者都为空时不能交给 SanitizeBoardID：它会给空串编一个 adhoc id，每轮都是新板。
func (a *DirectorAgent) SetBoardScope(sessionID, taskID string) {
	a.taskID = taskID
	switch {
	case sessionID != "":
		a.boardID = blackboard.SanitizeBoardID(sessionID)
	case taskID != "":
		a.boardID = blackboard.SanitizeBoardID(taskID)
	}
}

func (a *DirectorAgent) SetResumedTask(resumed bool) {
	a.resumedTask = resumed
}

// SetPendingResumptionOpt 允许传 nil：每一轮都必须显式设置，
// "这次不带落点"和"没设置过"必须是同一件事，否则上一轮的落点会漏进这一轮。
func (a *DirectorAgent) SetPendingResumptionOpt(stop *DelegationStop) {
	a.pendingResumption = stop
}

// ctx 提供父子坐标：delegation_id 由 directorToolRunner 在 delegate_* 分支注入、parent_session_id 由 Director 在自己的 writer 建好之后注入，两者都在这里一次性写进 writer，两个 SessionMeta 写入点（executor / planner）都不必各自记得去取 —— 漏一个的表现是那份文件没有坐标，恢复侧只能把它判成"不可原样重放"。
func (a *DirectorAgent) createRolloutWriter(ctx context.Context, agentName, task string) *memory.RolloutWriter {
	projectID := a.computeProjectID()

	writer, err := memory.NewRolloutWriter(agentName, a.taskID, projectID)
	if err != nil {
		slog.Warn("Rollout: failed to create writer, continuing without rollout logging",
			"agent", agentName,
			"error", err,
		)
		return nil
	}
	// 把便签板一起记进 rollout：/resume 靠它找回这块板。task_id 已经不够用了 ——
	// board 现在是 session 级的，一个 board 会横跨多个 task。
	writer.SetBoardID(a.boardID)
	writer.SetDelegation(memory.DelegationIDFrom(ctx), memory.RolloutSessionIDFrom(ctx))

	slog.Debug("Rollout: writer created for delegate agent",
		"agent", agentName,
		"file", writer.FilePath(),
	)

	return writer
}

func (a *DirectorAgent) computeProjectID() string {
	return director.ComputeProjectID(a.env.ProjectPath())
}

// delegationOutcome 是一次委派的**逐调用**结果载体。
//
// 它取代 Director 上的一个全局单槽（pendingSubAgentMemory）。单槽在并发委派下必然串味：
// A 先完成 → set(A)，B 后完成 → set(B) 覆盖，父线程给 A 提交时 take 到的是 B，
// 还以 A 的 callID 写进记忆 —— 摘要挂错委派，且不报错。
//
// 现在的形状：runner 在调用前把载体挂进 ctx，delegateTo 在自己的 goroutine 里写，
// ad.Call 返回后由同一个 runner 线程读 —— happens-before 由"调用返回"本身保证，
// 既不需要锁，也不留下任何跨调用、跨委派的全局残留。
type delegationOutcome struct {
	result AgentResult
	has    bool
}

type delegationOutcomeKey struct{}

func withDelegationOutcome(ctx context.Context) (context.Context, *delegationOutcome) {
	o := &delegationOutcome{}
	return context.WithValue(ctx, delegationOutcomeKey{}, o), o
}

// ctx 上没有载体（非委派路径、或旧调用点）时静默 no-op —— 记忆注入只是锦上添花，
// 不该因为载体缺失就让一次成功的委派失败。
func recordDelegationOutcome(ctx context.Context, result AgentResult) {
	o, ok := ctx.Value(delegationOutcomeKey{}).(*delegationOutcome)
	if !ok || o == nil {
		return
	}
	o.result, o.has = result, true
}

func (a *DirectorAgent) applyEnhancedCommander(
	agentType string,
	task string,
	result AgentResult,
	err error,
	noteRef string,
	unresolvedCount int,
) (string, error) {
	if err != nil {
		return "", err
	}

	return FormatForDirector(agentType, result, noteRef, unresolvedCount), nil
}

func blackboardEnabled() bool {
	return blackboard.Enabled()
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
	// 闸门 abandon 掉的那次委派**什么都不写**。它没有交付物，落上去就是一张空正文的便签，
	// 却会占住每轮重扫的索引位、并在下一次委派的任务前缀里冒充一条结论；而"恢复后接着做完"
	// 才是它的下一步，届时 writeResumedNote 落的是真交付（ok）或降级报告（degraded）。
	var abandoned *pause.AbandonedError
	if errors.As(runErr, &abandoned) {
		return ""
	}
	status := blackboard.StatusOK
	if runErr != nil {
		status = blackboard.StatusError
	}
	var artifactIDs []string
	if result.ArtifactRef != nil {
		artifactIDs = []string{result.ArtifactRef.ID}
	}
	// 正文恒为交付全文。以前在超预算路径上写的是 result.Summary
	// （= artifact.Reduce 的前缀，实测只保留原文 29%），便签就成了缩略图的缩略图。
	body := result.Deliverable
	if body == "" {
		body = result.Text
	}
	if result.BlueprintAccount != "" {
		// 蓝图账目挂在尾部、不进结论：它是"写通道这次成了没有"，不是交付内容，
		// 混进 conclusion 会让每轮重扫的索引行冒充一句架构判断。
		body = strings.TrimRight(body, "\n") + "\n\n> [blueprint] " + result.BlueprintAccount + "\n"
	}
	handoff := result.Handoff
	if handoff == nil {
		handoff = &blackboard.Handoff{Conclusion: result.Summary, Status: status}
	} else if handoff.Status == "" {
		// LLM 没自报状态时由委派结果判定：说 ok 但实际抛错的情况不能信它的。
		copied := *handoff
		handoff = &copied
		handoff.Status = status
	}
	noteRef, err := blackboard.WriteNote(blackboard.NoteInput{
		BoardID:     a.boardID,
		Agent:       agentName,
		Tool:        toolName,
		Task:        task,
		Summary:     handoff.Conclusion,
		Body:        body,
		ArtifactIDs: artifactIDs,
		Status:      status,
		Handoff:     *handoff,
	})
	if err != nil {
		slog.Warn("blackboard: failed to write note", "agent", agentName, "error", err)
		return ""
	}
	slog.Debug("blackboard: note written", "agent", agentName, "noteRef", noteRef, "boardID", a.boardID,
		"body_chars", utf8.RuneCountInString(body), "degraded", handoff.Degraded)
	return noteRef
}

// BoardID 返回当前便签板。便签板被 YAGENT_BLACKBOARD=0 关掉时返回空串 ——
// 开关的判定留在本包，调用方（app 的启动清理）不必认识这个环境变量。
func (a *DirectorAgent) BoardID() string {
	if !blackboardEnabled() {
		return ""
	}
	return a.boardID
}

// YAGENT_BLACKBOARD=0 时必须传空串：那条路径上既不写便签，也就不该注入索引。
func (a *DirectorAgent) boardIDForPlanner() string {
	return a.BoardID()
}

// 换算（index_line_chars = 0 → 不裁剪）在 blackboard 自己的构造函数里完成，
// 配置层与特性层不必互相认识。
func (a *DirectorAgent) indexOpts() blackboard.IndexOptions {
	c := config.GlobalBlackboard()
	return blackboard.IndexOptionsFromConfig(c.IndexMaxNotes, c.IndexLineChars)
}

func (a *DirectorAgent) resolveOpts() blackboard.ResolveOptions {
	c := config.GlobalBlackboard()
	return blackboard.ResolveOptions{
		SummaryRunes: c.SummaryMaxRunes,
	}
}

// WriteDelegateNote 实现 BoardWriter：把一次委派的产物写成便签。
func (a *DirectorAgent) WriteDelegateNote(agentName, toolName, task string, result AgentResult, runErr error) string {
	return a.writeDelegateNote(agentName, toolName, task, result, runErr)
}

// PackDelegateTask 实现 BoardWriter：给子任务前置便签索引与显式 refs 的内容。
func (a *DirectorAgent) PackDelegateTask(task string, params map[string]interface{}) (string, int) {
	return a.packDelegateTask(task, params)
}

// packDelegateTask 组装子 Agent 真正收到的任务文本：**索引 → 显式 refs 的 ContextPack → 任务**。
//
// 索引每轮从磁盘重扫，不来自会话历史，因此两件事同时成立：
//   - 上下文压缩（第二/三档会丢掉 tool 消息里的 note:/artifact 引用）动不到它；
//   - 子 Agent 能否发现前序结论，不再取决于 Director 是否记得传 context_refs。
//     context_refs 从此语义是"强调"（额外内联全文），而不是"准入"。
func (a *DirectorAgent) packDelegateTask(task string, params map[string]interface{}) (packedTask string, unresolvedCount int) {
	// [仓库蓝图] 先取"目标模块的现有散文"。它必须在黑图开关**之外**：
	// YAGENT_BLACKBOARD=0 关掉的是便签板，不是蓝图 —— 两个 kill-switch 各管各的存储。
	// 这一句是 R4 的承重件：Repo 拿到的不是空白页而是"这片已经被说成什么了"，
	// 它的提交才是**更新**而不是重新发明。
	bp := a.blueprint.ProseForTask(task)
	// [局部文档] 任务点到的目录的 AGENTS.md（根文件只装跨模块判据，包内约束住在盘上各份）。
	// 与蓝图同一侧、同样在黑图开关之外：关掉便签板不该顺带关掉"这个包有哪些硬约束"。
	md := ""
	if a.env != nil {
		md = LocalDocsForTask(a.env.ProjectPath(), task)
	}

	if !blackboardEnabled() {
		return bp + md + task, 0
	}

	idx, _, err := blackboard.BuildIndex(a.boardID, a.indexOpts())
	if err != nil {
		slog.Debug("blackboard: index build failed", "board", a.boardID, "error", err)
	}

	refs := parseContextRefs(params["context_refs"])
	var packText string
	if len(refs) > 0 {
		pack := blackboard.ResolveRefs(a.boardID, refs, a.artifactInfo, a.resolveOpts())
		unresolvedCount = len(pack.Unresolved)
		packText = pack.Text()
		if packText == "" && unresolvedCount == 0 {
			// refs 全被解析但什么都没渲染出来，说明 board 里确实没有——值得留一行日志。
			slog.Debug("blackboard: refs resolved to an empty pack", "board", a.boardID, "refs", len(refs))
		}
	}

	if idx == "" && packText == "" {
		return bp + md + task, unresolvedCount
	}

	var b strings.Builder
	b.WriteString(bp)
	b.WriteString(md)
	if idx != "" {
		b.WriteString(idx)
		b.WriteString("\n\n")
	}
	if packText != "" {
		b.WriteString(packText)
		b.WriteString("\n---\n")
	}
	b.WriteString(task)

	slog.Debug("blackboard: delegate task packed", "board", a.boardID,
		"index", idx != "", "refs", len(refs), "unresolved", unresolvedCount,
		"chars", utf8.RuneCountInString(b.String()))
	return b.String(), unresolvedCount
}

// artifactInfo 回查 artifact 的摘要与体积。blackboard 不 import artifact，
// 所以这里以回调形式提供（今天的 ArtifactExistsFn 只能回答"在不在"，
// 于是 ContextPack 打印的是 (0 chars)，子 Agent 只能盲调 read_artifact）。
func (a *DirectorAgent) artifactInfo(id string) (string, int, bool) {
	art, err := artifact.DefaultStore().Load(a.computeProjectID(), id)
	if err != nil {
		return "", 0, false
	}
	return art.Summary, art.CharCount, true
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
	// delegateSem 是**这一轮 Run** 的委派并发池：一次回复里三个 delegate_* 时，
	// 同时只跑 N 个子 Agent。池子挂在 runner 上而不是 DirectorAgent 上，
	// 于是嵌套委派（Repo→delegate_devops）用的是自己那一轮的池 ——
	// "父占一格、子在同一格里排队"这种死锁形状从结构上就不存在。
	delegateSem chan struct{}
}

// newDirectorToolRunner 是唯一的构造方式：漏了池子等于漏了 max_parallel_delegations。
func newDirectorToolRunner(a *DirectorAgent, state *director.RunState) *directorToolRunner {
	return &directorToolRunner{
		agent:       a,
		state:       state,
		delegateSem: newDelegatePool(NormalizeMaxParallelDelegations(a.maxParallelDelegations)),
	}
}

func (r *directorToolRunner) Specs() []llm.ToolDef {
	// 每次调用都重新向 Surface 取：deferred 的取回（load_tool）与 Meta 运行期注册的
	// Custom Agent 都要在下一步就对模型可见。过去这里返回构造期快照、planner 每轮再
	// 冻结一次，结果新注册的委派工具本回合只有代码能调到。
	return r.agent.surfaceOrDefault().Defs()
}

func (r *directorToolRunner) Call(ctx context.Context, name string, argsJSON string, callID string) (string, error) {
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
	// 委派并发上限（[agent] max_parallel_delegations）。取格子放在**建这轮 ctx 链之前**：
	// 排队不是子 Agent 的活动，不该算进 idle/total 看门狗，也就不需要在这条早退路上
	// 补一次 cancel。取不到格子只可能因为 ctx 已经结束 —— 交回 error，planner 会把它
	// 写成一条 tool 结果，配对不变量照守。
	if isDelegate {
		release, ok := acquireDelegateSlot(ctx, r.delegateSem)
		if !ok {
			return "", ctx.Err()
		}
		defer release()
	}
	cancelCtx, cancelCtxCancel := context.WithCancel(ctx)
	// 载体挂在 cancelCtx 上：它顺着 toolCtx 流进 delegateTo，
	// 而下面读它的是同一个 goroutine（ad.Call 已经返回）。
	cancelCtx, outcome := withDelegationOutcome(cancelCtx)
	toolCtx := cancelCtx
	toolCancel := cancelCtxCancel
	switch {
	case isInteractiveUserTool(name):
	case isDelegate:
		// 委派坐标在这一层注入：它是唯一一个既知道 tool_call_id、又在子 Agent 的
		// ctx 之下游还能被 createRolloutWriter 读到的位置。
		cancelCtx = memory.WithDelegationID(cancelCtx, callID)
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

	// 逐调用的载体，不是全局槽：并行批次里每次委派各拿各的结果，
	// 注入用的是**这一次**的 callID（ParentID 从此能回答"这条摘要来自哪一次委派"）。
	if outcome != nil && outcome.has {
		a.injectSubAgentMemory(outcome.result, callID, name)
		r.state.SetPendingSubAgentMemory(toSubAgentMemory(&outcome.result))
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
	// 一次性取用：下一轮不该再把同一份重放结果提交进 memory。
	pending := a.pendingResumption
	a.pendingResumption = nil

	// boardID 是按会话写入的（SetBoardScope），而 read_note/read_artifact/list_notes 的可用性取决于它，
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

		// [仓库蓝图] 常驻骨架（形状②）。每轮现算、只进这次调用的 system prompt（I2）：
		// 它必须扛住第二/三档整段重写消息，而进 messages 的东西会被摘要吞掉（§11 坑 11）。
		systemPrompt += a.blueprint.Resident()
		// 技能目录与 MCP 状态账也是稳定前缀：技能集与 server 拓扑在一次进程内基本不变。
		systemPrompt += a.skillCatalog()
		systemPrompt += a.mcpStatusLine()
		return systemPrompt, nil
	}

	var directorRolloutWriter *memory.RolloutWriter
	if rw := a.createRolloutWriter(ctx, "director", input); rw != nil {
		directorRolloutWriter = rw
		// 把自己的 session_id 挂到 ctx 上：被委派的子 Agent 会把它记成
		// ParentSessionID。没有它，父子只有单向指针 —— 从子文件能找到父 call，
		// 从父 call 却不知道该读哪份子文件（同一角色在一个会话里可能被委派多次）。
		ctx = memory.WithRolloutSessionID(ctx, rw.SessionID())
		defer func() {
			if directorRolloutWriter != nil {
				directorRolloutWriter.Close()
			}
		}()
	}

	toolRunner := newDirectorToolRunner(a, state)

	var recovery *director.RecoveryHandler
	var metrics *director.MetricsCollector
	if a.adapter != nil {
		recovery = a.adapter.recovery
		metrics = a.adapter.metrics
	}

	cfg := director.PlannerConfig{
		LLM:                a.LLM,
		Publisher:          a.Publisher,
		Journal:            a.thinkLink,
		Rollout:            directorRolloutWriter,
		Tools:              toolRunner,
		Prompts:            prompts,
		Compressor:         a.compressor,
		MaxSteps:           a.maxSteps,
		LLMTimeout:         a.currentLLMTimeout(),
		Recovery:           recovery,
		Metrics:            metrics,
		NormalizeMessages:  validateAndRepairToolCallPairs,
		EstimateTokensFn:   tokenutil.EstimateTokens,
		ConvertToolCallsFn: convertToolCalls,
		Compression:        compressionOptionsFromConfig(a.EnhancedCommanderCfg),
		ToolTimeout:        120 * time.Second,

		MaxParallelReadOnlyTools: a.maxParallelReadOnlyTools,
		IsParallelizableTool:     a.isToolParallelizable,
		BoardID:                  a.boardIDForPlanner(),
	}

	planner := director.NewPlanner(cfg, state)

	// 恢复的执行阶段放在门面层而不是 planner 里：委派机器（delegateTo、子 Agent 的
	// rollout writer、ResumeState 的接缝）都在这层，planner 只拿到成品文本。
	// 这一步不调 LLM，所以恢复本身确定且零 token 成本（§6.3）。
	var resumption *director.Resumption
	if pending != nil && config.ResumeEnabled() {
		resultText, degraded := a.resumeDelegation(ctx, *pending)
		resumption = &director.Resumption{
			DelegationID: pending.DelegationID,
			ToolName:     pending.ToolName,
			AgentName:    pending.AgentName,
			RolloutPath:  pending.ChildRolloutPath,
			Result:       resultText,
			Degraded:     degraded,
		}
	}

	result, err := planner.Run(ctx, director.PlanInput{
		Input:             input,
		Mem:               mem,
		TaskID:            a.taskID,
		Resumed:           resumedTask,
		PendingResumption: resumption,
	})
	a.carryOverMaterialized()
	return result.Text, err
}

func (a *DirectorAgent) resolveTool(name string) (*tools.Adapter, tools.ToolState) {
	if a.surface == nil {
		return nil, tools.StateUnknown
	}
	return a.surface.Lookup(name)
}

// isToolParallelizable 与子 Agent 侧共用同一份判据（toolCallIsParallelizable）。
// 分成两份实现的历史代价就是设计稿 R1：一边扫对了、一边扫错了也没人发现。
func (a *DirectorAgent) isToolParallelizable(name string) bool {
	return toolCallIsParallelizable(a.surface, name)
}

func (a *DirectorAgent) applyEmergencyCompression(ctx context.Context, messages []llm.Message, threshold int) ([]llm.Message, *compression.EmergencyCompressionStats) {
	return a.compressor.ApplyEmergency(ctx, messages, threshold, a.currentMemory)
}

type UltimateCompressionStats = compression.UltimateCompressionStats

func (a *DirectorAgent) applyUltimateCompression(messages []llm.Message, threshold int) ([]llm.Message, *UltimateCompressionStats) {
	// 保留数量也只从 compressionOptionsFromConfig 取：这里曾是执行层最后一处直接读
	// [enhanced_commander] 字段的地方（绕过单一转换点的第二份真相）。
	return a.compressor.ApplyUltimate(messages, threshold, a.currentMemory,
		compressionOptionsFromConfig(a.EnhancedCommanderCfg).UltimateKeepPlans)
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
