package agents

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"yagent/internal/toolspec"

	"yagent/internal/config"
	"yagent/internal/globalctx"
	"yagent/internal/llm"
	"yagent/internal/memory"
	"yagent/internal/messaging"
	"yagent/internal/tools"
)

// ─── Mock Engine ──────────────────────────────────────────────────────────────

type mockEngine struct {
	generateContent func(ctx context.Context, messages []llm.Message, tools []llm.ToolDef, opts *llm.CallOptions) (*llm.Response, error)
}

func (m *mockEngine) GenerateContent(ctx context.Context, messages []llm.Message, tools []llm.ToolDef, opts *llm.CallOptions) (*llm.Response, error) {
	if m.generateContent != nil {
		return m.generateContent(ctx, messages, tools, opts)
	}
	return &llm.Response{Choices: []llm.Choice{{Content: ""}}}, nil
}

func (m *mockEngine) Model() string {
	return "mock-model"
}

func (m *mockEngine) CloseIdleConnections() {
	// no-op for mock
}

// ─── Test Helpers ────────────────────────────────────────────────────────────

func newTestGlobalCtx(workDir string) *globalctx.GlobalCtx {
	return &globalctx.GlobalCtx{
		ProjectPath: workDir,
		OS:          "linux",
		Arch:        "amd64",
		SpeakLang:   "Chinese",
		// Publisher 必须为非 nil 实例:*MessagePublisher(nil 字段)装入 EventBus
		// 接口会产生 typed-nil,逃过 cfg.Publisher != nil 检查导致 Publish panic;
		// dispatcher 为 nil 时 Publish 直接返回(与 fullchain 测试同模式)。
		Publisher:    messaging.NewMessagePublisher(nil),
		FileOps:      tools.NewFileOperationsTool(workDir),
		SearchOps:    tools.NewSearchOperationsTool(workDir),
		SysOps:       tools.NewSystemOperationsTool(workDir),
		ReplaceTool:  tools.NewReplaceBlockTool(workDir),
		ThinkingTool: tools.NewThinkingTool(),
		// micro_agent/deepthinking/design 工具在 agent 构造期做方法值求值(接口不可为 nil),
		// 与旧测试 GlobalCtx 提供真实工具实例的语义一致;LLM 为 nil(测试不触发调用)。
		DeepThinkingTool: tools.NewDeepThinkingTool(nil),
		DesignTool:       tools.NewDesignTool(nil),
		MicroAgentTool:   tools.NewMicroAgentTool(nil),
		FlowOps:          tools.NewFlowControlTool(workDir),
		RepoOps:          tools.NewRepoOperationsTool(nil, workDir, 0),
	}
}

// newDirectorAgentForTest 用与生产语义一致的窄依赖构造 DirectorAgent：
// formatter/publisher/env/各工具集取自 gctx（具体类型天然实现各窄接口），
// 测试未用到的 microAgent/deepThinker/repoCtx/knowledge/mcpClient 传 nil
// （与旧测试 GlobalCtx 不设置这些字段的行为一致）。
// 参数顺序沿用旧签名（gctx, engine, 六个子代理, maxSteps, disabledAgents,
// metaRetryCount, cfg, llmClient），使各测试调用点零改动。
//
// 第六位在 P9a 从 `meta *MetaAgent` 换成生成器窄接口：Go 侧已经没有 Meta-Agent，
// 需要 delegate_meta 的用例注入假生成器，其余用例传 nil（= 降级，delegate_meta
// 不进工具面）。传入非 nil 生成器时**必须**同时给 plasmaStore/plasmaRunner，
// 否则会往用户 home 的产物仓里写 bundle（见 plasma_meta_test.go 的 newPlasmaDirector）。
func newDirectorAgentForTest(gctx *globalctx.GlobalCtx, engine llm.Engine, repo *RepoAgent, coding *CodingAgent, chat *ChatAgent, gen PlasmaGenerator, devops *DevOpsAgent, browser *BrowserAgent, maxSteps int, disabledAgents map[string]bool, metaRetryCount int, cfg config.Config, llmClient *llm.Client) *DirectorAgent {
	return NewDirectorAgent(DirectorAgentParams{
		Formatter:       gctx,                       // PromptFormatter: *GlobalCtx 天然实现
		Publisher:       gctx.Publisher,             // EventBus
		Env:             globalctx.NewEnvView(gctx), // Env
		Files:           gctx.FileOps,               // FileToolSet
		Search:          gctx.SearchOps,             // SearchToolSet
		Sys:             gctx.SysOps,                // SysToolSet
		Edit:            gctx.ReplaceTool,           // EditToolSet
		Flow:            gctx.FlowOps,               // FlowToolSet
		Thinker:         gctx.ThinkingTool,          // Thinker
		MicroAgent:      gctx.MicroAgentTool,        // MicroAgentRunner
		DeepThinker:     gctx.DeepThinkingTool,      // DeepThinker
		Guard:           gctx.Guard,                 // *tools.WorkspaceGuard: 测试中为 nil,与旧语义一致
		LLM:             engine,
		RepoAgent:       repo,
		CodingAgent:     coding,
		ChatAgent:       chat,
		PlasmaGenerator: gen,
		DevOpsAgent:     devops,
		BrowserAgent:    browser,
		MaxSteps:        maxSteps,
		DisabledAgents:  disabledAgents,
		MetaRetryCount:  metaRetryCount,
		Config:          cfg,
		LLMClient:       llmClient,
		// RepoCtx/Knowledge/MCPClient 测试未用，留 nil（与旧语义一致）
	})
}

