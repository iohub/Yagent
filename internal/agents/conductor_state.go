package agents

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// TaskState 任务状态
type TaskState struct {
	ID        string                 `json:"id"`
	Type      string                 `json:"type"`
	Status    string                 `json:"status"` // pending/running/completed/failed
	AgentID   string                 `json:"agent_id,omitempty"`
	Input     json.RawMessage        `json:"input,omitempty"`
	Output    json.RawMessage        `json:"output,omitempty"`
	Error     string                 `json:"error,omitempty"`
	StartedAt time.Time              `json:"started_at,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

// ConductorState Conductor 的可持久化状态快照
type ConductorState struct {
	SessionID    string                `json:"session_id"`
	Phase        Phase                 `json:"phase"`
	Iteration    int                   `json:"iteration"`
	StepCount    int                   `json:"step_count"`
	ActiveTasks  map[string]*TaskState `json:"active_tasks,omitempty"`
	CompletedIDs []string              `json:"completed_ids,omitempty"`
	Context      map[string]interface{} `json:"context,omitempty"`
	Version      int                   `json:"version"`      // schema 版本
	Checksum     string                `json:"checksum"`     // SHA256 校验
	SavedAt      time.Time             `json:"saved_at"`
}

// ComputeChecksum 计算状态数据的 SHA256 校验和
func (s *ConductorState) ComputeChecksum() string {
	// 排除 checksum 和 saved_at 字段
	data := struct {
		SessionID    string                `json:"session_id"`
		Phase        Phase                 `json:"phase"`
		Iteration    int                   `json:"iteration"`
		StepCount    int                   `json:"step_count"`
		ActiveTasks  map[string]*TaskState `json:"active_tasks,omitempty"`
		CompletedIDs []string              `json:"completed_ids,omitempty"`
		Context      map[string]interface{} `json:"context,omitempty"`
		Version      int                   `json:"version"`
	}{
		SessionID:    s.SessionID,
		Phase:        s.Phase,
		Iteration:    s.Iteration,
		StepCount:    s.StepCount,
		ActiveTasks:  s.ActiveTasks,
		CompletedIDs: s.CompletedIDs,
		Context:      s.Context,
		Version:      s.Version,
	}
	bytes, _ := json.Marshal(data)
	hash := sha256.Sum256(bytes)
	return fmt.Sprintf("%x", hash[:8])
}

// Validate 验证状态数据的完整性
func (s *ConductorState) Validate() error {
	if s.SessionID == "" {
		return fmt.Errorf("session_id is required")
	}
	if s.Phase == "" {
		return fmt.Errorf("phase is required")
	}
	expectedChecksum := s.ComputeChecksum()
	if s.Checksum != "" && s.Checksum != expectedChecksum {
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expectedChecksum, s.Checksum)
	}
	return nil
}

// Snapshot 从 ConductorAgent 创建状态快照
func (a *ConductorAgent) Snapshot() *ConductorState {
	return &ConductorState{
		SessionID:    fmt.Sprintf("session-%d", time.Now().UnixNano()),
		Phase:        PhaseIdle,
		Iteration:    0,
		StepCount:    0,
		ActiveTasks:  make(map[string]*TaskState),
		CompletedIDs: make([]string, 0),
		Version:      1,
		SavedAt:      time.Now(),
	}
}

// StateStore 状态存储接口
type StateStore interface {
	Save(state *ConductorState) error
	Load(sessionID string) (*ConductorState, error)
	List() ([]string, error)
	Delete(sessionID string) error
}

// FileStateStore 文件系统状态存储
type FileStateStore struct {
	baseDir string
	mu      sync.Mutex
}

// NewFileStateStore 创建文件状态存储
func NewFileStateStore(baseDir string) *FileStateStore {
	return &FileStateStore{
		baseDir: baseDir,
	}
}

// Save 保存状态快照到文件（原子写入：先写.tmp再rename）
func (s *FileStateStore) Save(state *ConductorState) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 计算校验和
	state.Checksum = state.ComputeChecksum()
	state.SavedAt = time.Now()

	sessionDir := filepath.Join(s.baseDir, state.SessionID)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}

	statePath := filepath.Join(sessionDir, "state.json")
	tmpPath := statePath + ".tmp"

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	// 原子写入
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("write temp state: %w", err)
	}
	if err := os.Rename(tmpPath, statePath); err != nil {
		return fmt.Errorf("rename state file: %w", err)
	}

	return nil
}

// Load 加载状态快照
func (s *FileStateStore) Load(sessionID string) (*ConductorState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	statePath := filepath.Join(s.baseDir, sessionID, "state.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("state not found for session %s", sessionID)
		}
		return nil, fmt.Errorf("read state file: %w", err)
	}

	var state ConductorState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("unmarshal state: %w", err)
	}

	// 验证校验和
	if err := state.Validate(); err != nil {
		return nil, fmt.Errorf("state validation failed: %w", err)
	}

	return &state, nil
}

// List 列出所有已保存的 session ID
func (s *FileStateStore) List() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list sessions: %w", err)
	}

	var sessions []string
	for _, entry := range entries {
		if entry.IsDir() {
			statePath := filepath.Join(s.baseDir, entry.Name(), "state.json")
			if _, err := os.Stat(statePath); err == nil {
				sessions = append(sessions, entry.Name())
			}
		}
	}
	return sessions, nil
}

// Delete 删除 session 状态
func (s *FileStateStore) Delete(sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	sessionDir := filepath.Join(s.baseDir, sessionID)
	if err := os.RemoveAll(sessionDir); err != nil {
		return fmt.Errorf("delete session dir: %w", err)
	}
	return nil
}
