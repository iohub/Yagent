package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"codee/internal/assistant/tools"
	"codee/internal/util"

	"github.com/rs/zerolog/log"
	"github.com/tmc/langchaingo/llms"
)

// Tool represents a tool definition loaded from tools.json
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  ToolParameters `json:"parameters"`
}

// ToolParameters represents the parameters of a tool
type ToolParameters struct {
	Type       string                  `json:"type"`
	Properties map[string]ToolProperty `json:"properties"`
	Required   []string                `json:"required"`
}

// ToolProperty represents a property of a tool parameter
type ToolProperty struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

// ToolResult represents the result of a tool execution
type ToolResult struct {
	Success bool        `json:"success"`
	Result  interface{} `json:"result,omitempty"`
	Error   string      `json:"error,omitempty"`
}

// EnhancedToolManager 使用langchaingo原生function calling功能的工具管理器
type EnhancedToolManager struct {
	tools           map[string]*llms.FunctionDefinition
	publisher       *MessagePublisher
	allowedTools    map[string]bool // 允许使用的工具集合，如果为空则允许所有工具
	disallowedTools map[string]bool // 禁止使用的工具集合，优先级高于 allow
	workingDir      string
	assistant       *CodingAssistant // 对主助手的引用
	restrictedMode  bool             // 是否为受限模式（子代理模式）

	// 工具实例
	fileOps     *tools.FileOperationsTool
	systemOps   *tools.SystemOperationsTool
	searchOps   *tools.SearchOperationsTool
	flowControl *tools.FlowControlTool
}

// NewEnhancedToolManager 创建新的增强工具管理器，加载所有工具
func NewEnhancedToolManager() (*EnhancedToolManager, error) {
	return NewEnhancedToolManagerWithTools(nil, nil)
}

func NewEnhancedToolManagerWithTools(allowTools []string, disallowTools []string) (*EnhancedToolManager, error) {
	tm := &EnhancedToolManager{
		tools:           make(map[string]*llms.FunctionDefinition),
		restrictedMode:  false,
		allowedTools:    make(map[string]bool),
		disallowedTools: make(map[string]bool),
	}
	ctx := context.Background()

	if allowTools != nil {
		for _, name := range allowTools {
			tm.allowedTools[name] = true
		}
	}
	if disallowTools != nil {
		for _, name := range disallowTools {
			tm.disallowedTools[name] = true
		}
	}

	if err := tm.LoadTools(); err != nil {
		return nil, util.WrapError(ctx, err, "NewEnhancedToolManagerWithFilters::LoadTools")
	}

	return tm, nil
}

// SetWorkingDirectory 设置工作目录
func (tm *EnhancedToolManager) SetWorkingDirectory(dir string) {
	tm.workingDir = dir
	// 初始化工具实例
	tm.fileOps = tools.NewFileOperationsTool(dir)
	tm.systemOps = tools.NewSystemOperationsTool(dir)
	tm.searchOps = tools.NewSearchOperationsTool(dir)
	tm.flowControl = tools.NewFlowControlTool(dir)
}

// GetWorkingDirectory 获取当前工作目录
func (tm *EnhancedToolManager) GetWorkingDirectory() string {
	return tm.workingDir
}

// SetAssistant 设置对主助手的引用
func (tm *EnhancedToolManager) SetAssistant(assistant *CodingAssistant) {
	tm.assistant = assistant
	// 更新对主助手的引用
	tm.assistant = assistant
}

// SetRestrictedMode 设置受限模式（子代理模式）
func (tm *EnhancedToolManager) SetRestrictedMode(restricted bool) {
	tm.restrictedMode = restricted
}

// IsRestrictedMode 检查是否为受限模式
func (tm *EnhancedToolManager) IsRestrictedMode() bool {
	return tm.restrictedMode
}

func (tm *EnhancedToolManager) LoadTools() error {
	ctx := context.Background()
	var tools []Tool
	if err := json.Unmarshal([]byte(ToolsJSON), &tools); err != nil {
		return util.WrapError(ctx, err, "LoadTools::Unmarshal")
	}

	for i := range tools {
		// 如果命中禁止列表，直接跳过（优先级最高）
		if len(tm.disallowedTools) > 0 && tm.disallowedTools[tools[i].Name] {
			continue
		}
		// 如果指定了允许的工具集合，且当前工具不在集合中，则跳过
		if len(tm.allowedTools) > 0 && !tm.allowedTools[tools[i].Name] {
			continue
		}
		// 转换为langchaingo的FunctionDefinition格式
		functionDef := &llms.FunctionDefinition{
			Name:        tools[i].Name,
			Description: tools[i].Description,
			Parameters:  tm.convertParameters(tools[i].Parameters),
		}
		tm.tools[tools[i].Name] = functionDef
	}

	log.Info().Int("tools_count", len(tools)).Msg("Enhanced tool manager loaded tools")
	return nil
}

