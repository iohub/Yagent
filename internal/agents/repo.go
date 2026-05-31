package agents

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"codeactor/internal/tools"
	"codeactor/internal/globalctx"
	"codeactor/internal/messaging"

	"codeactor/internal/llm"
)

//go:embed repo.prompt.md
var repoPrompt string

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
	} `json:"data"`
}

type RepoAgent struct {
	BaseAgent
	GlobalCtx *globalctx.GlobalCtx
	Adapters  []*tools.Adapter
	maxSteps  int
}

func NewRepoAgent(globalCtx *globalctx.GlobalCtx, llm llm.Engine, publisher *messaging.MessagePublisher, maxSteps int) *RepoAgent {
	var toolDefs []ToolDefinition
	if err := json.Unmarshal(ToolsJSON, &toolDefs); err != nil {
		slog.Error("Failed to unmarshal tools", "error", err)
	}

	adapters := make([]*tools.Adapter, 0)
	for _, def := range toolDefs {
		var fn tools.ToolFunc
		switch def.Name {
		case "read_file":
			fn = globalCtx.FileOps.ExecuteReadFile
		case "search_by_regex":
			fn = globalCtx.SearchOps.ExecuteGrepSearch
		case "list_dir":
			fn = globalCtx.FileOps.ExecuteListDir
		case "print_dir_tree":
			fn = globalCtx.FileOps.ExecutePrintDirTree
		case "semantic_search":
			fn = globalCtx.RepoOps.ExecuteSemanticSearch
		case "query_code_skeleton":
			fn = globalCtx.RepoOps.ExecuteQueryCodeSkeleton
		case "query_code_snippet":
			fn = globalCtx.RepoOps.ExecuteQueryCodeSnippet
		case "deepthinking":
			fn = globalCtx.DeepThinkingTool.Execute
		case "get_repo_overview":
			fn = makeGetRepoOverviewFn(globalCtx)
		default:
			continue
		}

		adapter := tools.NewAdapter(def.Name, def.Description, fn).WithSchema(def.Parameters)
		adapters = append(adapters, adapter)
	}
	tools.SetGuardOnAdapters(adapters, globalCtx.Guard)

	return &RepoAgent{
		BaseAgent: BaseAgent{
			LLM:       llm,
			Publisher: publisher,
		},
		GlobalCtx: globalCtx,
		Adapters:  adapters,
		maxSteps:  maxSteps,
	}
}

func (a *RepoAgent) Name() string {
	return "Repo-Agent"
}

func (a *RepoAgent) doPreInvestigate(projectDir string) (*PreInvestigateResponse, error) {
	requestData := map[string]string{
		"project_dir": projectDir,
	}

	jsonData, err := json.Marshal(requestData)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request data: %v", err)
	}

	url := fmt.Sprintf("%s/investigate_repo", a.GlobalCtx.CodebaseURL)
	slog.Info("RepoAgent pre-investigation request", "project_dir", projectDir)

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
		}

		req, err := http.NewRequest("POST", url, bytes.NewReader(jsonData))
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")

		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("failed to send request: %v", err)
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("failed to read response body: %v", err)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("server returned non-200 status: %d, body: %s", resp.StatusCode, string(body))
			continue
		}

		var response PreInvestigateResponse
		if err := json.Unmarshal(body, &response); err != nil {
			lastErr = fmt.Errorf("failed to unmarshal response: %v", err)
			continue
		}

		if !response.Success {
			lastErr = fmt.Errorf("server returned unsuccessful response: %s", string(body))
			continue
		}

		return &response, nil
	}

	return nil, fmt.Errorf("investigate_repo failed after 3 retries: %w", lastErr)
}

// makeGetRepoOverviewFn 创建一个闭包，捕获 globalCtx 用于后续调用
func makeGetRepoOverviewFn(globalCtx *globalctx.GlobalCtx) tools.ToolFunc {
	return func(_ context.Context, _ map[string]interface{}) (interface{}, error) {
		return executeGetRepoOverview(globalCtx)
	}
}

