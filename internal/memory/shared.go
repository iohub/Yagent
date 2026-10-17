package memory

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"yagent/internal/util"
)

// SharedMemory is global context visible to all agents.
// Thread-safe for concurrent access from multiple agents.
type SharedMemory struct {
	messages    []ChatMessage       // internal message storage
	maxSize     int                 // default: 500
	subscribers []func(ChatMessage) // notify on new message
	mu          sync.RWMutex

	// KV store for simple key-value persistence
	kv   map[string]string
	kvMu sync.RWMutex

	// ---- Persistent storage ----
	persistPath string        // file path for persistence, empty = no persistence
	persistTick *time.Ticker  // periodic save ticker
	persistDone chan struct{} // signal to stop ticker
	persistWg   sync.WaitGroup
	dirty       bool // whether there's unsaved data
}

// NewSharedMemory creates a new shared memory.
func NewSharedMemory(maxSize int) *SharedMemory {
	if maxSize <= 0 {
		maxSize = 500
	}
	return &SharedMemory{
		messages: make([]ChatMessage, 0),
		maxSize:  maxSize,
	}
}

// AddMessage adds a message to shared memory and notifies subscribers.
func (sm *SharedMemory) AddMessage(msg ChatMessage) error {
	return sm.Publish(msg)
}

// Publish adds a message to shared memory and notifies subscribers.
func (sm *SharedMemory) Publish(msg ChatMessage) error {
	sm.mu.Lock()

	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now()
	}

	sm.messages = append(sm.messages, msg)

	// Trim if exceeds max size
	if len(sm.messages) > sm.maxSize {
		overflow := len(sm.messages) - sm.maxSize
		sm.messages = sm.messages[overflow:]
	}

	// Copy subscribers for notification outside lock
	allSubs := make([]func(ChatMessage), len(sm.subscribers))
	copy(allSubs, sm.subscribers)

	sm.mu.Unlock()

	// Notify legacy subscribers (non-blocking, in goroutine)
	for _, fn := range allSubs {
		go func(f func(ChatMessage)) {
			defer func() {
				if r := recover(); r != nil {
					slog.Warn("[Memory] subscriber panicked", "recover", r)
				}
			}()
			fn(msg)
		}(fn)
	}

	return nil
}

// GetMessages returns all messages (thread-safe copy).
func (sm *SharedMemory) GetMessages() []ChatMessage {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	result := make([]ChatMessage, len(sm.messages))
	copy(result, sm.messages)
	return result
}

// GetContext returns formatted context string.
func (sm *SharedMemory) GetContext() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if len(sm.messages) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("--- Shared Context ---\n")
	for _, msg := range sm.messages {
		agentInfo := ""
		if id, ok := msg.Metadata["agent_id"].(string); ok && id != "" {
			agentInfo = fmt.Sprintf(" (from: %s)", id)
		}
		sb.WriteString(fmt.Sprintf("[%s%s] %s\n", string(msg.Type), agentInfo, msg.Content))
	}
	return sb.String()
}

// Clear clears all messages.
func (sm *SharedMemory) Clear() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.messages = make([]ChatMessage, 0)
	return nil
}

// Size returns the number of messages.
func (sm *SharedMemory) Size() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return len(sm.messages)
}

// Subscribe registers a callback for new messages.
// Returns an unsubscribe function.
func (sm *SharedMemory) Subscribe(fn func(ChatMessage)) func() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.subscribers = append(sm.subscribers, fn)
	index := len(sm.subscribers) - 1

	return func() {
		sm.mu.Lock()
		defer sm.mu.Unlock()
		sm.subscribers = append(sm.subscribers[:index], sm.subscribers[index+1:]...)
	}
}

// FilterByAgent returns messages from a specific agent.
func (sm *SharedMemory) FilterByAgent(agentID string) []ChatMessage {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	var filtered []ChatMessage
	for _, msg := range sm.messages {
		if id, ok := msg.Metadata["agent_id"].(string); ok && id == agentID {
			filtered = append(filtered, msg)
		}
	}
	return filtered
}

// SetKey stores a key-value pair. Thread-safe. Overwrites if key exists.
func (sm *SharedMemory) SetKey(key string, value string) error {
	sm.kvMu.Lock()
	defer sm.kvMu.Unlock()
	if sm.kv == nil {
		sm.kv = make(map[string]string)
	}
	sm.kv[key] = value
	sm.dirty = true
	return nil
}

// GetKey retrieves a value by key. Thread-safe.
// Returns error if key is not found.
func (sm *SharedMemory) GetKey(key string) (string, error) {
	sm.kvMu.RLock()
	defer sm.kvMu.RUnlock()
	if sm.kv == nil {
		return "", fmt.Errorf("key not found: %s", key)
	}
	val, ok := sm.kv[key]
	if !ok {
		return "", fmt.Errorf("key not found: %s", key)
	}
	return val, nil
}

// DeleteKey removes a key-value pair. Thread-safe. No-op if key doesn't exist.
func (sm *SharedMemory) DeleteKey(key string) error {
	sm.kvMu.Lock()
	defer sm.kvMu.Unlock()
	if sm.kv != nil {
		delete(sm.kv, key)
	}
	sm.dirty = true
	return nil
}

