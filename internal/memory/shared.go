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
	messages         []VersionedMessage              // internal MVCC storage
	maxSize          int                             // default: 500
	subscribers      []func(ChatMessage)             // notify on new message
	mu               sync.RWMutex

	// MVCC fields
	version          uint64                          // 全局版本计数器
	subIDCounter     uint64                          // 订阅 ID 计数器
	topicSubscribers map[string][]Subscriber          // topic → subscribers
	config           MVCCConfig                       // MVCC 配置
	gcClosed         chan struct{}                    // GC 控制

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
		messages:         make([]VersionedMessage, 0),
		maxSize:          maxSize,
		config:           DefaultMVCCConfig(),
		topicSubscribers: make(map[string][]Subscriber),
	}
}

// AddMessage adds a message to shared memory and notifies subscribers.
// Backward-compatible: delegates to Publish with topic "default".
func (sm *SharedMemory) AddMessage(msg ChatMessage) error {
	return sm.Publish(msg, "default")
}

// Publish adds a message with a topic, assigns a version number,
// and notifies subscribers (both legacy and MVCC-aware).
func (sm *SharedMemory) Publish(msg ChatMessage, topic string) error {
	sm.mu.Lock()

	// Assign version
	sm.version++

	// Create versioned message
	vMsg := VersionedMessage{
		ChatMessage: msg,
		Version:     sm.version,
		Topic:       topic,
	}
	if id, ok := msg.Metadata["agent_id"].(string); ok {
		vMsg.AgentID = id
	}
	if vMsg.Timestamp.IsZero() {
		vMsg.Timestamp = time.Now()
	}

	// Store as VersionedMessage for MVCC
	sm.messages = append(sm.messages, vMsg)

	// Trim if exceeds max size
	if len(sm.messages) > sm.maxSize {
		overflow := len(sm.messages) - sm.maxSize
		sm.messages = sm.messages[overflow:]
	}

	// Copy subscribers for notification outside lock
	allSubs := make([]func(ChatMessage), len(sm.subscribers))
	copy(allSubs, sm.subscribers)

	// Also copy topic-specific subscribers
	topicSubs := sm.topicSubscribers[topic]
	topicSubsCopy := make([]Subscriber, len(topicSubs))
	copy(topicSubsCopy, topicSubs)

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

	// Notify MVCC subscribers with filter
	for _, sub := range topicSubsCopy {
		if matchesFilter(sub.Filter, vMsg) {
			go func(s Subscriber, m VersionedMessage) {
				defer func() {
					if r := recover(); r != nil {
						slog.Warn("[Memory] MVCC subscriber panicked", "recover", r)
					}
				}()
				s.Handler(m)
			}(sub, vMsg)
		}
	}

	return nil
}

// GetMessages returns all messages (thread-safe copy).
// Converts internal VersionedMessage to ChatMessage for backward compatibility.
func (sm *SharedMemory) GetMessages() []ChatMessage {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	result := make([]ChatMessage, len(sm.messages))
	for i, vmsg := range sm.messages {
		result[i] = vmsg.ChatMessage
	}
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
	for _, vmsg := range sm.messages {
		agentInfo := ""
		if vmsg.AgentID != "" {
			agentInfo = fmt.Sprintf(" (from: %s)", vmsg.AgentID)
		}
		sb.WriteString(fmt.Sprintf("[%s%s] %s\n", string(vmsg.Type), agentInfo, vmsg.Content))
	}
	return sb.String()
}

// Clear clears all messages.
func (sm *SharedMemory) Clear() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.messages = make([]VersionedMessage, 0)
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
	for _, vmsg := range sm.messages {
		if vmsg.AgentID == agentID {
			filtered = append(filtered, vmsg.ChatMessage)
		}
	}
	return filtered
}

// Compact summarizes old messages (placeholder for Phase 2 enhancement).
func (sm *SharedMemory) Compact() error {
	// Future: use LLM to summarize older messages
	return nil
}

// ============================================================================
// Phase 2: MVCC Version Control
// ============================================================================

// VersionedMessage wraps a ChatMessage with MVCC version metadata
type VersionedMessage struct {
	ChatMessage
	Version uint64 `json:"version"` // 全局递增版本号
	Topic   string `json:"topic"`   // 消息主题（用于 pub/sub 过滤）
	AgentID string `json:"agent_id"` // 发布者 ID
}

// MVCCSnapshot represents a consistent point-in-time view of SharedMemory
type MVCCSnapshot struct {
	Version   uint64           `json:"version"`  // 快照版本号
	Messages  []VersionedMessage `json:"messages"` // 快照时的消息列表
	Timestamp time.Time        `json:"timestamp"`
}

// SubscriptionFilter defines criteria for filtering shared memory updates
type SubscriptionFilter struct {
	Topics   []string `json:"topics,omitempty"`   // 订阅的主题列表（空 = 全部）
	AgentIDs []string `json:"agent_ids,omitempty"` // 发布者过滤（空 = 全部）
	Types    []string `json:"types,omitempty"`     // 消息类型过滤（message type）
}

// SubscriptionID is a unique identifier for a subscription
type SubscriptionID uint64

// Subscriber represents a registered subscriber with filter
type Subscriber struct {
	ID      SubscriptionID   `json:"-"`
	Filter  SubscriptionFilter `json:"filter"`
	Handler func(VersionedMessage) `json:"-"`
}

