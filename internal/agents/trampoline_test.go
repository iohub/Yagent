package agents

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"yagent/internal/llm"
	"yagent/internal/plasma"
)

// 这组用例用**真 /bin/sh + 假 Python 脚本**打通 trampoline（设计稿 §8.1 的 T8/T9
// 与 ADR-14 的补偿控制）：不依赖真 zipapp（P10 才产），也不需要 PATH 上有 python3。
//
// 每个用例都能死在一条具体契约上：
//   - 序号对齐（llm_req_000 ↔ llm_resp_000）；
//   - 编译失败 → diagnostics.json → --fix 重生成；
//   - kill 之后超时生效且 workdir 被清理；
//   - max_llm_calls 到上限就停（不是无限采样）；
//   - ai_chunk 来自 Go 侧（StreamHandler 在驱动器的 LLM 调用里构造）。

// shellWorkdirPreamble 按契约取 `--workdir` 的右值。
// 位置无关（不写死第几个参数）：argv 顺序是 `generate [--resume] [--fix] --workdir W`，
// 而假脚本要能在三种模式下复用同一段解析。
const shellWorkdirPreamble = `
WD=""
prev=""
for a in "$@"; do
  if [ "$prev" = "--workdir" ]; then WD="$a"; fi
  prev="$a"
done
if [ -z "$WD" ]; then
  echo "fake plasma: missing --workdir" >&2
  exit 2
fi
`

const validMinimalSpec = `{"agent_name":"Demo Agent","agent_design":"你是 Demo。","tools_used":["read_file"],"task_for_agent":"读一读"}`

func writeFakePython(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "plasma.pyz")
	script := "#!/bin/sh\n" + shellWorkdirPreamble + body
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatalf("写假 Python: %v", err)
	}
	// 0700：ExecSpawner 直接 exec 它，没有可执行位就是"启动失败"分支。
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatalf("chmod 假 Python: %v", err)
	}
	return path
}

func assertNoWorkdirs(t *testing.T, base string) {
	t.Helper()
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("读 GCBase: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "plasma-") {
			t.Fatalf("workdir 泄漏（defer 清理或错误路径没覆盖）: %s", e.Name())
		}
	}
}

// fakeTrampolineEngine 是测试内的 llm.Engine：**主动**调用 StreamHandler，
// 用来证明 ai_chunk 由 Go 侧产生（真 provider 的行为，scripted.Engine 不调 handler）。
type fakeTrampolineEngine struct {
	mu    sync.Mutex
	calls int
	reply *llm.Response
	err   error
}

func (e *fakeTrampolineEngine) GenerateContent(ctx context.Context, _ []llm.Message, _ []llm.ToolDef, opts *llm.CallOptions) (*llm.Response, error) {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()
	if opts != nil && opts.StreamHandler != nil {
		for _, chunk := range []string{`{"agent`, `_name":"Demo`, ` Agent"}`} {
			if err := opts.StreamHandler(ctx, []byte(chunk)); err != nil {
				return nil, err
			}
		}
	}
	if e.err != nil {
		return nil, e.err
	}
	if e.reply != nil {
		return e.reply, nil
	}
	return &llm.Response{
		Choices: []llm.Choice{{Content: validMinimalSpec}},
		Usage:   &llm.TokenUsage{PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18, TotalInputTokens: 11},
	}, nil
}

func (e *fakeTrampolineEngine) Model() string         { return "fake-trampoline" }
func (e *fakeTrampolineEngine) CloseIdleConnections() {}
func (e *fakeTrampolineEngine) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

// recordingBus 记录事件类型与 payload，用来断言流式来自 Go 侧。
type recordingBus struct {
	mu       sync.Mutex
	types    []string
	payloads []interface{}
	metadata []map[string]interface{}
	froms    []string
}

func (b *recordingBus) Publish(eventType string, content interface{}, from string) error {
	return b.PublishWithMetadata(eventType, content, from, nil)
}

