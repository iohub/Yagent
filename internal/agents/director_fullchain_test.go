package agents

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"codeactor/internal/config"
	"codeactor/internal/llm"
	"codeactor/internal/messaging"
	"codeactor/internal/tools"
)

// ─── Full-Chain Mock LLM Engine ──────────────────────────────────────────────

type fullChainCallLog struct {
	Messages []llm.Message
	Tools    []llm.ToolDef
}

// fullChainMockLLM is a thread-safe mock LLM engine that returns pre-queued responses
// in order, recording each call for verification.
type fullChainMockLLM struct {
	mu             sync.Mutex
	responses      []*llm.Response
	callCount      int
	callLog        []fullChainCallLog
	defaultOnExhaust *llm.Response // returned when queue is exhausted (nil=error)
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

	// Queue exhausted
	if m.defaultOnExhaust != nil {
		return m.defaultOnExhaust, nil
	}
	return nil, fmt.Errorf("fullChainMockLLM: exceeded response queue (call %d, queue length %d)", m.callCount, len(m.responses))
}

func (m *fullChainMockLLM) Model() string {
	return "fullchain-mock"
}

func (m *fullChainMockLLM) CloseIdleConnections() {
	// no-op for mock
}

// CallCount returns the number of times GenerateContent was called.
func (m *fullChainMockLLM) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCount
}

// SetDefaultOnExhaust sets a response to return when the queue is exhausted.
func (m *fullChainMockLLM) SetDefaultOnExhaust(resp *llm.Response) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.defaultOnExhaust = resp
}

// ─── Helper Functions to Build Mock Responses ─────────────────────────────────

// makeToolCallResponse creates an LLM Response containing a single tool call.
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

// makeTextThenExitResponse creates an LLM Response with text + agent_exit tool call.
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

// ─── Director Factory ────────────────────────────────────────────────────────

func newFullChainDirector(t *testing.T, workDir string, mock *fullChainMockLLM) *DirectorAgent {
	t.Helper()

	gctx := newTestGlobalCtx(workDir)
	gctx.FullYoloMode = true

	publisher := messaging.NewMessagePublisher(nil)
	gctx.Publisher = publisher

	repoAgent := NewRepoAgent(gctx, mock, publisher, 10)
	codingAgent := NewCodingAgent(gctx, mock, 10, nil)
	chatAgent := NewChatAgent(gctx, mock, 10)
	metaAgent := NewMetaAgent(gctx, mock, 3)
	devopsAgent := NewDevOpsAgent(gctx, mock, 10)

	director := NewDirectorAgent(
		gctx,
		mock,
		repoAgent,
		codingAgent,
		chatAgent,
		metaAgent,
		devopsAgent,
		nil, // browser
		10,  // maxSteps
		nil, // disabledAgents
		3,   // metaRetryCount
		config.Config{},
		nil, // llmClient
	)

	return director
}

// safeExitResponse is a generic fallback response used when the mock queue is exhausted.
var safeExitResponse = makeTextThenExitResponse("重试安全响应")

// ─── Test 1: Direct Tool Call ────────────────────────────────────────────────

func TestFullChain_DirectToolCall(t *testing.T) {
	t.Parallel()

	// 1. Create temp working directory with test files
	workDir := t.TempDir()
	if err := writeFile(t, workDir, "main.go", "package main\n\nfunc main() {}\n"); err != nil {
		t.Fatalf("failed to create main.go: %v", err)
	}
	if err := writeFile(t, workDir, "README.md", "# Test Project\n"); err != nil {
		t.Fatalf("failed to create README.md: %v", err)
	}

	// 2. Create mock LLM with response queue:
	//    Call 1: list_dir tool call
	//    Call 2: text + agent_exit
	mock := &fullChainMockLLM{
		responses: []*llm.Response{
			makeToolCallResponse("list_dir", `{"dir_path": ".", "max_depth": 3}`),
			makeTextThenExitResponse("任务完成"),
		},
	}

	// 3. Create DirectorAgent
	director := newFullChainDirector(t, workDir, mock)

	// 4. Execute
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := director.Run(ctx, "列出项目根目录的文件", nil)
	if err != nil {
		t.Fatalf("director.Run() returned error: %v", err)
	}

	// 5. Assert
	t.Logf("Director returned result: %s", result)

	if mock.CallCount() != 2 {
		t.Errorf("expected 2 LLM calls, got %d", mock.CallCount())
	}

	if result == "" {
		t.Error("expected non-empty result")
	}
}

// ─── Test 2: Delegate to RepoAgent ───────────────────────────────────────────

func TestFullChain_DelegateToRepoAgent(t *testing.T) {
	t.Parallel()

	// 1. Create temp working directory
	workDir := t.TempDir()
	if err := writeFile(t, workDir, "main.go", "package main\n\nfunc main() {}\n"); err != nil {
		t.Fatalf("failed to create main.go: %v", err)
	}

	// 2. Mock response queue:
	//    Call 1 (Director): delegate_repo
	//    Call 2 (RepoAgent): list_dir
	//    Call 3 (RepoAgent): text + agent_exit
	//    Call 4 (Director): text + agent_exit
	//    Remaining: safe fallback for retries
	mock := &fullChainMockLLM{
		responses: []*llm.Response{
			// Call 1: Director delegates to Repo-Agent
			makeToolCallResponse("delegate_repo", `{"task": "分析项目结构"}`),
			// Call 2: Repo-Agent calls list_dir
			makeToolCallResponse("list_dir", `{"dir_path": ".", "max_depth": 3}`),
			// Call 3: Repo-Agent returns result + exit
			makeTextThenExitResponse("项目结构分析完成"),
			// Call 4: Director acknowledges and exits
			makeTextThenExitResponse("已收到分析结果"),
		},
		// Safe fallback for retry scenarios
		defaultOnExhaust: safeExitResponse,
	}

	// 3. Create DirectorAgent
	director := newFullChainDirector(t, workDir, mock)

	// 4. Execute
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := director.Run(ctx, "请分析项目结构", nil)
	if err != nil {
		t.Fatalf("director.Run() returned error: %v", err)
	}

	// 5. Assert
	t.Logf("Director returned result: %s", result)
	t.Logf("Total LLM calls: %d", mock.CallCount())

	// Expect at least 4 calls (core flow); retries may add more
	if mock.CallCount() < 4 {
		t.Errorf("expected at least 4 LLM calls, got %d", mock.CallCount())
	}

	if result == "" {
		t.Error("expected non-empty result")
	}
}

