package browserctl

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionRun_InjectsDirEnvAndArgv(t *testing.T) {
	m, fake := newTestManager(t, nil)
	session, err := m.Acquire(context.Background(), "argv")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer session.Release()

	before := fake.callCount()
	if _, err := session.Run(context.Background(), "click", "@e3", "--wait", "3"); err != nil {
		t.Fatalf("run: %v", err)
	}

	call := fake.callAt(before)
	if got := strings.Join(call.Args, " "); got != "click @e3 --wait 3" {
		t.Errorf("argv = %q, want %q", got, "click @e3 --wait 3")
	}
	if call.Dir != session.Dir() {
		t.Errorf("cmd.Dir = %s, want %s (项目工作目录绝不能被 CLI 污染)", call.Dir, session.Dir())
	}
	if !strings.Contains(strings.Join(call.Env, "\n"), "BROWSER_CONTROL_CDP_URL=") {
		t.Error("env must carry the CDP endpoint")
	}
	if call.Timeout <= 0 {
		t.Error("each call must carry a timeout")
	}
	// 工作区目录必须由 Go 侧创建，CLI 不会自己建
	if _, err := os.Stat(filepath.Join(session.Dir(), ".browser-control")); err == nil {
		t.Log("note: CLI creates .browser-control on demand")
	}
}

func TestSessionRun_TruncatesOversizedOutput(t *testing.T) {
	m, fake := newTestManager(t, func(c *Config) { c.MaxOutputBytes = 100 })
	session, err := m.Acquire(context.Background(), "big")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer session.Release()

	fake.handler = func(opts SpawnOpts) (SpawnOutput, error) {
		return okOutput(strings.Repeat("x", 5000)), nil
	}

	res, err := session.Run(context.Background(), "text")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Truncated {
		t.Error("oversized output must be marked truncated")
	}
	if len(res.Stdout) > 200 {
		t.Errorf("stdout not bounded: %d bytes", len(res.Stdout))
	}
	if !strings.Contains(res.Combined(), "截断") {
		t.Errorf("truncation must be visible to the model: %s", res.Combined())
	}
}

func TestSessionRun_AttachesTraceOnFailure(t *testing.T) {
	m, fake := newTestManager(t, nil)
	session, err := m.Acquire(context.Background(), "trace")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer session.Release()

	// 上游失败时会在工作区里落一份 trace
	traceDir := filepath.Join(session.Dir(), ".browser-control", "traces", "1750000000000")
	if err := os.MkdirAll(traceDir, 0o700); err != nil {
		t.Fatalf("mkdir trace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(traceDir, "error.txt"), []byte("not found: @e3"), 0o600); err != nil {
		t.Fatalf("write trace: %v", err)
	}

	fake.handler = func(opts SpawnOpts) (SpawnOutput, error) {
		if len(opts.Args) > 0 && opts.Args[0] == "page-info" {
			return okOutput(`{"url":"https://a.test","title":"t"}`), nil
		}
		return failOutput("not found: @e3"), nil
	}

	res, err := session.Run(context.Background(), "click", "@e3")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.OK() {
		t.Fatal("non-zero exit must not be reported as ok")
	}
	if !strings.Contains(res.Trace, "not found: @e3") {
		t.Errorf("trace excerpt missing: %q", res.Trace)
	}
	if !strings.Contains(res.Combined(), "trace:") {
		t.Errorf("combined output should include the trace: %s", res.Combined())
	}
}

func TestSessionRun_ParentCancelReturnsError(t *testing.T) {
	m, _ := newTestManager(t, nil)
	session, err := m.Acquire(context.Background(), "cancel")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer session.Release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := session.Run(ctx, "snapshot"); err == nil {
		t.Fatal("parent cancellation must surface as an error, not a result")
	}
}

func TestSessionRun_AutoHandlesDialog(t *testing.T) {
	m, fake := newTestManager(t, nil) // DialogPolicy 默认 accept
	session, err := m.Acquire(context.Background(), "dialog")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer session.Release()

	var pageInfoCalls int
	fake.handler = func(opts SpawnOpts) (SpawnOutput, error) {
		if len(opts.Args) == 0 {
			return okOutput(""), nil
		}
		switch opts.Args[0] {
		case "page-info":
			pageInfoCalls++
			if pageInfoCalls == 1 {
				return okOutput(`{"dialog":{"type":"alert","message":"hi"}}`), nil
			}
			return okOutput(`{"url":"https://a.test","title":"after"}`), nil
		case "dialog":
			return okOutput("ok\n"), nil
		default:
			return okOutput("ok\n"), nil
		}
	}

	res, err := session.Run(context.Background(), "page-info")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.OK() {
		t.Fatalf("retry after dialog should succeed: %+v", res)
	}
	if len(res.Notes) == 0 || !strings.Contains(res.Notes[0], "dialog auto-accepted") {
		t.Errorf("auto-handling must leave a trace for the model, notes=%v", res.Notes)
	}
	if !strings.Contains(res.Stdout, "after") {
		t.Errorf("expected the retried output, got %q", res.Stdout)
	}
	if pageInfoCalls != 2 {
		t.Errorf("page-info should be retried exactly once, got %d calls", pageInfoCalls)
	}
}

func TestSessionRun_DialogAskPolicyLeavesPageAlone(t *testing.T) {
	m, fake := newTestManager(t, func(c *Config) { c.DialogPolicy = DialogAsk })
	session, err := m.Acquire(context.Background(), "ask")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer session.Release()

	fake.handler = func(opts SpawnOpts) (SpawnOutput, error) {
		if len(opts.Args) > 0 && opts.Args[0] == "page-info" {
			return okOutput(`{"dialog":{"type":"confirm"}}`), nil
		}
		return okOutput("ok\n"), nil
	}

	res, err := session.Run(context.Background(), "page-info")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	for i := 0; i < fake.callCount(); i++ {
		if args := fake.callAt(i).Args; len(args) > 0 && args[0] == "dialog" {
			t.Fatal("policy=ask must not touch the dialog automatically")
		}
	}
	if !strings.Contains(res.Stdout, "confirm") {
		t.Errorf("dialog payload should be surfaced to the model, got %q", res.Stdout)
	}
}

func TestHasDialogKey(t *testing.T) {
	if !hasDialogKey(`{"dialog":{"type":"alert"}}`) {
		t.Error("dialog payload should be detected")
	}
	if hasDialogKey(`{"url":"https://a.test/dialog-guide"}`) {
		t.Error("unrelated JSON must not be mistaken for a dialog")
	}
	if hasDialogKey("plain text mentioning dialog") {
		t.Error("plain text must not be parsed")
	}
}

func TestResultCombined_OrderingAndNotes(t *testing.T) {
	res := &Result{
		Stdout: "main output\n",
		Stderr: "warning line",
		Trace:  "boom",
		Notes:  []string{"[dialog auto-accepted]"},
	}
	got := res.Combined()

	wantOrder := []string{"main output", "warning line", "trace:\nboom", "[dialog auto-accepted]"}
	last := -1
	for _, w := range wantOrder {
		idx := strings.Index(got, w)
		if idx < 0 {
			t.Fatalf("missing %q in:\n%s", w, got)
		}
		if idx < last {
			t.Errorf("section %q out of order in:\n%s", w, got)
		}
		last = idx
	}
}