// newTestDirectorAgent creates a DirectorAgent with real GlobalCtx but nil sub-agents.
// Tests that don't invoke delegate tools can use this safely.
func newTestDirectorAgent(t *testing.T, workDir string) *DirectorAgent {
	t.Helper()
	gctx := newTestGlobalCtx(workDir)
	engine := &mockEngine{}
	return newDirectorAgentForTest(gctx, engine, nil, nil, nil, nil, nil, nil, 10, nil, 3, config.Config{}, nil)
}

// ─── Custom Agent 工具池（取代原 getToolFunc）────────────────────────────────
// 过去 Custom Agent 能拿什么由 director.go 里第二个 switch 硬编码；现在由注册表按
// 名字解析 + Director 的 Palette 上限约束。这里钉住"历史可用集合仍然可用"。

func TestCustomAgentPool_KnownTools(t *testing.T) {
	workDir := t.TempDir()
	agent := newTestDirectorAgent(t, workDir)

	// 历史 getToolFunc 支持的 12 个工具，必须仍在可发放范围内。
	knownTools := []string{
		"read_file", "search_replace_in_file", "create_file", "run_bash",
		"search_by_regex", "list_dir", "print_dir_tree",
		"thinking", "micro_agent", "deepthinking",
		"agent_exit", "ask_user_for_help",
	}
	// meta.prompt.md 一直广告这三个，但旧 switch 没有 case，等于给了个假承诺
	// （docs/tool-registry-design.md §9.4）。
	newlyHonored := []string{"semantic_search", "query_code_skeleton", "query_code_snippet"}

	selected, unknown, rejected := toolspec.MatchNames(
		append(append([]string(nil), knownTools...), newlyHonored...),
		toolspec.ProfileDirector.Palette, agent.toolDeps())

	if len(unknown) != 0 {
		t.Errorf("未注册的工具：%v", unknown)
	}
	if len(rejected) != 0 {
		t.Errorf("被 Palette 拒绝的工具：%v", rejected)
	}
	got := map[string]bool{}
	for _, sp := range selected {
		got[sp.Name] = true
	}
	for _, name := range append(append([]string(nil), knownTools...), newlyHonored...) {
		if !got[name] {
			t.Errorf("%s 应可发放给 Custom Agent", name)
		}
	}
}

func TestCustomAgentPool_UnknownTool(t *testing.T) {
	workDir := t.TempDir()
	agent := newTestDirectorAgent(t, workDir)

	_, unknown, _ := toolspec.MatchNames([]string{"nonexistent_tool_xyz"}, toolspec.ProfileDirector.Palette, agent.toolDeps())
	if len(unknown) != 1 || unknown[0] != "nonexistent_tool_xyz" {
		t.Errorf("unknown = %v, want [nonexistent_tool_xyz]", unknown)
	}
}

// ─── registerCustomAgent Tests ──────────────────────────────────────────────

