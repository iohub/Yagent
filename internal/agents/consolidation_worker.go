package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"codeactor/internal/config"
	"codeactor/internal/llm"
	"codeactor/internal/logging"
	"codeactor/internal/mcp"
	"codeactor/internal/tools"
)

// ============================================================================
// Constants
// ============================================================================

const (
	// channelBufferSize 是 consolidation 请求通道的缓冲区大小
	channelBufferSize = 16

	// consolidationTimeout 是单次 consolidation LLM 调用的超时时间
	consolidationTimeout = 30 * time.Second

	// maxConsolidationRetries 是 LLM 调用最大重试次数
	maxConsolidationRetries = 2

	// pruneTriggerInterval 每 N 次 consolidation 触发一次 merge prune
	pruneTriggerInterval = 10

	// knowledgeExtractTimeout 是知识提取 LLM 调用的超时时间
	knowledgeExtractTimeout = 60 * time.Second
)

// ============================================================================
// ConsolidationTask
// ============================================================================

// ConsolidationTask 封装一次记忆整理任务的输入。
type ConsolidationTask struct {
	// NewObservations 是本次 RepoAgent.Run() 的输出文本
	NewObservations string
}

// ============================================================================
// ConsolidationWorker
// ============================================================================

// ConsolidationWorker 异步记忆整理工作器。
// 使用单 goroutine + channel 串行处理 consolidation 请求，
// 确保对 RepoMemoryStore 的串行写入。
type ConsolidationWorker struct {
	store  *RepoMemoryStore
	engine llm.Engine
	ch     chan *ConsolidationTask
	done   chan struct{}
	// [知识管理]
	mcpClient          *mcp.MCPClient
	knowledgeCfg       config.KnowledgeConfig
	consolidationCount int
}

// NewConsolidationWorker 创建 consolidation 工作器。
// 需要调用 Start() 启动后台 goroutine。
func NewConsolidationWorker(store *RepoMemoryStore, engine llm.Engine, mcpClient *mcp.MCPClient, knowledgeCfg config.KnowledgeConfig) *ConsolidationWorker {
	return &ConsolidationWorker{
		store:        store,
		engine:       engine,
		ch:           make(chan *ConsolidationTask, channelBufferSize),
		done:         make(chan struct{}),
		mcpClient:    mcpClient,
		knowledgeCfg: knowledgeCfg,
	}
}

// Start 启动后台 goroutine 开始处理 consolidation 请求。
func (w *ConsolidationWorker) Start() {
	go w.run()
}

// Stop 优雅停止工作器。等待所有排队的任务处理完成。
func (w *ConsolidationWorker) Stop() {
	close(w.ch)
	<-w.done
}

// Submit 提交 consolidation 请求到通道。
// 非阻塞：如果通道已满，返回 false 并丢弃请求（优雅降级）。
func (w *ConsolidationWorker) Submit(task *ConsolidationTask) bool {
	select {
	case w.ch <- task:
		return true
	default:
		slog.Warn("ConsolidationWorker: channel full, dropping request")
		return false
	}
}

// run 是后台 goroutine 的主循环。
func (w *ConsolidationWorker) run() {
	defer close(w.done)
	for task := range w.ch {
		w.process(task)
	}
}

// process 执行一次记忆整理。
func (w *ConsolidationWorker) process(task *ConsolidationTask) {
	// 1. 读取当前记忆
	currentMem := w.store.Get()

	// 2. 截断过长的观察文本
	obs := TruncateObservations(task.NewObservations)
	if obs == "" {
		return
	}

	// 3. 调用 LLM 进行 consolidation（带重试）
	consolidated, err := w.callConsolidationLLM(currentMem, obs)
	if err != nil {
		slog.Warn("ConsolidationWorker: LLM consolidation failed",
			"error", err,
			"will_retry", false,
		)
		return
	}

	consolidated = strings.TrimSpace(consolidated)
	if consolidated == "" {
		slog.Warn("ConsolidationWorker: LLM returned empty consolidation")
		return
	}

	// 4. 执行 token 预算检查
	consolidated = EnforceTokenBudget(consolidated)

	// 5. 验证 Markdown 格式（浅层检查）
	if !ValidateMemoryFormat(consolidated) {
		slog.Warn("ConsolidationWorker: invalid memory format, keeping old memory",
			"content_preview", truncatePreview(consolidated, 200),
		)
		return
	}

	// 6. 持久化到 SharedMemory
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := w.store.Save(ctx, consolidated); err != nil {
		slog.Warn("ConsolidationWorker: save failed",
			"error", err,
		)
		return
	}

	slog.Info("ConsolidationWorker: memory updated successfully",
		"size", len(consolidated),
		"tokens_est", EstimateTokens(consolidated),
	)

	// 7. 将记忆整理结果写入独立的日志文件
	w.writeConsolidationFile(consolidated)

	// 8. [知识管理] 知识提取阶段
	w.consolidationCount++
	if w.mcpClient != nil && w.knowledgeCfg.Enabled {
		w.extractKnowledge(consolidated)
	}
	// 9. [知识管理] 周期 prune
	if w.consolidationCount%pruneTriggerInterval == 0 {
		w.triggerPruneMerge()
	}
}

