package blackboard

import (
	"strings"
	"testing"
)

func TestParseRef(t *testing.T) {
	tests := []struct {
		input     string
		wantKind  string
		wantValue string
	}{
		{"agent:repo_agent", "agent", "repo_agent"},
		{"note:repo_agent/stem", "note", "repo_agent/stem"},
		{"artifact:art_001", "artifact", "art_001"},
		{"invalid", "", "invalid"},
		{":empty", "", "empty"},
	}
	for _, tt := range tests {
		kind, value := parseRef(tt.input)
		if kind != tt.wantKind || value != tt.wantValue {
			t.Errorf("parseRef(%q) = (%q, %q), want (%q, %q)", tt.input, kind, value, tt.wantKind, tt.wantValue)
		}
	}
}

func TestResolveRefsEmpty(t *testing.T) {
	pack := ResolveRefs("board", nil, nil)
	if pack.Text() != "" {
		t.Errorf("empty refs should produce empty text, got %q", pack.Text())
	}
}

func TestResolveRefsDedup(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("YAGENT_NOTES_ROOT", dir)

	noteRef, _ := WriteNote(NoteInput{
		BoardID: "board", Agent: "repo", Task: "t", Summary: "s", Body: "b", Status: "ok",
	})

	pack := ResolveRefs("board", []string{"note:" + noteRef, "note:" + noteRef}, nil)
	noteCount := 0
	for _, e := range pack.Entries {
		if e.Kind == EntryNote {
			noteCount++
		}
	}
	if noteCount != 1 {
		t.Errorf("duplicate refs should be deduped, got %d note entries", noteCount)
	}
}

func TestResolveRefsMaxLimit(t *testing.T) {
	refs := make([]string, 20)
	for i := range refs {
		refs[i] = "artifact:art_" + strings.Repeat("x", 5)
	}
	pack := ResolveRefs("board", refs, func(id string) bool { return true })
	total := len(pack.Entries) + len(pack.Unresolved)
	if total > BudgetMaxRefs {
		t.Errorf("refs should be capped at %d, got %d", BudgetMaxRefs, total)
	}
}

func TestResolveRefsUnresolved(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("YAGENT_NOTES_ROOT", dir)

	pack := ResolveRefs("board", []string{
		"note:nonexistent/ref",
		"artifact:missing_art",
		"badformat",
	}, func(id string) bool { return false })

	if len(pack.Unresolved) != 3 {
		t.Errorf("expected 3 unresolved, got %d: %v", len(pack.Unresolved), pack.Unresolved)
	}
}

func TestResolveRefsAgentRef(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("YAGENT_NOTES_ROOT", dir)

	for i := 0; i < 5; i++ {
		WriteNote(NoteInput{
			BoardID: "board", Agent: "repo", Task: "t", Summary: "s", Body: "body text", Status: "ok",
		})
	}

	pack := ResolveRefs("board", []string{"agent:repo"}, nil)
	noteCount := 0
	for _, e := range pack.Entries {
		if e.Kind == EntryNote {
			noteCount++
		}
	}
	if noteCount > BudgetAgentMaxNotes {
		t.Errorf("agent: ref should return at most %d notes, got %d", BudgetAgentMaxNotes, noteCount)
	}
	if noteCount == 0 {
		t.Error("agent: ref should return at least 1 note")
	}
}

func TestResolveRefsArtifactExists(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("YAGENT_NOTES_ROOT", dir)

	pack := ResolveRefs("board", []string{"artifact:art_001"}, func(id string) bool {
		return id == "art_001"
	})
	if len(pack.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(pack.Entries))
	}
	if pack.Entries[0].Kind != EntryArtifact {
		t.Errorf("entry kind = %q, want %q", pack.Entries[0].Kind, EntryArtifact)
	}
}

func TestContextPackBudgetTruncation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("YAGENT_NOTES_ROOT", dir)

	largeBody := strings.Repeat("x", BudgetNoteInline+500)
	noteRef, _ := WriteNote(NoteInput{
		BoardID: "board", Agent: "repo", Task: "t", Summary: "large note",
		Body: largeBody, Status: "ok",
	})

	pack := ResolveRefs("board", []string{"note:" + noteRef}, nil)
	if len(pack.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(pack.Entries))
	}
	entry := pack.Entries[0]
	if !entry.Truncated {
		t.Error("large note should be truncated")
	}
	if entry.Body != "" {
		t.Error("truncated entry should have empty body")
	}
}

func TestContextPackTotalBudget(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("YAGENT_NOTES_ROOT", dir)

	body := strings.Repeat("y", BudgetNoteInline-10)
	var refs []string
	for i := 0; i < 10; i++ {
		ref, _ := WriteNote(NoteInput{
			BoardID: "board", Agent: "repo", Task: "t", Summary: "s",
			Body: body, Status: "ok",
		})
		refs = append(refs, "note:"+ref)
	}

	pack := ResolveRefs("board", refs, nil)
	truncatedCount := 0
	for _, e := range pack.Entries {
		if e.Truncated {
			truncatedCount++
		}
	}
	if truncatedCount == 0 {
		t.Error("some notes should be truncated when total budget is exceeded")
	}
}

func TestContextPackText(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("YAGENT_NOTES_ROOT", dir)

	noteRef, _ := WriteNote(NoteInput{
		BoardID: "board", Agent: "repo", Task: "t", Summary: "found 3 issues",
		Body: "detailed analysis here", Status: "ok",
	})

	pack := ResolveRefs("board", []string{"note:" + noteRef, "artifact:art_999"}, func(id string) bool { return true })
	text := pack.Text()

	if !strings.Contains(text, "## Referenced Context (from prior agent work)") {
		t.Error("text should contain header")
	}
	if !strings.Contains(text, "[note]") {
		t.Error("text should contain [note] entry")
	}
	if !strings.Contains(text, "[artifact]") {
		t.Error("text should contain [artifact] entry")
	}
	if !strings.Contains(text, "found 3 issues") {
		t.Error("text should contain note summary")
	}
}

func TestContextPackTextEmpty(t *testing.T) {
	pack := ContextPack{}
	if pack.Text() != "" {
		t.Errorf("empty pack text should be empty, got %q", pack.Text())
	}
}
