package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"codeactor/internal/mcp"
)

type RepoOperationsTool struct {
	mcpClient   *mcp.MCPClient // MCP client for codeseek (required, may be nil if not configured)
	ProjectPath string
	timeout     int // MCP 请求超时秒数（从配置传入，0=默认30秒）
}

func NewRepoOperationsTool(mcpClient *mcp.MCPClient, projectPath string, timeout int) *RepoOperationsTool {
	return &RepoOperationsTool{
		mcpClient:   mcpClient,
		ProjectPath: projectPath,
		timeout:     timeout,
	}
}

// ── MCP 结果类型 ──

// SearchHit 表示语义搜索结果
type SearchHit struct {
	FilePath     string  `json:"file_path"`
	LineStart    int     `json:"line_start"`
	LineEnd      int     `json:"line_end"`
	Score        float64 `json:"final_score"`
	Snippet      string  `json:"code_block"`
	FunctionName string  `json:"symbol_name,omitempty"`
}

// CallSite 表示调用者或被调用者引用
type CallSite struct {
	FunctionName string `json:"function_name"`
	FilePath     string `json:"file_path"`
	LineNumber   int    `json:"line_number"`
	Column       int    `json:"column,omitempty"`
}

// CallGraphDirection 表示调用图遍历方向
type CallGraphDirection string

const (
	CallGraphDirectionCallers CallGraphDirection = "callers"
	CallGraphDirectionCallees CallGraphDirection = "callees"
)

// CallGraphNode 表示调用图中的节点
type CallGraphNode struct {
	FunctionName string          `json:"function_name"`
	FilePath     string          `json:"file_path"`
	CallSites    []CallSite      `json:"call_sites,omitempty"`
	Children     []CallGraphNode `json:"children,omitempty"`
}

// CallGraphResult 表示调用图查询结果
type CallGraphResult struct {
	Root  CallGraphNode `json:"root"`
	Depth int           `json:"depth"`
}

// IndexStatus 表示 CodeSeek 索引健康状态
type IndexStatus struct {
	ProjectPath string `json:"project_path"`
	Indexed     bool   `json:"indexed"`
	LastUpdated string `json:"last_updated,omitempty"`
	FileCount   int    `json:"file_count,omitempty"`
}

// QueryCodeSkeletonResponse 表示骨架查询响应
type QueryCodeSkeletonResponse struct {
	Success bool `json:"success"`
	Data    *struct {
		Skeletons []struct {
			Filepath     string `json:"filepath"`
			Language     string `json:"language"`
			SkeletonText string `json:"skeleton_text"`
		} `json:"skeletons"`
	} `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
}

// QueryCodeSnippetResponse 表示代码片段查询响应
type QueryCodeSnippetResponse struct {
	Success bool `json:"success"`
	Data    *struct {
		Filepath     string `json:"filepath"`
		FunctionName string `json:"function_name"`
		CodeSnippet  string `json:"code_snippet"`
		LineStart    int    `json:"line_start"`
		LineEnd      int    `json:"line_end"`
		Language     string `json:"language"`
	} `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
}

// ExecuteSemanticSearch 通过 CodeSeek MCP 执行语义代码搜索
func (t *RepoOperationsTool) ExecuteSemanticSearch(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	query, ok := params["query"].(string)
	if !ok {
		return nil, fmt.Errorf("query 参数必须是字符串")
	}
	limit := 10
	if l, ok := params["limit"].(float64); ok && int(l) > 0 {
		limit = int(l)
	}

	raw, err := t.callMCPTool(ctx, "codeseek_search", map[string]interface{}{
		"query": query,
		"limit": limit,
	})
	if err != nil {
		return nil, fmt.Errorf("语义搜索失败: %w", err)
	}

	hits, err := parseSearchResult(raw)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"source": "codeseek_mcp",
		"hits":   hits,
		"count":  len(hits),
	}, nil
}

// ensureClient 检查 MCP 客户端是否可用
func (t *RepoOperationsTool) ensureClient(ctx context.Context) error {
	if t.mcpClient == nil {
		return fmt.Errorf("CodeSeek MCP 客户端未配置，请设置 codeseek.binary_path")
	}
	if !t.mcpClient.IsAlive() {
		return fmt.Errorf("CodeSeek MCP 客户端未运行，请检查 codeseek 进程是否成功启动")
	}
	return nil
}

