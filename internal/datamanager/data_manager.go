package datamanager

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"yagent/internal/memory"
	"yagent/internal/supervise"
)

const (
	DataDirName = ".yagent"
)

var taskFlushDebounce = 5 * time.Second

type taskWriterState struct {
	mu               sync.Mutex
	pending          []memory.ChatMessage
	timer            *time.Timer
	lastFlushedCount int
	needsFullRewrite bool
}

type DataManager struct {
	dataDir string
	mu      sync.Mutex
	writers map[string]*taskWriterState

	index       *taskIndex
	indexMu     sync.RWMutex
	indexLoaded bool
	persistCh   chan struct{}
	stopCh      chan struct{}

	lastReconcileCheck time.Time
	reconcileCheckMu   sync.Mutex

	grp *supervise.Group
}

func NewDataManager() (*DataManager, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	dataDir := filepath.Join(homeDir, DataDirName, "tasks")

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, err
	}

	grp, _ := supervise.New(context.Background(), "datamanager")
	dm := &DataManager{
		dataDir:   dataDir,
		writers:   make(map[string]*taskWriterState),
		persistCh: make(chan struct{}, 1),
		stopCh:    make(chan struct{}),
		grp:       grp,
	}

	grp.Go("datamanager/startup-reconcile", func(context.Context) {
		start := time.Now()
		if err := dm.ensureIndex(); err != nil {
			slog.Warn("Failed to load index for startup reconciliation",
				"error", err,
				"duration", time.Since(start))
			return
		}
		if err := dm.reconcileIndex(dm.index); err != nil {
			slog.Warn("Index reconciliation on startup completed with errors",
				"error", err,
				"duration", time.Since(start))
		} else {
			slog.Debug("Index reconciliation on startup completed",
				"duration", time.Since(start))
		}
	})

	return dm, nil
}

func (dm *DataManager) Start() {
	dm.grp.Go("datamanager/index-persist", func(context.Context) { dm.indexPersistLoop() })
}

func (dm *DataManager) getOrCreateWriter(taskID, filePath string) *taskWriterState {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	ws, ok := dm.writers[taskID]
	if !ok {
		ws = &taskWriterState{
			pending: make([]memory.ChatMessage, 0),
		}
		dm.writers[taskID] = ws
	}
	return ws
}

func (dm *DataManager) flushWriter(taskID, filePath string, ws *taskWriterState) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if err := dm.doFlush(filePath, ws); err != nil {
		slog.Warn("deferred task memory flush failed", "task_id", taskID, "path", filePath, "error", err)
		return
	}
	dm.updateIndexFromFile(taskID, filePath)
}

var flushHook func(filePath string)

func (dm *DataManager) doFlush(filePath string, ws *taskWriterState) error {
	if flushHook != nil {
		flushHook(filePath)
	}
	if ws.needsFullRewrite {
		f, err := os.Create(filePath)
		if err != nil {
			return err
		}
		defer f.Close()

		for _, msg := range ws.pending {
			data, err := json.Marshal(msg)
			if err != nil {
				return err
			}
			if _, err := f.Write(append(data, '\n')); err != nil {
				return err
			}
		}
		ws.needsFullRewrite = false
	} else if len(ws.pending) > 0 {
		f, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return err
		}
		defer f.Close()

		for _, msg := range ws.pending {
			data, err := json.Marshal(msg)
			if err != nil {
				return err
			}
			if _, err := f.Write(append(data, '\n')); err != nil {
				return err
			}
		}
	}

	ws.pending = ws.pending[:0]
	return nil
}

func (dm *DataManager) SaveTaskMemory(taskID string, mem *memory.ConversationMemory) error {
	filePath := filepath.Join(dm.dataDir, taskID+".jsonl")

	ws := dm.getOrCreateWriter(taskID, filePath)
	ws.mu.Lock()
	defer ws.mu.Unlock()

	snapshot := mem.Snapshot()
	currentCount := len(snapshot)

	if currentCount < ws.lastFlushedCount {
		ws.needsFullRewrite = true
		ws.pending = ws.pending[:0]
		ws.lastFlushedCount = 0
	}

	newStart := ws.lastFlushedCount
	if newStart < 0 {
		newStart = 0
	}
	if currentCount > newStart {
		for i := newStart; i < currentCount; i++ {
			ws.pending = append(ws.pending, snapshot[i])
		}
	}
	ws.lastFlushedCount = currentCount

	if ws.timer != nil {
		ws.timer.Stop()
	}
	ws.timer = time.AfterFunc(taskFlushDebounce, func() {
		dm.flushWriter(taskID, filePath, ws)
	})

	dm.updateIndexFromMemory(taskID, mem)

	return nil
}

