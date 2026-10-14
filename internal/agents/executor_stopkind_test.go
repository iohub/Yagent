package agents

// executor_stopkind_test.go — P0.4 的闸门（docs/pause-resume-design.md §2 R4、§10）。
//
// 子 Agent 的终止事件此前只有 reason 字符串，StopReason 恒空
//（executor.go:342 的 RunState 只填 Steps/MaxSteps，Director 侧 planner_run.go
// 的 runState 填了四个）。恢复侧因此分不出"用户按了停止"与"委派超时了"，
// 而这两类的正确续跑策略相反：前者可以原样重放那一步，后者那一步可能已经把
// "Error: context canceled" 记进了 rollout，重放就是重复执行副作用。

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"yagent/internal/agents/activity"
	"yagent/internal/llm"
	"yagent/internal/memory"
	"yagent/internal/pause"
	"yagent/internal/thinklink"
	"yagent/internal/tools"
)

// abortedReader 跑一轮"注定失败"的子 Agent 并把终止事件读回来。
// engine 必须真的响应 ctx：忽略 ctx 的 mock 会把"取消没传播"直接掩盖掉
// （AGENTS.md §8.3 里那条脚本引擎规则在这里同样成立）。
func abortedReader(t *testing.T, ctx context.Context, engine llm.Engine, adapters []*tools.Adapter) (memory.RolloutInfo, error) {
	t.Helper()
	w, err := memory.NewRolloutWriter("stopkind-agent", "task-stopkind", "stopkind-test")
	if err != nil || !w.Enabled() {
		t.Skipf("rollout writer unavailable: %v", err)
	}
	path := w.FilePath()
	ctx = memory.WithRolloutWriter(ctx, w)

	runErr := func() error {
		_, e := RunAgentLoop(ctx, ExecutorConfig{
			SystemPrompt: "You are a test agent.",
			UserInput:    "do the thing",
			Adapters:     adapters,
			LLM:          engine,
			MaxSteps:     2,
			StopOnFinish: true,
			AgentName:    "stopkind-agent",
		})
		return e
	}()
	if runErr == nil {
		t.Errorf("RunAgentLoop returned no error, but this fixture is built to fail")
	}
	if cerr := w.Close(); cerr != nil {
		t.Fatalf("close rollout: %v", cerr)
	}

	_, info, rerr := memory.ReadRolloutMemory(path, memory.DefaultRolloutReadOptions())
	return info, rerr
}

// TestRunAgentLoop_UserCancelReportsCancelled 用户取消 → cancelled，而不是 llm_error。
//
// 取消让 LLM 调用失败，所以"按错误类型分类"会把它记成 llm_error —— 那正是
// §2 R1 里"停止看起来像一次失败"的最后一环：恢复侧看到 llm_error 就不敢重放。
func TestRunAgentLoop_UserCancelReportsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	info, err := abortedReader(t, ctx, newHangEngine("mock"), nil)
	if err != nil {
		t.Fatalf("ReadRolloutMemory: %v", err)
	}
	if !info.Aborted {
		t.Fatalf("Aborted = false, want true (info=%+v)", info)
	}
	if info.StopReason != StopKindCancelled {
		t.Errorf("StopReason = %q, want %q", info.StopReason, StopKindCancelled)
	}
	if info.StopReason == StopKindLLMError {
		t.Error("取消被误报成 llm_error：classifyStop 没有回头看 ctx")
	}
}

// TestRunAgentLoop_IdleTimeoutReportsIdleTimeout 是本组测试里最值钱的一条。
//
// activity 的 IdleTimeoutError 只挂在 context.Cause 上；引擎拿到的是裸
// context.Canceled。谁先看 err，谁就把每一次委派超时都误报成用户取消。
func TestRunAgentLoop_IdleTimeoutReportsIdleTimeout(t *testing.T) {
	base, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()

	toolCtx, cancelTool := activity.WithIdleTimeout(base, activity.Options{
		IdleTimeout:   20 * time.Millisecond,
		CheckInterval: 2 * time.Millisecond,
	})
	defer cancelTool()

	info, err := abortedReader(t, toolCtx, newHangEngine("mock"), nil)
	if err != nil {
		t.Fatalf("ReadRolloutMemory: %v", err)
	}
	if info.StopReason != StopKindIdleTimeout {
		t.Errorf("StopReason = %q, want %q (cause 通道没被优先采用)", info.StopReason, StopKindIdleTimeout)
	}
	// reason 里要留下可诊断的文案，不能只剩一个分类词。
	if !strings.Contains(info.AbortReason, "no activity for") {
		t.Errorf("AbortReason = %q, want the IdleTimeoutError text", info.AbortReason)
	}
}

