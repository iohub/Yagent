package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"yagent/internal/llm"
	"yagent/internal/mcp"
	"yagent/internal/supervise"
	"yagent/internal/tools"
)

type multiDimEntry struct {
	Title      string   `json:"title"`
	Content    string   `json:"content"`
	Tags       []string `json:"tags"`
	Confidence float64  `json:"confidence"`
}

// autoConsolidateSubtask 非阻塞地实现复合知识整理：
//
//	A（原始知识）：将 taskInput 作为 title、resultText 作为 content 直接入库
//	B（LLM 多维度提取）：若 engine 非 nil，从原始知识中提取多维度条目后逐个入库
//
// 仅在 mcpClient（CodeSeekMCP）可用时执行，失败仅 Warn 不 panic。
func autoConsolidateSubtask(mcpClient *mcp.MCPClient, engine llm.Engine, sourceAgent, knowledgeType, taskInput, resultText string) {
	if mcpClient == nil {
		return
	}
	if resultText == "" {
		return
	}

	// 后台知识整理，**不阻塞委派返回**：挂在进程根域而不是委派域上。
	// 挂委派域会让 delegateTo 结束时等它 —— 而它内部是一次最长 60s 的 LLM 调用，
	// 用户看到的症状是"子 Agent 早答完了，界面还转一分钟"。
	// 根域只在进程退出时按预算等一次，等不到就把这个名字报出来。
	// ctx 仍是 Background 派生：调用方（工具 ctx）会在委派返回时被取消，
	// 而这份知识写库不该跟着一起蒸发。
	supervise.Root().Go("knowledge/auto-consolidate", func(context.Context) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		// A：原始知识直接入库（不截断）
		title := taskInput
		if title == "" {
			title = sourceAgent + " 子任务总结"
		}
		content := resultText
		tags := []string{sourceAgent, "auto", "子任务总结"}

		tool := tools.NewConsolidateKnowledgeTool(mcpClient, engine, sourceAgent, knowledgeType)
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
	})
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
