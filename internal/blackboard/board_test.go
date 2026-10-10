package blackboard

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestNotesRootDir(t *testing.T) {
	t.Setenv("YAGENT_NOTES_ROOT", "/tmp/test-notes")
	if got := NotesRootDir(); got != "/tmp/test-notes" {
		t.Errorf("NotesRootDir() = %q, want %q", got, "/tmp/test-notes")
	}
}

func TestSanitizeBoardID(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"abc-123", "abc-123"},
		{"hello world", "hello_world"},
		{"../../../etc", ".._.._.._etc"},
		{"", ""},
		{strings.Repeat("a", 200), strings.Repeat("a", 128)},
		{"valid_ID.0", "valid_ID.0"},
	}
	for _, tt := range tests {
		got := SanitizeBoardID(tt.input)
		if tt.input == "" {
			if !strings.HasPrefix(got, "adhoc_") {
				t.Errorf("SanitizeBoardID(%q) = %q, want adhoc_ prefix", tt.input, got)
			}
			continue
		}
		if got != tt.want {
			t.Errorf("SanitizeBoardID(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestGenerateAdhocID(t *testing.T) {
	id := GenerateAdhocID()
	if !strings.HasPrefix(id, "adhoc_") {
		t.Errorf("GenerateAdhocID() = %q, want adhoc_ prefix", id)
	}
	parts := strings.Split(id, "_")
	if len(parts) != 3 {
		t.Errorf("GenerateAdhocID() = %q, want 3 parts", id)
	}
}

func TestAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "test.txt")
	data := []byte("hello world")

	if err := AtomicWrite(path, data); err != nil {
		t.Fatalf("AtomicWrite() error: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error: %v", err)
	}
	if string(got) != "hello world" {
		t.Errorf("file content = %q, want %q", got, "hello world")
	}

	tmpPath := path + tmpFileExt
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Errorf("tmp file still exists after rename")
	}
}

func TestGenerateNoteFilename(t *testing.T) {
	f1, err := GenerateNoteFilename()
	if err != nil {
		t.Fatalf("GenerateNoteFilename() error: %v", err)
	}
	f2, err := GenerateNoteFilename()
	if err != nil {
		t.Fatalf("GenerateNoteFilename() error: %v", err)
	}
	if f1 == f2 {
		t.Errorf("two consecutive filenames should differ: %q", f1)
	}
	if !strings.HasSuffix(f1, noteFileExt) {
		t.Errorf("filename %q should end with %q", f1, noteFileExt)
	}
}

func TestWithBoardID(t *testing.T) {
	ctx := context.Background()
	if got := BoardIDFrom(ctx); got != "" {
		t.Errorf("BoardIDFrom(empty ctx) = %q, want empty", got)
	}
	ctx = WithBoardID(ctx, "test-board")
	if got := BoardIDFrom(ctx); got != "test-board" {
		t.Errorf("BoardIDFrom() = %q, want %q", got, "test-board")
	}
}

func TestWriteAndReadNote(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("YAGENT_NOTES_ROOT", dir)

	input := NoteInput{
		BoardID:     "board-1",
		Agent:       "repo_agent",
		Tool:        "delegate_repo",
		Task:        "analyze the codebase structure",
		Summary:     "Found 3 main packages",
		Body:        "## Analysis\nThe codebase has 3 main packages: agents, tools, memory.",
		ArtifactIDs: []string{"art_001"},
		Status:      "ok",
	}

	noteRef, err := WriteNote(input)
	if err != nil {
		t.Fatalf("WriteNote() error: %v", err)
	}
	if !strings.HasPrefix(noteRef, "repo_agent/") {
		t.Errorf("noteRef = %q, want repo_agent/ prefix", noteRef)
	}

	note, err := ReadNote("board-1", noteRef)
	if err != nil {
		t.Fatalf("ReadNote() error: %v", err)
	}
	if note.Agent != "repo_agent" {
		t.Errorf("note.Agent = %q, want %q", note.Agent, "repo_agent")
	}
	if note.Summary != "Found 3 main packages" {
		t.Errorf("note.Summary = %q, want %q", note.Summary, "Found 3 main packages")
	}
	if !strings.Contains(note.Body, "3 main packages") {
		t.Errorf("note.Body should contain body text, got %q", note.Body)
	}
	if note.Status != "ok" {
		t.Errorf("note.Status = %q, want %q", note.Status, "ok")
	}
	if len(note.ArtifactIDs) != 1 || note.ArtifactIDs[0] != "art_001" {
		t.Errorf("note.ArtifactIDs = %v, want [art_001]", note.ArtifactIDs)
	}
	if note.BodySize <= 0 {
		t.Errorf("note.BodySize = %d, want > 0", note.BodySize)
	}
}

func TestWriteNoteTruncatesTaskAndSummary(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("YAGENT_NOTES_ROOT", dir)

	longTask := strings.Repeat("x", 500)
	longSummary := strings.Repeat("y", 400)

	input := NoteInput{
		BoardID: "board-2",
		Agent:   "coding_agent",
		Task:    longTask,
		Summary: longSummary,
		Body:    "body",
		Status:  "ok",
	}

	noteRef, err := WriteNote(input)
	if err != nil {
		t.Fatalf("WriteNote() error: %v", err)
	}

	note, err := ReadNote("board-2", noteRef)
	if err != nil {
		t.Fatalf("ReadNote() error: %v", err)
	}
	if len([]rune(note.Task)) > 301 {
		t.Errorf("task should be truncated to ~300 runes, got %d", len([]rune(note.Task)))
	}
	if len([]rune(note.Summary)) > 201 {
		t.Errorf("summary should be truncated to ~200 runes, got %d", len([]rune(note.Summary)))
	}
}