// callMCPTool 统一的 MCP 工具调用辅助方法
func (t *RepoOperationsTool) callMCPTool(ctx context.Context, toolName string, arguments map[string]interface{}) (string, error) {
	if err := t.ensureClient(ctx); err != nil {
		return "", err
	}

	// 如果有超时配置，应用超时
	if t.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(t.timeout)*time.Second)
		defer cancel()
	}

	result, err := t.mcpClient.CallTool(ctx, toolName, arguments)
	if err != nil {
		return "", fmt.Errorf("MCP 工具 %q 调用失败: %w", toolName, err)
	}

	if result.IsError {
		errMsg := "未知 MCP 错误"
		if len(result.Content) > 0 {
			errMsg = result.Content[0].Text
		}
		return "", fmt.Errorf("MCP 工具 %q 返回错误: %s", toolName, errMsg)
	}

	// 提取所有文本内容
	var texts []string
	for _, c := range result.Content {
		if c.Type == "text" && strings.TrimSpace(c.Text) != "" {
			texts = append(texts, c.Text)
		}
	}
	if len(texts) == 0 {
		return "", fmt.Errorf("MCP 工具 %q 返回了空内容", toolName)
	}

	return strings.Join(texts, "\n"), nil
}

// parseCallResult 解析 callers/callees 的 MCP 响应
func parseCallResult(jsonText string) ([]CallSite, error) {
	// 尝试直接解析为 []CallSite
	var sites []CallSite
	if err := json.Unmarshal([]byte(jsonText), &sites); err == nil {
		return sites, nil
	}

	// 尝试从包裹对象中提取
	var wrapper struct {
		Callers []CallSite `json:"callers"`
		Callees []CallSite `json:"callees"`
		Results []CallSite `json:"results"`
	}
	if err := json.Unmarshal([]byte(jsonText), &wrapper); err == nil {
		if len(wrapper.Callers) > 0 {
			return wrapper.Callers, nil
		}
		if len(wrapper.Callees) > 0 {
			return wrapper.Callees, nil
		}
		if len(wrapper.Results) > 0 {
			return wrapper.Results, nil
		}
	}

	// 尝试解析为 map 数组（codeseek 实际返回格式）
	var rawItems []map[string]interface{}
	if err := json.Unmarshal([]byte(jsonText), &rawItems); err == nil {
		sites = make([]CallSite, 0, len(rawItems))
		for _, item := range rawItems {
			site := CallSite{}
			if v, ok := item["caller"].(string); ok {
				site.FunctionName = v
			} else if v, ok := item["callee"].(string); ok {
				site.FunctionName = v
			}
			if v, ok := item["caller_file"].(string); ok {
				site.FilePath = v
			} else if v, ok := item["callee_file"].(string); ok {
				site.FilePath = v
			}
			if v, ok := item["callee_line"].(float64); ok {
				site.LineNumber = int(v)
			} else if v, ok := item["caller_line"].(float64); ok {
				site.LineNumber = int(v)
			}
			sites = append(sites, site)
		}
		return sites, nil
	}

	return nil, fmt.Errorf("无法解析调用关系结果")
}

// parseSearchResult 解析语义搜索的 MCP 响应
func parseSearchResult(jsonText string) ([]SearchHit, error) {
	// 尝试直接解析为 []SearchHit
	var hits []SearchHit
	if err := json.Unmarshal([]byte(jsonText), &hits); err == nil {
		return hits, nil
	}

	// 尝试从包裹对象提取
	var wrapper struct {
		Results []SearchHit `json:"results"`
		Hits    []SearchHit `json:"hits"`
	}
	if err := json.Unmarshal([]byte(jsonText), &wrapper); err == nil {
		if len(wrapper.Results) > 0 {
			return wrapper.Results, nil
		}
		return wrapper.Hits, nil
	}

	return nil, fmt.Errorf("无法解析搜索结果")
}

// parseCallGraphResult 解析调用图的 MCP 响应
func parseCallGraphResult(jsonText string) (*CallGraphResult, error) {
	var result CallGraphResult
	if err := json.Unmarshal([]byte(jsonText), &result); err != nil {
		// 尝试解析为嵌套结构
		var root CallGraphNode
		if err2 := json.Unmarshal([]byte(jsonText), &root); err2 == nil {
			return &CallGraphResult{Root: root, Depth: 1}, nil
		}
		return nil, fmt.Errorf("无法解析调用图结果: %w", err)
	}
	return &result, nil
}

