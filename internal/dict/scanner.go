package dict

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// ScannerMatcher 基于多模式匹配的文本扫描器
// 使用排序 + 二分查找实现高效的多关键词匹配
type ScannerMatcher struct {
	name    string
	sources []string
	words   atomic.Value // 存储 []string（已排序的关键词列表）
	mu      sync.RWMutex // 用于构建新关键词列表时的写锁
}

// NewScannerMatcher 创建一个新的扫描匹配器
// name: 匹配器名称
// sources: 关键词源列表（文件路径或逗号分隔的关键词）
func NewScannerMatcher(name string, sources []string) (*ScannerMatcher, error) {
	m := &ScannerMatcher{
		name:    name,
		sources: sources,
	}

	// 初始加载
	if err := m.reloadInternal(); err != nil {
		return nil, fmt.Errorf("初始化扫描器失败: %w", err)
	}

	return m, nil
}

// MatchAll 扫描文本，返回所有匹配结果
func (m *ScannerMatcher) MatchAll(text []byte) []Match {
	words := m.words.Load().([]string)
	if len(words) == 0 {
		return nil
	}

	matches := make([]Match, 0, len(words))
	textStr := string(text)

	// 对每个关键词进行匹配
	for _, keyword := range words {
		if idx := strings.Index(textStr, keyword); idx >= 0 {
			matches = append(matches, Match{
				Keyword:  keyword,
				Start:    idx,
				End:      idx + len(keyword),
				DictName: m.name,
			})
		}
	}

	// 按起始位置排序
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].Start < matches[j].Start
	})

	return matches
}

// Reload 重新加载词典并重建关键词列表
func (m *ScannerMatcher) Reload() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.reloadInternal()
}

// Name 返回匹配器名称
func (m *ScannerMatcher) Name() string {
	return m.name
}

// Sources 返回关键词源列表
func (m *ScannerMatcher) Sources() []string {
	return m.sources
}

// reloadInternal 内部重新加载方法
func (m *ScannerMatcher) reloadInternal() error {
	// 收集所有关键词
	var allWords []string

	for _, source := range m.sources {
		// 尝试作为文件路径加载
		data, err := readFileKeywords(source)
		if err == nil {
			allWords = append(allWords, data...)
			continue
		}

		// 如果不是文件，尝试作为逗号分隔的关键词
		if words := parseCSVWords(source); len(words) > 0 {
			allWords = append(allWords, words...)
		}
	}

	if len(allWords) == 0 {
		return fmt.Errorf("没有加载到任何关键词")
	}

	// 去重并排序
	uniqueWords := uniqueSorted(allWords)

	// 原子替换
	m.words.Store(uniqueWords)

	return nil
}

// readFileKeywords 从文件读取关键词
func readFileKeywords(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseKeywords(data), nil
}

// parseCSVWords 解析逗号分隔的关键词
func parseCSVWords(s string) []string {
	if strings.Contains(s, ",") {
		parts := strings.Split(s, ",")
		words := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				words = append(words, p)
			}
		}
		return words
	}
	return nil
}

// uniqueSorted 去重并排序字符串切片
func uniqueSorted(slice []string) []string {
	// 先排序
	sort.Strings(slice)

	// 去重
	unique := make([]string, 0, len(slice))
	seen := make(map[string]struct{})

	for _, s := range slice {
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			unique = append(unique, s)
		}
	}

	return unique
}
