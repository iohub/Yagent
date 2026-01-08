package agents

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"

	"codeactor/internal/assistant/tools"
	"codeactor/pkg/messaging"

	"github.com/tmc/langchaingo/llms"
)

//go:embed conductor.prompt.md
var conductorPrompt string

type ConductorAgent struct {
	BaseAgent
	RepoAgent   *RepoAgent
	CodingAgent *CodingAgent
	Adapters    []*tools.Adapter
	maxSteps    int
}

func NewConductorAgent(llm llms.LLM, publisher *messaging.MessagePublisher, repo *RepoAgent, coding *CodingAgent, maxSteps int) *ConductorAgent {
	delegateRepo := tools.NewAdapter("delegate_repo", "Delegate analysis task to Repo-Agent", func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		task, ok := params["task"].(string)
		if !ok {
			return nil, fmt.Errorf("task parameter required")
		}
		return repo.Run(ctx, task)
	}).WithSchema(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"task": map[string]interface{}{"type": "string", "description": "The task description for Repo-Agent"},
		},
		"required": []string{"task"},
	})

	delegateCoding := tools.NewAdapter("delegate_coding", "Delegate coding task to Coding-Agent", func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		task, ok := params["task"].(string)
		if !ok {
			return nil, fmt.Errorf("task parameter required")
		}
		return coding.Run(ctx, task)
	}).WithSchema(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"task": map[string]interface{}{"type": "string", "description": "The task description for Coding-Agent"},
		},
		"required": []string{"task"},
	})

	fileOps := tools.NewFileOperationsTool(repo.projectDir)
	sysOps := tools.NewSystemOperationsTool(repo.projectDir)
	searchOps := tools.NewSearchOperationsTool(repo.projectDir)
	flowOps := tools.NewFlowControlTool(repo.projectDir)

	adapters := []*tools.Adapter{
		tools.NewAdapter("finish", "Indicate that the current task is finished. The output of this tool call will be a description of why the task is finished, which could be because the task is completed or cannot be completed and must be terminated.", flowOps.ExecuteFinish).WithSchema(map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"reason": map[string]interface{}{"type": "string", "description": "A description of why the task is finished, e.g., task completed, cannot complete, or must terminate."},
			},
			"required": []string{"reason"},
		}),
		tools.NewAdapter("read_file", `Read the contents of a file. the output of this tool call will be the 1-indexed file contents from start_line_one_indexed to end_line_one_indexed_inclusive, together with a summary of the lines outside start_line_one_indexed and end_line_one_indexed_inclusive.
Note that this call can view at most 250 lines at a time and 200 lines minimum.

When using this tool to gather information, it's your responsibility to ensure you have the COMPLETE context. Specifically, each time you call this command you should:
1) Assess if the contents you viewed are sufficient to proceed with your task.
2) Take note of where there are lines not shown.
3) If the file contents you have viewed are insufficient, and you suspect they may be in lines not shown, proactively call the tool again to view those lines.
4) When in doubt, call this tool again to gather more information. Remember that partial file views may miss critical dependencies, imports, or functionality.

In some cases, if reading a range of lines is not enough, you may choose to read the entire file.
Reading entire files is often wasteful and slow, especially for large files (i.e. more than a few hundred lines). So you should use this option sparingly.
Reading the entire file is not allowed in most cases. You are only allowed to read the entire file if it has been edited or manually attached to the conversation by the user.`, fileOps.ExecuteReadFile).WithSchema(map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"target_file": map[string]interface{}{
					"type":        "string",
					"description": "The path of the file to read. You can use either a relative path in the workspace or an absolute path. If an absolute path is provided, it will be preserved as is.",
				},
				"start_line_one_indexed": map[string]interface{}{
					"type":        "integer",
					"description": "The one-indexed line number to start reading from (inclusive).",
				},
				"end_line_one_indexed_inclusive": map[string]interface{}{
					"type":        "integer",
					"description": "The one-indexed line number to end reading at (inclusive).",
				},
				"should_read_entire_file": map[string]interface{}{
					"type":        "boolean",
					"description": "Whether to read the entire file. Defaults to false.",
				},
				"explanation": map[string]interface{}{
					"type":        "string",
					"description": "One sentence explanation as to why this tool is being used, and how it contributes to the goal.",
				},
			},
			"required": []string{"target_file", "should_read_entire_file", "start_line_one_indexed", "end_line_one_indexed_inclusive"},
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

	return &ConductorAgent{
		BaseAgent:   BaseAgent{LLM: llm, Publisher: publisher},
		RepoAgent:   repo,
		CodingAgent: coding,
		Adapters:    append(adapters, delegateRepo, delegateCoding),
		maxSteps:    maxSteps,
	}
}

func (a *ConductorAgent) Name() string {
	return "Conductor"
}

