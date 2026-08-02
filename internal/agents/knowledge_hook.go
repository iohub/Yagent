package agents

import (
	"context"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"codeactor/internal/globalctx"
	"codeactor/internal/tools"
)

// autoConsolidateSubtask 非阻塞地将子任务结果沉淀到知识库。
// 仅在 gctx.CodeSeekMCP 可用时执行，失败仅 Warn 不 panic。
func autoConsolidateSubtask(gctx *globalctx.GlobalCtx, sourceAgent, knowledgeType, taskInput, resultText string) {
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

		// title 取 taskInput 前 30 个 rune
		title := taskInput
		if r := []rune(title); len(r) > 30 {
			title = string(r[:30])
		}
		if title == "" {
			title = sourceAgent + " 子任务总结"
		}

		// content 字节安全截断到 500 字节（rune 边界，避免中文乱码）
		content := truncateContentByBytes(resultText, 500)

		tags := []interface{}{sourceAgent, "auto", "子任务总结"}

		tool := tools.NewConsolidateKnowledgeTool(gctx.CodeSeekMCP, nil, sourceAgent, knowledgeType)
		_, err := tool.Execute(ctx, map[string]interface{}{
			"title":   title,
			"content": content,
			"tags":    tags,
		})
		if err != nil {
			slog.Warn("autoConsolidateSubtask failed", "source_agent", sourceAgent, "knowledge_type", knowledgeType, "error", err)
		}
	}()
}

// truncateContentByBytes 按字节安全截断（rune 边界，避免中文乱码），保证 len(result) <= maxBytes
func truncateContentByBytes(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	var sb strings.Builder
	for _, r := range s {
		if sb.Len()+utf8.RuneLen(r) > maxBytes {
			break
		}
		sb.WriteRune(r)
	}
	return sb.String()
}
