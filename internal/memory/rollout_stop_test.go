package memory

// rollout_stop_test.go — 尾读终止事实 + 续跑资格分类。
//
// 夹具全部写入 t.TempDir()：internal/memory 的测试**不像 internal/agents 那样**
// 隔离 $HOME，往 ~/.yagent 写夹具再扫目录的用例在并发下会把候选累加（AGENTS §11 坑 23）。

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func writeRollout(t *testing.T, name string, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func rrAbort(reason, stopKind string, step, maxSteps int) string {
	return rrLine(rrTs5, "event_msg", fmt.Sprintf(
		`{"type":"turn_aborted","reason":%q,"stop_reason":%q,"step_number":%d,"max_steps":%d}`,
		reason, stopKind, step, maxSteps))
}

func rrCheckpoint(stopKind, agent string, step, maxSteps int) string {
	return rrLine(rrTs6, RecordAgentCheckpoint, fmt.Sprintf(
		`{"schema_version":1,"stop_kind":%q,"agent_name":%q,"delegation_id":"call_7`+
			`","step":%d,"max_steps":%d,"message_count":41,"digest":"deadbeef","model":"qwen3-coder"}`,
		stopKind, agent, step, maxSteps))
}

func rrBaseLines() []string {
	return []string{
		rrLine(rrTs1, "session_meta", rrMeta("sess-1", "/tmp/proj", "main")),
		rrLine(rrTs2, "turn_context", rrTurnCtx("qwen3-coder", "/tmp/proj")),
		rrLine(rrTs3, "response_item", rrUserItem("u1", "任务：把 resume 界面重做一遍")),
	}
}

func TestReadRolloutStopFacts_TerminalEventAndCheckpoint(t *testing.T) {
	path := writeRollout(t, "a.jsonl", append(rrBaseLines(),
		rrAbort("user paused at gate", StopKindPaused, 12, 100),
		rrCheckpoint(StopKindPaused, "coding", 37, 150))...)

	facts, err := ReadRolloutStopFacts(path)
	if err != nil {
		t.Fatalf("ReadRolloutStopFacts: %v", err)
	}
	if !facts.Found || !facts.Aborted {
		t.Fatalf("Found=%v Aborted=%v, want both true (%+v)", facts.Found, facts.Aborted, facts)
	}
	if facts.StopReason != StopKindPaused {
		t.Errorf("StopReason = %q, want %q", facts.StopReason, StopKindPaused)
	}
	if facts.AbortReason != "user paused at gate" {
		t.Errorf("AbortReason = %q", facts.AbortReason)
	}
	if facts.Step != 12 || facts.MaxSteps != 100 {
		t.Errorf("step/max = %d/%d, want 12/100", facts.Step, facts.MaxSteps)
	}
	if facts.Checkpoint == nil {
		t.Fatal("Checkpoint = nil, want the agent_checkpoint record")
	}
	if facts.Checkpoint.Step != 37 || facts.Checkpoint.AgentName != "coding" {
		t.Errorf("checkpoint = step %d agent %q, want 37/coding",
			facts.Checkpoint.Step, facts.Checkpoint.AgentName)
	}
	if facts.Checkpoint.Digest != "deadbeef" {
		t.Errorf("checkpoint digest = %q", facts.Checkpoint.Digest)
	}
}

func TestReadRolloutStopFacts_LastCheckpointWins(t *testing.T) {
	// 一次会话可以暂停-继续多轮，每轮各写一条；重放只关心最后一个停点。
	path := writeRollout(t, "b.jsonl", append(rrBaseLines(),
		rrCheckpoint(StopKindPaused, "repo", 5, 50),
		rrAbort("again", StopKindPaused, 9, 100),
		rrCheckpoint(StopKindPaused, "coding", 37, 150))...)

	facts, err := ReadRolloutStopFacts(path)
	if err != nil {
		t.Fatalf("ReadRolloutStopFacts: %v", err)
	}
	if facts.Checkpoint == nil || facts.Checkpoint.Step != 37 {
		t.Fatalf("checkpoint = %+v, want the last one (step 37)", facts.Checkpoint)
	}
}

func TestReadRolloutStopFacts_TaskComplete(t *testing.T) {
	path := writeRollout(t, "c.jsonl", append(rrBaseLines(),
		rrLine(rrTs5, "event_msg", `{"type":"task_complete","step_number":8,"max_steps":100,"stop_reason":"agent_exit"}`))...)

	facts, err := ReadRolloutStopFacts(path)
	if err != nil {
		t.Fatalf("ReadRolloutStopFacts: %v", err)
	}
	if !facts.Found || !facts.Finished || facts.Aborted {
		t.Fatalf("facts = %+v, want Found+Finished, not Aborted", facts)
	}
	if state, _ := facts.State(); state != ResumeFinished {
		t.Errorf("State() = %v, want finished", state)
	}
}

func TestReadRolloutStopFacts_NoTerminalRecordIsNotFoundNotError(t *testing.T) {
	// 崩溃/被硬杀/本次改动之前的存量文件：没有终止记录也不能报错，
	// 否则调用方会把这条会话从清单里滤掉（静默滤掉是禁令）。
	path := writeRollout(t, "d.jsonl", rrBaseLines()...)

	facts, err := ReadRolloutStopFacts(path)
	if err != nil {
		t.Fatalf("ReadRolloutStopFacts returned error for a record-less file: %v", err)
	}
	if facts.Found {
		t.Fatalf("Found = true for %+v, want false", facts)
	}
	state, why := facts.State()
	if state != ResumeUnknown {
		t.Errorf("State() = %v, want unknown", state)
	}
	if why == "" {
		t.Error("unknown state must carry a reason, got empty")
	}
}

func TestReadRolloutStopFacts_WindowsGrowPastOneHugeRecord(t *testing.T) {
	// 末尾是一条巨大的 response_item 时，32 KB 窗口整窗落在它内部 ——
	// 必须放大窗口，而不是判成"没有终止记录"。
	huge := strings.Repeat("x", int(stopTailSmall)+4096)
	lines := append(rrBaseLines(),
		rrLine(rrTs4, "response_item",
			fmt.Sprintf(`{"type":"message","role":"assistant","id":"a1","content":[{"type":"output_text","text":%q}]}`, huge)),
		rrAbort("paused here", StopKindPaused, 12, 100))
	path := writeRollout(t, "e.jsonl", lines...)

	if size, err := os.Stat(path); err == nil && size.Size() <= stopTailSmall {
		t.Fatalf("夹具没有超过第一个窗口（%d 字节），这条用例没触发窗口放大", size.Size())
	}
	facts, err := ReadRolloutStopFacts(path)
	if err != nil {
		t.Fatalf("ReadRolloutStopFacts: %v", err)
	}
	if !facts.Found || facts.StopReason != StopKindPaused {
		t.Fatalf("facts = %+v, want the terminal record found after growing the window", facts)
	}
}

func TestReadRolloutStopFacts_MissingFileErrors(t *testing.T) {
	if _, err := ReadRolloutStopFacts(filepath.Join(t.TempDir(), "nope.jsonl")); err == nil {
		t.Fatal("missing file must error (the caller decides how to degrade)")
	}
}

// ── 分类表 ──

func TestStopFactsState_LegacyAbortedWithoutReasonIsReplayable(t *testing.T) {
	// 存量 turn_aborted 没有 stop_reason：按最保守的"取消"归类，
	// 让用户至少能试着继续，而不是把存量会话一律判成不可恢复。
	f := StopFacts{Found: true, Aborted: true, AbortReason: "context canceled"}
	state, why := f.State()
	if state != ResumeReplayable {
		t.Errorf("State() = %v, want replayable", state)
	}
	if why != "" {
		t.Errorf("replayable must not carry a reason, got %q", why)
	}
}

func TestStopFactsState_EveryKindIsClaimed(t *testing.T) {
	cases := []struct {
		kind  string
		state ResumeState
	}{
		{StopKindPaused, ResumeReplayable},
		{StopKindCancelled, ResumeReplayable},
		{StopKindMaxSteps, ResumeHandoff},
		{StopKindIdleTimeout, ResumeHandoff},
		{StopKindTotalTimeout, ResumeHandoff},
		{StopKindLLMError, ResumeFailed},
		{StopKindCircuitBreakerOpen, ResumeFailed},
		{StopKindPanic, ResumeFailed},
		{StopKindUnaccounted, ResumeHandoff},
		{StopKindPlainText, ResumeFinished},
		{StopKindAgentExit, ResumeFinished},
	}
	for _, c := range cases {
		f := StopFacts{Found: true, Aborted: true, StopReason: c.kind}
		state, why := f.State()
		if state != c.state {
			t.Errorf("kind %q: State() = %v, want %v", c.kind, state, c.state)
		}
		if (c.state == ResumeReplayable) != (why == "") {
			t.Errorf("kind %q: why=%q disagrees with replayability %v", c.kind, why, c.state)
		}
		if strings.HasPrefix(why, "unknown stop reason") {
			t.Errorf("kind %q fell through to the default branch", c.kind)
		}
	}
}

// TestStopFactsState_NoDeclaredKindFallsThrough 是漂移闸门：
// 新增一个 StopKind* 常量而没给分类表加判据时，它必须立刻红。
// （AGENTS §11 坑 15/18：只测"当前表全对"等于没测，漂走的那一边不会报错。）
func TestStopFactsState_NoDeclaredKindFallsThrough(t *testing.T) {
	src, err := os.ReadFile("rollout_types.go")
	if err != nil {
		t.Fatalf("read rollout_types.go: %v", err)
	}
	declared := regexp.MustCompile(`(?m)^\t(StopKind\w+)\s*=\s*"([^"]+)"`).FindAllStringSubmatch(string(src), -1)
	if len(declared) < 10 {
		t.Fatalf("只扫到 %d 个 StopKind 常量，词表正则或源文件变了 —— 这条闸门已经失效", len(declared))
	}
	for _, m := range declared {
		constName, kind := m[1], m[2]
		f := StopFacts{Found: true, Aborted: true, StopReason: kind}
		_, why := f.State()
		if strings.HasPrefix(why, "unknown stop reason") {
			t.Errorf("%s = %q 没有被分类表认领：它会走 default 分支（%q）", constName, kind, why)
		}
	}
}

func TestStopFactsState_UnknownKindDoesNotBecomeReplayable(t *testing.T) {
	// default 分支的存在意义：将来任何新增原因都不能自动获得重放资格。
	f := StopFacts{Found: true, Aborted: true, StopReason: "made_up_by_a_future_commit"}
	state, why := f.State()
	if state == ResumeReplayable {
		t.Fatal("an unrecognised stop reason must never be replayable")
	}
	if state != ResumeHandoff || !strings.Contains(why, "made_up_by_a_future_commit") {
		t.Errorf("state=%v why=%q, want handoff naming the kind", state, why)
	}
}

func TestResumeStateString(t *testing.T) {
	for s, want := range map[ResumeState]string{
		ResumeUnknown: "unknown", ResumeReplayable: "replayable", ResumeHandoff: "handoff",
		ResumeFailed: "failed", ResumeFinished: "finished",
	} {
		if got := s.String(); got != want {
			t.Errorf("ResumeState(%d).String() = %q, want %q", s, got, want)
		}
	}
}

func TestScanStopFactsLaterEmptyFieldsDoNotOverwrite(t *testing.T) {
	// applyRunState 的语义是 StepNumber>0 才覆盖、StopReason 非空才覆盖。
	// 尾读走的是同一个 handleLine，所以必须遵守同样的"后者不冲掉前者"规则。
	lines := append(rrBaseLines(),
		rrAbort("first", StopKindCancelled, 3, 50),
		rrLine(rrTs7, "event_msg", `{"type":"turn_aborted"}`))
	facts := scanStopFacts(lines)
	if !facts.Aborted {
		t.Fatal("Aborted = false, want true")
	}
	if facts.StopReason != StopKindCancelled || facts.Step != 3 || facts.MaxSteps != 50 {
		t.Errorf("the trailing record with empty fields overwrote the real ones: %+v", facts)
	}
}

// TestStopFactsMatchFullRead 钉住"终止字段的回填规则只有一份"：
// 同一份夹具，尾读与整读必须给出逐字相同的判据。
// 漂移的表现不是报错，而是列表显示"可原样重放"、Enter 之后弹框说不能。
func TestStopFactsMatchFullRead(t *testing.T) {
	fixtures := map[string][]string{
		"paused":          append(rrBaseLines(), rrAbort("user paused", StopKindPaused, 12, 100), rrCheckpoint(StopKindPaused, "coding", 37, 150)),
		"timeout":         append(rrBaseLines(), rrAbort("idle", StopKindIdleTimeout, 44, 150)),
		"complete":        append(rrBaseLines(), rrLine(rrTs5, "event_msg", `{"type":"task_complete","step_number":8,"max_steps":100,"stop_reason":"agent_exit"}`)),
		"no-terminus":     rrBaseLines(),
		"checkpoint-only": append(rrBaseLines(), rrCheckpoint(StopKindCancelled, "repo", 5, 50)),
	}
	for name, lines := range fixtures {
		path := writeRollout(t, name+".jsonl", lines...)

		tail, err := ReadRolloutStopFacts(path)
		if err != nil {
			t.Fatalf("%s: ReadRolloutStopFacts: %v", name, err)
		}
		_, info, err := ReadRolloutMemory(path, DefaultRolloutReadOptions())
		if err != nil && !tail.Aborted {
			// 无消息的夹具会报错，但终止事件是独立通道（AGENTS §11 坑 19 同源）
			t.Logf("%s: full read errored (%v) — 仍按 info 比对", name, err)
		}

		if tail.Finished != info.Finished || tail.Aborted != info.Aborted {
			t.Errorf("%s: finished/aborted 尾读(%v,%v) 与整读(%v,%v) 不一致",
				name, tail.Finished, tail.Aborted, info.Finished, info.Aborted)
		}
		if tail.StopReason != info.StopReason {
			t.Errorf("%s: StopReason 尾读 %q 与整读 %q 不一致", name, tail.StopReason, info.StopReason)
		}
		if tail.MaxSteps != info.MaxSteps {
			t.Errorf("%s: MaxSteps 尾读 %d 与整读 %d 不一致", name, tail.MaxSteps, info.MaxSteps)
		}
		if tail.AbortReason != info.AbortReason {
			t.Errorf("%s: AbortReason 尾读 %q 与整读 %q 不一致", name, tail.AbortReason, info.AbortReason)
		}
		// 状态判据必须两边同一个答案（整读侧用 StopFactsOf 汇合，不是第二份表）
		ts, tw := tail.State()
		fs, fw := StopFactsOf(&info).State()
		if ts != fs || tw != fw {
			t.Errorf("%s: State() 尾读 (%v,%q) 与整读 (%v,%q) 不一致", name, ts, tw, fs, fw)
		}
	}
}