// convertParameters 将自定义参数格式转换为langchaingo格式
func (tm *EnhancedToolManager) convertParameters(params ToolParameters) map[string]interface{} {
	result := map[string]interface{}{
		"type":       params.Type,
		"properties": make(map[string]interface{}),
		"required":   params.Required,
	}

	for name, prop := range params.Properties {
		result["properties"].(map[string]interface{})[name] = map[string]interface{}{
			"type":        prop.Type,
			"description": prop.Description,
		}
	}

	return result
}

// GetTool 获取工具定义
func (tm *EnhancedToolManager) GetTool(name string) (*llms.FunctionDefinition, bool) {
	tool, exists := tm.tools[name]
	return tool, exists
}

// ListTools 列出所有可用工具
func (tm *EnhancedToolManager) ListTools() []string {
	var names []string
	for name := range tm.tools {
		names = append(names, name)
	}
	return names
}

// GetToolsForLLM 获取langchaingo格式的工具列表
func (tm *EnhancedToolManager) GetToolsForLLM() []llms.Tool {
	var tools []llms.Tool
	for _, functionDef := range tm.tools {
		tools = append(tools, llms.Tool{
			Type:     "function",
			Function: functionDef,
		})
	}
	return tools
}

// ExecuteFunctionCall 执行函数调用
func (tm *EnhancedToolManager) ExecuteFunctionCall(ctx context.Context, call llms.FunctionCall) (interface{}, error) {
	log.Info().
		Str("function_name", call.Name).
		Msg("Executing function call")

	// 解析参数
	var params map[string]interface{}
	if err := json.Unmarshal([]byte(call.Arguments), &params); err != nil {
		return nil, util.WrapError(ctx, err, "ExecuteFunctionCall::Unmarshal")
	}

	// 根据函数名执行相应的操作
	switch call.Name {
	case "read_file":
		return tm.fileOps.ExecuteReadFile(ctx, params)
	case "run_terminal_cmd":
		result, err := tm.systemOps.ExecuteRunTerminalCmd(ctx, params)
		if tm.publisher != nil {
			tm.publisher.Publish("tool_execution_complete", map[string]interface{}{
				"tool_name": call.Name,
				"success":   err == nil,
				"error":     err,
			})
		}
		return result, err
	case "list_dir":
		result, err := tm.systemOps.ExecuteListDir(ctx, params)
		return result, err
	case "grep_search":
		return tm.searchOps.ExecuteGrepSearch(ctx, params)
	case "edit_file":
		result, err := tm.fileOps.ExecuteEditFile(ctx, params)
		if tm.publisher != nil {
			tm.publisher.Publish("tool_execution_complete", map[string]interface{}{
				"tool_name": call.Name,
				"success":   err == nil,
				"error":     err,
			})
		}
		return result, err
	case "file_search":
		result, err := tm.searchOps.ExecuteFileSearch(ctx, params)
		return result, err
	case "delete_file":
		return tm.fileOps.ExecuteDeleteFile(ctx, params)
	case "create_file":
		return tm.fileOps.ExecuteCreateFile(ctx, params)
	case "finish":
		return tm.flowControl.ExecuteFinish(ctx, params)
	case "ask_user_for_help":
		return tm.flowControl.ExecuteAskUserForHelp(ctx, params)
	case "investigate_repo":
		result, err := tm.ExecuteInvestigateRepo(ctx, params)
		if err != nil {
			return nil, err
		}
		return result, nil
	case "planning":
		return tm.ExecuteArchitect(ctx, params)
	case "return_result_with_summary":
		return tm.ExecuteReturnResultWithSummary(ctx, params)
	default:
		return nil, util.WrapError(ctx, fmt.Errorf("unknown function: %s", call.Name), "ExecuteFunctionCall")
	}
}

// resolveFilePath 解析文件路径（保留用于向后兼容）
func (tm *EnhancedToolManager) resolveFilePath(filePath string) string {
	if filepath.IsAbs(filePath) {
		return filePath
	}
	return filepath.Join(tm.workingDir, filePath)
}

type PreInvestigateResponse struct {
	Success bool `json:"success"`
	Data    struct {
		ProjectID      string `json:"project_id"`
		TotalFunctions int    `json:"total_functions"`
		CoreFunctions  []struct {
			Name      string `json:"name"`
			FilePath  string `json:"file_path"`
			OutDegree int    `json:"out_degree"`
			Callers   []struct {
				FunctionName string `json:"function_name"`
				FilePath     string `json:"file_path"`
			} `json:"callers"`
			Callees []struct {
				FunctionName string `json:"function_name"`
				FilePath     string `json:"file_path"`
			} `json:"callees"`
		} `json:"core_functions"`
		FileSkeletons []struct {
			Filepath     string `json:"filepath"`
			Language     string `json:"language"`
			SkeletonText string `json:"skeleton_text"`
		} `json:"file_skeletons"`
		DirectoryTree string `json:"directory_tree"`
	} `json:"data"`
}

