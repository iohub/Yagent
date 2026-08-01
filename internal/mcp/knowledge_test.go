package mcp

import (
	"encoding/json"
	"testing"
)

// ============================================================================
// extractText 测试
// ============================================================================

func TestExtractText_IsError(t *testing.T) {
	result := &ToolCallResult{
		IsError: true,
		Content: []ToolContent{{Type: "text", Text: "something went wrong"}},
	}
	_, err := extractText(result)
	if err == nil {
		t.Fatal("expected error for IsError=true, got nil")
	}
	expected := "knowledge tool error: something went wrong"
	if err.Error() != expected {
		t.Errorf("extractText(IsError) error = %q, want %q", err.Error(), expected)
	}
}

func TestExtractText_IsErrorEmptyContent(t *testing.T) {
	result := &ToolCallResult{
		IsError: true,
		Content: []ToolContent{},
	}
	_, err := extractText(result)
	if err == nil {
		t.Fatal("expected error for IsError=true with empty content, got nil")
	}
}

func TestExtractText_NormalText(t *testing.T) {
	result := &ToolCallResult{
		IsError: false,
		Content: []ToolContent{
			{Type: "text", Text: "hello world"},
			{Type: "text", Text: " foo bar"},
		},
	}
	text, err := extractText(result)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "hello world foo bar" {
		t.Errorf("extractText(normal) = %q, want %q", text, "hello world foo bar")
	}
}

func TestExtractText_MixedContentTypes(t *testing.T) {
	// 非 text 类型的 content 应被跳过
	result := &ToolCallResult{
		IsError: false,
		Content: []ToolContent{
			{Type: "text", Text: "keep this"},
			{Type: "image", Text: "skip this"},
			{Type: "text", Text: " and this"},
		},
	}
	text, err := extractText(result)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "keep this and this" {
		t.Errorf("extractText(mixed) = %q, want %q", text, "keep this and this")
	}
}

func TestExtractText_EmptyContent(t *testing.T) {
	result := &ToolCallResult{
		IsError: false,
		Content: []ToolContent{},
	}
	text, err := extractText(result)
	if err != nil {
		t.Fatalf("unexpected error for empty content: %v", err)
	}
	if text != "" {
		t.Errorf("extractText(empty) = %q, want empty", text)
	}
}

// ============================================================================
// KnowledgeRecord JSON 反序列化测试（对应 Rust 侧 serde 输出）
// ============================================================================

func TestKnowledgeRecord_Deserialize(t *testing.T) {
	raw := `{
		"id": "kn_1",
		"type": "repo_retrieval",
		"title": "Auth Flow",
		"content": "Uses JWT tokens for auth",
		"tags": ["auth", "jwt"],
		"related_files": ["auth.go", "middleware.go"],
		"source_agent": "repo_agent",
		"task_id": "task_42",
		"confidence": 0.95,
		"created_at": "2024-01-01T00:00:00Z",
		"updated_at": "2024-01-02T00:00:00Z",
		"access_count": 5,
		"parent_ids": []
	}`
	var record KnowledgeRecord
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		t.Fatalf("failed to unmarshal KnowledgeRecord: %v", err)
	}
	if record.ID != "kn_1" {
		t.Errorf("ID = %q, want 'kn_1'", record.ID)
	}
	if record.Type != "repo_retrieval" {
		t.Errorf("Type = %q, want 'repo_retrieval'", record.Type)
	}
	if record.Title != "Auth Flow" {
		t.Errorf("Title = %q, want 'Auth Flow'", record.Title)
	}
	if len(record.Tags) != 2 || record.Tags[0] != "auth" || record.Tags[1] != "jwt" {
		t.Errorf("Tags = %v, want [auth jwt]", record.Tags)
	}
	if len(record.RelatedFiles) != 2 {
		t.Errorf("RelatedFiles len = %d, want 2", len(record.RelatedFiles))
	}
	if record.Confidence != 0.95 {
		t.Errorf("Confidence = %f, want 0.95", record.Confidence)
	}
	if record.AccessCount != 5 {
		t.Errorf("AccessCount = %d, want 5", record.AccessCount)
	}
}

func TestKnowledgeRecord_Deserialize_OptionalFields(t *testing.T) {
	// 测试可选字段缺失时不报错
	raw := `{"id":"kn_2","type":"coding_modification","title":"T","content":"c","tags":["t"]}`
	var record KnowledgeRecord
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		t.Fatalf("failed to unmarshal minimal KnowledgeRecord: %v", err)
	}
	if record.ID != "kn_2" {
		t.Errorf("ID = %q, want 'kn_2'", record.ID)
	}
	if record.Confidence != 0 {
		t.Errorf("Confidence (missing) = %f, want 0", record.Confidence)
	}
	if record.AccessCount != 0 {
		t.Errorf("AccessCount (missing) = %d, want 0", record.AccessCount)
	}
}

// ============================================================================
// KnowledgeSearchResult JSON 反序列化测试（对应 Rust 侧 serde flatten 输出）
// ============================================================================

