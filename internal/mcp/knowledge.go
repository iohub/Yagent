package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

// KnowledgeRecord 对应 codeseek Rust 侧 KnowledgeRecord（vector 不返回）
type KnowledgeRecord struct {
	ID           string   `json:"id"`
	Type         string   `json:"type"`          // repo_retrieval | coding_modification
	Title        string   `json:"title"`
	Content      string   `json:"content"`
	Tags         []string `json:"tags"`
	RelatedFiles []string `json:"related_files"`
	SourceAgent  string   `json:"source_agent"`
	TaskID       string   `json:"task_id"`
	Confidence   float64  `json:"confidence"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
	AccessCount  int      `json:"access_count"`
	LastAccessed *string  `json:"last_accessed"`
	ParentIDs    []string `json:"parent_ids"`
}

// KnowledgeSearchResult 搜索结果（record 展平 + 评分）
type KnowledgeSearchResult struct {
	KnowledgeRecord
	VectorScore  *float64 `json:"vector_score"`
	Bm25Score    *float64 `json:"bm25_score"`
	FinalScore   float64  `json:"final_score"`
	RerankScore  *float64 `json:"rerank_score"`
}

// KnowledgeAddRequest
type KnowledgeAddRequest struct {
	Type         string   `json:"type"`
	Title        string   `json:"title"`
	Content      string   `json:"content"`
	Tags         []string `json:"tags,omitempty"`
	RelatedFiles []string `json:"related_files,omitempty"`
	SourceAgent  string   `json:"source_agent,omitempty"`
	TaskID       string   `json:"task_id,omitempty"`
	Confidence   float64  `json:"confidence,omitempty"`
}

// KnowledgeSearchRequest
type KnowledgeSearchRequest struct {
	Query   string   `json:"query"`
	Limit   int      `json:"limit,omitempty"`
	Rerank  bool     `json:"rerank,omitempty"`
	Domains []string `json:"domains,omitempty"` // 检索域: repo / coding; 空 = 全部
}

// KnowledgeListRequest
type KnowledgeListRequest struct {
	Limit int    `json:"limit,omitempty"`
	Type  string `json:"type,omitempty"`
	Tag   string `json:"tag,omitempty"`
}

// KnowledgeDeleteRequest
type KnowledgeDeleteRequest struct {
	ID string `json:"id"`
}

// extractText 从 ToolCallResult 提取 text 内容（IsError 时返回错误）
func extractText(result *ToolCallResult) (string, error) {
	if result.IsError {
		text := ""
		if len(result.Content) > 0 {
			text = result.Content[0].Text
		}
		return "", fmt.Errorf("knowledge tool error: %s", text)
	}
	var sb string
	for _, c := range result.Content {
		if c.Type == "text" {
			sb += c.Text
		}
	}
	return sb, nil
}

// KnowledgeAdd 添加知识，返回 *KnowledgeRecord（解析 CLI 输出的 JSON 记录）
func (c *MCPClient) KnowledgeAdd(ctx context.Context, req KnowledgeAddRequest) (*KnowledgeRecord, error) {
	args := map[string]interface{}{
		"type":         req.Type,
		"title":        req.Title,
		"content":      req.Content,
		"source_agent": req.SourceAgent,
		"task_id":      req.TaskID,
		"confidence":   req.Confidence,
	}
	if req.Tags != nil {
		args["tags"] = req.Tags
	}
	if req.RelatedFiles != nil {
		args["related_files"] = req.RelatedFiles
	}
	result, err := c.CallTool(ctx, "knowledge_add", args)
	if err != nil {
		return nil, fmt.Errorf("knowledge_add call failed: %w", err)
	}
	text, err := extractText(result)
	if err != nil {
		return nil, err
	}
	var record KnowledgeRecord
	if err := json.Unmarshal([]byte(text), &record); err != nil {
		return nil, fmt.Errorf("failed to parse knowledge_add result: %w\nraw: %s", err, text)
	}
	return &record, nil
}

// KnowledgeSearch 检索知识，返回 []KnowledgeSearchResult（解析 JSON 数组）
func (c *MCPClient) KnowledgeSearch(ctx context.Context, req KnowledgeSearchRequest) ([]KnowledgeSearchResult, error) {
	args := map[string]interface{}{
		"query":  req.Query,
		"limit":  req.Limit,
		"rerank": req.Rerank,
	}
	if req.Limit == 0 {
		args["limit"] = 8
	}
	if len(req.Domains) > 0 {
		args["domains"] = req.Domains
	}
	result, err := c.CallTool(ctx, "knowledge_search", args)
	if err != nil {
		return nil, fmt.Errorf("knowledge_search call failed: %w", err)
	}
	text, err := extractText(result)
	if err != nil {
		return nil, err
	}
	var results []KnowledgeSearchResult
	if err := json.Unmarshal([]byte(text), &results); err != nil {
		return nil, fmt.Errorf("failed to parse knowledge_search result: %w\nraw: %s", err, text)
	}
	return results, nil
}

// KnowledgeList 列出知识，返回 []KnowledgeRecord（解析 JSON 数组）
func (c *MCPClient) KnowledgeList(ctx context.Context, req KnowledgeListRequest) ([]KnowledgeRecord, error) {
	args := map[string]interface{}{
		"limit": req.Limit,
		"type":  req.Type,
		"tag":   req.Tag,
	}
	result, err := c.CallTool(ctx, "knowledge_list", args)
	if err != nil {
		return nil, fmt.Errorf("knowledge_list call failed: %w", err)
	}
	text, err := extractText(result)
	if err != nil {
		return nil, err
	}
	var records []KnowledgeRecord
	if err := json.Unmarshal([]byte(text), &records); err != nil {
		return nil, fmt.Errorf("failed to parse knowledge_list result: %w\nraw: %s", err, text)
	}
	return records, nil
}

// KnowledgeDelete 删除知识，返回 error
func (c *MCPClient) KnowledgeDelete(ctx context.Context, req KnowledgeDeleteRequest) error {
	args := map[string]interface{}{
		"id": req.ID,
	}
	result, err := c.CallTool(ctx, "knowledge_delete", args)
	if err != nil {
		return fmt.Errorf("knowledge_delete call failed: %w", err)
	}
	if _, err := extractText(result); err != nil {
		return err
	}
	return nil
}