// ExecuteInvestigateRepo implements the investigate_repo tool using sub-agent execution
// Note: This function now returns an error instead of ToolResult to be consistent with other tool functions
func (tm *EnhancedToolManager) ExecuteInvestigateRepo(ctx context.Context, params map[string]interface{}) (interface{}, error) {

	// 生成任务ID
	taskID := fmt.Sprintf("investigate_%d", time.Now().Unix())

	log.Info().
		Str("task_id", taskID).
		Msg("Executing investigate_repo using sub-agent mode")

	// 使用子代理模式执行任务
	initialMessage := "Analyze the project repository and provide a summary of the codebase's architecture."
	preInvestigateResult, err := tm.doPreInvestigate(tm.workingDir)
	if err != nil {
		return nil, util.WrapError(ctx, err, "executeInvestigateRepo::doPreInvestigate")
	}
	projectInfoStr, _ := json.Marshal(preInvestigateResult.Data)

	systemPrompt := strings.ReplaceAll(InvestigateRepoSystemPrompt, "{{.WorkingDir}}", tm.workingDir)
	systemPrompt = strings.ReplaceAll(systemPrompt, "{{.ProjectInfo}}", string(projectInfoStr))
	initialMessage = fmt.Sprintf("Analyze the project repository and provide a summary of the codebase's architecture. Project directory: %s", tm.workingDir)

	// 运行子代理
	result, err := tm.assistant.subAgent.RunSubAgentWithTools(ctx, systemPrompt, initialMessage, taskID, nil,
		nil, []string{"investigate_repo", "planning", "finish", "ask_user_for_help", "delete_file", "edit_file", "create_file"})
	if err != nil {
		return nil, util.WrapError(ctx, err, "executeInvestigateRepo::RunSubAgent")
	}

	// 返回结果使用 return_result_with_summary 工具格式
	return map[string]interface{}{
		"tool":    "return_result_with_summary",
		"result":  result,
		"summary": "Completed repository investigation: analyzed codebase architecture and provided structured summary.",
	}, nil
}

func (tm *EnhancedToolManager) executeInvestigateRepo(ctx context.Context, memory *ConversationMemory, args map[string]interface{}) (map[string]interface{}, error) {
	messages := memory.ToLangChainMessages()
	result, err := tm.assistant.client.GenerateCompletionWithMemory(ctx, messages, nil)
	if err != nil {
		return nil, util.WrapError(ctx, err, "executeInvestigateRepo::GenerateCompletionWithMemory")
	}
	log.Info().
		Str("result", result).
		Msg("Investigate repo result")

	resultMap := map[string]interface{}{
		"success":      true,
		"repo_summary": result,
	}

	return resultMap, nil
}

// ExecuteArchitect implements the architect tool using direct LLM call
func (tm *EnhancedToolManager) ExecuteArchitect(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	requirements, ok := params["requirements"].(string)
	if !ok {
		return nil, util.WrapError(ctx, fmt.Errorf("requirements parameter must be a string"), "executeArchitect")
	}

	context, _ := params["context"].(string)
	focusAreas, _ := params["focus_areas"].(string)

	log.Info().
		Str("requirements", requirements).
		Str("focus_areas", focusAreas).
		Msg("Executing architect using direct LLM call")

	// 构建系统提示词
	systemPrompt := strings.ReplaceAll(PlanningSystemPrompt, "{{.WorkingDir}}", tm.workingDir)

	// 构建用户消息
	userMessage := fmt.Sprintf("Act as an expert software architect to analyze technical requirements and produce clear, actionable implementation plans. Requirements: %s", requirements)
	if context != "" {
		userMessage += fmt.Sprintf("\n\nContext: %s", context)
	}
	if focusAreas != "" {
		userMessage += fmt.Sprintf(" Focus on: %s", focusAreas)
	}

	// 构建消息列表
	messages := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, systemPrompt),
		llms.TextParts(llms.ChatMessageTypeHuman, userMessage),
	}

	// 直接调用 LLM
	result, err := tm.assistant.client.GenerateCompletionWithMemory(ctx, messages, nil)
	if err != nil {
		return nil, util.WrapError(ctx, err, "executeArchitect::GenerateCompletionWithMemory")
	}

	log.Info().
		Str("result", result).
		Msg("Architect result generated")

	// 返回结果
	return map[string]interface{}{
		"success": true,
		"plan":    result,
		"message": "Architectural analysis completed",
	}, nil
}

func (tm *EnhancedToolManager) doPreInvestigate(projectDir string) (*PreInvestigateResponse, error) {
	// 准备请求数据
	requestData := map[string]string{
		"project_dir": projectDir,
	}

	jsonData, err := json.Marshal(requestData)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request data: %v", err)
	}

	// 创建HTTP请求
	req, err := http.NewRequest(
		"POST",
		"http://localhost:8080/investigate_repo",
		strings.NewReader(string(jsonData)),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %v", err)
	}

	// 设置请求头
	req.Header.Set("Content-Type", "application/json")

	// 发送请求
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned non-200 status: %d, body: %s", resp.StatusCode, string(body))
	}

	var response PreInvestigateResponse
	err = json.Unmarshal(body, &response)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %v", err)
	}

	if !response.Success {
		return nil, fmt.Errorf("server returned unsuccessful response: %s", string(body))
	}

	return &response, nil
}
