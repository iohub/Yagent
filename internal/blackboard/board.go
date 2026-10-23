package blackboard

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"yagent/internal/util"
)

const (
	notesDirName     = "notes"
	yagentDirName    = ".yagent"
	maxBoardIDLen    = 128
	noteFileExt      = ".md"
	tmpFileExt       = ".md.tmp"
	dirPerm          = 0o700
	filePerm         = 0o600
	frontMatterDelim = "---"
)

var boardIDPattern = regexp.MustCompile(`[^A-Za-z0-9_.\-]`)

// Enabled 报告便签板是否可用。唯一判据是 YAGENT_BLACKBOARD=0：
// 写便签、读便签、注入索引、启动清理四处都问它，开关规则因此只有一份。
func Enabled() bool {
	return os.Getenv("YAGENT_BLACKBOARD") != "0"
}

// NotesRootDir returns the root directory for blackboard notes.
// Priority: env YAGENT_NOTES_ROOT → ~/.yagent/notes → "./notes" fallback.
func NotesRootDir() string {
	if root := os.Getenv("YAGENT_NOTES_ROOT"); root != "" {
		return root
	}
	homeDir, err := os.UserHomeDir()
	if err != nil || homeDir == "" {
		homeDir = "."
	}
	return filepath.Join(homeDir, yagentDirName, notesDirName)
}

// SanitizeBoardID cleans a boardID for safe use as a directory name.
// Replaces invalid characters with '_', truncates to maxBoardIDLen.
// Returns "adhoc_<ts>_<rand4>" if input is empty after sanitization.
func SanitizeBoardID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return generateAdhocID()
	}
	id = boardIDPattern.ReplaceAllString(id, "_")
	if len(id) > maxBoardIDLen {
		id = id[:maxBoardIDLen]
	}
	if id == "" || id == "_" {
		return generateAdhocID()
	}
	return id
}

// GenerateAdhocID produces a fallback boardID: adhoc_<unix_ts>_<rand4>.
func GenerateAdhocID() string {
	return generateAdhocID()
}

func generateAdhocID() string {
	buf := make([]byte, 2)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("adhoc_%d_0000", time.Now().Unix())
	}
	return fmt.Sprintf("adhoc_%d_%s", time.Now().Unix(), hex.EncodeToString(buf))
}

// BoardDir returns the notes directory for a given boardID.
func BoardDir(boardID string) string {
	return filepath.Join(NotesRootDir(), SanitizeBoardID(boardID))
}

// AgentDir returns the notes subdirectory for a specific agent within a board.
func AgentDir(boardID, agent string) string {
	return filepath.Join(BoardDir(boardID), sanitizeAgentName(agent))
}

func sanitizeAgentName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "unknown"
	}
	name = boardIDPattern.ReplaceAllString(name, "_")
	if len(name) > 64 {
		name = name[:64]
	}
	return name
}

// AtomicWrite writes data to path via temp file + rename (crash-safe).
// 落盘语义的内核在 util.AtomicWrite（全仓唯一一份，I21）；这里只保留便签的两个既有约定：
// 目录 0700 / 文件 0600，以及 `.md.tmp` 后缀 —— 它刻意不以 `.md` 结尾，
// 这样 `index.go:129` 与 `gc.go:131` 的"只认 *.md"过滤不会把中间态当成一张便签。
func AtomicWrite(path string, data []byte) error {
	return util.AtomicWrite(path, data, util.AtomicWriteOptions{
		DirPerm:   dirPerm,
		FilePerm:  filePerm,
		TmpSuffix: tmpFileExt,
		Label:     "blackboard",
	})
}

// GenerateNoteFilename produces a unique, sortable filename:
// 20060102T150405Z_<rand4>.md
func GenerateNoteFilename() (string, error) {
	buf := make([]byte, 2)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("blackboard: generate filename: %w", err)
	}
	ts := time.Now().UTC().Format("20060102T150405Z")
	return ts + "_" + hex.EncodeToString(buf) + noteFileExt, nil
}
