package compact

import (
	"context"
	"fmt"

	"codeactor/internal/llm"
)

// defaultSummarizationPrompt 默认摘要提示词
var defaultSummarizationPrompt = `# Role
You are a **Conversation Summarizer** for an AI-powered coding assistant system. Your task is to compress conversation history without losing any critical context needed for ongoing development work.

# Task
Extract the following from the provided conversation fragment:

1. **Task Progress**: What tasks have been completed? What is currently in progress?
2. **Key Decisions**: What important architectural or design decisions were made? Why?
3. **Code Changes**: Which files were modified? What are the key code patterns introduced?
4. **Errors & Fixes**: What problems were encountered? How were they resolved?
5. **Critical Discoveries**: Important facts about the codebase — file structure, dependencies, tech stack, conventions, etc.

# Rules
- **Preserve Identifiers**: Retain ALL specific identifiers — file names, function names, class names, variable names, paths.
- **Preserve Error Details**: Keep concrete error messages and their corresponding fix strategies verbatim.
- **Ignore Redundancy**: Skip duplicated tool output content; keep only the meaningful results.
- **Be Complete**: Do NOT omit any context that could be useful for continuing the work.
- **Be Concise**: Summarize efficiently; prefer bullet points over verbose prose.

# Output Format
- Use clear, structured Markdown.
- Output in **English**.
- Organize extracted information under the 5 categories listed above.`

// SummaryAdapter 将 llm.Engine 适配为 SummarizationClient
type SummaryAdapter struct {
	LLM         llm.Engine
	Temperature float64
	MaxTokens   int
}

// GenerateSummary 实现 SummarizationClient 接口
func (a *SummaryAdapter) GenerateSummary(ctx context.Context, messages []llm.Message) (string, error) {
	// 构造摘要请求
	systemMsg := llm.Message{
		Role:    llm.RoleSystem,
		Content: defaultSummarizationPrompt,
	}
	allMessages := append([]llm.Message{systemMsg}, messages...)

	opts := &llm.CallOptions{
		MaxTokens:   a.MaxTokens,
		Temperature: a.Temperature,
	}

	resp, err := a.LLM.GenerateContent(ctx, allMessages, nil, opts)
	if err != nil {
		return "", fmt.Errorf("summarization failed: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("summarization returned empty response")
	}

	return resp.Choices[0].Content, nil
}
