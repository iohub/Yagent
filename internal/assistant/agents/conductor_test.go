package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"codeactor/internal/assistant/tools"
	"codeactor/internal/globalctx"
	"codeactor/internal/memory"

	"github.com/tmc/langchaingo/llms"
)

// ─── Mock LLM ────────────────────────────────────────────────────────────────

type mockLLM struct {
	generateContent func(ctx context.Context, messages []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error)
	call            func(ctx context.Context, prompt string, options ...llms.CallOption) (string, error)
}

func (m *mockLLM) GenerateContent(ctx context.Context, messages []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
	if m.generateContent != nil {
		return m.generateContent(ctx, messages, options...)
	}
	return &llms.ContentResponse{Choices: []*llms.ContentChoice{{Content: ""}}}, nil
}

func (m *mockLLM) Call(ctx context.Context, prompt string, options ...llms.CallOption) (string, error) {
	if m.call != nil {
		return m.call(ctx, prompt, options...)
	}
	return "", nil
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
	return NewConductorAgent(gctx, &mockLLM{}, nil, nil, nil, nil, 10)
}

// makeMetaOutput builds a valid Meta-Agent output string.
func makeMetaOutput(agentName, systemPrompt string, toolsUsed []string, result map[string]interface{}) string {
	resultJSON, _ := json.Marshal(result)
	return fmt.Sprintf(`<thinking>Designing agent for the task.</thinking>
<agent_design>%s</agent_design>
<execution_result>
{
  "agent_name": "%s",
  "tools_used": %s,
  "result": %s
}
</execution_result>`, systemPrompt, agentName, toJSON(toolsUsed), string(resultJSON))
}

func toJSON(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
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
	output := makeMetaOutput("Security Auditor", "You are a security auditor.", []string{"read_file", "search_by_regex"}, map[string]interface{}{
		"findings": "no issues found",
	})

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
	if result.Result["findings"] != "no issues found" {
		t.Errorf("result findings = %v, want 'no issues found'", result.Result["findings"])
	}
}

func TestParseMetaAgentOutput_MissingExecutionResult(t *testing.T) {
	output := "<agent_design>Some prompt</agent_design>\nNo result block here."
	_, _, err := parseMetaAgentOutput(output)
	if err == nil {
		t.Fatal("expected error for missing execution_result block")
	}
}

func TestParseMetaAgentOutput_InvalidJSON(t *testing.T) {
	output := `<agent_design>Some prompt</agent_design>
<execution_result>
{not valid json}
</execution_result>`
	_, _, err := parseMetaAgentOutput(output)
	if err == nil {
		t.Fatal("expected error for invalid JSON in execution_result")
	}
}

func TestParseMetaAgentOutput_NoAgentDesign(t *testing.T) {
	output := `<execution_result>
{
  "agent_name": "Test",
  "tools_used": ["read_file"],
  "result": {"key": "value"}
}
</execution_result>`

	sysPrompt, result, err := parseMetaAgentOutput(output)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sysPrompt != "" {
		t.Errorf("expected empty system prompt, got %q", sysPrompt)
	}
	if result.AgentName != "Test" {
		t.Errorf("agent name = %q, want 'Test'", result.AgentName)
	}
}

func TestParseMetaAgentOutput_EmptyResult(t *testing.T) {
	output := `<agent_design></agent_design>
<execution_result>
{
  "agent_name": "",
  "tools_used": [],
  "result": {}
}
</execution_result>`

	sysPrompt, result, err := parseMetaAgentOutput(output)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sysPrompt != "" {
		t.Errorf("expected empty system prompt, got %q", sysPrompt)
	}
	if result.AgentName != "" {
		t.Errorf("expected empty agent name, got %q", result.AgentName)
	}
}

// ─── fallbackParseMetaAgentOutput Tests ────────────────────────────────────

func TestFallbackParseMetaAgentOutput_WithAgentHeading(t *testing.T) {
	output := "## file-stats Agent 执行完成\n\n### 设计说明\n我为本次任务设计了一个专用的 **file-stats** agent。\n\n使用的工具包括: read_file, search_by_regex, finish.\n统计了文件行数和 import 数量。"

	agentName, description, toolsUsed, result := fallbackParseMetaAgentOutput(output)
	if agentName == "" {
		t.Error("expected agent name to be extracted from heading")
	}
	t.Logf("Extracted agent name: %q", agentName)
	t.Logf("Description: %q", description)
	t.Logf("Tools found: %v", toolsUsed)
	if result["raw_output"] == nil {
		t.Error("expected raw_output in result")
	}
	if result["extraction_method"] != "heuristic" {
		t.Error("expected extraction_method to be 'heuristic'")
	}
}