func (b *recordingBus) PublishWithMetadata(eventType string, content interface{}, from string, metadata map[string]interface{}) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.types = append(b.types, eventType)
	b.payloads = append(b.payloads, content)
	b.metadata = append(b.metadata, metadata)
	b.froms = append(b.froms, from)
	return nil
}

func (b *recordingBus) snapshot() ([]string, []interface{}, []map[string]interface{}, []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.types...), append([]interface{}(nil), b.payloads...),
		append([]map[string]interface{}(nil), b.metadata...), append([]string(nil), b.froms...)
}

func newTestTrampoline(t *testing.T, script string, engine llm.Engine, bus EventBus) (*PlasmaTrampoline, string) {
	t.Helper()
	base := t.TempDir()
	compiler := &plasma.Compiler{Palette: plasma.PlasmaPalette}
	tr := &PlasmaTrampoline{
		Spawner:      &ExecSpawner{Python: "/bin/sh", Pkg: script, KillGrace: 200 * time.Millisecond},
		LLM:          engine,
		Publisher:    bus,
		Manifest:     compiler.Manifest(),
		Compiler:     compiler,
		Elaborate:    func(raw []byte) (*plasma.Bundle, error) { return plasma.Elaborate(raw, plasma.DefaultDefaults()) },
		MaxLLMCalls:  3,
		RetryCount:   2,
		SpawnTimeout: 5 * time.Second,
		GCBase:       base,
	}
	return tr, base
}

// TestTrampoline_SamplingThenRespawnSequence 覆盖 ADR-14 的主路径：
// exit 4 → Go 采样 → 写 llm_resp_000.json → respawn（--resume）→ exit 0 → 编译通过。
func TestTrampoline_SamplingThenRespawnSequence(t *testing.T) {
	script := writeFakePython(t, `
if [ -f "$WD/llm_resp_000.json" ]; then
  # 第二次运行必须看到序号对齐的那份响应；看不到就说明 Go 侧写错了名字。
  printf '%s' '`+validMinimalSpec+`' > "$WD/bundle.json"
  exit 0
fi
# 第一次运行：先证明"序号从 0 开始"（000 而不是 001），再请求采样。
if [ -f "$WD/llm_req_000.json" ]; then
  echo "llm_req_000.json 不该由 Python 预先存在" >&2
  exit 1
fi
printf '%s' '{"messages":[{"role":"user","content":"请生成"}]}' > "$WD/llm_req_000.json"
exit 4
`)
	engine := &fakeTrampolineEngine{}
	tr, base := newTestTrampoline(t, script, engine, &recordingBus{})
	res, err := tr.Generate(context.Background(), "设计一个 Demo Agent", "上下文")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if res.LLMCalls != 1 {
		t.Fatalf("LLMCalls = %d，期望 1（一次采样）", res.LLMCalls)
	}
	if res.Attempts != 1 {
		t.Fatalf("Attempts = %d，期望 1（首轮就编译通过）", res.Attempts)
	}
	if res.Bundle == nil || res.Bundle.Name != "demo_agent" {
		t.Fatalf("Bundle 名字不对: %+v", res.Bundle)
	}
	if res.Report == nil || res.Report.HasErrors() {
		t.Fatalf("最终还是报了 error: %+v", res.Report)
	}
	if engine.callCount() != 1 {
		t.Fatalf("LLM 被调 %d 次，期望 1", engine.callCount())
	}
	assertNoWorkdirs(t, base)
}

