// todo_state.go 提供 TodoWrite 任务清单的数据层支撑（方案C·批次1）：
//   - TodoItem / TodoSnapshot：任务清单快照模型（全量替换语义）；
//   - CompletedLedgerEntry：完成台账（由"非 completed → completed"显式迁移追加，FIFO 有界）；
//   - Store 新增 API：SetTodos / CurrentTodos / TodoHistory / CompletedLedger / TodoRevision；
//   - 淘汰策略调整见 Store.evictLocked（旧快照优先淘汰，首条用户输入与最新快照永不淘汰）。
//
// 快照以 JSON 序列化存入 KindTodoSnapshot 条目的 Content 字段，与既有追加流
// （KindUserInput / KindTodoSnapshot）共存于同一容量池；本文件只做纯内存状态管理，
// 不含事件发布与工具适配（见 internal/agents/todo_tool.go）。
package thinklink

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// TodoStatus 任务清单条目状态（与 Claude Code TodoWrite 对齐的三态枚举）。
type TodoStatus string

const (
	// StatusPending 待办。
	StatusPending TodoStatus = "pending"
	// StatusInProgress 进行中；同一快照内至多 1 条（确定性校验保证）。
	StatusInProgress TodoStatus = "in_progress"
	// StatusCompleted 已完成。
	StatusCompleted TodoStatus = "completed"
)

// MaxTodoItems 单次 TodoWrite 快照允许的最大条目数（防止 LLM 生成失控的超长清单）。
const MaxTodoItems = 25

// DefaultMaxLedgerEntries 完成台账容量上限（FIFO，超出时淘汰最旧记录）。
const DefaultMaxLedgerEntries = 200

// TodoItem 任务清单单条条目。
// Content / ActiveForm trim 后必须非空；Status 必须为三态枚举之一；
// 同一快照中 in_progress 至多 1 条。
type TodoItem struct {
	Content    string     `json:"content"`
	ActiveForm string     `json:"active_form"`
	Status     TodoStatus `json:"status"`
}

// TodoSnapshot 任务清单快照：序列化为 JSON 存入 KindTodoSnapshot 条目的 Content。
// Revision 全局单调递增（相同快照刷新时同样递增，作为"有更新"的版本信号，
// 供事件 payload 使用，TUI 无需 diff）。
type TodoSnapshot struct {
	Items     []TodoItem `json:"items"`
	Revision  int        `json:"revision"`
	UpdatedAt time.Time  `json:"updated_at"`
	Step      int        `json:"step"`
}

// CompletedLedgerEntry 完成台账单条记录：条目由非 completed 变为 completed 时
// 显式追加（条目被删除不算"完成"；删除后重现按 content 文本匹配台账去重，
// 不重复记录）。
type CompletedLedgerEntry struct {
	Content         string    `json:"content"`
	ActiveForm      string    `json:"active_form"`
	CompletedAtStep int       `json:"completed_at_step"`
	CompletedAtTime time.Time `json:"completed_at_time"`
}

// ValidateTodoItems 校验任务清单条目的确定性约束（工具层与 Store 共用同一校验逻辑）：
//   - 长度 ≤ MaxTodoItems（空数组合法，语义为清空）；
//   - 每条 content / active_form trim 后非空；
//   - status 必须为三态枚举之一；
//   - in_progress 至多 1 条。
func ValidateTodoItems(items []TodoItem) error {
	if len(items) > MaxTodoItems {
		return fmt.Errorf("todos must contain at most %d items, got %d (empty array is allowed and clears the list)", MaxTodoItems, len(items))
	}
	inProgress := 0
	for i, it := range items {
		if strings.TrimSpace(it.Content) == "" {
			return fmt.Errorf("todos[%d].content must be a non-empty string", i)
		}
		if strings.TrimSpace(it.ActiveForm) == "" {
			return fmt.Errorf("todos[%d].active_form must be a non-empty string", i)
		}
		switch it.Status {
		case StatusPending, StatusInProgress, StatusCompleted:
		default:
			return fmt.Errorf("todos[%d].status must be one of %q/%q/%q, got %q", i, StatusPending, StatusInProgress, StatusCompleted, string(it.Status))
		}
		if it.Status == StatusInProgress {
			inProgress++
		}
	}
	if inProgress > 1 {
		return fmt.Errorf("at most 1 todo item can be %q, got %d", StatusInProgress, inProgress)
	}
	return nil
}

