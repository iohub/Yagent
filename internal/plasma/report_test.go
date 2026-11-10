package plasma_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yagent/internal/plasma"
)

// report.json 是**可变快照**（grade 会升降）：它的形态由 store.go 落盘、由
// `Store.ReadReport` 读回，两侧必须同一个 struct。本文件钉住读回侧的三条：
// 读得出来 / 缺失是哨兵而非"空报告" / 坏 JSON 不 panic。

const reportHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func seedReport(t *testing.T, dir, body string) {
	t.Helper()
	full := filepath.Join(dir, "coding", reportHash)
	if err := os.MkdirAll(full, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(full, "report.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write report.json: %v", err)
	}
}

func TestReadReportRoundTrip(t *testing.T) {
	dir := t.TempDir()
	seedReport(t, dir, `{
  "grade": "provisional",
  "escalation_required": true,
  "diagnostics": [
    {"code":"PLS-501","severity":"error","path":"fixtures.structural[0]","message":"结构夹具失败","note":"交付为空"}
  ],
  "outcomes": [
    {"FixtureID":"f1","Passed":false,"Reason":"交付为空","Steps":3},
    {"FixtureID":"f2","Passed":true,"Steps":1}
  ],
  "future_field": 7
}`)

	rec, err := plasma.NewStore(dir).ReadReport("coding", reportHash)
	if err != nil {
		t.Fatalf("ReadReport: %v", err)
	}
	if rec.Grade != plasma.GradeProvisional {
		t.Errorf("grade = %q, want provisional", rec.Grade)
	}
	if !rec.EscalationRequired {
		t.Error("escalation_required 丢了")
	}
	if len(rec.Outcomes) != 2 {
		t.Fatalf("outcomes = %d 条, want 2", len(rec.Outcomes))
	}
	if fails := rec.Outcomes.Failed(); len(fails) != 1 || fails[0].FixtureID != "f1" {
		t.Errorf("Failed() = %+v, want 只有 f1", fails)
	}
	// 诊断文本只走 Report.Text 一份实现：面板与 CLI 说同一句话。
	text := rec.Text()
	for _, want := range []string{"PLS-501", "结构夹具失败", "交付为空"} {
		if !strings.Contains(text, want) {
			t.Errorf("诊断文本缺 %q：\n%s", want, text)
		}
	}
}

// 缺文件必须是 ErrNoReport 哨兵：调用方据此说"没有快照"，
// 而不是把一次读失败说成"编译没发射诊断"（两种情况的修法完全不同）。
func TestReadReportMissingIsSentinel(t *testing.T) {
	_, err := plasma.NewStore(t.TempDir()).ReadReport("coding", reportHash)
	if !errors.Is(err, plasma.ErrNoReport) {
		t.Fatalf("err = %v, want ErrNoReport", err)
	}
}

// 坏 JSON 要报错而不是返回空报告：空报告会被渲染成"没有诊断"，把磁盘被改过
// 这件事整段藏起来。
func TestReadReportMalformed(t *testing.T) {
	dir := t.TempDir()
	seedReport(t, dir, `{"grade": `)
	rec, err := plasma.NewStore(dir).ReadReport("coding", reportHash)
	if err == nil {
		t.Fatalf("坏 JSON 居然读成功了：%+v", rec)
	}
	if errors.Is(err, plasma.ErrNoReport) {
		t.Errorf("坏 JSON 被报成「不存在」：%v", err)
	}
	if rec != nil {
		t.Errorf("失败时不该返回记录：%+v", rec)
	}
}
