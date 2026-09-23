package timeline

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codeactor/internal/tokenutil"
)

// mustRecorder 创建测试用 Recorder，失败时立即终止测试；测试结束后自动关闭。
func mustRecorder(t *testing.T, rootDir, taskID string) *Recorder {
	t.Helper()
	r, err := newRecorderIn(rootDir, taskID)
	if err != nil {
		t.Fatalf("创建 Recorder 失败: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// mustRecord 记录一条条目，失败时立即终止测试。
func mustRecord(t *testing.T, r *Recorder, kind Kind, content string) {
	t.Helper()
	if _, err := r.Record(kind, content); err != nil {
		t.Fatalf("记录条目失败: %v", err)
	}
}

// buildTestEntries 构造 1 条 user_input + 5 条 thought_plan 的时间线条目，
// 每条 thought_plan 内容为重复 2000 字符的中文文本（用于预算测试）。
func buildTestEntries() []Entry {
	now := time.Now()
	planContent := strings.Repeat("思", 2000)
	entries := []Entry{
		{Seq: 1, Kind: KindUserInput, Timestamp: now, Content: "任务：重构上下文压缩"},
	}
	for seq := int64(2); seq <= 6; seq++ {
		entries = append(entries, Entry{Seq: seq, Kind: KindThoughtPlan, Timestamp: now, Content: planContent})
	}
	return entries
}

// TestRecordAndLoadRoundTrip 验证 Record → Close → LoadEntries 可完整还原条目。
func TestRecordAndLoadRoundTrip(t *testing.T) {
	r := mustRecorder(t, t.TempDir(), "task-1")

	mustRecord(t, r, KindUserInput, "帮我修复登录页的崩溃")
	mustRecord(t, r, KindThoughtPlan, "第一步：定位崩溃堆栈")
	mustRecord(t, r, KindThoughtPlan, "第二步：修复空指针并补充回归测试")

	if err := r.Close(); err != nil {
		t.Fatalf("关闭 Recorder 失败: %v", err)
	}

	entries, err := LoadEntries(r.Path())
	if err != nil {
		t.Fatalf("加载时间线条目失败: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("期望 3 条条目，实际 %d 条", len(entries))
	}

	want := []struct {
		seq     int64
		kind    Kind
		content string
	}{
		{1, KindUserInput, "帮我修复登录页的崩溃"},
		{2, KindThoughtPlan, "第一步：定位崩溃堆栈"},
		{3, KindThoughtPlan, "第二步：修复空指针并补充回归测试"},
	}
	for i, w := range want {
		got := entries[i]
		if got.Seq != w.seq || got.Kind != w.kind || got.Content != w.content {
			t.Errorf("条目 %d 不匹配：got (seq=%d, kind=%s, content=%q)，want (seq=%d, kind=%s, content=%q)",
				i, got.Seq, got.Kind, got.Content, w.seq, w.kind, w.content)
		}
	}
}

// TestSnapshotDeepCopy 验证修改 Snapshot 返回的切片不影响 Recorder 内部状态。
func TestSnapshotDeepCopy(t *testing.T) {
	r := mustRecorder(t, t.TempDir(), "task-1")

	mustRecord(t, r, KindUserInput, "原始内容 A")
	mustRecord(t, r, KindThoughtPlan, "原始内容 B")

	snap := r.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("期望快照 2 条，实际 %d 条", len(snap))
	}
	snap[0].Content = "已篡改"
	snap[1].Kind = KindUserInput

	again := r.Snapshot()
	if len(again) != 2 {
		t.Fatalf("期望再次快照 2 条，实际 %d 条", len(again))
	}
	if again[0].Content != "原始内容 A" {
		t.Errorf("修改快照不应影响内部状态：got content=%q, want %q", again[0].Content, "原始内容 A")
	}
	if again[1].Kind != KindThoughtPlan {
		t.Errorf("修改快照不应影响内部状态：got kind=%s, want %s", again[1].Kind, KindThoughtPlan)
	}
}

// TestBuildTimelineInputBudget 验证超预算时逐条丢弃最旧思考计划并保证 token 上限。
func TestBuildTimelineInputBudget(t *testing.T) {
	entries := buildTestEntries()
	out := BuildTimelineInput(entries, 800)

	if got := tokenutil.EstimateTokens(out); got > 800 {
		t.Fatalf("重建输入 token 数超预算：got %d, want <= 800", got)
	}
	for _, want := range []string{
		"[ULTIMATE CONTEXT COMPRESSION]",
		"## 用户原始输入",
		"任务：重构上下文压缩",
		"已丢弃更早的",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("重建输入缺少期望内容 %q", want)
		}
	}
}

// TestBuildTimelineInputNoBudget 验证 maxTokens=0 时输出全量内容。
func TestBuildTimelineInputNoBudget(t *testing.T) {
	entries := buildTestEntries()
	out := BuildTimelineInput(entries, 0)

	if !strings.Contains(out, "任务：重构上下文压缩") {
		t.Errorf("不限制预算时应包含用户原始输入 %q", "任务：重构上下文压缩")
	}
	planContent := strings.Repeat("思", 2000)
	if got := strings.Count(out, planContent); got != 5 {
		t.Errorf("不限制预算时应包含全部 5 条思考计划内容：got %d 次, want 5 次", got)
	}
	for seq := int64(2); seq <= 6; seq++ {
		want := fmt.Sprintf("### [#%d]", seq)
		if !strings.Contains(out, want) {
			t.Errorf("不限制预算时应包含条目标题 %q", want)
		}
	}
}

// TestEmptyTaskIDFallback 验证空 taskID 时目录名回退为 session_ 前缀。
func TestEmptyTaskIDFallback(t *testing.T) {
	r, err := newRecorderIn(t.TempDir(), "")
	if err != nil {
		t.Fatalf("创建 Recorder 失败: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	dirName := filepath.Base(filepath.Dir(r.Path()))
	if !strings.HasPrefix(dirName, "session_") {
		t.Errorf("空 taskID 时目录名应以 session_ 开头：got %q", dirName)
	}
}
