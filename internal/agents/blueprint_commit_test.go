package agents

import (
	"encoding/json"
	"strings"
	"testing"

	"yagent/internal/memory"
)

// 这条接缝存在的理由：用户看到的症状是"目录建出来了，文件却是空的"，
// 而当时**日志里一个字都没有** —— I18 要求"没更新"与"更新失败"可区分，
// 而"什么都没发生"也得是一条读得出的账，不能留给人去猜。

func commitWithBlueprint(t *testing.T, withSnapshot bool) *BlueprintCommit {
	t.Helper()
	_, snap := blueprintProject(t)
	src := &testBlueprintSource{root: t.TempDir() + "/blueprint"}
	if withSnapshot {
		src.snap = snap
	}
	view := NewBlueprintView(src, "repo", true, 4000, 1500, nil)
	c := NewBlueprintCommit(view)
	if c == nil {
		t.Fatalf("钩子没建出来")
	}
	return c
}

// 读过 internal/a、以纯文本收尾 → 必须点名"写通道没被调用"以及读过哪几片。
func TestNoSubmissionNoteNamesTheMissingExit(t *testing.T) {
	c := commitWithBlueprint(t, true)
	res := AgentResult{
		Text:   "internal/a 是算法片……（一段直接结束的报告）",
		Memory: openedMemoryOf("internal/a/a.go"),
	}
	got := c.noSubmissionNote(res)
	for _, want := range []string{"agent_exit", "internal/a"} {
		if !strings.Contains(got, want) {
			t.Errorf("回执少了 %q，实得: %q", want, got)
		}
	}
	if !strings.Contains(got, "纯文本") {
		t.Errorf("纯文本收尾这条判据没说出来: %q", got)
	}
}

// 走了 agent_exit 但没填 blueprint[] → 是另一句話（两种"没更新"不该混成一条）。
func TestNoSubmissionNoteDistinguishesEmptyArray(t *testing.T) {
	c := commitWithBlueprint(t, true)
	res := AgentResult{
		Text:   `{"finished":true,"reason":"done"}`,
		Memory: openedMemoryOf("internal/a/a.go"),
	}
	got := c.noSubmissionNote(res)
	if !strings.Contains(got, "blueprint[]") {
		t.Errorf("该说清是数组为空: %q", got)
	}
	if strings.Contains(got, "纯文本") {
		t.Errorf("走了 agent_exit 却被告知其没走: %q", got)
	}
}

// 两种"不该说话"的情形：整套机制没装配、这一轮什么都没打开。
// 留着这两条是因为**多说话同样是一种淹没有用信息**。
func TestNoSubmissionNoteStaysSilentWhenIrrelevant(t *testing.T) {
	if got := commitWithBlueprint(t, false).noSubmissionNote(AgentResult{
		Text: "x", Memory: openedMemoryOf("internal/a/a.go"),
	}); got != "派生还没就绪" {
		t.Errorf("没派生时该说明派生没就绪, 实得 %q", got)
	}

	c := commitWithBlueprint(t, true)
	if got := c.noSubmissionNote(AgentResult{Text: "x"}); got != "" {
		t.Errorf("这一轮没读过任何目录时不该记这条账: %q", got)
	}
	if got := NewBlueprintCommit(nil); got != nil {
		t.Errorf("没有视图时钩子不该存在")
	}
	if (*BlueprintCommit)(nil).noSubmissionNote(AgentResult{Text: "x"}) != "" {
		t.Errorf("nil 接收者必须安全")
	}
}

// openedMemoryOf 造"这一轮真的打开过某个文件"的记忆形态（工具调用参数里有路径）。
func openedMemoryOf(paths ...string) []memory.ChatMessage {
	var calls []memory.ToolCallData
	for i, p := range paths {
		args, _ := json.Marshal(map[string]interface{}{"target_file": p})
		calls = append(calls, memory.ToolCallData{
			ID:   "c" + string(rune('0'+i)),
			Type: "function",
			Function: memory.ToolCallFunction{
				Name:      "read_file",
				Arguments: args,
			},
		})
	}
	return []memory.ChatMessage{{
		Type:      memory.MessageTypeAssistant,
		ToolCalls: calls,
	}}
}