// SetTodos 以全量替换语义写入任务清单快照，返回 changed（items 是否相对前值变化）。
// 写入链路（对齐设计文档 5.4）：
//  1. 确定性校验先行：content/active_form 非空（trim 后）、status 合法枚举、
//     in_progress 数量 ≤1、todos 长度 ≤25；允许空数组（=清空）；
//  2. 与上一条快照完全相同时仅更新该条目的 step/revision（不追加新条目，防刷流）；
//  3. 不同则追加 KindTodoSnapshot 条目并钉住为最新（淘汰策略永不淘汰最新快照）；
//  4. 对比前后快照做 CompletedLedger 迁移（幂等）。
func (s *Store) SetTodos(items []TodoItem, step int) (bool, error) {
	if err := ValidateTodoItems(items); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	prevItems := s.currentTodoItemsLocked()
	// 台账迁移放在 same 判断之前：相同快照重复调用时迁移天然幂等（前值已
	// completed 的条目被跳过、台账按 content 去重），不产生重复记录。
	s.migrateCompletedLedgerLocked(prevItems, items, step)

	if todoItemsEqual(prevItems, items) {
		// 相同快照：仅更新最新快照条目的 step/revision/timestamp，不追加（防刷流）
		s.todoRevision++
		s.refreshLatestTodoEntryLocked(items, step)
		return false, nil
	}

	s.todoRevision++
	if err := s.appendTodoSnapshotLocked(items, step); err != nil {
		s.todoRevision-- // 序列化失败时回滚修订号（未产生任何写入）
		return false, err
	}
	return true, nil
}

// CurrentTodos 返回当前任务清单条目的深拷贝（反序列化产物，修改副本不影响内部状态）；
// 尚无任何 TodoWrite 快照时返回 nil。
func (s *Store) CurrentTodos() []TodoItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.currentTodoItemsLocked()
}

// TodoRevision 返回当前任务清单修订号（线程安全）。
// 每次 SetTodos（含相同快照刷新）递增，作为事件 payload 中"有更新"的版本信号。
func (s *Store) TodoRevision() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.todoRevision
}

// TodoHistory 返回最近 n 条快照（按时间顺序，旧→新）；n<=0 时返回全部。
// 只包含仍存活的快照条目（旧快照在容量压力下可能已被淘汰）。
func (s *Store) TodoHistory(n int) []TodoSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var snaps []TodoSnapshot
	for _, e := range s.entries {
		if e.Kind != KindTodoSnapshot {
			continue
		}
		var snap TodoSnapshot
		if err := json.Unmarshal([]byte(e.Content), &snap); err != nil {
			continue // 防御：损坏的快照条目跳过
		}
		snaps = append(snaps, snap)
	}
	if n > 0 && len(snaps) > n {
		return snaps[len(snaps)-n:]
	}
	return snaps
}

// CompletedLedger 返回完成台账的深拷贝（线程安全）。
func (s *Store) CompletedLedger() []CompletedLedgerEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]CompletedLedgerEntry, len(s.completedLedger))
	copy(out, s.completedLedger)
	return out
}

// ─── 内部实现（调用方须持有相应锁） ──────────────────────────────────────────

// latestTodoEntryIndexLocked 返回最新一条 KindTodoSnapshot 条目的索引；
// 尚无快照时返回 -1。
func (s *Store) latestTodoEntryIndexLocked() int {
	for i := len(s.entries) - 1; i >= 0; i-- {
		if s.entries[i].Kind == KindTodoSnapshot {
			return i
		}
	}
	return -1
}

// currentTodoItemsLocked 反序列化最新快照条目的 items；无快照或内容损坏时返回 nil。
func (s *Store) currentTodoItemsLocked() []TodoItem {
	idx := s.latestTodoEntryIndexLocked()
	if idx < 0 {
		return nil
	}
	var snap TodoSnapshot
	if err := json.Unmarshal([]byte(s.entries[idx].Content), &snap); err != nil {
		return nil
	}
	return snap.Items
}

