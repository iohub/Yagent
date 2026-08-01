package knowledge

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"codeactor/internal/config"
	"codeactor/internal/mcp"

	"log/slog"
)

// InjectionContext 注入上下文
type InjectionContext struct {
	// UserMessage 当前用户输入/任务描述
	UserMessage string
	// TargetFiles 目标文件路径（可选）
	TargetFiles []string
}

// KnowledgeInjector 对话前知识检索注入器
type KnowledgeInjector struct {
	mcpClient *mcp.MCPClient
	cfg       config.KnowledgeConfig
}

// NewKnowledgeInjector 创建知识注入器
func NewKnowledgeInjector(mcpClient *mcp.MCPClient, cfg config.KnowledgeConfig) *KnowledgeInjector {
	return &KnowledgeInjector{
		mcpClient: mcpClient,
		cfg:       cfg,
	}
}

// BuildQuery 构造检索查询（≤500 字符，按 rune 截断避免切断中文）
func (k *KnowledgeInjector) BuildQuery(injCtx InjectionContext) string {
	var parts []string
	parts = append(parts, injCtx.UserMessage)
	for _, f := range injCtx.TargetFiles {
		parts = append(parts, filepath.Base(f))
	}
	query := strings.Join(parts, " 涉及文件：")
	// 按 rune 截断到 500
	if runeCount(query) > 500 {
		runes := []rune(query)
		query = string(runes[:500])
	}
	return strings.TrimSpace(query)
}

// Inject 执行知识检索和格式化注入块；失败或无关时返回空字符串（fail-safe，不阻塞主流程）
func (k *KnowledgeInjector) Inject(ctx context.Context, injCtx InjectionContext) (string, error) {
	if k.mcpClient == nil || !k.cfg.Enabled {
		return "", nil
	}
	query := k.BuildQuery(injCtx)
	if query == "" {
		return "", nil
	}

	limit := k.cfg.InjectionMaxEntries
	if limit <= 0 {
		limit = 8
	}
	results, err := k.mcpClient.KnowledgeSearch(ctx, mcp.KnowledgeSearchRequest{
		Query:  query,
		Limit:  limit,
		Rerank: k.cfg.InjectionRerank,
	})
	if err != nil {
		slog.Warn("知识检索失败，跳过注入", "error", err)
		return "", nil
	}

	// 过滤低于阈值的条目
	minScore := k.cfg.InjectionMinScore
	if minScore <= 0 {
		minScore = 0.3
	}
	var filtered []mcp.KnowledgeSearchResult
	for _, r := range results {
		score := r.FinalScore
		if r.RerankScore != nil {
			score = *r.RerankScore
		}
		if score >= minScore {
			filtered = append(filtered, r)
		}
	}

	if len(filtered) == 0 {
		return "", nil
	}

	block := k.FormatKnowledgeBlock(filtered)
	// 预留 </knowledge_context> 标签的 token 开销
	budget := k.cfg.InjectionMaxTokens - 50
	if budget <= 0 {
		budget = 950
	}
	block = k.TruncateToTokenBudget(block, budget)
	return block, nil
}

// FormatKnowledgeBlock 格式化 <knowledge_context> 块
func (k *KnowledgeInjector) FormatKnowledgeBlock(results []mcp.KnowledgeSearchResult) string {
	var sb strings.Builder
	sb.WriteString("\n\n<knowledge_context>\nThe following is semantically retrieved knowledge from previous sessions.\nUse this as additional context. Trust new findings over old knowledge.\n")

	for _, r := range results {
		tag := "[检索]"
		if r.Type == "coding_modification" {
			tag = "[编码]"
		}
		score := r.FinalScore
		if r.RerankScore != nil {
			score = *r.RerankScore
		}
		sb.WriteString(fmt.Sprintf("\n### %s %s\n%s\n", tag, r.Title, r.Content))
		if len(r.RelatedFiles) > 0 {
			sb.WriteString(fmt.Sprintf("\n**相关文件**: %s\n", strings.Join(r.RelatedFiles, ", ")))
		}
		sb.WriteString(fmt.Sprintf("\n**置信度**: %.2f | **得分**: %.3f\n", r.Confidence, score))
	}

	sb.WriteString("\n</knowledge_context>\n")
	return sb.String()
}

// TruncateToTokenBudget 按 token 预算截断（中文按 rune/2 估算）
func (k *KnowledgeInjector) TruncateToTokenBudget(text string, maxTokens int) string {
	estimatedTokens := runeCount(text) / 2
	if estimatedTokens <= maxTokens {
		return text
	}

	// 按 "### " 分隔条目，保留头部说明
	headerEnd := strings.Index(text, "### ")
	if headerEnd == -1 {
		// 没有条目，直接截断
		return truncateByRune(text, maxTokens*2)
	}

	header := text[:headerEnd]
	entries := strings.Split(text[headerEnd:], "### ")
	// entries[0] 是 header 之后的内容（可能为空），从索引1开始才是真正条目

	var sb strings.Builder
	sb.WriteString(header)
	sb.WriteString("### ")

	remainingTokens := maxTokens - (runeCount(header+"### ") / 2)
	if remainingTokens <= 0 {
		sb.WriteString("\n</knowledge_context>\n")
		return sb.String()
	}

	for _, entry := range entries[1:] {
		entryWithPrefix := "### " + entry
		entryTokens := runeCount(entryWithPrefix) / 2
		if entryTokens > remainingTokens {
			break
		}
		sb.WriteString(entryWithPrefix)
		remainingTokens -= entryTokens
	}

	sb.WriteString("\n</knowledge_context>\n")
	return sb.String()
}

// runeCount 返回字符串的 rune 数量
func runeCount(s string) int {
	return len([]rune(s))
}

// truncateByRune 按最大 rune 数截断字符串
func truncateByRune(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes])
}
