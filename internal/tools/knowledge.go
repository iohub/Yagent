package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"codeactor/internal/llm"
	"codeactor/internal/logging"
	"codeactor/internal/mcp"
)

// ============================================================================
// ConsolidateKnowledgeTool
// ============================================================================

// ConsolidateKnowledgeTool 知识整理工具，用于向知识图谱添加/合并知识条目。
type ConsolidateKnowledgeTool struct {
	mcp           *mcp.MCPClient
	engine        llm.Engine
	sourceAgent   string // 确定性来源 Agent（由代码 hook 注入，非空时覆盖 params 中的 source_agent）
	knowledgeType string // 确定性知识类型（由代码 hook 注入，非空时覆盖 params 中的 type）
}

// NewConsolidateKnowledgeTool 创建知识整理工具，sourceAgent/knowledgeType 由代码层确定性注入。
func NewConsolidateKnowledgeTool(mcpClient *mcp.MCPClient, engine llm.Engine, sourceAgent, knowledgeType string) *ConsolidateKnowledgeTool {
	return &ConsolidateKnowledgeTool{mcp: mcpClient, engine: engine, sourceAgent: sourceAgent, knowledgeType: knowledgeType}
}

// Execute 执行知识整理：校验 → 蒸馏 → 去重 → 合并 → add/delete。
func (t *ConsolidateKnowledgeTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	kl := logging.KnowledgeLogger()

	// ── 1. 参数解析 ───────────────────────────────────────────────────────────
	// type 优先使用代码层绑定的 knowledgeType（LLM 无法篡改）
	rawType := t.knowledgeType
	if rawType == "" {
		rawType, _ = params["type"].(string)
	}
	title, _ := params["title"].(string)
	content, _ := params["content"].(string)

	if rawType == "" {
		kl.Warn("consolidate param invalid", "event", "consolidate_param_error", "reason", "type is empty")
		return nil, fmt.Errorf("type parameter is required")
	}
	if title == "" {
		kl.Warn("consolidate param invalid", "event", "consolidate_param_error", "reason", "title is empty")
		return nil, fmt.Errorf("title parameter is required")
	}
	if content == "" {
		kl.Warn("consolidate param invalid", "event", "consolidate_param_error", "reason", "content is empty")
		return nil, fmt.Errorf("content parameter is required")
	}

	validTypes := map[string]bool{"repo_retrieval": true, "coding_modification": true}
	if !validTypes[rawType] {
		kl.Warn("consolidate param invalid", "event", "consolidate_param_error", "reason", fmt.Sprintf("invalid type: %q", rawType))
		return nil, fmt.Errorf("invalid type: %q (allowed: repo_retrieval, coding_modification)", rawType)
	}

	// 将 title 中的项目绝对路径替换为相对路径，减少字符占用
	if projectDir, err := os.Getwd(); err == nil {
		title = replaceProjectAbsPath(title, projectDir)
	}

	// title 超 200 字截断（按 rune 截断，避免中文等多字节字符截断乱码）
	if r := []rune(title); len(r) > 200 {
		title = string(r[:200])
	}

	// tags（至少 1 个）
	var tags []string
	if rawTags, ok := params["tags"].([]interface{}); ok {
		for _, v := range rawTags {
			if s, ok := v.(string); ok && s != "" {
				tags = append(tags, s)
			}
		}
	}
	if len(tags) == 0 {
		return nil, fmt.Errorf("tags parameter is required and must contain at least 1 non-empty string")
	}

	// related_files（可选）
	var relatedFiles []string
	if rawFiles, ok := params["related_files"].([]interface{}); ok {
		for _, v := range rawFiles {
			if s, ok := v.(string); ok {
				relatedFiles = append(relatedFiles, s)
			}
		}
	}

	sourceAgent := t.sourceAgent
	if sourceAgent == "" {
		sourceAgent, _ = params["source_agent"].(string)
	}
	if sourceAgent == "" {
		kl.Warn("consolidate param invalid", "event", "consolidate_param_error", "reason", "source_agent is empty")
		return nil, fmt.Errorf("source_agent is required")
	}
	taskID, _ := params["task_id"].(string)
	confidence := 1.0
	if rawConf, ok := params["confidence"].(float64); ok {
		confidence = rawConf
	}

	kl.Info("consolidate start", "event", "consolidate_start", "type", rawType, "title", title, "tags", tags, "source_agent", sourceAgent, "task_id", taskID)

	// ── 2. LLM 蒸馏（content > 1500 字时）──────────────────────────────────────
	origContentLen := len(content)
	if t.engine != nil && len(content) > 1500 {
		distilled, err := distillContent(t.engine, ctx, title, content)
		if err != nil {
			// 降级：硬截断
			kl.Warn("consolidate distill fallback", "event", "consolidate_distill_fallback", "title", title, "error", err)
			if r := []rune(content); len(r) > 1500 {
				content = string(r[:1500]) + "..."
			}
		} else {
			content = distilled
			kl.Info("consolidate distill", "event", "consolidate_distill", "title", title, "before_len", origContentLen, "after_len", len(distilled), "mode", "llm")
		}
	} else if len(content) > 1500 {
		kl.Info("consolidate distill", "event", "consolidate_distill", "title", title, "before_len", len(content), "after_len", 1503, "mode", "hard_truncate")
		if r := []rune(content); len(r) > 1500 {
			content = string(r[:1500]) + "..."
		}
	}

	// ── 3. 去重检测 ──────────────────────────────────────────────────────────
	var dupResult *mcp.KnowledgeSearchResult
	if t.mcp != nil {
		results, err := t.mcp.KnowledgeSearch(ctx, mcp.KnowledgeSearchRequest{
			Query:  title,
			Limit:  5,
			Rerank: true,
		})
		if err == nil {
			for i := range results {
				if results[i].RerankScore != nil && *results[i].RerankScore > 0.85 {
					dupResult = &results[i]
					kl.Info("duplicate detected", "event", "consolidate_dup_found", "title", title, "dup_id", dupResult.ID, "dup_title", dupResult.Title, "dup_score", *dupResult.RerankScore)
					break
				}
			}
			if dupResult == nil {
				kl.Debug("no duplicate found", "event", "consolidate_dup_none", "title", title)
			}
		} else {
			kl.Debug("dup check search error", "event", "consolidate_dup_check_error", "title", title, "error", err)
		}
	}

	// ── 4/5. 重复处理（合并）──────────────────────────────────────────────────
	if dupResult != nil && t.engine != nil {
		newTitle, newContent, newTags, err := mergeKnowledgeLLM(t.engine, ctx,
			title, content, tags, *dupResult)
		if err == nil {
			// 先 add 新，再 delete 旧
			newReq := mcp.KnowledgeAddRequest{
				Type:         rawType,
				Title:        newTitle,
				Content:      newContent,
				Tags:         newTags,
				RelatedFiles: relatedFiles,
				SourceAgent:  sourceAgent,
				TaskID:       taskID,
				Confidence:   confidence,
			}
			newRecord, err := t.mcp.KnowledgeAdd(ctx, newReq)
			if err == nil {
				_ = t.mcp.KnowledgeDelete(ctx, mcp.KnowledgeDeleteRequest{ID: dupResult.ID})
				kl.Info("merged with duplicate", "event", "consolidate_merged", "title", newTitle, "new_id", newRecord.ID, "parent_ids", []string{dupResult.ID})
				ts := time.Now().Format("2006-01-02 15:04:05")
				var rfLine string
				if len(relatedFiles) > 0 {
					rfLine = "\nrelated_files: " + strings.Join(relatedFiles, ",")
				}
				consolidateEntry := fmt.Sprintf("============================================================\n[%s] knowledge consolidate | event=merged | id=%s | type=%s | source_agent=%s | title=%s\ncontent: %s\ntags: %s%s",
					ts, newRecord.ID, rawType, sourceAgent, newTitle, newContent, strings.Join(newTags, ","), rfLine)
				if err := logging.WriteKnowledgeConsolidateLog(consolidateEntry); err != nil {
					kl.Warn("knowledge consolidate log write failed", "error", err)
				}
				return map[string]interface{}{
					"status":     "merged",
					"id":         newRecord.ID,
					"parent_ids": []string{dupResult.ID},
				}, nil
			}
			// add 失败则降级为直接 add 新条目
			kl.Warn("merge add failed, fallback to direct add", "event", "consolidate_merge_add_error", "title", title, "error", err)
		}
	}

	// ── 6. 普通添加 ──────────────────────────────────────────────────────────
	if t.mcp == nil {
		kl.Info("consolidate skipped", "event", "consolidate_skipped", "reason", "codeseek not available")
		return map[string]interface{}{"status": "skipped", "reason": "codeseek not available"}, nil
	}

	addReq := mcp.KnowledgeAddRequest{
		Type:         rawType,
		Title:        title,
		Content:      content,
		Tags:         tags,
		RelatedFiles: relatedFiles,
		SourceAgent:  sourceAgent,
		TaskID:       taskID,
		Confidence:   confidence,
	}
	record, err := t.mcp.KnowledgeAdd(ctx, addReq)
	if err != nil {
		kl.Warn("knowledge add failed", "event", "consolidate_add_error", "title", title, "error", err)
		return nil, fmt.Errorf("knowledge_add failed: %w", err)
	}
	kl.Info("knowledge added", "event", "consolidate_added", "title", title, "id", record.ID, "status", "added")
	ts := time.Now().Format("2006-01-02 15:04:05")
	var rfLine string
	if len(relatedFiles) > 0 {
		rfLine = "\nrelated_files: " + strings.Join(relatedFiles, ",")
	}
	consolidateEntry := fmt.Sprintf("============================================================\n[%s] knowledge consolidate | event=added | id=%s | type=%s | source_agent=%s | title=%s\ncontent: %s\ntags: %s%s",
		ts, record.ID, rawType, sourceAgent, title, content, strings.Join(tags, ","), rfLine)
	if err := logging.WriteKnowledgeConsolidateLog(consolidateEntry); err != nil {
		kl.Warn("knowledge consolidate log write failed", "error", err)
	}
	return map[string]interface{}{"status": "added", "id": record.ID}, nil
}

