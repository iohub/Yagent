package dict

import _ "embed"

//go:embed defaults_kw.txt
var defaultKeywordsBytes []byte

// DefaultKeywords 返回内置默认关键词列表
func DefaultKeywords() []string {
	return parseKeywords(defaultKeywordsBytes)
}
