package agents

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"codeactor/internal/logging"

	"log/slog"
)

// toolLogger is a separate logger for tool calls
var toolLogger *slog.Logger
var toolLogFile *os.File

// ensureToolLogFile checks if the current toolLogFile matches the task-aware path.
// If not (e.g., taskID changed), closes the old file and opens a new one.
func ensureToolLogFile() {
	currentPath := ""
	if toolLogFile != nil {
		currentPath = toolLogFile.Name()
	}

	taskID := logging.GetCurrentTaskID()
	logDir := logging.GetTaskLogDir(taskID)
	dateStr := time.Now().Format("2006-01-02")
	newPath := filepath.Join(logDir, fmt.Sprintf("tool-%s.log", dateStr))

	if currentPath != newPath {
		if toolLogFile != nil {
			toolLogFile.Close()
			toolLogFile = nil
		}
		if err := os.MkdirAll(logDir, 0755); err != nil {
			slog.Warn("Failed to create tool log directory", "dir", logDir, "error", err)
			return
		}
		var fileErr error
		toolLogFile, fileErr = os.OpenFile(newPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if fileErr != nil {
			slog.Warn("Failed to open tool log file", "file", newPath, "error", fileErr)
			toolLogFile = nil
			return
		}
		handler := slog.NewTextHandler(toolLogFile, &slog.HandlerOptions{
			Level: slog.LevelInfo,
		})
		toolLogger = slog.New(handler)
	}
}

// InitToolLogger initializes the tool logger in TUI mode.
// Creates the log directory if it doesn't exist.
// On file open failure, degrades gracefully using logging.GetFallbackWriter()
// which ensures TUI mode NEVER falls back to os.Stdout.
func InitToolLogger() error {
	logDir := logging.GetTaskLogDir(logging.GetCurrentTaskID())

	// Ensure log directory exists
	if err := os.MkdirAll(logDir, 0755); err != nil {
		slog.Warn("Failed to create tool log directory, using fallback writer",
			"dir", logDir, "error", err)
		handler := slog.NewTextHandler(logging.GetFallbackWriter(), &slog.HandlerOptions{
			Level: slog.LevelInfo,
		})
		toolLogger = slog.New(handler)
		return nil
	}

	dateStr := time.Now().Format("2006-01-02")
	logFileName := fmt.Sprintf("tool-%s.log", dateStr)
	logFilePath := filepath.Join(logDir, logFileName)

	var fileErr error
	toolLogFile, fileErr = os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if fileErr != nil {
		slog.Warn("Failed to open tool log file, using fallback writer",
			"file", logFilePath, "error", fileErr)
		// CRITICAL: Never fall back to os.Stdout in TUI mode.
		// Use logging.GetFallbackWriter() which returns discard or app log file in TUI mode.
		handler := slog.NewTextHandler(logging.GetFallbackWriter(), &slog.HandlerOptions{
			Level: slog.LevelInfo,
		})
		toolLogger = slog.New(handler)
		return nil
	}

	handler := slog.NewTextHandler(toolLogFile, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	toolLogger = slog.New(handler)
	return nil
}

// delegateLogFile and delegateLogger are separate from the tool logger
// They write exclusively to delegate-{date}.log for sub-agent delegation tracing.
var delegateLogFile *os.File
var delegateLogger *slog.Logger

// ensureDelegateLogFile checks if the current delegateLogFile matches the task-aware path.
// If not (e.g., taskID changed), closes the old file and opens a new one.
func ensureDelegateLogFile() {
	currentPath := ""
	if delegateLogFile != nil {
		currentPath = delegateLogFile.Name()
	}

	taskID := logging.GetCurrentTaskID()
	logDir := logging.GetTaskLogDir(taskID)
	dateStr := time.Now().Format("2006-01-02")
	newPath := filepath.Join(logDir, fmt.Sprintf("delegate-%s.log", dateStr))

	if currentPath != newPath {
		if delegateLogFile != nil {
			delegateLogFile.Close()
			delegateLogFile = nil
		}
		if err := os.MkdirAll(logDir, 0755); err != nil {
			slog.Warn("Failed to create delegate log directory", "dir", logDir, "error", err)
			return
		}
		var fileErr error
		delegateLogFile, fileErr = os.OpenFile(newPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if fileErr != nil {
			slog.Warn("Failed to open delegate log file", "file", newPath, "error", fileErr)
			delegateLogFile = nil
			return
		}
		handler := slog.NewTextHandler(delegateLogFile, &slog.HandlerOptions{
			Level: slog.LevelInfo,
		})
		delegateLogger = slog.New(handler)
	}
}

// InitDelegateLogger initializes the delegate logger in TUI mode.
// Creates the log directory if it doesn't exist.
// On file open failure, degrades gracefully using logging.GetFallbackWriter()
// which ensures TUI mode NEVER falls back to os.Stdout.
func InitDelegateLogger() error {
	logDir := logging.GetTaskLogDir(logging.GetCurrentTaskID())

	// Ensure log directory exists
	if err := os.MkdirAll(logDir, 0755); err != nil {
		slog.Warn("Failed to create delegate log directory, using fallback writer",
			"dir", logDir, "error", err)
		handler := slog.NewTextHandler(logging.GetFallbackWriter(), &slog.HandlerOptions{
			Level: slog.LevelInfo,
		})
		delegateLogger = slog.New(handler)
		return nil
	}

	dateStr := time.Now().Format("2006-01-02")
	logFileName := fmt.Sprintf("delegate-%s.log", dateStr)
	logFilePath := filepath.Join(logDir, logFileName)

	var fileErr error
	delegateLogFile, fileErr = os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if fileErr != nil {
		slog.Warn("Failed to open delegate log file, using fallback writer",
			"file", logFilePath, "error", fileErr)
		// CRITICAL: Never fall back to os.Stdout in TUI mode.
		// Use logging.GetFallbackWriter() which returns discard or app log file in TUI mode.
		handler := slog.NewTextHandler(logging.GetFallbackWriter(), &slog.HandlerOptions{
			Level: slog.LevelInfo,
		})
		delegateLogger = slog.New(handler)
		return nil
	}

	handler := slog.NewTextHandler(delegateLogFile, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	delegateLogger = slog.New(handler)
	return nil
}

// LogDelegateCall records a delegate tool call with timestamp, tool name, agent name,
// and full arguments. Designed for sub-agent delegation tracing.
// Format: [2025-01-15 10:30:45] tool=delegate_repo agent=repo args={"task":"分析项目结构"}
func LogDelegateCall(toolName, agentName, argsJSON string) {
	ensureDelegateLogFile()
	if delegateLogger == nil {
		return
	}

	timestamp := time.Now().Format("2006-01-02 15:04:05")

	logEntry := fmt.Sprintf("[%s] tool=%s agent=%s args=%s\n",
		timestamp, toolName, agentName, argsJSON)

	if _, err := delegateLogFile.WriteString(logEntry); err != nil {
		slog.Error("Failed to write to delegate log file", "error", err)
	}
}

// CloseDelegateLogger closes the delegate log file and cleans up resources.
func CloseDelegateLogger() {
	if delegateLogFile != nil {
		delegateLogFile.Close()
		delegateLogFile = nil
		delegateLogger = nil
	}
}

// LogToolCall records a tool call with timestamp, tool name, agent name,
// arguments (JSON), result, error message, and duration.
// Format: [2025-01-15 10:30:45] tool=agent_exit agent=DirectorAgent duration=12ms args={"reason":"task completed"} result="" error=""
func LogToolCall(toolName, agentName, argsJSON, result, errMsg string, duration time.Duration) {
	ensureToolLogFile()
	if toolLogger == nil {
		return
	}

	timestamp := time.Now().Format("2006-01-02 15:04:05")
	durationStr := formatDuration(duration)

	logEntry := fmt.Sprintf("[%s] tool=%s agent=%s duration=%s args=%s result=%s error=%s\n",
		timestamp, toolName, agentName, durationStr, argsJSON, result, errMsg)

	if _, err := toolLogFile.WriteString(logEntry); err != nil {
		slog.Error("Failed to write to tool log file", "error", err)
	}
}

// formatDuration formats a duration to a human-readable string.
// Returns milliseconds for durations < 1s, otherwise seconds.
func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.2fs", d.Seconds())
}