func TestRegisterCustomAgent_Success(t *testing.T) {
	workDir := t.TempDir()
	agent := newTestDirectorAgent(t, workDir)

	initialAdapterCount := len(agent.surface.Adapters())

	ca := &CustomAgent{
		Name:         "security_auditor",
		DisplayName:  "Security Auditor",
		SystemPrompt: "You are a security auditor.",
		ToolsUsed:    []string{"read_file", "search_by_regex", "thinking"},
		Description:  "Audits code for security vulnerabilities.",
	}
	agent.registerCustomAgent(ca)

	// Verify map entry
	stored, ok := agent.metaHandler.Get("delegate_security_auditor")
	if !ok {
		t.Fatal("custom agent not found in map")
	}
	if stored.DisplayName != "Security Auditor" {
		t.Errorf("display name = %q, want 'Security Auditor'", stored.DisplayName)
	}

	// Verify adapter was added
	if len(agent.surface.Adapters()) != initialAdapterCount+1 {
		t.Errorf("adapter count = %d, want %d", len(agent.surface.Adapters()), initialAdapterCount+1)
	}

	// Verify the new delegate tool exists
	found := false
	for _, ad := range agent.surface.Adapters() {
		if ad.Name() == "delegate_security_auditor" {
			found = true
			break
		}
	}
	if !found {
		t.Error("delegate_security_auditor adapter not found in the surface")
	}
}

func TestRegisterCustomAgent_DuplicateRegistration(t *testing.T) {
	workDir := t.TempDir()
	agent := newTestDirectorAgent(t, workDir)

	ca := &CustomAgent{
		Name:         "test_agent",
		DisplayName:  "Test Agent",
		SystemPrompt: "You are a test agent.",
		ToolsUsed:    []string{"read_file"},
		Description:  "Test agent.",
	}

	agent.registerCustomAgent(ca)
	countAfterFirst := len(agent.surface.Adapters())

	// Register the same agent again
	agent.registerCustomAgent(ca)
	if len(agent.surface.Adapters()) != countAfterFirst {
		t.Errorf("duplicate registration should not add new adapters, got %d, want %d", len(agent.surface.Adapters()), countAfterFirst)
	}
}

func TestRegisterCustomAgent_UnknownToolsIgnored(t *testing.T) {
	workDir := t.TempDir()
	agent := newTestDirectorAgent(t, workDir)

	ca := &CustomAgent{
		Name:         "partial_agent",
		DisplayName:  "Partial Agent",
		SystemPrompt: "You are a partial agent.",
		ToolsUsed:    []string{"read_file", "nonexistent_tool", "search_by_regex"},
		Description:  "Agent with some unknown tools.",
	}

	// Should not panic; unknown tools are skipped
	agent.registerCustomAgent(ca)

	stored, _ := agent.metaHandler.Get("delegate_partial_agent")
	if stored == nil {
		t.Fatal("agent should be registered even with unknown tools")
	}
}

// ─── Custom Agent Delegate Tool Execution ────────────────────────────────────

