package agents

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"yagent/internal/artifact"
	"yagent/internal/llm"
)

// newTestExecutor 构造 ExecutorResult。
func newTestExecutor(text string) ExecutorResult {
	return ExecutorResult{Text: text}
}

// setArtifactRoot 测试环境：YAGENT_ARTIFACT_ROOT 指向 t.TempDir()。
// 同时清理 projectPathProvider：第二批次接线后 NewDirectorAgent 构造时会进程级
// 注入 mock env 的路径，导致同包内后续测试的落盘 projectID 非 "default"；
// 每个测试从干净 provider 状态开始（保留"无 provider 注入"的原测试意图）。
func setArtifactRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("YAGENT_ARTIFACT_ROOT", root)
	t.Cleanup(func() { SetProjectPathProvider(nil) })
	SetProjectPathProvider(nil)
	return root
}

// TestFinalizeResultShortText 短文本（预算内）：全文即摘要、不落盘、Text=Summary。
func TestFinalizeResultShortText(t *testing.T) {
	setArtifactRoot(t)
	// 400 ascii = 100 token <= 500。
	text := strings.Repeat("a", 400)
	got := FinalizeResult("repo_agent", "task1", newTestExecutor(text))

	if got.Summary != text {
		t.Errorf("Summary = full text expected, got %d chars", len(got.Summary))
	}
	if got.ArtifactRef != nil {
		t.Errorf("ArtifactRef = %+v, want nil (within budget)", got.ArtifactRef)
	}
	if got.Text != got.Summary {
		t.Errorf("Text should equal Summary for backward compat: %q vs %q", got.Text, got.Summary)
	}
	if len(got.Memory) != 0 {
		t.Errorf("Memory = %d messages, want 0 (empty history)", len(got.Memory))
	}
}

// TestFinalizeResultLongText 长文本：落盘 + 分级摘要 + 往返一致 + summary 回写。
func TestFinalizeResultLongText(t *testing.T) {
	root := setArtifactRoot(t)
	// 2004 ascii = 501 token > 500。
	text := strings.Repeat("a", 2004)
	got := FinalizeResult("repo_agent", "fix bug", newTestExecutor(text))

	if got.ArtifactRef == nil {
		t.Fatal("ArtifactRef = nil, want non-nil for over-budget text")
	}
	if got.Summary == text {
		t.Error("Summary should be truncated, not the full text")
	}
	if !strings.Contains(got.Summary, "Truncated") {
		t.Errorf("Summary should contain truncation marker, got: %s", got.Summary)
	}
	if !strings.Contains(got.Summary, got.ArtifactRef.ID) {
		t.Errorf("Summary marker should contain artifact id %s", got.ArtifactRef.ID)
	}
	if got.Text != got.Summary {
		t.Error("Text should equal Summary for backward compat")
	}
	if got.ArtifactRef.CharCount != 2004 {
		t.Errorf("ArtifactRef.CharCount = %d, want 2004", got.ArtifactRef.CharCount)
	}

	// Load 往返：FullText 与输入一致。
	full, err := artifact.DefaultStore().LoadFullText("default", got.ArtifactRef.ID)
	if err != nil {
		t.Fatalf("LoadFullText failed: %v", err)
	}
	if full != text {
		t.Errorf("FullText round-trip mismatch: got %d chars, want %d", len(full), len(text))
	}

	// summary 已回写进 artifact。
	art, err := artifact.DefaultStore().Load("default", got.ArtifactRef.ID)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if art.Summary != got.Summary {
		t.Errorf("artifact.Summary not updated, got %q want %q", art.Summary, got.Summary)
	}
	if art.Agent != "repo_agent" {
		t.Errorf("artifact.Agent = %q, want repo_agent", art.Agent)
	}
	if art.Task != "fix bug" {
		t.Errorf("artifact.Task = %q, want fix bug", art.Task)
	}
	if art.ProjectID != "default" {
		t.Errorf("artifact.ProjectID = %q, want default (no provider injected)", art.ProjectID)
	}

	// 落盘位置：{root}/default/{id}.json。
	if _, err := os.Stat(filepath.Join(root, "default", got.ArtifactRef.ID+".json")); err != nil {
		t.Errorf("artifact file not found at expected path: %v", err)
	}
}

// TestFinalizeResultDisabled kill-switch：不落盘、全文摘要。
func TestFinalizeResultDisabled(t *testing.T) {
	root := setArtifactRoot(t)
	t.Setenv("YAGENT_ARTIFACT_DISABLE", "1")

	text := strings.Repeat("a", 2004)
	got := FinalizeResult("repo_agent", "task", newTestExecutor(text))

	if got.ArtifactRef != nil {
		t.Errorf("ArtifactRef = %+v, want nil when disabled", got.ArtifactRef)
	}
	if got.Summary != text || got.Text != text {
		t.Error("disabled mode should return full text as summary")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("no artifact should be written when disabled, found %d entries", len(entries))
	}
}

// TestFinalizeResultEmptyText 空文本：直接返回、不落盘。
func TestFinalizeResultEmptyText(t *testing.T) {
	setArtifactRoot(t)
	got := FinalizeResult("repo_agent", "task", ExecutorResult{})
	if got.Text != "" || got.Summary != "" {
		t.Errorf("empty text should stay empty, got Text=%q Summary=%q", got.Text, got.Summary)
	}
	if got.ArtifactRef != nil {
		t.Error("ArtifactRef should be nil for empty text")
	}
}

