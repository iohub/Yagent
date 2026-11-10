package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yagent/internal/config"
	"yagent/internal/plasma"
)

// 本文件是 `yagent plasma propose` 的**失败可见性**与遥测口径：
// Python 不可用必须给修复建议、信封坏了必须点名、坏行/窗口必须计数。
// 每条都对应一次"静默失败"事故 —— 只 slog.Debug 了事的那种不算通过。

// 假 zipapp：Available 判据只看它存不存在，内容无所谓。
func fakeProposePyz(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plasma.pyz")
	if err := os.WriteFile(path, []byte("#!/usr/bin/env python3\n"), 0o700); err != nil {
		t.Fatalf("写假 pyz 失败: %v", err)
	}
	return path
}

// 未知 agent 是**用法错**（exit 2），且文案要列出可用取值。
func TestPlasmaProposeUnknownAgentIsUsageError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runner := &plasmaProposeStubRunner{}
	_, err := runPlasmaPropose(context.Background(), plasmaProposeOptions{
		Agent:     "nope",
		Out:       filepath.Join(t.TempDir(), "props"),
		Settings:  config.DefaultPlasmaConfig(),
		Pyz:       fakeProposePyz(t),
		Runner:    runner,
		Available: func() (bool, string) { return true, "" },
		Notice:    io.Discard,
	})
	if err == nil {
		t.Fatal("未知 agent 没有报错")
	}
	var usage *usageError
	if !errors.As(err, &usage) {
		t.Errorf("未知 agent 应当是用法错（exit 2），得到 %T: %v", err, err)
	}
	for _, want := range []string{"coding", "director"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误文案没有列出可用 agent %q：%v", want, err)
		}
	}
	if runner.calls != 0 {
		t.Errorf("未知 agent 不该启动子进程，实际调用 %d 次", runner.calls)
	}
}

// Python 不可用：返回可读错误（含 python 与修复建议），不 panic、不启动子进程。
func TestPlasmaProposePythonUnavailable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	settings := config.DefaultPlasmaConfig()
	settings.Python = filepath.Join(t.TempDir(), "no-such-python3")
	runner := &plasmaProposeStubRunner{}

	_, err := runPlasmaPropose(context.Background(), plasmaProposeOptions{
		Agent:    "coding",
		Out:      filepath.Join(t.TempDir(), "props"),
		Settings: settings,
		Pyz:      fakeProposePyz(t),
		Runner:   runner,
		// Available 留空：走 ADR-15 的唯一判据（plasmaInterpreter 会给原因）。
		Notice: io.Discard,
	})
	if err == nil {
		t.Fatal("[plasma] python 指向不存在的路径，propose 却成功了")
	}
	for _, want := range []string{"python", "修复", "不可用"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误文案缺少 %q：%v", want, err)
		}
	}
	if runner.calls != 0 {
		t.Errorf("解释器不可用时不该启动子进程，实际调用 %d 次", runner.calls)
	}
}

// 信封坏了 / 指向不存在的文件：都必须是错误，且把可诊断的片段打出来。
func TestPlasmaProposeRejectsBadEnvelope(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		wants []string
	}{
		{
			name:  "空输出",
			raw:   "   \n",
			wants: []string{"stdout 是空的"},
		},
		{
			name:  "不是 JSON",
			raw:   "not a json envelope\n",
			wants: []string{"JSON 信封", "not a json"},
		},
		{
			name:  "缺字段",
			raw:   `{"id":"coding-1","status":"pending","json":"/tmp/x.json"}`,
			wants: []string{`缺少字段 "md"`},
		},
		{
			name: "产物不存在",
			raw: `{"id":"coding-1","agent":"coding","status":"pending","base_hash":"x",` +
				`"json":"/tmp/definitely-not-here.json","md":"/tmp/definitely-not-here.md"}`,
			wants: []string{"提案文件不存在", "definitely-not-here.json"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			runner := &plasmaProposeStubRunner{raw: []byte(tc.raw)}
			_, err := runPlasmaPropose(context.Background(), plasmaProposeOptions{
				Agent:     "coding",
				Out:       filepath.Join(t.TempDir(), "props"),
				Settings:  config.DefaultPlasmaConfig(),
				Pyz:       fakeProposePyz(t),
				Runner:    runner,
				Available: func() (bool, string) { return true, "" },
				Notice:    io.Discard,
			})
			if err == nil {
				t.Fatalf("%s：坏信封被当成成功", tc.name)
			}
			for _, want := range tc.wants {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("%s：错误文案缺少 %q：%v", tc.name, want, err)
				}
			}
			if runner.calls != 1 {
				t.Errorf("%s：应当恰好调用一次子进程，实际 %d 次", tc.name, runner.calls)
			}
		})
	}
}

// 子进程失败必须可见，且保留 stderr 里的原因。
func TestPlasmaProposeSurfacesPythonFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runner := &plasmaProposeStubRunner{err: fmt.Errorf("plasma: 子进程退出码 1（失败）；stderr 尾部：%s",
		"Tell the user to fix it")}
	_, err := runPlasmaPropose(context.Background(), plasmaProposeOptions{
		Agent:     "coding",
		Out:       filepath.Join(t.TempDir(), "props"),
		Settings:  config.DefaultPlasmaConfig(),
		Pyz:       fakeProposePyz(t),
		Runner:    runner,
		Available: func() (bool, string) { return true, "" },
		Notice:    io.Discard,
	})
	if err == nil {
		t.Fatal("子进程失败却被当成成功")
	}
	for _, want := range []string{"执行失败", "Tell the user to fix it"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误文案缺少 %q：%v", want, err)
		}
	}
}

