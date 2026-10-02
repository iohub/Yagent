package agents

import (
	"strings"
	"testing"

	"yagent/internal/artifact"
)

// 本文件为"委派返回物分级"第二批次接线断言：
//   - FinalizeResultFull（Full 变体）：超预算路径 ArtifactRef 非 nil、Summary 含
//     分级标记、Text 恒为完整输出（程序化消费保护）；
//   - FormatForDirector：有 ArtifactRef 时含 read_artifact 取回指令；
//   - 短文本路径回归保护（两变体一致）。

// TestFinalizeResultFullLongText Full 变体超长文本接线断言。
func TestFinalizeResultFullLongText(t *testing.T) {
	setArtifactRoot(t)
	// 3000 个中文字符（约 9000 bytes / 3000 runes），远超 500 token 预算。
	text := strings.Repeat("中", 3000)
	got := FinalizeResultFull("repo_agent", "wiring task", newTestExecutor(text))

	if got.ArtifactRef == nil {
		t.Fatal("ArtifactRef = nil, want non-nil for over-budget text (Full variant)")
	}
	if !strings.Contains(got.Summary, "Truncated") {
		t.Errorf("Summary should contain truncation marker, got: %s", got.Summary)
	}
	if !strings.Contains(got.Summary, got.ArtifactRef.ID) {
		t.Errorf("Summary should contain artifact id %s, got: %s", got.ArtifactRef.ID, got.Summary)
	}
	if got.Text != text {
		t.Errorf("Full variant: Text should be the full output, got %d chars, want %d", len(got.Text), len(text))
	}
	if got.Summary == text {
		t.Error("Summary should be truncated, not the full text")
	}
	if len(got.Memory) != 0 {
		t.Errorf("Memory = %d messages, want 0 (empty history)", len(got.Memory))
	}

	// 落盘往返：LoadFullText 与输入一致。
	full, err := artifact.DefaultStore().LoadFullText("default", got.ArtifactRef.ID)
	if err != nil {
		t.Fatalf("LoadFullText failed: %v", err)
	}
	if full != text {
		t.Errorf("FullText round-trip mismatch: got %d chars, want %d", len(full), len(text))
	}
}

// TestFormatForDirectorReadArtifactInstruction 接线断言：有 ArtifactRef 时
// FormatForDirector 输出含 artifact 元信息行与 read_artifact(id="{ID}") 取回指令。
func TestFormatForDirectorReadArtifactInstruction(t *testing.T) {
	const id = "20260219-120000-3fa9c2d1"
	r := AgentResult{
		Summary:     "kept summary text\n\n[Truncated: showing first 100 of 2000 chars. Full result stored as artifact " + id + ".]\n",
		ArtifactRef: &artifact.Ref{ID: id, CharCount: 2000},
	}
	got := FormatForDirector("delegate_repo", r)

	if !strings.Contains(got, "artifact: "+id) {
		t.Errorf("FormatForDirector should contain artifact meta line with id %q, got:\n%s", id, got)
	}
	wantInstr := `Call read_artifact(id="` + id + `")`
	if !strings.Contains(got, wantInstr) {
		t.Errorf("FormatForDirector should contain %q, got:\n%s", wantInstr, got)
	}
	if !strings.HasPrefix(got, "[Sub-Agent Result: delegate_repo]\n") {
		t.Errorf("FormatForDirector should keep [Sub-Agent Result: tool] header, got:\n%s", got)
	}
}

// TestFinalizeShortTextRegression 短文本路径回归保护：ArtifactRef == nil 且
// Summary == Text == 全文（FinalizeResult 与 FinalizeResultFull 两变体一致）。
func TestFinalizeShortTextRegression(t *testing.T) {
	setArtifactRoot(t)
	// 400 ascii = 100 token <= 500。
	text := strings.Repeat("a", 400)

	got := FinalizeResult("repo_agent", "task", newTestExecutor(text))
	if got.ArtifactRef != nil {
		t.Errorf("FinalizeResult: ArtifactRef = %+v, want nil (within budget)", got.ArtifactRef)
	}
	if got.Summary != text || got.Text != text {
		t.Error("FinalizeResult: short text Summary/Text should equal full text")
	}

	gotFull := FinalizeResultFull("repo_agent", "task", newTestExecutor(text))
	if gotFull.ArtifactRef != nil {
		t.Errorf("FinalizeResultFull: ArtifactRef = %+v, want nil (within budget)", gotFull.ArtifactRef)
	}
	if gotFull.Summary != text || gotFull.Text != text {
		t.Error("FinalizeResultFull: short text Summary/Text should equal full text")
	}
}
