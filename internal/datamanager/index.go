package datamanager

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	indexVersion  = 1
	indexFileName = "index.json"
	maxScanLines  = 50 // 提取元数据时最多扫描的行数
)

// IndexEntry 是单个任务的轻量元数据
type IndexEntry struct {
	TaskID   string    `json:"task_id"`
	Title    string    `json:"title"`     // 首条人类消息前30字符
	CreatedAt time.Time `json:"created_at"` // 首条消息时间
	UpdatedAt  time.Time `json:"updated_at"` // 末条消息时间
	MsgCount  int       `json:"msg_count"`  // 消息总数
	FileSize  int64     `json:"file_size"`  // JSONL 文件大小
	Mtime     time.Time `json:"mtime"`      // 文件修改时间，用于一致性校验
}

type taskIndex struct {
	Version int                    `json:"version"`
	Tasks   map[string]IndexEntry `json:"tasks"`
}

// indexPath 返回索引文件路径
func (dm *DataManager) indexPath() string {
	// 索引文件放在 .codeactor 目录（dataDir 的父目录）
	return filepath.Join(filepath.Dir(dm.dataDir), indexFileName)
}

// ensureIndex 确保索引已加载（懒加载，double-check locking）
func (dm *DataManager) ensureIndex() error {
	// 第一次快速检查（只读锁）
	dm.indexMu.RLock()
	if dm.indexLoaded {
		dm.indexMu.RUnlock()
		return nil
	}
	dm.indexMu.RUnlock()

	// 升级为写锁，二次检查并加载
	dm.indexMu.Lock()
	defer dm.indexMu.Unlock()

	if dm.indexLoaded {
		return nil
	}

	return dm.loadIndexLocked()
}

// loadIndexLocked 在调用者已持有 indexMu 写锁的情况下加载索引。
// 必须在 indexMu 锁内调用，不会尝试获取锁。
func (dm *DataManager) loadIndexLocked() error {
	if dm.indexLoaded && dm.index != nil {
		return nil
	}

	idx, err := dm.loadIndexFromDisk()
	if err != nil {
		slog.Warn("Failed to load index from disk, rebuilding", "error", err)
		idx, err = dm.rebuildIndex()
		if err != nil {
			return fmt.Errorf("rebuild index: %w", err)
		}
	}

	dm.index = idx
	dm.indexLoaded = true
	return nil
}

// loadIndexFromDisk 从磁盘读取并反序列化索引，做基本校验
func (dm *DataManager) loadIndexFromDisk() (*taskIndex, error) {
	path := dm.indexPath()

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read index file: %w", err)
	}

	var idx taskIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("parse index file: %w", err)
	}

	if idx.Version != indexVersion {
		return nil, fmt.Errorf("unsupported index version: %d (expected %d)", idx.Version, indexVersion)
	}

	if idx.Tasks == nil {
		return nil, fmt.Errorf("index tasks is nil")
	}

	return &idx, nil
}

// rebuildIndex 当索引不存在或损坏时，从所有 .jsonl 文件重建索引
// 关键优化：只读取每个文件前 N 行来提取元数据，不全量扫描
func (dm *DataManager) rebuildIndex() (*taskIndex, error) {
	idx := &taskIndex{
		Version: indexVersion,
		Tasks:   make(map[string]IndexEntry),
	}

	entries, err := os.ReadDir(dm.dataDir)
	if err != nil {
		return nil, fmt.Errorf("read task directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}

		path := filepath.Join(dm.dataDir, entry.Name())
		entry, err := dm.extractMetaFromFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[history] warning: failed to extract meta from %s: %v\n", path, err)
			continue
		}
		idx.Tasks[entry.TaskID] = entry
	}

	// 尝试原子写入重建的索引
	if err := dm.persistIndex(idx); err != nil {
		fmt.Fprintf(os.Stderr, "[history] warning: failed to persist rebuilt index: %v\n", err)
	}

	return idx, nil
}

