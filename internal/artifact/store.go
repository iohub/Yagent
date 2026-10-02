package artifact

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
	"unicode/utf8"
)

// schemaVersion 当前 artifact 落盘格式版本。
const schemaVersion = 1

// idPattern artifact ID 的合法形态：{8位日期}-{6位时间}-{8位小写hex}。
// Load 前强校验，防止非法 ID 引发路径穿越。
var idPattern = regexp.MustCompile(`^\d{8}-\d{6}-[0-9a-f]{8}$`)

// Ref artifact 引用：Save 成功后返回给调用方的轻量指针。
// 由 AgentResult.ArtifactRef 持有，Director 需要细节时按 ID 回查。
type Ref struct {
	ID        string // 形如 "20260219-120000-3fa9c2d1"
	CharCount int    // 全文 rune 数
}

// Artifact 落盘的完整委派返回物：JSON 单文件，按 {root}/{projectID}/{id}.json 组织。
type Artifact struct {
	SchemaVersion int    `json:"schema_version"` // 恒为 1
	ID            string `json:"id"`
	Agent         string `json:"agent"`
	Task          string `json:"task"`
	ProjectID     string `json:"project_id"`
	Summary       string `json:"summary"`
	FullText      string `json:"full_text"`
	CharCount     int    `json:"char_count"` // 全文 rune 数
	CreatedAt     string `json:"created_at"` // RFC3339
}

// Store artifact 存储：根目录下按项目分目录、按 ID 分文件。
// 并发安全：所有方法无共享可变状态（ID 生成用 crypto/rand）。
type Store struct {
	root string
}

// NewStore 以指定根目录创建存储。
func NewStore(root string) *Store {
	if root == "" {
		root = "."
	}
	return &Store{root: root}
}

// DefaultStore 返回默认存储：根目录 ~/.yagent/data/artifacts（沿用
// memory/rollout_writer.go 的 home fallback 惯例：home 获取失败回退当前目录）。
// 环境变量 YAGENT_ARTIFACT_ROOT 覆盖根目录——每次调用时读取，便于测试 t.Setenv。
func DefaultStore() *Store {
	if root := os.Getenv("YAGENT_ARTIFACT_ROOT"); root != "" {
		return NewStore(root)
	}
	homeDir, err := os.UserHomeDir()
	if err != nil || homeDir == "" {
		homeDir = "."
	}
	return NewStore(filepath.Join(homeDir, ".yagent", "data", "artifacts"))
}

// Save 将完整结果落盘为 artifact 并返回引用。
// ID 生成：{YYYYMMDD-HHmmss UTC}-{8位 crypto/rand 小写 hex}；
// 路径：{root}/{projectID}/{id}.json（MkdirAll 0700）；
// 原子写：先写 {id}.json.tmp（0600）再 os.Rename，崩溃不产生半写文件。
func (s *Store) Save(projectID, agent, task, summary, fullText string) (Ref, error) {
	id, err := newArtifactID()
	if err != nil {
		return Ref{}, err
	}
	if projectID == "" {
		projectID = "default"
	}
	art := Artifact{
		SchemaVersion: schemaVersion,
		ID:            id,
		Agent:         agent,
		Task:          task,
		ProjectID:     projectID,
		Summary:       summary,
		FullText:      fullText,
		CharCount:     utf8.RuneCountInString(fullText),
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	if err := s.write(projectID, art); err != nil {
		return Ref{}, err
	}
	return Ref{ID: id, CharCount: art.CharCount}, nil
}

// UpdateSummary 更新已落盘 artifact 的 summary 字段并覆盖写回同路径。
// 用于先 Save 拿 ID、再 Reduce 生成带 ID 的摘要后的回写步骤。
func (s *Store) UpdateSummary(projectID, id, summary string) error {
	art, err := s.Load(projectID, id)
	if err != nil {
		return err
	}
	art.Summary = summary
	return s.write(art.ProjectID, art)
}

// Load 按 projectID + id 读取并反序列化 artifact。
// 安全：id 必须全匹配 ^\d{8}-\d{6}-[0-9a-f]{8}$（防路径穿越）；
// 反序列化后校验 SchemaVersion==1。
func (s *Store) Load(projectID, id string) (Artifact, error) {
	if !idPattern.MatchString(id) {
		return Artifact{}, fmt.Errorf("invalid artifact id: %q", id)
	}
	if projectID == "" {
		projectID = "default"
	}
	data, err := os.ReadFile(filepath.Join(s.root, projectID, id+".json"))
	if err != nil {
		return Artifact{}, fmt.Errorf("artifact: load %s: %w", id, err)
	}
	var art Artifact
	if err := json.Unmarshal(data, &art); err != nil {
		return Artifact{}, fmt.Errorf("artifact: unmarshal %s: %w", id, err)
	}
	if art.SchemaVersion != schemaVersion {
		return Artifact{}, fmt.Errorf("artifact: unsupported schema version %d in %s (want %d)", art.SchemaVersion, id, schemaVersion)
	}
	return art, nil
}

// LoadFullText 便捷方法：仅取回完整文本。
func (s *Store) LoadFullText(projectID, id string) (string, error) {
	art, err := s.Load(projectID, id)
	if err != nil {
		return "", err
	}
	return art.FullText, nil
}

// write 原子写：先写 {id}.json.tmp（0600）再 os.Rename 到 {id}.json。
func (s *Store) write(projectID string, art Artifact) error {
	if !idPattern.MatchString(art.ID) {
		return fmt.Errorf("invalid artifact id: %q", art.ID)
	}
	dir := filepath.Join(s.root, projectID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("artifact: create dir: %w", err)
	}
	data, err := json.MarshalIndent(art, "", "  ")
	if err != nil {
		return fmt.Errorf("artifact: marshal: %w", err)
	}
	finalPath := filepath.Join(dir, art.ID+".json")
	tmpPath := finalPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return fmt.Errorf("artifact: write tmp: %w", err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath) // rename 失败时清理残留 tmp
		return fmt.Errorf("artifact: rename: %w", err)
	}
	return nil
}

// newArtifactID 生成 {YYYYMMDD-HHmmss UTC}-{8位 crypto/rand 小写 hex} 形态的 ID。
func newArtifactID() (string, error) {
	buf := make([]byte, 4) // 4 bytes -> 8 hex chars
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("artifact: generate random id: %w", err)
	}
	return time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(buf), nil
}
