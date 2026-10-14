package agents

import (
	"strings"
	"testing"

	"yagent/internal/artifact"
)

// 本文件为"委派返回物分级"第二批次接线断言：
//   - FinalizeResultFull（Full 变体）：超预算路径 ArtifactRef 非 nil、Summary 含
//     分级标记、Text 恒为完整输出（程序化消费保护）；
//   - FinalizeResultFull 的预算内路径：不落盘、Text=Summary=全文。
//
// FormatForDirector 的完整输出格式由 result_test.go 的
// TestFormatForDirectorWithRef 逐行钉住，本文件不再重复子串断言。

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

// TestFinalizeResultFullShortText Full 变体的预算内路径：不落盘、Text=Summary=全文。
// （短文本的 FinalizeResult 侧由 result_test.go 的 TestFinalizeResultShortText 钉住，
// 这里只补 Full 变体——TestFinalizeResultFullLongText 走的是超预算路径，覆盖不到这条分支。）
func TestFinalizeResultFullShortText(t *testing.T) {
	setArtifactRoot(t)
	// 400 ascii = 100 token <= 500。
	text := strings.Repeat("a", 400)

	gotFull := FinalizeResultFull("repo_agent", "task", newTestExecutor(text))
	if gotFull.ArtifactRef != nil {
		t.Errorf("FinalizeResultFull: ArtifactRef = %+v, want nil (within budget)", gotFull.ArtifactRef)
	}
	if gotFull.Summary != text || gotFull.Text != text {
		t.Error("FinalizeResultFull: short text Summary/Text should equal full text")
	}
}
