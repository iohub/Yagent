package agents

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	directorpkg "yagent/internal/agents/director"
	"yagent/internal/globalctx"
	"yagent/internal/llm"
	"yagent/internal/messaging"
)

type fullChainCallLog struct {
	Messages []llm.Message
	Tools    []llm.ToolDef
}

type fullChainMockLLM struct {
	mu               sync.Mutex
	responses        []*llm.Response
	callCount        int
	callLog          []fullChainCallLog
	defaultOnExhaust *llm.Response
}

func (m *fullChainMockLLM) GenerateContent(ctx context.Context, messages []llm.Message, tools []llm.ToolDef, opts *llm.CallOptions) (*llm.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.callCount++
	m.callLog = append(m.callLog, fullChainCallLog{
		Messages: messages,
		Tools:    tools,
	})

	if m.callCount <= len(m.responses) {
		return m.responses[m.callCount-1], nil
	}

	if m.defaultOnExhaust != nil {
		return m.defaultOnExhaust, nil
	}
	return nil, fmt.Errorf("fullChainMockLLM: exceeded response queue (call %d, queue length %d)", m.callCount, len(m.responses))
}

func (m *fullChainMockLLM) Model() string {
	return "fullchain-mock"
}

func (m *fullChainMockLLM) CloseIdleConnections() {
}

func (m *fullChainMockLLM) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCount
}

func (m *fullChainMockLLM) SetDefaultOnExhaust(resp *llm.Response) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.defaultOnExhaust = resp
}

func makeToolCallResponse(toolName string, args string) *llm.Response {
	return &llm.Response{
		Choices: []llm.Choice{{
			ToolCalls: []llm.ToolCall{{
				ID:   fmt.Sprintf("call_%s", toolName),
				Type: "function",
				Function: llm.FunctionCall{
					Name:      toolName,
					Arguments: args,
				},
			}},
		}},
	}
}

func makeTextThenExitResponse(content string) *llm.Response {
	return &llm.Response{
		Choices: []llm.Choice{{
			Content: content,
			ToolCalls: []llm.ToolCall{{
				ID:   "call_agent_exit",
				Type: "function",
				Function: llm.FunctionCall{
					Name:      "agent_exit",
					Arguments: fmt.Sprintf(`{"reason": "task completed: %s"}`, content),
				},
			}},
		}},
	}
}

func newFullChainDirector(t *testing.T, workDir string, mock *fullChainMockLLM) *DirectorAgent {
	t.Helper()

	gctx := newTestGlobalCtx(workDir)
	gctx.FullYoloMode = true

	publisher := messaging.NewMessagePublisher(nil)
	gctx.Publisher = publisher

	env := globalctx.NewEnvView(gctx)
	repoCtx := globalctx.NewRepoContextStore()

	repoAgent := NewRepoAgent(RepoAgentParams{
		Formatter:   gctx,
		Publisher:   publisher,
		Env:         env,
		Files:       gctx.FileOps,
		Search:      gctx.SearchOps,
		RepoOps:     gctx.RepoOps,
		Flow:        gctx.FlowOps,
		DeepThinker: gctx.DeepThinkingTool,
		Guard:       gctx.Guard,
		LLM:         mock,
		MaxSteps:    10,
	})
	codingAgent := NewCodingAgent(CodingAgentParams{
		Formatter:   gctx,
		Publisher:   publisher,
		Env:         env,
		Files:       gctx.FileOps,
		Search:      gctx.SearchOps,
		Sys:         gctx.SysOps,
		Edit:        gctx.ReplaceTool,
		RepoOps:     gctx.RepoOps,
		Flow:        gctx.FlowOps,
		Thinker:     gctx.ThinkingTool,
		DeepThinker: gctx.DeepThinkingTool,
		Designer:    gctx.DesignTool,
		MicroAgent:  gctx.MicroAgentTool,
		Guard:       gctx.Guard,
		RepoCtx:     repoCtx,
		LLM:         mock,
		MaxSteps:    10,
	})
	chatAgent := NewChatAgent(ChatAgentParams{
		Formatter:   gctx,
		Publisher:   publisher,
		Env:         env,
		Flow:        gctx.FlowOps,
		Thinker:     gctx.ThinkingTool,
		DeepThinker: gctx.DeepThinkingTool,
		MicroAgent:  gctx.MicroAgentTool,
		Guard:       gctx.Guard,
		LLM:         mock,
		MaxSteps:    10,
	})
	metaAgent := NewMetaAgent(gctx, publisher, mock, 3)
	devopsAgent := NewDevOpsAgent(DevOpsAgentParams{
		Formatter:  gctx,
		Publisher:  publisher,
		Env:        env,
		Files:      gctx.FileOps,
		Search:     gctx.SearchOps,
		Sys:        gctx.SysOps,
		Flow:       gctx.FlowOps,
		Thinker:    gctx.ThinkingTool,
		MicroAgent: gctx.MicroAgentTool,
		Guard:      gctx.Guard,
		LLM:        mock,
		MaxSteps:   10,
	})

	director := NewDirectorAgent(DirectorAgentParams{
		Formatter:      gctx,
		Publisher:      publisher,
		Env:            env,
		Files:          gctx.FileOps,
		Search:         gctx.SearchOps,
		Sys:            gctx.SysOps,
		Edit:           gctx.ReplaceTool,
		Flow:           gctx.FlowOps,
		Thinker:        gctx.ThinkingTool,
		MicroAgent:     gctx.MicroAgentTool,
		DeepThinker:    gctx.DeepThinkingTool,
		Guard:          gctx.Guard,
		RepoCtx:        repoCtx,
		LLM:            mock,
		RepoAgent:      repoAgent,
		CodingAgent:    codingAgent,
		ChatAgent:      chatAgent,
		MetaAgent:      metaAgent,
		DevOpsAgent:    devopsAgent,
		MaxSteps:       10,
		MetaRetryCount: 3,
	})

	return director
}

