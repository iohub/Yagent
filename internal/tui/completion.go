package tui

import (
	"codeactor/internal/dict"
)

// GetKeywordCompletions 从 keywordDict 获取前缀补全建议
func GetKeywordCompletions(m *model, prefix string, limit int) []string {
	if m.keywordDict == nil {
		return nil
	}
	// dict.CompletionDict 的 Complete 方法直接返回前缀匹配的关键词
	return m.keywordDict.Complete(prefix)
}

// InitKeywordDict 创建并初始化关键词词典（供 TUI 使用）
func InitKeywordDict(userPath, projectPath string) *dict.CompletionDict {
	d := dict.NewCompletionDict("autocomplete", []string{userPath, projectPath})
	d.AddWords(dict.DefaultKeywords())
	return d
}