// ExecuteQueryCodeSkeleton 通过 CodeSeek MCP 查询代码骨架
func (t *RepoOperationsTool) ExecuteQueryCodeSkeleton(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	filepathsInterface, ok := params["filepaths"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("filepaths parameter must be an array")
	}
	filepaths := make([]string, len(filepathsInterface))
	for i, v := range filepathsInterface {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("filepaths element must be a string")
		}
		filepaths[i] = s
	}

	raw, err := t.callMCPTool(ctx, "codeseek_skeleton", map[string]interface{}{
		"file_paths": filepaths,
	})
	if err != nil {
		// Graceful degradation: return empty result, not error
		return QueryCodeSkeletonResponse{
			Success: false,
			Error:   fmt.Sprintf("MCP skeleton 查询失败: %v", err),
		}, nil
	}

	var response QueryCodeSkeletonResponse
	if err := json.Unmarshal([]byte(raw), &response); err != nil {
		return nil, fmt.Errorf("解析 skeleton 结果失败: %w", err)
	}

	return response, nil
}

func (t *RepoOperationsTool) ExecuteQueryCodeSnippet(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	filepath, ok := params["filepath"].(string)
	if !ok {
		return nil, fmt.Errorf("filepath parameter must be a string")
	}
	functionName, ok := params["function_name"].(string)
	if !ok {
		return nil, fmt.Errorf("function_name parameter must be a string")
	}

	args := map[string]interface{}{
		"function_name": functionName,
	}
	if filepath != "" {
		args["file_path"] = filepath
	}

	raw, err := t.callMCPTool(ctx, "codeseek_snippet", args)
	if err != nil {
		// Graceful degradation: return empty result, not error
		return QueryCodeSnippetResponse{
			Success: false,
			Error:   fmt.Sprintf("MCP snippet 查询失败: %v", err),
		}, nil
	}

	var response QueryCodeSnippetResponse
	if err := json.Unmarshal([]byte(raw), &response); err != nil {
		return nil, fmt.Errorf("解析 snippet 结果失败: %w", err)
	}

	return response, nil
}

// ── 调用者/被调用者查询（完全迁移到 CodeSeek MCP） ──

func (t *RepoOperationsTool) ExecuteFindFunctionCallees(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	functionName, ok := params["function_name"].(string)
	if !ok || functionName == "" {
		return nil, fmt.Errorf("function_name 参数必须是字符串")
	}

	raw, err := t.callMCPTool(ctx, "codeseek_callees", map[string]interface{}{
		"symbol": functionName,
	})
	if err != nil {
		return nil, fmt.Errorf("查找被调用函数失败: %w", err)
	}

	sites, err := parseCallResult(raw)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"success":  true,
		"function": functionName,
		"callees":  sites,
		"count":    len(sites),
	}, nil
}

func (t *RepoOperationsTool) ExecuteFindFunctionCallers(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	functionName, ok := params["function_name"].(string)
	if !ok || functionName == "" {
		return nil, fmt.Errorf("function_name 参数必须是字符串")
	}

	raw, err := t.callMCPTool(ctx, "codeseek_callers", map[string]interface{}{
		"symbol": functionName,
	})
	if err != nil {
		return nil, fmt.Errorf("查找调用函数失败: %w", err)
	}

	sites, err := parseCallResult(raw)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"success":  true,
		"function": functionName,
		"callers":  sites,
		"count":    len(sites),
	}, nil
}

// ExecuteCallGraph 通过 CodeSeek MCP 查询调用图
func (t *RepoOperationsTool) ExecuteCallGraph(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	functionName, ok := params["function_name"].(string)
	if !ok || functionName == "" {
		return nil, fmt.Errorf("function_name 参数必须是字符串")
	}

	depth := 1
	if d, ok := params["depth"].(float64); ok && int(d) >= 1 && int(d) <= 3 {
		depth = int(d)
	}

	raw, err := t.callMCPTool(ctx, "codeseek_callgraph", map[string]interface{}{
		"function_name": functionName,
		"depth":         depth,
	})
	if err != nil {
		return nil, fmt.Errorf("调用图查询失败: %w", err)
	}

	result, err := parseCallGraphResult(raw)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"success": true,
		"result":  result,
	}, nil
}

// ExecuteCheckStatus 检查 CodeSeek 索引状态
func (t *RepoOperationsTool) ExecuteCheckStatus(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	raw, err := t.callMCPTool(ctx, "codeseek_status", map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("索引状态检查失败: %w", err)
	}

	var status IndexStatus
	if err := json.Unmarshal([]byte(raw), &status); err != nil {
		return nil, fmt.Errorf("解析索引状态失败: %w", err)
	}

	return map[string]interface{}{
		"success": true,
		"status":  status,
	}, nil
}
