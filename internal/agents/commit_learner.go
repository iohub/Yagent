package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"codeactor/internal/globalctx"
	"codeactor/internal/llm"
)

// CommitMeta 表示 git commit 的基本信息
type CommitMeta struct {
	Hash    string    // commit hash
	Subject string    // commit message 主题
	Author  string    // 作者
	Date    time.Time // 提交时间
	Files   []string  // 变更文件列表
	Diff    string    // 截断后的 diff 文本
}

// CommitSummary 表示 LLM 生成的 commit 摘要
type CommitSummary struct {
	Hash            string `json:"hash"`
	Requirement     string `json:"requirement"`     // 需求摘要
	Files           string `json:"files"`           // 变更文件
	Approach        string `json:"approach"`        // 思路
	Implementation  string `json:"implementation"`  // 实现
}

// CachedSummary 缓存的 commit 摘要（包含时间戳）
type CachedSummary struct {
	Summary     CommitSummary
	CachedAt    time.Time
}

// CommitLearnConfig commit 学习器配置
type CommitLearnConfig struct {
	Enabled               bool    `toml:"enabled"`                 // 是否启用
	MaxCommits            int     `toml:"max_commits"`             // 最大获取 commit 数量
	SimilarityThreshold   float64 `toml:"similarity_threshold"`    // 相似度阈值
	TopK                  int     `toml:"top_k"`                   // 搜索结果数量
	Trigger               string  `toml:"trigger"`                 // 触发方式："on_demand", "on_session_start", "both"
	CacheTTL              int     `toml:"cache_ttl"`               // 缓存有效期（秒）
	LLMSystemPrompt       string  `toml:"llm_system_prompt"`       // LLM 系统提示词
	SummarizationProvider string  `toml:"summarization_provider"`  // 专用的 LLM provider 名称
}

// DefaultCommitLearnConfig 返回默认配置
func DefaultCommitLearnConfig() CommitLearnConfig {
	return CommitLearnConfig{
		Enabled:             true,
		MaxCommits:          30,
		SimilarityThreshold: 0.75,
		TopK:                3,
		Trigger:             "both",
		CacheTTL:            3600,
		LLMSystemPrompt: `You are a software engineering analyst. Analyze the following git commit and provide a structured summary.
Output in JSON format with these fields:
- requirement: A brief description of what requirement this commit addresses
- files: List of changed files with brief description of changes
- approach: The technical approach or strategy used
- implementation: Key implementation details

Keep each field concise (2-3 sentences max).`,
	}
}

// CommitLearner 负责学习 git commit 并生成结构化摘要
//
// 主要功能：
// 1. 通过 git log 获取最近 commit 信息
// 2. 调用 LLM 为每个 commit 生成结构化摘要
// 3. 将摘要存储到 Rust 侧的向量数据库
// 4. 支持搜索与用户输入相似的 commit
type CommitLearner struct {
	config          CommitLearnConfig
	llmEngine       llm.Engine           // 默认 LLM 引擎
	dedicatedEngine llm.Engine           // 专用的 LLM 引擎（可选，用于摘要生成）
	globalCtx       *globalctx.GlobalCtx // 全局上下文，包含 CodebaseURL
	httpClient      *http.Client
	cache           map[string]*CachedSummary
	cacheMu         sync.RWMutex
	lastHead        string
	lastFetch       time.Time
}

// NewCommitLearner 创建新的 CommitLearner 实例
func NewCommitLearner(config CommitLearnConfig, llmEngine llm.Engine, dedicatedEngine llm.Engine, globalCtx *globalctx.GlobalCtx) *CommitLearner {
	return &CommitLearner{
		config:          config,
		llmEngine:       llmEngine,
		dedicatedEngine: dedicatedEngine,
		globalCtx:       globalCtx,
		httpClient:      &http.Client{Timeout: 30 * time.Second},
		cache:           make(map[string]*CachedSummary),
	}
}

