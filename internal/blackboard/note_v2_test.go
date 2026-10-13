package blackboard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteNoteCarriesHandoff(t *testing.T) {
	root := t.TempDir()
	t.Setenv("YAGENT_NOTES_ROOT", root)

	ref, err := WriteNote(NoteInput{
		BoardID:     "b1",
		Agent:       "repo_agent",
		Tool:        "delegate_repo",
		Task:        "查 TOML 库",
		Body:        "全文交付物\n第二行",
		ArtifactIDs: []string{"20261013-000000-aaaaaaaa"},
		Status:      StatusOK,
		Handoff: Handoff{
			Conclusion: "BurntSushi/toml v1.5.0",
			Facts:      []Fact{{Claim: "解码走 LoadFile", Where: "internal/config/config.go:120"}},
		},
	})
	if err != nil {
		t.Fatalf("WriteNote: %v", err)
	}

	note, err := ReadNote("b1", ref)
	if err != nil {
		t.Fatalf("ReadNote: %v", err)
	}
	if note.Version != noteSchemaVersion {
		t.Errorf("Version = %d, want %d", note.Version, noteSchemaVersion)
	}
	if note.Handoff.Conclusion != "BurntSushi/toml v1.5.0" {
		t.Errorf("Handoff.Conclusion = %q", note.Handoff.Conclusion)
	}
	if len(note.Handoff.Facts) != 1 || note.Handoff.Facts[0].Where == "" {
		t.Errorf("Handoff.Facts = %+v", note.Handoff.Facts)
	}
	if note.Handoff.Degraded != "" {
		t.Errorf("Degraded = %q, want empty", note.Handoff.Degraded)
	}
	// Summary 是 v1 读者的兼容位：值等于结论，不再是另一份摘要。
	if note.Summary != note.Handoff.Conclusion {
		t.Errorf("Summary = %q, want Conclusion %q", note.Summary, note.Handoff.Conclusion)
	}
	if note.Body != "全文交付物\n第二行" {
		t.Errorf("Body = %q, want the verbatim deliverable", note.Body)
	}
	if note.BodySize != runeLen("全文交付物\n第二行") {
		t.Errorf("BodySize = %d, want %d", note.BodySize, runeLen("全文交付物\n第二行"))
	}
}

// 系统侧的 ArtifactIDs 覆盖 LLM 自报的 id——后者从未被验证存在过。
func TestWriteNoteArtifactIDsWinOverHandoffArtifacts(t *testing.T) {
	root := t.TempDir()
	t.Setenv("YAGENT_NOTES_ROOT", root)

	ref, err := WriteNote(NoteInput{
		BoardID:     "b1",
		Agent:       "coding_agent",
		ArtifactIDs: []string{"20261013-010101-bbbbbbbb"},
		Status:      StatusOK,
		Handoff: Handoff{
			Conclusion: "c",
			Artifacts:  []string{"fabricated-id", "20261013-010101-bbbbbbbb"},
		},
	})
	if err != nil {
		t.Fatalf("WriteNote: %v", err)
	}
	note, err := ReadNote("b1", ref)
	if err != nil {
		t.Fatalf("ReadNote: %v", err)
	}
	if len(note.Handoff.Artifacts) != 1 || note.Handoff.Artifacts[0] != "20261013-010101-bbbbbbbb" {
		t.Errorf("Handoff.Artifacts = %v, want only the system-provided id", note.Handoff.Artifacts)
	}
}

func TestWriteNoteWithoutHandoffFallsBackToSummary(t *testing.T) {
	root := t.TempDir()
	t.Setenv("YAGENT_NOTES_ROOT", root)

	ref, err := WriteNote(NoteInput{
		BoardID: "b1", Agent: "chat_agent", Summary: "只有摘要的老路径", Status: StatusOK,
	})
	if err != nil {
		t.Fatalf("WriteNote: %v", err)
	}
	note, err := ReadNote("b1", ref)
	if err != nil {
		t.Fatalf("ReadNote: %v", err)
	}
	if note.Handoff.Conclusion != "只有摘要的老路径" {
		t.Errorf("Conclusion = %q, want it filled from Summary", note.Handoff.Conclusion)
	}
	if note.Handoff.Degraded != DegradedAbsent {
		t.Errorf("Degraded = %q, want %q", note.Handoff.Degraded, DegradedAbsent)
	}
	// 正文为空时用 handoff 渲染兜底，绝不留空白便签。
	if !strings.Contains(note.Body, "只有摘要的老路径") {
		t.Errorf("Body = %q, want the rendered handoff", note.Body)
	}
}

