package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"codeactor/internal/llm"
	"codeactor/internal/tools"
	"codeactor/internal/globalctx"
	"codeactor/internal/memory"
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

// ─── Test Helpers ────────────────────────────────────────────────────────────

func newTestGlobalCtx(workDir string) *globalctx.GlobalCtx {
	return &globalctx.GlobalCtx{
		ProjectPath:  workDir,
		OS:           "linux",
		Arch:         "amd64",
		SpeakLang:    "Chinese",
		CodebaseURL:  "http://127.0.0.1:12800",
		FileOps:      tools.NewFileOperationsTool(workDir),
		SearchOps:    tools.NewSearchOperationsTool(workDir),
		SysOps:       tools.NewSystemOperationsTool(workDir),
		ReplaceTool:  tools.NewReplaceBlockTool(workDir),
		ThinkingTool: tools.NewThinkingTool(),
		FlowOps:      tools.NewFlowControlTool(workDir),
		RepoOps:      tools.NewRepoOperationsTool("http://127.0.0.1:12800", workDir),
	}
}

// newTestConductorAgent creates a ConductorAgent with real GlobalCtx but nil sub-agents.
// Tests that don't invoke delegate tools can use this safely.
func newTestConductorAgent(t *testing.T, workDir string) *ConductorAgent {
	t.Helper()
	gctx := newTestGlobalCtx(workDir)
	engine := &mockEngine{}
	return NewConductorAgent(gctx, engine, nil, nil, nil, nil, nil, 10, nil, 3, nil, nil)
}

// makeMetaOutput builds a valid Meta-Agent JSON output string.
func makeMetaOutput(agentName, systemPrompt string, toolsUsed []string) string {
	obj := map[string]interface{}{
		"thinking":     "Designing agent for the task.",
		"agent_name":   agentName,
		"agent_design": systemPrompt,
		"tools_used":   toolsUsed,
			"task_for_agent": "Clean task for the agent to execute.",
	}
	b, _ := json.MarshalIndent(obj, "", "  ")
	return string(b)
}

// ─── extractJSONObject Tests ──────────────────────────────────────────────

func TestExtractJSONObject_PureJSON(t *testing.T) {
	input := `{"thinking": "test", "agent_name": "Test"}`
	got := extractJSONObject(input)
	if got != input {
		t.Errorf("extractJSONObject = %q, want %q", got, input)
	}
}

func TestExtractJSONObject_MarkdownFence(t *testing.T) {
	input := "```json\n{\"key\": \"value\"}\n```"
	expected := `{"key": "value"}`
	got := extractJSONObject(input)
	if got != expected {
		t.Errorf("extractJSONObject = %q, want %q", got, expected)
	}
}

func TestExtractJSONObject_SurroundingText(t *testing.T) {
	input := `Here's the output: {"key": "value"} with some trailing text.`
	expected := `{"key": "value"}`
	got := extractJSONObject(input)
	if got != expected {
		t.Errorf("extractJSONObject = %q, want %q", got, expected)
	}
}

func TestExtractJSONObject_NestedBraces(t *testing.T) {
	input := `{"key": {"nested": true}, "list": [1,2,3]}`
	got := extractJSONObject(input)
	if got != input {
		t.Errorf("extractJSONObject = %q, want %q", got, input)
	}
}

