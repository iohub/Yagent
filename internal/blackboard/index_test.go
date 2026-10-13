package blackboard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeNoteFileAt 用真实编码器在指定 stem 落一张便签。
// 手写 stem 是为了让"最新优先"的断言不依赖 WriteNote 里的 time.Now。
func writeNoteFileAt(t *testing.T, boardID, agent, stem string, note Note) {
	t.Helper()
	note.Version = noteSchemaVersion
	note.BoardID = SanitizeBoardID(boardID)
	note.Agent = agent
	note.Type = "deliverable"
	note.NoteRef = agent + "/" + stem
	if note.BodySize == 0 {
		note.BodySize = runeLen(note.Body)
	}
	if note.Handoff.Conclusion == "" {
		note.Handoff.Conclusion = note.Summary
	}
	data, err := encodeNote(note)
	if err != nil {
		t.Fatalf("encodeNote: %v", err)
	}
	dir := filepath.Join(BoardDir(boardID), agent)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, stem+noteFileExt), data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestBuildIndexNewestFirstAndCap(t *testing.T) {
	t.Setenv("YAGENT_NOTES_ROOT", t.TempDir())
	board := "b1"

	stems := []string{"20261013T000001Z_aaaa", "20261013T000002Z_bbbb", "20261013T000003Z_cccc",
		"20261013T000004Z_dddd", "20261013T000005Z_eeee"}
	for i, s := range stems {
		writeNoteFileAt(t, board, "repo_agent", s, Note{
			Tool:    "delegate_repo",
			Task:    "任务 " + string(rune('0'+i)),
			Summary: "结论 " + string(rune('0'+i)),
			Body:    "正文",
			Status:  StatusOK,
		})
	}

	text, total, err := BuildIndex(board, IndexOptions{MaxNotes: 3})
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	if total != len(stems) {
		t.Errorf("total = %d, want %d", total, len(stems))
	}
	if !strings.HasPrefix(text, IndexHeaderPrefix+board+", 5 notes") {
		t.Errorf("header wrong:\n%s", text)
	}

	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	// 1 行表头 + 3 条便签 + 1 行尾巴。
	if len(lines) != 5 {
		t.Fatalf("lines = %d, want 5:\n%s", len(lines), text)
	}
	if !strings.Contains(lines[1], stems[len(stems)-1]) {
		t.Errorf("newest first violated: %q", lines[1])
	}
	if !strings.Contains(lines[1], "结论 4") {
		t.Errorf("conclusion missing from newest line: %q", lines[1])
	}
	// MaxNotes=3 ⇒ 列出最新的三条（stems[4]、stems[3]、stems[2]），最旧两条被隐藏。
	for _, hidden := range []string{stems[0], stems[1]} {
		if strings.Contains(text, hidden) {
			t.Errorf("note %q is older than MaxNotes and must not be listed:\n%s", hidden, text)
		}
	}
	if !strings.Contains(text, stems[2]) {
		t.Errorf("the third-newest note should still be listed: %q", stems[2])
	}
	if !strings.Contains(lines[4], "[2 older notes not listed") {
		t.Errorf("footer wrong: %q", lines[4])
	}
}

func TestBuildIndexV1NoteUsesSummaryAsConclusion(t *testing.T) {
	t.Setenv("YAGENT_NOTES_ROOT", t.TempDir())
	writeNoteFileAt(t, "b1", "repo_agent", "20261013T000001Z_aaaa", Note{Version: 1, Summary: "v1 的摘要", Body: "正文", Status: StatusOK})

	text, _, err := BuildIndex("b1", IndexOptions{MaxNotes: 5})
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	if !strings.Contains(text, "v1 的摘要") {
		t.Errorf("v1 note must fall back to Summary:\n%s", text)
	}
}