func (dm *DataManager) FlushTaskMemory(taskID string) error {
	filePath := filepath.Join(dm.dataDir, taskID+".jsonl")

	dm.mu.Lock()
	ws, ok := dm.writers[taskID]
	dm.mu.Unlock()

	if !ok {
		return nil
	}

	ws.mu.Lock()
	defer ws.mu.Unlock()

	if ws.timer != nil {
		ws.timer.Stop()
		ws.timer = nil
	}

	if err := dm.doFlush(filePath, ws); err != nil {
		return fmt.Errorf("flush task memory failed (taskID=%s): %w", taskID, err)
	}

	dm.updateIndexFromFile(taskID, filePath)

	return nil
}

func (dm *DataManager) FlushAll() error {
	type entry struct {
		taskID   string
		filePath string
		ws       *taskWriterState
	}

	dm.mu.Lock()
	entries := make([]entry, 0, len(dm.writers))
	for taskID, ws := range dm.writers {
		entries = append(entries, entry{
			taskID:   taskID,
			filePath: filepath.Join(dm.dataDir, taskID+".jsonl"),
			ws:       ws,
		})
	}
	dm.mu.Unlock()

	var firstErr error
	for _, e := range entries {
		e.ws.mu.Lock()
		if e.ws.timer != nil {
			e.ws.timer.Stop()
			e.ws.timer = nil
		}
		err := dm.doFlush(e.filePath, e.ws)
		e.ws.mu.Unlock()

		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("flush task %s: %w", e.taskID, err)
			}
			continue
		}
		dm.updateIndexFromFile(e.taskID, e.filePath)
	}
	return firstErr
}

func (dm *DataManager) LoadTaskMemory(taskID string) (*memory.ConversationMemory, error) {
	filePath := filepath.Join(dm.dataDir, taskID+".jsonl")

	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	mem := memory.NewConversationMemory(300)
	loaded := make([]memory.ChatMessage, 0)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) == 0 {
			continue
		}
		var msg memory.ChatMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		loaded = append(loaded, msg)
	}
	mem.Replace(loaded)

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return mem, nil
}

func (dm *DataManager) GetTaskMemoryPath(taskID string) string {
	return filepath.Join(dm.dataDir, taskID+".jsonl")
}

func (dm *DataManager) DeleteTaskMemory(taskID string) error {
	filePath := dm.GetTaskMemoryPath(taskID)

	dm.mu.Lock()
	if ws, ok := dm.writers[taskID]; ok {
		ws.mu.Lock()
		if ws.timer != nil {
			ws.timer.Stop()
		}
		ws.mu.Unlock()
		delete(dm.writers, taskID)
	}
	dm.mu.Unlock()

	return os.Remove(filePath)
}

