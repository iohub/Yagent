package knowledge

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"codeactor/internal/config"
	"codeactor/internal/logging"
	"codeactor/internal/mcp"
	"codeactor/internal/tokenutil"

	"log/slog"
)

// InjectionContext 注入上下文
type InjectionContext struct {
	// UserMessage 当前用户输入/任务描述
	UserMessage string
	// TargetFiles 目标文件路径（可选）
	TargetFiles []string
	// AgentName 触发注入的 agent 名称（可选，用于日志关联）
	AgentName string
	// Domains 限定检索的知识域（repo / coding）；空 = 检索全部
	Domains []string
}

// EventPublisher 事件发布最小接口（*messaging.MessagePublisher 天然满足，便于测试 stub）
type EventPublisher interface {
	// Publish 发布事件；eventType 为事件类型字符串，content 为事件载荷，from 为发送方标识
	Publish(eventType string, content interface{}, from string) error
}

// KnowledgeInjector 对话前知识检索注入器
type KnowledgeInjector struct {
	mcpClient *mcp.MCPClient
	cfg       config.KnowledgeConfig
	// publisher 事件发布器（可为 nil；用于向 TUI 发布知识注入概要事件，fail-safe）
	publisher EventPublisher
}

// NewKnowledgeInjector 创建知识注入器
// publisher 为可选的事件发布器（传入 *messaging.MessagePublisher 即可），nil 时跳过事件发布
func NewKnowledgeInjector(mcpClient *mcp.MCPClient, cfg config.KnowledgeConfig, publisher EventPublisher) *KnowledgeInjector {
	return &KnowledgeInjector{
		mcpClient: mcpClient,
		cfg:       cfg,
		publisher: publisher,
	}
}

// BuildQuery 构造检索查询（使用完整原始输入，不做截断）
func (k *KnowledgeInjector) BuildQuery(injCtx InjectionContext) string {
	var parts []string
	parts = append(parts, injCtx.UserMessage)
	for _, f := range injCtx.TargetFiles {
		parts = append(parts, filepath.Base(f))
	}
	query := strings.Join(parts, " 涉及文件：")
	return strings.TrimSpace(query)
}

