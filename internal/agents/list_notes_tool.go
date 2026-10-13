package agents

import (
	"context"
	"fmt"
	"strings"

	"yagent/internal/blackboard"
)

// list_notes 让子 Agent 自己枚举便签板上有什么。
//
// 此前它只有 read_note：引用得由 Director 传进来，Director 忘了传就等于没有。
// 发现权因此不是系统义务，而是 LLM 的一次自由裁量——本工具把它补上。
// 输出刻意只含 front-matter 一行的信息，绝不带正文：枚举必须便宜到每轮都能用。
const (
	listNotesDefaultLimit    = 20
	listNotesMaxLimit        = 50
	listNotesConclusionRunes = 80
)

func executeListNotes(ctx context.Context, params map[string]interface{}, getBoardID func() string) (interface{}, error) {
	boardID := blackboard.BoardIDFrom(ctx)
	if boardID == "" && getBoardID != nil {
		boardID = getBoardID()
	}
	if boardID == "" {
		return readNoteErrorText("no active board session"), nil
	}

	agent := strings.TrimSpace(stringParam(params, "agent"))
	limit := intParam(params, "limit", listNotesDefaultLimit)
	if limit < 1 {
		limit = listNotesDefaultLimit
	}
	if limit > listNotesMaxLimit {
		limit = listNotesMaxLimit
	}
	offset := intParam(params, "offset", 0)
	if offset < 0 {
		offset = 0
	}

	// 整块板的条数由 [blackboard] max_notes_per_board 兜住，所以这里全量取再分页。
	metas, err := blackboard.ListNotesByAgent(boardID, agent, 0)
	if err != nil {
		return readNoteErrorText(fmt.Sprintf("list_notes failed: %v", err)), nil
	}
	total := len(metas)

	scope := "board"
	if agent != "" {
		scope = "agent " + agent
	}
	if total == 0 {
		return fmt.Sprintf("no notes on %s (%s) — nothing has been delegated yet, or the board id is wrong", scope, boardID), nil
	}
	if offset >= total {
		return fmt.Sprintf("list_notes | %s | %d notes | offset=%d is past the end (max offset %d). Retry with a smaller offset.",
			scope, total, offset, total-1), nil
	}

	end := min(offset+limit, total)
	var b strings.Builder
	fmt.Fprintf(&b, "notes on %s (%s) | %d total | showing %d-%d\n",
		scope, boardID, total, offset+1, end)
	for _, m := range metas[offset:end] {
		conclusion := m.Conclusion
		if conclusion == "" {
			conclusion = m.Summary
		}
		line := fmt.Sprintf("%s | %s | %s | %s | %dc",
			m.NoteRef, m.Tool, m.CreatedAt, m.Status, m.BodySize)
		if c := strings.Join(strings.Fields(conclusion), " "); c != "" {
			line += " | " + truncateForDisplay(c, listNotesConclusionRunes)
		}
		if len(m.ArtifactIDs) > 0 {
			line += " | artifact:" + strings.Join(m.ArtifactIDs, ",")
		}
		if m.Degraded != "" {
			line += " | degraded:" + m.Degraded
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	if remaining := total - end; remaining > 0 {
		fmt.Fprintf(&b, "[more: %d notes left; continue with offset=%d]", remaining, end)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// truncateForDisplay 按 rune 裁短并标省略号（展示用，不落盘，所以不复用 blackboard 的内部裁剪）。
func truncateForDisplay(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 1 {
		return "…"
	}
	return string(r[:max-1]) + "…"
}
