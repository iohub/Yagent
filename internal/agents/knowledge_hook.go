package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"codeactor/internal/llm"
	"codeactor/internal/globalctx"
	"codeactor/internal/tools"
)

type multiDimEntry struct {
	Title      string   `json:"title"`
	Content    string   `json:"content"`
	Tags       []string `json:"tags"`
	Confidence float64  `json:"confidence"`
}

// autoConsolidateSubtask 非阻塞地实现复合知识整理：
//   A（原始知识）：将 taskInput 作为 title、resultText 作为 content 直接入库
//   B（LLM 多维度提取）：若 engine 非 nil，从原始知识中提取多维度条目后逐个入库
// 仅在 gctx.CodeSeekMCP 可用时执行，失败仅 Warn 不 panic。
func autoConsolidateSubtask(gctx *globalctx.GlobalCtx, engine llm.Engine, sourceAgent, knowledgeType, taskInput, resultText string) {
	if gctx == nil || gctx.CodeSeekMCP == nil {
		return
	}
	if resultText == "" {
		return
	}

	// 非阻塞 goroutine，60s 超时
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		// A：原始知识直接入库（不截断）
		title := taskInput
		if title == "" {
			title = sourceAgent + " 子任务总结"
		}
		content := resultText
		tags := []string{sourceAgent, "auto", "子任务总结"}

		tool := tools.NewConsolidateKnowledgeTool(gctx.CodeSeekMCP, engine, sourceAgent, knowledgeType)
		_, err := tool.Execute(ctx, map[string]interface{}{
			"title":   title,
			"content": content,
			"tags":    tags,
		})
		if err != nil {
			slog.Warn("autoConsolidateSubtask A (raw) failed", "source_agent", sourceAgent, "knowledge_type", knowledgeType, "error", err)
		}

		// B：LLM 多维度提取（engine 非 nil 时才执行）
		if engine != nil {
			entries, extractErr := extractMultiDimKnowledge(engine, ctx, sourceAgent, title, content)
			if extractErr != nil {
				slog.Warn("autoConsolidateSubtask B (multi-dim extraction) failed", "source_agent", sourceAgent, "knowledge_type", knowledgeType, "error", extractErr)
				return
			}
			for i, entry := range entries {
				if entry.Content == "" {
					continue
				}
				if entry.Title == "" {
					entry.Title = title
				}
				if len(entry.Tags) == 0 {
					entry.Tags = []string{sourceAgent}
				}
				_, entryErr := tool.Execute(ctx, map[string]interface{}{
					"title":      entry.Title,
					"content":    entry.Content,
					"tags":       entry.Tags,
					"confidence": entry.Confidence,
				})
				if entryErr != nil {
					slog.Warn("autoConsolidateSubtask B entry failed", "source_agent", sourceAgent, "knowledge_type", knowledgeType, "index", i, "title", entry.Title, "error", entryErr)
				}
			}
		}
	}()
}

// extractMultiDimKnowledge 使用 LLM 从原始知识中提取多个独立维度的知识条目。
func extractMultiDimKnowledge(engine llm.Engine, ctx context.Context, sourceAgent, title, content string) ([]multiDimEntry, error) {
	prompt := fmt.Sprintf("来源Agent: %s\n任务描述: %s\n返回内容:\n%s", sourceAgent, title, content)

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "你是一个知识提取助手。请严格按照要求输出 JSON 格式，不要输出任何其他内容。"},
		{Role: llm.RoleUser, Content: prompt},
	}

	resp, err := engine.GenerateContent(ctx, messages, nil, &llm.CallOptions{Temperature: 0.1, MaxTokens: 2048})
	if err != nil {
		return nil, fmt.Errorf("extractMultiDimKnowledge: GenerateContent failed: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("extractMultiDimKnowledge: empty response")
	}

	raw := strings.TrimSpace(resp.Choices[0].Content)
	// 去除 markdown 围栏
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var entries []multiDimEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, fmt.Errorf("extractMultiDimKnowledge: failed to parse JSON: %w", err)
	}
	return entries, nil
}
