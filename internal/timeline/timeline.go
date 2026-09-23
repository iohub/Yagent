// Package timeline 提供任务时间线的 JSONL 追加式持久化记录。
package timeline

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Kind 时间线条目类型
type Kind string

const (
	KindUserInput   Kind = "user_input"
	KindThoughtPlan Kind = "thought_plan"
)

// maxMemEntries 内存中最多保留的条目数
const maxMemEntries = 2000

// Entry 时间线中的一条记录
type Entry struct {
	Seq       int64     `json:"seq"`
	Kind      Kind      `json:"kind"`
	Timestamp time.Time `json:"timestamp"`
	Content   string    `json:"content"`
}

// Recorder 将条目追加写入 JSONL 文件，并在内存中保留最近若干条
type Recorder struct {
	mu            sync.Mutex
	file          *os.File
	path          string
	entries       []Entry
	nextSeq       int64
	maxMemEntries int
}

// newRecorderIn 在 rootDir/taskID 下创建（或打开）JSONL 文件并返回 Recorder（私有，测试注入用）
func newRecorderIn(rootDir, taskID string) (*Recorder, error) {
	if taskID == "" {
		taskID = "session_" + time.Now().Format("20060102_150405")
	}
	dir := filepath.Join(rootDir, taskID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建时间线目录失败: %w", err)
	}
	// 文件名冲突时追加 -1、-2 后缀直到不冲突
	filename := fmt.Sprintf("%d_%s.jsonl", time.Now().Unix(), taskID)
	filePath := filepath.Join(dir, filename)
	for suffix := 1; ; suffix++ {
		if _, err := os.Stat(filePath); err != nil {
			break // 文件不存在，使用当前文件名
		}
		filename = fmt.Sprintf("%d_%s-%d.jsonl", time.Now().Unix(), taskID, suffix)
		filePath = filepath.Join(dir, filename)
	}
	f, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("打开时间线文件失败: %w", err)
	}
	return &Recorder{file: f, path: filePath, nextSeq: 1, maxMemEntries: maxMemEntries}, nil
}

// NewRecorder 使用默认根目录 ~/.codeactor/data/timeline 创建 Recorder
func NewRecorder(taskID string) (*Recorder, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil || homeDir == "" {
		homeDir = os.TempDir()
	}
	rootDir := filepath.Join(homeDir, ".codeactor", "data", "timeline")
	return newRecorderIn(rootDir, taskID)
}

// Record 追加一条记录：先落盘，成功后再入内存
func (r *Recorder) Record(kind Kind, content string) (Entry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return Entry{}, fmt.Errorf("timeline recorder 已关闭")
	}
	entry := Entry{Seq: r.nextSeq, Kind: kind, Timestamp: time.Now(), Content: content}
	data, err := json.Marshal(entry)
	if err != nil {
		return Entry{}, fmt.Errorf("序列化时间线条目失败: %w", err)
	}
	if _, err := r.file.Write(append(data, '\n')); err != nil {
		return Entry{}, fmt.Errorf("写入时间线文件失败: %w", err)
	}
	r.nextSeq++
	r.entries = append(r.entries, entry)
	if len(r.entries) > r.maxMemEntries {
		// 丢弃最旧的条目：切片整体前移，保持顺序
		copy(r.entries, r.entries[1:])
		r.entries[len(r.entries)-1] = Entry{}
		r.entries = r.entries[:len(r.entries)-1]
	}
	return entry, nil
}

// Snapshot 返回内存中条目的拷贝
func (r *Recorder) Snapshot() []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Entry, len(r.entries))
	copy(out, r.entries)
	return out
}

// Path 返回 JSONL 文件路径
func (r *Recorder) Path() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.path
}

// Close 关闭底层文件；幂等，可重复调用
func (r *Recorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}

// LoadEntries 从 JSONL 文件加载全部条目，解析失败的行跳过
func LoadEntries(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开时间线文件失败: %w", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	entries := make([]Entry, 0)
	for scanner.Scan() {
		var e Entry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue // 跳过解析失败的行
		}
		entries = append(entries, e)
	}
	if err := scanner.Err(); err != nil {
		return entries, fmt.Errorf("读取时间线文件失败: %w", err)
	}
	return entries, nil
}
