package compact

import (
	"sync"

	"codeactor/internal/llm"

	"github.com/pkoukk/tiktoken-go"
)

// Tokenizer token计数器接口
// 用于准确计算消息和文本的 token 数量
type Tokenizer interface {
	// CountTokens 计算给定文本的 token 数
	CountTokens(content string) (int, error)

	// CountMessagesTokens 计算一批消息的总 token 数
	CountMessagesTokens(messages []llm.Message) (int, error)

	// Encoding 返回当前使用的编码名称
	Encoding() string

	// SetEncoding 动态切换编码（不影响已缓存的结果）
	SetEncoding(encoding string) error
}

// tiktokenTokenizer tiktoken实现
type tiktokenTokenizer struct {
	mu         sync.RWMutex
	encoders   map[string]*tiktoken.Tiktoken
	cache      map[string]int
	cacheMu    sync.RWMutex
	maxCacheSize int
	encoding   string // 当前使用的编码名称
}

// GetGlobalTokenizer 获取全局tokenizer实例
func GetGlobalTokenizer() Tokenizer {
	return globalTokenizer
}

var globalTokenizer = &tiktokenTokenizer{
	encoders:       make(map[string]*tiktoken.Tiktoken),
	cache:          make(map[string]int),
	maxCacheSize:   10000,
	encoding:       "gpt-4",
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

	// 使用当前配置的编码
	enc, err := t.getEncoder(t.encoding)
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

// CountMessagesTokens 计算一批消息的总token数
func (t *tiktokenTokenizer) CountMessagesTokens(messages []llm.Message) (int, error) {
	var total int
	for _, msg := range messages {
		count, err := t.CountTokens(msg.Content)
		if err != nil {
			return 0, err
		}
		total += count

		// 计算tool call的token
		for _, toolCall := range msg.ToolCalls {
			if toolCall.ID != "" {
				count, err := t.CountTokens(toolCall.ID)
				if err != nil {
					return 0, err
				}
				total += count
			}
			if toolCall.Function.Name != "" {
				count, err := t.CountTokens(toolCall.Function.Name)
				if err != nil {
					return 0, err
				}
				total += count
			}
			if toolCall.Function.Arguments != "" {
				count, err := t.CountTokens(toolCall.Function.Arguments)
				if err != nil {
					return 0, err
				}
				total += count
			}
		}

		// 计算reasoning内容的token
		if msg.Reasoning != "" {
			count, err := t.CountTokens(msg.Reasoning)
			if err != nil {
				return 0, err
			}
			total += count
		}
	}
	return total, nil
}

// Encoding 返回当前使用的编码名称
func (t *tiktokenTokenizer) Encoding() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.encoding
}

// SetEncoding 动态切换编码（不影响已缓存的结果）
func (t *tiktokenTokenizer) SetEncoding(encoding string) error {
	if encoding == "" {
		return &EncodingError{"encoding cannot be empty"}
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	// 验证编码是否可用
	_, err := t.getEncoder(encoding)
	if err != nil {
		return err
	}

	t.encoding = encoding
	return nil
}

// EncodingError 编码错误
type EncodingError struct {
	Message string
}

func (e *EncodingError) Error() string {
	return "tokenizer: " + e.Message
}

// ResetCache 重置缓存（用于测试）
func (t *tiktokenTokenizer) ResetCache() {
	t.cacheMu.Lock()
	defer t.cacheMu.Unlock()
	t.cache = make(map[string]int)
}
