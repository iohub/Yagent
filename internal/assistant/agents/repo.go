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
	"codeactor/pkg/messaging"

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
	maxSteps   int
}

func NewRepoAgent(llm llms.LLM, publisher *messaging.MessagePublisher, projectDir string, maxSteps int) *RepoAgent {

	return &RepoAgent{
		BaseAgent: BaseAgent{
			LLM:       llm,
			Publisher: publisher,
		},
		Adapters:   []*tools.Adapter{},
		projectDir: projectDir,
		maxSteps:   maxSteps,
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
	systemPrompt := `You are the Repo-Agent, an expert code analyst. Your goal is to analyze the repository investigation report and summarize core information to facilitate subsequent coding tasks.
You are READ-ONLY. You cannot modify files.

You have been provided with a investigation report of the repository.
Your task is to analyze this data to provide a comprehensive summary including:

1. **Technical Stack**: Identify the primary programming languages, frameworks, and key libraries used in the project.
2. **Repository Structure**: Describe the high-level organization of the codebase. Explain the purpose of key directories and how the project is structured (e.g., hexagonal architecture, MVC, etc.).
3. **Core Components**:
   - Identify the most important functions and components based on the "Core Functions" list.
   - Highlight critical data flows or control flows.
4. **Key Entry Points**: Identify where the application starts or where the main logic resides.

Use the provided investigation data:
- **Directory Tree**: For structure analysis.
- **Core Functions**: For component and dependency analysis.
- **File Skeletons**: For technical stack identification and understanding file contents without reading them fully.

based on provided investigation report is insufficient for a complete summary
Output a clear, structured summary that gives a developer a solid "mental map" of the codebase.`

	if a.projectDir == "" {
		return "", fmt.Errorf("project_dir is empty")
	}

	slog.Info("RepoAgent performing pre-investigation", "project_dir", a.projectDir)
	investigation, err := a.doPreInvestigate(a.projectDir)
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

	messages := []llms.MessageContent{
		/**
		{
			Role:  llms.ChatMessageTypeSystem,
			Parts: []llms.ContentPart{llms.TextPart(systemPrompt)},
		},
		**/
		{
			Role:  llms.ChatMessageTypeHuman,
			Parts: []llms.ContentPart{llms.TextPart(systemPrompt)},
		},
	}

	// Convert adapters to llms.Tool
	llmTools := make([]llms.Tool, len(a.Adapters))
	for i, ad := range a.Adapters {
		llmTools[i] = ad.ToLLMSTool()
	}

	for i := 0; i < a.maxSteps; i++ {
		slog.Debug("RepoAgent calling LLM", "step", i, "messages", messages)
		if a.Publisher != nil {
			a.Publisher.Publish("status_update", fmt.Sprintf("RepoAgent is thinking (step %d/%d)...", i+1, a.maxSteps))
		}
		resp, err := a.LLM.GenerateContent(ctx, messages, llms.WithTools(llmTools))
		if err != nil {
			slog.Error("RepoAgent LLM error", "error", err, "step", i)
			return "", err
		}

		msg := resp.Choices[0]
		// slog.Debug("RepoAgent LLM response", "step", i, "message", msg)
		if msg.Content != "" {
			if a.Publisher != nil {
				a.Publisher.Publish("ai_response", msg.Content)
			}
		}
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

			if a.Publisher != nil {
				a.Publisher.Publish("tool_call_start", map[string]interface{}{
					"tool_name": tc.FunctionCall.Name,
					"arguments": tc.FunctionCall.Arguments,
				})
			}

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

			if a.Publisher != nil {
				a.Publisher.Publish("tool_call_result", map[string]interface{}{
					"tool_name": tc.FunctionCall.Name,
					"result":    toolResult,
				})
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
