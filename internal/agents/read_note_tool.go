package agents

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"yagent/internal/blackboard"
	"yagent/internal/tools"
)

const (
	readNoteHardMaxLines   = 2000
	readNoteMaxReturnRunes = 24000
	readNoteMaxLineRunes   = 4000
)

// newReadNoteAdapter constructs the read_note tool adapter (Director-only, mirrors read_artifact).
func newReadNoteAdapter(getBoardID func() string) *tools.Adapter {
	return tools.NewAdapter("read_note",
		"Read the full body of a blackboard note by its reference. "+
			"Use when a [Sub-Agent Result] header shows a 'note: <ref>' field and you need details "+
			"beyond the inline summary, or when a Context Pack entry was truncated. "+
			"Params: ref (required) — the note reference (format: agent/stem); "+
			"start_line (optional, 1-based, default 1); max_lines (optional, hard cap 2000).",
		func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
			return executeReadNote(ctx, params, getBoardID)
		}).WithSchema(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"ref": map[string]interface{}{
				"type":        "string",
				"description": "Note reference from the [Sub-Agent Result] header (note: field), format: agent/stem.",
			},
			"start_line": map[string]interface{}{
				"type":        "integer",
				"description": "1-based start line (default 1).",
			},
			"max_lines": map[string]interface{}{
				"type":        "integer",
				"description": "Max lines to return (hard cap 2000).",
			},
		},
		"required": []string{"ref"},
	})
}

func executeReadNote(ctx context.Context, params map[string]interface{}, getBoardID func() string) (interface{}, error) {
	ref, _ := params["ref"].(string)
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return readNoteErrorText("invalid note ref: (empty)"), nil
	}

	boardID := blackboard.BoardIDFrom(ctx)
	if boardID == "" && getBoardID != nil {
		boardID = getBoardID()
	}
	if boardID == "" {
		return readNoteErrorText("no active board session"), nil
	}

	note, err := blackboard.ReadNote(boardID, ref)
	if err != nil {
		return readNoteErrorText(fmt.Sprintf("read_note failed: %v", err)), nil
	}

	startLine := intParam(params, "start_line", 1)
	if startLine < 1 {
		startLine = 1
	}
	maxLines := intParam(params, "max_lines", 0)
	if maxLines < 1 {
		maxLines = readNoteHardMaxLines
	}
	if maxLines > readNoteHardMaxLines {
		maxLines = readNoteHardMaxLines
	}

	lines := strings.Split(note.Body, "\n")
	total := len(lines)

	if startLine > total {
		var b strings.Builder
		fmt.Fprintf(&b, "note %s | agent: %s | %d lines | showing nothing\n", note.NoteRef, note.Agent, total)
		fmt.Fprintf(&b, "start_line=%d exceeds total lines (%d). Retry with a start_line between 1 and %d.",
			startLine, total, total)
		return b.String(), nil
	}

	end := startLine - 1 + maxLines
	if end > total {
		end = total
	}

	width := len(strconv.Itoa(total))
	var lineOutputs []string
	runesConsumed := 0
	actualEnd := startLine - 1

	for i, line := range lines[startLine-1 : end] {
		n := startLine + i
		lineRunes := []rune(line)
		var lineOut string
		if len(lineRunes) > readNoteMaxLineRunes {
			lineOut = fmt.Sprintf("%*d: %s ... [line %d truncated, %d runes total]\n",
				width, n, string(lineRunes[:readNoteMaxLineRunes]), n, len(lineRunes))
		} else {
			lineOut = fmt.Sprintf("%*d: %s\n", width, n, line)
		}
		lineOutRunes := len([]rune(lineOut))
		if runesConsumed > 0 && runesConsumed+lineOutRunes > readNoteMaxReturnRunes {
			break
		}
		lineOutputs = append(lineOutputs, lineOut)
		runesConsumed += lineOutRunes
		actualEnd = n
	}

	var b strings.Builder
	fmt.Fprintf(&b, "note %s | agent: %s | status: %s | %d lines | showing %d-%d\n",
		note.NoteRef, note.Agent, note.Status, total, startLine, actualEnd)
	if len(note.ArtifactIDs) > 0 {
		fmt.Fprintf(&b, "artifacts: %s\n", strings.Join(note.ArtifactIDs, ", "))
	}
	for _, lineOut := range lineOutputs {
		fmt.Fprint(&b, lineOut)
	}
	if remaining := total - actualEnd; remaining > 0 {
		fmt.Fprintf(&b, "[more: %d lines left; continue with start_line=%d]", remaining, actualEnd+1)
	}
	return b.String(), nil
}

func readNoteErrorText(msg string) string {
	return msg + "\nPlease check the note ref from the [Sub-Agent Result] header (note: field, format: agent/stem)."
}