// writeConsolidationFile 将记忆整理结果写入独立的日志文件。
// 文件路径：~/.codeactor/logs/memory-consolidated-YYYY-MM-DD.log
// 每次写入包含时间戳分隔线和完整内容，便于查阅和回溯。
func (w *ConsolidationWorker) writeConsolidationFile(content string) {
	logDir := logging.GetLogDir()
	filename := fmt.Sprintf("memory-consolidated-%s.log", time.Now().Format("2006-01-02"))
	path := filepath.Join(logDir, filename)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		slog.Warn("ConsolidationWorker: failed to open consolidation log file",
			"path", path,
			"error", err,
		)
		return
	}
	defer f.Close()

	now := time.Now().Format("2006-01-02 15:04:05.000")
	separator := strings.Repeat("=", 60)
	header := fmt.Sprintf("\n%s\n[%s] Memory Consolidation Update\n%s\n\n", separator, now, separator)

	if _, err := f.WriteString(header); err != nil {
		slog.Warn("ConsolidationWorker: failed to write header to consolidation log",
			"error", err,
		)
		return
	}

	if _, err := f.WriteString(content); err != nil {
		slog.Warn("ConsolidationWorker: failed to write content to consolidation log",
			"error", err,
		)
		return
	}

	if _, err := f.WriteString("\n\n"); err != nil {
		slog.Warn("ConsolidationWorker: failed to write trailing newlines",
			"error", err,
		)
	}
}