// ============================================================================
// PruneHistoryTool
// ============================================================================

// PruneHistoryTool 知识维护工具，支持 list/merge/delete 三种操作。
type PruneHistoryTool struct {
	mcp    *mcp.MCPClient
	engine llm.Engine
}

// NewPruneHistoryTool 创建知识维护工具。
func NewPruneHistoryTool(mcpClient *mcp.MCPClient, engine llm.Engine) *PruneHistoryTool {
	return &PruneHistoryTool{mcp: mcpClient, engine: engine}
}

// Execute 执行知识维护操作。
func (t *PruneHistoryTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	action, _ := params["action"].(string)
	switch action {
	case "list":
		return t.executeList(ctx, params)
	case "delete":
		return t.executeDelete(ctx, params)
	case "merge":
		return t.executeMerge(ctx, params)
	default:
		return nil, fmt.Errorf("action must be one of: list, delete, merge")
	}
}

// ── list ─────────────────────────────────────────────────────────────────────
func (t *PruneHistoryTool) executeList(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	if t.mcp == nil {
		return map[string]interface{}{"status": "skipped", "reason": "codeseek not available"}, nil
	}
	limit := 50
	if f, ok := params["limit"].(float64); ok {
		limit = int(f)
	}
	typ, _ := params["type"].(string)
	tag, _ := params["tag"].(string)

	records, err := t.mcp.KnowledgeList(ctx, mcp.KnowledgeListRequest{Limit: limit, Type: typ, Tag: tag})
	if err != nil {
		return nil, fmt.Errorf("knowledge_list failed: %w", err)
	}
	return map[string]interface{}{
		"total":   len(records),
		"entries": records,
	}, nil
}

