package llm

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"

	"codeactor/internal/config"
)

// fallbackEntry 封装一个fallback engine及其元数据
type fallbackEntry struct {
	name   string
	engine Engine
	weight int
}

// FallbackEngine 实现 Engine 接口，支持带权重的故障转移。
// 当主引擎失败时，按权重降序尝试fallback引擎。
type FallbackEngine struct {
	primary   Engine
	fallbacks []fallbackEntry // 已按weight降序排列
	name      string          // primary provider name，用于日志
	mu        sync.RWMutex
}

// NewFallbackEngine 创建一个带故障转移能力的Engine。
// primary: 主引擎
// primaryName: 主provider名称（用于日志）
// fallbackCfgs: fallback provider配置列表（会被排序）
// createEngine: 工厂函数，根据provider名称创建引擎
func NewFallbackEngine(primary Engine, primaryName string, fallbackCfgs []config.FallbackProvider, allProviders map[string]config.ProviderConfig, llmCfg config.LLMConfig) *FallbackEngine {
	fe := &FallbackEngine{
		primary: primary,
		name:    primaryName,
	}

	// 按weight降序排列
	sorted := make([]config.FallbackProvider, 0, len(fallbackCfgs))
	for _, fp := range fallbackCfgs {
		// 过滤：跳过不存在的provider
		providerCfg, exists := allProviders[fp.Provider]
		if !exists {
			slog.Warn("Fallback provider not found, skipping", "provider", fp.Provider, "primary", primaryName)
			continue
		}
		// 过滤：跳过与primary同名的provider（防止自引用）
		if fp.Provider == primaryName {
			slog.Warn("Fallback provider references itself, skipping", "provider", fp.Provider)
			continue
		}
		sorted = append(sorted, fp)
		_ = providerCfg // 用于后续创建engine
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Weight > sorted[j].Weight
	})

	// 为每个fallback创建引擎（使用fallback专有的重试次数）
	fallbackCfg := llmCfg
	if llmCfg.FallbackMaxRetries > 0 {
		fallbackCfg.MaxRetries = llmCfg.FallbackMaxRetries
	}

	for _, fp := range sorted {
		providerCfg := allProviders[fp.Provider]
		engine := NewEngine(&providerCfg, fallbackCfg)
		loggingEngine := &LoggingEngine{inner: engine}
		fe.fallbacks = append(fe.fallbacks, fallbackEntry{
			name:   fp.Provider,
			engine: loggingEngine,
			weight: fp.Weight,
		})
		slog.Info("Registered fallback provider", "primary", primaryName, "fallback", fp.Provider, "weight", fp.Weight)
	}

	return fe
}

// Model 返回主引擎的模型名。
func (fe *FallbackEngine) Model() string {
	return fe.primary.Model()
}

// CloseIdleConnections 关闭所有引擎（主+fallback）的空闲连接。
func (fe *FallbackEngine) CloseIdleConnections() {
	fe.primary.CloseIdleConnections()
	fe.mu.RLock()
	defer fe.mu.RUnlock()
	for _, fb := range fe.fallbacks {
		fb.engine.CloseIdleConnections()
	}
}

// GenerateContent 实现 Engine 接口，带故障转移。
// 先尝试主引擎，失败后按权重顺序尝试fallback引擎。
func (fe *FallbackEngine) GenerateContent(ctx context.Context, messages []Message, tools []ToolDef, opts *CallOptions) (*Response, error) {
	// 1. 尝试主引擎
	resp, err := fe.primary.GenerateContent(ctx, messages, tools, opts)
	if err == nil {
		return resp, nil
	}

	primaryErr := err
	slog.Warn("Primary provider failed, attempting fallbacks",
		"provider", fe.name,
		"model", fe.primary.Model(),
		"error", primaryErr,
	)
	LogLLMError("Primary provider failed, starting fallback",
		"provider", fe.name,
		"model", fe.primary.Model(),
		"error", primaryErr,
	)

	// 2. 按权重顺序尝试fallback引擎
	fe.mu.RLock()
	fallbacks := make([]fallbackEntry, len(fe.fallbacks))
	copy(fallbacks, fe.fallbacks)
	fe.mu.RUnlock()

	if len(fallbacks) == 0 {
		slog.Warn("No fallback providers configured, returning primary error",
			"provider", fe.name,
			"error", primaryErr,
		)
		return nil, fmt.Errorf("primary provider '%s' failed and no fallback providers configured: %w", fe.name, primaryErr)
	}

	var lastErr error
	for _, fb := range fallbacks {
		slog.Info("Attempting fallback provider",
			"primary", fe.name,
			"fallback", fb.name,
			"model", fb.engine.Model(),
			"weight", fb.weight,
		)

		// 检查context是否已取消
		if ctx.Err() != nil {
			return nil, fmt.Errorf("fallback aborted: context cancelled: %w", ctx.Err())
		}

		resp, err := fb.engine.GenerateContent(ctx, messages, tools, opts)
		if err == nil {
			slog.Info("Fallback provider succeeded",
				"primary", fe.name,
				"fallback", fb.name,
				"model", fb.engine.Model(),
			)
			LogLLMContent("Fallback Success", fmt.Sprintf("Primary '%s' failed, fallback '%s' (model: %s) succeeded.\nPrimary error: %v",
				fe.name, fb.name, fb.engine.Model(), primaryErr))
			return resp, nil
		}

		slog.Warn("Fallback provider also failed",
			"primary", fe.name,
			"fallback", fb.name,
			"error", err,
		)
		LogLLMError("Fallback provider failed",
			"primary", fe.name,
			"fallback", fb.name,
			"error", err,
		)
		lastErr = err
	}

	return nil, fmt.Errorf("all providers failed (primary '%s' + %d fallbacks): primary error: %w; last fallback error: %v",
		fe.name, len(fallbacks), primaryErr, lastErr)
}