// TestRunAgentLoop_TotalTimeoutReportsTotalTimeout total 与 idle 必须可区分：
// 前者是"这轮委派太久了"，后者是"它不动了"，排查方向完全不同。
func TestRunAgentLoop_TotalTimeoutReportsTotalTimeout(t *testing.T) {
	base, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()

	toolCtx, cancelTool := activity.WithIdleTimeout(base, activity.Options{
		IdleTimeout:   time.Hour, // 故意不认空闲，只让 total 生效
		TotalTimeout:  20 * time.Millisecond,
		CheckInterval: 2 * time.Millisecond,
	})
	defer cancelTool()

	// hangEngine 会先进入 GenerateContent 再等到 ctx 结束；心跳只发生在步进时，
	// 所以这里由 total 上限而不是 idle 触发取消。
	info, err := abortedReader(t, toolCtx, newHangEngine("mock"), nil)
	if err != nil {
		t.Fatalf("ReadRolloutMemory: %v", err)
	}
	if info.StopReason != StopKindTotalTimeout {
		t.Errorf("StopReason = %q, want %q (info=%+v)", info.StopReason, StopKindTotalTimeout, info)
	}
}

// TestRunAgentLoop_RealLLMFailureIsNotCancellable 反向夹具：ctx 活得好好的、
// 引擎真的报错时必须是 llm_error。没有这条，上面两个用例可以由
// "永远记 cancelled"实现蒙混通过。
func TestRunAgentLoop_RealLLMFailureIsNotCancellable(t *testing.T) {
	failing := &mockLLM{responses: []*llm.Response{}} // 零条响应 → 每次调用都返回 error
	info, err := abortedReader(t, context.Background(), failing, nil)
	if err != nil {
		t.Fatalf("ReadRolloutMemory: %v", err)
	}
	if info.StopReason != StopKindLLMError {
		t.Errorf("StopReason = %q, want %q", info.StopReason, StopKindLLMError)
	}
}

// TestRunAgentLoop_SuccessExitsClaimTheirOwnKind 成功出口也要认领原因：
// 散文收尾与 agent_exit 收尾是两类不同结束，恢复侧都不该重放，但排查时值得区分。
func TestRunAgentLoop_SuccessExitsClaimTheirOwnKind(t *testing.T) {
	t.Run("plain_text", func(t *testing.T) {
		w, err := memory.NewRolloutWriter("ok-agent", "task-ok-plain", "stopkind-test")
		if err != nil || !w.Enabled() {
			t.Skipf("rollout writer unavailable: %v", err)
		}
		ctx := memory.WithRolloutWriter(context.Background(), w)
		if _, rerr := RunAgentLoop(ctx, ExecutorConfig{
			SystemPrompt: "sp", UserInput: "ui", LLM: &mockLLM{responses: []*llm.Response{newTextResponse("all done")}},
			MaxSteps: 3, StopOnFinish: true, AgentName: "ok-agent",
		}); rerr != nil {
			t.Fatalf("RunAgentLoop: %v", rerr)
		}
		path := w.FilePath()
		w.Close()

		_, info, rerr := memory.ReadRolloutMemory(path, memory.DefaultRolloutReadOptions())
		if rerr != nil {
			t.Fatalf("ReadRolloutMemory: %v", rerr)
		}
		if info.StopReason != StopKindPlainTextReply {
			t.Errorf("StopReason = %q, want %q", info.StopReason, StopKindPlainTextReply)
		}
		if !info.Finished {
			t.Error("Finished = false, want true (散文收尾不该被当成中断)")
		}
	})

	t.Run("agent_exit", func(t *testing.T) {
		w, err := memory.NewRolloutWriter("exit-agent", "task-ok-exit", "stopkind-test")
		if err != nil || !w.Enabled() {
			t.Skipf("rollout writer unavailable: %v", err)
		}
		ctx := memory.WithRolloutWriter(context.Background(), w)
		exit := tools.NewAdapter("agent_exit", "finish", func(context.Context, map[string]interface{}) (interface{}, error) {
			return `{"finished":true,"reason":"done"}`, nil
		})
		script := &mockLLM{responses: []*llm.Response{
			newToolResponse(makeCall("t1", "agent_exit")),
			newTextResponse("should never be reached"),
		}}
		if _, rerr := RunAgentLoop(ctx, ExecutorConfig{
			SystemPrompt: "sp", UserInput: "ui", Adapters: []*tools.Adapter{exit}, LLM: script,
			MaxSteps: 3, StopOnFinish: true, AgentName: "exit-agent",
		}); rerr != nil {
			t.Fatalf("RunAgentLoop: %v", rerr)
		}
		path := w.FilePath()
		w.Close()

		_, info, rerr := memory.ReadRolloutMemory(path, memory.DefaultRolloutReadOptions())
		if rerr != nil {
			t.Fatalf("ReadRolloutMemory: %v", rerr)
		}
		if info.StopReason != StopKindAgentExit {
			t.Errorf("StopReason = %q, want %q", info.StopReason, StopKindAgentExit)
		}
		if script.callCount != 1 {
			t.Errorf("LLM called %d times, want 1: agent_exit 没有真的收尾（见 AGENTS.md 坑 16）", script.callCount)
		}
	})
}

