package agents

// tool_pairing_test.go — 闸门 G3（docs/pause-resume-design.md §10）。
//
// 一次委派被暂停/取消后又在别的进程里恢复完成时，同一个 tool_call_id 会有**两条**
// function_call_output：取消时那条 "Error: context canceled"，和重放完成后的真实结果。
// 三条归一化路径必须选出同一条，否则"进程内继续"与"重启后继续"会让 Director 看到
// 两个不同的世界 —— 而且症状是"恢复后模型行为诡异"，没有报错，极难归因。
//
// 三条路径分别住在三个包里，这是唯一能同时看到它们的包：
//   - llm.NormalizeMessages → repairToolCallPairs（executor 每步调用，executor.go:408）
//   - validateAndRepairToolCallPairs（Director planner 每步调用，director.go:880 注入）
//   - memory.ReadRolloutMemory → normalizeToolPairing（/resume 读盘，rollout_reader.go:732）

import (
	"context"
	"testing"

	"yagent/internal/llm"
	"yagent/internal/memory"
)

const (
	pairedCallID    = "call_delegate_coding_1"
	staleOutput     = "Error: context canceled"
	resumedOutput   = "RESUMED DELIVERABLE: added the gate to executor.go:375"
	pausedIndicator = "PRE-PAUSE OUTPUT (must not survive)"
)

// duplicateCallOutputs 是三条路径共同消费的输入：一个 call、两条 output。
func duplicateCallOutputs() []llm.Message {
	return []llm.Message{
		{Role: llm.RoleUser, Content: "implement the pause gate"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{
			ID:       pairedCallID,
			Type:     "function",
			Function: llm.FunctionCall{Name: "delegate_coding", Arguments: `{"task":"implement the pause gate"}`},
		}}},
		{Role: llm.RoleTool, ToolCallID: pairedCallID, ToolName: "delegate_coding", Content: staleOutput},
		{Role: llm.RoleTool, ToolCallID: pairedCallID, ToolName: "delegate_coding", Content: resumedOutput},
	}
}

// soleOutputFor 返回该 call_id 归一化后**唯一**剩下的 output。
// 0 条或 2 条都算失败：前者说明结果被丢光，后者说明去重根本没发生
// （provider 会拒绝一个 call_id 对应两条 output 的请求）。
func soleOutputFor(t *testing.T, msgs []llm.Message, callID string) string {
	t.Helper()
	var got []string
	for _, m := range msgs {
		if m.Role == llm.RoleTool && m.ToolCallID == callID {
			got = append(got, m.Content)
		}
	}
	if len(got) != 1 {
		t.Fatalf("call_id=%s 归一化后剩 %d 条 output，want 1: %q", callID, len(got), got)
	}
	return got[0]
}

func TestToolPairingNormalizersAgreeOnDuplicateOutput(t *testing.T) {
	// 前置条件：夹具必须真的带上两条同 id 的 output，否则三条路径都无事可做，
	// 断言会退化成恒真（AGENTS.md §8.3 "fixture 小到根本没触发被测分支"）。
	if n := len(duplicateCallOutputs()); n != 4 {
		t.Fatalf("fixture = %d messages, want 4", n)
	}

	llmSide := soleOutputFor(t, llm.NormalizeMessages(duplicateCallOutputs()), pairedCallID)
	directorSide := soleOutputFor(t, validateAndRepairToolCallPairs(duplicateCallOutputs()), pairedCallID)
	readerSide := readBackRolledOutput(t)

	if llmSide != resumedOutput {
		t.Errorf("llm.NormalizeMessages 选中的不是后写的那条: got %q, want %q", llmSide, resumedOutput)
	}
	if directorSide != resumedOutput {
		t.Errorf("validateAndRepairToolCallPairs 选中的不是后写的那条: got %q, want %q", directorSide, resumedOutput)
	}
	if readerSide != resumedOutput {
		t.Errorf("ReadRolloutMemory 选中的不是后写的那条: got %q, want %q", readerSide, resumedOutput)
	}
	if llmSide != directorSide || directorSide != readerSide {
		t.Errorf("三条归一化路径结果不一致:\n  llm=%q\n  director=%q\n  reader=%q", llmSide, directorSide, readerSide)
	}
}

// readBackRolledOutput 用**生产写入器**把同一组消息落成 rollout 再读回来 ——
// 手工拼 JSONL 会测到一个生产不存在的形状上。
func readBackRolledOutput(t *testing.T) string {
	t.Helper()

	w, err := memory.NewRolloutWriter("pairing-agreement-agent", "task-pairing-agreement", "pairing-agreement-test")
	if err != nil || !w.Enabled() {
		t.Skipf("rollout writer unavailable: %v", err)
	}
	path := w.FilePath()
	ctx := memory.WithRolloutWriter(context.Background(), w)
	_ = ctx

	for _, msg := range duplicateCallOutputs() {
		items := memory.LLMMessageToResponseItems(msg, w.NextMessageID())
		for _, item := range items {
			if werr := w.WriteResponseItem(item); werr != nil {
				w.Close()
				t.Fatalf("write rollout item: %v", werr)
			}
		}
	}
	if cerr := w.Close(); cerr != nil {
		t.Fatalf("close rollout: %v", cerr)
	}

	mem, info, rerr := memory.ReadRolloutMemory(path, memory.DefaultRolloutReadOptions())
	if rerr != nil {
		t.Fatalf("ReadRolloutMemory: %v", rerr)
	}
	if info.SupersededOutputs != 1 {
		t.Errorf("SupersededOutputs = %d, want 1（读盘侧必须把后到的 output 记成覆盖而非丢弃）", info.SupersededOutputs)
	}
	if info.DroppedOrphanOutputs != 0 {
		t.Errorf("DroppedOrphanOutputs = %d, want 0（last-wins 不是孤儿丢弃）", info.DroppedOrphanOutputs)
	}
	if len(info.DanglingToolCalls) != 0 {
		t.Errorf("DanglingToolCalls = %+v, want 0（该 call 最终有了结果，不该再算悬空）", info.DanglingToolCalls)
	}
	return soleOutputFor(t, mem.ToMessages(), pairedCallID)
}

// TestToolPairingStaleOutputSurvivesWhenNeverSuperseded 反向夹具：只有一条 output 时
// 必须原样保留。没有这条，上面那组断言可以由"永远丢弃所有 output"的实现蒙混通过。
func TestToolPairingStaleOutputSurvivesWhenNeverSuperseded(t *testing.T) {
	single := []llm.Message{
		{Role: llm.RoleUser, Content: "task"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{
			ID:       pairedCallID,
			Type:     "function",
			Function: llm.FunctionCall{Name: "delegate_coding", Arguments: `{}`},
		}}},
		{Role: llm.RoleTool, ToolCallID: pairedCallID, Content: pausedIndicator},
	}

	for name, msgs := range map[string][]llm.Message{
		"llm":      llm.NormalizeMessages(single),
		"director": validateAndRepairToolCallPairs(single),
	} {
		if got := soleOutputFor(t, msgs, pairedCallID); got != pausedIndicator {
			t.Errorf("%s 侧改写了唯一一条 output: got %q, want %q", name, got, pausedIndicator)
		}
	}
}