// extractKnowledge 从整理后的记忆文本中提取知识条目，写入知识库。
func (w *ConsolidationWorker) extractKnowledge(consolidated string) {
	ctx, cancel := context.WithTimeout(context.Background(), knowledgeExtractTimeout)
	defer cancel()

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "你是一个知识提取助手。请严格按照要求输出 JSON 格式，不要输出任何其他内容。"},
		{Role: llm.RoleUser, Content: fmt.Sprintf(knowledgeExtractionPrompt, consolidated)},
	}

	resp, err := w.engine.GenerateContent(ctx, messages, nil, &llm.CallOptions{
		Temperature: 0.1,
		MaxTokens:   2048,
	})
	if err != nil {
		slog.Warn("ConsolidationWorker: knowledge extraction LLM call failed", "error", err)
		return
	}
	if len(resp.Choices) == 0 {
		return
	}

	raw := strings.TrimSpace(resp.Choices[0].Content)
	// 去除可能的 markdown 围栏
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var entries []struct {
		Type       string   `json:"type"`
		Title      string   `json:"title"`
		Content    string   `json:"content"`
		Tags       []string `json:"tags"`
		Confidence float64  `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		slog.Warn("ConsolidationWorker: failed to parse knowledge extraction JSON", "error", err)
		return
	}
	if len(entries) == 0 {
		return
	}

	kl := logging.KnowledgeLogger()
	agentTypeMap := map[string]string{
		"repo_retrieval":      "repo_agent",
		"coding_modification": "coding_agent",
	}

	extractTool := tools.NewConsolidateKnowledgeTool(w.mcpClient, w.engine)
	for _, entry := range entries {
		sourceAgent, _ := agentTypeMap[entry.Type]
		params := map[string]interface{}{
			"type":         entry.Type,
			"title":        entry.Title,
			"content":      entry.Content,
			"tags":         entry.Tags,
			"source_agent": sourceAgent,
			"task_id":      "",
			"confidence":   entry.Confidence,
		}
		kl.Debug("consolidation worker submit entry", "event", "worker_submit_entry", "title", entry.Title, "type", entry.Type)
		if _, err := extractTool.Execute(ctx, params); err != nil {
			slog.Warn("ConsolidationWorker: knowledge extract failed for entry",
				"type", entry.Type, "title", entry.Title, "error", err,
			)
		}
	}
	slog.Info("ConsolidationWorker: knowledge extraction completed", "entries", len(entries))
	kl.Info("consolidation worker extracted", "event", "worker_extract_done", "count", len(entries))
}

// triggerPruneMerge 触发知识库条目合并去重。
func (w *ConsolidationWorker) triggerPruneMerge() {
	kl := logging.KnowledgeLogger()
	kl.Info("consolidation worker trigger prune merge", "event", "worker_prune_trigger", "interval", 10)
	ctx, cancel := context.WithTimeout(context.Background(), knowledgeExtractTimeout)
	defer cancel()

	pruneTool := tools.NewPruneHistoryTool(w.mcpClient, w.engine)
	if _, err := pruneTool.Execute(ctx, map[string]interface{}{
		"action":               "merge",
		"limit":                200,
		"similarity_threshold": 0.80,
	}); err != nil {
		slog.Warn("ConsolidationWorker: prune merge failed", "error", err)
	} else {
		slog.Info("ConsolidationWorker: prune merge completed")
	}
}

// callConsolidationLLM 调用 LLM 进行记忆合并。
// 带重试逻辑（最多 maxConsolidationRetries 次）。
func (w *ConsolidationWorker) callConsolidationLLM(currentMem string, observations string) (string, error) {
	var lastErr error

	for attempt := 0; attempt <= maxConsolidationRetries; attempt++ {
		if attempt > 0 {
			// 指数退避：1s, 2s
			wait := time.Duration(1<<(attempt-1)) * time.Second
			slog.Info("ConsolidationWorker: retrying LLM call",
				"attempt", attempt,
				"wait", wait,
			)
			time.Sleep(wait)
		}

		ctx, cancel := context.WithTimeout(context.Background(), consolidationTimeout)
		defer cancel()

		userContent := fmt.Sprintf(
			"<current-memory>\n%s\n</current-memory>\n\n<new-observations>\n%s\n</new-observations>\n\nToken budget: %d tokens. Output ONLY the updated Markdown memory with EXACTLY the six sections.",
			currentMem, observations, MaxMemoryTokens,
		)

		messages := []llm.Message{
			{Role: llm.RoleSystem, Content: consolidationSystemPrompt},
			{Role: llm.RoleUser, Content: userContent},
		}

		resp, err := w.engine.GenerateContent(ctx, messages, nil, nil)
		if err != nil {
			lastErr = err
			slog.Warn("ConsolidationWorker: LLM call failed",
				"attempt", attempt,
				"error", err,
			)
			continue
		}

		if len(resp.Choices) == 0 {
			lastErr = fmt.Errorf("LLM returned empty choices")
			continue
		}

		return resp.Choices[0].Content, nil
	}

	return "", fmt.Errorf("LLM consolidation failed after %d retries: %w", maxConsolidationRetries+1, lastErr)
}

// truncatePreview 截断字符串用于日志预览。
func truncatePreview(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// ============================================================================
// Knowledge Extraction Prompt
// ============================================================================

const knowledgeExtractionPrompt = `请从以下整理结果中提取可存入知识库的条目。
要求：
1. 每条知识只记录一个独立发现或经验
2. 内容必须包含文件路径、函数名或调用链坐标
3. 语言简洁，只记核心，不记过程
4. tags 至少 1 个，用于语义检索
5. type 取值 repo_retrieval（检索发现）或 coding_modification（编码修改）

输出 JSON 数组格式（不要输出其他内容）：
[
  {"type": "repo_retrieval", "title": "标题（≤30字）", "content": "内容（≤500字）", "tags": ["标签1"], "confidence": 0.9}
]

如果没有可提取的知识，输出 []。

整理结果：
%s`

// ============================================================================
// Consolidation System Prompt
// ============================================================================

const consolidationSystemPrompt = `You are a memory consolidation engine for a repository analysis system. Your job is to merge NEW observations about a codebase into an existing structured memory document.

## Output Format
Produce a Markdown document with EXACTLY these six section headings, in this order:

## Architecture
## Patterns
## Conventions
## Dependencies
## Gotchas
## Key Files

## Consolidation Rules

1. MERGE: Integrate new observations into the appropriate sections. Each new fact should be added as a new bullet point under the relevant section heading.
2. DEDUPLICATE: Remove redundant or overlapping information within and across sections.
3. PRIORITIZE: Keep concrete, actionable facts. Drop vague or speculative observations.
4. CONCISE: Use bullet points. One fact per line. No prose paragraphs.
5. PRESERVE: Keep existing valid information unless explicitly contradicted by new observations.
6. UPDATE: If new observations contradict existing memory, prefer the newer information.
7. EMPTY: If a section has no data after merging, write "(No data yet)" under the heading.
8. BUDGET: Your output MUST NOT exceed the specified token budget. If you estimate you are over budget, drop the least important items first. Priority order for dropping: Gotchas > Key Files > Dependencies > Conventions > Patterns > Architecture.
9. OUTPUT: Produce ONLY the Markdown content. No explanations, no meta-commentary, no wrapping tags.

## Quality Guidelines

- BE SPECIFIC: Include file paths, function names, and concrete details when available.
- BE CORRECT: Only include knowledge you are confident about from the observations.
- BE STRUCTURED: Maintain clean Markdown formatting at all times.`
