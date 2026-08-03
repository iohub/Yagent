package agents

import (
	"fmt"
	"strings"
	"time"

	"codeactor/internal/memory"
)

// ResultCompressor 结果压缩器
// 大结果存入 SharedMemory，返回摘要给 Director
// 小结果直接返回，不压缩
type ResultCompressor struct {
	sharedMemory  *SharedMemoryStore // 使用 SharedMemory 的 KV 存储能力
	threshold     int                // 压缩阈值（字节），超过此值才压缩
	summaryMaxLen int                // 摘要最大长度（字符）
}

// NewResultCompressor 创建结果压缩器
// threshold: 压缩阈值（字节），<=0 时使用默认值 4096
// summaryMaxLen: 摘要最大长度，<=0 时使用默认值 2048
func NewResultCompressor(threshold, summaryMaxLen int) *ResultCompressor {
	if threshold <= 0 {
		threshold = 4096
	}
	if summaryMaxLen <= 0 {
		summaryMaxLen = 2048
	}
	return &ResultCompressor{
		threshold:     threshold,
		summaryMaxLen: summaryMaxLen,
	}
}

// CompressionResult 压缩结果
type CompressionResult struct {
	Compressed     bool   `json:"compressed"`      // 是否被压缩
	Content        string `json:"content"`         // 压缩后的内容（摘要或原始内容）
	StorageKey     string `json:"storage_key,omitempty"` // SharedMemory 中的存储路径
	OriginalSize   int    `json:"original_size"`   // 原始结果大小（字符数）
	CompressedSize int    `json:"compressed_size"` // 压缩后大小（字符数）
	CreatedAt      string `json:"created_at,omitempty"` // 压缩时间戳
}

// Compress 执行结果压缩
// agentID: 产生结果的 Agent ID
// taskID: 关联的任务 ID
// result: 原始结果文本
// 返回压缩结果，包含摘要或原始内容
func (rc *ResultCompressor) Compress(agentID string, taskID string, result string) *CompressionResult {
	originalSize := len(result)

	// 1. 检查结果大小，小于阈值不压缩
	if originalSize <= rc.threshold {
		return &CompressionResult{
			Compressed:     false,
			Content:        result,
			OriginalSize:   originalSize,
			CompressedSize: originalSize,
			CreatedAt:      time.Now().Format(time.RFC3339),
		}
	}

	// 2. 生成摘要
	summary := rc.generateSummary(result)

	// 3. 尝试存储完整结果到 SharedMemory
	compressedSize := len(summary)
	storageKey := fmt.Sprintf("result:%s:%s:full", agentID, taskID)

	if rc.sharedMemory != nil {
		err := rc.sharedMemory.Store(storageKey, result)
		if err == nil {
			// 成功存储，添加引用信息
			compressedContent := fmt.Sprintf(
				"%s\n\n[完整结果已存储到 SharedMemory (key: %s)]\n[原始大小: %d 字符, 摘要大小: %d 字符]",
				summary, storageKey, originalSize, compressedSize,
			)
			return &CompressionResult{
				Compressed:     true,
				Content:        compressedContent,
				StorageKey:     storageKey,
				OriginalSize:   originalSize,
				CompressedSize: len(compressedContent),
				CreatedAt:      time.Now().Format(time.RFC3339),
			}
		}
	}

	// 4. 存储失败时的降级：返回摘要 + 警告
	fallback := fmt.Sprintf(
		"%s\n\n[警告: 完整结果存储失败，结果已截断]\n[原始大小: %d 字符]",
		summary, originalSize,
	)
	return &CompressionResult{
		Compressed:     true,
		Content:        fallback,
		OriginalSize:   originalSize,
		CompressedSize: len(fallback),
		CreatedAt:      time.Now().Format(time.RFC3339),
	}
}

// generateSummary 生成结果摘要
// 策略：保留头部关键信息 → 截断到 summaryMaxLen → 在完整行边界截断 → 保留尾部关键内容
func (rc *ResultCompressor) generateSummary(result string) string {
	if len(result) <= rc.summaryMaxLen {
		return result
	}

	// 保留前 summaryMaxLen 个字符
	summary := result[:rc.summaryMaxLen]

	// 在最后一个完整行截断（如果截断点 > 一半长度）
	if idx := strings.LastIndex(summary, "\n"); idx > rc.summaryMaxLen/2 {
		summary = summary[:idx]
	}

	// 保留尾部关键信息（错误、结论等）
	tailLen := 256
	if len(result) > rc.summaryMaxLen+tailLen {
		tail := result[len(result)-tailLen:]
		summary = summary + "\n\n... [中间内容省略] ...\n\n" + tail
	}

	return summary
}

// ────────────────────────────────────────────────────────────────
// SharedMemoryStore 是对 memory.SharedMemory 的简单 KV 封装
// ────────────────────────────────────────────────────────────────

// SharedMemoryStore 封装 SharedMemory 提供 KV 存储语义
type SharedMemoryStore struct {
	sm *memory.SharedMemory
}

// Store 存储键值对到 SharedMemory
// 使用 SharedMemory 的 Publish 机制，通过 topic "kvstore" 存储
func (s *SharedMemoryStore) Store(key string, value string) error {
	if s.sm == nil {
		return fmt.Errorf("shared memory is nil")
	}

	msg := memory.ChatMessage{
		Type:    memory.MessageTypeHuman, // 使用 Human 类型作为通用存储
		Content: value,
		Metadata: map[string]interface{}{
			"kv_key":   key,
			"kv_store": "true",
		},
	}

	return s.sm.Publish(msg)
}

// Retrieve 从 SharedMemory 检索键值对
func (s *SharedMemoryStore) Retrieve(key string) (string, error) {
	if s.sm == nil {
		return "", fmt.Errorf("shared memory is nil")
	}

	msgs := s.sm.GetMessages()
	for i := len(msgs) - 1; i >= 0; i-- {
		msg := msgs[i]
		if msg.Metadata != nil {
			if k, ok := msg.Metadata["kv_key"]; ok && k == key {
				if store, ok := msg.Metadata["kv_store"]; ok && store == "true" {
					return msg.Content, nil
				}
			}
		}
	}

	return "", fmt.Errorf("key %s not found in shared memory", key)
}

