package tokenutil

import (
	"strings"
	"testing"
)

func TestEstimateTokens_Empty(t *testing.T) {
	if got := EstimateTokens(""); got != 0 {
		t.Errorf("EstimateTokens(\"\") = %d, want 0", got)
	}
}

func TestEstimateTokens_English(t *testing.T) {
	text := "Hello, world!"
	got := EstimateTokens(text)
	if got <= 0 {
		t.Errorf("EstimateTokens(english) = %d, want > 0", got)
	}
	// cl100k_base 对英文接近 len/4，允许有小幅偏差
	if got < len(text)/4 {
		t.Errorf("EstimateTokens(english) = %d, expected >= %d (len/4)", got, len(text)/4)
	}
}

func TestEstimateTokens_Chinese(t *testing.T) {
	// 中文文本：cl100k_base 下每个中文字符约 1 token，而 len(text)/4 会严重低估
	text := "你好，世界！这是一个中文测试。"
	got := EstimateTokens(text)
	fallback := len(text) / 4
	if got <= fallback {
		t.Errorf("EstimateTokens(chinese) = %d, expected > %d (len/4 fallback)", got, fallback)
	}
	// 8 个中文字符 + 2 个标点 ≈ 10 tokens，至少应明显大于 fallback
	if got < 8 {
		t.Errorf("EstimateTokens(chinese) = %d, expected >= 8", got)
	}
}

func TestEstimateTokens_FallbackOnEmpty(t *testing.T) {
	// 空字符串应始终返回 0（无论是否降级）
	if got := EstimateTokens(""); got != 0 {
		t.Errorf("EstimateTokens(\"\") = %d, want 0", got)
	}
}

func TestEstimateTokens_LargeChinese(t *testing.T) {
	// 构造一段较长的中文文本，验证估算结果合理
	text := strings.Repeat("这是一个中文测试句子。", 100)
	got := EstimateTokens(text)
	// 降级路径下：len/4 至少为正数；正常路径下应显著更大
	if got <= 0 {
		t.Error("EstimateTokens(large chinese) = 0, want > 0")
	}
	// 100 句 × 10 字/句 ≈ 1000 字符，降级估算 ≈ 250，真实应 > 500
	if got < 250 {
		t.Errorf("EstimateTokens(large chinese) = %d, expected >= 250", got)
	}
}

func TestEstimateTokens_Mixed(t *testing.T) {
	text := "Hello, 世界！This is a mixed test."
	got := EstimateTokens(text)
	if got <= 0 {
		t.Errorf("EstimateTokens(mixed) = %d, want > 0", got)
	}
}