func (a *ConductorAgent) Run(ctx context.Context, input string) (string, error) {
	messages := []llms.MessageContent{
		{
			Role: llms.ChatMessageTypeSystem,
			Parts: []llms.ContentPart{llms.TextPart(`
# Role Definition
You are the **Conductor**, the intelligent orchestration engine and Technical Lead for an advanced autonomous coding system. You do not modify code or access the file system directly. Instead, you effectively manage a team of specialized sub-agents to complete complex software engineering tasks.

Your Goal: Analyze user requests, formulate a stepwise plan, delegate sub-tasks to the appropriate specialized agents, and strictly review their outputs to ensure high-quality software delivery.

# The Team (Sub-Agents)
You have access to the following distinct sub-agents. 
1.  **Repo-Agent (The Architect/Auditor)**:
    *   **Capabilities**: Codebase navigation, semantic search, dependency analysis, symbol finding, and file tree visualization.
    *   **Use Case**: When you need to understand *where* code lives, *how* files interact, or obtain context *before* any changes are made.
    *   **Restriction**: Read-Only. Cannot modify files.

2.  **Coding-Agent (The Engineer)**:
    *   **Capabilities**: Writing code, applying patches, running shell commands, executing tests (Linter/Pytest), and self-debugging via reflection.
    *   **Use Case**: When specific code changes, file creation, or terminal executions are required.
    *   **Restriction**: Focused on execution. Do not assign it broad research tasks; give it clear file paths and requirements.

# Workflow Strategy (SOP)

You must strictly follow this Loop: **Analyze -> Plan -> Delegate -> Review -> Iterate**.

## Phase 1: Analysis & Information Gathering
*   Upon receiving a task, do not rush to code. First, map out the "Knowns" and "Unknowns".
*   **MANDATORY**: If the task involves existing code, you MUST first dispatch the **Repo-Agent** to map the file structure and locate relevant code definitions. Never guess file paths.

## Phase 2: Planning (The TODO List)
*   Break the user's request into atomic, logical steps (TODOs).
*   Prioritize dependencies (e.g., "Install library X" before "Import library X").
*   Keep the Plan dynamic. You will mark items as [COMPLETED] or [FAILED] based on agent feedback.

## Phase 3: Delegation & Execution
*   Dispatch exactly **one** sub-task to the most suitable sub-agent at a time.
*   **Context is King**: When delegating to the Coding-Agent, you must pass the context found by the Repo-Agent.

## Phase 4: Review & React
*   **Critical**: Trust but verify. Analyze the TaskResult returned by a sub-agent.
*   **If Success**: Mark the current step as complete in your mental state and move to the next step.
*   **If Failure**: Analyze the error message.
    *   Is it a context issue? -> Send Repo-Agent to research.
    *   Is it a coding error? -> Instruct Coding-Agent to retry, possibly suggesting a different approach or enabling their thinking_tool.

# Decision Protocols

1.  **No Hallucinations**: You do not have eyes on the repo. You only know what Repo-Agent tells you. Do not invent file names.
2.  **Coding Separation**: You are the Project Manager, not the Typer. **Never** output raw code blocks intended for the final file in your own response. Always delegate the writing to Coding-Agent.
3.  **Step-by-Step**: Do not stack multiple execution commands in one delegation. Execute -> Check Result -> Execute Next.
4.  **Failure Recovery**: If a sub-agent gets stuck (fails 3 times on the same sub-task), do not mindlessly retry. Stop, refine the plan, and potentially ask the User for clarification.

# Response Format

You must process every interaction using the following thought process, followed by the specific Delegation Tool call or a Final Response to the user.

### 1. State Analysis
- Analysis  
*   Current High-Level Goal: ...
*   Completed Steps: [List steps with sequence number]
*   Current Step Status: ...
*   Reasoning for next action: ...

### 2. Action (Choose A or B)

**Option A: Delegate (Internal Monologue -> Tool Call)**
*   Call the sub-agent with specific arguments:
    *   delegate_repo: "Repo-Agent"
    *   delegate_coding: "Coding-Agent"

**Option B: Final Response (Call finish tool)**
*   Use this ONLY when the request is fully completed or you need human input.
*   Summarize what was done.

---`)},
		},
		{
			Role:  llms.ChatMessageTypeHuman,
			Parts: []llms.ContentPart{llms.TextPart(input)},
		},
	}

	llmTools := make([]llms.Tool, len(a.Adapters))
	for i, ad := range a.Adapters {
		llmTools[i] = ad.ToLLMSTool()
	}

	for i := 0; i < a.maxSteps; i++ {
		slog.Debug("ConductorAgent calling LLM", "step", i, "messages", messages)
		if a.Publisher != nil {
			a.Publisher.Publish("status_update", fmt.Sprintf("ConductorAgent is thinking (step %d/%d)...", i+1, a.maxSteps))
		}
		resp, err := a.LLM.GenerateContent(ctx, messages, llms.WithTools(llmTools))
		if err != nil {
			slog.Error("ConductorAgent LLM error", "error", err, "step", i)
			return "", err
		}

		msg := resp.Choices[0]
		slog.Debug("ConductorAgent LLM response", "step", i, "message", msg)

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
			if tc.FunctionCall.Name == "finish" {
				return "Task completed successfully", nil
			}

			for _, t := range a.Adapters {
				if t.Name() == tc.FunctionCall.Name {
					found = true
					toolResult, err = t.Call(ctx, tc.FunctionCall.Arguments)
					if err != nil {
						toolResult = fmt.Sprintf("Error: %v", err)
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

	return "", fmt.Errorf("ConductorAgent exceeded max steps")
}
