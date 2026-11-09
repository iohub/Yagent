package plasma_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"yagent/internal/plasma"
	"yagent/internal/toolspec"
)

// 这组用例钉的是**跨语言契约**（设计稿 §6.3）：文件名、序号对齐、0700、
// 原子写、GC。它们全部只依赖 stdlib + plasma，不需要假 Python ——
// 进程侧的行为在 internal/agents/trampoline_test.go 里用真 /bin/sh 验。

func TestNewWorkdir_PermsAndUniqueness(t *testing.T) {
	base := t.TempDir()
	a, err := plasma.NewWorkdir(base)
	if err != nil {
		t.Fatalf("NewWorkdir: %v", err)
	}
	defer func() { _ = a.Cleanup() }()

	info, err := os.Stat(a.Path())
	if err != nil {
		t.Fatalf("stat workdir: %v", err)
	}
	// T8：0700 必须是事实，不是"通常如此"（umask 会削弱 MkdirTemp 的权限）。
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("workdir 权限 = %04o，期望 0700", perm)
	}

	b, err := plasma.NewWorkdir(base)
	if err != nil {
		t.Fatalf("NewWorkdir 第二次: %v", err)
	}
	defer func() { _ = b.Cleanup() }()
	if a.Path() == b.Path() {
		t.Fatalf("两次 NewWorkdir 得到同一目录 %s —— 并发委派会互相窥探（T8）", a.Path())
	}
	if !strings.HasPrefix(filepath.Base(a.Path()), "plasma-") {
		t.Fatalf("目录名 %q 没有 plasma- 前缀，启动期 GC 认不出来", filepath.Base(a.Path()))
	}
}

func TestWorkdir_CleanupIsIdempotent(t *testing.T) {
	w, err := plasma.NewWorkdir(t.TempDir())
	if err != nil {
		t.Fatalf("NewWorkdir: %v", err)
	}
	if err := w.Cleanup(); err != nil {
		t.Fatalf("首次 Cleanup: %v", err)
	}
	if err := w.Cleanup(); err != nil {
		t.Fatalf("第二次 Cleanup 必须幂等（defer 与 GC 可能各删一次）: %v", err)
	}
	if _, err := os.Stat(w.Path()); !os.IsNotExist(err) {
		t.Fatalf("Cleanup 之后目录仍存在: %v", err)
	}
}

func TestWorkdir_ContractFileNames(t *testing.T) {
	w, err := plasma.NewWorkdir(t.TempDir())
	if err != nil {
		t.Fatalf("NewWorkdir: %v", err)
	}
	defer func() { _ = w.Cleanup() }()

	// 序号按下标对齐，三位补零 —— Python 与 Go 各自从 0 数，不扫目录。
	if got, want := filepath.Base(w.LLMReq(0)), "llm_req_000.json"; got != want {
		t.Fatalf("LLMReq(0) = %q，期望 %q", got, want)
	}
	if got, want := filepath.Base(w.LLMResp(12)), "llm_resp_012.json"; got != want {
		t.Fatalf("LLMResp(12) = %q，期望 %q", got, want)
	}
	if got, want := filepath.Base(w.LLMReq(1000)), "llm_req_1000.json"; got != want {
		t.Fatalf("LLMReq(1000) = %q，期望 %q（三位补零是下限，不是截断）", got, want)
	}
	names := map[string]string{
		plasma.FileReq:      "req.json",
		plasma.FileManifest: "manifest.json",
		plasma.FileState:    "state.json",
		plasma.FileDiag:     "diagnostics.json",
		plasma.FileBundle:   "bundle.json",
	}
	for got, want := range names {
		if got != want {
			t.Fatalf("契约文件名漂移：%q != %q（改它必须同时改 Python）", got, want)
		}
	}
}

func TestWorkdir_WriteReqLeavesNoTempFiles(t *testing.T) {
	w, err := plasma.NewWorkdir(t.TempDir())
	if err != nil {
		t.Fatalf("NewWorkdir: %v", err)
	}
	defer func() { _ = w.Cleanup() }()

	if err := w.WriteReq("设计一个审计 Agent", "上下文"); err != nil {
		t.Fatalf("WriteReq: %v", err)
	}
	raw, err := os.ReadFile(w.File(plasma.FileReq))
	if err != nil {
		t.Fatalf("读 req.json: %v", err)
	}
	var req plasma.Request
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("req.json 不是合法 JSON: %v\n%s", err, raw)
	}
	if req.Task != "设计一个审计 Agent" || req.Context != "上下文" {
		t.Fatalf("req.json 往返丢字段: %+v", req)
	}

	// 空 context 必须省略键：Python 侧靠"键在不在"区分"没有上下文"与"空字符串"。
	if err := w.WriteReq("只有任务", ""); err != nil {
		t.Fatalf("WriteReq(空 context): %v", err)
	}
	raw, _ = os.ReadFile(w.File(plasma.FileReq))
	if strings.Contains(string(raw), `"context"`) {
		t.Fatalf("空 context 不该写出 key：\n%s", raw)
	}

	entries, err := os.ReadDir(w.Path())
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("原子写留下了临时文件 %q（读者会看到它）", e.Name())
		}
	}
}

