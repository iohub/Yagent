package assistant

import (
	"context"
	"log/slog"
	"runtime"
	"sync"

	"codeactor/internal/assistant/agents"
	"codeactor/internal/assistant/tools"
	"codeactor/internal/config"
	"codeactor/internal/globalctx"
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

	globalCtx *globalctx.GlobalCtx
}

// NewCodingAssistant creates a new CodingAssistant.
func NewCodingAssistant(client *Client) (*CodingAssistant, error) {
	ca := &CodingAssistant{
		userResponseChannels: make(map[string]chan string),
		logger:               slog.Default().With("component", "coding_assistant"),
		llm:                  client.llm,
		config:               client.config,
	}
	client.assistant = ca
	return ca, nil
}

// Init initializes the assistant with LLM and creates agents.
func (ca *CodingAssistant) Init(llm llms.LLM, workDir string) {
	ca.llm = llm

	// Initialize agents
	publisher := messaging.NewMessagePublisher(ca.dispatcher)

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
		FlowOps:      tools.NewFlowControlTool(workDir),
		RepoOps:      tools.NewRepoOperationsTool("http://127.0.0.1:12800", workDir),
	}
	ca.globalCtx = &gctx
	// Get max steps from config, default to 10 if not set
	repoMaxSteps := 20
	codingMaxSteps := 30
	conductorMaxSteps := 20

	if ca.config != nil {
		if ca.config.Agent.RepoMaxSteps > 0 {
			repoMaxSteps = ca.config.Agent.RepoMaxSteps
		}
		if ca.config.Agent.CodingMaxSteps > 0 {
			codingMaxSteps = ca.config.Agent.CodingMaxSteps
		}
		if ca.config.Agent.ConductorMaxSteps > 0 {
			conductorMaxSteps = ca.config.Agent.ConductorMaxSteps
		}
	}

	metaMaxSteps := 30
	if ca.config != nil && ca.config.Agent.MetaMaxSteps > 0 {
		metaMaxSteps = ca.config.Agent.MetaMaxSteps
	}

	repoAgent := agents.NewRepoAgent(ca.globalCtx, llm, publisher, repoMaxSteps)
	codingAgent := agents.NewCodingAgent(ca.globalCtx, llm, codingMaxSteps)
	chatAgent := agents.NewChatAgent(ca.globalCtx, llm)
	metaAgent := agents.NewMetaAgent(ca.globalCtx, llm, metaMaxSteps)
	ca.conductor = agents.NewConductorAgent(ca.globalCtx, llm, repoAgent, codingAgent, chatAgent, metaAgent, conductorMaxSteps)
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
	publisher   *MessagePublisher
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

func (r *TaskRequest) WithMessagePublisher(p *MessagePublisher) *TaskRequest {
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