// ─── thinklink 落盘（R7）───────────────────────────────────────────────────────

// TestRunAgentLoop_ThinklinkEntriesLandInRollout 子 Agent 的 T&P 块此前只发事件总线，
// 从不写 rollout（Director 侧 planner_run.go:48-55 两件事都做）。后果是
// 恢复后这份日志彻底不存在，而 ultimate 压缩重建上下文全靠它
// （RebuildPrompt 的原料是"用户输入 + T&P 块"）。
func TestRunAgentLoop_ThinklinkEntriesLandInRollout(t *testing.T) {
	w, err := memory.NewRolloutWriter("thinklink-agent", "task-thinklink", "thinklink-test")
	if err != nil || !w.Enabled() {
		t.Skipf("rollout writer unavailable: %v", err)
	}
	ctx := memory.WithRolloutWriter(context.Background(), w)

	block := "<thought>\n" +
		"goal: pin the thinklink journal\n" +
		"step: read the executor loop\n" +
		"reason: write the entries to rollout so a resume can rebuild them\n" +
		"plan:\n" +
		"- [x] read executor.go\n" +
		"- [>] add the rollout write\n" +
		"- [ ] run the suite\n" +
		"</thought>"
	store := thinklink.NewStore(0)
	publisher := &spyPublisher{}
	// 带 T&P 块 **且**带工具调用的那一轮才会走到提取点：executor 在
	// append assistant 之后、执行工具之前抽块（executor.go:686 一带），
	// 纯文本回复的出口在提取之后，用它测不到落盘。
	script := &mockLLM{responses: []*llm.Response{
		{Choices: []llm.Choice{{
			Content:   block,
			ToolCalls: []llm.ToolCall{makeCall("t1", "noop_tool")},
		}}},
		newTextResponse("done"),
	}}
	noop := tools.NewAdapter("noop_tool", "noop", func(context.Context, map[string]interface{}) (interface{}, error) {
		return "ok", nil
	})

	if _, rerr := RunAgentLoop(ctx, ExecutorConfig{
		SystemPrompt: "You are a test agent.", UserInput: "journal my reasoning",
		Adapters: []*tools.Adapter{noop}, LLM: script, MaxSteps: 4,
		StopOnFinish: true, AgentName: "thinklink-agent", Publisher: publisher,
		UltimateThinkLink: store,
	}); rerr != nil {
		t.Fatalf("RunAgentLoop: %v", rerr)
	}
	path := w.FilePath()
	w.Close()

	_, info, rerr := memory.ReadRolloutMemory(path, memory.DefaultRolloutReadOptions())
	if rerr != nil {
		t.Fatalf("ReadRolloutMemory: %v", rerr)
	}
	if len(info.ThinklinkEntries) != 1 {
		t.Fatalf("ThinklinkEntries = %d, want exactly 1: %+v", len(info.ThinklinkEntries), info.ThinklinkEntries)
	}
	got := info.ThinklinkEntries[0]
	if !strings.Contains(got.Content, "pin the thinklink journal") {
		t.Errorf("entry content lost the goal line: %q", got.Content)
	}
	if got.Step != 0 {
		t.Errorf("entry step = %d, want 0 (第一个 step 的下标)", got.Step)
	}

	// 进程内那份日志也只能有一条：双写会让 RebuildPrompt 把同一段推理说两遍，
	// 而 ultimate 压缩是按 token 预算截断的，重复条目挤掉的是真实历史。
	entries := store.Snapshot()
	if len(entries) != 1 {
		t.Errorf("in-process thinklink store has %d entries, want 1", len(entries))
	}

	// 事件总线仍要有（TUI 的 thinklink 面板靠它），且不能因为落盘而重复发布。
	published := 0
	for _, e := range publisher.snapshot() {
		if e.evType == "thinklink_entry" {
			published++
		}
	}
	if published != 1 {
		t.Errorf("thinklink_entry published %d times, want 1", published)
	}
}

