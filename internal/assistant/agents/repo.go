package agents

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"codeactor/internal/assistant/tools"
	"codeactor/internal/globalctx"
	"codeactor/pkg/messaging"

	"github.com/tmc/langchaingo/llms"
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
		DirectoryTree string `json:"directory_tree"`
	} `json:"data"`
}

type RepoAgent struct {
	BaseAgent
	GlobalCtx *globalctx.GlobalCtx
	Adapters  []*tools.Adapter
	maxSteps  int
}

func NewRepoAgent(globalCtx *globalctx.GlobalCtx, llm llms.LLM, publisher *messaging.MessagePublisher, maxSteps int) *RepoAgent {
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
		default:
			continue
		}

		adapter := tools.NewAdapter(def.Name, def.Description, fn).WithSchema(def.Parameters)
		adapters = append(adapters, adapter)
	}

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
	// 准备请求数据
	requestData := map[string]string{
		"project_dir": projectDir,
	}

	jsonData, err := json.Marshal(requestData)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request data: %v", err)
	}

	// 创建 HTTP 请求
	req, err := http.NewRequest(
		"POST",
		fmt.Sprintf("%s/investigate_repo", a.GlobalCtx.CodebaseURL),
		strings.NewReader(string(jsonData)),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %v", err)
	}

	// 设置请求头
	req.Header.Set("Content-Type", "application/json")
	slog.Info("RepoAgent pre-investigation request", "project_dir", projectDir)

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

func (a *RepoAgent) Run(ctx context.Context, input string) (string, error) {
	systemPrompt := repoPrompt

	if a.GlobalCtx.ProjectPath == "" {
		return "", fmt.Errorf("project_dir is empty")
	}

	slog.Info("RepoAgent performing pre-investigation", "project_dir", a.GlobalCtx.ProjectPath)
	investigation, err := a.doPreInvestigate(a.GlobalCtx.ProjectPath)
	if err != nil {
		slog.Warn("RepoAgent pre-investigation failed", "error", err)
	} else {
		// Add investigation results to system prompt
		info := "\n\nRepository Information:\n"
		info += "\nDirectory Tree:\n" + investigation.Data.DirectoryTree + "\n"
		info += "\nCore Functions:\n"
		for _, fn := range investigation.Data.CoreFunctions {
			info += fmt.Sprintf("- %s (in %s)\n", fn.Name, fn.FilePath)
			if len(fn.Callers) > 0 {
				info += "  Callers: "
				for i, caller := range fn.Callers {
					if i > 0 {
						info += ", "
					}
					info += fmt.Sprintf("%s (%s)", caller.FunctionName, caller.FilePath)
				}
				info += "\n"
			}
			if len(fn.Callees) > 0 {
				info += "  Callees: "
				for i, callee := range fn.Callees {
					if i > 0 {
						info += ", "
					}
					info += fmt.Sprintf("%s (%s)", callee.FunctionName, callee.FilePath)
				}
				info += "\n"
			}
		}

		info += "\nFile Skeletons (Context):\n"
		for _, sk := range investigation.Data.FileSkeletons {
			info += fmt.Sprintf("File: %s\n```%s\n%s\n```\n", sk.Filepath, sk.Language, sk.SkeletonText)
		}

		systemPrompt += info
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
	return RunAgentLoop(ctx, cfg)
}
