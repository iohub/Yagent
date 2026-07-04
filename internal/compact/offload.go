package compact

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"codeactor/internal/llm"
)

// ─────────────────────────────────────────────────────────
// OffloadStorage — 外部存储模块
// ─────────────────────────────────────────────────────────
// 用于将超限的工具输出存储到磁盘，消息中仅保留预览 + 引用 ID。
//
// 设计要点：
//  1. SHA256 内容哈希作为 ID，天然去重
//  2. 会话级子目录隔离，不同会话互不干扰
//  3. 总大小限制 + LRU 淘汰（按修改时间）
//  4. 线程安全（RWMutex）
//  5. 与 truncator.go 配合：截断后的内容写入外部存储，消息中保留 TruncationMarker

// OffloadContent 外部存储的元信息
type OffloadContent struct {
	// ID 内容的 SHA256 哈希（去重用）
	ID string
	// ToolName 产生该内容的工具名称，如 "run_bash", "read_file"
	ToolName string
	// MessageRef 消息引用标识（可以是消息索引或 ToolCallID）
	MessageRef string
	// OriginalSize 原始内容字节大小
	OriginalSize int
	// Preview 内容预览
	Preview string
	// TokenCount 估算的 token 数
	TokenCount int
	// StoredPath 磁盘上的完整存储路径
	StoredPath string
	// Timestamp 存储时间
	Timestamp time.Time
	// SessionID 所属会话 ID
	SessionID string
}

// OffloadStorage 外部存储管理器
// 管理超限工具输出的磁盘存储、检索、淘汰。
type OffloadStorage struct {
	mu           sync.RWMutex
	basePath     string
	sessionID    string
	offloaded    map[string]*OffloadContent
	totalSize    int64
	maxTotalSize int64
	tokenizer    Tokenizer
}

const (
	defaultMaxTotalSize = 100 * 1024 * 1024
	defaultPreviewLen   = 256
)

// NewOffloadStorage 创建外部存储实例
//
// Parameters:
//   - basePath: 存储根目录，每个会话创建独立子目录
//   - sessionID: 会话 ID，用于目录隔离
//   - maxTotalSize: 总大小限制（字节），0 表示使用默认 100MB
func NewOffloadStorage(basePath, sessionID string, maxTotalSize int64) (*OffloadStorage, error) {
	if basePath == "" {
		return nil, fmt.Errorf("offload: basePath cannot be empty")
	}
	if sessionID == "" {
		return nil, fmt.Errorf("offload: sessionID cannot be empty")
	}
	if maxTotalSize <= 0 {
		maxTotalSize = defaultMaxTotalSize
	}

	sessionDir := filepath.Join(basePath, sessionID)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		return nil, fmt.Errorf("offload: create session dir: %w", err)
	}

	storage := &OffloadStorage{
		basePath:     basePath,
		sessionID:    sessionID,
		offloaded:    make(map[string]*OffloadContent),
		maxTotalSize: maxTotalSize,
		tokenizer:    GetGlobalTokenizer(),
	}

	if err := storage.restoreExisting(); err != nil {
		return nil, fmt.Errorf("offload: restore existing: %w", err)
	}

	return storage, nil
}

// Store 将内容存储到磁盘
//
// 流程：
//  1. 计算 SHA256 哈希
//  2. 如已存在则直接返回已有元信息（去重）
//  3. 检查总大小限制，触发 LRU 淘汰
//  4. 写入磁盘
//  5. 更新内存元信息
func (s *OffloadStorage) Store(
	toolName string,
	messageRef string,
	content string,
	preview string,
	tokenCount int,
) (*OffloadContent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if content == "" {
		return nil, fmt.Errorf("offload: content cannot be empty")
	}

	// 计算 SHA256 哈希
	hash := sha256.Sum256([]byte(content))
	id := hex.EncodeToString(hash[:])

	// 去重：如已存在则直接返回
	if existing, ok := s.offloaded[id]; ok {
		return existing, nil
	}

	// 计算预览
	if preview == "" {
		preview = s.makePreview(content)
	}

	originalSize := len(content)

	// 检查总大小限制，触发 LRU 淘汰
	for s.totalSize+int64(originalSize) > s.maxTotalSize && len(s.offloaded) > 0 {
		if err := s.evictOldestLocked(1); err != nil {
			return nil, fmt.Errorf("offload: evict before store: %w", err)
		}
	}

	// 再次检查
	if s.totalSize+int64(originalSize) > s.maxTotalSize {
		return nil, fmt.Errorf("offload: cannot store %d bytes (limit: %d, current: %d, no items to evict)",
			originalSize, s.maxTotalSize, s.totalSize)
	}

	// 写入磁盘
	filename := id + ".dat"
	filePath := filepath.Join(s.sessionDir(), filename)
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		return nil, fmt.Errorf("offload: write content: %w", err)
	}

	// 构建元信息
	meta := &OffloadContent{
		ID:           id,
		ToolName:     toolName,
		MessageRef:   messageRef,
		OriginalSize: originalSize,
		Preview:      preview,
		TokenCount:   tokenCount,
		StoredPath:   filePath,
		Timestamp:    time.Now(),
		SessionID:    s.sessionID,
	}

	s.offloaded[id] = meta
	s.totalSize += int64(originalSize)

	return meta, nil
}

// Retrieve 从磁盘检索内容
func (s *OffloadStorage) Retrieve(id string) (string, error) {
	s.mu.RLock()
	meta, ok := s.offloaded[id]
	s.mu.RUnlock()

	if !ok {
		return "", fmt.Errorf("offload: content not found: %s", id)
	}

	content, err := os.ReadFile(meta.StoredPath)
	if err != nil {
		return "", fmt.Errorf("offload: read content: %w", err)
	}

	return string(content), nil
}