// Config 返回 CommitLearner 的配置
//
// 返回值:
//   - CommitLearnConfig: 当前配置（副本，不可变）
func (cl *CommitLearner) Config() CommitLearnConfig {
	return cl.config
}

// FetchRecentCommits 获取最近 N 个 commit 的详细信息
//
// 执行 git log 命令获取 commit hash、主题、作者、日期、变更文件和 diff
func (cl *CommitLearner) FetchRecentCommits(ctx context.Context, repoPath string, count int) ([]CommitMeta, error) {
	if count <= 0 {
		count = cl.config.MaxCommits
	}

	// 构建 git log 命令
	// 使用自定义格式输出：HASH|SUBJECT|AUTHOR|DATE
	// --name-only 获取变更文件列表
	// --patch 获取 diff 内容
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "log",
		fmt.Sprintf("--max-count=%d", count),
		"--pretty=format:COMMIT_START%n%H|%s|%an|%ai%nCOMMIT_END",
		"--name-only",
		"--patch",
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		stderrStr := stderr.String()
		if stderrStr == "" {
			stderrStr = "no output"
		}
		return nil, fmt.Errorf("failed to run git log: %w, stderr: %s", err, stderrStr)
	}

	return parseGitLogOutput(stdout.String())
}

// SummarizeCommits 使用 LLM 为 commits 批量生成结构化摘要
//
// 采用并发处理，限制最大并发数为 3 以避免 API 限流
func (cl *CommitLearner) SummarizeCommits(ctx context.Context, commits []CommitMeta) ([]CommitSummary, error) {
	var (
		summaries []CommitSummary
		mu        sync.Mutex
		wg        sync.WaitGroup
	)

	// 使用信号量限制并发数，避免 LLM API 限流
	semaphore := make(chan struct{}, 3)

	for _, commit := range commits {
		wg.Add(1)
		go func(c CommitMeta) {
			defer wg.Done()

			// 获取信号量（如果满了会阻塞）
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			summary, err := cl.summarizeSingleCommit(ctx, c)
			if err != nil {
				fmt.Printf("[CommitLearner] Failed to summarize commit %s: %v\n", c.Hash[:min(8, len(c.Hash))], err)
				return
			}

			mu.Lock()
			summaries = append(summaries, summary)
			mu.Unlock()
		}(commit)
	}

	wg.Wait()
	return summaries, nil
}

// StoreEmbeddings 将摘要存储到 Rust 向量数据库
//
// 调用 POST /commit/embed 接口为每个摘要生成向量并存储
// 单个存储失败不会阻断其他 commit 的处理
func (cl *CommitLearner) StoreEmbeddings(ctx context.Context, summaries []CommitSummary) error {
	for _, s := range summaries {
		// 构建摘要文本
		summaryText := fmt.Sprintf("Requirement: %s\nFiles: %s\nApproach: %s\nImplementation: %s",
			s.Requirement, s.Files, s.Approach, s.Implementation)

		// 构建请求体
		reqBody := map[string]string{
			"commit_hash":  s.Hash,
			"summary_text": summaryText,
		}

		reqJSON, err := json.Marshal(reqBody)
		if err != nil {
			fmt.Printf("[CommitLearner] Failed to marshal request for commit %s: %v\n", s.Hash, err)
			continue
		}

		// 调用 Rust API
		reqURL := cl.globalCtx.CodebaseURL + "/commit/embed"
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(reqJSON))
		if err != nil {
			fmt.Printf("[CommitLearner] Failed to create request for commit %s: %v\n", s.Hash, err)
			continue
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := cl.httpClient.Do(req)
		if err != nil {
			fmt.Printf("[CommitLearner] Failed to store embedding for commit %s: %v\n", s.Hash, err)
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			fmt.Printf("[CommitLearner] Failed to store embedding for commit %s: status %d, body: %s\n",
				s.Hash, resp.StatusCode, string(body))
			continue
		}

		// 更新缓存
		cl.cacheMu.Lock()
		cl.cache[s.Hash] = &CachedSummary{
			Summary:  s,
			CachedAt: time.Now(),
		}
		cl.cacheMu.Unlock()
	}

	return nil
}