// refreshLatestTodoEntryLocked 以新 revision/step 刷新最新快照条目（不追加）。
// 仅在"与上一条快照相同"分支调用；idx<0 时静默返回（防御：空清单首次调用等场景）。
func (s *Store) refreshLatestTodoEntryLocked(items []TodoItem, step int) {
	idx := s.latestTodoEntryIndexLocked()
	if idx < 0 {
		return
	}
	content, err := s.marshalTodoSnapshotLocked(items, step)
	if err != nil {
		return // 序列化不可能失败（纯基本类型）；防御性跳过
	}
	s.entries[idx].Content = content
	s.entries[idx].Step = step
	s.entries[idx].Timestamp = time.Now()
}

// appendTodoSnapshotLocked 追加一条 KindTodoSnapshot 条目并经 evictLocked 维持容量。
func (s *Store) appendTodoSnapshotLocked(items []TodoItem, step int) error {
	content, err := s.marshalTodoSnapshotLocked(items, step)
	if err != nil {
		return err
	}
	s.appendLocked(KindTodoSnapshot, content, step)
	return nil
}

// marshalTodoSnapshotLocked 序列化当前修订号下的快照（调用方须持有写锁）。
func (s *Store) marshalTodoSnapshotLocked(items []TodoItem, step int) (string, error) {
	snap := TodoSnapshot{
		Items:     cloneTodoItems(items),
		Revision:  s.todoRevision,
		UpdatedAt: time.Now(),
		Step:      step,
	}
	content, err := json.Marshal(snap)
	if err != nil {
		return "", fmt.Errorf("serialize todo snapshot: %w", err)
	}
	return string(content), nil
}

// migrateCompletedLedgerLocked 对比前后快照做完成台账迁移：
//   - 新快照中 completed 且前值非 completed（或前值中不存在）→ 追加台账；
//   - 条目被删除（前值有、新值无）→ 不算"完成"，不追加；
//   - 删除后重现且再次 completed → 按 content 文本匹配台账去重，不重复记录；
//   - 追加后维持 DefaultMaxLedgerEntries 容量（FIFO 淘汰最旧记录）。
func (s *Store) migrateCompletedLedgerLocked(prev, next []TodoItem, step int) {
	prevStatus := make(map[string]TodoStatus, len(prev))
	for _, it := range prev {
		prevStatus[it.Content] = it.Status
	}
	for _, it := range next {
		if it.Status != StatusCompleted {
			continue
		}
		// 前值中该条目已是 completed → 非状态迁移，跳过
		if ps, ok := prevStatus[it.Content]; ok && ps == StatusCompleted {
			continue
		}
		// 台账按 content 去重（涵盖"删除后重现"场景）
		if s.ledgerHasContentLocked(it.Content) {
			continue
		}
		s.completedLedger = append(s.completedLedger, CompletedLedgerEntry{
			Content:         it.Content,
			ActiveForm:      it.ActiveForm,
			CompletedAtStep: step,
			CompletedAtTime: time.Now(),
		})
	}
	if overflow := len(s.completedLedger) - DefaultMaxLedgerEntries; overflow > 0 {
		s.completedLedger = append([]CompletedLedgerEntry(nil), s.completedLedger[overflow:]...)
	}
}

// ledgerHasContentLocked 判断台账中是否已存在同 content 记录。
func (s *Store) ledgerHasContentLocked(content string) bool {
	for _, le := range s.completedLedger {
		if le.Content == content {
			return true
		}
	}
	return false
}

// todoItemsEqual 逐条比较两组 items（content/active_form/status 全等）。
// nil 与空切片视为相等（都表示空清单）。
func todoItemsEqual(a, b []TodoItem) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Content != b[i].Content || a[i].ActiveForm != b[i].ActiveForm || a[i].Status != b[i].Status {
			return false
		}
	}
	return true
}

// cloneTodoItems 深拷贝 items；空切片返回 nil（序列化为 null，反序列化仍为空清单）。
func cloneTodoItems(items []TodoItem) []TodoItem {
	if len(items) == 0 {
		return nil
	}
	out := make([]TodoItem, len(items))
	copy(out, items)
	return out
}
