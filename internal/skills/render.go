package skills

import (
	"strconv"
	"strings"
)

// Catalog 渲染注入 system prompt 的目录块。
//
// 形状与 deferred 工具目录同族（一行一个 name: description + 一行截断标记）：
// 它是稳定前缀的一部分，所以排序必须是确定性的（按名字字典序），
// 否则每次启动 provider 的前缀缓存都会失效。
//
// maxTokens<=0 表示不限预算。没有技能时返回空串 —— 调用方不该注入一个空标题
// （与 Surface.Catalog() 同语义）。
func (r *Registry) Catalog(maxTokens int) string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(r.skills) == 0 {
		return ""
	}

	names := sortedNames(r.skills)
	lines := make([]string, 0, len(names))
	for _, name := range names {
		lines = append(lines, "- "+name+": "+shorten(r.skills[name].Description, r.opts.LineChars))
	}

	header := []string{
		"## Skills",
		"Reusable procedures the user wrote for this project. Call `use_skill` with a name to fetch its full body, then follow it.",
	}

	// 预算贪心：从头保留，超出就把尾部换成一行截断标记。
	// 标记必须写进正文 —— 只写日志的话模型不知道自己看到的不是全部（蓝图 TRUNC 的同一教训）。
	kept := len(lines)
	if maxTokens > 0 {
		kept = 0
		used := estimateTokens(strings.Join(header, "\n"))
		for _, line := range lines {
			cost := estimateTokens(line)
			// 至少保留一行：预算小到装不下一条时，宁可超一点也不能让目录变成空标题。
			if used+cost > maxTokens && kept > 0 {
				break
			}
			used += cost
			kept++
		}
	}

	out := append([]string(nil), header...)
	out = append(out, lines[:kept]...)
	if hidden := len(lines) - kept; hidden > 0 {
		out = append(out, "("+strconv.Itoa(hidden)+" more not shown — call use_skill with the name directly)")
	}
	return strings.Join(out, "\n")
}

// shorten 把单行描述截到 limit rune（尾部省略）。
// 不用中间省略：目录行是给人扫的，保留开头比保留结尾有用。
func shorten(text string, limit int) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\n", " "))
	if limit <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	if limit <= 1 {
		return string(runes[:limit])
	}
	return string(runes[:limit-1]) + "…"
}

// estimateTokens 用 len/4 近似（与 surface.go 的预算口径一致）。
// 不引 tiktoken：这段只为决定截几行，每次 Run 都可能调用。
func estimateTokens(s string) int { return len(s)/4 + 1 }