func TestExtractJSONObject_NoBraces(t *testing.T) {
	got := extractJSONObject("Just plain text without any braces.")
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestExtractJSONObject_MetaOutput(t *testing.T) {
output := makeMetaOutput("Security Auditor", "You are a security auditor.", []string{"read_file"})
	got := extractJSONObject(output)
	if got == "" {
		t.Fatal("extractJSONObject returned empty for valid Meta-Agent output")
	}
	var result metaAgentResult
	if err := json.Unmarshal([]byte(got), &result); err != nil {
		t.Fatalf("extracted JSON is not valid: %v\nraw: %s", err, got)
	}
	if result.AgentName != "Security Auditor" {
		t.Errorf("agent_name = %q, want 'Security Auditor'", result.AgentName)
	}
}

// ─── toSnakeCase Tests ──────────────────────────────────────────────────────

func TestToSnakeCase(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Security Auditor", "security_auditor"},
		{"Data-Export Agent", "data_export_agent"},
		{"SQL Injection Scanner", "sql_injection_scanner"},
		{"Simple", "simple"},
		{"", "custom_agent"},
		{"!!!Special!!!", "special"},
		{"UPPER CASE", "upper_case"},
		{"Multi   Space   Agent", "multi_space_agent"},
		{"_Leading_Trailing_", "leading_trailing"},
		{"Test__Double__Underscore", "test_double_underscore"},
		{"agent123", "agent123"},
		{"   ", "custom_agent"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := toSnakeCase(tc.input)
			if got != tc.expected {
				t.Errorf("toSnakeCase(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

// ─── parseMetaAgentOutput Tests ──────────────────────────────────────────────

func TestParseMetaAgentOutput_Valid(t *testing.T) {
	output := makeMetaOutput("Security Auditor", "You are a security auditor.", []string{"read_file", "search_by_regex"})

	sysPrompt, result, err := parseMetaAgentOutput(output)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sysPrompt != "You are a security auditor." {
		t.Errorf("system prompt = %q, want %q", sysPrompt, "You are a security auditor.")
	}
	if result.AgentName != "Security Auditor" {
		t.Errorf("agent name = %q, want %q", result.AgentName, "Security Auditor")
	}
	if len(result.ToolsUsed) != 2 {
		t.Errorf("tools count = %d, want 2", len(result.ToolsUsed))
	}

}
func TestParseMetaAgentOutput_MissingJSON(t *testing.T) {
	output := "Just some plain text without JSON."
	_, _, err := parseMetaAgentOutput(output)
	if err == nil {
		t.Fatal("expected error for missing JSON object")
	}
}

func TestParseMetaAgentOutput_InvalidJSON(t *testing.T) {
	output := `{"thinking": "test", "agent_name": "Test", "agent_design": "prompt", "tools_used": ["read_file"], "result": {not valid json}}`
	_, _, err := parseMetaAgentOutput(output)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParseMetaAgentOutput_NoAgentDesign(t *testing.T) {
	// agent_design is now required — missing it should cause an error
	output := `{"thinking": "designing...", "agent_name": "Test", "tools_used": ["read_file"], "result": {"key": "value"}}`
	_, _, err := parseMetaAgentOutput(output)
	if err == nil {
		t.Fatal("expected error when agent_design is missing")
	}
}

func TestParseMetaAgentOutput_EmptyAgentName(t *testing.T) {
	// agent_name is now required — empty should cause an error
	output := `{"thinking": "test", "agent_name": "", "agent_design": "Some prompt", "tools_used": [], "result": {}}`
	_, _, err := parseMetaAgentOutput(output)
	if err == nil {
		t.Fatal("expected error when agent_name is empty")
	}
}
// ─── getToolFunc Tests ──────────────────────────────────────────────────────

func TestGetToolFunc_KnownTools(t *testing.T) {
	workDir := t.TempDir()
	agent := newTestConductorAgent(t, workDir)

	knownTools := []string{
		"read_file", "search_replace_in_file", "create_file", "run_bash",
		"search_by_regex", "delete_file", "rename_file", "list_dir",
		"print_dir_tree", "semantic_search", "query_code_skeleton",
		"query_code_snippet", "thinking", "micro_agent", "agent_exit",
	}

	for _, name := range knownTools {
		t.Run(name, func(t *testing.T) {
			fn := agent.getToolFunc(name)
			if fn == nil {
				t.Errorf("getToolFunc(%q) returned nil for known tool", name)
			}
		})
	}
}

func TestGetToolFunc_UnknownTool(t *testing.T) {
	workDir := t.TempDir()
	agent := newTestConductorAgent(t, workDir)

	fn := agent.getToolFunc("nonexistent_tool_xyz")
	if fn != nil {
		t.Error("getToolFunc should return nil for unknown tool")
	}
}

// ─── registerCustomAgent Tests ──────────────────────────────────────────────

func TestRegisterCustomAgent_Success(t *testing.T) {
	workDir := t.TempDir()
	agent := newTestConductorAgent(t, workDir)

	initialAdapterCount := len(agent.Adapters)

	ca := &CustomAgent{
		Name:         "security_auditor",
		DisplayName:  "Security Auditor",
		SystemPrompt: "You are a security auditor.",
		ToolsUsed:    []string{"read_file", "search_by_regex", "thinking"},
		Description:  "Audits code for security vulnerabilities.",
	}
	agent.registerCustomAgent(ca)

	// Verify map entry
	stored, ok := agent.customAgents["delegate_security_auditor"]
	if !ok {
		t.Fatal("custom agent not found in map")
	}
	if stored.DisplayName != "Security Auditor" {
		t.Errorf("display name = %q, want 'Security Auditor'", stored.DisplayName)
	}

	// Verify adapter was added
	if len(agent.Adapters) != initialAdapterCount+1 {
		t.Errorf("adapter count = %d, want %d", len(agent.Adapters), initialAdapterCount+1)
	}

	// Verify the new delegate tool exists
	found := false
	for _, ad := range agent.Adapters {
		if ad.Name() == "delegate_security_auditor" {
			found = true
			break
		}
	}
	if !found {
		t.Error("delegate_security_auditor adapter not found in Adapters")
	}
}

func TestRegisterCustomAgent_DuplicateRegistration(t *testing.T) {
	workDir := t.TempDir()
	agent := newTestConductorAgent(t, workDir)

	ca := &CustomAgent{
		Name:         "test_agent",
		DisplayName:  "Test Agent",
		SystemPrompt: "You are a test agent.",
		ToolsUsed:    []string{"read_file"},
		Description:  "Test agent.",
	}

	agent.registerCustomAgent(ca)
	countAfterFirst := len(agent.Adapters)

	// Register the same agent again
	agent.registerCustomAgent(ca)
	if len(agent.Adapters) != countAfterFirst {
		t.Errorf("duplicate registration should not add new adapters, got %d, want %d", len(agent.Adapters), countAfterFirst)
	}
}

func TestRegisterCustomAgent_UnknownToolsIgnored(t *testing.T) {
	workDir := t.TempDir()
	agent := newTestConductorAgent(t, workDir)

	ca := &CustomAgent{
		Name:         "partial_agent",
		DisplayName:  "Partial Agent",
		SystemPrompt: "You are a partial agent.",
		ToolsUsed:    []string{"read_file", "nonexistent_tool", "search_by_regex"},
		Description:  "Agent with some unknown tools.",
	}

	// Should not panic; unknown tools are skipped
	agent.registerCustomAgent(ca)

	stored := agent.customAgents["delegate_partial_agent"]
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

	// Build conductor with mocked LLM
	conductor := NewConductorAgent(gctx, customEngine, nil, nil, nil, nil, nil, 10, nil, 3, nil, nil)

	ca := &CustomAgent{
		Name:         "test_executor",
		DisplayName:  "Test Executor",
		SystemPrompt: "You are a test executor. Complete the task.",
		ToolsUsed:    []string{"read_file", "thinking", "agent_exit"},
		Description:  "Executes test tasks.",
	}
	conductor.registerCustomAgent(ca)

	// Find the newly created delegate tool
	var delegateTool *tools.Adapter
	for _, ad := range conductor.Adapters {
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

	conductor := NewConductorAgent(gctx, customEngine, nil, nil, nil, nil, nil, 10, nil, 3, nil, nil)

	ca := &CustomAgent{
		Name:         "finisher",
		DisplayName:  "Finisher Agent",
		SystemPrompt: "You exit immediately.",
		ToolsUsed:    []string{"agent_exit"},
		Description:  "Always exits.",
	}
	conductor.registerCustomAgent(ca)

	var delegateTool *tools.Adapter
	for _, ad := range conductor.Adapters {
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

func TestSystemPrompt_NoCustomAgents(t *testing.T) {
	workDir := t.TempDir()
	agent := newTestConductorAgent(t, workDir)

	// Build system prompt like Run() does
	systemPrompt := agent.GlobalCtx.FormatPrompt(conductorPrompt)
	if len(agent.customAgents) > 0 {
		systemPrompt += "\n\n### Custom Agents..."
	}

	// The conductor prompt references ### Custom Agents in the Meta-Agent section,
	// so we check that the actual markdown block (with agent listings) is NOT present.
	// The key difference: the listing block starts with "\n\n### Custom Agents\nThe following specialized"
	if strings.Contains(systemPrompt, "\n\n### Custom Agents\nThe following specialized agents") {
		t.Error("system prompt should NOT contain the ### Custom Agents listing block when no custom agents registered")
	}
}

func TestSystemPrompt_WithCustomAgents(t *testing.T) {
	workDir := t.TempDir()
	agent := newTestConductorAgent(t, workDir)

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

	// Build system prompt like Run() does
	systemPrompt := agent.GlobalCtx.FormatPrompt(conductorPrompt)
	if len(agent.customAgents) > 0 {
		systemPrompt += "\n\n### Custom Agents\nThe following specialized agents have been designed by Meta-Agent and are permanently available for delegation:\n\n"
		for _, ca := range agent.customAgents {
			systemPrompt += fmt.Sprintf("- **%s** (`delegate_%s`): %s\n", ca.DisplayName, ca.Name, ca.Description)
		}
		systemPrompt += "\nUse these agents via their delegate tools for tasks matching their specializations.\n\n"
	}

	if !strings.Contains(systemPrompt, "### Custom Agents") {
		t.Error("system prompt SHOULD contain ### Custom Agents block")
	}
	if !strings.Contains(systemPrompt, "Security Auditor") {
		t.Error("system prompt should mention Security Auditor")
	}
	if !strings.Contains(systemPrompt, "delegate_security_auditor") {
		t.Error("system prompt should mention delegate_security_auditor")
	}
	if !strings.Contains(systemPrompt, "Data Migrator") {
		t.Error("system prompt should mention Data Migrator")
	}
	if !strings.Contains(systemPrompt, "delegate_data_migrator") {
		t.Error("system prompt should mention delegate_data_migrator")
	}
}

// ─── delegate_meta Full Integration Tests ────────────────────────────────────

// metaAgentMockLLM returns a Meta-Agent-style response based on the input.
func metaAgentMockLLM(responseContent string) *mockEngine {
	return &mockEngine{
		generateContent: func(ctx context.Context, messages []llm.Message, tools []llm.ToolDef, opts *llm.CallOptions) (*llm.Response, error) {
			return &llm.Response{
				Choices: []llm.Choice{{Content: responseContent}},
			}, nil
		},
	}
}

func TestDelegateMeta_DynamicRegistration(t *testing.T) {
	workDir := t.TempDir()
	gctx := newTestGlobalCtx(workDir)

	// Meta-Agent output that designs a Security Auditor
	metaOutput := makeMetaOutput(
		"Security Auditor",
		"You are a Security Auditor. Review code for vulnerabilities. Use tools to search and read files.",
		[]string{"search_by_regex", "read_file", "thinking", "agent_exit"},
	)

	// MetaAgent that returns the pre-defined output (single LLM call, no tool calls)
	metaAgent := NewMetaAgent(gctx, metaAgentMockLLM(metaOutput))

	// ConductorAgent
	conductor := NewConductorAgent(gctx, &mockEngine{}, nil, nil, nil, metaAgent, nil, 10, nil, 3, nil, nil)
	initialAdapterCount := len(conductor.Adapters)

	// Find and call delegate_meta tool
	var delegateMeta *tools.Adapter
	for _, ad := range conductor.Adapters {
		if ad.Name() == "delegate_meta" {
			delegateMeta = ad
			break
		}
	}
	if delegateMeta == nil {
		t.Fatal("delegate_meta tool not found in Conductor Adapters")
	}

	result, err := delegateMeta.Call(context.Background(), `{"task": "Perform a security audit of the project"}`)
	if err != nil {
		t.Fatalf("delegate_meta call failed: %v", err)
	}

	// Verify custom agent was registered
	customAgent, ok := conductor.customAgents["delegate_security_auditor"]
	if !ok {
		t.Fatal("Security Auditor was not registered in customAgents")
	}
	if customAgent.DisplayName != "Security Auditor" {
		t.Errorf("display name = %q, want 'Security Auditor'", customAgent.DisplayName)
	}
	if customAgent.SystemPrompt != "You are a Security Auditor. Review code for vulnerabilities. Use tools to search and read files." {
		t.Errorf("system prompt mismatch")
	}
	if len(customAgent.ToolsUsed) != 4 {
		t.Errorf("tools count = %d, want 4", len(customAgent.ToolsUsed))
	}

	// Verify adapter was added
	if len(conductor.Adapters) != initialAdapterCount+1 {
		t.Errorf("adapter count = %d, want %d", len(conductor.Adapters), initialAdapterCount+1)
	}

	// Verify delegate tool exists for the new agent
	found := false
	for _, ad := range conductor.Adapters {
		if ad.Name() == "delegate_security_auditor" {
			found = true
			break
		}
	}
	if !found {
		t.Error("delegate_security_auditor adapter not found after registration")
	}

	// Verify result message contains registration info
	resultStr := result
	if !strings.Contains(resultStr, "New Agent Registered") {
		t.Error("result should contain 'New Agent Registered' notification")
	}
	if !strings.Contains(resultStr, "delegate_security_auditor") {
		t.Error("result should mention delegate_security_auditor")
	}
	if !strings.Contains(resultStr, "Security Auditor") {
		t.Error("result should mention Security Auditor")
	}
}

func TestDelegateMeta_DuplicateRegistrationPrevented(t *testing.T) {
	workDir := t.TempDir()
	gctx := newTestGlobalCtx(workDir)

	metaOutput := makeMetaOutput(
		"Tester",
		"You are a tester.",
		[]string{"read_file"},
	)

	metaAgent := NewMetaAgent(gctx, metaAgentMockLLM(metaOutput))
	conductor := NewConductorAgent(gctx, &mockEngine{}, nil, nil, nil, metaAgent, nil, 10, nil, 3, nil, nil)

	// Call delegate_meta twice with the same agent design
	var delegateMeta *tools.Adapter
	for _, ad := range conductor.Adapters {
		if ad.Name() == "delegate_meta" {
			delegateMeta = ad
			break
		}
	}

	// First call: registers the agent
	_, err := delegateMeta.Call(context.Background(), `{"task": "Test first"}`)
	if err != nil {
		t.Fatalf("first delegate_meta call failed: %v", err)
	}
	countAfterFirst := len(conductor.Adapters)
	mapCountAfterFirst := len(conductor.customAgents)

	// Second call: should NOT register duplicate
	_, err = delegateMeta.Call(context.Background(), `{"task": "Test second"}`)
	if err != nil {
		t.Fatalf("second delegate_meta call failed: %v", err)
	}

	if len(conductor.Adapters) != countAfterFirst {
		t.Errorf("duplicate registration added adapters: %d → %d", countAfterFirst, len(conductor.Adapters))
	}
	if len(conductor.customAgents) != mapCountAfterFirst {
		t.Errorf("duplicate registration added map entries: %d → %d", mapCountAfterFirst, len(conductor.customAgents))
	}
}

func TestDelegateMeta_ParseFailure_ReturnsRawOutput(t *testing.T) {
	workDir := t.TempDir()
	gctx := newTestGlobalCtx(workDir)

	// Meta-Agent returns malformed output (no execution_result block)
	malformedOutput := "Just some plain text without structured blocks."
	metaAgent := NewMetaAgent(gctx, metaAgentMockLLM(malformedOutput))
	conductor := NewConductorAgent(gctx, &mockEngine{}, nil, nil, nil, metaAgent, nil, 10, nil, 3, nil, nil)

	var delegateMeta *tools.Adapter
	for _, ad := range conductor.Adapters {
		if ad.Name() == "delegate_meta" {
			delegateMeta = ad
			break
		}
	}

	initialCount := len(conductor.Adapters)
	result, err := delegateMeta.Call(context.Background(), `{"task": "Test"}`)
	if err != nil {
		t.Fatalf("delegate_meta call failed: %v", err)
	}

	// The adapter.Call() JSON-encodes the return value, so unmarshal to get the raw string
	var actualOutput string
	if err := json.Unmarshal([]byte(result), &actualOutput); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	// Should return raw output directly
	if actualOutput != malformedOutput {
		t.Errorf("expected raw output on parse failure, got: %s", actualOutput)
	}
	// Should NOT register any agent
	if len(conductor.Adapters) != initialCount {
		t.Errorf("no agent should be registered on parse failure")
	}
}

func TestDelegateMeta_EmptyAgentName_NoRegistration(t *testing.T) {
	workDir := t.TempDir()
	gctx := newTestGlobalCtx(workDir)

	// Meta-Agent output with empty agent name
	metaOutput := makeMetaOutput(
		"", // empty agent name
		"Some system prompt",
		[]string{"read_file"},
	)
	metaAgent := NewMetaAgent(gctx, metaAgentMockLLM(metaOutput))
	conductor := NewConductorAgent(gctx, &mockEngine{}, nil, nil, nil, metaAgent, nil, 10, nil, 3, nil, nil)

	var delegateMeta *tools.Adapter
	for _, ad := range conductor.Adapters {
		if ad.Name() == "delegate_meta" {
			delegateMeta = ad
			break
		}
	}

	initialCount := len(conductor.Adapters)
	_, err := delegateMeta.Call(context.Background(), `{"task": "Test"}`)
	if err != nil {
		t.Fatalf("delegate_meta call failed: %v", err)
	}

	// No agent should be registered when name is empty
	if len(conductor.Adapters) != initialCount {
		t.Errorf("no agent should be registered with empty name, adapters: %d → %d", initialCount, len(conductor.Adapters))
	}
}

func TestDelegateMeta_NoAgentDesign_NoRegistration(t *testing.T) {
	workDir := t.TempDir()
	gctx := newTestGlobalCtx(workDir)

	// Meta-Agent output with missing agent_design field (all retries return same malformed output)
	output := `{"thinking": "designing...", "agent_name": "Test Agent", "tools_used": ["read_file"], "result": {"key": "value"}}`

	metaAgent := NewMetaAgent(gctx, metaAgentMockLLM(output))
	conductor := NewConductorAgent(gctx, &mockEngine{}, nil, nil, nil, metaAgent, nil, 10, nil, 3, nil, nil)

	var delegateMeta *tools.Adapter
	for _, ad := range conductor.Adapters {
		if ad.Name() == "delegate_meta" {
			delegateMeta = ad
			break
		}
	}

	initialCount := len(conductor.Adapters)
	_, err := delegateMeta.Call(context.Background(), `{"task": "Test"}`)
	if err != nil {
		t.Fatalf("delegate_meta call failed: %v", err)
	}

	// No agent_design field → parse fails on all retries → no registration
	if len(conductor.Adapters) != initialCount {
		t.Errorf("no agent should be registered without agent_design field, adapters: %d → %d", initialCount, len(conductor.Adapters))
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

	llmMsg := convertMemoryMessageToLLMSMessage(msg)

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

	llmMsg := convertMemoryMessageToLLMSMessage(msg)

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

// ─── ToolDefMap Tests ───────────────────────────────────────────────────────

func TestToolDefMap_Populated(t *testing.T) {
	workDir := t.TempDir()
	agent := newTestConductorAgent(t, workDir)

	// toolDefMap should contain all tools from tools.json
	expectedTools := []string{
		"read_file", "search_replace_in_file", "create_file", "run_bash",
		"search_by_regex", "delete_file", "rename_file", "list_dir",
		"print_dir_tree", "semantic_search", "query_code_skeleton",
		"query_code_snippet", "thinking", "micro_agent", "agent_exit",
	}

	for _, name := range expectedTools {
		if _, ok := agent.toolDefMap[name]; !ok {
			t.Errorf("toolDefMap missing expected tool: %s", name)
		}
	}
}

// ─── Adapter Count Test ─────────────────────────────────────────────────────

func TestConductorAgent_InitialAdapters(t *testing.T) {
	workDir := t.TempDir()
	agent := newTestConductorAgent(t, workDir)

	// Should have: agent_exit + search_by_regex + list_dir + read_file + print_dir_tree
	// + delegate_repo + delegate_coding + delegate_chat + delegate_meta
	// = 9 adapters minimum
	if len(agent.Adapters) < 9 {
		t.Errorf("expected at least 9 initial adapters, got %d", len(agent.Adapters))
	}

	// Verify all core adapters are present
	coreNames := []string{
		"agent_exit", "search_by_regex", "list_dir", "read_file", "print_dir_tree",
		"delegate_repo", "delegate_coding", "delegate_chat", "delegate_meta",
	}
	for _, name := range coreNames {
		found := false
		for _, ad := range agent.Adapters {
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
