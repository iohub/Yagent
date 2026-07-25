package memory

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// MemoryJSONLConfig JSONL写入器配置
type MemoryJSONLConfig struct {
	Enable    bool   `toml:"enable"`
	OutputDir string `toml:"output_dir"`
}

// MemoryRecord JSONL单条记录
type MemoryRecord struct {
	Timestamp  time.Time       `json:"timestamp"`
	AgentName  string          `json:"agent_name"`
	MessageIdx int             `json:"message_idx"`
	Message    json.RawMessage `json:"message"`
}

// JSONLWriter 内存记录JSONL实时写入器
type JSONLWriter struct {
	mu        sync.Mutex
	file      *os.File
	encoder   *json.Encoder
	enabled   bool
	closed    bool
	agentName string
	task      string
	taskHash  string
	msgIdx    int
	filePath  string
}

// jsonlWriterCtxKey 私有context key类型
type jsonlWriterCtxKey struct{}

// WithJSONLWriter 将JSONLWriter注入到context中
func WithJSONLWriter(ctx context.Context, w *JSONLWriter) context.Context {
	if w == nil || !w.Enabled() {
		return ctx
	}
	return context.WithValue(ctx, jsonlWriterCtxKey{}, w)
}

// GetJSONLWriter 从context中取出JSONLWriter，不存在则返回nil
func GetJSONLWriter(ctx context.Context) *JSONLWriter {
	if w, ok := ctx.Value(jsonlWriterCtxKey{}).(*JSONLWriter); ok {
		return w
	}
	return nil
}

// hashTask 计算任务描述的MD5哈希前8位(hex编码)
func hashTask(task string) string {
	hash := md5.Sum([]byte(task))
	return hex.EncodeToString(hash[:])[:8]
}

// NewJSONLWriter 创建JSONL写入器
func NewJSONLWriter(cfg MemoryJSONLConfig, projectID, agentName, task string) (*JSONLWriter, error) {
	// 如果未启用，返回disabled writer
	if !cfg.Enable {
		return &JSONLWriter{
			enabled:   false,
			agentName: agentName,
			task:      task,
			taskHash:  hashTask(task),
		}, nil
	}

	// 确定输出目录
	outputDir := cfg.OutputDir
	if outputDir == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			slog.Warn("JSONLWriter: failed to get user home directory, using current directory",
				"error", err,
			)
			outputDir = filepath.Join(".", "memory_jsonl", projectID)
		} else {
			outputDir = filepath.Join(homeDir, ".codeactor", "data", "memory_jsonl", projectID)
		}
	}

	// 创建目录
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		slog.Warn("JSONLWriter: failed to create output directory",
			"path", outputDir,
			"error", err,
		)
		return &JSONLWriter{
			enabled:   false,
			agentName: agentName,
			task:      task,
			taskHash:  hashTask(task),
		}, fmt.Errorf("failed to create output directory: %w", err)
	}

	// 生成文件名: {YYYYMMDD_HHMMSS}_{agent_name}_{task_hash_8}.jsonl
	timestamp := time.Now().Format("20060102_150405")
	filename := fmt.Sprintf("%s_%s_%s.jsonl", timestamp, agentName, hashTask(task))
	filePath := filepath.Join(outputDir, filename)

	// 打开文件
	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		slog.Warn("JSONLWriter: failed to open output file",
			"path", filePath,
			"error", err,
		)
		return &JSONLWriter{
			enabled:   false,
			agentName: agentName,
			task:      task,
			taskHash:  hashTask(task),
		}, fmt.Errorf("failed to open output file: %w", err)
	}

	return &JSONLWriter{
		file:      file,
		encoder:   json.NewEncoder(file),
		enabled:   true,
		agentName: agentName,
		task:      task,
		taskHash:  hashTask(task),
		filePath:  filePath,
	}, nil
}

// WriteMessage 写入一条消息到JSONL文件
func (w *JSONLWriter) WriteMessage(msg interface{}) error {
	if !w.enabled || w.file == nil {
		return nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	// 序列化为JSON
	msgBytes, err := json.Marshal(msg)
	if err != nil {
		slog.Warn("JSONLWriter: failed to marshal message",
			"error", err,
		)
		return nil
	}

	// 构造记录
	record := MemoryRecord{
		Timestamp:  time.Now(),
		AgentName:  w.agentName,
		MessageIdx: w.msgIdx,
		Message:    msgBytes,
	}

	w.msgIdx++

	// 写入一行
	if err := w.encoder.Encode(record); err != nil {
		slog.Warn("JSONLWriter: failed to encode record",
			"error", err,
		)
		return nil
	}

	return nil
}

// Close 关闭文件
func (w *JSONLWriter) Close() error {
	if w.file == nil || w.closed {
		return nil
	}
	w.closed = true
	return w.file.Close()
}

// FilePath 返回文件路径
func (w *JSONLWriter) FilePath() string {
	return w.filePath
}

// Enabled 返回是否启用
func (w *JSONLWriter) Enabled() bool {
	return w.enabled
}