// 遥测口径：窗口取尾部 200 条匹配记录、坏行计数、别的 agent 不泄漏。
func TestPlasmaProposeTelemetryWindowAndSkippedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telemetry.jsonl")
	lines := make([]string, 0, 260)
	// 前 50 条 steps=1，后 200 条 steps=2：窗口取尾部 ⇒ avg 必须是 2（不是 1.8）。
	for i := 0; i < 250; i++ {
		steps := 1
		stopReason := "finished"
		if i >= 50 {
			steps = 2
		}
		if i == 249 {
			stopReason = "max_steps"
		}
		lines = append(lines, mustPlasmaTelemetryLine(t, plasma.TelemetryRecord{
			Agent: "delegate_coding", Steps: steps, StopReason: stopReason,
		}))
	}
	lines = append(lines,
		mustPlasmaTelemetryLine(t, plasma.TelemetryRecord{Agent: "repo", Steps: 500}),
		"",
		"{ 截断的 JSON",
		"[]",
		"42",
	)
	writeProposeTelemetry(t, path, lines)

	summary, err := aggregatePlasmaTelemetry(path, "coding")
	if err != nil {
		t.Fatalf("聚合遥测失败: %v", err)
	}
	if summary.Runs != 200 {
		t.Errorf("runs = %d，want 200（窗口只留最近 200 条匹配样本）", summary.Runs)
	}
	if summary.AvgSteps != 2 {
		t.Errorf("avg_steps = %v，want 2（窗口没生效会把前 50 条 steps=1 算进来）", summary.AvgSteps)
	}
	if summary.StopReasons["max_steps"] != 1 {
		t.Errorf("stop_reasons[max_steps] = %d，want 1", summary.StopReasons["max_steps"])
	}
	// 三条坏行：截断 JSON、null、42（都能 Unmarshal 失败或顶层不是对象）。
	if summary.skipped != 3 {
		t.Errorf("skipped = %d，want 3（坏行必须计数，不许静默跳过）", summary.skipped)
	}
	if summary.missing {
		t.Error("账本存在时 missing 不该为真")
	}
}

// 账本不存在是正常状态（还没跑过生产委派），不是错误。
func TestPlasmaProposeTelemetryMissingLedger(t *testing.T) {
	summary, err := aggregatePlasmaTelemetry(filepath.Join(t.TempDir(), "nope.jsonl"), "coding")
	if err != nil {
		t.Fatalf("缺账本不该报错: %v", err)
	}
	if !summary.missing {
		t.Error("缺账本必须标记 missing（调用方要给人一句可见提示）")
	}
	if summary.Runs != 0 || summary.skipped != 0 {
		t.Errorf("缺账本时不该有样本：runs=%d skipped=%d", summary.Runs, summary.skipped)
	}
	if summary.StopReasons == nil || summary.TopErrors == nil {
		t.Error("缺账本时 map/slice 也要非 nil（否则 JSON 里会出现 null）")
	}
}

// argv 形状：恰 5 个 token，且**没有 --workdir**。
// 这一条钉的是"别把 P8 的 ExecSpawner 用在这里"：它会强制追加 --workdir，
// 而 propose 的 argparse 不认识 ⇒ 每次 exit 2，症状却像"Python 挂了"。
func TestPlasmaProposeArgvHasNoWorkdir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	outDir := filepath.Join(t.TempDir(), "props")
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		t.Fatalf("建提案目录失败: %v", err)
	}
	jsonPath := filepath.Join(outDir, "coding-fake.json")
	mdPath := filepath.Join(outDir, "coding-fake.md")
	if err := os.WriteFile(jsonPath, []byte(`{"status":"pending"}`), 0o600); err != nil {
		t.Fatalf("写假提案 JSON 失败: %v", err)
	}
	if err := os.WriteFile(mdPath, []byte("# proposal\n"), 0o600); err != nil {
		t.Fatalf("写假提案 md 失败: %v", err)
	}
	raw := fmt.Sprintf(`{"id":"coding-fake","agent":"coding","status":"pending",`+
		`"base_hash":"deadbeef","json":%q,"md":%q}`, jsonPath, mdPath)
	runner := &plasmaProposeStubRunner{raw: []byte(raw)}

	result, err := runPlasmaPropose(context.Background(), plasmaProposeOptions{
		// Agent 留空：缺省取 coding（--agent 在 Use 里是可选项）。
		Agent:     "",
		Out:       outDir,
		Settings:  config.DefaultPlasmaConfig(),
		Pyz:       fakeProposePyz(t),
		Runner:    runner,
		Available: func() (bool, string) { return true, "" },
		Notice:    io.Discard,
		TempBase:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("propose 失败: %v", err)
	}
	if result.Agent != plasmaProposeDefaultAgent {
		t.Errorf("--agent 缺省时用了 %q，want %q", result.Agent, plasmaProposeDefaultAgent)
	}
	if len(runner.args) != 1 {
		t.Fatalf("子进程应当恰好启动一次，实际 %d 次", len(runner.args))
	}
	args := runner.args[0]
	if len(args) != 5 {
		t.Fatalf("argv 长度 = %d，want 5：%v（多出来的多半是 --workdir）", len(args), args)
	}
	if args[0] != "propose" || args[1] != "--in" || args[3] != "--out" || args[4] != outDir {
		t.Errorf("argv 形状不对：%v", args)
	}
	if joined := strings.Join(args, " "); strings.Contains(joined, "--workdir") {
		t.Errorf("argv 里出现了 --workdir：%v", args)
	}
	if !filepath.IsAbs(args[2]) {
		t.Errorf("--in 必须是绝对路径（子进程的 cwd 不该参与解析）：%q", args[2])
	}
	if result.JSONPath != jsonPath || result.MDPath != mdPath {
		t.Errorf("相对/绝对路径解析错了：%+v", result)
	}
}
