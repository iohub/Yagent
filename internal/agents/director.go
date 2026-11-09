package agents

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
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
	"yagent/internal/plasma"
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

	surface *tools.Surface

	materializedAcrossTurns map[string]bool
	disabledAgents          map[string]bool
	maxSteps                int
	metaRetryCount          int
	metaHandler             *director.MetaAgentHandler
	adapter                 *DirectorAdapter
	llmClient               *llm.Client

	// plasmaGen 是生成式 Agent 的**唯一**生成入口（ADR-14/15）。nil = 不可用：
	// delegate_meta 不进工具面，原因进 Surface.Skipped。Director 不认识 spawner/
	// workdir/--fix —— 它只知道"给我一份编译通过的 bundle，或一句可读的失败原因"。
	plasmaGen plasmaGenerator
	// plasmaWhy 是装配期判出的降级原因（nil 生成器时给模型与日志看）。
	plasmaWhy string
	// plasmaStore 是产物仓（nil ⇒ config.PlasmaStoreDir()）。测试必须能指到临时目录，
	// 否则一条单测会把 bundle 写进用户的 home。
	plasmaStore *plasma.Store
	// plasmaRunner 是 5a 结构夹具的执行器（nil ⇒ 零值 PlasmaFixtureRunner）。
	plasmaRunner plasma.FixtureRunner
	// execConfigs 是 LinkPlan.Config 的存放处（key = CustomAgent.Name）。
	// 为什么不在 CustomAgent 上加字段：那是 director 子包的纯数据类型，加一个
	// plasma 的 view 会让"编排内核"认识装配参数；执行参数归门面层。
	execConfigs map[string]plasma.ExecutorConfigView
	execMu      sync.RWMutex

	blueprint *BlueprintView

	external External

	currentMemory *memory.ConversationMemory
	compressor    *compression.ContextCompressor

	llmTimeout time.Duration

	delegateIdleTimeout  time.Duration
	delegateTotalTimeout time.Duration

	// onDelegationMetrics 是遥测接缝（ADR-6，P4 提供机制，P9 由 app 接线）。nil = 关闭。
	// 写入点是 directorToolRunner.Call 的 outcome 汇聚处 —— 三条委派分支唯一的共同出口。
	onDelegationMetrics func(DelegationMetrics)

	maxParallelReadOnlyTools int

	maxParallelDelegations int

	EnhancedCommanderCfg config.EnhancedCommanderConfig
	thinkLink            *thinklink.Store
	taskID               string
	resumedTask          bool

	pendingResumption *DelegationStop
	boardID           string
}

func (a *DirectorAgent) skillCatalog() string {
	if s := skillCatalogFor("director", a.external.Skills); s != "" {
		return "\n\n" + s
	}
	return ""
}

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
	DevOpsAgent  *DevOpsAgent
	BrowserAgent *BrowserAgent

	// PlasmaGenerator 是生成器（nil = 不可用，走降级）。类型窄到一次 Generate：
	// 装配层传 *PlasmaTrampoline，测试传假实现 —— 都不必起子进程。
	PlasmaGenerator plasmaGenerator
	// PlasmaUnavailable 是装配期判出的降级原因（nil 生成器时进 Skipped 与错误文案）。
	PlasmaUnavailable string
	// PlasmaStore 是产物仓（nil ⇒ config.PlasmaStoreDir()）。
	PlasmaStore *plasma.Store
	// PlasmaFixtureRunner 是 5a 执行器（nil ⇒ 零值 PlasmaFixtureRunner，与 CLI 同形）。
	PlasmaFixtureRunner plasma.FixtureRunner

	MaxSteps       int
	DisabledAgents map[string]bool
	MetaRetryCount int
	Config         config.Config
	LLMClient      *llm.Client

	Blueprint *BlueprintView

	External External
}