type TaskHistoryItem struct {
	TaskID       string    `json:"task_id"`
	Title        string    `json:"title"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	MessageCount int       `json:"message_count"`
}

func (dm *DataManager) ListTaskHistory(limit int) ([]TaskHistoryItem, error) {
	entries, err := os.ReadDir(dm.dataDir)
	if err != nil {
		return nil, err
	}

	var items []TaskHistoryItem
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(dm.dataDir, entry.Name())

		f, err := os.Open(path)
		if err != nil {
			continue
		}

		var loaded []memory.ChatMessage
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if len(line) == 0 {
				continue
			}
			var msg memory.ChatMessage
			if err := json.Unmarshal([]byte(line), &msg); err != nil {
				continue
			}
			loaded = append(loaded, msg)
		}
		f.Close()
		if err := scanner.Err(); err != nil {
			continue
		}

		title := ""
		var createdAt time.Time
		for _, m := range loaded {
			if m.Type == memory.MessageTypeHuman {
				title = strings.TrimSpace(m.Content)
				createdAt = m.Timestamp
				break
			}
		}
		if title == "" {
			title = entry.Name()
		}
		if createdAt.IsZero() {
			if info, err := entry.Info(); err == nil {
				createdAt = info.ModTime()
			} else {
				createdAt = time.Now()
			}
		}

		var updatedAt time.Time
		if len(loaded) > 0 {
			updatedAt = loaded[len(loaded)-1].Timestamp
		}
		if updatedAt.IsZero() {
			if info, err := entry.Info(); err == nil {
				updatedAt = info.ModTime()
			} else {
				updatedAt = time.Now()
			}
		}

		if runeCount := len([]rune(title)); runeCount > 30 {
			tr := []rune(title)
			title = string(tr[:30]) + "…"
		}
		nameLen := len(entry.Name())
		if nameLen > 6 {
			taskID := entry.Name()[:nameLen-6]
			items = append(items, TaskHistoryItem{
				TaskID:       taskID,
				Title:        title,
				CreatedAt:    createdAt,
				UpdatedAt:    updatedAt,
				MessageCount: len(loaded),
			})
		}
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})

	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (dm *DataManager) ListTaskHistoryFast(limit int) ([]TaskHistoryItem, error) {
	if err := dm.ensureIndex(); err != nil {
		fmt.Fprintf(os.Stderr, "[history] index unavailable, fallback: %v\n", err)
		return dm.listTaskHistoryFallback(limit)
	}

	dm.scheduleLightReconcile()

	dm.indexMu.RLock()
	defer dm.indexMu.RUnlock()

	items := make([]TaskHistoryItem, 0, len(dm.index.Tasks))
	for _, e := range dm.index.Tasks {
		items = append(items, TaskHistoryItem{
			TaskID:       e.TaskID,
			Title:        e.Title,
			CreatedAt:    e.CreatedAt,
			UpdatedAt:    e.UpdatedAt,
			MessageCount: e.MsgCount,
		})
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})

	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (dm *DataManager) updateIndexFromMemory(taskID string, mem *memory.ConversationMemory) {
	if mem == nil {
		return
	}
	msgs := mem.Snapshot()
	if len(msgs) == 0 {
		return
	}

	var title string
	var createdAt time.Time
	var updatedAt time.Time

	for _, msg := range msgs {
		if msg.Type == memory.MessageTypeHuman && title == "" {
			title = strings.TrimSpace(msg.Content)
			createdAt = msg.Timestamp
		}
		if !msg.Timestamp.IsZero() {
			updatedAt = msg.Timestamp
		}
	}

	if runeCount := len([]rune(title)); runeCount > 30 {
		tr := []rune(title)
		title = string(tr[:30]) + "…"
	}

	msgCount := len(msgs)

	filePath := filepath.Join(dm.dataDir, taskID+".jsonl")
	info, err := os.Stat(filePath)
	var fileSize int64
	var mtime time.Time
	if err == nil {
		fileSize = info.Size()
		mtime = info.ModTime()
	}

	dm.updateIndexEntry(taskID, title, createdAt, updatedAt, msgCount, fileSize, mtime)
}

func (dm *DataManager) updateIndexFromFile(taskID, filePath string) {
	info, err := os.Stat(filePath)
	if err != nil {
		return
	}

	dm.indexMu.Lock()
	defer dm.indexMu.Unlock()

	if !dm.indexLoaded || dm.index == nil {
		if err := dm.loadIndexLocked(); err != nil {
			slog.Warn("Failed to load index for updateIndexFromFile, skipping",
				"taskID", taskID, "error", err)
			return
		}
	}

	entry, exists := dm.index.Tasks[taskID]

	if !exists {
		newEntry, err := dm.extractMetaFromFile(filePath)
		if err != nil {
			return
		}
		dm.index.Tasks[taskID] = newEntry
		dm.schedulePersist()
		return
	}

	entry.FileSize = info.Size()
	entry.Mtime = info.ModTime()
	dm.index.Tasks[taskID] = entry
	dm.schedulePersist()
}

func (dm *DataManager) scheduleLightReconcile() {
	dm.reconcileCheckMu.Lock()
	if time.Since(dm.lastReconcileCheck) < 30*time.Second {
		dm.reconcileCheckMu.Unlock()
		return
	}
	dm.lastReconcileCheck = time.Now()
	dm.reconcileCheckMu.Unlock()

	entries, err := os.ReadDir(dm.dataDir)
	if err != nil {
		return
	}

	jsonlCount := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl") {
			jsonlCount++
		}
	}

	dm.indexMu.RLock()
	indexCount := len(dm.index.Tasks)
	dm.indexMu.RUnlock()

	if jsonlCount != indexCount {
		slog.Info("Index inconsistency detected, scheduling async reconciliation",
			"files_on_disk", jsonlCount,
			"entries_in_index", indexCount)
		dm.grp.Go("datamanager/async-reconcile", func(context.Context) {
			dm.indexMu.RLock()
			idx := dm.index
			dm.indexMu.RUnlock()
			if idx != nil {
				if err := dm.reconcileIndex(idx); err != nil {
					slog.Warn("Async reconciliation failed", "error", err)
				}
			}
		})
	}
}
