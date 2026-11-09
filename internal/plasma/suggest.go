package plasma

import "yagent/internal/toolspec"

// suggestTool 给幻觉工具名一个最近候选。取向照 models 的三级（完整名→裸名→相似度），
// 但这里只做相似度：完整名已经在 Resolve 里查过了，裸名对工具名没有意义（没有 provider 前缀）。
// 并列时取字典序更小者 —— toolspec.Names() 已排序，所以遍历顺序就是 tie-break，
// 不依赖 map 迭代（那会让诊断每次启动都抖）。
func suggestTool(name string) string {
	const maxDistance = 3
	best, bestDist := "", maxDistance+1
	for _, cand := range toolspec.Names() {
		d := editDistance(name, cand)
		if d < bestDist {
			best, bestDist = cand, d
		}
	}
	if bestDist > maxDistance {
		return ""
	}
	return best
}

// editDistance 是标准 Levenshtein，只按 rune 算（工具名是 ASCII，但别对多字节输入算出字节距离）。
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	curr := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		curr[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			curr[j] = min3(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(rb)]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}
