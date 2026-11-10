package plasma_test

import (
	"bytes"
	"strings"
	"testing"

	"yagent/internal/plasma"
)

// 本文件钉住"人读账目"的唯一实现（CLI 与 TUI 共用）。
//
// 为什么这些断言值得写：CLI 早有 `plasma list` / `plasma promote` / `plasma test`，
// TUI 的 `/plasma` 面板读的是同一批函数 —— 一旦有人为了"面板更好看"在 TUI 侧另写一份
// 格式化，两边对同一份 store 会说两种话，而**没有任何一处会报错**。

// 空 store 的那一行文案是 CLI 既有契约（cmd_plasma_test.go 断言过），不许改写成
// "no agents" 之类的同义词：中文文案是给中文界面的一致性，不是随手写的。
func TestFormatEntriesEmptyStore(t *testing.T) {
	var b bytes.Buffer
	if err := plasma.FormatEntries(&b, nil); err != nil {
		t.Fatalf("FormatEntries(nil): %v", err)
	}
	if !strings.Contains(b.String(), "尚无 plasma agent") {
		t.Errorf("空 store 的输出 = %q，want 含「尚无 plasma agent」", b.String())
	}
}

// 表格列：name / 前 12 位 hash / grade / score / runs。
// hash 必须**截断**（全文 64 位会把一行撑爆，面板里也放不下）。
func TestFormatEntriesRowShape(t *testing.T) {
	full := strings.Repeat("ab", 32)
	entries := []plasma.Entry{{
		Name:    "coding",
		Hash:    full,
		Meta:    plasma.Meta{Grade: plasma.GradeVerified, Score: 1.5, Runs: 7},
		Current: true,
	}}
	var b bytes.Buffer
	if err := plasma.FormatEntries(&b, entries); err != nil {
		t.Fatalf("FormatEntries: %v", err)
	}
	got := b.String()
	for _, want := range []string{"NAME", "HASH", "GRADE", "SCORE", "RUNS", "coding", "verified", "1.50", "7"} {
		if !strings.Contains(got, want) {
			t.Errorf("列表里没有 %q：\n%s", want, got)
		}
	}
	if !strings.Contains(got, full[:12]) {
		t.Errorf("列表里没有前 12 位 hash %q：\n%s", full[:12], got)
	}
	if strings.Contains(got, full) {
		t.Errorf("列表里出现了完整 hash（应截断到 12 位）：\n%s", got)
	}
}

func TestShortHash(t *testing.T) {
	long := strings.Repeat("f", 64)
	if got := plasma.ShortHash(long); got != long[:12] {
		t.Errorf("ShortHash(64 位) = %q, want 12 位", got)
	}
	if got := plasma.ShortHash("abc"); got != "abc" {
		t.Errorf("ShortHash(短串) = %q, want 原样", got)
	}
}

// 未升档的原因必须**两半都写**：只说一半会让人去改错的地方。
func TestPromoteBlockedReason(t *testing.T) {
	cases := []struct {
		name         string
		grade        plasma.Grade
		runs, minRun int
		want         []string
	}{
		{"两半都不够", plasma.GradeProvisional, 0, 5, []string{"runs 不足", "0/5", "grade=provisional"}},
		{"只差 runs", plasma.GradeVerified, 2, 5, []string{"runs 不足", "2/5"}},
		{"只差 grade", plasma.GradeProvisional, 9, 5, []string{"grade=provisional"}},
		{"阈值没配", plasma.GradeVerified, 9, 0, []string{"promote_min_runs"}},
	}
	for _, tc := range cases {
		got := plasma.PromoteBlockedReason(tc.grade, tc.runs, tc.minRun)
		for _, want := range tc.want {
			if !strings.Contains(got, want) {
				t.Errorf("%s: PromoteBlockedReason = %q，want 含 %q", tc.name, got, want)
			}
		}
	}
	// 可升档时必须返回空串：返回任何文本都会被 FormatPromote 当成"被拒"。
	if got := plasma.PromoteBlockedReason(plasma.GradeVerified, 5, 5); got != "" {
		t.Errorf("够条件时 = %q，want 空串", got)
	}
}

func TestFormatPromote(t *testing.T) {
	var b bytes.Buffer
	ok := &plasma.PromoteResult{Name: "coding", Hash: strings.Repeat("c", 40),
		Grade: string(plasma.GradePromoted), Runs: 6, MinRuns: 5, Promoted: true}
	if err := plasma.FormatPromote(&b, ok); err != nil {
		t.Fatalf("FormatPromote: %v", err)
	}
	if got := b.String(); !strings.Contains(got, "promoted coding@"+strings.Repeat("c", 12)) {
		t.Errorf("升档输出 = %q，want 含 promoted <name>@<12 位 hash>", got)
	}

	b.Reset()
	blocked := &plasma.PromoteResult{Name: "coding", Hash: strings.Repeat("d", 40),
		Grade: string(plasma.GradeProvisional), Runs: 0, MinRuns: 5}
	if err := plasma.FormatPromote(&b, blocked); err != nil {
		t.Fatalf("FormatPromote: %v", err)
	}
	got := b.String()
	if !strings.Contains(got, "未升档") {
		t.Errorf("未升档输出 = %q，want 含「未升档」", got)
	}
	// CLI 的既有测试只钉了"未升档"三个字；这里补上"原因必须可见"（静默是缺陷）。
	if !strings.Contains(got, "runs 不足") {
		t.Errorf("未升档输出 = %q，want 含具体原因", got)
	}
}

// 一条夹具怎么念只有一份实现：通过的条目不许出现 reason 行，
// 失败的那条必须带 reason（"FAIL" 两个字说明不了该改哪里）。
func TestFormatOutcomes(t *testing.T) {
	out := plasma.Outcomes{
		{FixtureID: "ok", Passed: true, Steps: 2},
		{FixtureID: "bad", Passed: false, Steps: 4, Reason: "交付为空", TokensIn: 10, TokensOut: 5, StopKind: "agent_exit"},
	}
	var b bytes.Buffer
	if err := plasma.FormatOutcomes(&b, out); err != nil {
		t.Fatalf("FormatOutcomes: %v", err)
	}
	got := b.String()
	if !strings.Contains(got, "ok  PASS  steps=2") {
		t.Errorf("通过条目渲染不对：\n%s", got)
	}
	if !strings.Contains(got, "bad  FAIL  steps=4") {
		t.Errorf("失败条目渲染不对：\n%s", got)
	}
	if !strings.Contains(got, "tokens=10/5") || !strings.Contains(got, "stop=agent_exit") {
		t.Errorf("行为夹具的 tokens/stop 丢了：\n%s", got)
	}
	if !strings.Contains(got, "reason: 交付为空") {
		t.Errorf("失败原因没写出来：\n%s", got)
	}
	if strings.Count(got, "reason:") != 1 {
		t.Errorf("通过的条目也打了 reason：\n%s", got)
	}
}
