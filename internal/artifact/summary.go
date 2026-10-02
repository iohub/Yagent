package artifact

import (
	"fmt"
	"os"
	"unicode/utf8"
)

// EstTokens 保守估算字符串 token 数：
// ASCII 字符约 4 字符/token、非 ASCII 字符（如中文）约 1 字符/token。
// 确定性纯函数，无随机性。
func EstTokens(s string) int {
	return estTokensRunes([]rune(s))
}

// estTokensRunes 直接在 rune 切片上估算，避免 Reduce 二分过程中的 string 拷贝。
func estTokensRunes(runes []rune) int {
	ascii, nonAscii := 0, 0
	for _, r := range runes {
		if r < 128 {
			ascii++
		} else {
			nonAscii++
		}
	}
	return ascii/4 + nonAscii
}

const (
	// DefaultBudgetTokens 分级摘要的默认 token 预算（给 Director 的上下文）。
	DefaultBudgetTokens = 500
	// ReserveForMarker 截断标记行预留的 token 数。
	ReserveForMarker = 60
)

// Reduce 将 fullText 压缩到 budgetTokens 内：
//   - 未超预算：原样返回全文，truncated=false；
//   - 超预算：在 budget-ReserveForMarker 内二分找最大 rune 前缀，
//     前缀末尾在最后 10% 窗口内回退到最近的 '\n' 边界（找不到则不回退），
//     追加截断标记行（\n 结尾），truncated=true。
//
// 确定性好、不引入随机性。
func Reduce(fullText, id string, budgetTokens int) (summary string, truncated bool) {
	if EstTokens(fullText) <= budgetTokens {
		return fullText, false
	}

	runes := []rune(fullText)
	target := budgetTokens - ReserveForMarker
	if target < 1 {
		target = 1 // 防御：预算过小时至少保留一个字符
	}

	// 二分找最大前缀长度使 EstTokens(prefix) <= target（estTokensRunes 单调不减）。
	lo, hi := 0, len(runes)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if estTokensRunes(runes[:mid]) <= target {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	kept := lo
	if kept == 0 {
		kept = 1
	}

	// 在最后 10% 窗口内（rune 域）回退到最近的 '\n' 边界，保留到 '\n'（含）。
	if window := kept / 10; window > 0 {
		searchStart := kept - window
		for i := kept - 1; i >= searchStart; i-- {
			if runes[i] == '\n' {
				kept = i + 1
				break
			}
		}
	}

	keptText := string(runes[:kept])
	keptChars := utf8.RuneCountInString(keptText)
	totalChars := utf8.RuneCountInString(fullText)
	summary = keptText + fmt.Sprintf("\n\n[Truncated: showing first %d of %d chars. Full result stored as artifact %s.]\n", keptChars, totalChars, id)
	return summary, true
}

// Disabled 返回 artifact 功能是否被禁用（kill-switch）：
// 环境变量 YAGENT_ARTIFACT_DISABLE=1 时为 true，运行时回退旧行为。
func Disabled() bool {
	return os.Getenv("YAGENT_ARTIFACT_DISABLE") == "1"
}
