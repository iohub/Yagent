package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yagent/internal/agents"
	"yagent/internal/config"
	"yagent/internal/plasma"
)

// 本文件是 `yagent plasma propose` 的正向路径：真子进程（/bin/sh 假 Python，照 P8 的
// 假 Python 形状）+ 默认目录 + --json 形状。错误路径在 cmd_plasma_propose_errors_test.go。
//
// 每个用例都 t.Setenv("HOME", t.TempDir())：plasma 的 store/提案根来自 ~/.yagent，
// 不拥有自己的目录就会读到开发机上的真实产物（坑 23）。

// plasmaProposeStubRunner 是 plasmaPythonRunner 的假实现（错误路径与断言 args 用）。
type plasmaProposeStubRunner struct {
	calls int
	args  [][]string
	raw   []byte
	err   error
}

func (r *plasmaProposeStubRunner) Run(_ context.Context, args ...string) ([]byte, error) {
	r.calls++
	r.args = append(r.args, append([]string(nil), args...))
	return r.raw, r.err
}

// fakePlasmaProposePython 写一个 /bin/sh 脚本冒充 plasma.pyz：
// 解析 --in/--out、把 --in 原文抄到 $FAKE_CAPTURE、把 --in 的路径写到 $FAKE_ARGS、
// 按契约产出两个文件与一份 JSON 信封。它**不**校验内容 —— 校验在 Go 侧做。
func fakePlasmaProposePython(t *testing.T) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "fake-plasma-propose.py")
	body := `#!/bin/sh
set -eu
in=""
out=""
while [ $# -gt 0 ]; do
  case "$1" in
    --in) in="$2"; shift 2 ;;
    --out) out="$2"; shift 2 ;;
    *) shift ;;
  esac
done
[ -n "$in" ] || { echo "fake plasma: missing --in" >&2; exit 2; }
[ -n "$out" ] || { echo "fake plasma: missing --out" >&2; exit 2; }
[ -f "$in" ] || { echo "fake plasma: no such input $in" >&2; exit 3; }
cp "$in" "$FAKE_CAPTURE"
printf '%s\n%s\n' "$in" "$out" > "$FAKE_ARGS"
mkdir -p "$out"
id="coding-fake"
printf '{"id":"%s","agent":"coding","status":"pending","base_hash":"deadbeef","rule_ids":["R5"]}\n' "$id" > "$out/$id.json"
printf '# proposal %s\n' "$id" > "$out/$id.md"
printf '{"id":"%s","agent":"coding","status":"pending","base_hash":"deadbeef","rule_ids":["R5"],"json":"%s/%s.json","md":"%s/%s.md"}\n' "$id" "$out" "$id" "$out" "$id"
`
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatalf("写假 Python 失败: %v", err)
	}
	return script
}

// writeProposeTelemetry 把 lines 写成 telemetry.jsonl（父目录 0700）。
func writeProposeTelemetry(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("建遥测目录失败: %v", err)
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("写遥测账本失败: %v", err)
	}
}

func mustPlasmaTelemetryLine(t *testing.T, rec plasma.TelemetryRecord) string {
	t.Helper()
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("序列化遥测记录失败: %v", err)
	}
	return string(raw)
}