func TestListNotes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("YAGENT_NOTES_ROOT", dir)

	for i := 0; i < 5; i++ {
		_, err := WriteNote(NoteInput{
			BoardID: "board-3",
			Agent:   "repo_agent",
			Task:    "task",
			Summary: "summary",
			Body:    "body",
			Status:  "ok",
		})
		if err != nil {
			t.Fatalf("WriteNote() error: %v", err)
		}
	}
	_, err := WriteNote(NoteInput{
		BoardID: "board-3",
		Agent:   "coding_agent",
		Task:    "task",
		Summary: "summary",
		Body:    "body",
		Status:  "ok",
	})
	if err != nil {
		t.Fatalf("WriteNote() error: %v", err)
	}

	metas, err := ListNotes("board-3", "repo_agent", 3)
	if err != nil {
		t.Fatalf("ListNotes() error: %v", err)
	}
	if len(metas) != 3 {
		t.Errorf("ListNotes(repo_agent, 3) returned %d entries, want 3", len(metas))
	}

	allMetas, err := ListNotes("board-3", "", 0)
	if err != nil {
		t.Fatalf("ListNotes(all) error: %v", err)
	}
	if len(allMetas) != 6 {
		t.Errorf("ListNotes(all) returned %d entries, want 6", len(allMetas))
	}
}

func TestListNotesEmptyBoard(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("YAGENT_NOTES_ROOT", dir)

	metas, err := ListNotes("nonexistent", "agent", 10)
	if err != nil {
		t.Fatalf("ListNotes() error: %v", err)
	}
	if len(metas) != 0 {
		t.Errorf("ListNotes(nonexistent) = %d entries, want 0", len(metas))
	}
}

func TestConcurrentWriteNotes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("YAGENT_NOTES_ROOT", dir)

	const goroutines = 20
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, err := WriteNote(NoteInput{
				BoardID: "concurrent-board",
				Agent:   "repo_agent",
				Task:    "concurrent task",
				Summary: "concurrent summary",
				Body:    strings.Repeat("x", 100),
				Status:  "ok",
			})
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent WriteNote() error: %v", err)
	}

	metas, err := ListNotes("concurrent-board", "repo_agent", 0)
	if err != nil {
		t.Fatalf("ListNotes() error: %v", err)
	}
	if len(metas) != goroutines {
		t.Errorf("expected %d notes, got %d", goroutines, len(metas))
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	original := Note{
		Version:     1,
		NoteRef:     "agent/stem",
		BoardID:     "board",
		Agent:       "agent",
		Type:        "deliverable",
		CreatedAt:   "2025-06-01T09:00:00Z",
		Tool:        "delegate_repo",
		Task:        "test task",
		Summary:     "test summary",
		ArtifactIDs: []string{"a1", "a2"},
		BodySize:    11,
		Status:      "ok",
		Body:        "hello world",
	}

	data, err := encodeNote(original)
	if err != nil {
		t.Fatalf("encodeNote() error: %v", err)
	}

	decoded, err := decodeNote(data)
	if err != nil {
		t.Fatalf("decodeNote() error: %v", err)
	}

	if decoded.NoteRef != original.NoteRef {
		t.Errorf("NoteRef = %q, want %q", decoded.NoteRef, original.NoteRef)
	}
	if decoded.Body != original.Body {
		t.Errorf("Body = %q, want %q", decoded.Body, original.Body)
	}
	if len(decoded.ArtifactIDs) != 2 {
		t.Errorf("ArtifactIDs = %v, want 2 items", decoded.ArtifactIDs)
	}
}

func TestDecodeNoteCorrupted(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"no delimiter", "just plain text"},
		{"unterminated", "---\n{\"version\":1}\nno closing"},
		{"bad json", "---\n{invalid}\n---\nbody"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decodeNote([]byte(tt.input))
			if err == nil {
				t.Errorf("decodeNote(%q) should fail", tt.input)
			}
		})
	}
}

func TestParseNoteRef(t *testing.T) {
	agent, stem, err := parseNoteRef("repo_agent/20250601T090000Z_a1b2")
	if err != nil {
		t.Fatalf("parseNoteRef() error: %v", err)
	}
	if agent != "repo_agent" {
		t.Errorf("agent = %q, want %q", agent, "repo_agent")
	}
	if stem != "20250601T090000Z_a1b2" {
		t.Errorf("stem = %q, want %q", stem, "20250601T090000Z_a1b2")
	}

	_, _, err = parseNoteRef("invalid")
	if err == nil {
		t.Error("parseNoteRef(invalid) should fail")
	}
	_, _, err = parseNoteRef("/stem")
	if err == nil {
		t.Error("parseNoteRef(/stem) should fail")
	}
}

func TestReadNoteNotFound(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("YAGENT_NOTES_ROOT", dir)

	_, err := ReadNote("board", "agent/nonexistent")
	if err == nil {
		t.Error("ReadNote(nonexistent) should fail")
	}
}