// TestTrampoline_CompileFailureWritesDiagnosticsAndFixes 覆盖 ADR-11：
// 一轮幻觉工具名 → diagnostics.json（含 PLS-101）→ --fix 重生成 → 合法 bundle。
func TestTrampoline_CompileFailureWritesDiagnosticsAndFixes(t *testing.T) {
	script := writeFakePython(t, `
if [ -f "$WD/diagnostics.json" ]; then
  # 模式是契约的一部分：修一轮必须带 --fix（否则 Python 会重头生成同一份坏 bundle）。
  case " $* " in
    *" --fix "*) ;;
    *) echo "第二轮 argv 缺 --fix: $*" >&2; exit 1 ;;
  esac
  if ! grep -q 'PLS-101' "$WD/diagnostics.json"; then
    echo "diagnostics.json 不含 PLS-101: $(cat "$WD/diagnostics.json")" >&2
    exit 1
  fi
  # --fix 轮必须能看见 diagnostics.json；修复后的名字要真的合法。
  printf '%s' '{"agent_name":"Fixed Agent","agent_design":"修好了。","tools_used":["read_file"],"task_for_agent":"x"}' > "$WD/bundle.json"
  exit 0
fi
# 首轮不该带 --resume/--fix。
case " $* " in
  *" --resume "*|*" --fix "*)
    echo "首轮 argv 不该带 --resume/--fix: $*" >&2
    exit 1 ;;
esac
printf '%s' '{"agent_name":"Broken Agent","agent_design":"坏工具名。","tools_used":["read_filez"],"task_for_agent":"x"}' > "$WD/bundle.json"
exit 0
`)
	engine := &fakeTrampolineEngine{}
	tr, base := newTestTrampoline(t, script, engine, &recordingBus{})
	res, err := tr.Generate(context.Background(), "生成一个会犯错的 Agent", "")
	if err != nil {
		t.Fatalf("Generate（第二轮应修复成功）: %v", err)
	}
	if res.Attempts != 2 {
		t.Fatalf("Attempts = %d，期望 2（首轮编译失败，第二轮 --fix 成功）", res.Attempts)
	}
	if res.Bundle.Name != "fixed_agent" {
		t.Fatalf("最终 Bundle = %q，期望 fixed_agent", res.Bundle.Name)
	}
	if res.Report.HasErrors() {
		t.Fatalf("最终报告不该有 error: %v", res.Report.Text())
	}
	// 这条用例不请求 LLM：diagnostics → --fix 是"编译期反馈"，不是采样。
	if engine.callCount() != 0 {
		t.Fatalf("LLM 被调 %d 次，期望 0", engine.callCount())
	}
	assertNoWorkdirs(t, base)
}

// TestTrampoline_TimeoutKillsAndCleansWorkdir 是 ADR-14 的补偿控制原文：
// "kill Python 后 Go 超时生效且 workdir 被清理"（§1.3 第 10 条）。
func TestTrampoline_TimeoutKillsAndCleansWorkdir(t *testing.T) {
	script := writeFakePython(t, "exec sleep 30\n")
	tr, base := newTestTrampoline(t, script, &fakeTrampolineEngine{}, &recordingBus{})
	tr.SpawnTimeout = 300 * time.Millisecond
	tr.Spawner = &ExecSpawner{Python: "/bin/sh", Pkg: script, KillGrace: 200 * time.Millisecond}

	start := time.Now()
	_, err := tr.Generate(context.Background(), "会挂住的生成", "")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("超时必须返回错误，而不是当成一次空产物")
	}
	var spawnErr *plasma.SpawnError
	if !errors.As(err, &spawnErr) || spawnErr.Code != plasma.ExitTimeout {
		t.Fatalf("超时错误应带 ExitTimeout，得到 %v", err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("超时看门狗没生效：耗时 %v（SpawnTimeout=300ms）", elapsed)
	}
	assertNoWorkdirs(t, base)
}

