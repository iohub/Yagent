package artifact

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mustDefaultStore 返回以 t.TempDir() 为根的默认存储（t.Setenv 覆盖 root）。
func mustDefaultStore(t *testing.T) *Store {
	t.Helper()
	t.Setenv("YAGENT_ARTIFACT_ROOT", t.TempDir())
	return DefaultStore()
}

// TestSaveLoadRoundTrip Save/Load 往返字段等价；CharCount 正确（含中文）。
func TestSaveLoadRoundTrip(t *testing.T) {
	s := mustDefaultStore(t)

	const fullText = "中文摘要 mixed with ascii.\n第二行内容\n"
	ref, err := s.Save("proj1", "repo_agent", "fix bug", "摘要", fullText)
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	if ref.ID == "" {
		t.Fatal("Save returned empty id")
	}
	if want := len([]rune(fullText)); ref.CharCount != want {
		t.Errorf("Ref.CharCount = %d, want %d (rune count incl. chinese)", ref.CharCount, want)
	}

	art, err := s.Load("proj1", ref.ID)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if art.ID != ref.ID {
		t.Errorf("ID = %q, want %q", art.ID, ref.ID)
	}
	if art.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d, want 1", art.SchemaVersion)
	}
	if art.Agent != "repo_agent" {
		t.Errorf("Agent = %q, want %q", art.Agent, "repo_agent")
	}
	if art.Task != "fix bug" {
		t.Errorf("Task = %q, want %q", art.Task, "fix bug")
	}
	if art.ProjectID != "proj1" {
		t.Errorf("ProjectID = %q, want %q", art.ProjectID, "proj1")
	}
	if art.Summary != "摘要" {
		t.Errorf("Summary = %q, want %q", art.Summary, "摘要")
	}
	if art.FullText != fullText {
		t.Errorf("FullText mismatch:\n got: %q\nwant: %q", art.FullText, fullText)
	}
	if want := len([]rune(fullText)); art.CharCount != want {
		t.Errorf("CharCount = %d, want %d", art.CharCount, want)
	}
	if art.CreatedAt == "" {
		t.Error("CreatedAt is empty")
	}
}

// TestSaveLoadProjectIDEmpty 空项目 ID 归一化为 "default"。
func TestSaveLoadProjectIDEmpty(t *testing.T) {
	s := mustDefaultStore(t)
	ref, err := s.Save("", "agent", "task", "", "text")
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	art, err := s.Load("", ref.ID)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if art.ProjectID != "default" {
		t.Errorf("ProjectID = %q, want %q", art.ProjectID, "default")
	}
}

