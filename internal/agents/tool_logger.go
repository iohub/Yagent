package agents

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"codeactor/internal/util"

	"log/slog"
)

// toolLogger is a separate logger for tool calls
var toolLogger *slog.Logger
var toolLogFile *os.File

// InitToolLogger initializes the tool logger in TUI mode.
// Creates the log directory if it doesn't exist.
// On file open failure, degrades gracefully with a warning.
func InitToolLogger() error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return util.WrapError(context.Background(), err, "failed to get user home directory")
	}
	logDir := filepath.Join(homeDir, ".codeactor", "logs")

	// Create log directory if it doesn't exist
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return util.WrapError(context.Background(), err, "failed to create logs directory")
	}

	dateStr := time.Now().Format("2006-01-02")
	logFileName := fmt.Sprintf("tool-%s.log", dateStr)
	logFilePath := filepath.Join(logDir, logFileName)

	var fileErr error
	toolLogFile, fileErr = os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if fileErr != nil {
		slog.Warn("Failed to open tool log file, tool calls will not be logged to file",
			"file", logFilePath,
			"error", fileErr)
		// Degrade gracefully: set logger to discard handler
		toolLogger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
			Level: slog.LevelInfo,
		}))
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