func TestCustomAgentDelegateTool_Execution(t *testing.T) {
	workDir := t.TempDir()
	gctx := newTestGlobalCtx(workDir)

	// Use a mock LLM that returns a simple response for the custom agent
	customEngine := &mockEngine{
		generateContent: func(ctx context.Context, messages []llm.Message, tools []llm.ToolDef, opts *llm.CallOptions) (*llm.Response, error) {
			return &llm.Response{
				Choices: []llm.Choice{
					{Content: "Custom agent task completed."},
				},
			}, nil
		},
	}

	// Build director with mocked LLM
	director := newDirectorAgentForTest(gctx, customEngine, nil, nil, nil, nil, nil, nil, 10, nil, 3, config.Config{}, nil)

	ca := &CustomAgent{
		Name:         "test_executor",
		DisplayName:  "Test Executor",
		SystemPrompt: "You are a test executor. Complete the task.",
		ToolsUsed:    []string{"read_file", "thinking", "agent_exit"},
		Description:  "Executes test tasks.",
	}
	director.registerCustomAgent(ca)

	// Find the newly created delegate tool
	var delegateTool *tools.Adapter
	for _, ad := range director.surface.Adapters() {
		if ad.Name() == "delegate_test_executor" {
			delegateTool = ad
			break
		}
	}
	if delegateTool == nil {
		t.Fatal("delegate_test_executor tool not found")
	}

	// Call the delegate tool — should run executeCustomAgent
	result, err := delegateTool.Call(context.Background(), `{"task": "Do something"}`)
	if err != nil {
		t.Fatalf("delegate tool call failed: %v", err)
	}
	if !strings.Contains(result, "Custom agent task completed") {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestCustomAgentDelegateTool_FinishTerminates(t *testing.T) {
	workDir := t.TempDir()
	gctx := newTestGlobalCtx(workDir)

	callCount := 0
	customEngine := &mockEngine{
		generateContent: func(ctx context.Context, messages []llm.Message, tools []llm.ToolDef, opts *llm.CallOptions) (*llm.Response, error) {
			callCount++
			if callCount == 1 {
				// First call: use agent_exit to complete
				return &llm.Response{
					Choices: []llm.Choice{{
						Content: "Task is complete.",
						ToolCalls: []llm.ToolCall{{
							ID:   "call_agent_exit",
							Type: "function",
							Function: llm.FunctionCall{
								Name:      "agent_exit",
								Arguments: `{"reason": "Test completed successfully"}`,
							},
						}},
					}},
				}, nil
			}
			return &llm.Response{
				Choices: []llm.Choice{{Content: "Should not reach here"}},
			}, nil
		},
	}

	director := newDirectorAgentForTest(gctx, customEngine, nil, nil, nil, nil, nil, nil, 10, nil, 3, config.Config{}, nil)

	ca := &CustomAgent{
		Name:         "finisher",
		DisplayName:  "Finisher Agent",
		SystemPrompt: "You exit immediately.",
		ToolsUsed:    []string{"agent_exit"},
		Description:  "Always exits.",
	}
	director.registerCustomAgent(ca)

	var delegateTool *tools.Adapter
	for _, ad := range director.surface.Adapters() {
		if ad.Name() == "delegate_finisher" {
			delegateTool = ad
			break
		}
	}
	if delegateTool == nil {
		t.Fatal("delegate_finisher tool not found")
	}

	result, err := delegateTool.Call(context.Background(), `{"task": "Finish"}`)
	if err != nil {
		t.Fatalf("delegate tool call failed: %v", err)
	}

	// Should have called finish and returned the result
	if callCount != 1 {
		t.Errorf("expected 1 LLM call, got %d", callCount)
	}
	if !strings.Contains(result, "Test completed successfully") {
		t.Errorf("unexpected result: %s", result)
	}
}

// ─── System Prompt Custom Agents Tests ───────────────────────────────────────
// 断言走生产构造器 customAgentPromptSection()（director.go:832 拼进 system prompt 的
// 就是它），此前测试自己重写了一遍拼接逻辑，生产改文案测试也不会红。

func TestSystemPrompt_NoCustomAgents(t *testing.T) {
	workDir := t.TempDir()
	agent := newTestDirectorAgent(t, workDir)

	if got := agent.customAgentPromptSection(); got != "" {
		t.Errorf("no custom agents registered, section should be empty, got %q", got)
	}
}

func TestSystemPrompt_WithCustomAgents(t *testing.T) {
	workDir := t.TempDir()
	agent := newTestDirectorAgent(t, workDir)

	// Register two custom agents
	agent.registerCustomAgent(&CustomAgent{
		Name:         "security_auditor",
		DisplayName:  "Security Auditor",
		SystemPrompt: "You are a security auditor.",
		ToolsUsed:    []string{"read_file", "search_by_regex"},
		Description:  "Audits code for security vulnerabilities.",
	})
	agent.registerCustomAgent(&CustomAgent{
		Name:         "data_migrator",
		DisplayName:  "Data Migrator",
		SystemPrompt: "You are a data migration specialist.",
		ToolsUsed:    []string{"create_file", "run_bash"},
		Description:  "Handles database migration planning.",
	})

	section := agent.customAgentPromptSection()
	for _, want := range []string{
		"### Custom Agents",
		"Security Auditor", "delegate_security_auditor", "Audits code for security vulnerabilities.",
		"Data Migrator", "delegate_data_migrator", "Handles database migration planning.",
	} {
		if !strings.Contains(section, want) {
			t.Errorf("custom agents section missing %q:\n%s", want, section)
		}
	}

	// 清单必须真的出现在最终 system prompt 里（拼接位置由 Run 决定）
	full := agent.promptFmt.FormatPrompt(directorPrompt) + section
	if !strings.Contains(full, "delegate_security_auditor") {
		t.Error("system prompt should carry the custom agents section")
	}
}

// ─── Sub-Agent Engine Refresh / Auto-Routing Tests ──────────────────────────

// refreshSubAgentEngines must keep the *llm.RoutingEngine wrapper. Assigning a bare
// provider engine makes RunAgentLoop's ResolveTierEngine type assertion fail, so the
// tier written by routeCtx is silently discarded and auto-routing stops working.
//
// P9a 之后内建 Agent 是 6 个：meta 那一档已经交给生成器（plasma），Director 上不再有
// MetaAgent 字段，所以「刷新子 Agent 引擎」的清单里也没有它。
func TestRefreshSubAgentEngines_KeepsRoutingEngine(t *testing.T) {
	a := &DirectorAgent{
		llmClient:    &llm.Client{},
		RepoAgent:    &RepoAgent{},
		CodingAgent:  &CodingAgent{},
		ChatAgent:    &ChatAgent{},
		DevOpsAgent:  &DevOpsAgent{},
		BrowserAgent: &BrowserAgent{},
	}

	a.refreshSubAgentEngines()

	engines := map[string]llm.Engine{
		"repo":     a.RepoAgent.LLM,
		"coding":   a.CodingAgent.LLM,
		"chat":     a.ChatAgent.LLM,
		"devops":   a.DevOpsAgent.LLM,
		"browser":  a.BrowserAgent.LLM,
		"director": a.routingEngine("director"),
	}
	for name, eng := range engines {
		if _, ok := eng.(*llm.RoutingEngine); !ok {
			t.Errorf("%s engine = %T, want *llm.RoutingEngine", name, eng)
		}
	}
}

// ─── Memory Conversion Tests (existing) ─────────────────────────────────────

func TestConvertMemoryMessageToLLMSMessage_ToolMessage(t *testing.T) {
	toolCallID := "call_123"
	content := "Tool execution result"

	msg := memory.ChatMessage{
		Type:       memory.MessageTypeTool,
		Content:    content,
		ToolCallID: &toolCallID,
	}

	llmMsg := memory.ConvertMemoryMessageToLLMSMessage(msg)

	if llmMsg.Role != llm.RoleTool {
		t.Errorf("Expected role %s, got %s", llm.RoleTool, llmMsg.Role)
	}
	if llmMsg.ToolCallID != toolCallID {
		t.Errorf("Expected ToolCallID %q, got %q", toolCallID, llmMsg.ToolCallID)
	}
	if llmMsg.Content != content {
		t.Errorf("Expected Content %q, got %q", content, llmMsg.Content)
	}
}

func TestConvertMemoryMessageToLLMSMessage_AssistantWithToolCalls(t *testing.T) {
	msg := memory.ChatMessage{
		Type:    memory.MessageTypeAssistant,
		Content: "Let me check that.",
		ToolCalls: []memory.ToolCallData{
			{
				ID:   "call_1",
				Type: "function",
				Function: memory.ToolCallFunction{
					Name:      "read_file",
					Arguments: json.RawMessage(`{"target_file": "/tmp/test.go"}`),
				},
			},
		},
	}

	llmMsg := memory.ConvertMemoryMessageToLLMSMessage(msg)

	if llmMsg.Role != llm.RoleAssistant {
		t.Errorf("Expected role %s, got %s", llm.RoleAssistant, llmMsg.Role)
	}
	if llmMsg.Content != "Let me check that." {
		t.Errorf("Expected Content to be set, got %q", llmMsg.Content)
	}
	if len(llmMsg.ToolCalls) != 1 {
		t.Fatalf("Expected 1 ToolCall, got %d", len(llmMsg.ToolCalls))
	}
	if llmMsg.ToolCalls[0].ID != "call_1" {
		t.Errorf("Expected ToolCall ID 'call_1', got %q", llmMsg.ToolCalls[0].ID)
	}
	if llmMsg.ToolCalls[0].Function.Name != "read_file" {
		t.Errorf("Expected ToolCall function name 'read_file', got %q", llmMsg.ToolCalls[0].Function.Name)
	}
}

// ─── Adapter Count Test ─────────────────────────────────────────────────────

func TestDirectorAgent_InitialAdapters(t *testing.T) {
	workDir := t.TempDir()
	agent := newTestDirectorAgent(t, workDir)

	// Should have: agent_exit + search_by_regex + list_dir + read_file + print_dir_tree
	// + delegate_repo + delegate_coding + delegate_chat
	// = 8 adapters minimum
	//
	// delegate_meta 不在这里：它的可用性由 plasma 生成器决定（ADR-15），测试环境没有
	// plasma.pyz ⇒ 它被降级掉。那条降级本身由
	// TestDelegateMeta_GatedByPlasmaAvailability 钉住。
	if len(agent.surface.Adapters()) < 8 {
		t.Errorf("expected at least 8 initial adapters, got %d", len(agent.surface.Adapters()))
	}

	// Verify all core adapters are present
	coreNames := []string{
		"agent_exit", "search_by_regex", "list_dir", "read_file", "print_dir_tree",
		"delegate_repo", "delegate_coding", "delegate_chat",
	}
	for _, name := range coreNames {
		found := false
		for _, ad := range agent.surface.Adapters() {
			if ad.Name() == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("core adapter %q not found", name)
		}
	}
}
