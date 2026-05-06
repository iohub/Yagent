package app

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"

	"codeactor/internal/agents"
	"codeactor/internal/tools"
	"codeactor/internal/config"
	"codeactor/internal/globalctx"
	"codeactor/internal/llm"
	"codeactor/internal/memory"
	"codeactor/pkg/messaging"
)

// CodingAssistant is the main entry point for the agent system.
type CodingAssistant struct {
	engine               llm.Engine
	config               *config.Config
	conductor            *agents.ConductorAgent
	dispatcher           *messaging.MessageDispatcher
	mu                   sync.Mutex
	userResponseChannels map[string]chan string
	logger               *slog.Logger

	globalCtx      *globalctx.GlobalCtx
	DisabledAgents string // comma-separated list of agent names to disable (e.g. "repo,coding,chat")
	CodebasePort   int    // codebase 服务端口，由 main 函数动态分配
}

// NewCodingAssistant creates a new CodingAssistant.
func NewCodingAssistant(client *llm.Client) (*CodingAssistant, error) {
	ca := &CodingAssistant{
		userResponseChannels: make(map[string]chan string),
		logger:               slog.Default().With("component", "coding_assistant"),
		engine:               client.Engine,
		config:               client.Config,
	}
	return ca, nil
}

// Init initializes the assistant with Engine and creates agents.
func (ca *CodingAssistant) Init(engine llm.Engine, workDir string) {
	ca.engine = engine

	// Initialize agents
	publisher := messaging.NewMessagePublisher(ca.dispatcher)

	userConfirmMgr := tools.NewUserConfirmManager()

	gctx := globalctx.GlobalCtx{
		SpeakLang:   ca.config.Agent.SpeakLang,
		ProjectPath: workDir,
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		// Global utility
		Publisher:   publisher,
		CodebaseURL: fmt.Sprintf("http://127.0.0.1:%d", ca.CodebasePort),

		// Tools
		FileOps:      tools.NewFileOperationsTool(workDir),
		SearchOps:    tools.NewSearchOperationsTool(workDir),
		SysOps:       tools.NewSystemOperationsTool(workDir),
		ReplaceTool:  tools.NewReplaceBlockTool(workDir),
		ThinkingTool: tools.NewThinkingTool(),
		MicroAgentTool: tools.NewMicroAgentTool(engine),
		ImplPlanTool:   tools.NewImplPlanTool(),
		FlowOps:           tools.NewFlowControlTool(workDir),
		RepoOps:           tools.NewRepoOperationsTool(fmt.Sprintf("http://127.0.0.1:%d", ca.CodebasePort), workDir),
		UserConfirmMgr:    userConfirmMgr,
	}
	ca.globalCtx = &gctx

	// Wire up UserConfirmManager: register as consumer and set publisher
	userConfirmMgr.SetPublisher(publisher)
	gctx.FlowOps.UserConfirmMgr = userConfirmMgr

	// Create workspace guard for authorizing dangerous operations
	guard := tools.NewWorkspaceGuard(workDir, userConfirmMgr)
	gctx.Guard = guard
	if ca.dispatcher != nil {
		ca.dispatcher.RegisterConsumer(userConfirmMgr)
	}
	// Get max steps from config, default to 10 if not set
	repoMaxSteps := 20
	codingMaxSteps := 30
	chatMaxSteps := 10
	devopsMaxSteps := 15
	conductorMaxSteps := 20

	if ca.config != nil {
		if ca.config.Agent.RepoMaxSteps > 0 {
			repoMaxSteps = ca.config.Agent.RepoMaxSteps
		}
		if ca.config.Agent.CodingMaxSteps > 0 {
			codingMaxSteps = ca.config.Agent.CodingMaxSteps
		}
		if ca.config.Agent.ChatMaxSteps > 0 {
			chatMaxSteps = ca.config.Agent.ChatMaxSteps
		}
		if ca.config.Agent.DevOpsMaxSteps > 0 {
			devopsMaxSteps = ca.config.Agent.DevOpsMaxSteps
		}
		if ca.config.Agent.ConductorMaxSteps > 0 {
			conductorMaxSteps = ca.config.Agent.ConductorMaxSteps
		}
	}
	metaRetryCount := 5 // default
	if ca.config != nil && ca.config.Agent.MetaRetryCount > 0 {
		metaRetryCount = ca.config.Agent.MetaRetryCount
	}

	// Parse disabled agents from comma-separated string
	disabledAgents := parseDisabledAgents(ca.DisabledAgents)

	repoAgent := agents.NewRepoAgent(ca.globalCtx, engine, publisher, repoMaxSteps)
	codingAgent := agents.NewCodingAgent(ca.globalCtx, engine, codingMaxSteps)
	chatAgent := agents.NewChatAgent(ca.globalCtx, engine, chatMaxSteps)
	metaAgent := agents.NewMetaAgent(ca.globalCtx, engine)
	devopsAgent := agents.NewDevOpsAgent(ca.globalCtx, engine, devopsMaxSteps)
	ca.conductor = agents.NewConductorAgent(ca.globalCtx, engine, repoAgent, codingAgent, chatAgent, metaAgent, devopsAgent, conductorMaxSteps, disabledAgents, metaRetryCount)
}

func (ca *CodingAssistant) IntegrateMessaging(dispatcher *messaging.MessageDispatcher) {
	ca.dispatcher = dispatcher
}

// TaskRequest encapsulates the request context.
type TaskRequest struct {
	ctx         context.Context
	taskID      string
	projectDir  string
	taskDesc    string
	memory      *memory.ConversationMemory
	wsCallback  func(string, string)
	publisher   *messaging.MessagePublisher
	userMessage string
}

func NewTaskRequest(ctx context.Context, taskID string) *TaskRequest {
	return &TaskRequest{
		ctx:    ctx,
		taskID: taskID,
	}
}

func (r *TaskRequest) WithProjectDir(dir string) *TaskRequest {
	r.projectDir = dir
	return r
}

func (r *TaskRequest) WithTaskDesc(desc string) *TaskRequest {
	r.taskDesc = desc
	return r
}

func (r *TaskRequest) WithMemory(mem *memory.ConversationMemory) *TaskRequest {
	r.memory = mem
	return r
}

func (r *TaskRequest) WithWSCallback(cb func(string, string)) *TaskRequest {
	r.wsCallback = cb
	return r
}

func (r *TaskRequest) WithMessagePublisher(p *messaging.MessagePublisher) *TaskRequest {
	r.publisher = p
	return r
}

func (r *TaskRequest) WithUserMessage(msg string) *TaskRequest {
	r.userMessage = msg
	return r
}

// ProcessCodingTaskWithCallback executes the task using the agent system.
func (ca *CodingAssistant) ProcessCodingTaskWithCallback(req *TaskRequest) (string, error) {
	ca.Init(ca.engine, req.projectDir)

	return ca.conductor.Run(req.ctx, req.taskDesc, req.memory)
}

// ProcessConversation handles chat messages.
func (ca *CodingAssistant) ProcessConversation(req *TaskRequest) (string, error) {
	ca.Init(ca.engine, req.projectDir)

	return ca.conductor.Run(req.ctx, req.userMessage, req.memory)
}

// parseDisabledAgents converts a comma-separated string of agent names
// into a map[string]bool for O(1) lookup. Valid agent names: repo, coding, chat, meta, devops.
func parseDisabledAgents(s string) map[string]bool {
	result := make(map[string]bool)
	if s == "" {
		return result
	}
	for _, name := range strings.Split(s, ",") {
		name = strings.TrimSpace(name)
		if name != "" {
			result[name] = true
		}
	}
	return result
}
