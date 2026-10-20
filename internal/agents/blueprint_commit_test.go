package agents

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"yagent/internal/blackboard"
	"yagent/internal/blueprint"
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

// ---- 账目回到对话（2026-10-20）----
//
// 症状：一轮 Repo 交了 4 条散文、一次文件都没读，四条全被 outside-evidence-set 拒收，
// 而 Director 收到的仍是"这次委派成功"—— 拒收只进 slog，模型也不知道自己白交了，
// 下一轮同一批散文还会再交一次。Commit 因此多了一条出口：账目随结果同行。

func exitedResult(items []blueprint.FoldItem, opened ...string) AgentResult {
	return AgentResult{
		Text:          `{"finished":true,"reason":"done"}`,
		Deliverable:   "这是交付物正文。",
		Summary:       "这是交付物正文。",
		Memory:        openedMemoryOf(opened...),
		Blueprint:     items,
		EvidenceFiles: opened,
	}
}

// 全被拒收：账目必须报出原因与被点名的片，而且不能长得像成功。
func TestCommitAccountNamesEachRejectionReason(t *testing.T) {
	c := commitWithBlueprint(t, true)
	res := exitedResult([]blueprint.FoldItem{{
		Section: "modules", Module: "internal/a", Field: "purpose",
		Claim: "a claim about a module this run never opened", Where: "internal/a/a.go",
	}}, "internal/b/b.go")
	account := c.Commit(context.Background(), "Repo-Agent", res)
	for _, want := range []string{"提交 1 条", "写入 0 条", "outside-evidence-set", "internal/a"} {
		if !strings.Contains(account, want) {
			t.Errorf("账目少了 %q，实得: %q", want, account)
		}
	}
}

// 顺利落地：也要说一句话（Director 据此判断"这轮的结论进蓝图了没有"），但不许出现拒收。
func TestCommitAccountOnLandedSubmission(t *testing.T) {
	c := commitWithBlueprint(t, true)
	res := exitedResult([]blueprint.FoldItem{{
		Section: "modules", Module: "internal/a", Field: "purpose",
		Claim: "holds the fixture algorithms", Where: "internal/a/a.go",
	}}, "internal/a/a.go")
	account := c.Commit(context.Background(), "Repo-Agent", res)
	if !strings.Contains(account, "写入 1 条") {
		t.Errorf("落地账目不对: %q", account)
	}
	if strings.Contains(account, "拒收") {
		t.Errorf("没有拒收时不该提拒收: %q", account)
	}
}

// 没装配蓝图（nil 钩子 / 空存储根）⇒ 闭嘴。两处"无话可说"混进对话只会淹掉要紧那句。
func TestCommitAccountStaysSilentWhenUnwired(t *testing.T) {
	var nilCommit *BlueprintCommit
	if got := nilCommit.Commit(context.Background(), "Repo-Agent", exitedResult(nil)); got != "" {
		t.Errorf("nil 钩子说话了: %q", got)
	}
	src := &testBlueprintSource{root: "", snap: nil}
	view := NewBlueprintView(src, "repo", true, 4000, 1500, nil)
	c := NewBlueprintCommit(view)
	if c == nil {
		t.Fatalf("钩子该建出来（root 为空是运行期判据，不是构造判据）")
	}
	res := exitedResult([]blueprint.FoldItem{{Section: "modules", Module: "internal/a", Field: "purpose", Claim: "x"}})
	if got := c.Commit(context.Background(), "Repo-Agent", res); got != "" {
		t.Errorf("没有存储根时不该说话: %q", got)
	}
}

// 承重的一条：账目必须同时到 Director 的这一次决策（工具输出）与便签正文（下一次委派）。
// 只到一处 = 半个修复：工具输出会随压缩消失，便签索引又不含正文。
func TestBlueprintAccountReachesDirectorTextAndNote(t *testing.T) {
	res := AgentResult{
		Deliverable:      "这是交付物正文。",
		Summary:          "这是交付物正文。",
		BlueprintAccount: "蓝图：提交 4 条，写入 0 条；拒收 4 条：outside-evidence-set（片：internal/tui）",
	}
	out := FormatForDirector("delegate_repo", res, "", 0)
	if !strings.Contains(out, "[blueprint] 蓝图：提交 4 条") {
		t.Errorf("Director 的工具输出少了账目:\n%s", out)
	}

	a := newResumeTestDirector(t)
	a.SetBoardScope("bp-account-"+strings.ReplaceAll(t.Name(), "/", "_"), "bp-account-task")
	ref := a.writeDelegateNote("repo_agent", "delegate_repo", "讲一下架构", res, nil)
	if ref == "" {
		t.Fatal("便签没落")
	}
	note, err := blackboard.ReadNote(a.boardID, ref)
	if err != nil {
		t.Fatalf("ReadNote(%s): %v", ref, err)
	}
	if !strings.Contains(note.Body, "> [blueprint] 蓝图：提交 4 条") {
		t.Errorf("便签正文尾部少了账目:\n%s", note.Body)
	}
	if !strings.Contains(note.Body, "这是交付物正文") {
		t.Errorf("尾注挤掉了交付物正文:\n%s", note.Body)
	}
	// 账目不许冒充结论：索引行每轮从磁盘重扫，混进 conclusion 就是程序替模型下了一句架构判断。
	if strings.Contains(note.Handoff.Conclusion, "蓝图：") {
		t.Errorf("conclusion 冒充了账目: %q", note.Handoff.Conclusion)
	}
}
