package agents

import (
	"codeactor/internal/llm"
	"codeactor/internal/memory"
	"context"
	"fmt"
	"log/slog"
	"time"
)

// ============================================================
// LLM Engine → LLMClient Adapter
// ============================================================

// llmEngineAdapter 将 llm.Engine 适配为 memory.LLMClient
type llmEngineAdapter struct {
	engine llm.Engine
}

// Complete 实现 memory.LLMClient 接口
func (a *llmEngineAdapter) Complete(systemPrompt, userPrompt string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: systemPrompt},
		{Role: llm.RoleUser, Content: userPrompt},
	}

	opts := &llm.CallOptions{
		Temperature: 0.1,       // 低温度确保一致性
		MaxTokens:   4096,
	}

	resp, err := a.engine.GenerateContent(ctx, messages, nil, opts)
	if err != nil {
		return "", fmt.Errorf("llm complete failed: %w", err)
	}

	if resp == nil || len(resp.Choices) == 0 {
		return "", fmt.Errorf("llm returned empty response")
	}

	return resp.Choices[0].Content, nil
}

// ============================================================
// SharedMemoryConsolidationRunner
// 后台定期运行共享记忆整合
// ============================================================

// SharedMemoryConsolidationRunner 定期对共享记忆执行LLM深度整合
type SharedMemoryConsolidationRunner struct {
	consolidator *memory.SharedDimensionConsolidator
	interval     time.Duration
	userID       string
	projectID    string
	done         chan struct{}
}

// NewSharedMemoryConsolidationRunner 创建整合运行器
func NewSharedMemoryConsolidationRunner(
	store *memory.SharedDimensionStore,
	engine llm.Engine,
	interval time.Duration,
	userID, projectID string,
) *SharedMemoryConsolidationRunner {
	adapter := &llmEngineAdapter{engine: engine}
	consolidator := memory.NewSharedDimensionConsolidator(store, adapter)
	return &SharedMemoryConsolidationRunner{
		consolidator: consolidator,
		interval:     interval,
		userID:       userID,
		projectID:    projectID,
		done:         make(chan struct{}),
	}
}

// Start 在后台goroutine中启动定期整合
func (r *SharedMemoryConsolidationRunner) Start() {
	go r.run()
	slog.Info("Shared memory consolidation runner started",
		"interval", r.interval,
		"user", r.userID,
		"project", r.projectID,
	)
}

// Stop 停止整合运行器
func (r *SharedMemoryConsolidationRunner) Stop() {
	close(r.done)
}

func (r *SharedMemoryConsolidationRunner) run() {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-r.done:
			slog.Info("Shared memory consolidation runner stopped")
			return
		case <-ticker.C:
			r.consolidate()
		}
	}
}

func (r *SharedMemoryConsolidationRunner) consolidate() {
	slog.Debug("Starting shared memory consolidation",
		"user", r.userID,
		"project", r.projectID,
	)

	errs := r.consolidator.ConsolidateAll(r.userID, r.projectID)
	for _, err := range errs {
		if err != nil {
			slog.Warn("Shared memory consolidation error",
				"error", err,
				"user", r.userID,
				"project", r.projectID,
			)
		}
	}

	if len(errs) == 0 {
		slog.Debug("Shared memory consolidation completed",
			"user", r.userID,
			"project", r.projectID,
		)
	}
}