// Delete 从磁盘删除内容
func (s *OffloadStorage) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	meta, ok := s.offloaded[id]
	if !ok {
		return fmt.Errorf("offload: content not found: %s", id)
	}

	if err := os.Remove(meta.StoredPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("offload: delete file: %w", err)
	}

	delete(s.offloaded, id)
	s.totalSize -= int64(meta.OriginalSize)

	return nil
}

// List 列出所有已存储内容的 ID
func (s *OffloadStorage) List() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ids := make([]string, 0, len(s.offloaded))
	for id := range s.offloaded {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Cleanup 清理整个会话的存储
func (s *OffloadStorage) Cleanup() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	sessionDir := s.sessionDir()
	if err := os.RemoveAll(sessionDir); err != nil {
		return fmt.Errorf("offload: remove session dir: %w", err)
	}

	s.offloaded = make(map[string]*OffloadContent)
	s.totalSize = 0

	return nil
}

// EvictOldest 淘汰最旧的内容，直到释放 targetSize 字节
func (s *OffloadStorage) EvictOldest(targetSize int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.evictOldestLocked(targetSize)
}

// Stats 返回当前存储的统计信息
func (s *OffloadStorage) Stats() (count int, totalSize int64, maxTotalSize int64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.offloaded), s.totalSize, s.maxTotalSize
}

// ─────────────────────────────────────────────────────────
// 内部方法
// ─────────────────────────────────────────────────────────

func (s *OffloadStorage) sessionDir() string {
	return filepath.Join(s.basePath, s.sessionID)
}

// makePreview 生成内容预览
func (s *OffloadStorage) makePreview(content string) string {
	const maxLen = defaultPreviewLen
	if len(content) <= maxLen {
		return content
	}
	preview := content[:maxLen]
	if idx := strings.LastIndex(preview, "\n"); idx > maxLen/2 {
		preview = preview[:idx]
	}
	return preview + "\n\n... [truncated, see full content in external storage] ..."
}

// evictOldestLocked 淘汰最旧的内容（内部方法，调用者必须持有写锁）
func (s *OffloadStorage) evictOldestLocked(targetSize int64) error {
	if len(s.offloaded) == 0 {
		return fmt.Errorf("offload: no items to evict")
	}

	sorted := make([]*OffloadContent, 0, len(s.offloaded))
	for _, meta := range s.offloaded {
		sorted = append(sorted, meta)
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Timestamp.Before(sorted[j].Timestamp)
	})

	evicted := int64(0)
	for _, meta := range sorted {
		if evicted >= targetSize {
			break
		}
		if err := os.Remove(meta.StoredPath); err != nil && !os.IsNotExist(err) {
			continue
		}
		delete(s.offloaded, meta.ID)
		s.totalSize -= int64(meta.OriginalSize)
		evicted += int64(meta.OriginalSize)
	}

	if evicted == 0 {
		return fmt.Errorf("offload: failed to evict any items (target: %d bytes)", targetSize)
	}

	return nil
}

// restoreExisting 启动时从磁盘恢复已存在的元数据
func (s *OffloadStorage) restoreExisting() error {
	sessionDir := s.sessionDir()
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if !strings.HasSuffix(name, ".dat") {
			continue
		}

		id := strings.TrimSuffix(name, ".dat")
		filePath := filepath.Join(sessionDir, name)

		fileInfo, err := os.Stat(filePath)
		if err != nil {
			continue
		}

		s.offloaded[id] = &OffloadContent{
			ID:           id,
			OriginalSize: int(fileInfo.Size()),
			Preview:      "[restored from disk, no metadata]",
			Timestamp:    fileInfo.ModTime(),
			SessionID:    s.sessionID,
			StoredPath:   filePath,
		}
		s.totalSize += int64(fileInfo.Size())
	}

	return nil
}

// ─────────────────────────────────────────────────────────
// 与 TruncationMarker 集成
// ─────────────────────────────────────────────────────────

// OffloadAndMark 将工具结果 offload 到磁盘，并在消息上设置 TruncationMarker
// 这是与 truncator.go 配合使用的便捷方法。
func (s *OffloadStorage) OffloadAndMark(
	msg *llm.Message,
	toolName string,
	headLen, tailLen int,
) (*OffloadContent, error) {
	content := msg.Content
	if content == "" {
		return nil, nil
	}

	preview := s.makePreviewWithBoundaries(content, headLen, tailLen)

	meta, err := s.Store(toolName, msg.ToolCallID, content, preview, 0)
	if err != nil {
		return nil, err
	}

	msg.Content = fmt.Sprintf(
		"[%s output offloaded to external storage]\n\nPreview:\n%s\n\n"+
			"Reference ID: %s\n"+
			"Original size: %d bytes\n"+
			"Stored at: %s",
		toolName,
		preview,
		meta.ID,
		meta.OriginalSize,
		meta.StoredPath,
	)

	msg.TruncationMarker = &llm.TruncationMarker{
		ToolName:       toolName,
		OriginalLen:    meta.OriginalSize,
		OmittedLen:     meta.OriginalSize - len(msg.Content),
		TruncationPass: 0,
	}

	return meta, nil
}

// makePreviewWithBoundaries 生成 head + tail 预览
func (s *OffloadStorage) makePreviewWithBoundaries(content string, headLen, tailLen int) string {
	if len(content) <= headLen+tailLen {
		return content
	}

	head := content[:headLen]
	tail := content[len(content)-tailLen:]

	return head + "\n\n... [truncated, see full content in external storage] ...\n\n" + tail
}