func TestWorkdir_ManifestRoundTripIsReadOnlyContract(t *testing.T) {
	w, err := plasma.NewWorkdir(t.TempDir())
	if err != nil {
		t.Fatalf("NewWorkdir: %v", err)
	}
	defer func() { _ = w.Cleanup() }()

	c := &plasma.Compiler{Palette: plasma.PlasmaPalette}
	m := c.Manifest()
	if err := w.WriteManifest(m); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	raw, err := os.ReadFile(w.File(plasma.FileManifest))
	if err != nil {
		t.Fatalf("读 manifest.json: %v", err)
	}
	var back plasma.Manifest
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("manifest.json 不是合法 JSON: %v", err)
	}
	if back.CapabilityLock != m.CapabilityLock {
		t.Fatalf("capability_lock 往返变了：%q != %q", back.CapabilityLock, m.CapabilityLock)
	}
	if len(back.Codes) == 0 || len(back.Tools) == 0 {
		t.Fatalf("manifest 缺少码表或工具池：codes=%d tools=%d", len(back.Codes), len(back.Tools))
	}
	if err := w.WriteManifest(nil); err == nil {
		t.Fatal("WriteManifest(nil) 必须报错：Python 侧没有清单就会瞎选工具")
	}
}

func TestWorkdir_DiagnosticsRoundTripUsesLowercaseKeys(t *testing.T) {
	w, err := plasma.NewWorkdir(t.TempDir())
	if err != nil {
		t.Fatalf("NewWorkdir: %v", err)
	}
	defer func() { _ = w.Cleanup() }()

	report := &plasma.Report{Diagnostics: []plasma.Diagnostic{{
		Code:     plasma.CodeUnknownTool,
		Severity: plasma.SeverityError,
		Path:     "bundle.tools[0]",
		Message:  "unknown tool",
		Note:     "did you mean",
	}}}
	if err := w.WriteDiagnostics(report); err != nil {
		t.Fatalf("WriteDiagnostics: %v", err)
	}
	raw, err := os.ReadFile(w.DiagnosticsPath())
	if err != nil {
		t.Fatalf("读 diagnostics.json: %v", err)
	}
	// 键名是跨语言契约：没有 json tag 时这里会是 "Severity"/"Message"。
	for _, key := range []string{`"code"`, `"severity"`, `"path"`, `"message"`, `"note"`} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("diagnostics.json 缺少键 %s：\n%s", key, raw)
		}
	}
	back, err := w.ReadDiagnostics()
	if err != nil {
		t.Fatalf("ReadDiagnostics: %v", err)
	}
	if len(back.Diagnostics) != 1 || back.Diagnostics[0].Code != plasma.CodeUnknownTool {
		t.Fatalf("诊断往返丢内容: %+v", back.Diagnostics)
	}
}

