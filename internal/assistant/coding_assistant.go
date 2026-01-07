package assistant

import (
	"context"
	"log/slog"
	"sync"

	"codeactor/internal/assistant/agents"
	"codeactor/internal/assistant/tools"
	"codeactor/internal/config"
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

	// Tools
	fileOps      *tools.FileOperationsTool
	searchOps    *tools.SearchOperationsTool
	sysOps       *tools.SystemOperationsTool
	replaceTool  *tools.ReplaceBlockTool
	thinkingTool *tools.ThinkingTool
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

	// Initialize tools
	ca.fileOps = tools.NewFileOperationsTool(workDir)
	ca.searchOps = tools.NewSearchOperationsTool(workDir)
	ca.sysOps = tools.NewSystemOperationsTool(workDir)
	ca.replaceTool = tools.NewReplaceBlockTool(workDir)
	ca.thinkingTool = tools.NewThinkingTool()

	// Initialize agents
	publisher := messaging.NewMessagePublisher(ca.dispatcher)
	// Get max steps from config, default to 10 if not set
	repoMaxSteps := 10
	codingMaxSteps := 10
	conductorMaxSteps := 10

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

	repoAgent := agents.NewRepoAgent(llm, publisher, ca.fileOps, ca.searchOps, ca.sysOps, workDir, repoMaxSteps)
	codingAgent := agents.NewCodingAgent(llm, publisher, ca.fileOps, ca.sysOps, ca.replaceTool, ca.thinkingTool, codingMaxSteps)
	ca.conductor = agents.NewConductorAgent(llm, publisher, repoAgent, codingAgent, conductorMaxSteps)
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
	memory      *ConversationMemory
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

func (r *TaskRequest) WithMemory(mem *ConversationMemory) *TaskRequest {
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
	if ca.conductor == nil {
		ca.Init(ca.llm, req.projectDir)
	} else {
		ca.Init(ca.llm, req.projectDir)
	}

	return ca.conductor.Run(req.ctx, req.taskDesc)
}

// ProcessConversation handles chat messages.
func (ca *CodingAssistant) ProcessConversation(req *TaskRequest) (string, error) {
	if ca.conductor == nil {
		ca.Init(ca.llm, req.projectDir)
	} else {
		ca.Init(ca.llm, req.projectDir)
	}

	return ca.conductor.Run(req.ctx, req.userMessage)
}
