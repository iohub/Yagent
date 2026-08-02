package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Mode represents the application's runtime mode.
type Mode string

const (
	ModeTUI  Mode = "tui"
	ModeHTTP Mode = "http"
)

var (
	currentMode  Mode
	logDir       string
	appLogFile   *os.File
	appLogWriter io.Writer
	knowledgeLogFile *os.File
	knowledgeLogger *slog.Logger
	initialized  bool
	mu           sync.Mutex
	// taskID 管理变量，带 RWMutex 保护
	currentTaskID string
	taskMu        sync.RWMutex
)

// syncedWriter wraps an io.Writer with a mutex for concurrent safety.
type syncedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (sw *syncedWriter) Write(p []byte) (n int, err error) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	return sw.w.Write(p)
}

// Init initializes the global logging system based on the application mode.
// Must be called from main() after mode detection, before any meaningful work.
func Init(mode Mode) error {
	mu.Lock()
	defer mu.Unlock()

	currentMode = mode
	logDir = getLogDir()

	// Ensure log directory exists
	if err := os.MkdirAll(logDir, 0755); err != nil {
		setupFallbackHandler(mode, fmt.Errorf("create log dir: %w", err))
		return fmt.Errorf("logging: failed to create log directory %s: %w", logDir, err)
	}

	// Open app log file
	logFile, err := openLogFile(logDir, "app")
	if err != nil {
		setupFallbackHandler(mode, fmt.Errorf("open log file: %w", err))
		return fmt.Errorf("logging: failed to open app log file: %w", err)
	}
	appLogFile = logFile
	appLogWriter = &syncedWriter{w: logFile}

	// Determine log level (configurable via env var for debugging)
	level := parseLogLevel()

	opts := &slog.HandlerOptions{Level: level}

	switch mode {
	case ModeTUI:
		// TUI mode: ONLY file output — never stdout/stderr
		slog.SetDefault(slog.New(slog.NewTextHandler(appLogWriter, opts)))
	case ModeHTTP:
		// HTTP mode: stderr + file (stderr for operational visibility, file for persistence)
		multiWriter := &syncedWriter{w: io.MultiWriter(os.Stderr, appLogFile)}
		slog.SetDefault(slog.New(slog.NewTextHandler(multiWriter, opts)))
	default:
		// Unknown mode: safe default — file only
		slog.SetDefault(slog.New(slog.NewTextHandler(appLogWriter, opts)))
	}

	initialized = true
	slog.Info("logging initialized", "mode", mode, "log_dir", logDir)

	// 初始化独立知识日志
	if err := initKnowledgeLoggerLocked(level); err != nil {
		slog.Warn("knowledge logger init failed", "error", err)
	} else {
		knowledgeLogger.Info("knowledge logger initialized", "log_dir", logDir)
	}

	return nil
}

// setupFallbackHandler sets a safe handler when file logging fails.
// In TUI mode: io.Discard (never corrupt terminal).
// In HTTP mode: os.Stderr (still visible to operator).
func setupFallbackHandler(mode Mode, cause error) {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	switch mode {
	case ModeTUI:
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, opts)))
	case ModeHTTP:
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, opts)))
	default:
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, opts)))
	}
	// Use the newly set default logger to log the error
	slog.Error("logging fallback activated", "mode", mode, "cause", cause.Error())
}

// parseLogLevel reads CODEACTOR_LOG_LEVEL env var.
// Defaults to slog.LevelInfo for more verbose logging.
func parseLogLevel() slog.Level {
	envLevel := os.Getenv("CODEACTOR_LOG_LEVEL")
	switch envLevel {
	case "DEBUG":
		return slog.LevelDebug
	case "INFO":
		return slog.LevelInfo
	case "WARN":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// getLogDir returns the log directory path.
func getLogDir() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "codeactor", "logs")
	}
	return filepath.Join(homeDir, ".codeactor", "logs")
}

// openLogFile opens or creates a date-stamped log file.
func openLogFile(dir, prefix string) (*os.File, error) {
	filename := fmt.Sprintf("%s-%s.log", prefix, time.Now().Format("2006-01-02"))
	path := filepath.Join(dir, filename)
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
}

// Close flushes and closes log file resources. Call in defer from main().
func Close() {
	mu.Lock()
	defer mu.Unlock()
	if appLogFile != nil {
		appLogFile.Close()
		appLogFile = nil
		appLogWriter = nil
	}
	if knowledgeLogFile != nil {
		knowledgeLogFile.Close()
		knowledgeLogFile = nil
		knowledgeLogger = nil
	}
}

// GetLogDir returns the current log directory path.
// Used by other packages (tool_logger, llm) for path consistency.
func GetLogDir() string {
	mu.Lock()
	defer mu.Unlock()
	if logDir != "" {
		return logDir
	}
	return getLogDir()
}

// GetTaskLogDir returns the task-specific log directory path.
// Format: ~/.codeactor/logs/{taskID}/
// If taskID is empty, returns the global log directory.
func GetTaskLogDir(taskID string) string {
	if taskID == "" {
		return GetLogDir()
	}
	return filepath.Join(GetLogDir(), taskID)
}

// SetCurrentTaskID sets the current taskID for log file routing.
// If taskID is non-empty, creates the task-specific log directory.
// If taskID is empty, clears the current taskID (logs go to default directory).
// Thread-safe via RWMutex.
func SetCurrentTaskID(taskID string) {
	taskMu.Lock()
	defer taskMu.Unlock()

	// If setting a new taskID, ensure the directory exists
	if taskID != "" {
		dir := filepath.Join(GetLogDir(), taskID)
		if err := os.MkdirAll(dir, 0755); err != nil {
			slog.Warn("Failed to create task log directory",
				"task_id", taskID, "dir", dir, "error", err)
		}
	}

	currentTaskID = taskID
	slog.Debug("Current taskID set", "task_id", taskID)
}

// GetCurrentTaskID returns the current taskID.
func GetCurrentTaskID() string {
	taskMu.RLock()
	defer taskMu.RUnlock()
	return currentTaskID
}

// GetFallbackWriter returns a safe io.Writer for fallback scenarios.
// TUI mode: app log file (or io.Discard if not initialized).
// HTTP mode: os.Stderr.
// Used by tool_logger.go when its own file open fails.
func GetFallbackWriter() io.Writer {
	mu.Lock()
	defer mu.Unlock()
	switch currentMode {
	case ModeTUI:
		if appLogWriter != nil {
			return appLogWriter
		}
		return io.Discard
	case ModeHTTP:
		return os.Stderr
	default:
		if appLogWriter != nil {
			return appLogWriter
		}
		return io.Discard
	}
}

// initKnowledgeLoggerLocked initializes the independent knowledge log file and logger.
// Must be called while holding mu.
func initKnowledgeLoggerLocked(level slog.Level) error {
	logFile, err := openLogFile(logDir, "knowledge")
	if err != nil {
		return err
	}
	knowledgeLogFile = logFile
	knowledgeLogger = slog.New(slog.NewTextHandler(&syncedWriter{w: logFile}, &slog.HandlerOptions{Level: level}))
	return nil
}

// KnowledgeLogger 返回独立知识日志；未初始化时 fallback 到全局默认 logger（保证永不 nil、永不 panic）。
func KnowledgeLogger() *slog.Logger {
	mu.Lock()
	defer mu.Unlock()
	if knowledgeLogger != nil {
		return knowledgeLogger
	}
	return slog.Default()
}
