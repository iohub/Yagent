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

// InitToolLogger initializes the tool logger in TUI mode.
// Creates the log directory if it doesn't exist.
// On file open failure, degrades gracefully using logging.GetFallbackWriter()
// which ensures TUI mode NEVER falls back to os.Stdout.
func InitToolLogger() error {
	logDir := logging.GetLogDir()

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

// LogToolCall records a tool call with timestamp, tool name, agent name,
// arguments (JSON), result, error message, and duration.
// Format: [2025-01-15 10:30:45] tool=agent_exit agent=ConductorAgent duration=12ms args={"reason":"task completed"} result="" error=""
func LogToolCall(toolName, agentName, argsJSON, result, errMsg string, duration time.Duration) {
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