// SearchSimilar 搜索与用户输入最相似的 commit
//
// 调用 POST /commit/search 接口进行向量相似度搜索
// 自动过滤低于配置阈值的匹配结果
func (cl *CommitLearner) SearchSimilar(ctx context.Context, userInput string, topK int) ([]CommitSummary, error) {
	if topK <= 0 {
		topK = cl.config.TopK
	}

	// 构建请求体
	reqBody := map[string]interface{}{
		"query":    userInput,
		"top_k":    topK,
		"threshold": cl.config.SimilarityThreshold,
	}
	reqJSON, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal search request: %w", err)
	}

	// 调用 Rust API
	reqURL := cl.globalCtx.CodebaseURL + "/commit/search"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(reqJSON))
	if err != nil {
		return nil, fmt.Errorf("failed to create search request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := cl.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to search similar commits: %w", err)
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
			Matches []struct {
				CommitHash  string  `json:"commit_hash"`
				SummaryText string  `json:"summary_text"`
				Similarity  float32 `json:"similarity"`
			} `json:"matches"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
		return nil, fmt.Errorf("failed to parse search response: %w", err)
	}

	// 过滤低于阈值的匹配，并解析摘要文本
	var results []CommitSummary
	for _, m := range searchResp.Data.Matches {
		if float64(m.Similarity) < cl.config.SimilarityThreshold {
			continue
		}
		summary := parseSummaryText(m.SummaryText)
		results = append(results, summary)
	}

	return results, nil
}

// ClearCommits 清空所有 commit 向量数据
//
// 调用 POST /commit/clear 接口删除所有存储的 commit 嵌入
func (cl *CommitLearner) ClearCommits(ctx context.Context) error {
	reqBody := map[string]interface{}{}
	reqJSON, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("failed to marshal clear request: %w", err)
	}

	reqURL := cl.globalCtx.CodebaseURL + "/commit/clear"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(reqJSON))
	if err != nil {
		return fmt.Errorf("failed to create clear request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := cl.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to clear commits: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("clear returned status %d: %s", resp.StatusCode, string(body))
	}

	// 清除本地缓存
	cl.cacheMu.Lock()
	cl.cache = make(map[string]*CachedSummary)
	cl.cacheMu.Unlock()

	return nil
}

// EnsureLatest 确保 commit 索引是最新的（用于缓存机制）
//
// 检查当前 HEAD 是否变化或缓存是否过期，如果是则重新学习
func (cl *CommitLearner) EnsureLatest(ctx context.Context, repoPath string) error {
	// 获取当前 HEAD
	head, err := cl.getCurrentHead(repoPath)
	if err != nil {
		return fmt.Errorf("failed to get current head: %w", err)
	}

	// 检查缓存是否有效
	cl.cacheMu.RLock()
	lastHead := cl.lastHead
	lastFetch := cl.lastFetch
	cl.cacheMu.RUnlock()

	if head == lastHead && time.Since(lastFetch) < time.Duration(cl.config.CacheTTL)*time.Second {
		return nil // 缓存有效，跳过
	}

	fmt.Printf("[CommitLearner] Starting commit learning (head: %s)\n", head[:min(8, len(head))])

	// 获取 commits
	commits, err := cl.FetchRecentCommits(ctx, repoPath, cl.config.MaxCommits)
	if err != nil {
		return fmt.Errorf("failed to fetch commits: %w", err)
	}

	fmt.Printf("[CommitLearner] Fetched %d commits\n", len(commits))

	// 生成摘要
	summaries, err := cl.SummarizeCommits(ctx, commits)
	if err != nil {
		return fmt.Errorf("failed to summarize commits: %w", err)
	}

	fmt.Printf("[CommitLearner] Generated %d summaries\n", len(summaries))

	// 存储到向量数据库
	if err := cl.StoreEmbeddings(ctx, summaries); err != nil {
		return fmt.Errorf("failed to store embeddings: %w", err)
	}

	// 更新缓存状态
	cl.cacheMu.Lock()
	cl.lastHead = head
	cl.lastFetch = time.Now()
	cl.cacheMu.Unlock()

	fmt.Printf("[CommitLearner] Commit learning complete\n")
	return nil
}

// GetCachedSummaries 获取缓存的 commit 摘要
func (cl *CommitLearner) GetCachedSummaries() []CommitSummary {
	cl.cacheMu.RLock()
	defer cl.cacheMu.RUnlock()

	var results []CommitSummary
	for _, cached := range cl.cache {
		results = append(results, cached.Summary)
	}
	return results
}

// FormatSummaryAsText 将 commit 摘要格式化为人类可读文本
func FormatSummaryAsText(summaries []CommitSummary) string {
	if len(summaries) == 0 {
		return ""
	}

	var buf strings.Builder
	buf.WriteString("## Recent Relevant Commits\n\n")

	for _, s := range summaries {
		hashDisplay := s.Hash
		if len(hashDisplay) > 8 {
			hashDisplay = hashDisplay[:8]
		}
		buf.WriteString(fmt.Sprintf("### Commit `%s`:\n", hashDisplay))
		buf.WriteString(fmt.Sprintf("- **Requirement**: %s\n", s.Requirement))
		buf.WriteString(fmt.Sprintf("- **Files**: %s\n", s.Files))
		buf.WriteString(fmt.Sprintf("- **Approach**: %s\n", s.Approach))
		buf.WriteString(fmt.Sprintf("- **Implementation**: %s\n\n", s.Implementation))
	}

	return buf.String()
}

// ==================== 私有辅助方法 ====================

// summarizeSingleCommit 使用 LLM 为单个 commit 生成结构化摘要
func (cl *CommitLearner) summarizeSingleCommit(ctx context.Context, commit CommitMeta) (CommitSummary, error) {
	// 构建用户提示词
	prompt := fmt.Sprintf("Commit Message:\n%s\n\nChanged Files:\n%s\n\nDiff:\n%s",
		commit.Subject,
		strings.Join(commit.Files, ", "),
		commit.Diff,
	)

	// 构建消息列表
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: cl.config.LLMSystemPrompt},
		{Role: llm.RoleUser, Content: prompt},
	}

	// 调用 LLM
	resp, err := cl.llmEngine.GenerateContent(ctx, messages, nil, nil)
	if err != nil {
		return CommitSummary{}, fmt.Errorf("LLM generate failed: %w", err)
	}

	if len(resp.Choices) == 0 || resp.Choices[0].Content == "" {
		return CommitSummary{}, fmt.Errorf("empty LLM response")
	}

	// 尝试解析 JSON 响应
	var summary CommitSummary
	if err := json.Unmarshal([]byte(resp.Choices[0].Content), &summary); err != nil {
		// 如果不是有效 JSON，使用 LLM 重新提取结构化信息
		return cl.extractSummaryWithLLM(ctx, resp.Choices[0].Content, commit.Hash)
	}
	summary.Hash = commit.Hash

	return summary, nil
}

// extractSummaryWithLLM 使用 LLM 从原始文本中提取结构化 commit 摘要
// 当 LLM 返回非 JSON 响应时调用此方法进行二次提取
func (cl *CommitLearner) extractSummaryWithLLM(ctx context.Context, rawContent string, hash string) (CommitSummary, error) {
	// 构建一个 prompt，要求 LLM 从原始文本中提取结构化信息
	// 如果原始文本本身就是 LLM 的回答但格式不对，让它重新输出 JSON
	prompt := fmt.Sprintf(`The previous attempt to generate a structured commit summary failed. Please extract the following information from this text and output ONLY a valid JSON object (no markdown, no explanation):

{
  "hash": "%s",
  "requirement": "Brief description of what this commit addresses",
  "files": "List of changed files with brief description of changes",
  "approach": "The technical approach or strategy used",
  "implementation": "Key implementation details"
}

Original text:
%s`, hash, rawContent)

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: cl.config.LLMSystemPrompt},
		{Role: llm.RoleUser, Content: prompt},
	}

	// 使用 dedicatedEngine（如果有），否则使用默认引擎
	engine := cl.llmEngine
	if cl.dedicatedEngine != nil {
		engine = cl.dedicatedEngine
	}

	resp, err := engine.GenerateContent(ctx, messages, nil, nil)
	if err != nil {
		return CommitSummary{}, fmt.Errorf("LLM extraction fallback failed: %w", err)
	}

	if len(resp.Choices) == 0 || resp.Choices[0].Content == "" {
		return CommitSummary{}, fmt.Errorf("empty LLM extraction response")
	}

	// 尝试解析 JSON
	var summary CommitSummary
	if err := json.Unmarshal([]byte(resp.Choices[0].Content), &summary); err != nil {
		// 最终降级：从文本中提取
		summary = extractSummaryFromText(resp.Choices[0].Content, hash)
	}
	summary.Hash = hash
	return summary, nil
}

// parseGitLogOutput 解析 git log 命令的输出
//
// 输入格式：
// COMMIT_START
// HASH|SUBJECT|AUTHOR|DATE
// COMMIT_END
// file1
// file2
// ...
// COMMIT_START
// ...
func parseGitLogOutput(output string) ([]CommitMeta, error) {
	var commits []CommitMeta
	lines := strings.Split(output, "\n")

	var current CommitMeta
	var inCommit bool

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "COMMIT_START" {
			inCommit = true
			continue
		}
		if line == "COMMIT_END" {
			inCommit = false
			continue
		}

		if !inCommit {
			continue
		}

		// 解析 COMMIT_HEADER: HASH|SUBJECT|AUTHOR|DATE
		// 判断是否是 header 行：包含 '|' 且不在 diff 块中
		if strings.Contains(line, "|") && !strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "-") {
			parts := strings.SplitN(line, "|", 4)
			if len(parts) == 4 {
				// 新的 commit header，保存之前的 commit
				if current.Hash != "" && current.Subject != "" {
					commits = append(commits, current)
				}
				current = CommitMeta{}

				current.Hash = strings.TrimSpace(parts[0])
				current.Subject = strings.TrimSpace(parts[1])
				current.Author = strings.TrimSpace(parts[2])

				// 解析日期
				if date, err := time.Parse("2006-01-02 15:04:05 +0000", strings.TrimSpace(parts[3])); err == nil {
					current.Date = date
				} else if date, err := time.Parse("2006-01-02 15:04:05 -0700", strings.TrimSpace(parts[3])); err == nil {
					current.Date = date
				} else {
					// 尝试 ISO 格式
					if date, err := time.Parse(time.RFC3339, strings.TrimSpace(parts[3])); err == nil {
						current.Date = date
					}
				}
				continue
			}
		}

		// 文件列表（非 diff 行）
		if strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") || strings.HasPrefix(line, "@") {
			// diff 行，累积到 Diff 字段
			current.Diff += line + "\n"
		} else if strings.HasPrefix(line, "diff --git") {
			// diff 开始行
			current.Diff += line + "\n"
		} else if line != "" && current.Hash != "" {
			// 文件路径
			current.Files = append(current.Files, line)
		}
	}

	// 添加最后一个 commit
	if current.Hash != "" && current.Subject != "" {
		commits = append(commits, current)
	}

	// 截断 diff 过长的 commit（每个最多 5000 字符）
	for i := range commits {
		if len(commits[i].Diff) > 5000 {
			commits[i].Diff = commits[i].Diff[:5000] + "\n... (truncated)"
		}
	}

	return commits, nil
}

// parseSummaryText 解析 "Requirement: ...\nFiles: ...\nApproach: ...\nImplementation: ..." 格式
func parseSummaryText(text string) CommitSummary {
	var summary CommitSummary

	lines := strings.Split(text, "\n")
	currentKey := ""

	for _, line := range lines {
		line = strings.TrimSpace(line)

		switch {
		case strings.HasPrefix(line, "Requirement:") || strings.HasPrefix(line, "requirement:"):
			currentKey = "requirement"
			summary.Requirement = strings.TrimSpace(strings.TrimPrefix(line, "Requirement:"))
			if summary.Requirement == "" {
				summary.Requirement = strings.TrimSpace(strings.TrimPrefix(line, "requirement:"))
			}
		case strings.HasPrefix(line, "Files:") || strings.HasPrefix(line, "files:"):
			currentKey = "files"
			summary.Files = strings.TrimSpace(strings.TrimPrefix(line, "Files:"))
			if summary.Files == "" {
				summary.Files = strings.TrimSpace(strings.TrimPrefix(line, "files:"))
			}
		case strings.HasPrefix(line, "Approach:") || strings.HasPrefix(line, "approach:"):
			currentKey = "approach"
			summary.Approach = strings.TrimSpace(strings.TrimPrefix(line, "Approach:"))
			if summary.Approach == "" {
				summary.Approach = strings.TrimSpace(strings.TrimPrefix(line, "approach:"))
			}
		case strings.HasPrefix(line, "Implementation:") || strings.HasPrefix(line, "implementation:"):
			currentKey = "implementation"
			summary.Implementation = strings.TrimSpace(strings.TrimPrefix(line, "Implementation:"))
			if summary.Implementation == "" {
				summary.Implementation = strings.TrimSpace(strings.TrimPrefix(line, "implementation:"))
			}
		case currentKey != "" && (strings.HasPrefix(line, "-") || strings.HasPrefix(line, "*")):
			// 列表项，追加到当前字段
			item := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(line, "-"), "*"))
			switch currentKey {
			case "requirement":
				summary.Requirement += " " + item
			case "files":
				summary.Files += " " + item
			case "approach":
				summary.Approach += " " + item
			case "implementation":
				summary.Implementation += " " + item
			}
		}
	}

	return summary
}

// extractSummaryFromText 从非 JSON 文本中提取 summary 字段
func extractSummaryFromText(text string, hash string) CommitSummary {
	summary := CommitSummary{Hash: hash}

	// 尝试查找关键字
	if idx := strings.Index(text, "requirement"); idx != -1 {
		summary.Requirement = extractFieldValue(text, "requirement")
	}
	if idx := strings.Index(text, "files"); idx != -1 {
		summary.Files = extractFieldValue(text, "files")
	}
	if idx := strings.Index(text, "approach"); idx != -1 {
		summary.Approach = extractFieldValue(text, "approach")
	}
	if idx := strings.Index(text, "implementation"); idx != -1 {
		summary.Implementation = extractFieldValue(text, "implementation")
	}

	return summary
}

// extractFieldValue 从文本中提取指定关键字后面的值
func extractFieldValue(text string, key string) string {
	lowerText := strings.ToLower(text)
	lowerKey := strings.ToLower(key)

	idx := strings.Index(lowerText, lowerKey)
	if idx == -1 {
		return ""
	}

	// 查找冒号或换行
	rest := text[idx+len(key):]
	colonIdx := strings.Index(rest, ":")
	if colonIdx != -1 {
		rest = rest[colonIdx+1:]
	}

	// 截取到下一个关键字或结尾
	nextKeys := []string{"\n- ", "\n* ", "\n###", "\n##", "Files:", "files:", "Approach:", "approach:",
		"Implementation:", "implementation:", "Requirement:", "requirement:"}
	endIdx := len(rest)
	for _, nk := range nextKeys {
		if ni := strings.Index(rest, nk); ni != -1 && ni < endIdx {
			endIdx = ni
		}
	}

	return strings.TrimSpace(rest[:endIdx])
}

// getCurrentHead 获取当前 git HEAD 的完整 hash
func (cl *CommitLearner) getCurrentHead(repoPath string) (string, error) {
	cmd := exec.Command("git", "-C", repoPath, "rev-parse", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse failed: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// min 返回两个整数中的较小值
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
