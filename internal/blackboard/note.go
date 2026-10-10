package blackboard

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const noteSchemaVersion = 1

// Note represents a blackboard note: JSON front-matter + Markdown body.
type Note struct {
	Version     int      `json:"version"`
	NoteRef     string   `json:"note_ref"`
	BoardID     string   `json:"board_id"`
	Agent       string   `json:"agent"`
	Type        string   `json:"type"`
	CreatedAt   string   `json:"created_at"`
	Tool        string   `json:"tool"`
	Task        string   `json:"task"`
	Summary     string   `json:"summary"`
	ArtifactIDs []string `json:"artifact_ids"`
	BodySize    int      `json:"body_size"`
	Status      string   `json:"status"`

	Body string `json:"-"`
}

// NoteMeta is a lightweight view of a note (front-matter only, no body).
type NoteMeta struct {
	NoteRef     string   `json:"note_ref"`
	Agent       string   `json:"agent"`
	Type        string   `json:"type"`
	CreatedAt   string   `json:"created_at"`
	Tool        string   `json:"tool"`
	Task        string   `json:"task"`
	Summary     string   `json:"summary"`
	ArtifactIDs []string `json:"artifact_ids"`
	BodySize    int      `json:"body_size"`
	Status      string   `json:"status"`
}

// NoteInput carries the fields needed to write a note.
// Decoupled from agents.AgentResult to avoid circular imports.
type NoteInput struct {
	BoardID     string
	Agent       string
	Tool        string
	Task        string
	Summary     string
	Body        string
	ArtifactIDs []string
	Status      string // "ok" or "error"
}

const (
	maxTaskLen    = 300
	maxSummaryLen = 200
)

// WriteNote creates a note file on disk and returns its noteRef.
// noteRef format: "{agent}/{filename-stem}" (e.g. "repo_agent/20250601T090000Z_a1b2").
func WriteNote(input NoteInput) (string, error) {
	boardID := SanitizeBoardID(input.BoardID)
	agent := sanitizeAgentName(input.Agent)

	filename, err := GenerateNoteFilename()
	if err != nil {
		return "", err
	}
	stem := strings.TrimSuffix(filename, noteFileExt)
	noteRef := agent + "/" + stem

	body := input.Body
	if body == "" {
		body = input.Summary
	}

	note := Note{
		Version:     noteSchemaVersion,
		NoteRef:     noteRef,
		BoardID:     boardID,
		Agent:       agent,
		Type:        "deliverable",
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
		Tool:        input.Tool,
		Task:        truncateRunes(input.Task, maxTaskLen),
		Summary:     truncateRunes(input.Summary, maxSummaryLen),
		ArtifactIDs: input.ArtifactIDs,
		BodySize:    utf8.RuneCountInString(body),
		Status:      input.Status,
		Body:        body,
	}

	data, err := encodeNote(note)
	if err != nil {
		return "", err
	}

	dir := AgentDir(boardID, agent)
	path := filepath.Join(dir, filename)
	if err := AtomicWrite(path, data); err != nil {
		return "", err
	}
	return noteRef, nil
}

// ReadNote loads a note by boardID and noteRef ("{agent}/{stem}").
func ReadNote(boardID, noteRef string) (Note, error) {
	boardID = SanitizeBoardID(boardID)
	agent, stem, err := parseNoteRef(noteRef)
	if err != nil {
		return Note{}, err
	}
	path := filepath.Join(BoardDir(boardID), agent, stem+noteFileExt)
	data, err := os.ReadFile(path)
	if err != nil {
		return Note{}, fmt.Errorf("blackboard: read note %s: %w", noteRef, err)
	}
	note, err := decodeNote(data)
	if err != nil {
		return Note{}, fmt.Errorf("blackboard: decode note %s: %w", noteRef, err)
	}
	return note, nil
}

