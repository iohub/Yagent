package tui

import (
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// autocompleteMsg is sent by the debounce timer to trigger autocomplete computation.
type autocompleteMsg struct{}

// shouldAttemptCompletion 只查基本状态与光标位置、避免调用 m.input.Value()；实际字符检查在 scheduleAutocomplete 中进行。
func (m *model) shouldAttemptCompletion() bool {
	if m.commandMode || m.taskRunning {
		return false
	}

	column := m.input.Column()
	if column > 0 {
		return true
	}

	if m.input.Line() > 0 {
		return true
	}

	return false
}

func (m *model) clearAutocomplete() {
	m.skillAutoComplete = false
	m.skillSuggestions = nil
	m.skillSuggestionIdx = 0
	m.keywordAutoComplete = false
	m.keywordSuggestions = nil
	m.keywordSuggestionIdx = 0
}

// scheduleAutocomplete 启动或重置补全防抖：细粒度缓存键（单词+是否有/）提高命中率，Value()/Line()/Column() 只各调一次。
func (m *model) scheduleAutocomplete() tea.Cmd {
	if !m.shouldAttemptCompletion() {
		m.clearAutocomplete()
		return nil
	}

	text := m.input.Value()
	line := m.input.Line()
	column := m.input.Column()

	contentRunes := []rune(text)
	word := extractWordAtCursorRunes(contentRunes, column)
	hasSlash := hasSlashAtWordBoundary(text)

	cacheKey := autocompleteCacheKey{word: word, hasSlash: hasSlash}

	if cached, ok := m.autocompleteCache[cacheKey]; ok {
		result := *cached
		m.skillSuggestions = result.skillSuggestions
		m.skillSuggestionIdx = result.skillSuggestionIdx
		m.keywordSuggestions = result.keywordSuggestions
		m.keywordSuggestionIdx = result.keywordSuggestionIdx
		return nil
	}

	if m.debounceTimer != nil {
		m.debounceTimer.Stop()
		// drain timer channel to prevent leak
		select {
		case <-m.debounceTimer.C:
		default:
		}
	}

	m.snapshotText = text
	m.snapshotCursor = line*10000 + column // 编码 line 和 column
	m.pendingAutocomplete = true

	// 启动 100ms 防抖（从 50ms 增加到 100ms，减少快速输入时的计算频率）
	m.debounceTimer = time.NewTimer(100 * time.Millisecond)

	return func() tea.Msg {
		return autocompleteMsg{}
	}
}

func (m *model) doAutocomplete(text string, cursor int) {
	contentRunes := []rune(text)

	m.doSkillAutocomplete(text, contentRunes, cursor)

	m.doKeywordAutocomplete(text, contentRunes, cursor)
}

func (m *model) doSkillAutocomplete(text string, contentRunes []rune, cursor int) {
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

	query := text[lastSlash+1:]

	// 构建匹配列表：内置命令在前、技能在后（撞名时内置胜，见 skillCompletionItems）。
	if m.skillCollisionWarned == nil {
		m.skillCollisionWarned = map[string]bool{}
	}
	matches := skillCompletionItems(m.assistant.Skills(), query, m.skillCollisionWarned)

	if len(matches) > 0 {
		m.skillAutoComplete = true
		m.skillSuggestions = matches
		if m.skillSuggestionIdx >= len(matches) {
			m.skillSuggestionIdx = 0
		}
	} else {
		m.skillAutoComplete = false
		m.skillSuggestions = nil
		m.skillSuggestionIdx = 0
	}
}

func (m *model) doKeywordAutocomplete(text string, contentRunes []rune, cursor int) {
	if !m.keywordCompletionCfg.enabled {
		m.keywordAutoComplete = false
		m.keywordSuggestions = nil
		m.keywordSuggestionIdx = 0
		return
	}

	if m.commandMode || m.taskRunning {
		m.keywordAutoComplete = false
		m.keywordSuggestions = nil
		m.keywordSuggestionIdx = 0
		return
	}

	// 解码 scheduleAutocomplete 里编码成 line*10000 + column 的光标位置
	line := cursor / 10000
	column := cursor % 10000

	byteOffset := 0
	if line > 0 {
		lines := splitLogicalLines(contentRunes, line)
		for _, l := range lines {
			byteOffset += len(l) + 1 // +1 for newline
		}
	}
	byteOffset += column

	word := extractWordAtCursorRunes(contentRunes, byteOffset)

	// 带 "/" 前缀的词归技能补全管，这里跳过
	if word == "" || strings.HasPrefix(word, "/") {
		m.keywordAutoComplete = false
		m.keywordSuggestions = nil
		m.keywordSuggestionIdx = 0
		return
	}

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

func (m *model) handleAutocompleteMsg(msg autocompleteMsg) (tea.Model, tea.Cmd) {
	if !m.pendingAutocomplete {
		return m, nil
	}

	if currentText := m.input.Value(); currentText != m.snapshotText {
		m.scheduleAutocomplete()
		return m, nil
	}

	m.doAutocomplete(m.snapshotText, m.snapshotCursor)
	// 补全状态变化时刷新 footer 缓存以正确计算 viewport 高度
	m.invalidateFooterCache()

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

	if len(m.autocompleteCache) > 64 {
		m.autocompleteCache = make(map[autocompleteCacheKey]*AutocompleteResult)
	}

	m.pendingAutocomplete = false
	return m, nil
}

// extractWordAtCursorRunes extracts the word immediately before the cursor from a pre-computed runes slice, avoiding the string→runes conversion.
func extractWordAtCursorRunes(runes []rune, cursorPos int) string {
	if cursorPos <= 0 || cursorPos > len(runes) {
		return ""
	}

	// Extract backwards from cursor position using append (O(n) instead of O(n²))
	var word []rune
	for i := cursorPos - 1; i >= 0; i-- {
		r := runes[i]
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || (r >= 0x4e00 && r <= 0x9fff) {
			word = append(word, r)
		} else {
			break
		}
	}

	for i, j := 0, len(word)-1; i < j; i, j = i+1, j-1 {
		word[i], word[j] = word[j], word[i]
	}

	return string(word)
}

// splitLogicalLines returns the first `count` logical lines (newline-separated) of the content runes.
func splitLogicalLines(content []rune, count int) [][]rune {
	if count <= 0 {
		return nil
	}

	result := make([][]rune, 0, count)
	rest := content
	for len(rest) > 0 {
		idx := slices.Index(rest, '\n')
		if idx < 0 {
			if len(rest) > 0 {
				result = append(result, rest)
			}
			break
		}
		result = append(result, rest[:idx])
		rest = rest[idx+1:]
		if len(result) >= count {
			return result
		}
	}
	return result
}

// hasPrefixIgnoreCase compares ASCII bytes without allocating, unlike strings.ToLower (skill names are ASCII).
func hasPrefixIgnoreCase(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		a := s[i]
		b := prefix[i]
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

// isSlashAtWordBoundary 单词边界 = "/" 在文本开头，或前面是空白字符。
func isSlashAtWordBoundary(text string, slashIndex int) bool {
	if slashIndex == 0 {
		return true
	}
	prev := text[slashIndex-1]
	return prev == ' ' || prev == '\t' || prev == '\n' || prev == '\r'
}

// hasSlashAtWordBoundary 是统一的触发条件判断，确保触发逻辑和缓存键计算使用相同的判断标准。
func hasSlashAtWordBoundary(text string) bool {
	lastSlash := strings.LastIndex(text, "/")
	if lastSlash < 0 {
		return false
	}
	return isSlashAtWordBoundary(text, lastSlash)
}
