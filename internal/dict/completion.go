package dict

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
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

// LoadFromFile 从文件加载关键词（一行一个）
// 文件格式：每行一个关键词，支持注释（# 开头）和空行
func (d *CompletionDict) LoadFromFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读取词典文件失败: %w", err)
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	newWords := parseKeywords(data)
	d.words = uniqueAndSorted(append(d.words, newWords...))
	d.source = path

	return nil
}

// LoadFromData 从字符串数据加载关键词
func (d *CompletionDict) LoadFromData(data []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	newWords := parseKeywords(data)
	d.words = uniqueAndSorted(append(d.words, newWords...))
	d.source = "data"

	return nil
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

// Reload 重新加载词典（从原始源）
// 由于当前实现不存储原始源路径，此方法返回 nil
func (d *CompletionDict) Reload() error {
	// 如果 source 是文件路径，重新加载
	if d.source != "empty" && d.source != "data" && !strings.HasPrefix(d.source, "sources:") {
		// 保存源路径（Clear 会重置 source 为 "empty"）
		sourcePath := d.source
		d.Clear()
		return d.LoadFromFile(sourcePath)
	}
	return nil
}

// WordCount 返回关键词数量
func (d *CompletionDict) WordCount() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.words)
}

// Name 返回词典名称
func (d *CompletionDict) Name() string {
	return d.name
}

// Clear 清空所有关键词
func (d *CompletionDict) Clear() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.words = make([]string, 0)
	d.source = "empty"
}

// LoadFromEmbedFS 从嵌入的完整文件系统加载词典
// embedFS: 嵌入的文件系统
// dirPath: 目录路径，函数会遍历该目录下所有 .txt 文件
func (d *CompletionDict) LoadFromEmbedFS(embedFS embed.FS, dirPath string) error {
	entries, err := embedFS.ReadDir(dirPath)
	if err != nil {
		return fmt.Errorf("读取嵌入目录失败: %w", err)
	}

	allWords := make([]string, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".txt") {
			continue
		}

		filePath := filepath.Join(dirPath, entry.Name())
		data, err := embedFS.ReadFile(filePath)
		if err != nil {
			// 单个文件读取失败，跳过并记录
			continue
		}

		allWords = append(allWords, parseKeywords(data)...)
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	d.words = uniqueAndSorted(allWords)
	d.source = fmt.Sprintf("embedfs:%s", dirPath)

	return nil
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
