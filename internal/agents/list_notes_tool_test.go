package agents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yagent/internal/blackboard"
)

// writeRawNoteFile 直接落一个便签文件，用来构造 v1 存量形态（WriteNote 只会写 v2）。
func writeRawNoteFile(t *testing.T, boardID, agent, filename, content string) {
	t.Helper()
	dir := filepath.Join(blackboard.BoardDir(boardID), agent)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func notesRoot(t *testing.T) {
	t.Helper()
	t.Setenv("YAGENT_NOTES_ROOT", t.TempDir())
}

func seed(t *testing.T, boardID, agent, tool, conclusion string, artifacts ...string) string {
	t.Helper()
	ref, err := blackboard.WriteNote(blackboard.NoteInput{
		BoardID: boardID, Agent: agent, Tool: tool, Task: "任务", Status: blackboard.StatusOK,
		Body:        "正文正文正文",
		Handoff:     blackboard.Handoff{Conclusion: conclusion},
		ArtifactIDs: artifacts,
	})
	if err != nil {
		t.Fatalf("WriteNote: %v", err)
	}
	return ref
}

func callListNotes(t *testing.T, boardID string, params map[string]interface{}) string {
	t.Helper()
	ctx := context.Background()
	if boardID != "" {
		ctx = blackboard.WithBoardID(ctx, boardID)
	}
	got, err := executeListNotes(ctx, params, func() string { return "" })
	if err != nil {
		t.Fatalf("executeListNotes: %v", err)
	}
	text, ok := got.(string)
	if !ok {
		t.Fatalf("result type = %T, want string", got)
	}
	return text
}

func TestListNotesWithoutBoard(t *testing.T) {
	notesRoot(t)
	got := callListNotes(t, "", nil)
	if !strings.Contains(got, "no active board session") {
		t.Errorf("got %q, want the board-less error text", got)
	}
}

func TestListNotesEmptyBoard(t *testing.T) {
	notesRoot(t)
	got := callListNotes(t, "b1", nil)
	if !strings.Contains(got, "no notes on board (b1)") {
		t.Errorf("got %q", got)
	}
}

func TestListNotesLineFields(t *testing.T) {
	notesRoot(t)
	ref := seed(t, "b1", "repo_agent", "delegate_repo", "TOML 库是 BurntSushi", "20261013-000000-aaaaaaaa")

	got := callListNotes(t, "b1", nil)
	if !strings.Contains(got, "1 total") {
		t.Errorf("header wrong:\n%s", got)
	}
	if !strings.Contains(got, ref) {
		t.Errorf("ref missing:\n%s", got)
	}
	if !strings.Contains(got, "delegate_repo") || !strings.Contains(got, "TOML 库是 BurntSushi") {
		t.Errorf("tool/conclusion missing:\n%s", got)
	}
	if !strings.Contains(got, "artifact:20261013-000000-aaaaaaaa") {
		t.Errorf("artifact id missing:\n%s", got)
	}
	// 枚举必须便宜：绝不把正文吐出来。
	if strings.Contains(got, "正文正文正文") {
		t.Errorf("list_notes must not return bodies:\n%s", got)
	}
}

func TestListNotesAgentFilterUsesAlias(t *testing.T) {
	notesRoot(t)
	seed(t, "b1", "repo_agent", "delegate_repo", "repo 结论")
	seed(t, "b1", "coding_agent", "delegate_coding", "coding 结论")

	short := callListNotes(t, "b1", map[string]interface{}{"agent": "repo"})
	if !strings.Contains(short, "repo 结论") || strings.Contains(short, "coding 结论") {
		t.Errorf("agent=repo filter wrong:\n%s", short)
	}
	full := callListNotes(t, "b1", map[string]interface{}{"agent": "repo_agent"})
	if !strings.Contains(full, "repo 结论") {
		t.Errorf("agent=repo_agent should list the same notes:\n%s", full)
	}
	none := callListNotes(t, "b1", map[string]interface{}{"agent": "devops"})
	if !strings.Contains(none, "no notes on agent devops") {
		t.Errorf("unknown agent should say so:\n%s", none)
	}
}

func TestListNotesPagination(t *testing.T) {
	notesRoot(t)
	for i := 0; i < 3; i++ {
		seed(t, "b1", "repo_agent", "delegate_repo", string(rune('a'+i))+" 结论")
	}

	page1 := callListNotes(t, "b1", map[string]interface{}{"limit": 2})
	if !strings.Contains(page1, "showing 1-2") || !strings.Contains(page1, "[more: 1 notes left; continue with offset=2]") {
		t.Errorf("first page wrong:\n%s", page1)
	}
	page2 := callListNotes(t, "b1", map[string]interface{}{"limit": 2, "offset": 2})
	if !strings.Contains(page2, "showing 3-3") || strings.Contains(page2, "more:") {
		t.Errorf("last page wrong:\n%s", page2)
	}
	// limit 有硬上限，越界 offset 要给可纠正的提示而不是空串。
	clamped := callListNotes(t, "b1", map[string]interface{}{"limit": 9999})
	if strings.Count(clamped, "\n") > 5 {
		t.Errorf("limit was not clamped:\n%s", clamped)
	}
	over := callListNotes(t, "b1", map[string]interface{}{"offset": 9})
	if !strings.Contains(over, "past the end") {
		t.Errorf("over-range offset wrong:\n%s", over)
	}
}

// read_note 必须把 handoff 块渲染出来——那是这条便签的主要信息。
func TestReadNoteRendersHandoff(t *testing.T) {
	notesRoot(t)
	ref, err := blackboard.WriteNote(blackboard.NoteInput{
		BoardID: "b1", Agent: "repo_agent", Tool: "delegate_repo", Status: blackboard.StatusOK,
		Body:        "第一行正文\n第二行正文",
		ArtifactIDs: []string{"20261013-000000-bbbbbbbb"},
		Handoff: blackboard.Handoff{
			Conclusion:    "配置写回走 persist.go",
			Facts:         []blackboard.Fact{{Claim: "SaveConfig 是原子替换", Where: "internal/config/persist.go:41"}},
			OpenQuestions: []string{"热加载要不要同步"},
		},
	})
	if err != nil {
		t.Fatalf("WriteNote: %v", err)
	}

	got, err := executeReadNote(context.Background(), map[string]interface{}{"ref": ref}, func() string { return "b1" })
	if err != nil {
		t.Fatalf("executeReadNote: %v", err)
	}
	text := got.(string)

	for _, want := range []string{
		"--- handoff ---",
		"结论: 配置写回走 persist.go",
		"- SaveConfig 是原子替换  @ internal/config/persist.go:41",
		"待确认: 热加载要不要同步",
		"--- body ---",
		"第二行正文",
		"artifacts: 20261013-000000-bbbbbbbb",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("read_note output missing %q:\n%s", want, text)
		}
	}
	// 合规 handoff 不该出现降级标记。
	if strings.Contains(text, "degraded:") {
		t.Errorf("compliant handoff should not advertise degradation:\n%s", text)
	}
}