func TestKnowledgeSearchResult_Deserialize(t *testing.T) {
	raw := `{
		"id": "kn_3",
		"type": "repo_retrieval",
		"title": "Search Results",
		"content": "Found relevant code",
		"tags": ["search"],
		"related_files": ["search.go"],
		"source_agent": "repo_agent",
		"task_id": "",
		"confidence": 0.8,
		"created_at": "2024-01-01T00:00:00Z",
		"updated_at": "2024-01-01T00:00:00Z",
		"access_count": 0,
		"vector_score": 0.75,
		"bm25_score": 0.6,
		"final_score": 0.85,
		"rerank_score": 0.9
	}`
	var result KnowledgeSearchResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("failed to unmarshal KnowledgeSearchResult: %v", err)
	}
	// 验证平铺字段
	if result.ID != "kn_3" {
		t.Errorf("ID = %q, want 'kn_3'", result.ID)
	}
	if result.FinalScore != 0.85 {
		t.Errorf("FinalScore = %f, want 0.85", result.FinalScore)
	}
	if result.VectorScore == nil || *result.VectorScore != 0.75 {
		t.Errorf("VectorScore = %v, want 0.75", result.VectorScore)
	}
	if result.Bm25Score == nil || *result.Bm25Score != 0.6 {
		t.Errorf("Bm25Score = %v, want 0.6", result.Bm25Score)
	}
	if result.RerankScore == nil || *result.RerankScore != 0.9 {
		t.Errorf("RerankScore = %v, want 0.9", result.RerankScore)
	}
}

func TestKnowledgeSearchResult_Deserialize_NilScores(t *testing.T) {
	// 测试评分字段缺失的情况（Rust 侧可能不返回这些字段）
	raw := `{
		"id": "kn_4",
		"type": "repo_retrieval",
		"title": "No Scores",
		"content": "Test",
		"tags": ["t"],
		"final_score": 0.5
	}`
	var result KnowledgeSearchResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("failed to unmarshal KnowledgeSearchResult with nil scores: %v", err)
	}
	if result.FinalScore != 0.5 {
		t.Errorf("FinalScore = %f, want 0.5", result.FinalScore)
	}
	if result.VectorScore != nil {
		t.Errorf("VectorScore should be nil, got %v", result.VectorScore)
	}
	if result.Bm25Score != nil {
		t.Errorf("Bm25Score should be nil, got %v", result.Bm25Score)
	}
	if result.RerankScore != nil {
		t.Errorf("RerankScore should be nil, got %v", result.RerankScore)
	}
}

func TestKnowledgeSearchResult_Deserialize_Array(t *testing.T) {
	// 测试 JSON 数组反序列化（KnowledgeSearch 返回的结果）
	raw := `[
		{
			"id": "kn_a",
			"type": "repo_retrieval",
			"title": "Result A",
			"content": "Content A",
			"tags": ["a"],
			"final_score": 0.9,
			"confidence": 0.85
		},
		{
			"id": "kn_b",
			"type": "coding_modification",
			"title": "Result B",
			"content": "Content B",
			"tags": ["b"],
			"final_score": 0.7,
			"confidence": 0.6
		}
	]`
	var results []KnowledgeSearchResult
	if err := json.Unmarshal([]byte(raw), &results); err != nil {
		t.Fatalf("failed to unmarshal array: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	if results[0].ID != "kn_a" {
		t.Errorf("results[0].ID = %q, want 'kn_a'", results[0].ID)
	}
	if results[1].Type != "coding_modification" {
		t.Errorf("results[1].Type = %q, want 'coding_modification'", results[1].Type)
	}
}

func TestKnowledgeRecord_Deserialize_Array(t *testing.T) {
	// 测试 KnowledgeList 返回的 JSON 数组
	raw := `[
		{"id":"kn_1","type":"repo_retrieval","title":"T1","content":"C1","tags":["t"]},
		{"id":"kn_2","type":"coding_modification","title":"T2","content":"C2","tags":["t"]}
	]`
	var records []KnowledgeRecord
	if err := json.Unmarshal([]byte(raw), &records); err != nil {
		t.Fatalf("failed to unmarshal array: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("len(records) = %d, want 2", len(records))
	}
	if records[0].ID != "kn_1" {
		t.Errorf("records[0].ID = %q, want 'kn_1'", records[0].ID)
	}
	if records[1].Type != "coding_modification" {
		t.Errorf("records[1].Type = %q, want 'coding_modification'", records[1].Type)
	}
}

// ============================================================================
// KnowledgeAddRequest / KnowledgeSearchRequest 参数构造验证
// ============================================================================

func TestKnowledgeAddRequest_JSONSerialization(t *testing.T) {
	req := KnowledgeAddRequest{
		Type:         "repo_retrieval",
		Title:        "Test",
		Content:      "Content here",
		Tags:         []string{"tag1", "tag2"},
		RelatedFiles: []string{"file.go"},
		SourceAgent:  "repo_agent",
		Confidence:   0.9,
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("failed to marshal KnowledgeAddRequest: %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if decoded["type"] != "repo_retrieval" {
		t.Errorf("type = %v, want 'repo_retrieval'", decoded["type"])
	}
	if decoded["confidence"] != float64(0.9) {
		t.Errorf("confidence = %v, want 0.9", decoded["confidence"])
	}
}

func TestKnowledgeSearchRequest_JSONSerialization(t *testing.T) {
	req := KnowledgeSearchRequest{
		Query:  "test query",
		Limit:  5,
		Rerank: true,
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("failed to marshal KnowledgeSearchRequest: %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if decoded["query"] != "test query" {
		t.Errorf("query = %v, want 'test query'", decoded["query"])
	}
	if decoded["limit"] != float64(5) {
		t.Errorf("limit = %v, want 5", decoded["limit"])
	}
	if decoded["rerank"] != true {
		t.Errorf("rerank = %v, want true", decoded["rerank"])
	}
}

func TestKnowledgeDeleteRequest_JSONSerialization(t *testing.T) {
	req := KnowledgeDeleteRequest{ID: "kn_123"}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("failed to marshal KnowledgeDeleteRequest: %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if decoded["id"] != "kn_123" {
		t.Errorf("id = %v, want 'kn_123'", decoded["id"])
	}
}
