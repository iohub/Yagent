package assistant

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"codeactor/internal/memory"
)

const (
	DataDirName = ".codeactor" // 隐藏数据目录名称
)

// DataManager 负责管理在home目录下的隐藏数据目录
type DataManager struct {
	dataDir string
}

// NewDataManager 创建新的数据管理器
func NewDataManager() (*DataManager, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	dataDir := filepath.Join(homeDir, DataDirName)

	// 创建隐藏数据目录（如果不存在）
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, err
	}

	return &DataManager{
		dataDir: dataDir,
	}, nil
}

// SaveTaskMemory 保存任务的memory到文件
func (dm *DataManager) SaveTaskMemory(taskID string, mem *memory.ConversationMemory) error {
	filePath := filepath.Join(dm.dataDir, taskID+".json")

	memoryData, err := json.MarshalIndent(mem, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(filePath, memoryData, 0644)
}

// LoadTaskMemory 从文件加载任务的memory
func (dm *DataManager) LoadTaskMemory(taskID string) (*memory.ConversationMemory, error) {
	filePath := filepath.Join(dm.dataDir, taskID+".json")

	memoryData, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	var mem memory.ConversationMemory
	if err := json.Unmarshal(memoryData, &mem); err != nil {
		return nil, err
	}

	return &mem, nil
}

// GetTaskMemoryPath 获取任务memory文件的路径
func (dm *DataManager) GetTaskMemoryPath(taskID string) string {
	return filepath.Join(dm.dataDir, taskID+".json")
}

// DeleteTaskMemory 删除任务的memory文件
func (dm *DataManager) DeleteTaskMemory(taskID string) error {
	filePath := filepath.Join(dm.dataDir, taskID+".json")
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
		if !file.IsDir() && filepath.Ext(file.Name()) == ".json" {
			taskIDs = append(taskIDs, file.Name()[:len(file.Name())-5]) // 去掉.json后缀
		}
	}

	return taskIDs, nil
}

// TaskHistoryItem 用于TUI展示的历史任务信息
type TaskHistoryItem struct {
	TaskID    string    `json:"task_id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
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
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dm.dataDir, entry.Name())
		// 读取文件
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var mem memory.ConversationMemory
		if err := json.Unmarshal(raw, &mem); err != nil {
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
		// 截断标题以适配TUI
		if runeCount := len([]rune(title)); runeCount > 120 {
			// 简单截断避免打断多字节
			tr := []rune(title)
			title = string(tr[:120]) + "…"
		}
		// 任务ID为文件名去后缀
		taskID := entry.Name()[:len(entry.Name())-5]
		items = append(items, TaskHistoryItem{
			TaskID:    taskID,
			Title:     title,
			CreatedAt: createdAt,
		})
	}

	// 按时间倒序
	sort.Slice(items, func(i, j int) bool {
		return items[i].CreatedAt.After(items[j].CreatedAt)
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