// TestDefaultStoreEnvOverride YAGENT_ARTIFACT_ROOT 覆盖默认根目录。
func TestDefaultStoreEnvOverride(t *testing.T) {
	root := t.TempDir()
	t.Setenv("YAGENT_ARTIFACT_ROOT", root)
	s := DefaultStore()
	if _, err := s.Save("p", "a", "t", "", "x"); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "p"))
	if err != nil {
		t.Fatalf("expected artifact saved under env root: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
}

// TestLoadInvalidIDs 非法 ID 一律拒绝（防路径穿越），表驱动。
func TestLoadInvalidIDs(t *testing.T) {
	s := mustDefaultStore(t)

	// 先落一个合法 artifact，确保非法 ID 不会误读到任何文件。
	ref, err := s.Save("p", "a", "t", "", "x")
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	_ = ref

	invalid := []string{
		"",                           // 空串
		"../etc/passwd",              // 相对路径穿越
		"..\\..\\windows",            // 反斜杠穿越
		"/etc/passwd",                // 绝对路径
		"p/../../secret",             // 目录拼接穿越
		"20260219-120000-3fa9c2d1\n", // 携带换行
		" 20260219-120000-3fa9c2d1",  // 前导空格
		"ABCD1234-120000-3fa9c2d1",   // 日期部分含字母
		"20260219-120000-3FA9C2D1",   // hex 大写
		"20260219-1200-3fa9c2d1",     // 时间错误长度
		"20260219-120000-3fa9c2d",    // hex 缺一位
		"20260219-120000-3fa9c2d11",  // hex 多一位
		"20260219-120000",            // 缺 hex 段
		"0-0-0",                      // 全错
		"../../../" + ref.ID,         // 合法 ID + 穿越前缀
	}
	for _, id := range invalid {
		art, err := s.Load("p", id)
		if err == nil {
			t.Errorf("Load(%q) should fail, got artifact %+v", id, art)
			continue
		}
		if !strings.Contains(err.Error(), "invalid artifact id") {
			t.Errorf("Load(%q) error = %v, want 'invalid artifact id'", id, err)
		}
	}
}

// TestAtomicWriteNoTmpResidue tmp+rename 后目录无 .tmp 残留。
func TestAtomicWriteNoTmpResidue(t *testing.T) {
	s := mustDefaultStore(t)
	for i := 0; i < 3; i++ {
		if _, err := s.Save("p", "a", "t", "", strings.Repeat("x", 100)); err != nil {
			t.Fatalf("Save failed: %v", err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(s.root, "p"))
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3 (no .tmp residue)", len(entries))
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("residue tmp file found: %s", e.Name())
		}
		if !strings.HasSuffix(e.Name(), ".json") {
			t.Errorf("unexpected file name: %s", e.Name())
		}
	}
}

// TestFilePermissions 文件权限 0600、目录 0700。
func TestFilePermissions(t *testing.T) {
	s := mustDefaultStore(t)
	ref, err := s.Save("p", "a", "t", "", "x")
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	fi, err := os.Stat(filepath.Join(s.root, "p", ref.ID+".json"))
	if err != nil {
		t.Fatalf("Stat file failed: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("file perm = %o, want 600", got)
	}

	di, err := os.Stat(filepath.Join(s.root, "p"))
	if err != nil {
		t.Fatalf("Stat dir failed: %v", err)
	}
	if got := di.Mode().Perm(); got != 0o700 {
		t.Errorf("dir perm = %o, want 700", got)
	}
}

// TestUpdateSummary 更新 summary 后其他字段保持不变。
func TestUpdateSummary(t *testing.T) {
	s := mustDefaultStore(t)
	const fullText = "complete original result text"
	ref, err := s.Save("p", "repo_agent", "task1", "", fullText)
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	newSummary := "reduced summary with artifact id"
	if err := s.UpdateSummary("p", ref.ID, newSummary); err != nil {
		t.Fatalf("UpdateSummary failed: %v", err)
	}

	art, err := s.Load("p", ref.ID)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if art.Summary != newSummary {
		t.Errorf("Summary = %q, want %q", art.Summary, newSummary)
	}
	if art.FullText != fullText {
		t.Errorf("FullText changed: %q, want %q", art.FullText, fullText)
	}
	if art.Agent != "repo_agent" || art.Task != "task1" {
		t.Errorf("Agent/Task changed: %q/%q", art.Agent, art.Task)
	}
	if art.CharCount != ref.CharCount {
		t.Errorf("CharCount changed: %d, want %d", art.CharCount, ref.CharCount)
	}
}

// TestUpdateSummaryInvalidID 非法 ID 的 UpdateSummary 被拒绝。
func TestUpdateSummaryInvalidID(t *testing.T) {
	s := mustDefaultStore(t)
	if err := s.UpdateSummary("p", "../evil", "s"); err == nil {
		t.Error("UpdateSummary with invalid id should fail")
	}
}

// TestLoadFullText 便捷方法取回完整文本。
func TestLoadFullText(t *testing.T) {
	s := mustDefaultStore(t)
	const want = "full text body 中文"
	ref, err := s.Save("p", "a", "t", "", want)
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	got, err := s.LoadFullText("p", ref.ID)
	if err != nil {
		t.Fatalf("LoadFullText failed: %v", err)
	}
	if got != want {
		t.Errorf("LoadFullText = %q, want %q", got, want)
	}
}

// TestLoadSchemaVersionMismatch schema_version != 1 时 Load 报错。
func TestLoadSchemaVersionMismatch(t *testing.T) {
	s := mustDefaultStore(t)
	ref, err := s.Save("p", "a", "t", "", "x")
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// 手工改写落盘文件的 schema_version 为 2。
	path := filepath.Join(s.root, "p", ref.ID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	raw["schema_version"] = 2
	updated, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	if err := os.WriteFile(path, updated, 0o600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	if _, err := s.Load("p", ref.ID); err == nil {
		t.Error("Load should fail on unsupported schema version")
	}
}

// TestLoadMissingFile 不存在的 ID（合法形态）返回读文件错误。
func TestLoadMissingFile(t *testing.T) {
	s := mustDefaultStore(t)
	_, err := s.Load("p", "20260219-120000-00000000")
	if err == nil {
		t.Error("Load missing file should fail")
	}
}
