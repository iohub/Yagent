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
)

const (
	notesDirName    = "notes"
	yagentDirName   = ".yagent"
	maxBoardIDLen   = 128
	noteFileExt     = ".md"
	tmpFileExt      = ".md.tmp"
	dirPerm         = 0o700
	filePerm        = 0o600
	frontMatterDelim = "---"
)

var boardIDPattern = regexp.MustCompile(`[^A-Za-z0-9_.\-]`)

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

// EnsureDir creates the directory (and parents) if it doesn't exist.
func EnsureDir(dir string) error {
	return os.MkdirAll(dir, dirPerm)
}

// AtomicWrite writes data to path via temp file + rename (crash-safe).
// Mirrors artifact/store.go write() semantics.
func AtomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return fmt.Errorf("blackboard: create dir: %w", err)
	}
	tmpPath := path + tmpFileExt
	if err := os.WriteFile(tmpPath, data, filePerm); err != nil {
		return fmt.Errorf("blackboard: write tmp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("blackboard: rename: %w", err)
	}
	return nil
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