// ListNotes returns note metadata for a given agent within a board,
// sorted newest-first, limited to maxEntries. If agent is "", lists all agents.
func ListNotes(boardID, agent string, maxEntries int) ([]NoteMeta, error) {
	boardID = SanitizeBoardID(boardID)
	boardDir := BoardDir(boardID)

	var metas []NoteMeta

	if agent != "" {
		agentDir := filepath.Join(boardDir, sanitizeAgentName(agent))
		agentMetas, err := listNotesInDir(agentDir)
		if err != nil {
			return nil, err
		}
		metas = append(metas, agentMetas...)
	} else {
		entries, err := os.ReadDir(boardDir)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, nil
			}
			return nil, fmt.Errorf("blackboard: list board dir: %w", err)
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			agentMetas, err := listNotesInDir(filepath.Join(boardDir, e.Name()))
			if err != nil {
				continue
			}
			metas = append(metas, agentMetas...)
		}
	}

	sort.Slice(metas, func(i, j int) bool {
		return metas[i].CreatedAt > metas[j].CreatedAt
	})

	if maxEntries > 0 && len(metas) > maxEntries {
		metas = metas[:maxEntries]
	}
	return metas, nil
}

func listNotesInDir(dir string) ([]NoteMeta, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("blackboard: list agent dir: %w", err)
	}
	var metas []NoteMeta
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), noteFileExt) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		note, err := decodeNote(data)
		if err != nil {
			continue
		}
		metas = append(metas, note.toMeta())
	}
	return metas, nil
}

func (n Note) toMeta() NoteMeta {
	return NoteMeta{
		NoteRef:     n.NoteRef,
		Agent:       n.Agent,
		Type:        n.Type,
		CreatedAt:   n.CreatedAt,
		Tool:        n.Tool,
		Task:        n.Task,
		Summary:     n.Summary,
		ArtifactIDs: n.ArtifactIDs,
		BodySize:    n.BodySize,
		Status:      n.Status,
	}
}

// encodeNote serializes a Note to JSON front-matter + Markdown body.
func encodeNote(n Note) ([]byte, error) {
	fm, err := json.MarshalIndent(n, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("blackboard: marshal front-matter: %w", err)
	}
	var buf strings.Builder
	buf.WriteString(frontMatterDelim)
	buf.WriteByte('\n')
	buf.Write(fm)
	buf.WriteByte('\n')
	buf.WriteString(frontMatterDelim)
	buf.WriteByte('\n')
	if n.Body != "" {
		buf.WriteString(n.Body)
		if !strings.HasSuffix(n.Body, "\n") {
			buf.WriteByte('\n')
		}
	}
	return []byte(buf.String()), nil
}

// decodeNote parses JSON front-matter + Markdown body from raw bytes.
func decodeNote(data []byte) (Note, error) {
	content := string(data)
	if !strings.HasPrefix(content, frontMatterDelim) {
		return Note{}, fmt.Errorf("blackboard: missing front-matter delimiter")
	}
	rest := content[len(frontMatterDelim):]
	idx := strings.Index(rest, "\n"+frontMatterDelim)
	if idx < 0 {
		return Note{}, fmt.Errorf("blackboard: unterminated front-matter")
	}
	jsonPart := strings.TrimSpace(rest[:idx])
	bodyPart := rest[idx+len("\n"+frontMatterDelim):]
	bodyPart = strings.TrimPrefix(bodyPart, "\n")

	var note Note
	if err := json.Unmarshal([]byte(jsonPart), &note); err != nil {
		return Note{}, fmt.Errorf("blackboard: unmarshal front-matter: %w", err)
	}
	note.Body = strings.TrimRight(bodyPart, "\n")
	return note, nil
}

// parseNoteRef splits "agent/stem" into components.
func parseNoteRef(ref string) (agent, stem string, err error) {
	ref = strings.TrimSpace(ref)
	parts := strings.SplitN(ref, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("blackboard: invalid note ref %q (want agent/stem)", ref)
	}
	return sanitizeAgentName(parts[0]), parts[1], nil
}

func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max]) + "…"
}