var safeExitResponse = makeTextThenExitResponse("重试安全响应")

func TestFullChain_DirectToolCall(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	if err := writeFile(t, workDir, "main.go", "package main\n\nfunc main() {}\n"); err != nil {
		t.Fatalf("failed to create main.go: %v", err)
	}
	if err := writeFile(t, workDir, "README.md", "# Test Project\n"); err != nil {
		t.Fatalf("failed to create README.md: %v", err)
	}

	mock := &fullChainMockLLM{
		responses: []*llm.Response{
			makeToolCallResponse("list_dir", `{"dir_path": ".", "max_depth": 3}`),
			makeTextThenExitResponse("任务完成"),
		},
	}

	director := newFullChainDirector(t, workDir, mock)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	agentResult, err := director.Run(ctx, "列出项目根目录的文件")
	if err != nil {
		t.Fatalf("director.Run() returned error: %v", err)
	}
	result := agentResult.Text

	t.Logf("Director returned result: %s", result)

	if mock.CallCount() != 2 {
		t.Errorf("expected 2 LLM calls, got %d", mock.CallCount())
	}

	if result == "" {
		t.Error("expected non-empty result")
	}
}

func TestFullChain_DelegateToRepoAgent(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	if err := writeFile(t, workDir, "main.go", "package main\n\nfunc main() {}\n"); err != nil {
		t.Fatalf("failed to create main.go: %v", err)
	}

	mock := &fullChainMockLLM{
		responses: []*llm.Response{
			makeToolCallResponse("delegate_repo", `{"task": "分析项目结构"}`),
			makeToolCallResponse("list_dir", `{"dir_path": ".", "max_depth": 3}`),
			makeTextThenExitResponse("项目结构分析完成"),
			makeTextThenExitResponse("已收到分析结果"),
		},
		defaultOnExhaust: safeExitResponse,
	}

	director := newFullChainDirector(t, workDir, mock)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	agentResult, err := director.Run(ctx, "请分析项目结构")
	if err != nil {
		t.Fatalf("director.Run() returned error: %v", err)
	}
	result := agentResult.Text

	t.Logf("Director returned result: %s", result)
	t.Logf("Total LLM calls: %d", mock.CallCount())

	if mock.CallCount() < 4 {
		t.Errorf("expected at least 4 LLM calls, got %d", mock.CallCount())
	}

	if result == "" {
		t.Error("expected non-empty result")
	}
}

