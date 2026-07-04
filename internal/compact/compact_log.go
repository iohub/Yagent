package compact

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ──────────────────────────────────────────────
// LayerResult 单层压缩结果
// ──────────────────────────────────────────────
type LayerResult struct {
	LayerName    string         `json:"layer_name"`
	LayerIndex   int            `json:"layer_index"`
	TokensBefore int            `json:"tokens_before"`
	TokensAfter  int            `json:"tokens_after"`
	TokensSaved  int            `json:"tokens_saved"`
	DurationMs   int64          `json:"duration_ms"`
	Details      map[string]any `json:"details,omitempty"`
}

// ──────────────────────────────────────────────
// CompactSession 一次完整的压缩操作记录
// ──────────────────────────────────────────────
type CompactSession struct {
	StartTime      time.Time      `json:"start_time"`
	EndTime        time.Time      `json:"end_time"`
	DurationMs     int64          `json:"duration_ms"`
	OriginalTokens int            `json:"original_tokens"`
	FinalTokens    int            `json:"final_tokens"`
	TotalSaved     int            `json:"total_saved"`
	TotalRatio     float64        `json:"total_ratio"`
	LayersApplied  []string       `json:"layers_applied"`
	LayerResults   []LayerResult  `json:"layer_results"`
	Error          string         `json:"error,omitempty"`
}

// NewCompactSession 创建新的压缩会话记录
func NewCompactSession(originalTokens int) *CompactSession {
	return &CompactSession{
		StartTime:      time.Now(),
		OriginalTokens: originalTokens,
		FinalTokens:    originalTokens,
		LayersApplied:  make([]string, 0),
		LayerResults:   make([]LayerResult, 0),
	}
}

// AddLayerResult 添加一层压缩结果（单 goroutine 调用，无需锁）
func (s *CompactSession) AddLayerResult(name string, index int, tokensBefore int, tokensAfter int, durationMs int64, details map[string]any) {
	saved := tokensBefore - tokensAfter
	if saved < 0 {
		saved = 0 // 某些层（如 compensate）可能增加 token，不计为节省
	}
	result := LayerResult{
		LayerName:    name,
		LayerIndex:   index,
		TokensBefore: tokensBefore,
		TokensAfter:  tokensAfter,
		TokensSaved:  saved,
		DurationMs:   durationMs,
		Details:      details,
	}
	s.LayerResults = append(s.LayerResults, result)
	s.LayersApplied = append(s.LayersApplied, name)
	s.FinalTokens = tokensAfter
}

// Finalize 完成会话统计
func (s *CompactSession) Finalize() {
	s.EndTime = time.Now()
	s.DurationMs = s.EndTime.Sub(s.StartTime).Milliseconds()
	s.TotalSaved = s.OriginalTokens - s.FinalTokens
	if s.OriginalTokens > 0 {
		s.TotalRatio = float64(s.TotalSaved) / float64(s.OriginalTokens)
	}
}

// FinalizeWithError 记录错误的会话
func (s *CompactSession) FinalizeWithError(err error) {
	s.Finalize()
	if err != nil {
		s.Error = err.Error()
	}
}

// ──────────────────────────────────────────────
// CompactLogWriter 日志写入接口（解耦，便于测试）
// ──────────────────────────────────────────────
type CompactLogWriter interface {
	LogSession(session *CompactSession)
	Close() error
}

// NoOpCompactLogWriter 空实现
type NoOpCompactLogWriter struct{}

func (n *NoOpCompactLogWriter) LogSession(_ *CompactSession) {}
func (n *NoOpCompactLogWriter) Close() error                 { return nil }

// ──────────────────────────────────────────────
// CompactLogger 文件日志实现（JSON Lines 格式）
// ──────────────────────────────────────────────
type CompactLogger struct {
	mu     sync.Mutex
	logDir string
	file   *os.File
	date   string // 当前文件对应的日期
}

// NewCompactLogger 创建文件日志写入器
// logDir: 日志文件所在目录（如 ~/.codeactor/logs/）
func NewCompactLogger(logDir string) *CompactLogger {
	if logDir == "" {
		return &CompactLogger{} // 空的 logger，写入时静默失败
	}
	return &CompactLogger{logDir: logDir}
}

// ensureFileLocked 确保日志文件已打开（必须持有 l.mu）
func (l *CompactLogger) ensureFileLocked() error {
	today := time.Now().Format("2006-01-02")
	if l.file != nil && l.date == today {
		return nil
	}
	// 关闭旧文件
	if l.file != nil {
		l.file.Close()
		l.file = nil
	}
	// 确保目录存在
	if err := os.MkdirAll(l.logDir, 0755); err != nil {
		return fmt.Errorf("create compact log dir: %w", err)
	}
	// 打开/创建日志文件
	filename := fmt.Sprintf("compact-%s.log", today)
	path := filepath.Join(l.logDir, filename)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open compact log file: %w", err)
	}
	l.file = f
	l.date = today
	return nil
}

// LogSession 将一次压缩会话写入日志（JSON Lines 格式，每行一个 JSON 对象）
// 非阻塞设计：失败时仅记录 slog.Error，不影响压缩主流程
func (l *CompactLogger) LogSession(session *CompactSession) {
	if l == nil || session == nil || l.logDir == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	if err := l.ensureFileLocked(); err != nil {
		slog.Error("compact log: ensure file failed", "error", err)
		return
	}

	data, err := json.Marshal(session)
	if err != nil {
		slog.Error("compact log: marshal failed", "error", err)
		return
	}

	if _, err := l.file.Write(append(data, '\n')); err != nil {
		slog.Error("compact log: write failed", "error", err)
		l.file.Close()
		l.file = nil
	}
}

// Close 关闭日志文件
func (l *CompactLogger) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		err := l.file.Close()
		l.file = nil
		l.date = ""
		return err
	}
	return nil
}