// 正向路径：材料真的递过去了（假 Python 把 --in 抄回来）+ 产物只有 .md/.json。
func TestPlasmaProposeHandsPromptToPython(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("YAGENT_PLASMA", "")

	capture := filepath.Join(t.TempDir(), "captured-input.json")
	argsFile := filepath.Join(t.TempDir(), "captured-args.txt")
	t.Setenv("FAKE_CAPTURE", capture)
	t.Setenv("FAKE_ARGS", argsFile)
	script := fakePlasmaProposePython(t)

	telemetryPath := filepath.Join(home, ".yagent", "agents", "telemetry.jsonl")
	writeProposeTelemetry(t, telemetryPath, []string{
		mustPlasmaTelemetryLine(t, plasma.TelemetryRecord{
			Agent: "coding", StopReason: "max_steps", Steps: 30,
		}),
		mustPlasmaTelemetryLine(t, plasma.TelemetryRecord{
			Agent: "coding", StopReason: "finished", Steps: 10, Escalated: true, Err: "provider timeout",
		}),
		mustPlasmaTelemetryLine(t, plasma.TelemetryRecord{
			Agent: "repo", StopReason: "finished", Steps: 99,
		}),
		"这不是 JSON",
	})

	outDir := filepath.Join(t.TempDir(), "proposals")
	var notice bytes.Buffer
	result, err := runPlasmaPropose(context.Background(), plasmaProposeOptions{
		Agent:    "coding",
		Out:      outDir,
		Settings: config.DefaultPlasmaConfig(),
		Pyz:      script,
		Runner:   &agents.PlasmaCLI{Python: "/bin/sh", Pkg: script},
		Available: func() (bool, string) {
			return true, ""
		},
		Notice:        &notice,
		TempBase:      t.TempDir(),
		TelemetryPath: telemetryPath,
	})
	if err != nil {
		t.Fatalf("propose 失败: %v\nnotice: %s", err, notice.String())
	}

	// 1) 材料递过去了：--in 的原文被假 Python 抄了回来。
	raw, err := os.ReadFile(capture)
	if err != nil {
		t.Fatalf("假 Python 没有收到 --in（%v）；notice: %s", err, notice.String())
	}
	var input plasmaProposeInput
	if err := json.Unmarshal(raw, &input); err != nil {
		t.Fatalf("--in 不是合法 JSON: %v\n%s", err, raw)
	}
	info, ok := agents.LookupPrompt("coding")
	if !ok {
		t.Fatal("LookupPrompt(coding) 失败")
	}
	if input.Prompt != info.Text {
		t.Errorf("--in 里的 prompt 不是全文：got %d 字节，want %d 字节", len(input.Prompt), len(info.Text))
	}
	if input.PromptHash != plasmaPromptHash(info.Text) {
		t.Errorf("prompt_hash = %q，与 prompt 全文的 sha256 不一致", input.PromptHash)
	}
	if input.PromptPath != info.Path {
		t.Errorf("prompt_path = %q，want %q", input.PromptPath, info.Path)
	}
	if input.SchemaVersion != plasmaProposeSchemaVersion {
		t.Errorf("schema_version = %d，want %d", input.SchemaVersion, plasmaProposeSchemaVersion)
	}
	if len(input.EditableFields) == 0 {
		t.Error("editable_fields 为空：提案产物就没有边界说明")
	}
	if input.Lint == nil {
		t.Error("lint 是 null；空数组与\"忘了填\"必须能区分")
	}
	if input.Telemetry.Window != plasmaProposeTelemetryWindow {
		t.Errorf("telemetry.window = %d，want %d", input.Telemetry.Window, plasmaProposeTelemetryWindow)
	}
	if input.Telemetry.Runs != 2 {
		t.Errorf("telemetry.runs = %d，want 2（repo 样本不该被算进来）", input.Telemetry.Runs)
	}
	if input.Telemetry.Failures != 1 || input.Telemetry.Escalations != 1 {
		t.Errorf("failures/escalations = %d/%d，want 1/1",
			input.Telemetry.Failures, input.Telemetry.Escalations)
	}
	if get := input.Telemetry.StopReasons["max_steps"]; get != 1 {
		t.Errorf("stop_reasons[max_steps] = %d，want 1", get)
	}
	// 坏行计数是 Go 侧的账目（不进输入 JSON），所以看返回结果。
	if result.Skipped != 1 {
		t.Errorf("坏行计数 = %d，want 1", result.Skipped)
	}
	if !strings.Contains(notice.String(), "跳过 1 行") {
		t.Errorf("坏行必须留下可见账目，notice = %q", notice.String())
	}

	// 2) 临时 workdir 已清理（--in 的父目录必须消失）。
	rawArgs, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("假 Python 没有记下 --in 路径: %v", err)
	}
	argLines := strings.Split(strings.TrimSpace(string(rawArgs)), "\n")
	if len(argLines) != 2 {
		t.Fatalf("假 Python 应该记下 --in 与 --out 两个值，实际 %d 行", len(argLines))
	}
	if _, err := os.Stat(filepath.Dir(strings.TrimSpace(argLines[0]))); !os.IsNotExist(err) {
		t.Errorf("临时 workdir 没有被清理：%v", err)
	}

	// 3) 产物只有 .md/.json，且**没有任何 *.prompt.md**（§6.9 的硬保证）。
	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatalf("读提案目录失败: %v", err)
	}
	if len(entries) != 2 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("提案目录里应有恰好 2 个文件，实际 %d 个：%v", len(entries), names)
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".prompt.md") {
			t.Fatalf("提案流程写出了 %s —— 内建 prompt 绝不能被这个命令改写", name)
		}
		if !strings.HasSuffix(name, ".json") && !strings.HasSuffix(name, ".md") {
			t.Fatalf("提案目录出现了第三种产物 %s", name)
		}
	}

	// 4) 提案 JSON 可解析、status 恒 pending；结果字段与信封同源。
	proposalRaw, err := os.ReadFile(filepath.Join(outDir, "coding-fake.json"))
	if err != nil {
		t.Fatalf("提案 JSON 没落盘: %v", err)
	}
	var proposal struct {
		Status   string `json:"status"`
		BaseHash string `json:"base_hash"`
	}
	if err := json.Unmarshal(proposalRaw, &proposal); err != nil {
		t.Fatalf("提案 JSON 不可解析: %v", err)
	}
	if proposal.Status != "pending" {
		t.Errorf("提案 status = %q，want pending", proposal.Status)
	}
	if result.Status != "pending" || result.BaseHash != "deadbeef" {
		t.Errorf("结果 = %+v，与信封不一致", result)
	}
	if result.JSONPath != filepath.Join(outDir, "coding-fake.json") {
		t.Errorf("result.JSONPath = %q", result.JSONPath)
	}

	// 5) 目录权限是 0700 这一**事实**（提案里有 prompt 的 diff）。
	dirInfo, err := os.Stat(outDir)
	if err != nil {
		t.Fatalf("stat 提案目录失败: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("提案目录权限 = %04o，want 0700", perm)
	}
}

