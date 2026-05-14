package datamanager

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"codeactor/internal/memory"
)

const (
	DataDirName = ".codeactor" // 隐藏数据目录名称
)

// taskWriterState 管理单个任务的写入状态
type taskWriterState struct {
	mu               sync.Mutex
	pending          []memory.ChatMessage
	timer            *time.Timer
	lastFlushedCount int
	needsFullRewrite bool // 当消息被截断时设为 true
}

// DataManager 负责管理在home目录下的隐藏数据目录
type DataManager struct {
	dataDir string
	mu      sync.Mutex
	writers map[string]*taskWriterState
}

// NewDataManager 创建新的数据管理器
func NewDataManager() (*DataManager, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	dataDir := filepath.Join(homeDir, DataDirName, "tasks")

	// 创建隐藏数据目录（如果不存在）
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, err
	}

	return &DataManager{
		dataDir: dataDir,
		writers: make(map[string]*taskWriterState),
	}, nil
}

// getOrCreateWriter 获取或创建任务的写入状态
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

// flushWriter 内部方法：刷新单个任务的待写入消息
func (dm *DataManager) flushWriter(taskID, filePath string, ws *taskWriterState) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	dm.doFlush(filePath, ws)
}

// doFlush 执行实际的写入操作（调用者需持有 ws.mu 锁）
func (dm *DataManager) doFlush(filePath string, ws *taskWriterState) error {
	if ws.needsFullRewrite {
		// 全量覆写：打开文件（truncate），写入所有 pending
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
		// 增量追加
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

// SaveTaskMemory 保存任务的memory到文件（增量追加 + 5秒防抖）
func (dm *DataManager) SaveTaskMemory(taskID string, mem *memory.ConversationMemory) error {
	filePath := filepath.Join(dm.dataDir, taskID+".jsonl")

	ws := dm.getOrCreateWriter(taskID, filePath)
	ws.mu.Lock()
	defer ws.mu.Unlock()

	currentCount := len(mem.Messages)

	if currentCount < ws.lastFlushedCount {
		// 消息被截断或重置，需要全量覆写
		ws.needsFullRewrite = true
		ws.pending = ws.pending[:0] // 清空旧 pending（磁盘将被截断）
		ws.lastFlushedCount = 0     // 重置，让下面逻辑重新收集所有消息
	}

	// 提取新消息加入 pending
	newStart := ws.lastFlushedCount
	if newStart < 0 {
		newStart = 0
	}
	if currentCount > newStart {
		for i := newStart; i < currentCount; i++ {
			ws.pending = append(ws.pending, mem.Messages[i])
		}
	}
	ws.lastFlushedCount = currentCount

	// 重置 5s 定时器
	if ws.timer != nil {
		ws.timer.Stop()
	}
	ws.timer = time.AfterFunc(5*time.Second, func() {
		dm.flushWriter(taskID, filePath, ws)
	})

	return nil
}

// FlushTaskMemory 立即刷新指定任务的待写入消息到磁盘
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

	// 停止定时器
	if ws.timer != nil {
		ws.timer.Stop()
		ws.timer = nil
	}

	return dm.doFlush(filePath, ws)
}

// FlushAll 刷新所有任务的待写入消息
func (dm *DataManager) FlushAll() error {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	for taskID, ws := range dm.writers {
		ws.mu.Lock()
		if ws.timer != nil {
			ws.timer.Stop()
			ws.timer = nil
		}
		filePath := filepath.Join(dm.dataDir, taskID+".jsonl")
		if err := dm.doFlush(filePath, ws); err != nil {
			ws.mu.Unlock()
			return err
		}
		ws.mu.Unlock()
	}
	return nil
}

// LoadTaskMemory 从文件加载任务的memory
func (dm *DataManager) LoadTaskMemory(taskID string) (*memory.ConversationMemory, error) {
	filePath := filepath.Join(dm.dataDir, taskID+".jsonl")

	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	mem := memory.NewConversationMemory(300)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024) // 支持大行
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) == 0 {
			continue
		}
		var msg memory.ChatMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			// 跳过无法解析的行（健壮性）
			continue
		}
		mem.Messages = append(mem.Messages, msg)
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return mem, nil
}

// GetTaskMemoryPath 获取任务memory文件的路径
func (dm *DataManager) GetTaskMemoryPath(taskID string) string {
	return filepath.Join(dm.dataDir, taskID+".jsonl")
}