func TestBuildIndexSkipsUnreadableNotes(t *testing.T) {
	root := t.TempDir()
	t.Setenv("YAGENT_NOTES_ROOT", root)
	writeNoteFileAt(t, "b1", "repo_agent", "20261013T000002Z_bbbb", Note{Summary: "好的那条", Body: "正文"})

	dir := filepath.Join(BoardDir("b1"), "repo_agent")
	if err := os.WriteFile(filepath.Join(dir, "20261013T000001Z_bad.md"), []byte("没有 front-matter"), 0o600); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}

	text, total, err := BuildIndex("b1", IndexOptions{MaxNotes: 5})
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	if total != 2 {
		t.Errorf("total = %d, want 2 (count is cheap, no decode)", total)
	}
	if !strings.Contains(text, "好的那条") {
		t.Errorf("readable note missing:\n%s", text)
	}
	if !strings.Contains(text, "1 unreadable notes skipped") {
		t.Errorf("skip not reported:\n%s", text)
	}
}

func TestBuildIndexEmptyAndValve(t *testing.T) {
	t.Setenv("YAGENT_NOTES_ROOT", t.TempDir())

	if text, total, err := BuildIndex("nonexistent", IndexOptions{}); err != nil || text != "" || total != 0 {
		t.Errorf("missing board → (%q,%d,%v), want (\"\",0,nil)", text, total, err)
	}

	writeNoteFileAt(t, "b1", "repo_agent", "20261013T000001Z_aaaa", Note{Summary: "s", Body: "正文"})

	// index_max_notes = 0 是应急阀门：语义必须是"关掉"，不是"用默认"。
	if text, total, err := BuildIndex("b1", IndexOptions{MaxNotes: 0}); err != nil || text != "" || total != 0 {
		t.Errorf("valve 0 → (%q,%d,%v), want (\"\",0,nil)", text, total, err)
	}

	// <0 走默认值。
	if text, _, err := BuildIndex("b1", IndexOptions{MaxNotes: -1}); err != nil || !strings.Contains(text, "repo_agent/") {
		t.Errorf("negative → (%q, %v), want default-sized index", text, err)
	}
}

func TestBuildIndexOneLinePerNoteAndKeepsRef(t *testing.T) {
	t.Setenv("YAGENT_NOTES_ROOT", t.TempDir())
	writeNoteFileAt(t, "b1", "repo_agent", "20261013T000001Z_aaaa", Note{
		Tool:        "delegate_repo",
		Task:        "多行\n任务\n描述",
		Handoff:     Handoff{Conclusion: "多行\n结论\n也要压平"},
		ArtifactIDs: []string{"20261013-000000-aaaaaaaa"},
		Body:        strings.Repeat("正文", 200),
		Status:      StatusOK,
	})

	text, _, err := BuildIndex("b1", IndexOptions{MaxNotes: 5, LineChars: 160})
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2 (header + one note line):\n%s", len(lines), lines)
	}
	line := lines[1]
	if !strings.Contains(line, "repo_agent/20261013T000001Z_aaaa") {
		t.Errorf("note_ref must never be truncated (it is the retrieval handle): %q", line)
	}
	if !strings.Contains(line, "artifact:20261013-000000-aaaaaaaa") {
		t.Errorf("artifact id missing: %q", line)
	}
	if strings.Contains(line, "\n") {
		t.Errorf("line contains newline: %q", line)
	}
	if n := runeLen(line); n > 240 {
		t.Errorf("line badly over budget: %d runes: %q", n, line)
	}
}

func TestFormatIndexLineTinyBudgetStillHasRef(t *testing.T) {
	m := NoteMeta{
		NoteRef:    "repo_agent/20261013T000001Z_aaaa",
		Tool:       "delegate_repo",
		Task:       strings.Repeat("很长的任务描述", 10),
		Conclusion: strings.Repeat("很长的结论", 10),
		BodySize:   1234,
	}
	line := formatIndexLine(m, 40)
	if !strings.HasPrefix(line, m.NoteRef) {
		t.Errorf("ref must survive any budget: %q", line)
	}
	if !strings.Contains(line, "1234c") {
		t.Errorf("body size missing: %q", line)
	}
}
