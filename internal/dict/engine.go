package dict

// Match 表示一次匹配结果
type Match struct {
	Keyword  string
	Start    int
	End      int
	DictName string
}

// Matcher 扫描器匹配器通用接口
type Matcher interface {
	MatchAll(text []byte) []Match
	Name() string
	Reload() error
}

// CompletionProvider 补全专用接口
type CompletionProvider interface {
	Complete(prefix string) []string
	Name() string
	Reload() error
}

// Manager 词典管理器接口
type Manager interface {
	// 自动补全
	AutoComplete(prefix string) []string
	// 匹配扫描
	MatchAll(dictName string, text []byte) ([]Match, error)
	// 列出所有可用词典名称
	ListDicts() []string
	// 关闭资源
	Close() error
}
