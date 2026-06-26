package agents

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"codeactor/internal/memory"
)

// ============================================================================
// Memory Sections
// ============================================================================

// MemorySection 定义记忆的分区名称
type MemorySection string

const (
	SectionArchitecture MemorySection = "Architecture"
	SectionPatterns     MemorySection = "Patterns"
	SectionConventions  MemorySection = "Conventions"
	SectionDependencies MemorySection = "Dependencies"
	SectionGotchas      MemorySection = "Gotchas"
	SectionKeyFiles     MemorySection = "Key Files"
)

// AllMemorySections 返回所有分区的有序列表
var AllMemorySections = []MemorySection{
	SectionArchitecture,
	SectionPatterns,
	SectionConventions,
	SectionDependencies,
	SectionGotchas,
	SectionKeyFiles,
}

// ============================================================================
// Constants
// ============================================================================

const (
	// MaxMemoryTokens 是记忆 token 硬限制
	MaxMemoryTokens = 1500

	// charsPerToken 是字符到 token 的估算系数（保守估计）
	charsPerToken = 3

	// kvKeyPrefix 是 SharedMemory KV 存储的 key 前缀
	kvKeyPrefix = "repo_memory:"
)

// DefaultMemoryContent 是空仓库的默认记忆模板
const DefaultMemoryContent = `# Repository Memory

## Architecture
(No data yet)

## Patterns
(No data yet)

## Conventions
(No data yet)

## Dependencies
(No data yet)

## Gotchas
(No data yet)

## Key Files
(No data yet)`

// ============================================================================
// RepoMemoryStore
// ============================================================================

// RepoMemoryStore 提供仓库记忆的本地缓存 + SharedMemory 后端存储。
// 并发安全：Load/Get 使用读锁，Save 使用写锁。
// ConsolidationWorker 是唯一的写入者，RepoAgent.Run() 是读取者。
type RepoMemoryStore struct {
	repoID string
	shared *memory.SharedMemory

	mu     sync.RWMutex
	cache  string
	loaded bool
}

// NewRepoMemoryStore 创建绑定到指定 repoID 的记忆存储。
// repoID 通常使用 GlobalCtx.ProjectPath。
func NewRepoMemoryStore(repoID string, shared *memory.SharedMemory) *RepoMemoryStore {
	return &RepoMemoryStore{
		repoID: repoID,
		shared: shared,
	}
}

func (s *RepoMemoryStore) kvKey() string {
	return kvKeyPrefix + s.repoID
}

// Load 从 SharedMemory 加载记忆到本地缓存。
// 首次调用时执行实际加载，后续调用是安全的 no-op。
// 若 SharedMemory 中无数据，使用默认模板。
func (s *RepoMemoryStore) Load(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.loaded {
		return nil
	}

	val, err := s.shared.GetKey(s.kvKey())
	if err != nil {
		// 首次使用，无历史记忆
		s.cache = DefaultMemoryContent
	} else {
		s.cache = val
	}
	s.loaded = true
	return nil
}

// Get 返回缓存的记忆内容。必须在 Load 之后调用。
// 这是热路径 — 每次 RepoAgent.Run() 都会调用。
func (s *RepoMemoryStore) Get() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cache
}

// Save 持久化记忆内容到 SharedMemory 并更新本地缓存。
// 仅由 ConsolidationWorker 调用。
func (s *RepoMemoryStore) Save(ctx context.Context, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.shared.SetKey(s.kvKey(), content); err != nil {
		return fmt.Errorf("repo memory save: %w", err)
	}
	s.cache = content
	return nil
}

// IsEmpty 检查缓存的记忆是否为默认模板（即无实际知识）。
func (s *RepoMemoryStore) IsEmpty() bool {
	return s.Get() == DefaultMemoryContent
}

// ============================================================================
// Memory Injection Rendering
// ============================================================================

// RenderMemoryForInjection 将记忆内容渲染为可注入 system prompt 的格式。
// 空内容或默认模板返回空字符串（表示无需注入）。
func RenderMemoryForInjection(content string) string {
	if content == "" || content == DefaultMemoryContent {
		return ""
	}
	return fmt.Sprintf(
		"\n\n<repository_knowledge>\nThe following is accumulated knowledge from your previous analysis sessions on this repository.\nUse this as prior context. If new findings contradict this knowledge, trust the new findings.\n\n%s\n</repository_knowledge>",
		content,
	)
}

// ============================================================================
// Token Budget Enforcement
// ============================================================================

// EnforceTokenBudget 截断内容使其不超过 MaxMemoryTokens 的 token 预算。
// 尝试在 Markdown 分区边界处截断以保持结构完整。
func EnforceTokenBudget(content string) string {
	maxChars := MaxMemoryTokens * charsPerToken
	if len(content) <= maxChars {
		return content
	}

	// 尝试在最后一个完整的分区边界处截断
	truncated := content[:maxChars]
	lastSection := strings.LastIndex(truncated, "\n## ")
	if lastSection > maxChars/2 {
		return truncated[:lastSection]
	}
	return truncated
}

// EstimateTokens 估算字符串的 token 数。
// 使用保守的 3 字符/token 估算。仅用于预算检查，不用于计费。
func EstimateTokens(text string) int {
	return len(text) / charsPerToken
}

// ============================================================================
// Format Validation
// ============================================================================

// ValidateMemoryFormat 检查记忆内容是否包含所有必需的分区标题。
// 只做浅层检查，不验证具体内容。
func ValidateMemoryFormat(content string) bool {
	if content == "" {
		return false
	}
	for _, section := range AllMemorySections {
		heading := "## " + string(section)
		if !strings.Contains(content, heading) {
			return false
		}
	}
	return true
}

// ============================================================================
// Observation Truncation
// ============================================================================

const maxObservationChars = 10000

// TruncateObservations 限制观察文本长度，防止 LLM consolidation 调用过大。
func TruncateObservations(text string) string {
	if len(text) <= maxObservationChars {
		return text
	}
	return text[:maxObservationChars] + "\n...(truncated)"
}
