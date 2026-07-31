package memory

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

// SharedMemory is global context visible to all agents.
// Thread-safe for concurrent access from multiple agents.
type SharedMemory struct {
	messages    []ChatMessage             // internal message storage
	maxSize     int                       // default: 500
	subscribers []func(ChatMessage)       // notify on new message
	mu          sync.RWMutex

	// KV store for simple key-value persistence
	kv   map[string]string
	kvMu sync.RWMutex

	// ---- Persistent storage ----
	persistPath string        // file path for persistence, empty = no persistence
	persistTick *time.Ticker  // periodic save ticker
	persistDone chan struct{} // signal to stop ticker
	persistWg   sync.WaitGroup
	dirty       bool          // whether there's unsaved data
}

// NewSharedMemory creates a new shared memory.
func NewSharedMemory(maxSize int) *SharedMemory {
	if maxSize <= 0 {
		maxSize = 500
	}
	return &SharedMemory{
		messages:    make([]ChatMessage, 0),
		maxSize:     maxSize,
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

// Compact summarizes old messages (placeholder for future enhancement).
func (sm *SharedMemory) Compact() error {
	// Future: use LLM to summarize older messages
	return nil
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
	go sm.persistLoop()

	return nil
}

// persistLoop runs periodically to flush dirty data to file.
func (sm *SharedMemory) persistLoop() {
	defer sm.persistWg.Done()

	for {
		select {
		case <-sm.persistTick.C:
			sm.flushToFile()
		case <-sm.persistDone:
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

	// Atomic write: write to temp file, then rename
	tmpFile := sm.persistPath + ".tmp"
	if err := os.WriteFile(tmpFile, payload, 0644); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := os.Rename(tmpFile, sm.persistPath); err != nil {
		return fmt.Errorf("rename temp to target: %w", err)
	}

	return nil
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
func (sm *SharedMemory) Close() error {
	sm.kvMu.Lock()
	if sm.persistDone == nil {
		sm.kvMu.Unlock()
		return nil
	}
	done := sm.persistDone
	sm.persistDone = nil
	sm.persistTick = nil
	sm.kvMu.Unlock()

	close(done)
	if sm.persistTick != nil {
		sm.persistTick.Stop()
	}
	sm.persistWg.Wait()
	return nil
}