// ── delete ───────────────────────────────────────────────────────────────────
func (t *PruneHistoryTool) executeDelete(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	if t.mcp == nil {
		return map[string]interface{}{"status": "skipped", "reason": "codeseek not available"}, nil
	}
	var ids []string
	if rawIDs, ok := params["ids"].([]interface{}); ok {
		for _, v := range rawIDs {
			if s, ok := v.(string); ok {
				ids = append(ids, s)
			}
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("ids parameter is required for delete action")
	}

	var deletedCount int
	var failedIDs []string
	for _, id := range ids {
		if err := t.mcp.KnowledgeDelete(ctx, mcp.KnowledgeDeleteRequest{ID: id}); err != nil {
			failedIDs = append(failedIDs, id)
		} else {
			deletedCount++
		}
	}
	if len(failedIDs) == len(ids) {
		return nil, fmt.Errorf("all delete operations failed: %v", failedIDs)
	}
	return map[string]interface{}{
		"status":     "deleted",
		"count":      deletedCount,
		"failed_ids": failedIDs,
	}, nil
}

// ── merge ────────────────────────────────────────────────────────────────────
func (t *PruneHistoryTool) executeMerge(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	kl := logging.KnowledgeLogger()
	if t.mcp == nil {
		return map[string]interface{}{"status": "skipped", "reason": "codeseek not available"}, nil
	}
	if t.engine == nil {
		return map[string]interface{}{"status": "no_merge_needed", "reason": "LLM engine not available"}, nil
	}

	limit := 200
	if f, ok := params["limit"].(float64); ok {
		limit = int(f)
	}
	typ, _ := params["type"].(string)
	threshold := 0.80
	if f, ok := params["similarity_threshold"].(float64); ok && f > 0 {
		threshold = f
	}

	candidates, err := t.mcp.KnowledgeList(ctx, mcp.KnowledgeListRequest{Limit: limit, Type: typ})
	if err != nil {
		return nil, fmt.Errorf("knowledge_list failed: %w", err)
	}

	type mergeGroup struct {
		newID     string
		parentIDs []string
	}
	var groups []mergeGroup
	mergedIDs := make(map[string]bool) // 已被合并的条目 ID

	for i, a := range candidates {
		if mergedIDs[a.ID] {
			continue
		}
		// 以 A.Content 为 query 检索，看是否有 B 与 A 高度相似
		results, err := t.mcp.KnowledgeSearch(ctx, mcp.KnowledgeSearchRequest{
			Query:  a.Content,
			Limit:  5,
			Rerank: true,
		})
		if err != nil {
			continue
		}
		for j := range results {
			r := &results[j]
			if r.ID == a.ID || mergedIDs[r.ID] {
				continue
			}
			if r.RerankScore != nil && *r.RerankScore > threshold {
				// 发现重复，合并这一组
				kl.Info("prune merge group found", "event", "prune_merge_group", "a_id", a.ID, "b_id", r.ID, "score", *r.RerankScore)
				newTitle, newContent, newTags, merr := mergeKnowledgeLLM(t.engine, ctx,
					a.Title, a.Content, a.Tags, *r)
				if merr != nil {
					continue
				}
				addReq := mcp.KnowledgeAddRequest{
					Type:         a.Type,
					Title:        newTitle,
					Content:      newContent,
					Tags:         newTags,
					RelatedFiles: mergeStrings(a.RelatedFiles, r.RelatedFiles),
					Confidence:   a.Confidence,
				}
				newRecord, aerr := t.mcp.KnowledgeAdd(ctx, addReq)
				if aerr != nil {
					continue
				}
				_ = t.mcp.KnowledgeDelete(ctx, mcp.KnowledgeDeleteRequest{ID: a.ID})
				_ = t.mcp.KnowledgeDelete(ctx, mcp.KnowledgeDeleteRequest{ID: r.ID})
				mergedIDs[a.ID] = true
				mergedIDs[r.ID] = true
				kl.Info("prune merge done", "event", "prune_merge_done", "new_id", newRecord.ID, "parent_ids", []string{a.ID, r.ID})
				groups = append(groups, mergeGroup{
					newID:     newRecord.ID,
					parentIDs: []string{a.ID, r.ID},
				})
				break // 只合并第一对
			}
		}
		_ = i // suppress unused variable
	}

	if len(groups) == 0 {
		return map[string]interface{}{"status": "no_merge_needed"}, nil
	}
	return map[string]interface{}{
		"status":        "merged",
		"merged_count":  len(groups),
		"merged_groups": groups,
	}, nil
}

// ============================================================================
// LLM 辅助函数
// ============================================================================

func distillContent(engine llm.Engine, ctx context.Context, title, content string) (string, error) {
	prompt := `You are a knowledge distillation assistant. Given a title and content, extract the core要点 (key points) in Chinese.
Rules:
1. Output must be ≤1500 characters (Chinese characters count as 1).
2. Preserve all file paths, function names, and symbol names exactly as written.
3. Keep the technical accuracy — do not lose critical details.
4. Output ONLY the distilled content, no explanation, no markdown.`
	resp, err := engine.GenerateContent(ctx, []llm.Message{
		{Role: llm.RoleSystem, Content: prompt},
		{Role: llm.RoleUser, Content: fmt.Sprintf("Title: %s\n\nContent:\n%s", title, content)},
	}, nil, &llm.CallOptions{Temperature: 0.3, MaxTokens: 2048})
	if err != nil {
		return "", err
	}
	result := strings.TrimSpace(resp.Choices[0].Content)
	if r := []rune(result); len(r) > 1500 {
		result = string(r[:1500])
	}
	return result, nil
}

type mergeOutput struct {
	Title   string   `json:"title"`
	Content string   `json:"content"`
	Tags    []string `json:"tags"`
}

func mergeKnowledgeLLM(engine llm.Engine, ctx context.Context,
	newTitle, newContent string, newTags []string,
	old mcp.KnowledgeSearchResult,
) (title, content string, tags []string, err error) {
	prompt := `You are a knowledge consolidation assistant. Given a new knowledge entry and an existing similar entry,
merge them into a single, unified entry.
Output ONLY a valid JSON object with these fields (no markdown fence, no explanation):
{"title":"...","content":"...","tags":["..."]}
Rules:
1. Title should be a clear, concise summary (≤200 chars).
2. Content should preserve all unique facts from both entries, keeping file paths and function names exact.
3. Tags: union of both entries' tags, deduplicated.
4. Output must be valid JSON.`

	userContent := fmt.Sprintf("New entry:\nTitle: %s\nContent: %s\nTags: %v\n\nExisting entry:\nTitle: %s\nContent: %s\nTags: %v\nType: %s",
		newTitle, newContent, newTags,
		old.Title, old.Content, old.Tags, old.Type)

	resp, err := engine.GenerateContent(ctx, []llm.Message{
		{Role: llm.RoleSystem, Content: prompt},
		{Role: llm.RoleUser, Content: userContent},
	}, nil, &llm.CallOptions{Temperature: 0.3, MaxTokens: 2048})
	if err != nil {
		return "", "", nil, err
	}

	rawJSON := extractJSON(resp.Choices[0].Content)
	var out mergeOutput
	if err := json.Unmarshal([]byte(rawJSON), &out); err != nil {
		return "", "", nil, fmt.Errorf("failed to parse merge output JSON: %w", err)
	}
	if out.Title == "" {
		out.Title = newTitle
	}
	if out.Content == "" {
		out.Content = newContent
	}
	// 去重合并 tags
	tagSet := make(map[string]bool)
	for _, tag := range newTags {
		tagSet[tag] = true
	}
	for _, tag := range old.Tags {
		tagSet[tag] = true
	}
	for tag := range tagSet {
		tags = append(tags, tag)
	}
	return out.Title, out.Content, tags, nil
}

// extractJSON 从文本中提取 JSON 块：去除 ```json fence，取第一个 '{' 到最后一个 '}'。
func extractJSON(text string) string {
	text = strings.TrimSpace(text)
	// 去除 markdown fence
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start == -1 || end == -1 || end <= start {
		return text
	}
	return text[start : end+1]
}

// mergeStrings 合并两个字符串切片并去重（保持顺序）。
func mergeStrings(a, b []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, s := range append(a, b...) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// replaceProjectAbsPath 将 title 中出现的所有项目绝对路径替换为相对路径，
// 以减少 title 中的字符占用。若 projectDir 为空或 title 不含 projectDir，
// 则原样返回 title，永不返回错误。
func replaceProjectAbsPath(title, projectDir string) string {
	if title == "" || projectDir == "" {
		return title
	}
	// 精确匹配：title 恰好等于 projectDir
	if title == projectDir {
		return "."
	}
	// Step 1：替换所有出现的 projectDir/ 和 projectDir\ 路径前缀片段（兼容跨平台）
	result := strings.ReplaceAll(title, projectDir+"/", "")
	result = strings.ReplaceAll(result, projectDir+"\\", "")
	// Step 2：处理单独出现的 projectDir（如 "参考 /path 文档"）
	// 条件：前面是路径分隔符，后面是非路径分隔符或字符串结尾
	var sb strings.Builder
	i := 0
	for i <= len(result)-len(projectDir) {
		if result[i:i+len(projectDir)] == projectDir {
			afterPos := i + len(projectDir)
			afterIsAlnum := afterPos < len(result) && (result[afterPos] >= '0' && result[afterPos] <= '9' || result[afterPos] >= 'A' && result[afterPos] <= 'Z' || result[afterPos] >= 'a' && result[afterPos] <= 'z')
			if !afterIsAlnum {
				sb.WriteString(".")
				i = afterPos
				continue
			}
		}
		sb.WriteByte(result[i])
		i++
	}
	sb.WriteString(result[i:])
	result = sb.String()
	// Step 3：仅在有实际替换时才清理首尾斜杠，避免误伤未修改的原始路径前缀
	if result != title {
		result = strings.Trim(result, "/\\")
		if result == "" {
			return "."
		}
	}
	return result
}