func TestFallbackParseMetaAgentOutput_WithBoldAgentName(t *testing.T) {
	output := "我创建了一个 **Security Auditor** agent。\n它使用 read_file 和 search_by_regex 来分析代码。\n另外还需要 thinking 工具进行思考。"

	agentName, _, toolsUsed, _ := fallbackParseMetaAgentOutput(output)
	if agentName != "Security Auditor" {
		t.Errorf("expected 'Security Auditor', got %q", agentName)
	}
	if len(toolsUsed) < 3 {
		t.Errorf("expected at least 3 tools, got %d: %v", len(toolsUsed), toolsUsed)
	}
}

func TestFallbackParseMetaAgentOutput_WithAgentPrefix(t *testing.T) {
	output := "Agent Name: Data Migration Planner\nTools: create_file, run_terminal_cmd\n将会执行数据库迁移计划。"

	agentName, _, _, _ := fallbackParseMetaAgentOutput(output)
	if agentName == "" {
		t.Error("expected agent name to be extracted from 'Agent Name:' pattern")
	}
	t.Logf("Extracted: %q", agentName)
}

func TestFallbackParseMetaAgentOutput_WithJSONAgentName(t *testing.T) {
	output := "{\"agent_name\": \"CodeReviewBot\", \"tools_used\": [\"read_file\", \"search_by_regex\"]}\n其他文本内容..."

	agentName, _, toolsUsed, _ := fallbackParseMetaAgentOutput(output)
	if agentName != "CodeReviewBot" {
		t.Errorf("expected 'CodeReviewBot', got %q", agentName)
	}
	if len(toolsUsed) < 2 {
		t.Errorf("expected at least 2 tools, got %d", len(toolsUsed))
	}
}

func TestFallbackParseMetaAgentOutput_EmptyOutput(t *testing.T) {
	agentName, _, toolsUsed, _ := fallbackParseMetaAgentOutput("")
	if agentName != "" {
		t.Errorf("expected empty agent name for empty output, got %q", agentName)
	}
	if len(toolsUsed) != 0 {
		t.Errorf("expected no tools for empty output, got %v", toolsUsed)
	}
}

func TestFallbackParseMetaAgentOutput_NoAgentNameButTools(t *testing.T) {
	output := "执行了以下操作：\n1. 使用 read_file 读取文件\n2. 使用 search_by_regex 搜索\n3. 使用 finish 完成任务"

	agentName, _, toolsUsed, _ := fallbackParseMetaAgentOutput(output)
	if agentName != "" {
		t.Logf("Unexpectedly found agent name: %q", agentName)
	}
	if len(toolsUsed) != 3 {
		t.Errorf("expected 3 tools, got %d: %v", len(toolsUsed), toolsUsed)
	}
}

func TestFallbackParseMetaAgentOutput_ChineseAgentName(t *testing.T) {
	output := "我设计了一个 **代码安全审计 Agent**。\n使用的工具包括 read_file, search_by_regex, create_file。"

	agentName, _, toolsUsed, _ := fallbackParseMetaAgentOutput(output)
	if agentName == "" {
		t.Error("expected Chinese agent name to be extracted")
	}
	t.Logf("Extracted: %q", agentName)
	if len(toolsUsed) < 3 {
		t.Errorf("expected at least 3 tools, got %d", len(toolsUsed))
	}
}

func TestFallbackParseMetaAgentOutput_DesignedAgentPattern(t *testing.T) {
	output := "I have designed a specialized **Database Schema Generator** agent for this task.\nThe agent uses create_file, read_file, and thinking tools."

	agentName, _, toolsUsed, _ := fallbackParseMetaAgentOutput(output)
	if agentName == "" {
		t.Error("expected agent name from 'designed a **X** agent' pattern")
	}
	t.Logf("Extracted: %q, tools: %v", agentName, toolsUsed)
}

func TestFallbackParseMetaAgentOutput_WhitespaceOnly(t *testing.T) {
	agentName, _, toolsUsed, _ := fallbackParseMetaAgentOutput("   \n\t  ")
	if agentName != "" {
		t.Errorf("expected empty for whitespace-only, got %q", agentName)
	}
	if len(toolsUsed) != 0 {
		t.Errorf("expected no tools for whitespace, got %v", toolsUsed)
	}
}

// ─── extractAgentName Tests ─────────────────────────────────────────────────

func TestExtractAgentName_Heading(t *testing.T) {
	name := extractAgentName("### Security Auditor Agent\n\nDescription here.")
	if name == "" {
		t.Error("expected to extract agent name from heading")
	}
	t.Logf("Extracted: %q", name)
}

func TestExtractAgentName_IgnoresFilePath(t *testing.T) {
	name := extractAgentName("internal/assistant/agents/conductor.go")
	if name != "" {
		t.Errorf("should not extract file path as agent name, got %q", name)
	}
}