func TestWorkdir_BundleRoundTrip(t *testing.T) {
	w, err := plasma.NewWorkdir(t.TempDir())
	if err != nil {
		t.Fatalf("NewWorkdir: %v", err)
	}
	defer func() { _ = w.Cleanup() }()

	body := []byte(`{"agent_name":"Demo Agent","agent_design":"x","tools_used":["read_file"]}`)
	if err := w.WriteBundleRaw(body); err != nil {
		t.Fatalf("WriteBundleRaw: %v", err)
	}
	got, err := w.ReadBundleRaw()
	if err != nil {
		t.Fatalf("ReadBundleRaw: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("bundle 往返变了：%q != %q", got, body)
	}
	if err := w.WriteBundleRaw(nil); err == nil {
		t.Fatal("WriteBundleRaw(nil) 必须报错：空产物会让 Elaborate 报一个误导性的 JSON 错")
	}
	if _, err := w.ReadBundleRaw(); err != nil {
		t.Fatalf("读回失败: %v", err)
	}
}

func TestWorkdir_WriteLLMRespIsAtomic(t *testing.T) {
	w, err := plasma.NewWorkdir(t.TempDir())
	if err != nil {
		t.Fatalf("NewWorkdir: %v", err)
	}
	defer func() { _ = w.Cleanup() }()

	if err := w.WriteLLMResp(3, []byte(`{"choices":[{"content":"hi"}]}`)); err != nil {
		t.Fatalf("WriteLLMResp: %v", err)
	}
	raw, err := os.ReadFile(w.LLMResp(3))
	if err != nil {
		t.Fatalf("读 llm_resp_003.json: %v", err)
	}
	if string(raw) != `{"choices":[{"content":"hi"}]}` {
		t.Fatalf("响应内容被改写：%s", raw)
	}
	if err := w.WriteLLMResp(3, nil); err == nil {
		t.Fatal("空响应必须报错：Python 侧会读到一份空文件并把它当\"模型什么都没说\"")
	}
}

func TestGCWorkdirs_RemovesOnlyExpired(t *testing.T) {
	base := t.TempDir()
	oldDir, err := plasma.NewWorkdir(base)
	if err != nil {
		t.Fatalf("NewWorkdir: %v", err)
	}
	freshDir, err := plasma.NewWorkdir(base)
	if err != nil {
		t.Fatalf("NewWorkdir: %v", err)
	}
	defer func() { _ = freshDir.Cleanup() }()

	// 造一个两小时前的目录：ModTime 是 GC 的唯一判据。
	stamp := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(oldDir.Path(), stamp, stamp); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	// 同一个 base 下的非 plasma 目录绝不能被删。
	foreign := filepath.Join(base, "not-plasma-keep")
	if err := os.MkdirAll(foreign, 0o700); err != nil {
		t.Fatalf("Mkdir foreign: %v", err)
	}
	if err := os.Chtimes(foreign, stamp, stamp); err != nil {
		t.Fatalf("Chtimes foreign: %v", err)
	}

	removed, err := plasma.GCWorkdirs(base, time.Hour, time.Now())
	if err != nil {
		t.Fatalf("GCWorkdirs: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d，期望 1（只删过期的 plasma-*）", removed)
	}
	if _, err := os.Stat(oldDir.Path()); !os.IsNotExist(err) {
		t.Fatalf("过期目录仍在: %v", err)
	}
	if _, err := os.Stat(freshDir.Path()); err != nil {
		t.Fatalf("未过期目录被删了: %v", err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("非 plasma 目录被删了: %v", err)
	}

	// ttl<=0 = 不清理（配置里 0 有语义，归一化不能把它变成"全清"）。
	if n, err := plasma.GCWorkdirs(base, 0, time.Now()); err != nil || n != 0 {
		t.Fatalf("ttl=0 应什么都不做，得到 (%d,%v)", n, err)
	}
}

func TestSpawnError_CarriesCodeAndTruncatedStderr(t *testing.T) {
	long := strings.Repeat("x", plasma.SpawnStderrLimit*3) + "TAIL-MARK"
	err := plasma.NewSpawnError(plasma.ExitFailed, "/tmp/plasma-x", []byte(long))
	msg := err.Error()
	if !strings.Contains(msg, "退出码 1") {
		t.Fatalf("错误信息没有退出码：%s", msg)
	}
	if !strings.Contains(msg, "TAIL-MARK") {
		t.Fatalf("截断应保留尾部：%s", msg)
	}
	if len(msg) > plasma.SpawnStderrLimit+512 {
		t.Fatalf("错误信息过长（%d 字节）—— stderr 没被截断，会撑爆 tool 结果", len(msg))
	}

	var typed *plasma.SpawnError
	if !errors.As(error(err), &typed) {
		t.Fatal("SpawnError 必须能被 errors.As 取回")
	}
	if typed.Code != plasma.ExitFailed {
		t.Fatalf("Code = %d，期望 %d", typed.Code, plasma.ExitFailed)
	}
}

func TestTruncateTail_KeepsValidUTF8(t *testing.T) {
	// 每个汉字 3 字节：limit 落在字符中间时必须回退到边界，否则 Python 会读到
	// 一段非法 UTF-8 并在 json 解析时报一个与真实原因无关的错。
	s := strings.Repeat("诊", 100)
	got := plasma.TruncateTail(s, 100)
	if !utf8.ValidString(got) {
		t.Fatalf("截断产生了非法 UTF-8: %q", got)
	}
	if !strings.Contains(got, "(truncated)") {
		t.Fatalf("截断必须留一行说明：%q", got)
	}
	if plasma.TruncateTail("short", 100) != "short" {
		t.Fatal("未超限时应原样返回")
	}
}

func TestExitCodeNamesAreDistinct(t *testing.T) {
	codes := []int{plasma.ExitOK, plasma.ExitFailed, plasma.ExitUsage, plasma.ExitTimeout, plasma.ExitSampling}
	seen := map[string]int{}
	for _, c := range codes {
		name := plasma.ExitCodeName(c)
		if prev, ok := seen[name]; ok && prev != c {
			t.Fatalf("退出码 %d 与 %d 共用名字 %q —— 错误信息无法区分", prev, c, name)
		}
		seen[name] = c
	}
	if plasma.ExitCodeName(99) == plasma.ExitCodeName(plasma.ExitFailed) {
		t.Fatal("未知退出码不该冒充业务失败")
	}
}

// 编译期确认：PlasmaPalette 能自动追加 agent_exit（MatchNames 的唯一判据）。
func TestPlasmaPaletteAdmitsExitTool(t *testing.T) {
	selected, unknown, rejected := toolspec.MatchNames([]string{"read_file"}, plasma.PlasmaPalette, nil)
	if len(unknown) != 0 || len(rejected) != 0 {
		t.Fatalf("read_file 应被 palette 接受：unknown=%v rejected=%v", unknown, rejected)
	}
	found := false
	for _, s := range selected {
		if s.Name == toolspec.ExitToolName {
			found = true
		}
	}
	if !found {
		t.Fatal("agent_exit 没有被自动追加：trampoline 的编译阶段会报 PLS-202")
	}
}