// TestRunAgentLoop_ThinklinkWriteIsSkippedWithoutWriter 关掉 writer 时不能 panic，
// 也不能悄悄改变行为（循环单测普遍不带 writer）。
func TestRunAgentLoop_ThinklinkWriteIsSkippedWithoutWriter(t *testing.T) {
	store := thinklink.NewStore(0)
	script := &mockLLM{responses: []*llm.Response{newTextResponse("<thought>\ngoal: g\nstep: s\nreason: r\nplan:\n- [x] a\n</thought>")}}
	res, err := RunAgentLoop(context.Background(), ExecutorConfig{
		SystemPrompt: "sp", UserInput: "ui", LLM: script, MaxSteps: 2,
		AgentName: "no-writer-agent", UltimateThinkLink: store,
	})
	if err != nil {
		t.Fatalf("RunAgentLoop: %v", err)
	}
	if res.Text == "" {
		t.Error("empty result text")
	}
	if len(store.Snapshot()) != 1 {
		t.Errorf("store entries = %d, want 1", len(store.Snapshot()))
	}
}

// TestStopKindTracker_DefaultsToUnaccounted 保证"没人认领的结束"是可见的。
// 今天不存在这条路径；写在这里是为了将来有人新加 return 而忘了认领时，
// 落到 rollout 的是一个能搜到的词，而不是空字符串。
func TestStopKindTracker_DefaultsToUnaccounted(t *testing.T) {
	tracker := newStopKindTracker(context.Background())
	if got := tracker.kindOf(); got != StopKindUnaccounted {
		t.Errorf("kindOf() = %q, want %q", got, StopKindUnaccounted)
	}
}

// TestStopKindTracker_FailureBeatsSuccessAndFirstFailureWins 钉住两条覆盖规则：
// 先记了成功、后面真失败 → 失败胜出；已有失败原因时保持**首因**（取消会连锁引发
// 后续失败，让后写者覆盖记到的永远是最后那层噪声）。
func TestStopKindTracker_FailureBeatsSuccessAndFirstFailureWins(t *testing.T) {
	t.Run("failure overrides a success", func(t *testing.T) {
		tracker := newStopKindTracker(context.Background())
		tracker.record(StopKindPlainTextReply, nil)
		tracker.record(StopKindPanic, fmt.Errorf("boom"))
		if got := tracker.kindOf(); got != StopKindPanic {
			t.Errorf("kindOf() = %q, want %q", got, StopKindPanic)
		}
	})

	t.Run("first failure wins", func(t *testing.T) {
		tracker := newStopKindTracker(context.Background())
		tracker.record(StopKindCancelled, context.Canceled)
		tracker.record(StopKindLLMError, fmt.Errorf("500"))
		if got := tracker.kindOf(); got != StopKindCancelled {
			t.Errorf("kindOf() = %q, want %q (首因)", got, StopKindCancelled)
		}
	})

	t.Run("success does not override a failure", func(t *testing.T) {
		tracker := newStopKindTracker(context.Background())
		tracker.record(StopKindPanic, fmt.Errorf("boom"))
		tracker.record(StopKindAgentExit, nil)
		if got := tracker.kindOf(); got != StopKindPanic {
			t.Errorf("kindOf() = %q, want %q", got, StopKindPanic)
		}
	})

	t.Run("empty kind with no signal records nothing", func(t *testing.T) {
		tracker := newStopKindTracker(context.Background())
		tracker.record("", nil)
		if got := tracker.kindOf(); got != StopKindUnaccounted {
			t.Errorf("kindOf() = %q, want %q", got, StopKindUnaccounted)
		}
	})
}