func TestExtractAgentName_IgnoresGoKeywords(t *testing.T) {
	name := extractAgentName("package agents\nimport \"fmt\"")
	if name != "" {
		t.Errorf("should not extract Go keywords, got %q", name)
	}
}

// ─── extractToolsUsed Tests ─────────────────────────────────────────────────

func TestExtractToolsUsed_FindsTools(t *testing.T) {
	output := "使用 read_file 读取文件，通过 search_by_regex 搜索模式，最后用 create_file 写结果。"
	tools := extractToolsUsed(output)
	if len(tools) < 3 {
		t.Errorf("expected at least 3 tools, got %d: %v", len(tools), tools)
	}
	hasTool := func(name string) bool {
		for _, t := range tools {
			if t == name {
				return true
			}
		}
		return false
	}
	if !hasTool("read_file") || !hasTool("search_by_regex") || !hasTool("create_file") {
		t.Error("missing expected tools")
	}
}

func TestExtractToolsUsed_EmptyOutput(t *testing.T) {
	tools := extractToolsUsed("")
	if len(tools) != 0 {
		t.Errorf("expected no tools for empty output, got %v", tools)
	}
}


// ─── getToolFunc Tests ──────────────────────────────────────────────────────

func TestGetToolFunc_KnownTools(t *testing.T) {
	workDir := t.TempDir()
	agent := newTestConductorAgent(t, workDir)

	knownTools := []string{
		"read_file", "search_replace_in_file", "create_file", "run_terminal_cmd",
		"search_by_regex", "delete_file", "rename_file", "list_dir",
		"print_dir_tree", "semantic_search", "query_code_skeleton",
		"query_code_snippet", "thinking", "finish",
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
	customLLM := &mockLLM{
		generateContent: func(ctx context.Context, messages []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
			return &llms.ContentResponse{
				Choices: []*llms.ContentChoice{
					{Content: "Custom agent task completed."},
				},
			}, nil
		},
	}

	// Build conductor with mocked LLM
	conductor := NewConductorAgent(gctx, customLLM, nil, nil, nil, nil, 10)

	ca := &CustomAgent{
		Name:         "test_executor",
		DisplayName:  "Test Executor",
		SystemPrompt: "You are a test executor. Complete the task.",
		ToolsUsed:    []string{"read_file", "thinking", "finish"},
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
	customLLM := &mockLLM{
		generateContent: func(ctx context.Context, messages []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
			callCount++
			if callCount == 1 {
				// First call: use finish to complete
				return &llms.ContentResponse{
					Choices: []*llms.ContentChoice{{
						Content: "Task is complete.",
						ToolCalls: []llms.ToolCall{{
							ID:   "call_finish",
							Type: "function",
							FunctionCall: &llms.FunctionCall{
								Name:      "finish",
								Arguments: `{"reason": "Test completed successfully"}`,
							},
						}},
					}},
				}, nil
			}
			return &llms.ContentResponse{
				Choices: []*llms.ContentChoice{{Content: "Should not reach here"}},
			}, nil
		},
	}

	conductor := NewConductorAgent(gctx, customLLM, nil, nil, nil, nil, 10)

	ca := &CustomAgent{
		Name:         "finisher",
		DisplayName:  "Finisher Agent",
		SystemPrompt: "You finish immediately.",
		ToolsUsed:    []string{"finish"},
		Description:  "Always finishes.",
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
		systemPrompt += "\n\n<custom_agents>..."
	}

	// The conductor prompt references <custom_agents> in the Meta-Agent section,
	// so we check that the actual XML block (with agent listings) is NOT present.
	// The key difference: the listing block starts with "\n\n<custom_agents>\nThe following specialized"
	if strings.Contains(systemPrompt, "\n\n<custom_agents>\nThe following specialized agents") {
		t.Error("system prompt should NOT contain the <custom_agents> listing block when no custom agents registered")
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
		ToolsUsed:    []string{"create_file", "run_terminal_cmd"},
		Description:  "Handles database migration planning.",
	})

	// Build system prompt like Run() does
	systemPrompt := agent.GlobalCtx.FormatPrompt(conductorPrompt)
	if len(agent.customAgents) > 0 {
		systemPrompt += "\n\n<custom_agents>\nThe following specialized agents have been designed by Meta-Agent and are permanently available for delegation:\n\n"
		for _, ca := range agent.customAgents {
			systemPrompt += fmt.Sprintf("- **%s** (`delegate_%s`): %s\n", ca.DisplayName, ca.Name, ca.Description)
		}
		systemPrompt += "\nUse these agents via their delegate tools for tasks matching their specializations.\n</custom_agents>\n"
	}

	if !strings.Contains(systemPrompt, "<custom_agents>") {
		t.Error("system prompt SHOULD contain <custom_agents> block")
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
func metaAgentMockLLM(responseContent string) *mockLLM {
	return &mockLLM{
		generateContent: func(ctx context.Context, messages []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
			return &llms.ContentResponse{
				Choices: []*llms.ContentChoice{{Content: responseContent}},
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
		[]string{"search_by_regex", "read_file", "thinking", "finish"},
		map[string]interface{}{
			"findings":      "No critical vulnerabilities found",
			"files_checked": "3",
		},
	)

	// MetaAgent that returns the pre-defined output (single LLM call, no tool calls)
	metaAgent := NewMetaAgent(gctx, metaAgentMockLLM(metaOutput), 5)

	// ConductorAgent
	conductor := NewConductorAgent(gctx, &mockLLM{}, nil, nil, nil, metaAgent, 10)
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
		map[string]interface{}{"status": "done"},
	)

	metaAgent := NewMetaAgent(gctx, metaAgentMockLLM(metaOutput), 5)
	conductor := NewConductorAgent(gctx, &mockLLM{}, nil, nil, nil, metaAgent, 10)

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
	metaAgent := NewMetaAgent(gctx, metaAgentMockLLM(malformedOutput), 5)
	conductor := NewConductorAgent(gctx, &mockLLM{}, nil, nil, nil, metaAgent, 10)

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

	// Should return gracefully formatted raw output with [Meta-Agent Raw Output] prefix
	if !strings.Contains(actualOutput, "[Meta-Agent Raw Output]") {
		t.Errorf("expected [Meta-Agent Raw Output] prefix, got: %s", actualOutput)
	}
	if !strings.Contains(actualOutput, malformedOutput) {
		t.Errorf("expected output to contain original text, got: %s", actualOutput)
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
		map[string]interface{}{"status": "done"},
	)
	metaAgent := NewMetaAgent(gctx, metaAgentMockLLM(metaOutput), 5)
	conductor := NewConductorAgent(gctx, &mockLLM{}, nil, nil, nil, metaAgent, 10)

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

	// Meta-Agent output with execution_result but NO agent_design block
	output := `<thinking>designing...</thinking>
<execution_result>
{
  "agent_name": "Test Agent",
  "tools_used": ["read_file"],
  "result": {"key": "value"}
}
</execution_result>`

	metaAgent := NewMetaAgent(gctx, metaAgentMockLLM(output), 5)
	conductor := NewConductorAgent(gctx, &mockLLM{}, nil, nil, nil, metaAgent, 10)

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

	// No agent_design means empty systemPrompt, which means no registration
	if len(conductor.Adapters) != initialCount {
		t.Errorf("no agent should be registered without agent_design block")
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

	if len(llmMsg.Parts) != 1 {
		t.Errorf("Expected 1 part, got %d", len(llmMsg.Parts))
	}

	part := llmMsg.Parts[0]
	_, ok := part.(llms.ToolCallResponse)
	if !ok {
		t.Errorf("Expected part to be ToolCallResponse, got %T", part)
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

	if len(llmMsg.Parts) != 2 {
		t.Errorf("Expected 2 parts (text + tool call), got %d", len(llmMsg.Parts))
	}

	// First part should be text (TextContent)
	if _, ok := llmMsg.Parts[0].(llms.TextContent); !ok {
		t.Errorf("Expected first part to be TextContent, got %T", llmMsg.Parts[0])
	}

	// Second part should be ToolCall
	if _, ok := llmMsg.Parts[1].(llms.ToolCall); !ok {
		t.Errorf("Expected second part to be ToolCall")
	}
}

// ─── ToolDefMap Tests ───────────────────────────────────────────────────────

func TestToolDefMap_Populated(t *testing.T) {
	workDir := t.TempDir()
	agent := newTestConductorAgent(t, workDir)

	// toolDefMap should contain all tools from tools.json
	expectedTools := []string{
		"read_file", "search_replace_in_file", "create_file", "run_terminal_cmd",
		"search_by_regex", "delete_file", "rename_file", "list_dir",
		"print_dir_tree", "semantic_search", "query_code_skeleton",
		"query_code_snippet", "thinking", "finish",
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

	// Should have: finish + search_by_regex + list_dir + read_file + print_dir_tree
	// + delegate_repo + delegate_coding + delegate_chat + delegate_meta
	// = 9 adapters minimum
	if len(agent.Adapters) < 9 {
		t.Errorf("expected at least 9 initial adapters, got %d", len(agent.Adapters))
	}

	// Verify all core adapters are present
	coreNames := []string{
		"finish", "search_by_regex", "list_dir", "read_file", "print_dir_tree",
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