// TestTrampoline_MaxLLMCallsStopsRunaway 是 T9：Python 永远 exit 4 时必须停在上限。
func TestTrampoline_MaxLLMCallsStopsRunaway(t *testing.T) {
	script := writeFakePython(t, `
N=$(cat "$WD/calls" 2>/dev/null || echo 0)
echo $((N+1)) > "$WD/calls"
REQ=$(printf 'llm_req_%03d.json' "$N")
printf '%s' '{"messages":[{"role":"user","content":"again"}]}' > "$WD/$REQ"
exit 4
`)
	engine := &fakeTrampolineEngine{}
	tr, base := newTestTrampoline(t, script, engine, &recordingBus{})
	tr.MaxLLMCalls = 3

	done := make(chan error, 1)
	go func() { _, err := tr.Generate(context.Background(), "永不收敛的生成", ""); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("无限 exit 4 必须报错")
		}
		if !strings.Contains(err.Error(), "上限") {
			t.Fatalf("错误应点名上限，得到: %v", err)
		}
		if !strings.Contains(err.Error(), "3") {
			t.Fatalf("错误应带次数，得到: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("用例超时：trampoline 没有在上限处停下（死循环）")
	}
	if engine.callCount() != 3 {
		t.Fatalf("LLM 被调 %d 次，期望恰好 3（max_llm_calls）", engine.callCount())
	}
	assertNoWorkdirs(t, base)
}

// TestTrampoline_StreamingComesFromGoSide 是 ADR-14 断言④：
// StreamHandler 在 Go 侧构造，所以 ai_chunk 进的是父进程 dispatcher。
func TestTrampoline_StreamingComesFromGoSide(t *testing.T) {
	script := writeFakePython(t, `
if [ -f "$WD/llm_resp_000.json" ]; then
  printf '%s' '`+validMinimalSpec+`' > "$WD/bundle.json"
  exit 0
fi
printf '%s' '{"messages":[{"role":"user","content":"请生成"}]}' > "$WD/llm_req_000.json"
exit 4
`)
	bus := &recordingBus{}
	tr, _ := newTestTrampoline(t, script, &fakeTrampolineEngine{}, bus)
	if _, err := tr.Generate(context.Background(), "流式生成", ""); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	types, payloads, metadata, froms := bus.snapshot()
	start, chunk, end := -1, -1, -1
	chunks := 0
	for i, typ := range types {
		switch typ {
		case "ai_stream_start":
			if start < 0 {
				start = i
			}
		case "ai_chunk":
			if chunk < 0 {
				chunk = i
			}
			chunks++
		case "ai_stream_end":
			if end < 0 {
				end = i
			}
			if metadata[i] == nil || metadata[i]["usage"] == nil {
				t.Fatalf("ai_stream_end 必须带 usage（成本面板靠它）: %+v", metadata[i])
			}
		}
	}
	if chunks == 0 {
		t.Fatal("没有 ai_chunk：StreamHandler 没在 Go 侧构造，TUI 会全程空白")
	}
	if !(start >= 0 && start < chunk && chunk < end) {
		t.Fatalf("事件顺序应为 start(%d) < chunk(%d) < end(%d)，实际: %v", start, chunk, end, types)
	}
	for i, from := range froms {
		if from != metaTrampolineAgent {
			t.Fatalf("事件 %d（%s）的 from = %q，期望 %q", i, types[i], from, metaTrampolineAgent)
		}
	}
	// 逐块内容拼起来就是引擎写进去的那三块（证明 chunk 没被截断/改写）。
	var joined strings.Builder
	for i, typ := range types {
		if typ != "ai_chunk" {
			continue
		}
		payload, ok := payloads[i].(map[string]interface{})
		if !ok {
			t.Fatalf("ai_chunk payload 不是 map: %T", payloads[i])
		}
		joined.WriteString(payload["content"].(string))
	}
	if joined.String() != `{"agent_name":"Demo Agent"}` {
		t.Fatalf("chunk 拼接 = %q", joined.String())
	}
}

// TestTrampoline_MissingDependencyNamesIt 覆盖装配错误：缺哪一件必须点名。
func TestTrampoline_MissingDependencyNamesIt(t *testing.T) {
	base := t.TempDir()
	compiler := &plasma.Compiler{Palette: plasma.PlasmaPalette}
	cases := []struct {
		name string
		mut  func(*PlasmaTrampoline)
		want string
	}{
		{"spawner", func(t *PlasmaTrampoline) { t.Spawner = nil }, "Spawner"},
		{"llm", func(t *PlasmaTrampoline) { t.LLM = nil }, "LLM"},
		{"manifest", func(t *PlasmaTrampoline) { t.Manifest = nil }, "Manifest"},
		{"compiler", func(t *PlasmaTrampoline) { t.Compiler = nil }, "Compiler"},
		{"elaborate", func(t *PlasmaTrampoline) { t.Elaborate = nil }, "Elaborate"},
		{"max_llm_calls", func(t *PlasmaTrampoline) { t.MaxLLMCalls = 0 }, "max_llm_calls"},
		{"retry_count", func(t *PlasmaTrampoline) { t.RetryCount = 0 }, "meta_retry_count"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := &PlasmaTrampoline{
				Spawner:     &ExecSpawner{Python: "/bin/sh", Pkg: "/nonexistent"},
				LLM:         &fakeTrampolineEngine{},
				Manifest:    compiler.Manifest(),
				Compiler:    compiler,
				Elaborate:   func(raw []byte) (*plasma.Bundle, error) { return plasma.Elaborate(raw, plasma.DefaultDefaults()) },
				MaxLLMCalls: 1,
				RetryCount:  1,
				GCBase:      base,
			}
			tc.mut(tr)
			_, err := tr.Generate(context.Background(), "x", "")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("缺 %s 时必须点名，得到: %v", tc.name, err)
			}
		})
	}
}