// executeGetRepoOverview 是 get_repo_overview 工具的实际实现
// 它调用 codebase 服务的 /investigate_repo 端点获取仓库全景画像
func executeGetRepoOverview(globalCtx *globalctx.GlobalCtx) (interface{}, error) {
	projectDir := globalCtx.ProjectPath

	if projectDir == "" {
		return "", fmt.Errorf("project_dir is empty")
	}

	requestData := map[string]string{
		"project_dir": projectDir,
	}

	jsonData, err := json.Marshal(requestData)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request data: %v", err)
	}

	url := fmt.Sprintf("%s/investigate_repo", globalCtx.CodebaseURL)
	slog.Info("get_repo_overview request", "project_dir", projectDir)

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
		}

		req, err := http.NewRequest("POST", url, bytes.NewReader(jsonData))
		if err != nil {
			lastErr = fmt.Errorf("failed to create request: %v", err)
			continue
		}
		req.Header.Set("Content-Type", "application/json")

		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("failed to send request: %v", err)
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("failed to read response body: %v", err)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("server returned non-200 status: %d, body: %s", resp.StatusCode, string(body))
			continue
		}

		var response PreInvestigateResponse
		if err := json.Unmarshal(body, &response); err != nil {
			lastErr = fmt.Errorf("failed to unmarshal response: %v", err)
			continue
		}

		if !response.Success {
			lastErr = fmt.Errorf("server returned unsuccessful response: %s", string(body))
			continue
		}

		// Format output as readable text
		var result string
		result = fmt.Sprintf("=== Repository Overview (Project: %s) ===\n\n", projectDir)
		result += fmt.Sprintf("Total Functions: %d\n\n", response.Data.TotalFunctions)

		result += "Core Functions (ranked by call relationships):\n"
		for _, fn := range response.Data.CoreFunctions {
			result += fmt.Sprintf("  - %s (in %s, out_degree: %d)\n", fn.Name, fn.FilePath, fn.OutDegree)
			if len(fn.Callers) > 0 {
				result += "    Callers: "
				for i, caller := range fn.Callers {
					if i > 0 {
						result += ", "
					}
					result += fmt.Sprintf("%s (%s)", caller.FunctionName, caller.FilePath)
				}
				result += "\n"
			}
			if len(fn.Callees) > 0 {
				result += "    Callees: "
				for i, callee := range fn.Callees {
					if i > 0 {
						result += ", "
					}
					result += fmt.Sprintf("%s (%s)", callee.FunctionName, callee.FilePath)
				}
				result += "\n"
			}
		}

		result += "\nFile Skeletons:\n"
		for _, sk := range response.Data.FileSkeletons {
			result += fmt.Sprintf("\nFile: %s (%s)\n```%s\n%s\n```\n", sk.Filepath, sk.Language, sk.Language, sk.SkeletonText)
		}

		return result, nil
	}

	return "", fmt.Errorf("get_repo_overview failed after 3 retries: %w", lastErr)
}

func (a *RepoAgent) Run(ctx context.Context, input string) (AgentResult, error) {
	systemPrompt := repoPrompt

	if a.GlobalCtx.ProjectPath == "" {
		return AgentResult{}, fmt.Errorf("project_dir is empty")
	}

	systemPrompt = a.GlobalCtx.FormatPrompt(systemPrompt)

	cfg := ExecutorConfig{
		SystemPrompt:  systemPrompt,
		UserInput:     input,
		Adapters:      a.Adapters,
		LLM:           a.LLM,
		MaxSteps:      a.maxSteps,
		Publisher:     a.Publisher,
		AgentName:     a.Name(),
		SystemAsHuman: true, // RepoAgent uses Human role for its prompt
	}
	result, err := RunAgentLoop(ctx, cfg)
	if err != nil {
		return AgentResult{}, err
	}
	return AgentResult{
		Text:   result.Text,
		Memory: ConvertLLMHistoryToMemory(result.History),
	}, nil
}
