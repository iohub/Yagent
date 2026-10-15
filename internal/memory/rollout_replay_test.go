package memory

// rollout_replay_test.go — 重放读侧的契约（docs/pause-resume-design.md §6.1、§10 G5/G8）。
//
// 这里钉的是"重放出来的消息序列 == 暂停前喂给模型的那一段"。断言的值必须是
// 旧读法（ReadRolloutMemory）给不出来的：丢 system、压 reasoning、截工具输出、
// 打 IsSubAgent —— 任何一条发生了，测试都要红，否则它只是在证明代码能跑。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yagent/internal/llm"
)

// isolateHome 把 rollout 根目录挪进临时目录。memory 包没有 TestMain 隔离 $HOME
// （AGENTS.md §11 那条已知坑），所以每个用 writer 的用例自己隔离，
// 不然会往真实的 ~/.yagent/data/rollout 里堆文件、挤脏 /resume 的清单。
func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

// replayFixture 写一份"跑到第 3 步被停住"的子 Agent rollout，返回文件路径。
func replayFixture(t *testing.T, agentName string, msgs []llm.Message, cp *AgentCheckpoint) string {
	t.Helper()
	isolateHome(t)
	w, err := NewRolloutWriter(agentName, "task-replay", "proj-replay")
	if err != nil || !w.Enabled() {
		t.Skipf("rollout writer unavailable: %v", err)
	}
	cwd, _ := os.Getwd()
	if err := w.WriteSessionMeta(SessionMeta{
		ID: w.SessionID(), SessionID: w.SessionID(), Cwd: cwd,
	}); err != nil {
		t.Fatalf("write session meta: %v", err)
	}
	if err := w.WriteTurnContext(TurnContext{TurnID: "t1", Cwd: cwd, Model: "claude-test"}); err != nil {
		t.Fatalf("write turn context: %v", err)
	}
	for _, m := range msgs {
		for _, item := range LLMMessageToResponseItems(m, w.NextMessageID()) {
			if err := w.WriteResponseItem(item); err != nil {
				t.Fatalf("write item: %v", err)
			}
		}
	}
	if cp != nil {
		if err := w.WriteAgentCheckpoint(*cp); err != nil {
			t.Fatalf("write checkpoint: %v", err)
		}
	} else {
		w.WriteRunAborted("context canceled", RunState{Steps: 3, MaxSteps: 10, StopReason: StopKindCancelled})
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return w.FilePath()
}

// 双向用例：写出去的消息序列必须能一字不差地读回来（含 Reasoning、
// 完整工具输出、system 消息、assistant 上的 tool_calls 分组）。
func TestReadRolloutForReplayRoundTripsMessages(t *testing.T) {
	in := []llm.Message{
		{Role: llm.RoleSystem, Content: "you are the coding agent"},
		{Role: llm.RoleUser, Content: "fix the flaky test"},
		{Role: llm.RoleAssistant, Content: "I will read it first", Reasoning: "thinking: the failing assert is line 12",
			ToolCalls: []llm.ToolCall{
				{ID: "call_a", Type: "function", Function: llm.FunctionCall{Name: "read_file", Arguments: `{"target_file":"a.go"}`}},
				{ID: "call_b", Type: "function", Function: llm.FunctionCall{Name: "list_dir", Arguments: `{"dir_path":"."}`}},
			}},
		{Role: llm.RoleTool, ToolCallID: "call_a", Content: "package main\n// 一整段很长的输出，不能被截断：" + strings.Repeat("x", 30000)},
		{Role: llm.RoleTool, ToolCallID: "call_b", Content: "a.go\nb.go"},
		{Role: llm.RoleAssistant, Content: "done", Reasoning: "thinking: wrapping up"},
	}
	path := replayFixture(t, "coding", in, nil)

	r, err := ReadRolloutForReplay(path, ReplayOptions{})
	if err != nil {
		t.Fatalf("ReadRolloutForReplay: %v", err)
	}
	if len(r.Messages) != len(in) {
		t.Fatalf("rebuilt %d messages, want %d (%+v)", len(r.Messages), len(in), rolesOf(r.Messages))
	}
	for i, want := range in {
		got := r.Messages[i]
		if got.Role != want.Role || got.Content != want.Content {
			t.Errorf("message %d = (%s, %d chars), want (%s, %d chars)",
				i, got.Role, len(got.Content), want.Role, len(want.Content))
		}
		if got.Reasoning != want.Reasoning {
			t.Errorf("message %d Reasoning = %q, want %q（thinking 要回灌 provider，丢了就不是同一个世界）",
				i, trunc(got.Reasoning, 60), trunc(want.Reasoning, 60))
		}
		if len(got.ToolCalls) != len(want.ToolCalls) {
			t.Fatalf("message %d has %d tool calls, want %d", i, len(got.ToolCalls), len(want.ToolCalls))
		}
		for j, tc := range want.ToolCalls {
			g := got.ToolCalls[j]
			if g.ID != tc.ID || g.Function.Name != tc.Function.Name || g.Function.Arguments != tc.Function.Arguments {
				t.Errorf("tool call %d/%d = %+v, want %+v", i, j, g, tc)
			}
		}
		if want.ToolCallID != "" && got.ToolCallID != want.ToolCallID {
			t.Errorf("message %d ToolCallID = %q, want %q", i, got.ToolCallID, want.ToolCallID)
		}
	}
	if r.Model != "claude-test" {
		t.Errorf("Model = %q, want claude-test", r.Model)
	}
	if r.AgentName != "coding" {
		t.Errorf("AgentName = %q, want coding（文件名回退）", r.AgentName)
	}
	// 没有检查点的文件必须明说，而不是悄悄给个 step=0。
	if r.Checkpoint == nil {
		if r.Step != 2 {
			t.Errorf("Step = %d, want 2（按 assistant 消息数派生的下界）", r.Step)
		}
		if !containsSubstring(r.Degraded, "checkpoint missing") {
			t.Errorf("Degraded = %v, want a 'checkpoint missing' entry", r.Degraded)
		}
	}
}

func TestReadRolloutForReplayUsesCheckpointStep(t *testing.T) {
	in := []llm.Message{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleUser, Content: "task"},
		{Role: llm.RoleAssistant, Content: "worked", ToolCalls: []llm.ToolCall{
			{ID: "c1", Function: llm.FunctionCall{Name: "run_bash", Arguments: `{}`}}}},
		{Role: llm.RoleTool, ToolCallID: "c1", Content: "ok"},
	}
	cp := &AgentCheckpoint{StopKind: StopKindCancelled, AgentName: "coding", Step: 37, MaxSteps: 150}
	path := replayFixture(t, "coding", in, cp)

	r, err := ReadRolloutForReplay(path, ReplayOptions{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if r.Checkpoint == nil || r.Checkpoint.Step != 37 {
		t.Fatalf("Checkpoint = %+v, want step 37", r.Checkpoint)
	}
	if r.Step != 37 {
		t.Errorf("Step = %d, want 37（有检查点时必须用它的游标，不是消息数）", r.Step)
	}
	if containsSubstring(r.Degraded, "checkpoint missing") {
		t.Errorf("有检查点却报了缺失：%v", r.Degraded)
	}
}

// 指纹的作用是"文件变了吗"，所以它必须由读侧重算并可比。
func TestReplayDigestDetectsTruncation(t *testing.T) {
	in := []llm.Message{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleUser, Content: "task"},
		{Role: llm.RoleAssistant, Content: "halfway", ToolCalls: []llm.ToolCall{
			{ID: "c1", Function: llm.FunctionCall{Name: "run_bash", Arguments: `{"command":"make"}`}}}},
		{Role: llm.RoleTool, ToolCallID: "c1", Content: "build ok"},
	}
	path := replayFixture(t, "coding", in, nil)

	full, err := ReadRolloutForReplay(path, ReplayOptions{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(full.Digest) != digestPrefix {
		t.Fatalf("Digest = %q, want %d chars", full.Digest, digestPrefix)
	}
	if len(full.Dangling) != 0 {
		t.Fatalf("完整文件的 Dangling = %+v, want none", full.Dangling)
	}

	// 精确的变异：把那条工具输出的记录删掉（GC 或截断都可能只少这一行）。
	// 直接砍文件尾是不行的 —— 最后一行是终止事件，删掉它消息序列根本没变，
	// 测试会以"什么都没测到"的方式通过。
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	keep := make([]string, 0, len(lines))
	dropped := 0
	for _, line := range lines {
		if dropped == 0 && strings.Contains(line, `"function_call_output"`) {
			dropped = 1 // 只删第一条命中的（本例里也只有一条）
			continue
		}
		keep = append(keep, line)
	}
	if dropped == 0 {
		t.Fatal("夹具失效：文件里没有 function_call_output 记录")
	}
	truncPath := filepath.Join(filepath.Dir(path), "truncated.jsonl")
	if err := os.WriteFile(truncPath, []byte(strings.Join(keep, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write truncated: %v", err)
	}

	short, err := ReadRolloutForReplay(truncPath, ReplayOptions{})
	if err != nil {
		t.Fatalf("replay truncated: %v", err)
	}
	if short.Digest == full.Digest {
		t.Errorf("截掉一条记录之后指纹没变：%q —— 防漂移这一环等于没做", short.Digest)
	}
	if d, ok := short.LastDangling(); !ok || d.Name != "run_bash" {
		t.Errorf("truncated replay Dangling = %+v (ok=%v), want the run_bash call", d, ok)
	}
	if len(short.Messages) >= len(full.Messages) {
		t.Errorf("truncated replay rebuilt %d messages, want fewer than %d", len(short.Messages), len(full.Messages))
	}
}

// 冷工具是派生态：load_tool 成功配对才算材料化，失败/未配对的都不算。
func TestReplayDerivesMaterializedTools(t *testing.T) {
	call := func(id, name, args string) llm.Message {
		return llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
			{ID: id, Function: llm.FunctionCall{Name: name, Arguments: args}}}}
	}
	in := []llm.Message{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleUser, Content: "task"},
		call("l1", "load_tool", `{"names":["run_bash","git_checkpoint_create"]}`),
		{Role: llm.RoleTool, ToolCallID: "l1", Content: "loaded 2 tools"},
		call("l2", "load_tool", `{"names":["deepthinking"]}`),
		{Role: llm.RoleTool, ToolCallID: "l2", Content: "Error: unknown tool deepthinking"},
		call("l3", "load_tool", `{"names":["prune_history"]}`), // 没有输出 = 没取回
	}
	path := replayFixture(t, "coding", in, nil)

	r, err := ReadRolloutForReplay(path, ReplayOptions{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	got := strings.Join(r.Materialized, ",")
	if got != "run_bash,git_checkpoint_create" {
		t.Errorf("Materialized = %q, want run_bash,git_checkpoint_create（错误输出与未配对的调用都不能算已取回）", got)
	}
}

// compacted 记录之后，模型看到的世界就是摘要 —— 重放必须照做，
// 并把这件事写进 Degraded（不是静默近似）。
func TestReplayAppliesCompactedRecord(t *testing.T) {
	isolateHome(t)
	w, err := NewRolloutWriter("coding", "task-compact", "proj-compact")
	if err != nil || !w.Enabled() {
		t.Skipf("writer unavailable: %v", err)
	}
	cwd, _ := os.Getwd()
	_ = w.WriteSessionMeta(SessionMeta{ID: w.SessionID(), SessionID: w.SessionID(), Cwd: cwd})
	for _, m := range []llm.Message{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleUser, Content: "task"},
		{Role: llm.RoleAssistant, Content: "a very long turn that compaction should drop"},
	} {
		for _, item := range LLMMessageToResponseItems(m, w.NextMessageID()) {
			_ = w.WriteResponseItem(item)
		}
	}
	_ = w.WriteCompacted(CompactedPayload{Summary: "SUMMARY OF PRIOR WORK", Kind: CompactionKindEmergency})
	_ = w.WriteRunAborted("cancelled", RunState{Steps: 4, MaxSteps: 10, StopReason: StopKindCancelled})
	path := w.FilePath()
	_ = w.Close()

	r, err := ReadRolloutForReplay(path, ReplayOptions{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(r.Messages) != 2 {
		t.Fatalf("rebuilt %d messages (%+v), want [system, summary] only", len(r.Messages), rolesOf(r.Messages))
	}
	if r.Messages[1].Content != "SUMMARY OF PRIOR WORK" {
		t.Errorf("post-compaction message = %q, want the summary", r.Messages[1].Content)
	}
	if !containsSubstring(r.Degraded, "compacted record") {
		t.Errorf("Degraded = %v, want a compacted-record note", r.Degraded)
	}
}

func TestReplayTokenBudgetRejectsOversizedContext(t *testing.T) {
	body := strings.Repeat("word ", 400)
	in := []llm.Message{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleUser, Content: body},
		{Role: llm.RoleAssistant, Content: body},
	}
	path := replayFixture(t, "coding", in, nil)

	// 估算器用长度换算，避免把 tiktoken 拉进这条用例（它测的是预算闸门，不是计数精度）。
	r, err := ReadRolloutForReplay(path, ReplayOptions{
		TokenBudget:    100,
		EstimateTokens: func(s string) int { return len(s) / 4 },
	})
	if err == nil {
		t.Fatalf("want ErrReplayTooLarge, got nil (replay=%+v)", r)
	}
	if !strings.Contains(err.Error(), ErrReplayTooLarge.Error()) {
		t.Errorf("err = %v, want it to wrap ErrReplayTooLarge", err)
	}
	// 超预算也要把已重建的事实带回来：调用方要用同一份内容生成降级报告，
	// 只返回 error 的话它得再读一遍文件。
	if r == nil || len(r.Messages) == 0 {
		t.Error("超预算时也必须返回已重建的 Replay，而不是只给一个 error")
	}
	if r.EstTokens <= 100 {
		t.Errorf("EstTokens = %d, want > budget 100", r.EstTokens)
	}
}

func TestReplayMaxMessagesCapIsHonest(t *testing.T) {
	var in []llm.Message
	in = append(in, llm.Message{Role: llm.RoleSystem, Content: "sys"})
	for i := 0; i < 6; i++ {
		in = append(in, llm.Message{Role: llm.RoleUser, Content: "turn"})
		in = append(in, llm.Message{Role: llm.RoleAssistant, Content: "reply"})
	}
	path := replayFixture(t, "coding", in, nil)

	r, err := ReadRolloutForReplay(path, ReplayOptions{MaxMessages: 4})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(r.Messages) != 4 {
		t.Errorf("rebuilt %d messages, want the cap 4", len(r.Messages))
	}
	if !containsSubstring(r.Degraded, "message cap") {
		t.Errorf("Degraded = %v, want a 'message cap reached' note（截了却不说明就是静默降级）", r.Degraded)
	}
}

func TestReplayMissingFile(t *testing.T) {
	if _, err := ReadRolloutForReplay(filepath.Join(t.TempDir(), "absent.jsonl"), ReplayOptions{}); err == nil {
		t.Error("want an error for a missing file")
	}
}

// FinalizeCheckpoint 的指纹必须由读侧算，否则"文件完好但内存被压缩改写过"会被误判成损坏。
func TestFinalizeCheckpointFillsDerivedFields(t *testing.T) {
	in := []llm.Message{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleUser, Content: "task"},
		{Role: llm.RoleAssistant, Content: "did work", Reasoning: "thought"},
	}
	path := replayFixture(t, "coding", in, nil)

	replay, err := ReadRolloutForReplay(path, ReplayOptions{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	cp, degraded := FinalizeCheckpoint(path, AgentCheckpoint{StopKind: StopKindCancelled, Step: 3})
	if cp.MessageCount != len(replay.Messages) {
		t.Errorf("MessageCount = %d, want %d", cp.MessageCount, len(replay.Messages))
	}
	if cp.Digest != replay.Digest {
		t.Errorf("Digest = %q, want the reader's %q — 两边算法必须同一次实现", cp.Digest, replay.Digest)
	}
	// 读侧的诚实必须原样传出去：这份文件里没有检查点，就要说"没有"。
	// 把它吞掉的话，调用方会以为坐标齐全，然后拿一个按消息数猜出来的起点去重放。
	if !containsSubstring(degraded, "checkpoint missing") {
		t.Errorf("Degraded = %v, want the reader's 'checkpoint missing' note to propagate", degraded)
	}

	// 打不开文件时不能留一个看起来可用的指纹。
	missing, degraded := FinalizeCheckpoint(filepath.Join(t.TempDir(), "nope.jsonl"), AgentCheckpoint{})
	if missing.Digest != "" || missing.MessageCount != 0 {
		t.Errorf("un-readable checkpoint = %+v, want empty digest and zero count", missing)
	}
	if !containsSubstring(degraded, "digest unavailable") {
		t.Errorf("Degraded = %v, want an 'unavailable' note", degraded)
	}
}

// WriteCheckpoint 端到端：写进去的指纹必须能被重放侧复算出来。
// 这条如果红，意味着每一次暂停-继续都会被自己的校验判成"文件损坏"。
func TestWriteCheckpointDigestMatchesReplay(t *testing.T) {
	isolateHome(t)
	w, err := NewRolloutWriter("coding", "task-digest", "proj-digest")
	if err != nil || !w.Enabled() {
		t.Skipf("writer unavailable: %v", err)
	}
	cwd, _ := os.Getwd()
	_ = w.WriteSessionMeta(SessionMeta{ID: w.SessionID(), SessionID: w.SessionID(), Cwd: cwd, DelegationID: "call_del"})
	for _, m := range []llm.Message{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleUser, Content: "task"},
		{Role: llm.RoleAssistant, Content: "worked", Reasoning: "thoughts", ToolCalls: []llm.ToolCall{
			{ID: "c1", Function: llm.FunctionCall{Name: "read_file", Arguments: `{"target_file":"a.go"}`}}}},
		{Role: llm.RoleTool, ToolCallID: "c1", Content: "package main"},
	} {
		for _, item := range LLMMessageToResponseItems(m, w.NextMessageID()) {
			_ = w.WriteResponseItem(item)
		}
	}
	WriteCheckpoint(t.Context(), w, CheckpointInput{
		AgentName: "coding", Model: "m", StopKind: StopKindCancelled, Steps: 4, MaxSteps: 10,
		CollectWorkspace: false, // 用例不关心 git 事实，且临时目录可能不是仓库
	})
	path := w.FilePath()
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	r, err := ReadRolloutForReplay(path, ReplayOptions{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if r.Checkpoint == nil {
		t.Fatal("检查点没落盘")
	}
	if r.Checkpoint.Digest != r.Digest {
		t.Errorf("stored digest %q != recomputed %q — 写入端与读取端的规则不是同一份实现",
			r.Checkpoint.Digest, r.Digest)
	}
	if r.Checkpoint.MessageCount != len(r.Messages) {
		t.Errorf("stored MessageCount = %d, rebuilt %d", r.Checkpoint.MessageCount, len(r.Messages))
	}
	if !strings.Contains(r.Checkpoint.Workspace.StatusError, "checkpoint_workspace") {
		t.Errorf("关掉工作区探测时必须留下说明，而不是记成\"工作区干净\"：%+v", r.Checkpoint.Workspace)
	}
}

// G8：正常跑 N 步只产生 0 或 1 条检查点，绝不能每步一条。
// 加 per-step 快照的那个人会以为"更精确"，代价是 rollout 体积翻倍、
// 以及恢复时要从 N 条里猜哪条才是真的停点。
func TestNoPerStepCheckpointWritten(t *testing.T) {
	isolateHome(t)
	w, err := NewRolloutWriter("coding", "task-g8", "proj-g8")
	if err != nil || !w.Enabled() {
		t.Skipf("writer unavailable: %v", err)
	}
	cwd, _ := os.Getwd()
	_ = w.WriteSessionMeta(SessionMeta{ID: w.SessionID(), SessionID: w.SessionID(), Cwd: cwd})
	for i := 0; i < 8; i++ {
		m := llm.Message{Role: llm.RoleAssistant, Content: "step"}
		for _, item := range LLMMessageToResponseItems(m, w.NextMessageID()) {
			_ = w.WriteResponseItem(item)
		}
	}
	WriteCheckpoint(t.Context(), w, CheckpointInput{AgentName: "coding", StopKind: StopKindCancelled, Steps: 8, CollectWorkspace: false})
	path := w.FilePath()
	_ = w.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var count int
	for _, line := range strings.Split(string(data), "\n") {
		var env struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(line), &env) == nil && env.Type == RecordAgentCheckpoint {
			count++
		}
	}
	if count != 1 {
		t.Errorf("%d 条 agent_checkpoint，want 1（一次停止一条）", count)
	}
}

func rolesOf(msgs []llm.Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = string(m.Role)
	}
	return out
}

func containsSubstring(hay []string, needle string) bool {
	for _, s := range hay {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
