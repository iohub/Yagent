package app

import (
	"context"
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

	"github.com/tmc/langchaingo/llms"
)

// CodingAssistant is the main entry point for the agent system.
type CodingAssistant struct {
	llm                  llms.LLM
	config               *config.Config
	conductor            *agents.ConductorAgent
	dispatcher           *messaging.MessageDispatcher
	mu                   sync.Mutex
	userResponseChannels map[string]chan string
	logger               *slog.Logger

	globalCtx      *globalctx.GlobalCtx
	DisabledAgents string // comma-separated list of agent names to disable (e.g. "repo,coding,chat")
}

// NewCodingAssistant creates a new CodingAssistant.
func NewCodingAssistant(client *llm.Client) (*CodingAssistant, error) {
	ca := &CodingAssistant{
		userResponseChannels: make(map[string]chan string),
		logger:               slog.Default().With("component", "coding_assistant"),
		llm:                  client.LLM,
		config:               client.Config,
	}
	return ca, nil
}

// Init initializes the assistant with LLM and creates agents.
func (ca *CodingAssistant) Init(llm llms.LLM, workDir string) {
	ca.llm = llm

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
		CodebaseURL: "http://127.0.0.1:12800",

		// Tools
		FileOps:      tools.NewFileOperationsTool(workDir),
		SearchOps:    tools.NewSearchOperationsTool(workDir),
		SysOps:       tools.NewSystemOperationsTool(workDir),
		ReplaceTool:  tools.NewReplaceBlockTool(workDir),
		ThinkingTool: tools.NewThinkingTool(),
		MicroAgentTool: tools.NewMicroAgentTool(llm),
		ImplPlanTool:   tools.NewImplPlanTool(),
		FlowOps:           tools.NewFlowControlTool(workDir),
		RepoOps:           tools.NewRepoOperationsTool("http://127.0.0.1:12800", workDir),
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

	repoAgent := agents.NewRepoAgent(ca.globalCtx, llm, publisher, repoMaxSteps)
	codingAgent := agents.NewCodingAgent(ca.globalCtx, llm, codingMaxSteps)
	chatAgent := agents.NewChatAgent(ca.globalCtx, llm, chatMaxSteps)
	metaAgent := agents.NewMetaAgent(ca.globalCtx, llm)
	ca.conductor = agents.NewConductorAgent(ca.globalCtx, llm, repoAgent, codingAgent, chatAgent, metaAgent, conductorMaxSteps, disabledAgents, metaRetryCount)
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
	ca.Init(ca.llm, req.projectDir)

	return ca.conductor.Run(req.ctx, req.taskDesc, req.memory)
}

// ProcessConversation handles chat messages.
func (ca *CodingAssistant) ProcessConversation(req *TaskRequest) (string, error) {
	ca.Init(ca.llm, req.projectDir)

	return ca.conductor.Run(req.ctx, req.userMessage, req.memory)
}

// parseDisabledAgents converts a comma-separated string of agent names
// into a map[string]bool for O(1) lookup. Valid agent names: repo, coding, chat, meta.
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
