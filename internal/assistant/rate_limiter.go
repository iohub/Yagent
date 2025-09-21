package assistant

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/tmc/langchaingo/llms"
)

// RateLimiter 负责处理429限流错误和重试逻辑
type RateLimiter struct {
	assistant *CodingAssistant
}

// NewRateLimiter 创建新的限流处理器
func NewRateLimiter(assistant *CodingAssistant) *RateLimiter {
	return &RateLimiter{
		assistant: assistant,
	}
}

// HandleRateLimitRetry 处理429限流错误的重试逻辑
func (rl *RateLimiter) HandleRateLimitRetry(ctx context.Context, wsCallback func(messageType string, content string)) error {
	waitTime := InitialRetryWaitTime
	totalWaitTime := time.Duration(0)

	for totalWaitTime < MaxRetryWaitTime {
		log.Info().
			Dur("wait_time", waitTime).
			Dur("total_wait_time", totalWaitTime).
			Msg("Waiting before retry due to rate limit")

		if wsCallback != nil {
			wsCallback("rate_limit_wait", fmt.Sprintf("等待 %v 后重试...", waitTime))
		}

		// 等待指定时间
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(waitTime):
			// 继续执行
		}

		totalWaitTime += waitTime

		// 尝试重新调用API来检查限流是否已解除
		log.Info().Msg("Testing API call after rate limit wait")
		if wsCallback != nil {
			wsCallback("rate_limit_test", "正在测试API调用...")
		}

		// 创建一个简单的测试请求来检查限流状态
		testMessages := []llms.MessageContent{
			llms.TextParts(llms.ChatMessageTypeHuman, "test"),
		}

		// 尝试调用API
		_, err := rl.assistant.client.GenerateCompletionWithTools(ctx, testMessages, nil, nil)
		if err == nil {
			// 没有错误，说明限流已解除
			log.Info().
				Dur("total_wait_time", totalWaitTime).
				Msg("Rate limit resolved, API call successful")

			if wsCallback != nil {
				wsCallback("rate_limit_resolved", "限流已恢复，继续处理")
			}
			return nil
		}

		// 检查是否仍然是429错误
		if !rl.assistant.isRateLimitError(err) {
			// 不是429错误，说明有其他问题，返回错误
			log.Error().Err(err).Msg("Non-rate-limit error during retry")
			return fmt.Errorf("non-rate-limit error during retry: %w", err)
		}

		// 仍然是429错误，继续等待
		log.Info().
			Dur("wait_time", waitTime).
			Dur("total_wait_time", totalWaitTime).
			Msg("Rate limit still active, continuing to wait")

		if wsCallback != nil {
			wsCallback("rate_limit_still_active", fmt.Sprintf("限流仍然存在，继续等待 %v...", waitTime*2))
		}

		// 指数退避：下次等待时间翻倍
		waitTime *= 2

		// 如果等待时间超过最大等待时间，退出循环
		if totalWaitTime+waitTime > MaxRetryWaitTime {
			break
		}
	}

	// 如果超过最大等待时间，返回错误
	if totalWaitTime >= MaxRetryWaitTime {
		err := fmt.Errorf("rate limit retry timeout after %v", MaxRetryWaitTime)
		log.Error().Err(err).Msg("Rate limit retry timeout")
		if wsCallback != nil {
			wsCallback("rate_limit_timeout", fmt.Sprintf("限流重试超时，已等待 %v", MaxRetryWaitTime))
		}
		return err
	}

	return nil
}

// IsRateLimitError 检查错误是否为429限流错误
func (rl *RateLimiter) IsRateLimitError(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()
	return strings.Contains(errStr, fmt.Sprintf("status code: %d", HTTPStatusTooManyRequests)) ||
		strings.Contains(errStr, fmt.Sprintf("429"))
}