// MVCCConfig configures MVCC behavior
type MVCCConfig struct {
	MaxVersionsToKeep  int           // 保留的最大版本数（GC 用，默认 1000）
	GCTriggerInterval  time.Duration // GC 触发间隔（默认 5 分钟）
}

// DefaultMVCCConfig returns default MVCC configuration
func DefaultMVCCConfig() MVCCConfig {
	return MVCCConfig{
		MaxVersionsToKeep:  1000,
		GCTriggerInterval:  5 * time.Minute,
	}
}

// Snapshot returns the current version number for later incremental reads
func (sm *SharedMemory) Snapshot() uint64 {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.version
}

// GetMessagesSince returns all non-deleted messages after the given version
func (sm *SharedMemory) GetMessagesSince(version uint64) []VersionedMessage {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	result := make([]VersionedMessage, 0)
	for _, msg := range sm.messages {
		if msg.Version > version {
			result = append(result, msg)
		}
	}
	return result
}

// GetMessagesByTopic filters messages by topic since a version
func (sm *SharedMemory) GetMessagesByTopic(topic string, sinceVersion uint64) []VersionedMessage {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	result := make([]VersionedMessage, 0)
	for _, msg := range sm.messages {
		if msg.Version > sinceVersion && msg.Topic == topic {
			result = append(result, msg)
		}
	}
	return result
}

// SubscribeWithFilter registers a subscriber with filtering
// Returns a SubscriptionID for unsubscription
func (sm *SharedMemory) SubscribeWithFilter(filter SubscriptionFilter, handler func(VersionedMessage)) SubscriptionID {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.subIDCounter++
	id := SubscriptionID(sm.subIDCounter)

	sub := Subscriber{
		ID:      id,
		Filter:  filter,
		Handler: handler,
	}

	// Register under specified topics (or "default")
	topics := filter.Topics
	if len(topics) == 0 {
		topics = []string{"default"}
	}

	for _, topic := range topics {
		sm.topicSubscribers[topic] = append(sm.topicSubscribers[topic], sub)
	}

	return id
}

// Unsubscribe removes a subscription by ID
func (sm *SharedMemory) Unsubscribe(id SubscriptionID) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	for topic, subs := range sm.topicSubscribers {
		for i, sub := range subs {
			if sub.ID == id {
				sm.topicSubscribers[topic] = append(subs[:i], subs[i+1:]...)
				return true
			}
		}
	}
	return false
}

// matchesFilter checks if a versioned message matches the subscription filter
func matchesFilter(filter SubscriptionFilter, msg VersionedMessage) bool {
	// Check topic filter
	if len(filter.Topics) > 0 {
		topicMatched := false
		for _, t := range filter.Topics {
			if msg.Topic == t {
				topicMatched = true
				break
			}
		}
		if !topicMatched {
			return false
		}
	}

	// Check agent ID filter
	if len(filter.AgentIDs) > 0 {
		idMatched := false
		for _, id := range filter.AgentIDs {
			if msg.AgentID == id {
				idMatched = true
				break
			}
		}
		if !idMatched {
			return false
		}
	}

	// Check message type filter
	if len(filter.Types) > 0 {
		typeMatched := false
		for _, t := range filter.Types {
			if string(msg.Type) == t {
				typeMatched = true
				break
			}
		}
		if !typeMatched {
			return false
		}
	}

	return true
}

// GC performs garbage collection on old versions
func (sm *SharedMemory) GC() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if len(sm.messages) <= sm.config.MaxVersionsToKeep {
		return
	}

	// Keep only the most recent MaxVersionsToKeep messages
	keepFrom := len(sm.messages) - sm.config.MaxVersionsToKeep
	keep := make([]VersionedMessage, 0, sm.config.MaxVersionsToKeep)
	copy(keep, sm.messages[keepFrom:])
	sm.messages = keep
}

// StartGC starts the background GC loop (call in a goroutine)
func (sm *SharedMemory) StartGC() {
	if sm.gcClosed != nil {
		return // already started
	}
	sm.gcClosed = make(chan struct{})
	go sm.gcLoop()
}

// StopGC stops the background GC loop
func (sm *SharedMemory) StopGC() {
	if sm.gcClosed != nil {
		close(sm.gcClosed)
		sm.gcClosed = nil
	}
}

func (sm *SharedMemory) gcLoop() {
	ticker := time.NewTicker(sm.config.GCTriggerInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			sm.GC()
		case <-sm.gcClosed:
			return
		}
	}
}

// GetMVCCConfig returns the current MVCC config
func (sm *SharedMemory) GetMVCCConfig() MVCCConfig {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.config
}

// SetMVCCConfig updates the MVCC configuration
func (sm *SharedMemory) SetMVCCConfig(cfg MVCCConfig) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if cfg.MaxVersionsToKeep > 0 {
		sm.config.MaxVersionsToKeep = cfg.MaxVersionsToKeep
	}
	if cfg.GCTriggerInterval > 0 {
		sm.config.GCTriggerInterval = cfg.GCTriggerInterval
	}
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
	Version   uint64            `json:"version"`
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
		Version:   sm.version,
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
	sm.version = snapshot.Version

	slog.Info("Shared memory restored from file",
		"path", sm.persistPath,
		"kv_entries", len(snapshot.KV),
		"version", snapshot.Version,
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
