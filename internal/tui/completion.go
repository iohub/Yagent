package tui

// GetKeywordCompletions 从 keywordDict 获取前缀补全建议
func GetKeywordCompletions(m *model, prefix string, limit int) []string {
	if m.keywordDict == nil {
		return nil
	}
	// dict.CompletionDict 的 Complete 方法直接返回前缀匹配的关键词
	return m.keywordDict.Complete(prefix)
}