// HasKey checks if a key exists. Thread-safe.
func (sm *SharedMemory) HasKey(key string) bool {
	sm.kvMu.RLock()
	defer sm.kvMu.RUnlock()
	if sm.kv == nil {
		return false
	}
	_, ok := sm.kv[key]
	return ok
}

// ============================================================================
// Persistence Support
// ============================================================================

// SharedMemorySnapshot represents a serializable snapshot of the KV store for persistence.
type SharedMemorySnapshot struct {
	Timestamp time.Time         `json:"timestamp"`
	KV        map[string]string `json:"kv"`
}

// EnablePersistence enables file-based persistence of KV data.
// It loads any existing data from the file and starts a background goroutine
// that periodically flushes dirty data.
//
// saveInterval: how often to auto-save (e.g., 5 * time.Second)
// filePath:     where to save the JSON file
func (sm *SharedMemory) EnablePersistence(saveInterval time.Duration, filePath string) error {
	sm.kvMu.Lock()
	defer sm.kvMu.Unlock()

	sm.persistPath = filePath
	sm.persistDone = make(chan struct{})

	// Load existing data from file (if exists)
	if err := sm.loadFromFileLocked(); err != nil {
		// File doesn't exist is fine (first run)
		slog.Warn("Failed to load shared memory from file (may not exist yet)", "path", filePath, "error", err)
	}

	// Start periodic save
	sm.persistTick = time.NewTicker(saveInterval)
	sm.persistWg.Add(1)
	// 句柄在起跑时**捕获成参数**：原来 goroutine 的 select 每次迭代都重新读
	// sm.persistTick / sm.persistDone 字段，于是 Close 一旦先置 nil，
	// goroutine 就变成"nil channel 永不就绪"的永久阻塞（或 nil panic）。
	go sm.persistLoop(sm.persistDone, sm.persistTick)

	return nil
}

// persistLoop runs periodically to flush dirty data to file.
func (sm *SharedMemory) persistLoop(done <-chan struct{}, tick *time.Ticker) {
	defer sm.persistWg.Done()

	for {
		select {
		case <-tick.C:
			sm.flushToFile()
		case <-done:
			sm.flushToFile() // final save before exit
			return
		}
	}
}

// flushToFile writes dirty data to the persistence file.
// Safe to call concurrently; acquires kvMu internally.
func (sm *SharedMemory) flushToFile() {
	sm.kvMu.Lock()
	defer sm.kvMu.Unlock()

	if !sm.dirty || sm.persistPath == "" {
		return
	}

	if err := sm.saveToFileLocked(); err != nil {
		slog.Warn("Failed to persist shared memory", "path", sm.persistPath, "error", err)
		return // don't reset dirty; retry next tick
	}
	sm.dirty = false
}

// saveToFileLocked performs the actual file write.
// Caller must hold sm.kvMu.
func (sm *SharedMemory) saveToFileLocked() error {
	// Collect all KV data
	data := make(map[string]string, len(sm.kv))
	for k, v := range sm.kv {
		data[k] = v
	}

	// Build snapshot
	snapshot := SharedMemorySnapshot{
		Timestamp: time.Now(),
		KV:        data,
	}

	payload, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}

	// 原子写走全仓唯一内核（I21）。原来这里 rename 失败会把 .tmp 留在盘上，现在会清掉。
	return util.AtomicWrite(sm.persistPath, payload, util.AtomicWriteOptions{
		FilePerm:  0o644,
		TmpSuffix: ".tmp",
		Label:     "shared memory",
	})
}

// loadFromFileLocked loads KV data from the persistence file.
// Caller must hold sm.kvMu. Returns nil if the file does not exist (first run).
func (sm *SharedMemory) loadFromFileLocked() error {
	data, err := os.ReadFile(sm.persistPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // first run, no file is normal
		}
		return fmt.Errorf("read file: %w", err)
	}

	var snapshot SharedMemorySnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return fmt.Errorf("unmarshal snapshot: %w", err)
	}

	// Restore KV data
	if sm.kv == nil {
		sm.kv = make(map[string]string)
	}
	for k, v := range snapshot.KV {
		sm.kv[k] = v
	}

	slog.Info("Shared memory restored from file",
		"path", sm.persistPath,
		"kv_entries", len(snapshot.KV),
	)
	return nil
}

// MarkDirty marks the KV store as needing persistence on the next flush.
// This is called automatically by SetKey/DeleteKey when persistence is enabled.
func (sm *SharedMemory) MarkDirty() {
	sm.kvMu.Lock()
	defer sm.kvMu.Unlock()
	sm.dirty = true
}

// Close shuts down the SharedMemory persistence goroutine and performs a final flush.
// Safe to call multiple times or when persistence is not enabled.
//
// 置 nil 是安全的，因为 goroutine 的 select 读的是起跑时捕获的参数而不是这两个字段
// （见 EnablePersistence 里那条注释）—— 修之前正是那个重新读字段的写法让 Close 变成
// nil panic 或永久阻塞。persistDone 置 nil 同时给二次 Close 当幂等标记。
func (sm *SharedMemory) Close() error {
	sm.kvMu.Lock()
	if sm.persistDone == nil {
		sm.kvMu.Unlock()
		return nil
	}
	done := sm.persistDone
	tick := sm.persistTick
	sm.persistDone = nil
	sm.persistTick = nil
	sm.kvMu.Unlock()

	close(done)
	sm.persistWg.Wait()
	if tick != nil {
		tick.Stop()
	}
	return nil
}