// TestFinalizeResultMemoryConvert Memory 来自 ConvertLLMHistoryToMemory。
func TestFinalizeResultMemoryConvert(t *testing.T) {
	setArtifactRoot(t)
	exec := ExecutorResult{
		Text: "short",
		History: []llm.Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "hello"},
		},
	}
	got := FinalizeResult("chat", "task", exec)
	if len(got.Memory) != 2 {
		t.Fatalf("Memory = %d messages, want 2", len(got.Memory))
	}
}

// TestFinalizeResultSaveFailure 落盘失败（root 指向一个文件）：降级为全文摘要、不 panic。
func TestFinalizeResultSaveFailure(t *testing.T) {
	root := t.TempDir()
	notADir := filepath.Join(root, "notadir")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	t.Setenv("YAGENT_ARTIFACT_ROOT", notADir) // 非法：root 是文件，MkdirAll 必失败

	text := strings.Repeat("a", 2004)
	got := FinalizeResult("repo_agent", "task", newTestExecutor(text))

	if got.ArtifactRef != nil {
		t.Errorf("ArtifactRef = %+v, want nil on save failure", got.ArtifactRef)
	}
	if got.Summary != text {
		t.Errorf("save failure should degrade to full-text summary (integrity first), got %d chars want %d", len(got.Summary), len(text))
	}
	if got.Text != text {
		t.Error("Text should equal full text on save failure")
	}
}

// TestFormatForDirectorNoRef 无 ArtifactRef：旧格式（与 injectSubAgentMemory 一致）。
func TestFormatForDirectorNoRef(t *testing.T) {
	r := AgentResult{Summary: "hello world"}
	got := FormatForDirector("delegate_repo", r, "", 0)
	want := "[Sub-Agent Result: delegate_repo]\nhello world"
	if got != want {
		t.Errorf("FormatForDirector = %q, want %q", got, want)
	}
}

// TestFormatForDirectorWithRef 有 ArtifactRef：逐行断言完整格式。
func TestFormatForDirectorWithRef(t *testing.T) {
	const id = "20260219-120000-3fa9c2d1"
	r := AgentResult{
		Summary:     "kept summary text\n\n[Truncated: showing first 100 of 2000 chars. Full result stored as artifact " + id + ".]\n",
		ArtifactRef: &artifact.Ref{ID: id, CharCount: 2000},
	}
	got := FormatForDirector("delegate_coding", r, "", 0)

	lines := strings.Split(got, "\n")
	wantLines := []string{
		"[Sub-Agent Result: delegate_coding]",
		fmt.Sprintf("artifact: %s (2000 chars total)", id),
		"kept summary text",
		"",
		"[Truncated: showing first 100 of 2000 chars. Full result stored as artifact " + id + ".]",
		"",
		fmt.Sprintf("[Output truncated to fit context. Full result stored as artifact %s. Call read_artifact(id=\"%s\") if you need details beyond the summary above.]", id, id),
	}
	if len(lines) != len(wantLines) {
		t.Fatalf("got %d lines, want %d:\n%s", len(lines), len(wantLines), got)
	}
	for i, want := range wantLines {
		if lines[i] != want {
			t.Errorf("line %d = %q, want %q", i, lines[i], want)
		}
	}
}

// TestSetProjectPathProvider 提供者注入影响落盘 projectID。
func TestSetProjectPathProvider(t *testing.T) {
	root := setArtifactRoot(t)
	t.Cleanup(func() { SetProjectPathProvider(nil) })

	SetProjectPathProvider(func() string { return "/tmp/myproj" })
	// "/tmp/myproj" -> base "myproj" + 短哈希。

	text := strings.Repeat("a", 2004)
	got := FinalizeResult("repo_agent", "task", newTestExecutor(text))
	if got.ArtifactRef == nil {
		t.Fatal("expected artifact ref")
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}
	found := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "myproj_") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected project dir with myproj_ prefix under root, got %v", entries)
	}
}

// TestFinalizeResultConcurrent 并发 50 次：ID 唯一、文件数正确。
func TestFinalizeResultConcurrent(t *testing.T) {
	root := setArtifactRoot(t)

	const n = 50
	ids := make(chan string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got := FinalizeResult("repo_agent", fmt.Sprintf("task-%d", i), newTestExecutor(strings.Repeat("a", 2004)))
			if got.ArtifactRef == nil {
				t.Errorf("goroutine %d: ArtifactRef = nil", i)
				return
			}
			ids <- got.ArtifactRef.ID
		}(i)
	}
	wg.Wait()
	close(ids)

	seen := make(map[string]bool, n)
	for id := range ids {
		if seen[id] {
			t.Errorf("duplicate artifact id: %s", id)
		}
		seen[id] = true
	}
	if len(seen) != n {
		t.Errorf("unique ids = %d, want %d", len(seen), n)
	}

	entries, err := os.ReadDir(filepath.Join(root, "default"))
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}
	if len(entries) != n {
		t.Errorf("artifact files = %d, want %d", len(entries), n)
	}
}
