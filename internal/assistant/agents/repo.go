package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"codeactor/internal/assistant/tools"

	"github.com/tmc/langchaingo/llms"
)

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
	Adapters   []*tools.Adapter
	projectDir string
}

func NewRepoAgent(llm llms.LLM, fileOps *tools.FileOperationsTool, searchOps *tools.SearchOperationsTool, sysOps *tools.SystemOperationsTool, projectDir string) *RepoAgent {
	adapters := []*tools.Adapter{
		tools.NewAdapter("read_file", "Read file content", fileOps.ExecuteReadFile).WithSchema(map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"target_file":                    map[string]interface{}{"type": "string", "description": "The path of the file to read"},
				"start_line_one_indexed":         map[string]interface{}{"type": "integer", "description": "Start line (1-indexed)"},
				"end_line_one_indexed_inclusive": map[string]interface{}{"type": "integer", "description": "End line (inclusive)"},
			},
			"required": []string{"target_file"},
		}),
		tools.NewAdapter("list_dir", "List directory", sysOps.ExecuteListDir).WithSchema(map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"absolute_path": map[string]interface{}{"type": "string", "description": "Absolute path to list"},
			},
			"required": []string{"absolute_path"},
		}),
		tools.NewAdapter("grep_search", "Search code using grep", searchOps.ExecuteGrepSearch).WithSchema(map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query":           map[string]interface{}{"type": "string", "description": "Regex query"},
				"include_pattern": map[string]interface{}{"type": "string", "description": "File pattern to include"},
			},
			"required": []string{"query"},
		}),
		tools.NewAdapter("file_search", "Find file paths", searchOps.ExecuteFileSearch).WithSchema(map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{"type": "string", "description": "Filename query"},
			},
			"required": []string{"query"},
		}),
	}

	return &RepoAgent{
		BaseAgent: BaseAgent{
			LLM: llm,
		},
		Adapters: adapters,
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
		"http://localhost:8080/investigate_repo",
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
	slog.Info("RepoAgent pre-investigation response", "status_code", resp.StatusCode, "body", string(body))

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
	systemPrompt := `You are the Repo-Agent, an expert code analyst. Your goal is to help the user understand the codebase.
You are READ-ONLY. You cannot modify files.

You have been provided with a pre-investigation report of the repository.
Use this information to answer user queries efficiently.
- **Directory Tree**: Use this to understand the project structure and locate relevant files.
- **Core Functions**: These are key functions with their call graph. Use them to understand the control flow and main components.
- **File Skeletons**: These provide outlines of important files. Use them to understand the code organization without reading the full content.

If the provided information is sufficient, answer the user's question directly.
If you need more details, use your available tools (read_file, grep_search, etc.) to explore further.`

	if a.projectDir == "" {
		return "", fmt.Errorf("project_dir is empty")
	}

	slog.Info("RepoAgent performing pre-investigation", "project_dir", a.projectDir)
	investigation, err := a.doPreInvestigate(a.projectDir)
	if err != nil {
		slog.Warn("RepoAgent pre-investigation failed", "error", err)
	} else {
		// Add investigation results to system prompt
		info := fmt.Sprintf("\n\nRepository Information:\nProject ID: %s\nTotal Functions: %d\n",
			investigation.Data.ProjectID, investigation.Data.TotalFunctions)

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

	messages := []llms.MessageContent{
		{
			Role:  llms.ChatMessageTypeSystem,
			Parts: []llms.ContentPart{llms.TextPart(systemPrompt)},
		},
		{
			Role:  llms.ChatMessageTypeHuman,
			Parts: []llms.ContentPart{llms.TextPart(input)},
		},
	}

	// Convert adapters to llms.Tool
	llmTools := make([]llms.Tool, len(a.Adapters))
	for i, ad := range a.Adapters {
		llmTools[i] = ad.ToLLMSTool()
	}

	maxSteps := 3
	for i := 0; i < maxSteps; i++ {
		slog.Debug("RepoAgent calling LLM", "step", i, "messages", messages)
		resp, err := a.LLM.GenerateContent(ctx, messages, llms.WithTools(llmTools))
		if err != nil {
			slog.Error("RepoAgent LLM error", "error", err, "step", i)
			return "", err
		}

		msg := resp.Choices[0]
		slog.Debug("RepoAgent LLM response", "step", i, "message", msg)
		parts := []llms.ContentPart{llms.TextPart(msg.Content)}
		for _, tc := range msg.ToolCalls {
			parts = append(parts, tc)
		}

		messages = append(messages, llms.MessageContent{
			Role:  llms.ChatMessageTypeAI,
			Parts: parts,
		})

		if len(msg.ToolCalls) == 0 {
			return msg.Content, nil
		}

		for _, tc := range msg.ToolCalls {
			var toolResult string
			var err error
			found := false
			for _, t := range a.Adapters {
				if t.Name() == tc.FunctionCall.Name {
					found = true
					toolResult, err = t.Call(ctx, tc.FunctionCall.Arguments)
					if err != nil {
						toolResult = fmt.Sprintf("Error executing tool %s: %v", t.Name(), err)
					}
					break
				}
			}
			if !found {
				toolResult = fmt.Sprintf("Tool %s not found", tc.FunctionCall.Name)
			}

			messages = append(messages, llms.MessageContent{
				Role: llms.ChatMessageTypeTool,
				Parts: []llms.ContentPart{
					llms.ToolCallResponse{
						ToolCallID: tc.ID,
						Name:       tc.FunctionCall.Name,
						Content:    toolResult,
					},
				},
			})
		}
	}

	return "", fmt.Errorf("RepoAgent exceeded max steps")
}