// extractMetaFromFile 从单个 .jsonl 文件中提取元数据
// 只读取前 50 行来获取 title/createdAt/updatedAt
// 使用轻量字段解析（类似 ListTaskHistoryFast 的 fieldOnly 技巧）
func (dm *DataManager) extractMetaFromFile(path string) (IndexEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return IndexEntry{}, err
	}
	defer f.Close()

	// 获取文件信息
	info, err := os.Stat(path)
	if err != nil {
		return IndexEntry{}, err
	}

	// 从文件名提取 taskID
	baseName := filepath.Base(path)
	taskID := strings.TrimSuffix(baseName, ".jsonl")

	var (
		fieldOnly struct {
			Type    string `json:"type"`
			Content string `json:"content"`
			TS      string `json:"timestamp"`
		}
		title       string
		createdAt   time.Time
		updatedAt   time.Time
		lineCount   int
		foundHuman  bool
	)

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if len(line) == 0 {
			continue
		}
		lineCount++

		// 快速跳过没有 type 字段的行
		if !strings.Contains(line, `"type"`) {
			continue
		}

		if err := json.Unmarshal([]byte(line), &fieldOnly); err != nil {
			continue
		}

		// 追踪第一条人类消息
		if !foundHuman && fieldOnly.Type == "human" {
			title = strings.TrimSpace(fieldOnly.Content)
			if ts, err := time.Parse(time.RFC3339, fieldOnly.TS); err == nil {
				createdAt = ts
			}
			foundHuman = true
		}

		// 追踪最后一条消息的时间
		if fieldOnly.TS != "" {
			if ts, err := time.Parse(time.RFC3339, fieldOnly.TS); err == nil {
				updatedAt = ts
			}
		}

		// 只扫描前 N 行
		if lineCount >= maxScanLines {
			// 继续扫描但不解析，只计数
			for scanner.Scan() {
				lineCount++
				if len(scanner.Text()) == 0 {
					continue
				}
				// 检查是否包含 timestamp
				if strings.Contains(scanner.Text(), `"timestamp"`) {
					if err := json.Unmarshal([]byte(scanner.Text()), &fieldOnly); err == nil {
						if fieldOnly.TS != "" {
							if ts, err := time.Parse(time.RFC3339, fieldOnly.TS); err == nil {
								updatedAt = ts
							}
						}
					}
				}
			}
			break
		}
	}

	if err := scanner.Err(); err != nil {
		// 不返回错误，仍然返回已收集的数据
	}

	// 如果没有找到人类消息，使用文件名作为标题
	if !foundHuman || title == "" {
		title = taskID
	}

	// CreatedAt fallback
	if createdAt.IsZero() {
		createdAt = info.ModTime()
	}

	// UpdatedAt fallback
	if updatedAt.IsZero() {
		updatedAt = info.ModTime()
	}

	// 标题截断到 30 字符
	if runeCount := len([]rune(title)); runeCount > 30 {
		tr := []rune(title)
		title = string(tr[:30]) + "…"
	}

	return IndexEntry{
		TaskID:   taskID,
		Title:    title,
		CreatedAt: createdAt,
		UpdatedAt:  updatedAt,
		MsgCount:  lineCount,
		FileSize:  info.Size(),
		Mtime:     info.ModTime(),
	}, nil
}

// persistIndex 原子写索引文件（先写 .tmp 再 rename）
func (dm *DataManager) persistIndex(idx *taskIndex) error {
	path := dm.indexPath()
	tmpPath := path + ".tmp"

	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal index: %w", err)
	}

	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("write temp index: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		// rename 失败时清理临时文件
		os.Remove(tmpPath)
		return fmt.Errorf("rename index: %w", err)
	}

	return nil
}

// reconcileIndex 轻量校验：检查每个任务的 mtime 是否匹配，不匹配则从文件重新提取
// 这是一个增量校验，只处理发生过变动的任务
func (dm *DataManager) reconcileIndex(idx *taskIndex) error {
	if idx == nil {
		return nil
	}

	// 第一步：收集磁盘上所有 .jsonl 文件
	entries, err := os.ReadDir(dm.dataDir)
	if err != nil {
		return fmt.Errorf("reconcileIndex: read data dir: %w", err)
	}

	diskFiles := make(map[string]bool)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		taskID := strings.TrimSuffix(entry.Name(), ".jsonl")
		diskFiles[taskID] = true
	}

	// 第二步：处理索引中已有的条目 — 检查文件是否存在/变更
	updatedCount := 0
	deletedCount := 0
	for taskID, entry := range idx.Tasks {
		path := filepath.Join(dm.dataDir, taskID+".jsonl")
		info, err := os.Stat(path)
		if err != nil {
			// 文件已删除，从索引中移除
			delete(idx.Tasks, taskID)
			deletedCount++
			continue
		}

		// 检查 mtime 是否匹配
		if !info.ModTime().Equal(entry.Mtime) || info.Size() != entry.FileSize {
			// 文件已变更，重新提取元数据
			newEntry, err := dm.extractMetaFromFile(path)
			if err != nil {
				continue
			}
			idx.Tasks[taskID] = newEntry
			updatedCount++
		}
	}

	// 第三步：添加磁盘上有但索引中缺失的条目
	addedCount := 0
	for taskID := range diskFiles {
		if _, exists := idx.Tasks[taskID]; !exists {
			path := filepath.Join(dm.dataDir, taskID+".jsonl")
			newEntry, err := dm.extractMetaFromFile(path)
			if err != nil {
				slog.Warn("reconcileIndex: failed to extract meta for missing entry",
					"taskID", taskID, "error", err)
				continue
			}
			idx.Tasks[taskID] = newEntry
			addedCount++
		}
	}

	if updatedCount > 0 || deletedCount > 0 || addedCount > 0 {
		slog.Info("Index reconciliation completed",
			"added", addedCount,
			"updated", updatedCount,
			"deleted", deletedCount,
			"total", len(idx.Tasks))
		// 持久化修复后的索引
		if err := dm.persistIndex(idx); err != nil {
			slog.Warn("Failed to persist reconciled index", "error", err)
		}
	}

	return nil
}

