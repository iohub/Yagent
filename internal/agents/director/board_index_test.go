package director

import (
	"strings"
	"testing"

	"yagent/internal/blackboard"
	"yagent/internal/llm"
)

func newBoardPlanner(t *testing.T, boardID string) *Planner {
	t.Helper()
	return NewPlanner(PlannerConfig{BoardID: boardID}, &RunState{})
}

// seedNote 往临时便签板写一张可直接由 Director 侧构造的便签。
func seedNote(t *testing.T, boardID, agent, conclusion string) {
	t.Helper()
	if _, err := blackboard.WriteNote(blackboard.NoteInput{
		BoardID: boardID, Agent: agent, Tool: "delegate_" + strings.TrimSuffix(agent, "_agent"),
		Task: "任务描述", Status: blackboard.StatusOK,
		Handoff: blackboard.Handoff{Conclusion: conclusion},
	}); err != nil {
		t.Fatalf("WriteNote: %v", err)
	}
}

func countIndexMessages(msgs []llm.Message) int {
	n := 0
	for _, m := range msgs {
		if strings.HasPrefix(strings.TrimSpace(m.Content), blackboard.IndexHeaderPrefix) {
			n++
		}
	}
	return n
}

func TestMessagesForLLMCallAppendsIndex(t *testing.T) {
	t.Setenv("YAGENT_NOTES_ROOT", t.TempDir())
	seedNote(t, "b1", "repo_agent", "TOML 库是 BurntSushi v1.5.0")

	p := newBoardPlanner(t, "b1")
	base := []llm.Message{
		{Role: llm.RoleSystem, Content: "system prompt"},
		{Role: llm.RoleUser, Content: "把模型选择持久化"},
	}

	got := p.messagesForLLMCall(base)

	if len(got) != len(base)+1 {
		t.Fatalf("messages = %d, want %d", len(got), len(base)+1)
	}
	last := got[len(got)-1]
	if last.Role != llm.RoleUser || !strings.Contains(last.Content, "BurntSushi") {
		t.Errorf("last message should be the index, got %+v", last)
	}
	// 外层切片必须保持无索引：这是"派生态、瞬态"的不变量。
	// 一旦索引留在 messages 里，下一步的压缩会把目录算进预算，紧急压缩还会把它当用户输入折叠掉。
	if countIndexMessages(base) != 0 {
		t.Error("messagesForLLMCall mutated the caller's slice")
	}
	for i := range base {
		if got[i].Content != base[i].Content || got[i].Role != base[i].Role {
			t.Errorf("prefix message %d changed: %+v vs %+v", i, got[i], base[i])
		}
	}
}

// 同一步内 LLM 重试会多次调用本函数：目录只能有一份。
func TestMessagesForLLMCallIsIdempotent(t *testing.T) {
	t.Setenv("YAGENT_NOTES_ROOT", t.TempDir())
	seedNote(t, "b1", "repo_agent", "结论")

	p := newBoardPlanner(t, "b1")
	base := []llm.Message{{Role: llm.RoleSystem, Content: "s"}, {Role: llm.RoleUser, Content: "task"}}

	once := p.messagesForLLMCall(base)
	twice := p.messagesForLLMCall(once)

	if n := countIndexMessages(once); n != 1 {
		t.Errorf("first call put %d index messages, want 1", n)
	}
	if n := countIndexMessages(twice); n != 1 {
		t.Errorf("second call put %d index messages, want 1 (must strip before re-adding)", n)
	}
	if len(twice) != len(once) {
		t.Errorf("length grew across retries: %d → %d", len(once), len(twice))
	}
}

func TestMessagesForLLMCallNoBoardNoNotes(t *testing.T) {
	t.Setenv("YAGENT_NOTES_ROOT", t.TempDir())
	base := []llm.Message{{Role: llm.RoleUser, Content: "task"}}

	if got := newBoardPlanner(t, "").messagesForLLMCall(base); len(got) != 1 {
		t.Errorf("empty BoardID must be a no-op, got %d messages", len(got))
	}
	// board 存在但一张便签都没有：不注入空块。
	if got := newBoardPlanner(t, "empty-board").messagesForLLMCall(base); len(got) != 1 || countIndexMessages(got) != 0 {
		t.Errorf("empty board must not inject an empty index, got %d messages", len(got))
	}
}

// 第二/三档压缩把 tool 消息（note:/artifact 引用的唯一栖身处）整段丢掉。
// 索引因此不能依赖会话历史 —— 它必须能在重建后的 [system, user] 形状上重新出现。
func TestIndexSurvivesEmergencyCompressionShape(t *testing.T) {
	t.Setenv("YAGENT_NOTES_ROOT", t.TempDir())
	seedNote(t, "b1", "coding_agent", "空格=永久切换已实现")

	p := newBoardPlanner(t, "b1")
	// 压缩后的形状：只剩 system + 一条折叠了历史的 user 消息，[Sub-Agent Result] 全部消失。
	compressed := []llm.Message{
		{Role: llm.RoleSystem, Content: "system prompt"},
		{Role: llm.RoleUser, Content: "### 执行过程总结（LLM 生成）\n之前调查过配置写回。"},
	}
	if strings.Contains(compressed[1].Content, "note:") {
		t.Fatal("fixture wrong: compressed messages must have lost the note refs")
	}

	got := p.messagesForLLMCall(compressed)
	if countIndexMessages(got) != 1 {
		t.Fatalf("index must be regenerated from disk after compression, got %d", countIndexMessages(got))
	}
	if !strings.Contains(got[len(got)-1].Content, "空格=永久切换已实现") {
		t.Errorf("index lost the prior conclusion:\n%s", got[len(got)-1].Content)
	}
}