func TestFullChain_FullYoloMode(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()

	mock := &fullChainMockLLM{
		responses: []*llm.Response{
			makeTextThenExitResponse("YOLO mode active"),
		},
		defaultOnExhaust: safeExitResponse,
	}

	director := newFullChainDirector(t, workDir, mock)

	adapterNames := make(map[string]bool)
	for _, ad := range director.Adapters {
		adapterNames[ad.Name()] = true
	}

	if adapterNames["ask_user_for_help"] {
		t.Error("ask_user_for_help should NOT be registered in FullYoloMode")
	}

	if !adapterNames["agent_exit"] {
		t.Error("agent_exit should be registered")
	}

	delegateTools := []string{"delegate_repo", "delegate_coding", "delegate_chat", "delegate_meta"}
	for _, tool := range delegateTools {
		if !adapterNames[tool] {
			t.Errorf("delegate tool %q should be registered", tool)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	agentResult, err := director.Run(ctx, "YOLO mode test")
	if err != nil {
		t.Fatalf("director.Run() in FullYoloMode returned error: %v", err)
	}
	result := agentResult.Text

	t.Logf("FullYoloMode test result: %s", result)

	if mock.CallCount() != 1 {
		t.Errorf("expected 1 LLM call, got %d", mock.CallCount())
	}

	if result == "" {
		t.Error("expected non-empty result")
	}
}

func TestFullChain_MultiDelegation(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	mainGoContent := `package main

import "fmt"

func main() {
	fmt.Println("Hello, World!")
}
`
	if err := writeFile(t, workDir, "main.go", mainGoContent); err != nil {
		t.Fatalf("failed to create main.go: %v", err)
	}

	mock := &fullChainMockLLM{
		responses: []*llm.Response{
			makeToolCallResponse("delegate_repo", `{"task": "找出项目中的 Go 文件"}`),
			makeToolCallResponse("search_by_regex", `{"query": "\\.go$", "path": "."}`),
			makeTextThenExitResponse("找到 main.go"),
			makeToolCallResponse("delegate_coding", `{"task": "检查 main.go 的代码质量"}`),
			makeToolCallResponse("read_file", `{"target_file": "main.go"}`),
			makeTextThenExitResponse("代码质量良好"),
			makeTextThenExitResponse("所有检查完成"),
		},
		defaultOnExhaust: safeExitResponse,
	}

	director := newFullChainDirector(t, workDir, mock)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	agentResult, err := director.Run(ctx, "全面检查项目")
	if err != nil {
		t.Fatalf("director.Run() returned error: %v", err)
	}
	result := agentResult.Text

	t.Logf("Director returned result: %s", result)
	t.Logf("Total LLM calls: %d", mock.CallCount())

	if mock.CallCount() < 7 {
		t.Errorf("expected at least 7 LLM calls, got %d", mock.CallCount())
	}

	if result == "" {
		t.Error("expected non-empty result")
	}
}

func writeFile(t *testing.T, dir, name, content string) error {
	t.Helper()
	return writeSafeFile(t, dir, name, content, 0644)
}

func writeSafeFile(t *testing.T, dir, name, content string, perm os.FileMode) error {
	t.Helper()
	path := dir + "/" + name
	data := []byte(content)
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	return os.WriteFile(path, data, perm)
}

// 强制委派提醒的文案来自 director/planner_run.go:533/535。
// 这里只留一份副本给三个断言点共用，避免文案改了而某处断言静默失效。
const (
	forceDelegationFirst  = "You must delegate an agent to complete the task. Do not reply with plain text — call a delegate_* tool now."
	forceDelegationRepeat = "You still have not delegated any agent. You MUST call a delegate_* tool to complete the task before responding."
)

func isForceDelegationMsg(msg llm.Message) bool {
	return msg.Role == llm.RoleUser &&
		(msg.Content == forceDelegationFirst || msg.Content == forceDelegationRepeat)
}

func TestDirector_ForceDelegation_InjectUserMessage(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()

	mock := &fullChainMockLLM{
		responses: []*llm.Response{
			{Choices: []llm.Choice{{Content: "I'll just return text without delegating."}}},
			makeTextThenExitResponse("task completed via delegation"),
		},
	}

	director := newFullChainDirector(t, workDir, mock)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	agentResult, err := director.Run(ctx, "complete the task")
	if err != nil {
		t.Fatalf("director.Run() returned error: %v", err)
	}
	result := agentResult.Text

	if result == "I'll just return text without delegating." {
		t.Error("expected result after force delegation, but got original plain text")
	}
	if result == "" {
		t.Error("expected non-empty result")
	}

	if mock.CallCount() != 2 {
		t.Errorf("expected 2 LLM calls, got %d", mock.CallCount())
	}

	if len(mock.callLog) < 2 {
		t.Fatalf("expected at least 2 call logs, got %d", len(mock.callLog))
	}
	secondCallMessages := mock.callLog[1].Messages
	foundForceMsg := false
	for _, msg := range secondCallMessages {
		if msg.Role == llm.RoleUser && msg.Content == forceDelegationFirst {
			foundForceMsg = true
			break
		}
	}
	if !foundForceMsg {
		t.Errorf("expected force delegation user message in second call, got messages: %+v",
			func() []string {
				msgs := make([]string, len(secondCallMessages))
				for i, m := range secondCallMessages {
					msgs[i] = fmt.Sprintf("%s:%s", m.Role, m.Content)
				}
				return msgs
			}())
	}
}

func TestDirector_ForceDelegation_MaxPromptsThenReturn(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()

	plainTextResponses := make([]*llm.Response, directorpkg.MaxNonDelegationPrompts+2)
	for i := range plainTextResponses {
		plainTextResponses[i] = &llm.Response{Choices: []llm.Choice{{Content: fmt.Sprintf("plain text response %d", i)}}}
	}
	mock := &fullChainMockLLM{
		responses: plainTextResponses,
	}

	director := newFullChainDirector(t, workDir, mock)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	agentResult, err := director.Run(ctx, "complete the task")
	if err != nil {
		t.Fatalf("director.Run() returned error: %v", err)
	}
	result := agentResult.Text

	if result == "" {
		t.Error("expected non-empty result")
	}

	callCount := mock.CallCount()
	if callCount <= 1 {
		t.Errorf("expected more than 1 LLM call due to force delegation, got %d", callCount)
	}
	if callCount > director.maxSteps {
		t.Errorf("expected at most %d LLM calls, got %d", director.maxSteps, callCount)
	}

	injectionCount := 0
	var prevForceCount int
	for idx, log := range mock.callLog {
		forceCount := 0
		for _, msg := range log.Messages {
			if isForceDelegationMsg(msg) {
				forceCount++
			}
		}
		if idx > 0 && forceCount > prevForceCount {
			injectionCount++
		}
		prevForceCount = forceCount
	}
	if injectionCount != directorpkg.MaxNonDelegationPrompts {
		t.Errorf("expected %d force message injections, got %d (total calls: %d)", directorpkg.MaxNonDelegationPrompts, injectionCount, mock.CallCount())
	}
}

func TestDirector_NoForceAfterDelegation(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()

	mock := &fullChainMockLLM{
		responses: []*llm.Response{
			makeToolCallResponse("delegate_repo", `{"task": "analyze project"}`),
			makeToolCallResponse("list_dir", `{"dir_path": ".", "max_depth": 2}`),
			makeTextThenExitResponse("project analysis done"),
			{Choices: []llm.Choice{{Content: "task completed after delegation"}}},
		},
		defaultOnExhaust: &llm.Response{Choices: []llm.Choice{{Content: "task completed after delegation"}}},
	}

	director := newFullChainDirector(t, workDir, mock)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	agentResult, err := director.Run(ctx, "complete the task")
	if err != nil {
		t.Fatalf("director.Run() returned error: %v", err)
	}
	result := agentResult.Text

	if result == "" {
		t.Error("expected non-empty result")
	}
	if result != "task completed after delegation" && result != "fallback" {
		t.Logf("result: %q (expected 'task completed after delegation' or 'fallback')", result)
	}

	if mock.CallCount() < 2 {
		t.Errorf("expected at least 2 LLM calls, got %d", mock.CallCount())
	}

	for _, log := range mock.callLog {
		for _, msg := range log.Messages {
			if isForceDelegationMsg(msg) {
				t.Error("unexpected force delegation user message after successful delegation")
			}
		}
	}
}
