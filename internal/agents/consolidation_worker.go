package agents

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"codeactor/internal/llm"
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
}

// NewConsolidationWorker 创建 consolidation 工作器。
// 需要调用 Start() 启动后台 goroutine。
func NewConsolidationWorker(store *RepoMemoryStore, engine llm.Engine) *ConsolidationWorker {
	return &ConsolidationWorker{
		store:  store,
		engine: engine,
		ch:     make(chan *ConsolidationTask, channelBufferSize),
		done:   make(chan struct{}),
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