// TestTrampoline_ElaborateDiagBecomesDiagnosticsFile：形态错误也要写成一份
// Python 能读的诊断，而不是直接终止（否则 --fix 永远看不到原因）。
func TestTrampoline_ElaborateDiagBecomesDiagnosticsFile(t *testing.T) {
	script := writeFakePython(t, `
if [ -f "$WD/diagnostics.json" ]; then
  if ! grep -q 'PLS-000' "$WD/diagnostics.json"; then
    echo "形态错误的诊断码应是 PLS-000: $(cat "$WD/diagnostics.json")" >&2
    exit 1
  fi
  printf '%s' '`+validMinimalSpec+`' > "$WD/bundle.json"
  exit 0
fi
printf '%s' 'not-json' > "$WD/bundle.json"
exit 0
`)
	tr, base := newTestTrampoline(t, script, &fakeTrampolineEngine{}, &recordingBus{})
	res, err := tr.Generate(context.Background(), "先写坏 JSON", "")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if res.Attempts != 2 {
		t.Fatalf("Attempts = %d，期望 2", res.Attempts)
	}
	assertNoWorkdirs(t, base)
}

// ── ExecSpawner（真实子进程）──

func TestExecSpawner_ExitCodeAndStderr(t *testing.T) {
	script := writeFakePython(t, "echo boom >&2\nexit 7\n")
	s := &ExecSpawner{Python: "/bin/sh", Pkg: script, KillGrace: 200 * time.Millisecond}
	code, err := s.Spawn(context.Background(), t.TempDir(), []string{"generate"})
	if code != 7 {
		t.Fatalf("exit code = %d，期望 7", code)
	}
	var spawnErr *plasma.SpawnError
	if !errors.As(err, &spawnErr) {
		t.Fatalf("非 0 退出必须带 *plasma.SpawnError，得到 %v", err)
	}
	if !strings.Contains(spawnErr.Stderr, "boom") {
		t.Fatalf("SpawnError 没带 stderr: %+v", spawnErr)
	}
}

func TestExecSpawner_SamplingExitIsNotAnError(t *testing.T) {
	script := writeFakePython(t, "exit 4\n")
	s := &ExecSpawner{Python: "/bin/sh", Pkg: script, KillGrace: 200 * time.Millisecond}
	code, err := s.Spawn(context.Background(), t.TempDir(), []string{"generate"})
	if code != plasma.ExitSampling {
		t.Fatalf("exit code = %d，期望 %d", code, plasma.ExitSampling)
	}
	if err != nil {
		t.Fatalf("exit 4 是正常信号，不该带 error: %v", err)
	}
}

