package globalctx

import "sync"

// RepoContextStore 带锁的仓库上下文存储。
// 替代原 GlobalCtx.RepoSummary 共享可变字段：Director 在处理 delegate_repo
// 工具结果时写入（Set），prompt 构建器（coding/browser agent）读取（Get）。
// 使用 sync.RWMutex 保护，消除原先对共享字符串字段的并发读写数据竞争。
type RepoContextStore struct {
	mu      sync.RWMutex
	summary string
}

// NewRepoContextStore 创建一个空的 RepoContextStore。
func NewRepoContextStore() *RepoContextStore {
	return &RepoContextStore{}
}

// Get 读取当前仓库上下文摘要（读锁）。
func (s *RepoContextStore) Get() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.summary
}

// Set 更新仓库上下文摘要（写锁）。
func (s *RepoContextStore) Set(summary string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.summary = summary
}