// v1 存量便签读出来要诚实标降级。
func TestReadNoteMarksV1Note(t *testing.T) {
	notesRoot(t)
	v1 := "---\n{\n  \"version\": 1,\n  \"note_ref\": \"repo_agent/20250601T090000Z_a1b2\",\n  \"board_id\": \"b1\",\n  \"agent\": \"repo_agent\",\n  \"tool\": \"delegate_repo\",\n  \"task\": \"t\",\n  \"summary\": \"当年那句摘要\",\n  \"body_size\": 8,\n  \"status\": \"ok\"\n}\n---\n正文内容在此\n"
	writeRawNoteFile(t, "b1", "repo_agent", "20250601T090000Z_a1b2.md", v1)

	got, err := executeReadNote(context.Background(), map[string]interface{}{"ref": "repo_agent/20250601T090000Z_a1b2"}, func() string { return "b1" })
	if err != nil {
		t.Fatalf("executeReadNote: %v", err)
	}
	text := got.(string)
	if !strings.Contains(text, "degraded: v1") {
		t.Errorf("v1 note must be marked:\n%s", text)
	}
	if !strings.Contains(text, "结论: 当年那句摘要") {
		t.Errorf("v1 conclusion should come from Summary:\n%s", text)
	}
	if !strings.Contains(text, "正文内容在此") {
		t.Errorf("v1 body must still be readable:\n%s", text)
	}
}