// 默认 --out：取 [plasma] proposal_dir（空 ⇒ ~/.yagent/agents/proposals）。
func TestPlasmaProposeDefaultsToConfiguredProposalDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	saved := config.GlobalPlasma()
	if err := config.SetGlobalPlasma(config.PlasmaConfig{Enable: true},
		config.PlasmaPresence{Section: true, Enable: true}); err != nil {
		t.Fatalf("设置 [plasma] 失败: %v", err)
	}
	t.Cleanup(func() {
		_ = config.SetGlobalPlasma(saved, config.PlasmaPresence{Section: true, Enable: true})
	})

	capture := filepath.Join(t.TempDir(), "captured-input.json")
	argsFile := filepath.Join(t.TempDir(), "captured-args.txt")
	t.Setenv("FAKE_CAPTURE", capture)
	t.Setenv("FAKE_ARGS", argsFile)
	script := fakePlasmaProposePython(t)

	result, err := runPlasmaPropose(context.Background(), plasmaProposeOptions{
		Agent:     "coding",
		Out:       "",
		Settings:  config.DefaultPlasmaConfig(),
		Pyz:       script,
		Runner:    &agents.PlasmaCLI{Python: "/bin/sh", Pkg: script},
		Available: func() (bool, string) { return true, "" },
		Notice:    io.Discard,
		TempBase:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("propose 失败: %v", err)
	}
	wantDir := filepath.Join(home, ".yagent", "agents", "proposals")
	if result.JSONPath != filepath.Join(wantDir, "coding-fake.json") {
		t.Errorf("默认 --out 没落到 %s：JSONPath = %q", wantDir, result.JSONPath)
	}
	if _, err := os.Stat(wantDir); err != nil {
		t.Errorf("默认提案目录没有被创建: %v", err)
	}

	// 假 Python 收到的 --out 必须是同一个值（不是在前一步被谁改写过的）。
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("读 args 记录失败: %v", err)
	}
	if !strings.Contains(string(raw), wantDir) {
		t.Errorf("假 Python 收到的 --in 记录里没有默认目录 %s", wantDir)
	}
}

// --json 的形状：数组 + 四个字段（agent/path/base_hash/status）。
func TestPlasmaProposeJSONSummaryShape(t *testing.T) {
	var buf bytes.Buffer
	result := plasmaProposeOutput{
		ID:       "coding-1",
		Agent:    "coding",
		Status:   "pending",
		BaseHash: "abc",
		JSONPath: "/tmp/props/coding-1.json",
		MDPath:   "/tmp/props/coding-1.md",
	}
	if err := renderPlasmaPropose(&buf, result, true); err != nil {
		t.Fatalf("渲染 --json 失败: %v", err)
	}
	var payload []map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatalf("--json 不是 JSON 数组: %v\n%s", err, buf.String())
	}
	if len(payload) != 1 {
		t.Fatalf("摘要数组长度 = %d，want 1", len(payload))
	}
	for _, key := range []string{"agent", "path", "base_hash", "status"} {
		if _, ok := payload[0][key]; !ok {
			t.Errorf("摘要缺少字段 %q：%v", key, payload[0])
		}
	}
	if payload[0]["status"] != "pending" {
		t.Errorf("status = %v，want pending", payload[0]["status"])
	}
}
