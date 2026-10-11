package dict

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
)

// CompletionDict 管理关键词词典，支持前缀匹配
// 关键词列表维护为已排序状态，使用二分查找加速前缀匹配
type CompletionDict struct {
	mu     sync.RWMutex
	words  []string // 已排序的关键词列表
	source string   // 词典来源描述
	name   string   // 词典名称
}

// NewCompletionDict 创建一个新的补全词典
func NewCompletionDict(name string, sources []string) *CompletionDict {
	d := &CompletionDict{
		words:  make([]string, 0),
		source: "empty",
		name:   name,
	}

	// 如果有 sources，自动加载
	if len(sources) > 0 {
		d.loadFromSources(sources)
	}

	return d
}

// loadFromSources 从多个源加载关键词
func (d *CompletionDict) loadFromSources(sources []string) {
	allWords := make([]string, 0)

	for _, source := range sources {
		// 判断是文件路径还是嵌入资源路径
		if _, err := os.Stat(source); err == nil {
			// 是文件路径
			data, err := os.ReadFile(source)
			if err != nil {
				continue
			}
			allWords = append(allWords, parseKeywords(data)...)
		}
		// 否则忽略（可能是嵌入资源，需要特殊处理）
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	d.words = uniqueAndSorted(allWords)
	d.source = fmt.Sprintf("sources:%s", strings.Join(sources, ","))
}

// AddWords 批量添加关键词
// 批量处理比逐个添加更高效
func (d *CompletionDict) AddWords(words []string) {
	if len(words) == 0 {
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	// 合并现有关键词和新关键词
	allWords := make([]string, 0, len(d.words)+len(words))
	allWords = append(allWords, d.words...)
	allWords = append(allWords, words...)

	// 去重并排序
	d.words = uniqueAndSorted(allWords)
}

// Complete 按前缀搜索关键词，返回最多 limit 个结果
// 忽略大小写
func (d *CompletionDict) Complete(prefix string) []string {
	const defaultLimit = 20

	if prefix == "" {
		d.mu.RLock()
		defer d.mu.RUnlock()

		result := make([]string, 0, defaultLimit)
		for i, word := range d.words {
			if i >= defaultLimit {
				break
			}
			result = append(result, word)
		}
		return result
	}

	prefixLower := strings.ToLower(prefix)

	d.mu.RLock()
	defer d.mu.RUnlock()

	// 使用二分查找找到第一个可能匹配的前缀位置
	startIdx := searchPrefixStart(d.words, prefixLower)

	// 从前缀匹配位置开始收集结果
	result := make([]string, 0, defaultLimit)
	for i := startIdx; i < len(d.words) && len(result) < defaultLimit; i++ {
		wordLower := strings.ToLower(d.words[i])
		if strings.HasPrefix(wordLower, prefixLower) {
			result = append(result, d.words[i])
		} else {
			// 由于列表已排序，后续词不会再匹配
			break
		}
	}

	return result
}

// Name 返回词典名称
func (d *CompletionDict) Name() string {
	return d.name
}

// parseKeywords 解析关键词数据，忽略注释和空行
func parseKeywords(data []byte) []string {
	lines := strings.Split(string(data), "\n")
	words := make([]string, 0, len(lines))

	for _, line := range lines {
		// 去除前后空白
		line = strings.TrimSpace(line)

		// 跳过空行和注释
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		words = append(words, line)
	}

	return words
}

// uniqueAndSorted 去重并排序字符串切片
func uniqueAndSorted(words []string) []string {
	if len(words) == 0 {
		return words
	}

	// 先排序
	sort.Strings(words)

	// 去重
	unique := []string{words[0]}
	for i := 1; i < len(words); i++ {
		if words[i] != words[i-1] {
			unique = append(unique, words[i])
		}
	}

	return unique
}

// searchPrefixStart 使用二分查找找到第一个可能匹配前缀的位置
func searchPrefixStart(words []string, prefix string) int {
	return sort.Search(len(words), func(i int) bool {
		return strings.ToLower(words[i]) >= prefix
	})
}