func NewDirectorAgent(p DirectorAgentParams) *DirectorAgent {
	adapterCfg := director.DefaultRecoveryConfig()
	adapterCfg.MaxRetries = p.MaxSteps
	adapterCfg.CircuitBreakerThreshold = p.Config.LLM.CircuitBreakerThreshold
	adapterCfg.CircuitBreakerResetTimeout = p.Config.LLM.CircuitBreakerResetTimeout
	adapterCfg.LLMRetries = p.Config.LLM.StepRetries

	self := &DirectorAgent{
		BaseAgent:      BaseAgent{LLM: p.LLM, Publisher: p.Publisher},
		RepoAgent:      p.RepoAgent,
		CodingAgent:    p.CodingAgent,
		ChatAgent:      p.ChatAgent,
		DevOpsAgent:    p.DevOpsAgent,
		BrowserAgent:   p.BrowserAgent,
		plasmaGen:      p.PlasmaGenerator,
		plasmaWhy:      p.PlasmaUnavailable,
		plasmaStore:    p.PlasmaStore,
		plasmaRunner:   p.PlasmaFixtureRunner,
		execConfigs:    make(map[string]plasma.ExecutorConfigView),
		promptFmt:      p.Formatter,
		env:            p.Env,
		files:          p.Files,
		search:         p.Search,
		sys:            p.Sys,
		edit:           p.Edit,
		flow:           p.Flow,
		thinker:        p.Thinker,
		microAgent:     p.MicroAgent,
		deepThinker:    p.DeepThinker,
		guard:          p.Guard,
		repoCtx:        p.RepoCtx,
		maxSteps:       p.MaxSteps,
		metaRetryCount: p.MetaRetryCount,
		metaHandler:    director.NewMetaAgentHandler(),
		adapter:        NewDirectorAdapter(true, adapterCfg),
		llmClient:      p.LLMClient,
		blueprint:      p.Blueprint,
		external:       p.External,

		llmTimeout: func() time.Duration {
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

func (a *DirectorAgent) routeCtx(ctx context.Context, agentName, task string) context.Context {
	if a.llmClient == nil {
		return ctx
	}
	r := a.llmClient.GetRouter()
	if !r.Enabled() {
		return ctx
	}

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
	cfg.Publisher = a.Publisher
	cfg.AgentName = ca.DisplayName
	// 执行参数来自 bundle.runtime（LinkPlan.Config），不再硬编码：写死 MaxSteps=15 会让
	// step-budget 夹具永远绿，也让"plasma 决定运行时"变成空话。
	//
	// 没有计划（老路径直接 registerCustomAgent 的 Agent）时**整套回落**历史值，而不是把
	// 零值 view 套上去：view.StopOnFinish 的零值是 false，套上去的后果是 agent_exit 不再
	// 终止循环（用例：TestCustomAgentDelegateTool_FinishTerminates 从 1 次 LLM 调用变 2 次）。
	planCfg, fromPlan := a.executorConfigFor(ca.Name)
	if fromPlan {
		applyExecutorConfig(&cfg, planCfg, a.currentLLMTimeout())
	} else {
		slog.Warn("custom agent 没有 plasma 装配计划，回落历史执行参数",
			"agent", ca.Name, "max_steps", defaultCustomAgentMaxSteps, "stop_on_finish", true)
		cfg.MaxSteps = defaultCustomAgentMaxSteps
		cfg.StopOnFinish = true
		cfg.LLMTimeout = a.currentLLMTimeout()
	}

	if rolloutWriter := a.createRolloutWriter(ctx, ca.DisplayName, task); rolloutWriter != nil {
		defer rolloutWriter.Close()
		ctx = memory.WithRolloutWriter(ctx, rolloutWriter)
	}

	result, err := RunAgentLoop(ctx, cfg)
	if err != nil {
		return "", err
	}

	agentResult := FinalizeResultFull(ca.DisplayName, task, result)
	recordDelegationOutcome(ctx, agentResult)

	a.writeDelegateNote(ca.Name, "delegate_"+ca.Name, task, agentResult, nil)
	return result.Text, nil
}

// applyExecutorConfig 把 plasma 的运行时视图套到 ExecutorConfig 上。
//
// 三条守卫，缺一条都会让"配置读到了但没生效"变成静默缺陷：
//   - MaxSteps ≤0 → 兜底 15：0 会让循环立刻停下（生成侧写了个空字段就等于 Agent 不动）；
//   - LLMTimeout ≤0 → 用调用方给的全局超时：0 会被当作"不设超时"，与 [llm] timeout 脱钩；
//   - found=false（老路径的 CustomAgent）→ 整套值保持历史硬编码并 warn，
//     **不许**静默用零值 —— 那正是上面两条要挡的东西。
func applyExecutorConfig(cfg *ExecutorConfig, view plasma.ExecutorConfigView, fallbackTimeout time.Duration) {
	if view.MaxSteps <= 0 {
		view.MaxSteps = defaultCustomAgentMaxSteps
	}
	if view.LLMTimeout <= 0 {
		view.LLMTimeout = fallbackTimeout
	}
	cfg.MaxSteps = view.MaxSteps
	cfg.StopOnFinish = view.StopOnFinish
	cfg.LLMTimeout = view.LLMTimeout
	if view.MaxParallelReadOnlyTools > 0 {
		cfg.MaxParallelReadOnlyTools = NormalizeMaxParallelReadOnlyTools(view.MaxParallelReadOnlyTools)
	}
	// 只覆盖 bundle 声明的那两个旋钮：compression.Options 的其余字段（Threshold/
	// UltimateEnabled…）来自 DefaultExecutorConfig，整份替换会把它们清零。
	cfg.Compression.Enabled = view.CompressionEnable
	if view.KeepToolResultTokens > 0 {
		cfg.Compression.KeepTokens = view.KeepToolResultTokens
	}
}

// defaultCustomAgentMaxSteps 是"没有 plan"时的兜底步数，与历史硬编码同值。
// 它现在只服务老路径（P9a 之前的 CustomAgent），不是可调参数。
const defaultCustomAgentMaxSteps = 15

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

		a.currentMemory.Append(summaryMsg)
	}

}

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

func (a *DirectorAgent) SetPendingResumptionOpt(stop *DelegationStop) {
	a.pendingResumption = stop
}

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

type delegationOutcome struct {
	result AgentResult
	has    bool
}

type delegationOutcomeKey struct{}

func withDelegationOutcome(ctx context.Context) (context.Context, *delegationOutcome) {
	o := &delegationOutcome{}
	return context.WithValue(ctx, delegationOutcomeKey{}, o), o
}

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

	body := result.Deliverable
	if body == "" {
		body = result.Text
	}
	if result.BlueprintAccount != "" {
		body = strings.TrimRight(body, "\n") + "\n\n> [blueprint] " + result.BlueprintAccount + "\n"
	}
	handoff := result.Handoff
	if handoff == nil {
		handoff = &blackboard.Handoff{Conclusion: result.Summary, Status: status}
	} else if handoff.Status == "" {
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

func (a *DirectorAgent) BoardID() string {
	if !blackboardEnabled() {
		return ""
	}
	return a.boardID
}

func (a *DirectorAgent) boardIDForPlanner() string {
	return a.BoardID()
}

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

func (a *DirectorAgent) WriteDelegateNote(agentName, toolName, task string, result AgentResult, runErr error) string {
	return a.writeDelegateNote(agentName, toolName, task, result, runErr)
}

func (a *DirectorAgent) PackDelegateTask(task string, params map[string]interface{}, targetAgent string) (string, int) {
	return a.packDelegateTask(task, params, targetAgent)
}

func (a *DirectorAgent) packDelegateTask(task string, params map[string]interface{}, targetAgent string) (packedTask string, unresolvedCount int) {
	bp := a.blueprint.ProseForTask(task)

	md := ""
	// 局部文档的唯一注入判据是 targetAgent（moduleDocsExcludedTarget 在 module_docs.go，只此一处调用）。
	if a.env != nil && !moduleDocsExcludedTarget(targetAgent) {
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

var directorToolTimeout = 120 * time.Second

type directorToolRunner struct {
	agent *DirectorAgent
	state *director.RunState

	delegateSem chan struct{}
}

func newDirectorToolRunner(a *DirectorAgent, state *director.RunState) *directorToolRunner {
	return &directorToolRunner{
		agent:       a,
		state:       state,
		delegateSem: newDelegatePool(NormalizeMaxParallelDelegations(a.maxParallelDelegations)),
	}
}

func (r *directorToolRunner) Specs() []llm.ToolDef {
	return r.agent.surfaceOrDefault().Defs()
}

func (r *directorToolRunner) Call(ctx context.Context, name string, argsJSON string, callID string) (string, error) {
	a := r.agent

	ad, state := a.resolveTool(name)
	if state == tools.StateUnknown || ad == nil {
		return fmt.Sprintf("Tool %s not found", name), nil
	}

	if state == tools.StateColdKnown {
		schema, _ := a.surfaceOrDefault().Materialize([]string{name})
		return fmt.Sprintf("Error: schema for %s was not loaded. Full schema:\n%s\nRetry the call now with these parameters.", name, schema), nil
	}

	if strings.HasPrefix(ad.Name(), "delegate_") {
		agentName := strings.TrimPrefix(ad.Name(), "delegate_")
		LogDelegateCall(ad.Name(), agentName, argsJSON)
	}

	toolTimeout := directorToolTimeout
	isDelegate := strings.HasPrefix(name, "delegate_")

	if isDelegate {
		release, ok := acquireDelegateSlot(ctx, r.delegateSem)
		if !ok {
			return "", ctx.Err()
		}
		defer release()
	}
	cancelCtx, cancelCtxCancel := context.WithCancel(ctx)

	cancelCtx, outcome := withDelegationOutcome(cancelCtx)
	toolCtx := cancelCtx
	toolCancel := cancelCtxCancel
	switch {
	case isInteractiveUserTool(name):
	case isDelegate:

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

	if outcome != nil && outcome.has {
		a.injectSubAgentMemory(outcome.result, callID, name)
		r.state.SetPendingSubAgentMemory(toSubAgentMemory(&outcome.result))
		a.emitDelegationMetrics(name, callID, err, outcome.result)
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

	pending := a.pendingResumption
	a.pendingResumption = nil

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
		systemPrompt += a.customAgentPromptSection()

		systemPrompt += a.blueprint.Resident()

		systemPrompt += a.skillCatalog()
		systemPrompt += a.mcpStatusLine()
		return systemPrompt, nil
	}

	var directorRolloutWriter *memory.RolloutWriter
	if rw := a.createRolloutWriter(ctx, "director", input); rw != nil {
		directorRolloutWriter = rw

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

func (a *DirectorAgent) isToolParallelizable(name string) bool {
	return toolCallIsParallelizable(a.surface, name)
}

func (a *DirectorAgent) applyEmergencyCompression(ctx context.Context, messages []llm.Message, threshold int) ([]llm.Message, *compression.EmergencyCompressionStats) {
	return a.compressor.ApplyEmergency(ctx, messages, threshold, a.currentMemory)
}

type UltimateCompressionStats = compression.UltimateCompressionStats

func (a *DirectorAgent) applyUltimateCompression(messages []llm.Message, threshold int) ([]llm.Message, *UltimateCompressionStats) {
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
