package blackboard

import (
	"fmt"
	"strings"
)

// Budget constants for Context Pack assembly (Phase 1: hardcoded; Phase 2: configurable).
const (
	BudgetTotalChars    = 6000
	BudgetNoteInline    = 2000
	BudgetAgentMaxNotes = 3
	BudgetMaxRefs       = 10
)

// PackEntryKind distinguishes entry types in a ContextPack.
type PackEntryKind string

const (
	EntryNote       PackEntryKind = "note"
	EntryArtifact   PackEntryKind = "artifact"
	EntryUnresolved PackEntryKind = "unresolved"
)

// PackEntry is one resolved reference in a ContextPack.
type PackEntry struct {
	Kind    PackEntryKind
	Ref     string
	Summary string
	Body    string // inlined content (empty if over budget)
	Size    int    // total body size in chars
	Truncated bool // true if body was omitted due to budget
}

// ContextPack holds resolved references ready for injection into a delegate task.
type ContextPack struct {
	Entries    []PackEntry
	Unresolved []string
}

// Text renders the ContextPack as a text block for prepending to a task string.
// Returns "" if the pack is empty.
// Sub-agents have read_note and read_artifact tools auto-injected by the executor,
// so the text can reference them directly.
func (p ContextPack) Text() string {
	if len(p.Entries) == 0 && len(p.Unresolved) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Referenced Context (from prior agent work)\n")
	for _, e := range p.Entries {
		switch e.Kind {
		case EntryNote:
			if e.Truncated {
				fmt.Fprintf(&b, "[note] %s — %s — (content truncated, call read_note(ref=\"%s\") for full text)\n", e.Ref, e.Summary, e.Ref)
			} else {
				fmt.Fprintf(&b, "[note] %s — %s\n%s\n", e.Ref, e.Summary, e.Body)
			}
		case EntryArtifact:
			fmt.Fprintf(&b, "[artifact] %s (%d chars) — call read_artifact(id=\"%s\") for full text\n", e.Ref, e.Size, e.Ref)
		}
	}
	for _, u := range p.Unresolved {
		fmt.Fprintf(&b, "[unresolved] %s — ignored\n", u)
	}
	return b.String()
}

// ArtifactExistsFn is a callback to check artifact existence without importing the artifact package.
type ArtifactExistsFn func(id string) bool

// ResolveRefs parses context_refs, loads notes/artifacts, and assembles a ContextPack
// within budget constraints.
func ResolveRefs(boardID string, refs []string, artifactExists ArtifactExistsFn) ContextPack {
	if len(refs) == 0 {
		return ContextPack{}
	}
	if len(refs) > BudgetMaxRefs {
		refs = refs[:BudgetMaxRefs]
	}

	seen := make(map[string]bool)
	var pack ContextPack
	totalChars := 0

	for _, raw := range refs {
		ref := strings.TrimSpace(raw)
		if ref == "" || seen[ref] {
			continue
		}
		seen[ref] = true

		kind, value := parseRef(ref)
		switch kind {
		case "agent":
			entries := resolveAgentRef(boardID, value, &totalChars)
			pack.Entries = append(pack.Entries, entries...)
		case "note":
			entry, ok := resolveNoteRef(boardID, value, &totalChars)
			if !ok {
				pack.Unresolved = append(pack.Unresolved, ref)
				continue
			}
			pack.Entries = append(pack.Entries, entry)
		case "artifact":
			entry, ok := resolveArtifactRef(value, artifactExists)
			if !ok {
				pack.Unresolved = append(pack.Unresolved, ref)
				continue
			}
			pack.Entries = append(pack.Entries, entry)
		default:
			pack.Unresolved = append(pack.Unresolved, ref)
		}
	}
	return pack
}

// parseRef splits "kind:value" into components.
func parseRef(ref string) (kind, value string) {
	idx := strings.Index(ref, ":")
	if idx < 0 {
		return "", ref
	}
	return ref[:idx], ref[idx+1:]
}

func resolveAgentRef(boardID, agent string, totalChars *int) []PackEntry {
	metas, err := ListNotes(boardID, agent, BudgetAgentMaxNotes)
	if err != nil || len(metas) == 0 {
		return nil
	}
	var entries []PackEntry
	for _, m := range metas {
		entry, ok := loadNoteEntry(boardID, m, totalChars)
		if !ok {
			continue
		}
		entries = append(entries, entry)
	}
	return entries
}

func resolveNoteRef(boardID, noteRef string, totalChars *int) (PackEntry, bool) {
	note, err := ReadNote(boardID, noteRef)
	if err != nil {
		return PackEntry{}, false
	}
	meta := note.toMeta()
	return loadNoteEntry(boardID, meta, totalChars)
}

func loadNoteEntry(boardID string, meta NoteMeta, totalChars *int) (PackEntry, bool) {
	entry := PackEntry{
		Kind:    EntryNote,
		Ref:     meta.NoteRef,
		Summary: meta.Summary,
		Size:    meta.BodySize,
	}

	remaining := BudgetTotalChars - *totalChars
	if remaining <= 0 {
		entry.Truncated = true
		return entry, true
	}

	note, err := ReadNote(boardID, meta.NoteRef)
	if err != nil {
		entry.Truncated = true
		return entry, true
	}

	body := note.Body
	bodyLen := len([]rune(body))
	if bodyLen > BudgetNoteInline || bodyLen > remaining {
		entry.Truncated = true
		*totalChars += len([]rune(entry.Summary)) + len([]rune(entry.Ref)) + 20
		return entry, true
	}

	entry.Body = body
	*totalChars += bodyLen + len([]rune(entry.Summary)) + len([]rune(entry.Ref)) + 20
	return entry, true
}

func resolveArtifactRef(id string, exists ArtifactExistsFn) (PackEntry, bool) {
	if exists != nil && !exists(id) {
		return PackEntry{}, false
	}
	return PackEntry{
		Kind: EntryArtifact,
		Ref:  id,
		Size: 0,
	}, true
}