// DeleteTaskMemory 删除任务的memory文件
func (dm *DataManager) DeleteTaskMemory(taskID string) error {
	filePath := filepath.Join(dm.dataDir, taskID+".jsonl")

	// 清理 writer 状态
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

// ListTaskMemories 列出所有保存的任务memory文件
func (dm *DataManager) ListTaskMemories() ([]string, error) {
	files, err := os.ReadDir(dm.dataDir)
	if err != nil {
		return nil, err
	}

	var taskIDs []string
	for _, file := range files {
		if !file.IsDir() && strings.HasSuffix(file.Name(), ".jsonl") {
			taskIDs = append(taskIDs, file.Name()[:len(file.Name())-6]) // 去掉.jsonl后缀
		}
	}

	return taskIDs, nil
}

// TaskHistoryItem 用于TUI展示的历史任务信息
type TaskHistoryItem struct {
	TaskID       string    `json:"task_id"`
	Title        string    `json:"title"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	MessageCount int       `json:"message_count"`
}

// ListTaskHistory 返回最近的历史任务（按时间倒序），包含任务ID、标题（首条用户消息）与时间。
// limit<=0 时返回全部。
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

		// 逐行解析 JSONL 文件
		f, err := os.Open(path)
		if err != nil {
			continue
		}

		var mem memory.ConversationMemory
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
			mem.Messages = append(mem.Messages, msg)
		}
		f.Close()
		if err := scanner.Err(); err != nil {
			continue
		}

		// 提取首条用户消息作为标题
		title := ""
		var createdAt time.Time
		for _, m := range mem.Messages {
			if m.Type == memory.MessageTypeHuman {
				title = strings.TrimSpace(m.Content)
				createdAt = m.Timestamp
				break
			}
		}
		if title == "" {
			// fallback: 文件名
			title = entry.Name()
		}
		if createdAt.IsZero() {
			if info, err := entry.Info(); err == nil {
				createdAt = info.ModTime()
			} else {
				createdAt = time.Now()
			}
		}

		// UpdatedAt: last message timestamp or file mod time
		var updatedAt time.Time
		if len(mem.Messages) > 0 {
			updatedAt = mem.Messages[len(mem.Messages)-1].Timestamp
		}
		if updatedAt.IsZero() {
			if info, err := entry.Info(); err == nil {
				updatedAt = info.ModTime()
			} else {
				updatedAt = time.Now()
			}
		}

		// 标题只展示用户第一条消息的前30个字符
		if runeCount := len([]rune(title)); runeCount > 30 {
			tr := []rune(title)
			title = string(tr[:30]) + "…"
		}
		// 任务ID为文件名去后缀
		nameLen := len(entry.Name())
		if nameLen > 6 {
			taskID := entry.Name()[:nameLen-6]
			items = append(items, TaskHistoryItem{
				TaskID:       taskID,
				Title:        title,
				CreatedAt:    createdAt,
				UpdatedAt:    updatedAt,
				MessageCount: len(mem.Messages),
			})
		}
	}

	// 按时间倒序
	sort.Slice(items, func(i, j int) bool {
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})

	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

// ListTaskHistoryFast 快速返回历史任务列表，只读取必要的行。
// 相比 ListTaskHistory 性能更好，因为它：
// 1. 只解析第一条人类消息作为标题
// 2. 用行计数代替全量 JSON 解析来计算消息数
func (dm *DataManager) ListTaskHistoryFast(limit int) ([]TaskHistoryItem, error) {
	entries, err := os.ReadDir(dm.dataDir)
	if err != nil {
		return nil, err
	}

	// 添加调试日志（输出到 stderr，不影响正常运行）
	if len(entries) == 0 {
		fmt.Fprintf(os.Stderr, "[history] no entries in task directory: %s\n", dm.dataDir)
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

		var (
			title       string
			createdAt   time.Time
			updatedAt   time.Time
			lineCount   int
			foundHuman  bool
		)
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)

		// 轻量结构体，只解析需要的字段
		var fieldOnly struct {
			Type      string `json:"type"`
			Content   string `json:"content"`
			Timestamp string `json:"timestamp"`
		}

		for scanner.Scan() {
			line := scanner.Text()
			if len(line) == 0 {
				continue
			}
			lineCount++

			// 只解析有 type 字段的行（减少无效解析）
			if !strings.Contains(line, `"type"`) {
				continue
			}

			if err := json.Unmarshal([]byte(line), &fieldOnly); err != nil {
				continue
			}

			// 追踪第一条人类消息
			if !foundHuman && fieldOnly.Type == "human" {
				title = strings.TrimSpace(fieldOnly.Content)
				if ts, err := time.Parse(time.RFC3339, fieldOnly.Timestamp); err == nil {
					createdAt = ts
				}
				foundHuman = true
			}

			// 追踪最后一条消息的时间
			if fieldOnly.Timestamp != "" {
				if ts, err := time.Parse(time.RFC3339, fieldOnly.Timestamp); err == nil {
					updatedAt = ts
				}
			}
		}
		f.Close()
		if err := scanner.Err(); err != nil {
			continue
		}

		// 如果没有找到人类消息，使用文件名作为标题
		if !foundHuman || title == "" {
			title = entry.Name()
		}

		// CreatedAt fallback
		if createdAt.IsZero() {
			if info, err := entry.Info(); err == nil {
				createdAt = info.ModTime()
			} else {
				createdAt = time.Now()
			}
		}

		// UpdatedAt fallback
		if updatedAt.IsZero() {
			if info, err := entry.Info(); err == nil {
				updatedAt = info.ModTime()
			} else {
				updatedAt = time.Now()
			}
		}

		// 标题截断到 30 字符
		if runeCount := len([]rune(title)); runeCount > 30 {
			tr := []rune(title)
			title = string(tr[:30]) + "…"
		}

		// 任务ID为文件名去后缀
		nameLen := len(entry.Name())
		if nameLen > 6 {
			taskID := entry.Name()[:nameLen-6]
			items = append(items, TaskHistoryItem{
				TaskID:       taskID,
				Title:        title,
				CreatedAt:    createdAt,
				UpdatedAt:    updatedAt,
				MessageCount: lineCount,
			})
		}
	}

	// 按时间倒序
	sort.Slice(items, func(i, j int) bool {
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})

	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

// GetDataDir 获取数据目录路径
func (dm *DataManager) GetDataDir() string {
	return dm.dataDir
}