func TestExecSpawner_StderrIsBounded(t *testing.T) {
	script := writeFakePython(t, `
i=0
while [ $i -lt 5000 ]; do printf 'x' >&2; i=$((i+1)); done
printf 'TAIL-MARK' >&2
exit 1
`)
	s := &ExecSpawner{Python: "/bin/sh", Pkg: script, KillGrace: 200 * time.Millisecond}
	_, err := s.Spawn(context.Background(), t.TempDir(), []string{"generate"})
	var spawnErr *plasma.SpawnError
	if !errors.As(err, &spawnErr) {
		t.Fatalf("期望 *plasma.SpawnError，得到 %v", err)
	}
	if len(spawnErr.Stderr) > plasma.SpawnStderrLimit+64 {
		t.Fatalf("stderr 没被截断（%d 字节）—— 会撑爆 tool 结果", len(spawnErr.Stderr))
	}
	if !strings.Contains(spawnErr.Stderr, "TAIL-MARK") {
		t.Fatalf("截断应保留尾部: %q", spawnErr.Stderr)
	}
}

func TestExecSpawner_TimeoutReportsExitTimeout(t *testing.T) {
	script := writeFakePython(t, "exec sleep 30\n")
	s := &ExecSpawner{Python: "/bin/sh", Pkg: script, KillGrace: 100 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	code, err := s.Spawn(ctx, t.TempDir(), []string{"generate"})
	if code != plasma.ExitTimeout {
		t.Fatalf("exit code = %d，期望 ExitTimeout(%d)", code, plasma.ExitTimeout)
	}
	if err == nil {
		t.Fatal("超时必须返回 error（调用方靠它区分业务失败）")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("WaitDelay 没生效：耗时 %v", elapsed)
	}
}

func TestExecSpawner_MissingPkgFailsFast(t *testing.T) {
	s := &ExecSpawner{Python: "/bin/sh"}
	if _, err := s.Spawn(context.Background(), t.TempDir(), []string{"generate"}); err == nil {
		t.Fatal("缺 zipapp 路径必须报错，而不是猜一个默认位置")
	}
}

func TestExecSpawner_ArgvShape(t *testing.T) {
	// 脚本把 argv 原样输出到 stderr；exit 7 让 ExecSpawner 把它带回 error。
	script := writeFakePython(t, "echo \"ARGS:$*\" >&2\nexit 7\n")
	dir := t.TempDir()
	s := &ExecSpawner{Python: "/bin/sh", Pkg: script, KillGrace: 200 * time.Millisecond}
	_, err := s.Spawn(context.Background(), dir, []string{"generate", "--resume"})
	var spawnErr *plasma.SpawnError
	if !errors.As(err, &spawnErr) {
		t.Fatalf("期望 *plasma.SpawnError，得到 %v", err)
	}
	want := "generate --resume --workdir " + dir
	if !strings.Contains(spawnErr.Stderr, want) {
		t.Fatalf("argv 形状不对，期望含 %q，得到 %q", want, spawnErr.Stderr)
	}
}

// ── tailBuffer ──

func TestTailBuffer_KeepsTailAndMarksTruncation(t *testing.T) {
	b := newTailBuffer(8)
	for _, part := range []string{"aaaa", "bbbb", "cccc"} {
		if _, err := b.Write([]byte(part)); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	got := string(b.Bytes())
	if !strings.Contains(got, "(truncated)") {
		t.Fatalf("超限后必须标记截断: %q", got)
	}
	if !strings.HasSuffix(got, "bbbbcccc") {
		t.Fatalf("应保留最后 8 字节，得到 %q", got)
	}
}

func TestTailBuffer_WriteCountIsAlwaysFullLength(t *testing.T) {
	// io.Writer 契约：返回的 n 必须是 len(p)，否则 os/exec 的 copy 会报短写。
	b := newTailBuffer(4)
	p := []byte("0123456789")
	n, err := b.Write(p)
	if err != nil || n != len(p) {
		t.Fatalf("Write 返回 (%d,%v)，期望 (%d,nil)", n, err, len(p))
	}
	if got := string(b.Bytes()); !strings.HasSuffix(got, "6789") {
		t.Fatalf("尾部不对: %q", got)
	}
}
