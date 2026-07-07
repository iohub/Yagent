package memory

import (
	"codeactor/internal/logging"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SharedMemoryLogger 记录共享记忆的更新操作日志到独立文件
// 日志文件路径：~/.codeactor/logs/shared-memory-YYYY-MM-DD.log
type SharedMemoryLogger struct{}

// NewSharedMemoryLogger 创建共享记忆日志记录器
func NewSharedMemoryLogger() *SharedMemoryLogger {
	return &SharedMemoryLogger{}
}

// LogUpdate 记录一次共享记忆更新操作
// proposal: 更新提议
// result: 更新结果（包含接受/拒绝状态和原因）
// beforeJSON: 更新前的记忆内容(JSON)
// afterJSON: 更新后的记忆内容(JSON)
func (l *SharedMemoryLogger) LogUpdate(proposal *MemoryUpdateProposal, result UpdateResult, beforeJSON, afterJSON string) {
	logDir := logging.GetLogDir()
	filename := fmt.Sprintf("shared-memory-%s.log", time.Now().Format("2006-01-02"))
	path := filepath.Join(logDir, filename)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		slog.Warn("SharedMemoryLogger: failed to open log file",
			"path", path,
			"error", err,
		)
		return
	}
	defer f.Close()

	now := time.Now().Format("2006-01-02 15:04:05.000")

	var entry string
	if result.Accepted {
		// Accepted entry - visual separator with checkmark
		separator := strings.Repeat("=", 60)
		entry = fmt.Sprintf("\n%s\n[%s] ✅ SHARED MEMORY UPDATED\n%s\n\n", separator, now, separator)
	} else {
		// Rejected entry - shorter separator
		separator := strings.Repeat("-", 40)
		entry = fmt.Sprintf("\n%s\n[%s] ⏭️ SHARED MEMORY UPDATE REJECTED\n%s\n\n", separator, now, separator)
	}

	// Basic info
	entry += fmt.Sprintf("Dimension    : %s\n", proposal.Dimension)
	entry += fmt.Sprintf("Action       : %s\n", proposal.Action)
	entry += fmt.Sprintf("Proposed By  : %s\n", proposal.ProposedBy)
	entry += fmt.Sprintf("Reason       : %s\n", proposal.Reason)

	// Payload
	payloadStr := fmt.Sprintf("%+v", proposal.Payload)
	if len(payloadStr) > 2000 {
		payloadStr = payloadStr[:2000] + "... (truncated)"
	}
	entry += fmt.Sprintf("Payload      : %s\n", payloadStr)

	// Result
	entry += fmt.Sprintf("Accepted     : %v\n", result.Accepted)
	entry += fmt.Sprintf("Result       : %s\n", result.Reason)

	// Before/After diff summary
	if result.Accepted && beforeJSON != "" && afterJSON != "" {
		if len(beforeJSON) > 1000 {
			entry += fmt.Sprintf("Before (JSON): %s... (%d bytes total)\n", beforeJSON[:1000], len(beforeJSON))
		} else {
			entry += fmt.Sprintf("Before (JSON): %s\n", beforeJSON)
		}
		if len(afterJSON) > 1000 {
			entry += fmt.Sprintf("After  (JSON): %s... (%d bytes total)\n", afterJSON[:1000], len(afterJSON))
		} else {
			entry += fmt.Sprintf("After  (JSON): %s\n", afterJSON)
		}
	}

	entry += "\n"

	if _, err := f.WriteString(entry); err != nil {
		slog.Warn("SharedMemoryLogger: failed to write to log file",
			"error", err,
		)
	}
}
