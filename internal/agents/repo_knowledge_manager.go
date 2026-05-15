package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"codeactor/internal/globalctx"
)

// RepoKnowledgeMatch 表示从 Rust /repo_knowledge/search 返回的一个匹配结果
type RepoKnowledgeMatch struct {
	ID     string  `json:"id"`
	Task   string  `json:"task"`
	Result string  `json:"result"`
	Score  float64 `json:"score"`
}

// RepoKnowledgeManager 封装 RepoAgent 的分析能力并添加向量缓存层。
// 工作流程：
//   1. 搜索相似的历史分析
//   2. 如果有高相似度匹配（>= threshold），直接返回缓存结果
//   3. 否则执行 RepoAgent 并异步存储结果到知识库
type RepoKnowledgeManager struct {
	agent      Agent
	globalCtx  *globalctx.GlobalCtx
	threshold  float64
	httpClient *http.Client
}

// NewRepoKnowledgeManager 创建一个新的 RepoKnowledgeManager。
// 如果 threshold <= 0，默认为 0.95。
func NewRepoKnowledgeManager(agent Agent, globalCtx *globalctx.GlobalCtx, threshold float64) *RepoKnowledgeManager {
	if threshold <= 0 {
		threshold = 0.95 // 默认阈值
	}
	return &RepoKnowledgeManager{
		agent:     agent,
		globalCtx: globalCtx,
		threshold: threshold,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// AnalyseTask 核心方法：先搜索相似历史，命中则返回缓存，否则执行并异步存储。
// 如果搜索失败，fallback 到正常 RepoAgent 执行。
// 如果 globalCtx.CodebaseURL 为空，直接 fallback 到正常执行。
func (m *RepoKnowledgeManager) AnalyseTask(ctx context.Context, task string) (string, error) {
	// 如果 CodebaseURL 为空，优雅降级
	if m.globalCtx == nil || m.globalCtx.CodebaseURL == "" {
		return m.agent.Run(ctx, task)
	}

	// 1. 搜索相似的历史分析
	matches, err := m.searchSimilar(ctx, task)
	if err != nil {
		// 搜索失败，fallback 到正常 RepoAgent 执行
		fmt.Printf("[RepoKnowledge] search failed, falling back to full run: %v\n", err)
		return m.agent.Run(ctx, task)
	}

	// 2. 检查是否有高相似度匹配
	if len(matches) > 0 && matches[0].Score >= m.threshold {
		fmt.Printf("[RepoKnowledge] cache hit! score=%.4f, returning cached result\n", matches[0].Score)
		return matches[0].Result, nil
	}

	// 3. Cache miss: 正常执行 RepoAgent
	result, err := m.agent.Run(ctx, task)
	if err != nil {
		return "", err
	}

	// 4. 存储分析结果到知识库（异步，非致命错误）
	go func() {
		if err := m.embedTaskAndResult(context.Background(), task, result); err != nil {
			fmt.Printf("[RepoKnowledge] failed to embed: %v\n", err)
		}
	}()

	return result, nil
}

// searchSimilar 调用 Rust /repo_knowledge/search 搜索相似的历史分析。
func (m *RepoKnowledgeManager) searchSimilar(ctx context.Context, task string) ([]RepoKnowledgeMatch, error) {
	reqBody := map[string]interface{}{
		"task":  task,
		"top_k": 5,
	}
	reqJSON, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	reqURL := m.globalCtx.CodebaseURL + "/repo_knowledge/search"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(reqJSON))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("search returned status %d: %s", resp.StatusCode, string(body))
	}

	// 解析响应
	var searchResp struct {
		Success bool `json:"success"`
		Data    struct {
			Matches []RepoKnowledgeMatch `json:"matches"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	if !searchResp.Success {
		return nil, fmt.Errorf("search unsuccessful")
	}

	return searchResp.Data.Matches, nil
}

// embedTaskAndResult 调用 Rust /repo_knowledge/embed 存储分析结果。
func (m *RepoKnowledgeManager) embedTaskAndResult(ctx context.Context, task, result string) error {
	reqBody := map[string]string{
		"task":   task,
		"result": result,
	}
	reqJSON, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	reqURL := m.globalCtx.CodebaseURL + "/repo_knowledge/embed"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(reqJSON))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("embed returned status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}