// v1 存量便签（front-matter 里没有 handoff 键）必须还能读，并合成出降级 Handoff。
func TestReadV1NoteSynthesizesHandoff(t *testing.T) {
	root := t.TempDir()
	t.Setenv("YAGENT_NOTES_ROOT", root)

	v1 := `---
{
  "version": 1,
  "note_ref": "repo_agent/20250601T090000Z_a1b2",
  "board_id": "b1",
  "agent": "repo_agent",
  "type": "deliverable",
  "created_at": "2025-06-01T09:00:00Z",
  "tool": "delegate_repo",
  "task": "老任务",
  "summary": "基于我的分析，我现在可以回答您的问题了：",
  "artifact_ids": ["20250601-090000-cccccccc"],
  "body_size": 24,
  "status": "ok"
}
---
v1 便签的正文
`
	dir := filepath.Join(root, "b1", "repo_agent")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "20250601T090000Z_a1b2.md"), []byte(v1), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	note, err := ReadNote("b1", "repo_agent/20250601T090000Z_a1b2")
	if err != nil {
		t.Fatalf("ReadNote v1: %v", err)
	}
	if note.Version != 1 {
		t.Errorf("Version = %d, want the stored 1 (must not be rewritten)", note.Version)
	}
	if note.Handoff.Conclusion != "基于我的分析，我现在可以回答您的问题了：" {
		t.Errorf("Conclusion = %q, want the v1 summary", note.Handoff.Conclusion)
	}
	if note.Handoff.Degraded != DegradedV1 {
		t.Errorf("Degraded = %q, want %q", note.Handoff.Degraded, DegradedV1)
	}
	if len(note.Handoff.Artifacts) != 1 {
		t.Errorf("Artifacts = %v, want inherited from artifact_ids", note.Handoff.Artifacts)
	}
	if note.Body != "v1 便签的正文" {
		t.Errorf("Body = %q", note.Body)
	}
}

func TestListNotesMetaCarriesConclusion(t *testing.T) {
	root := t.TempDir()
	t.Setenv("YAGENT_NOTES_ROOT", root)

	if _, err := WriteNote(NoteInput{
		BoardID: "b1", Agent: "repo_agent", Status: StatusOK,
		Handoff: Handoff{Conclusion: "索引要用的结论"},
	}); err != nil {
		t.Fatalf("WriteNote: %v", err)
	}
	if _, err := WriteNote(NoteInput{
		BoardID: "b1", Agent: "devops_agent", Summary: "v1 风格摘要", Status: StatusOK,
	}); err != nil {
		t.Fatalf("WriteNote: %v", err)
	}

	metas, err := ListNotes("b1", "", 0)
	if err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	if len(metas) != 2 {
		t.Fatalf("metas = %d, want 2", len(metas))
	}
	byAgent := map[string]NoteMeta{}
	for _, m := range metas {
		byAgent[m.Agent] = m
	}
	if got := byAgent["repo_agent"].Conclusion; got != "索引要用的结论" {
		t.Errorf("repo_agent Conclusion = %q", got)
	}
	if byAgent["repo_agent"].Degraded != "" {
		t.Errorf("repo_agent Degraded = %q, want empty", byAgent["repo_agent"].Degraded)
	}
	// 没给 handoff 的那条：Conclusion 来自 Summary，且标降级。
	devops := byAgent["devops_agent"]
	if devops.Conclusion != "v1 风格摘要" || devops.Degraded != DegradedAbsent {
		t.Errorf("devops_agent meta = %+v, want Summary-derived conclusion + absent", devops)
	}
}