// Inject 执行知识检索和格式化注入块；失败或无关时返回空字符串（fail-safe，不阻塞主流程）
func (k *KnowledgeInjector) Inject(ctx context.Context, injCtx InjectionContext) (string, error) {
	kl := logging.KnowledgeLogger()
	agent := injCtx.AgentName
	if agent == "" {
		agent = "unknown"
	}

	if k.mcpClient == nil || !k.cfg.Enabled {
		kl.Debug("knowledge inject skipped", "event", "inject_skipped", "agent", agent, "reason", "disabled_or_no_mcp")
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
	kl.Info("knowledge injection start", "event", "inject_start", "agent", agent, "query", truncateByRune(query, 120), "query_len", runeCount(query), "limit", limit, "rerank", k.cfg.InjectionRerank, "domains", injCtx.Domains)

	results, err := k.mcpClient.KnowledgeSearch(ctx, mcp.KnowledgeSearchRequest{
		Query:   query,
		Limit:   limit,
		Rerank:  k.cfg.InjectionRerank,
		Domains: injCtx.Domains,
	})
	if err != nil {
		slog.Warn("知识检索失败，跳过注入", "error", err)
		kl.Warn("knowledge search failed, skip injection", "event", "inject_search_error", "agent", agent, "error", err)
		return "", nil
	}
	kl.Info("knowledge search done", "event", "inject_search_done", "agent", agent, "hits", len(results))
	for i, r := range results {
		var rerankScore interface{}
		if r.RerankScore != nil {
			rerankScore = *r.RerankScore
		} else {
			rerankScore = nil
		}
		kl.Info("inject raw hit", "event", "inject_raw_hit", "agent", agent, "idx", i, "title", r.Title, "final_score", r.FinalScore, "rerank_score", rerankScore, "confidence", r.Confidence, "type", r.Type, "content", truncateByRune(r.Content, 200))
		if len(r.RelatedFiles) > 0 {
			kl.Info("inject raw hit", "event", "inject_raw_hit_related_files", "agent", agent, "idx", i, "related_files", r.RelatedFiles)
		}
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
		passed := score >= minScore
		kl.Debug("knowledge result filter", "event", "inject_filter_item", "agent", agent, "title", r.Title, "score", score, "passed", passed)
		if passed {
			filtered = append(filtered, r)
		}
	}

	if len(filtered) == 0 {
		kl.Info("no relevant knowledge after filter", "event", "inject_filtered_empty", "agent", agent, "raw_hits", len(results), "min_score", minScore)
		return "", nil
	}

	// 发布知识注入概要事件(仅命中>0 时;无命中保持静默;fail-safe,不影响主流程)
	k.publishInjectedSummary(agent, query, filtered)

	block := k.FormatKnowledgeBlock(filtered)
	// 预留 </knowledge_context> 标签的 token 开销
	budget := k.cfg.InjectionMaxTokens - 50
	if budget <= 0 {
		budget = 950
	}
	block = k.TruncateToTokenBudget(block, budget)
	kl.Info("knowledge block injected", "event", "inject_done", "agent", agent, "entries", len(filtered), "block_len", runeCount(block), "max_tokens", k.cfg.InjectionMaxTokens)
	if block != "" {
		ts := time.Now().Format("2006-01-02 15:04:05")
		entry := fmt.Sprintf("============================================================\n[%s] knowledge inject | agent=%s | query=%s | entries=%d | tokens=%d\n%s",
			ts, agent, query, len(filtered), budget, block)
		if err := logging.WriteKnowledgeInjectLog(entry); err != nil {
			kl.Warn("knowledge inject log write failed", "error", err)
		}
	}
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

// TruncateToTokenBudget 按 token 预算截断。
// 使用 tiktoken-go cl100k_base 精确估算 token 数。
// 按 "### " 分隔条目逐条保留，直到 token 预算耗尽。
func (k *KnowledgeInjector) TruncateToTokenBudget(text string, maxTokens int) string {
	estimatedTokens := tokenutil.EstimateTokens(text)
	if estimatedTokens <= maxTokens {
		return text
	}

	// 按 "### " 分隔条目，保留头部说明
	headerEnd := strings.Index(text, "### ")
	if headerEnd == -1 {
		// 没有条目，逐步截断直到满足预算
		return truncateToTokenBudgetFallback(text, maxTokens)
	}

	header := text[:headerEnd]
	entries := strings.Split(text[headerEnd:], "### ")
	// entries[0] 是 header 之后的内容（可能为空），从索引1开始才是真正条目

	var sb strings.Builder
	sb.WriteString(header)
	sb.WriteString("### ")

	remainingTokens := maxTokens - tokenutil.EstimateTokens(header+"### ")
	if remainingTokens <= 0 {
		sb.WriteString("\n</knowledge_context>\n")
		return sb.String()
	}

	for _, entry := range entries[1:] {
		entryWithPrefix := "### " + entry
		entryTokens := tokenutil.EstimateTokens(entryWithPrefix)
		if entryTokens > remainingTokens {
			break
		}
		sb.WriteString(entryWithPrefix)
		remainingTokens -= entryTokens
	}

	sb.WriteString("\n</knowledge_context>\n")
	return sb.String()
}

// truncateToTokenBudgetFallback 当文本没有条目分隔符时，逐步截断 rune 直到满足 token 预算。
func truncateToTokenBudgetFallback(text string, maxTokens int) string {
	if len(text) == 0 {
		return text
	}
	// 每次移除约 1/10 的 rune（至少 1 个），直到满足预算
	runes := []rune(text)
	for len(runes) > 0 {
		if tokenutil.EstimateTokens(string(runes)) <= maxTokens {
			break
		}
		// 移除约 1/10
		remove := len(runes) / 10
		if remove < 1 {
			remove = 1
		}
		runes = runes[:len(runes)-remove]
	}
	return string(runes)
}

// runeCount 返回字符串的 rune 数量
func runeCount(s string) int {
	return len([]rune(s))
}

// buildInjectedSummary 构建知识注入概要：agent、query（截断至 100 rune）、命中数、最高分（RerankScore 优先）、条目标题（前 5 条，单条截断至 60 rune）
func buildInjectedSummary(agent, query string, results []mcp.KnowledgeSearchResult) map[string]interface{} {
	maxScore := 0.0
	titles := make([]string, 0, len(results))
	for i, r := range results {
		score := r.FinalScore
		if r.RerankScore != nil {
			score = *r.RerankScore
		}
		if score > maxScore {
			maxScore = score
		}
		if i < 5 {
			titles = append(titles, truncateByRune(r.Title, 60))
		}
	}
	if agent == "" {
		agent = "unknown"
	}
	return map[string]interface{}{
		"agent":     agent,
		"query":     truncateByRune(query, 100),
		"hits":      len(results),
		"max_score": maxScore,
		"titles":    titles,
	}
}

// publishInjectedSummary 发布知识注入概要事件（事件类型 knowledge_injected，source 为 knowledge）。
// 仅当命中数 > 0 时发布；无命中保持静默（与既有行为一致）。
// fail-safe 铁律：publisher 为 nil 时跳过；任何 panic 均恢复；发布错误仅吞掉；
// 任何情况下不得影响 Inject 主流程的返回值。
func (k *KnowledgeInjector) publishInjectedSummary(agent, query string, results []mcp.KnowledgeSearchResult) {
	if k == nil || k.publisher == nil {
		return
	}
	// 仅命中>0 时发布；无命中保持静默
	if len(results) == 0 {
		return
	}
	defer func() {
		// 发布事件绝不能拖垮 Inject 主流程
		_ = recover()
	}()
	summary := buildInjectedSummary(agent, query, results)
	// 发布错误仅吞掉：事件展示失败不影响知识注入本身
	_ = k.publisher.Publish("knowledge_injected", summary, "knowledge")
}

// truncateByRune 按最大 rune 数截断字符串
func truncateByRune(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes])
}