// updateIndexEntry 更新单个任务的索引条目（线程安全）
// 由 SaveTaskMemory/FlushTaskMemory 等写入路径回调
func (dm *DataManager) updateIndexEntry(taskID string, title string, createdAt, updatedAt time.Time, msgCount int, fileSize int64, mtime time.Time) {
	dm.indexMu.Lock()
	defer dm.indexMu.Unlock()

	// 修复：索引未加载时，在锁内加载而不是静默丢弃
	if !dm.indexLoaded || dm.index == nil {
		if err := dm.loadIndexLocked(); err != nil {
			slog.Warn("Failed to load index for updateIndexEntry, skipping",
				"taskID", taskID, "error", err)
			return
		}
	}

	entry, exists := dm.index.Tasks[taskID]
	if exists {
		// 只更新可能变化的字段
		entry.Title = title
		entry.MsgCount = msgCount
		entry.FileSize = fileSize
		entry.Mtime = mtime
		// 注意：UpdatedAt 可能已经在 extractMetaFromFile 中获取到正确的时间
		entry.UpdatedAt = updatedAt
	} else {
		// 新增条目
		entry = IndexEntry{
			TaskID:   taskID,
			Title:    title,
			CreatedAt: createdAt,
			UpdatedAt:  updatedAt,
			MsgCount:  msgCount,
			FileSize:  fileSize,
			Mtime:     mtime,
		}
		dm.index.Tasks[taskID] = entry
	}

	// 触发异步持久化
	dm.schedulePersist()
}

// indexPersistLoop 防抖持久化循环（每 2 秒检查一次是否有未持久化的更新）
func (dm *DataManager) indexPersistLoop() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// 检查是否有未持久化的更新
			select {
			case <-dm.persistCh:
				dm.indexMu.RLock()
				idx := dm.index
				loaded := dm.indexLoaded
				dm.indexMu.RUnlock()

				if loaded && idx != nil {
					if err := dm.persistIndex(idx); err != nil {
						fmt.Fprintf(os.Stderr, "[history] persist index failed: %v\n", err)
					}
				}
			default:
				// 没有未持久化的更新，跳过
			}

		case <-dm.stopCh:
			// 最终刷新一次
			dm.indexMu.RLock()
			idx := dm.index
			loaded := dm.indexLoaded
			dm.indexMu.RUnlock()

			if loaded && idx != nil {
				// 清空 pending persist
			drainLoop:
				for {
					select {
					case <-dm.persistCh:
					default:
						break drainLoop
					}
				}
				if err := dm.persistIndex(idx); err != nil {
					fmt.Fprintf(os.Stderr, "[history] final persist index failed: %v\n", err)
				}
			}
			return
		}
	}
}

// schedulePersist 触发一次异步持久化（非阻塞）
func (dm *DataManager) schedulePersist() {
	select {
	case dm.persistCh <- struct{}{}:
	default:
		// 通道已满，已经有待处理的持久化请求
	}
}

// Stop 关闭 DataManager，停止 persistLoop
func (dm *DataManager) Stop() {
	select {
	case <-dm.stopCh:
		// 已经关闭
	default:
		close(dm.stopCh)
	}
}

// listTaskHistoryFallback 当索引不可用时的降级路径（复用原来的全量扫描逻辑）
func (dm *DataManager) listTaskHistoryFallback(limit int) ([]TaskHistoryItem, error) {
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
			Type    string `json:"type"`
			Content string `json:"content"`
			TS      string `json:"timestamp"`
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
				if ts, err := time.Parse(time.RFC3339, fieldOnly.TS); err == nil {
					createdAt = ts
				}
				foundHuman = true
			}

			// 追踪最后一条消息的时间
			if fieldOnly.TS != "" {
				if ts, err := time.Parse(time.RFC3339, fieldOnly.TS); err == nil {
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
