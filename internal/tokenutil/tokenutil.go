// Package tokenutil 提供基于 tiktoken-go 的 token 数估算工具。
//
// 使用 cl100k_base 编码（适用于 GPT-4/GPT-3.5/Codex 系列模型），
// 通过包级 sync.Once 缓存编码器实例，避免重复加载 BPE 词表。
// 若 BPE 加载失败（离线环境等），自动降级为 len(text)/4 估算，
// 保证不 panic、不崩溃。
package tokenutil

import (
	"log/slog"
	"sync"

	"github.com/pkoukk/tiktoken-go"
)

// encoder 是包级缓存的 tiktoken 编码器实例，只初始化一次。
var encoder *tiktoken.Tiktoken

// encoderInit 负责一次性加载 cl100k_base 编码器。
var encoderInit sync.Once

// estimateFallback 记录降级估算是否已启用（warn 只打印一次）。
var estimateFallback sync.Once

// EstimateTokens 使用 tiktoken-go cl100k_base 精确估算文本的 token 数量。
//
// 成功路径：调用 Encode(text, nil, nil) 并返回 token 切片长度。
// 降级路径：若编码器未就绪或 Encode 返回 nil，退化为 len(text)/4 估算。
// 降级仅首次发生时通过 slog.Warn 记录一次，之后静默。
func EstimateTokens(text string) int {
	encoderInit.Do(func() {
		var err error
		encoder, err = tiktoken.GetEncoding("cl100k_base")
		if err != nil {
			slog.Warn("tokenutil: 无法加载 cl100k_base 编码器，将降级为 len/4 估算", "error", err)
		}
	})

	if encoder == nil {
		estimateFallback.Do(func() {
			slog.Warn("tokenutil: 编码器加载失败，后续所有 token 估算使用 len(text)/4 降级")
		})
		return len(text) / 4
	}

	tokens := encoder.Encode(text, nil, nil)
	if tokens == nil {
		return len(text) / 4
	}
	return len(tokens)
}
