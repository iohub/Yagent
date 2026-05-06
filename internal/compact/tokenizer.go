package compact

import (
	"sync"

	"github.com/pkoukk/tiktoken-go"
)

// Tokenizer token计数器接口
type Tokenizer interface {
	CountTokens(content string) (int, error)
}

// tiktokenTokenizer tiktoken实现
type tiktokenTokenizer struct {
	mu         sync.RWMutex
	encoders   map[string]*tiktoken.Tiktoken
	cache      map[string]int
	cacheMu    sync.RWMutex
	maxCacheSize int
}

// GetGlobalTokenizer 获取全局tokenizer实例
func GetGlobalTokenizer() Tokenizer {
	return globalTokenizer
}

var globalTokenizer = &tiktokenTokenizer{
	encoders:       make(map[string]*tiktoken.Tiktoken),
	cache:          make(map[string]int),
	maxCacheSize:   10000,
}

// getEncoder 获取或创建encoder
func (t *tiktokenTokenizer) getEncoder(model string) (*tiktoken.Tiktoken, error) {
	t.mu.RLock()
	enc, ok := t.encoders[model]
	t.mu.RUnlock()
	if ok {
		return enc, nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	
	// Double-check after lock
	if enc, ok = t.encoders[model]; ok {
		return enc, nil
	}

	var err error
	enc, err = tiktoken.EncodingForModel(model)
	if err != nil {
		// Fallback to cl100k_base (covers gpt-4/3.5/ada)
		enc, err = tiktoken.GetEncoding("cl100k_base")
		if err != nil {
			return nil, err
		}
	}
	t.encoders[model] = enc
	return enc, nil
}

// CountTokens 计算token数量
func (t *tiktokenTokenizer) CountTokens(content string) (int, error) {
	if content == "" {
		return 0, nil
	}
	
	// Check cache
	t.cacheMu.RLock()
	if count, ok := t.cache[content]; ok {
		t.cacheMu.RUnlock()
		return count, nil
	}
	t.cacheMu.RUnlock()

	enc, err := t.getEncoder("gpt-4")
	if err != nil {
		return 0, err
	}

	count := len(enc.Encode(content, nil, nil))
	
	// Update cache
	t.cacheMu.Lock()
	if len(t.cache) >= t.maxCacheSize {
		// Simple LRU eviction
		t.cache = make(map[string]int)
	}
	t.cache[content] = count
	t.cacheMu.Unlock()

	return count, nil
}

// ResetCache 重置缓存（用于测试）
func (t *tiktokenTokenizer) ResetCache() {
	t.cacheMu.Lock()
	defer t.cacheMu.Unlock()
	t.cache = make(map[string]int)
}
