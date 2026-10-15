package agents

// e2e_replay_test.go — 重放的保真闸门（docs/pause-resume-design.md §10 G2、G5）。
//
// 这两条是 B 层的全部理由，也是最容易"看起来成功"的地方：
// 重放出来的消息少了一条、或者顺序变了，模型不会报错，它会心平气和地
// 把已经做完的步骤再做一遍 —— 于是恢复变成重复执行副作用。
// 所以断言必须逐条比对，而不是"跑通了就算对"。

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"yagent/internal/llm"
	"yagent/internal/memory"
	"yagent/internal/pause"
	"yagent/internal/tools"
)

// runStopped 跑一轮"做完了一步才停住、随后被丢弃"的子 Agent，返回它的 rollout writer。
//
// 一定要让第一步真的做完：G2 要验的是"重放起点接得上"，如果第一轮一步都没跑，
// 文件里只有 system+user，Step=0 —— 那种夹具验不出游标是否连续。
//
// 时机必须由信号决定，不能靠"调用发出去了就暂停"：那样循环可能在下一次
// 边界检查之前就跑完了整轮，Blocked() 永远不会成立 —— 写出来是一个有时序窗口的
// 假闸门（本机单跑过、整包跑就红，正是这种夹具的形状）。所以让工具本身
// 停在在飞状态里，由测试先按下暂停再放行。
func runStopped(t *testing.T, script map[string][]scriptStep) *memory.RolloutWriter {
	t.Helper()
	w, err := memory.NewRolloutWriter("coding", "task-replay-"+t.Name(), "replay-test")
	if err != nil || !w.Enabled() {
		t.Skipf("rollout writer unavailable: %v", err)
	}

	g := pause.NewGate()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ctx = pause.WithGate(ctx, g)
	ctx = memory.WithRolloutWriter(ctx, w)

	blocker := newStepBlockingAdapter(t)
	done := make(chan error, 1)
	go func() {
		_, rerr := RunAgentLoop(ctx, ExecutorConfig{
			SystemPrompt: codingSystemPrompt,
			UserInput:    "do the thing then stop",
			Adapters:     []*tools.Adapter{blocker.ad},
			LLM:          newScripted(script),
			MaxSteps:     5,
			AgentName:    "coding",
		})
		done <- rerr
	}()

	<-blocker.started // 第一步的工具正在飞
	g.Pause("/pause") // 暂停必然落在这一批之后
	close(blocker.release)

	waitUntil(t, "loop parked at the gate", func() bool { return g.Blocked() })
	cancel() // 停住之后被丢弃：这一轮以 stop_kind=paused 结束
	if rerr := <-done; rerr == nil {
		t.Fatal("被丢弃的暂停应当返回错误")
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return w
}

// G2：重放第一步发给模型的 messages 必须与暂停前逐条相等，含 Reasoning。
func TestResumeReplaysExactMessages(t *testing.T) {
	first := makeToolCallResponse("slow_tool", `{}`)
	first.Choices[0].Reasoning = "thinking: I should call the slow tool first"

	w := runStopped(t, map[string][]scriptStep{
		"coding": {fixed(first)},
	})

	replay, err := memory.ReadRolloutForReplay(w.FilePath(), memory.ReplayOptions{})
	if err != nil {
		t.Fatalf("ReadRolloutForReplay: %v", err)
	}
	if len(replay.Messages) == 0 {
		t.Fatalf("重建出 0 条消息（文件：%s）", w.FilePath())
	}
	if replay.Checkpoint == nil {
		t.Fatal("停止路径必须留下一条 agent_checkpoint")
	}
	if replay.Checkpoint.StopKind != StopKindPaused {
		t.Errorf("checkpoint.StopKind = %q, want %q", replay.Checkpoint.StopKind, StopKindPaused)
	}
	if replay.Checkpoint.Digest != replay.Digest {
		t.Errorf("检查点指纹 %q 与重放重算的 %q 不符 —— 每一次恢复都会被自己的校验判成损坏",
			replay.Checkpoint.Digest, replay.Digest)
	}
	if replay.Checkpoint.MessageCount != len(replay.Messages) {
		t.Errorf("checkpoint.MessageCount = %d, rebuilt %d", replay.Checkpoint.MessageCount, len(replay.Messages))
	}

	// 第二轮：带着 ResumeState 起跑，抓第一次 LLM 调用看到的消息。
	engine := newScripted(map[string][]scriptStep{"coding": {fixed(textResponse("all done"))}})
	w2, err := memory.NewRolloutWriter("coding", "task-replay2-"+t.Name(), "replay-test")
	if err != nil || !w2.Enabled() {
		t.Skipf("second writer unavailable: %v", err)
	}
	defer w2.Close()

	ctx := memory.WithRolloutWriter(context.Background(), w2)
	ctx = WithResumeState(ctx, &ResumeState{
		Messages:  replay.Messages,
		StartStep: replay.Step,
	})
	if _, rerr := RunAgentLoop(ctx, ExecutorConfig{
		SystemPrompt: codingSystemPrompt,
		UserInput:    "do the thing then stop",
		Adapters:     []*tools.Adapter{tools.NewAdapter("noop_tool", "noop", func(context.Context, map[string]interface{}) (interface{}, error) { return "ok", nil })},
		LLM:          engine,
		MaxSteps:     6,
		AgentName:    "coding",
	}); rerr != nil {
		t.Fatalf("重放运行失败：%v", rerr)
	}

	calls := engine.CallsFor("coding")
	if len(calls) != 1 {
		t.Fatalf("重放期间模型被调用 %d 次, want 1", len(calls))
	}
	got := calls[0].messages
	if len(got) != len(replay.Messages) {
		t.Fatalf("重放第一步发送 %d 条消息, want %d（暂停前的条数）", len(got), len(replay.Messages))
	}
	for i, want := range replay.Messages {
		have := got[i]
		if have.Role != want.Role || have.Content != want.Content {
			t.Errorf("消息 %d = (%s,%q), want (%s,%q)", i, have.Role, truncateForFailure(have.Content), want.Role, truncateForFailure(want.Content))
		}
		if have.Reasoning != want.Reasoning {
			t.Errorf("消息 %d 的 Reasoning 丢了或变了：have=%q want=%q（thinking 会回灌 provider，这是模型实际看到的世界）",
				i, truncateForFailure(have.Reasoning), truncateForFailure(want.Reasoning))
		}
		if len(have.ToolCalls) != len(want.ToolCalls) {
			t.Errorf("消息 %d 的 tool_calls = %d, want %d", i, len(have.ToolCalls), len(want.ToolCalls))
		}
	}

	// step 计数必须接续，而不是从 0 重来 —— 否则 max_steps 会被暂停-继续白白吃光。
	if replay.Step < 1 {
		t.Errorf("重放起点 Step = %d, want >= 1（第一轮做完了一步）", replay.Step)
	}
	// 重放文件不该再写一遍 system/user：那会让两份文件的消息对不上。
	r2, err := memory.ReadRolloutForReplay(w2.FilePath(), memory.ReplayOptions{})
	if err != nil {
		t.Fatalf("replay second file: %v", err)
	}
	if n := countRole(r2.Messages, llm.RoleSystem); n != 0 {
		t.Errorf("重放文件里写了 %d 条 system 消息, want 0（它们已经在原文件里了）", n)
	}
	// step 游标必须接续：第二轮的终止事件记的是"从 startStep 累加"，
	// 从 0 重来的话 max_steps 会被每次暂停-继续白吃掉一格。
	// 这里读终止事件而不是 replay.Step —— 第二轮正常跑完，没有检查点，
	// replay.Step 是"按消息数派生的下界"，它测不到游标是否连续。
	_, info2, ierr := memory.ReadRolloutMemory(w2.FilePath(), memory.DefaultRolloutReadOptions())
	if ierr != nil {
		t.Fatalf("read second rollout info: %v", ierr)
	}
	if info2.BestStep() <= replay.Step {
		t.Errorf("第二轮的 step 游标 = %d, want 大于第一轮的 %d", info2.BestStep(), replay.Step)
	}
}

// G5：硬取消（不是暂停）也要留下可用的检查点 —— 关终端、进程被杀都走这条路。
func TestCancelWritesUsableCheckpoint(t *testing.T) {
	w, err := memory.NewRolloutWriter("coding", "task-cancel-"+t.Name(), "cancel-test")
	if err != nil || !w.Enabled() {
		t.Skipf("writer unavailable: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ctx = memory.WithRolloutWriter(ctx, w)

	noop := tools.NewAdapter("noop_tool", "noop", func(context.Context, map[string]interface{}) (interface{}, error) {
		cancel() // 在工具执行的中途取消：这是最脏的一条路径
		return "ok", nil
	})
	first := makeToolCallResponse("noop_tool", `{}`)
	engine := newScripted(map[string][]scriptStep{"coding": {fixed(first), fixed(textResponse("never reached"))}})

	_, rerr := RunAgentLoop(ctx, ExecutorConfig{
		SystemPrompt: codingSystemPrompt,
		UserInput:    "cancel me mid-run",
		Adapters:     []*tools.Adapter{noop},
		LLM:          engine,
		MaxSteps:     4,
		AgentName:    "coding",
	})
	if rerr == nil {
		t.Fatal("取消之后 RunAgentLoop 应当返回错误")
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	replay, err := memory.ReadRolloutForReplay(w.FilePath(), memory.ReplayOptions{})
	if err != nil {
		t.Fatalf("重放读侧读不了刚取消的会话：%v", err)
	}
	if replay.Checkpoint == nil {
		t.Fatal("硬取消没有落检查点 —— 这条会话从此捞不回来")
	}
	if replay.Checkpoint.StopKind != StopKindCancelled {
		t.Errorf("StopKind = %q, want %q", replay.Checkpoint.StopKind, StopKindCancelled)
	}
	if replay.Checkpoint.Digest != replay.Digest {
		t.Errorf("指纹不符：stored=%q recomputed=%q", replay.Checkpoint.Digest, replay.Digest)
	}
	if replay.DelegationID != "" {
		t.Errorf("DelegationID = %q, want 空（这条运行不是被委派起来的）", replay.DelegationID)
	}
}

// delegation_id 必须一路落到子 Agent 的文件里（§5.1 / R3）。
// 没有它，"这份子 Agent 的 rollout 对应哪一次委派"就无从回答 —— 恢复只能回到 Director 重来。
func TestDelegationIDLandsInChildRollout(t *testing.T) {
	w, err := memory.NewRolloutWriter("coding", "task-deleg-"+t.Name(), "deleg-test")
	if err != nil || !w.Enabled() {
		t.Skipf("writer unavailable: %v", err)
	}
	w.SetDelegation("call_delegate_42", "sess_parent_7")
	cwd, _ := os.Getwd()
	if err := w.WriteSessionMeta(memory.SessionMeta{ID: w.SessionID(), SessionID: w.SessionID(), Cwd: cwd}); err != nil {
		t.Fatalf("write meta: %v", err)
	}
	// 前置条件：必须有一条真实消息。整文件没有 message 条目的会话会被清单判成 Empty
	// 直接滤掉 —— 少了这一行，下面的扫描断言失败原因会看起来像"扫描漏了"。
	items := memory.LLMMessageToResponseItems(llm.Message{Role: llm.RoleUser, Content: "delegate me"}, w.NextMessageID())
	for _, item := range items {
		if err := w.WriteResponseItem(item); err != nil {
			t.Fatalf("write item: %v", err)
		}
	}
	// 兜底必须生效：调用点（executor）压根没传这两个字段。
	if err := w.WriteRunAborted("cancelled", memory.RunState{Steps: 2, MaxSteps: 5, StopReason: StopKindCancelled}); err != nil {
		t.Fatalf("abort: %v", err)
	}
	path := w.FilePath()
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	replay, err := memory.ReadRolloutForReplay(path, memory.ReplayOptions{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replay.DelegationID != "call_delegate_42" || replay.ParentSessionID != "sess_parent_7" {
		t.Errorf("坐标没落到盘上：deleg=%q parent=%q", replay.DelegationID, replay.ParentSessionID)
	}

	sessions, err := memory.ListRolloutSessions(filepath.Dir(path), 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var found bool
	for _, s := range sessions {
		if s.Path == path {
			found = true
			if s.DelegationID != "call_delegate_42" {
				t.Errorf("清单里的 DelegationID = %q, want call_delegate_42", s.DelegationID)
			}
		}
	}
	if !found {
		t.Errorf("清单没扫到这份文件：%+v", sessions)
	}
}

func countRole(msgs []llm.Message, role llm.Role) int {
	n := 0
	for _, m := range msgs {
		if m.Role == role {
			n++
		}
	}
	return n
}
