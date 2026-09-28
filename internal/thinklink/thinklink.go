// Package thinklink 提供 Director "终极上下文压缩"（Ultimate Context Compression）的数据支撑：
// 以追加方式实时记录 (1) 用户原始输入与 (2) Director 输出的 Thought & Plan 块。
// 当两级常规压缩（tool 结果截断 / 紧急压缩）仍无法控制上下文体积时，
// 可通过 RebuildPrompt 用这些记录重建一份极简 user 消息，
// 使任务在上下文被重置后无需用户介入即可无缝续接。
package thinklink

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// Kind thinklink 条目种类。
type Kind int

const (
	// KindUserInput 用户原始输入。第一条此类条目承载最初的任务需求，永不淘汰。
	KindUserInput Kind = iota
	// KindThoughtPlan Director 输出的 Thought & Plan 块。
	KindThoughtPlan
)

// String 返回 Kind 的稳定字符串表示（用于事件 payload / 日志等序列化场景）。
func (k Kind) String() string {
	switch k {
	case KindUserInput:
		return "user_input"
	case KindThoughtPlan:
		return "thought_plan"
	default:
		return fmt.Sprintf("kind(%d)", int(k))
	}
}

// DefaultMaxEntries Store 默认容量上限（条数），超出时按最旧优先淘汰。
const DefaultMaxEntries = 200

// Entry thinklink 单条记录。
type Entry struct {
	ID        string    // 条目 ID（时间戳+序号生成）
	Kind      Kind      // 条目种类
	Content   string    // 内容原文
	Timestamp time.Time // 记录时间
	Step      int       // 记录发生时 Director 的步数（无则为 0）
}

// Store 线程安全的 thinklink 存储。
// 容量超限时淘汰最旧条目，但永远不允许丢弃第一条 KindUserInput 条目
// （它是最初的用户原始输入，是终极压缩重建上下文的底线信息）。
type Store struct {
	mu         sync.RWMutex
	entries    []Entry
	idSeq      uint64
	maxEntries int // 容量上限（条数），超出时按最旧优先淘汰
}

// NewStore 创建一个空 Store。maxEntries 为容量上限（条数），
// maxEntries<=0 时使用 DefaultMaxEntries。
func NewStore(maxEntries int) *Store {
	if maxEntries <= 0 {
		maxEntries = DefaultMaxEntries
	}
	return &Store{
		entries:    make([]Entry, 0, maxEntries),
		maxEntries: maxEntries,
	}
}

// AddUserInput 记录一条用户原始输入，返回写入的条目；成功写入时 added 为 true。
//   - 空白内容不记录；
//   - 去重：若最后一条同为用户输入且内容完全相同则跳过（防止重复注入同一任务）。
func (s *Store) AddUserInput(content string, step int) (Entry, bool) {
	if strings.TrimSpace(content) == "" {
		return Entry{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := len(s.entries); n > 0 {
		if last := s.entries[n-1]; last.Kind == KindUserInput && last.Content == content {
			return Entry{}, false
		}
	}
	return s.appendLocked(KindUserInput, content, step), true
}

// AddThoughtPlan 记录一条 Thought & Plan 块，返回写入的条目；
// 成功写入时 added 为 true。空白内容不记录。
func (s *Store) AddThoughtPlan(content string, step int) (Entry, bool) {
	if strings.TrimSpace(content) == "" {
		return Entry{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendLocked(KindThoughtPlan, content, step), true
}

// Snapshot 返回全部条目的深拷贝切片（线程安全，修改副本不影响内部状态）。
func (s *Store) Snapshot() []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Entry, len(s.entries))
	copy(out, s.entries)
	return out
}

// Len 返回当前条目总数。
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries)
}

// Count 返回指定 Kind 的条目数（线程安全）。
func (s *Store) Count(kind Kind) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, e := range s.entries {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

// RebuildPrompt 生成终极压缩用的新输入文本（英文，将作为 user 消息发给 LLM）：
//   - 顶部说明段：告知模型上下文因超出 token 限制已被重置，须无缝继续任务；
//   - "Original user input(s)" 区：全部用户原始输入按时间顺序列出，
//     最后一条前标注 [CURRENT TASK]；无论 keepPlans 取值，此区始终全部保留；
//   - "Thought & Plan blocks" 区：按时间顺序列出，带 [TP-n] 全局序号与时间戳；
//   - keepPlans>0 时仅保留最近 keepPlans 个 T&P 块；keepPlans<=0 表示全部保留。
func (s *Store) RebuildPrompt(keepPlans int) string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var userInputs, plans []Entry
	for _, e := range s.entries {
		if e.Kind == KindUserInput {
			userInputs = append(userInputs, e)
		} else {
			plans = append(plans, e)
		}
	}

	var sb strings.Builder
	sb.WriteString("Your previous conversation context has been RESET because it exceeded the token limit. ")
	sb.WriteString("Below are the original requirements of the task and all Thought & Plan progress recorded so far. ")
	sb.WriteString("Seamlessly continue the task from where it left off: do NOT ask the user any questions, do NOT apologize, and do NOT repeat work that has already been completed.\n")

	// ── Original user input(s)：无论 keepPlans 取值，全部保留 ──
	sb.WriteString("\n=== Original user input(s) ===\n")
	if len(userInputs) == 0 {
		sb.WriteString("(no user input recorded)\n")
	}
	for i, e := range userInputs {
		marker := ""
		if i == len(userInputs)-1 {
			marker = "[CURRENT TASK] "
		}
		fmt.Fprintf(&sb, "\n%s[USER INPUT %d] (%s, step %d)\n%s\n",
			marker, i+1, e.Timestamp.Format(time.RFC3339), e.Step, e.Content)
	}

	// ── Thought & Plan blocks：keepPlans>0 时仅保留最近 keepPlans 个 ──
	offset := 0
	if keepPlans > 0 && len(plans) > keepPlans {
		offset = len(plans) - keepPlans
		plans = plans[offset:]
	}
	sb.WriteString("\n=== Thought & Plan blocks ===\n")
	if len(plans) == 0 {
		sb.WriteString("(no Thought & Plan blocks recorded)\n")
	}
	for i, e := range plans {
		fmt.Fprintf(&sb, "\n[TP-%d] (%s, step %d)\n%s\n",
			offset+i+1, e.Timestamp.Format(time.RFC3339), e.Step, e.Content)
	}

	return sb.String()
}

// appendLocked 追加一条记录并返回写入的条目；超出容量时淘汰（调用方须持有写锁）。
func (s *Store) appendLocked(kind Kind, content string, step int) Entry {
	s.idSeq++
	entry := Entry{
		ID:        fmt.Sprintf("tl-%d-%06d", time.Now().UnixNano(), s.idSeq),
		Kind:      kind,
		Content:   content,
		Timestamp: time.Now(),
		Step:      step,
	}
	s.entries = append(s.entries, entry)
	s.evictLocked()
	return entry
}

// evictLocked 容量淘汰：丢弃最旧条目，但第一条 KindUserInput 条目永久保留。
// 若最旧条目正是受保护的用户输入，则从其下一条开始淘汰（调用方须持有写锁）。
func (s *Store) evictLocked() {
	for len(s.entries) > s.maxEntries {
		start := 0
		if s.entries[0].Kind == KindUserInput {
			start = 1
		}
		if start >= len(s.entries) {
			// 只剩受保护的第一条，无法继续淘汰
			return
		}
		s.entries = append(s.entries[:start], s.entries[start+1:]...)
	}
}