// ─── Test 3: FullYoloMode ───────────────────────────────────────────────────

func TestFullChain_FullYoloMode(t *testing.T) {
	t.Parallel()

	// 1. Create temp working directory
	workDir := t.TempDir()

	// 2. Create mock with 1 simple response
	mock := &fullChainMockLLM{
		responses: []*llm.Response{
			makeTextThenExitResponse("YOLO mode active"),
		},
		defaultOnExhaust: safeExitResponse,
	}

	// 3. Create DirectorAgent
	director := newFullChainDirector(t, workDir, mock)

	// 4. Assert adapter list (verification on construction)
	adapterNames := make(map[string]bool)
	for _, ad := range director.Adapters {
		adapterNames[ad.Name()] = true
	}

	t.Logf("Director adapters: %v", sortedAdapterNames(director.Adapters))

	// ask_user_for_help should NOT exist in FullYoloMode
	if adapterNames["ask_user_for_help"] {
		t.Error("ask_user_for_help should NOT be registered in FullYoloMode")
	}

	// agent_exit should exist
	if !adapterNames["agent_exit"] {
		t.Error("agent_exit should be registered")
	}

	// delegate tools should exist
	delegateTools := []string{"delegate_repo", "delegate_coding", "delegate_chat", "delegate_meta"}
	for _, tool := range delegateTools {
		if !adapterNames[tool] {
			t.Errorf("delegate tool %q should be registered", tool)
		}
	}

	// 5. Run to verify flow executes normally
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := director.Run(ctx, "YOLO mode test", nil)
	if err != nil {
		t.Fatalf("director.Run() in FullYoloMode returned error: %v", err)
	}

	t.Logf("FullYoloMode test result: %s", result)

	if mock.CallCount() != 1 {
		t.Errorf("expected 1 LLM call, got %d", mock.CallCount())
	}

	if result == "" {
		t.Error("expected non-empty result")
	}
}

// ─── Test 4: Multi-Delegation ───────────────────────────────────────────────

func TestFullChain_MultiDelegation(t *testing.T) {
	t.Parallel()

	// 1. Create temp working directory with a simple main.go
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

	// 2. Mock response queue:
	//    Call 1 (Director):  delegate_repo
	//    Call 2 (RepoAgent): search_by_regex
	//    Call 3 (RepoAgent): text + agent_exit
	//    Call 4 (Director):  delegate_coding
	//    Call 5 (CodingAgent): read_file
	//    Call 6 (CodingAgent): text + agent_exit
	//    Call 7 (Director):  text + agent_exit
	//    Remaining: safe fallback for retries
	mock := &fullChainMockLLM{
		responses: []*llm.Response{
			// Call 1: Director delegates to Repo-Agent
			makeToolCallResponse("delegate_repo", `{"task": "找出项目中的 Go 文件"}`),
			// Call 2: Repo-Agent searches for .go files
			makeToolCallResponse("search_by_regex", `{"query": "\\.go$", "path": "."}`),
			// Call 3: Repo-Agent returns result + exit
			makeTextThenExitResponse("找到 main.go"),
			// Call 4: Director delegates to Coding-Agent
			makeToolCallResponse("delegate_coding", `{"task": "检查 main.go 的代码质量"}`),
			// Call 5: Coding-Agent reads main.go
			makeToolCallResponse("read_file", `{"target_file": "main.go"}`),
			// Call 6: Coding-Agent returns quality assessment + exit
			makeTextThenExitResponse("代码质量良好"),
			// Call 7: Director returns final result + exit
			makeTextThenExitResponse("所有检查完成"),
		},
		// Safe fallback for retry scenarios
		defaultOnExhaust: safeExitResponse,
	}

	// 3. Create DirectorAgent
	director := newFullChainDirector(t, workDir, mock)

	// 4. Execute
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := director.Run(ctx, "全面检查项目", nil)
	if err != nil {
		t.Fatalf("director.Run() returned error: %v", err)
	}

	// 5. Assert
	t.Logf("Director returned result: %s", result)
	t.Logf("Total LLM calls: %d", mock.CallCount())

	// Expect at least 7 calls (core flow); retries may add more
	if mock.CallCount() < 7 {
		t.Errorf("expected at least 7 LLM calls, got %d", mock.CallCount())
	}

	if result == "" {
		t.Error("expected non-empty result")
	}
}

// ─── Utility Functions ───────────────────────────────────────────────────────

func writeFile(t *testing.T, dir, name, content string) error {
	t.Helper()
	return writeSafeFile(t, dir, name, content, 0644)
}

func writeSafeFile(t *testing.T, dir, name, content string, perm os.FileMode) error {
	t.Helper()
	path := dir + "/" + name
	data := []byte(content)
	if _, err := os.Stat(path); err == nil {
		return nil // file already exists
	}
	return os.WriteFile(path, data, perm)
}

func sortedAdapterNames(adapters []*tools.Adapter) []string {
	names := make([]string, len(adapters))
	for i, ad := range adapters {
		names[i] = ad.Name()
	}
	return names
}
