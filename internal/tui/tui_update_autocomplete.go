package tui

import (
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// autocompleteMsg is sent by the debounce timer to trigger autocomplete computation.
type autocompleteMsg struct{}

// ─────────────────────────────────────────────────────────────────────────────
// 补全防抖与缓存辅助函数
// ─────────────────────────────────────────────────────────────────────────────

// shouldAttemptCompletion 检查是否有触发补全的条件
// 优化：仅检查基本状态和光标位置，避免调用 m.input.Value()
// 实际字符检查在 scheduleAutocomplete 中进行
func (m *model) shouldAttemptCompletion() bool {
	if m.commandMode || m.taskRunning {
		return false
	}

	// 快速检查：光标在有效范围内（Column 从 0 开始，0 表示行首）
	// 如果 Column > 0 或者 Line > 0，说明光标不在文档开头
	column := m.input.Column()
	if column > 0 {
		return true
	}

	// Column == 0 时，如果 Line > 0，说明光标在后续行的行首，也是有效的
	if m.input.Line() > 0 {
		return true
	}

	// 光标在文档开头（Line=0, Column=0），不触发补全
	return false
}

// clearAutocomplete 清除补全状态
func (m *model) clearAutocomplete() {
	m.skillAutoComplete = false
	m.skillSuggestions = nil
	m.skillSuggestionIdx = 0
	m.keywordAutoComplete = false
	m.keywordSuggestions = nil
	m.keywordSuggestionIdx = 0
}

// scheduleAutocomplete 启动或重置补全防抖
// 优化：
// 1. 使用细粒度缓存键（单词+是否有/）提高命中率
// 2. 只调用一次 Value() 和 Line()/Column()
// 3. 共享 rune 切片给后续函数
// 4. 防抖时间增加到 100ms
func (m *model) scheduleAutocomplete() tea.Cmd {
	// 提前退出检查：无触发条件时直接清除补全
	if !m.shouldAttemptCompletion() {
		m.clearAutocomplete()
		return nil
	}

	// 获取当前输入快照（只调用一次）
	text := m.input.Value()
	line := m.input.Line()
	column := m.input.Column()

	// 提取光标前的单词和检查是否有 /（只转换一次 rune 切片）
	contentRunes := []rune(text)
	word := extractWordAtCursorRunes(contentRunes, column)
	hasSlash := hasSlashAtWordBoundary(text)

	cacheKey := autocompleteCacheKey{word: word, hasSlash: hasSlash}

	// 检查缓存
	if cached, ok := m.autocompleteCache[cacheKey]; ok {
		// 缓存命中，直接应用结果
		result := *cached
		m.skillSuggestions = result.skillSuggestions
		m.skillSuggestionIdx = result.skillSuggestionIdx
		m.keywordSuggestions = result.keywordSuggestions
		m.keywordSuggestionIdx = result.keywordSuggestionIdx
		return nil
	}

	// 缓存未命中，启动或重置防抖
	if m.debounceTimer != nil {
		m.debounceTimer.Stop()
		// drain timer channel to prevent leak
		select {
		case <-m.debounceTimer.C:
		default:
		}
	}

	// 保存快照
	m.snapshotText = text
	m.snapshotCursor = line*10000 + column // 编码 line 和 column
	m.pendingAutocomplete = true

	// 启动 100ms 防抖（从 50ms 增加到 100ms，减少快速输入时的计算频率）
	m.debounceTimer = time.NewTimer(100 * time.Millisecond)

	// 返回 Cmd 用于在防抖超时后发送 autocompleteMsg
	return func() tea.Msg {
		// 等待 100ms 后发送消息
		return autocompleteMsg{}
	}
}

// doAutocomplete 执行补全计算（接收已提取的文本和光标，避免重复调用 Value()）
// 优化：共享 rune 切片，避免在多个函数中重复转换
func (m *model) doAutocomplete(text string, cursor int) {
	// 只转换一次 rune 切片
	contentRunes := []rune(text)

	// 执行技能补全
	m.doSkillAutocomplete(text, contentRunes, cursor)

	// 执行关键词补全
	m.doKeywordAutocomplete(text, contentRunes, cursor)
}

// doSkillAutocomplete 执行技能补全计算
// 优化：接收 contentRunes 参数（为未来优化预留）
func (m *model) doSkillAutocomplete(text string, contentRunes []rune, cursor int) {
	// 仅在编辑模式且任务未运行时激活
	if m.commandMode || m.taskRunning {
		m.skillAutoComplete = false
		m.skillSuggestions = nil
		m.skillSuggestionIdx = 0
		return
	}

	// 查找最后一个 '/'，且必须在单词边界上（避免粘贴 URL/路径时误触发）
	lastSlash := strings.LastIndex(text, "/")
	if lastSlash < 0 || !isSlashAtWordBoundary(text, lastSlash) {
		m.skillAutoComplete = false
		m.skillSuggestions = nil
		m.skillSuggestionIdx = 0
		return
	}

	// 提取 '/' 之后的文本作为查询
	query := text[lastSlash+1:]

	// 构建匹配的技能列表
	var matches []string
	if m.assistant.SkillRegistry != nil {
		allSkills := m.assistant.SkillRegistry.List()
		matches = make([]string, 0, len(allSkills)+1)
		for _, name := range allSkills {
			if hasPrefixIgnoreCase(name, query) {
				matches = append(matches, name)
			}
		}

		// 添加 "history" 作为内置命令
		if hasPrefixIgnoreCase("history", query) {
			matches = append([]string{"history"}, matches...)
		}
	}

	if len(matches) > 0 {
		m.skillAutoComplete = true
		m.skillSuggestions = matches
		// 重置索引如果超出范围
		if m.skillSuggestionIdx >= len(matches) {
			m.skillSuggestionIdx = 0
		}
	} else {
		m.skillAutoComplete = false
		m.skillSuggestions = nil
		m.skillSuggestionIdx = 0
	}
}

// doKeywordAutocomplete 执行关键词补全计算
// 优化：
// 1. 接收 contentRunes 参数，避免重复转换
// 2. 如果光标在第一行，避免调用 splitLogicalLines
func (m *model) doKeywordAutocomplete(text string, contentRunes []rune, cursor int) {
	// 检查配置：如果补全功能被禁用，直接返回
	if !m.keywordCompletionCfg.enabled {
		m.keywordAutoComplete = false
		m.keywordSuggestions = nil
		m.keywordSuggestionIdx = 0
		return
	}

	// 仅在编辑模式且任务未运行时激活
	if m.commandMode || m.taskRunning {
		m.keywordAutoComplete = false
		m.keywordSuggestions = nil
		m.keywordSuggestionIdx = 0
		return
	}

	// 将编码的 cursor 解码为实际的光标位置（字节偏移量）
	// cursor = line*10000 + column
	line := cursor / 10000
	column := cursor % 10000

	// 计算光标前的字节偏移量
	byteOffset := 0
	if line > 0 {
		// 只有在多行时才调用 splitLogicalLines
		lines := splitLogicalLines(contentRunes, line)
		for _, l := range lines {
			byteOffset += len(l) + 1 // +1 for newline
		}
	}
	byteOffset += column

	// 提取光标前的单词
	word := extractWordAtCursorRunes(contentRunes, byteOffset)

	// 仅在单词非空且无特殊前缀时触发
	if word == "" || strings.HasPrefix(word, "/") {
		m.keywordAutoComplete = false
		m.keywordSuggestions = nil
		m.keywordSuggestionIdx = 0
		return
	}

	// 从关键词字典获取补全建议
	suggestions := GetKeywordCompletions(m, word, 20)

	if len(suggestions) > 0 {
		m.keywordAutoComplete = true
		m.keywordSuggestions = suggestions
		m.keywordSuggestionIdx = 0
	} else {
		m.keywordAutoComplete = false
		m.keywordSuggestions = nil
		m.keywordSuggestionIdx = 0
	}
}

// cleanupDebounceTimer 清理防抖定时器（在退出时调用）
func (m *model) cleanupDebounceTimer() {
	if m.debounceTimer != nil {
		m.debounceTimer.Stop()
		// drain timer channel to prevent leak
		select {
		case <-m.debounceTimer.C:
		default:
		}
		m.debounceTimer = nil
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// handleAutocompleteMsg — 原 Update case autocompleteMsg 提取
// ─────────────────────────────────────────────────────────────────────────────

func (m *model) handleAutocompleteMsg(msg autocompleteMsg) (tea.Model, tea.Cmd) {
	if !m.pendingAutocomplete {
		return m, nil
	}

	// 检查快照是否仍然有效
	if currentText := m.input.Value(); currentText != m.snapshotText {
		// 输入已变化，重新调度
		m.scheduleAutocomplete()
		return m, nil
	}

	// 执行补全计算
	m.doAutocomplete(m.snapshotText, m.snapshotCursor)
	// 补全状态变化时刷新 footer 缓存以正确计算 viewport 高度
	m.invalidateFooterCache()

	// 存入缓存 - 使用细粒度缓存键（基于单词和是否有/）
	contentRunes := []rune(m.snapshotText)
	column := m.input.Column()
	word := extractWordAtCursorRunes(contentRunes, column)
	hasSlash := hasSlashAtWordBoundary(m.snapshotText)
	cacheKey := autocompleteCacheKey{word: word, hasSlash: hasSlash}
	m.autocompleteCache[cacheKey] = &AutocompleteResult{
		skillSuggestions:     m.skillSuggestions,
		skillSuggestionIdx:   m.skillSuggestionIdx,
		keywordSuggestions:   m.keywordSuggestions,
		keywordSuggestionIdx: m.keywordSuggestionIdx,
	}

	// 清理缓存大小（防止内存泄漏）
	if len(m.autocompleteCache) > 64 {
		m.autocompleteCache = make(map[autocompleteCacheKey]*AutocompleteResult)
	}

	m.pendingAutocomplete = false
	return m, nil
}

// extractWordAtCursorRunes extracts the word immediately before the cursor position
// from a pre-computed runes slice. This avoids the overhead of converting the
// string to runes again when the runes are already available.
func extractWordAtCursorRunes(runes []rune, cursorPos int) string {
	if cursorPos <= 0 || cursorPos > len(runes) {
		return ""
	}

	// Extract backwards from cursor position using append (O(n) instead of O(n²))
	var word []rune
	for i := cursorPos - 1; i >= 0; i-- {
		r := runes[i]
		// Allow alphanumeric, underscore, and common Chinese characters
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || (r >= 0x4e00 && r <= 0x9fff) {
			word = append(word, r)
		} else {
			break
		}
	}

	// Reverse the word (collected backwards)
	for i, j := 0, len(word)-1; i < j; i, j = i+1, j-1 {
		word[i], word[j] = word[j], word[i]
	}

	return string(word)
}

// splitLogicalLines splits the content runes into logical lines (separated by newlines).
// It returns the first `count` logical lines from the content.
//
// Optimized for early termination: uses efficient slices.Index to skip directly to
// newline positions, and returns immediately once count lines are found.
func splitLogicalLines(content []rune, count int) [][]rune {
	if count <= 0 {
		return nil
	}

	result := make([][]rune, 0, count)
	// Use slices.Index for efficient newline scanning
	rest := content
	for len(rest) > 0 {
		// Find next newline position (fast slice search)
		idx := slices.Index(rest, '\n')
		if idx < 0 {
			// No more newlines: remaining content is the last line
			if len(rest) > 0 {
				result = append(result, rest)
			}
			break
		}
		// Extract line before newline (exclusive)
		result = append(result, rest[:idx])
		rest = rest[idx+1:] // Skip the newline character
		if len(result) >= count {
			return result
		}
	}
	return result
}

// hasPrefixIgnoreCase reports whether string s starts with prefix, ignoring case.
// Uses byte-level comparison for ASCII characters, avoiding the overhead of
// strings.ToLower() which creates a new string allocation on every call.
// This is safe for ASCII-only prefixes (skill names are typically ASCII).
func hasPrefixIgnoreCase(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		a := s[i]
		b := prefix[i]
		// Convert to lowercase if uppercase ASCII
		if a >= 'A' && a <= 'Z' {
			a += 32
		}
		if b >= 'A' && b <= 'Z' {
			b += 32
		}
		if a != b {
			return false
		}
	}
	return true
}

// isSlashAtWordBoundary 判断指定位置的 "/" 是否在单词边界上。
// 单词边界定义为："/" 在文本开头，或前面是空白字符（空格、制表符、换行、回车）。
func isSlashAtWordBoundary(text string, slashIndex int) bool {
	if slashIndex == 0 {
		return true
	}
	prev := text[slashIndex-1]
	return prev == ' ' || prev == '\t' || prev == '\n' || prev == '\r'
}

// hasSlashAtWordBoundary 判断文本中最后一个 "/" 是否在单词边界上。
// 这是统一的触发条件判断函数，确保触发逻辑和缓存键计算使用相同的判断标准。
func hasSlashAtWordBoundary(text string) bool {
	lastSlash := strings.LastIndex(text, "/")
	if lastSlash < 0 {
		return false
	}
	return isSlashAtWordBoundary(text, lastSlash)
}