// TestRunAgentLoop_AbandonedPauseReportsPaused 停在闸门上之后被取消（用户按停止、
// 或直接关终端）→ 账本上必须是 paused，而不是被 Unwrap 出来的 context.Canceled
// 掩盖成 cancelled。差别在于"停在哪"：闸门只在 step 边界生效，所以这一轮的
// 工具批次已全部提交完，P2 可以按这个证据决定原样继续。
func TestRunAgentLoop_AbandonedPauseReportsPaused(t *testing.T) {
	g := pause.NewGate()
	g.Pause("/pause")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = pause.WithGate(ctx, g)

	go func() {
		for i := 0; i < 1500; i++ {
			if g.Blocked() {
				cancel()
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
		cancel() // 夹具失效也要让循环结束，否则这里是死等
	}()

	info, err := abortedReader(t, ctx, newHangEngine("mock"), nil)
	if err != nil {
		t.Fatalf("ReadRolloutMemory: %v", err)
	}
	if !g.Blocked() && !info.Aborted {
		t.Fatal("夹具失效：循环从没停在闸门上，这一轮走的是别的出口")
	}
	if info.StopReason != StopKindPaused {
		t.Errorf("StopReason = %q, want %q（AbandonedError 被 Unwrap 成裸取消了）",
			info.StopReason, StopKindPaused)
	}
	if !strings.Contains(info.AbortReason, "paused run abandoned") {
		t.Errorf("AbortReason = %q, want it to say the pause was abandoned", info.AbortReason)
	}
}

// TestRunAgentLoop_ReleasedPauseLeavesNoAbort 放开闸门之后，这一轮的账本必须
// 看起来根本没被打断过 —— 写 task_complete 而不是 turn_aborted。
// 把"恢复"记成一次中断的话，/resume 的清单会凭空多出一个坏任务，
// 而用户刚刚明明已经让它跑完了。
func TestRunAgentLoop_ReleasedPauseLeavesNoAbort(t *testing.T) {
	g := pause.NewGate()
	g.Pause("/pause")
	ctx := pause.WithGate(context.Background(), g)

	w, err := memory.NewRolloutWriter("pause-agent", "task-pause-agent", "pause-release")
	if err != nil || !w.Enabled() {
		t.Skipf("rollout writer unavailable: %v", err)
	}
	path := w.FilePath()
	ctx = memory.WithRolloutWriter(ctx, w)

	script := &mockLLM{responses: []*llm.Response{newTextResponse("done")}}
	done := make(chan error, 1)
	go func() {
		_, rerr := RunAgentLoop(ctx, ExecutorConfig{
			SystemPrompt: "You are a test agent.",
			UserInput:    "park once then finish",
			LLM:          script,
			MaxSteps:     3,
			AgentName:    "pause-agent",
		})
		done <- rerr
	}()

	deadline := time.Now().Add(3 * time.Second)
	for !g.Blocked() && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if !g.Blocked() {
		t.Fatal("夹具失效：循环没有在第一个 step 边界上停住")
	}
	g.Resume()
	if rerr := <-done; rerr != nil {
		t.Fatalf("恢复后 RunAgentLoop 返回错误：%v", rerr)
	}
	if cerr := w.Close(); cerr != nil {
		t.Fatalf("close rollout: %v", cerr)
	}

	_, info, rerr := memory.ReadRolloutMemory(path, memory.DefaultRolloutReadOptions())
	if rerr != nil {
		t.Fatalf("ReadRolloutMemory: %v", rerr)
	}
	if info.Aborted {
		t.Errorf("Aborted = true（一次被放开的暂停被记成了中断）：%+v", info)
	}
	if info.StopReason != StopKindPlainTextReply {
		t.Errorf("StopReason = %q, want %q", info.StopReason, StopKindPlainTextReply)
	}
}
