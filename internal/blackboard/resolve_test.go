package blackboard

import (
	"fmt"
	"strings"
	"testing"
)

// existsAll 是 ArtifactInfoFn 的测试替身：所有 id 都存在，体积 1234。
func existsAll(id string) (string, int, bool) {
	return "artifact summary for " + id, 1234, true
}

func missingAll(string) (string, int, bool) { return "", 0, false }

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
	pack := ResolveRefs("board", nil, nil, ResolveOptions{})
	if pack.Text() != "" {
		t.Errorf("empty refs should produce empty text, got %q", pack.Text())
	}
}

func TestResolveRefsDedup(t *testing.T) {
	t.Setenv("YAGENT_NOTES_ROOT", t.TempDir())

	noteRef, _ := WriteNote(NoteInput{
		BoardID: "board", Agent: "repo_agent", Task: "t", Summary: "s", Body: "b", Status: "ok",
	})

	pack := ResolveRefs("board", []string{"note:" + noteRef, "note:" + noteRef}, nil, ResolveOptions{})
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

// refs 必须真的被 MaxRefs 截断：20 个**互不相同**的 ref 只留前 BudgetMaxRefs 个。
// （以前用 20 个完全相同的 ref，去重后必然只剩 1 条，任何上限都能"通过"。）
func TestResolveRefsMaxLimit(t *testing.T) {
	refs := make([]string, BudgetMaxRefs*2)
	for i := range refs {
		refs[i] = fmt.Sprintf("artifact:art_%03d", i)
	}
	pack := ResolveRefs("board", refs, existsAll, ResolveOptions{})

	if len(pack.Unresolved) != 0 {
		t.Fatalf("unresolved = %v, want none (all ids report as existing)", pack.Unresolved)
	}
	if len(pack.Entries) != BudgetMaxRefs {
		t.Errorf("entries = %d, want exactly the cap %d", len(pack.Entries), BudgetMaxRefs)
	}
	// 被截掉的是尾部，不是头部——顺序即优先级。
	if got := pack.Entries[len(pack.Entries)-1].Ref; got != refs[BudgetMaxRefs-1][len("artifact:"):] {
		t.Errorf("last kept entry = %q, want the %d-th ref (%q)", got, BudgetMaxRefs, refs[BudgetMaxRefs-1])
	}
}

func TestResolveRefsUnresolved(t *testing.T) {
	t.Setenv("YAGENT_NOTES_ROOT", t.TempDir())

	pack := ResolveRefs("board", []string{
		"note:nonexistent/ref",
		"artifact:missing_art",
		"badformat",
	}, missingAll, ResolveOptions{})

	if len(pack.Unresolved) != 3 {
		t.Errorf("expected 3 unresolved, got %d: %v", len(pack.Unresolved), pack.Unresolved)
	}
}

func TestResolveRefsAgentRef(t *testing.T) {
	t.Setenv("YAGENT_NOTES_ROOT", t.TempDir())

	// fixture 必须用真实目录名 repo_agent：以前写 "repo" 恰好掩盖了生产侧的命名不对称。
	for i := 0; i < 5; i++ {
		WriteNote(NoteInput{
			BoardID: "board", Agent: "repo_agent", Task: "t", Summary: "s", Body: "body text", Status: "ok",
		})
	}

	pack := ResolveRefs("board", []string{"agent:repo_agent"}, nil, ResolveOptions{})
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
		t.Fatal("agent: ref should return at least 1 note")
	}
	if len(pack.Unresolved) != 0 {
		t.Errorf("unresolved = %v, want none", pack.Unresolved)
	}
}

// agent:repo 与 agent:repo_agent 必须命中同一批便签（别名归一）。
func TestResolveRefsAgentNameAlias(t *testing.T) {
	t.Setenv("YAGENT_NOTES_ROOT", t.TempDir())
	for i := 0; i < 3; i++ {
		WriteNote(NoteInput{
			BoardID: "board", Agent: "repo_agent", Task: "t", Summary: "s",
			Body: "alias body", Status: "ok",
		})
	}

	short := ResolveRefs("board", []string{"agent:repo"}, nil, ResolveOptions{})
	full := ResolveRefs("board", []string{"agent:repo_agent"}, nil, ResolveOptions{})

	if len(short.Entries) == 0 {
		t.Fatalf("agent:repo must resolve via alias, got %d entries (unresolved=%v)", len(short.Entries), short.Unresolved)
	}
	if len(short.Entries) != len(full.Entries) {
		t.Errorf("alias mismatch: %d vs %d entries", len(short.Entries), len(full.Entries))
	}
	for _, e := range short.Entries {
		if !strings.HasPrefix(e.Ref, "repo_agent/") {
			t.Errorf("entry ref = %q, want repo_agent/…", e.Ref)
		}
	}
}

// 名字彻底对不上时必须上报 unresolved —— 静默返回空 pack 正是本次要修的缺陷。
func TestResolveRefsAgentRefMissingReportsUnresolved(t *testing.T) {
	t.Setenv("YAGENT_NOTES_ROOT", t.TempDir())
	WriteNote(NoteInput{BoardID: "board", Agent: "repo_agent", Summary: "s", Body: "b", Status: "ok"})

	pack := ResolveRefs("board", []string{"agent:nonexistent"}, nil, ResolveOptions{})
	if len(pack.Entries) != 0 {
		t.Errorf("entries = %d, want 0", len(pack.Entries))
	}
	if len(pack.Unresolved) != 1 || pack.Unresolved[0] != "agent:nonexistent" {
		t.Errorf("unresolved = %v, want [agent:nonexistent]", pack.Unresolved)
	}
	if !strings.Contains(pack.Text(), "[unresolved] agent:nonexistent") {
		t.Errorf("Text must surface the failure:\n%s", pack.Text())
	}
}

func TestAgentNameCandidates(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"repo", "repo,repo_agent"},
		{"repo_agent", "repo_agent,repo"},
		{"  coding_agent", "coding_agent,coding"},
		{"_agent", "_agent"},
		{"", ""},
	}
	for _, c := range cases {
		if got := strings.Join(agentNameCandidates(c.in), ","); got != c.want {
			t.Errorf("agentNameCandidates(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// 截断分支必须给出节选正文（以前是"截断即只剩 200 字符摘要"，等于什么都没给）。
func TestContextPackBudgetTruncationKeepsExcerpt(t *testing.T) {
	t.Setenv("YAGENT_NOTES_ROOT", t.TempDir())

	largeBody := "开头是客套话。\n" + strings.Repeat("x", BudgetNoteInline+500) + "\n真正的结论在最后一行：改 persist.go"
	noteRef, _ := WriteNote(NoteInput{
		BoardID: "board", Agent: "repo_agent", Task: "t", Summary: "large note",
		Body: largeBody, Status: "ok",
	})

	pack := ResolveRefs("board", []string{"note:" + noteRef}, nil, ResolveOptions{})
	if len(pack.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(pack.Entries))
	}
	entry := pack.Entries[0]
	if !entry.Truncated {
		t.Fatal("large note should be truncated")
	}
	if entry.Body == "" {
		t.Fatal("truncated entry must still carry an excerpt")
	}
	if runeLen(entry.Body) > BudgetNoteInline {
		t.Errorf("excerpt over budget: %d runes", runeLen(entry.Body))
	}
	// 没有 handoff 的便签走头尾摘录，所以结尾必须可见。
	if !strings.Contains(entry.Body, "真正的结论在最后一行") {
		t.Errorf("tail lost from excerpt:\n%s", entry.Body)
	}
	if !strings.Contains(entry.Body, "中间省略") {
		t.Errorf("omission marker missing:\n%s", entry.Body)
	}
}

// 有合规 handoff 时，节选来自 handoff 而不是头尾摘录。
func TestContextPackUsesHandoffWhenAvailable(t *testing.T) {
	t.Setenv("YAGENT_NOTES_ROOT", t.TempDir())

	body := strings.Repeat("x", BudgetNoteInline+500)
	noteRef, _ := WriteNote(NoteInput{
		BoardID: "board", Agent: "repo_agent", Task: "t", Body: body, Status: "ok",
		Handoff: Handoff{Conclusion: "配置写回走 persist.go 的原子替换",
			Facts: []Fact{{Claim: "SaveConfig 是唯一入口", Where: "internal/config/persist.go:41"}}},
		ArtifactIDs: []string{"20261013-000000-aaaaaaaa"},
	})

	pack := ResolveRefs("board", []string{"note:" + noteRef}, nil, ResolveOptions{})
	entry := pack.Entries[0]
	if !entry.Truncated {
		t.Fatal("should be truncated")
	}
	if !strings.Contains(entry.Body, "结论: 配置写回走 persist.go 的原子替换") {
		t.Errorf("excerpt should come from handoff:\n%s", entry.Body)
	}
	if !strings.Contains(entry.Body, "@ internal/config/persist.go:41") {
		t.Errorf("facts missing from excerpt:\n%s", entry.Body)
	}
	if strings.Contains(entry.Body, "中间省略") {
		t.Errorf("handoff-backed excerpt should not use the head/tail marker:\n%s", entry.Body)
	}
	if len(entry.ArtifactIDs) != 1 || entry.ArtifactIDs[0] != "20261013-000000-aaaaaaaa" {
		t.Errorf("ArtifactIDs = %v, want the note's ids so the reader can hop to read_artifact", entry.ArtifactIDs)
	}
	text := pack.Text()
	if !strings.Contains(text, `read_note(ref="`+entry.Ref+`")`) {
		t.Errorf("pack text must tell the reader how to get the full body:\n%s", text)
	}
	if !strings.Contains(text, "artifacts: 20261013-000000-aaaaaaaa") {
		t.Errorf("pack text must surface artifact ids on truncated notes:\n%s", text)
	}
}

func TestContextPackTotalBudget(t *testing.T) {
	t.Setenv("YAGENT_NOTES_ROOT", t.TempDir())

	body := strings.Repeat("y", BudgetNoteInline-10)
	var refs []string
	for i := 0; i < 10; i++ {
		ref, _ := WriteNote(NoteInput{
			BoardID: "board", Agent: "repo_agent", Task: "t", Summary: "s",
			Body: body, Status: "ok",
		})
		refs = append(refs, "note:"+ref)
	}

	pack := ResolveRefs("board", refs, nil, ResolveOptions{})
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

func TestContextPackArtifactEntryCarriesInfo(t *testing.T) {
	t.Setenv("YAGENT_NOTES_ROOT", t.TempDir())

	pack := ResolveRefs("board", []string{"artifact:art_001"}, existsAll, ResolveOptions{})
	if len(pack.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(pack.Entries))
	}
	e := pack.Entries[0]
	if e.Kind != EntryArtifact {
		t.Errorf("entry kind = %q, want %q", e.Kind, EntryArtifact)
	}
	// 以前 Size 恒为 0，渲染出 "(0 chars)"，等于让子 Agent 盲调 read_artifact。
	if e.Size != 1234 {
		t.Errorf("Size = %d, want the artifact's real char count", e.Size)
	}
	if e.ArtifactSummary == "" {
		t.Error("ArtifactSummary should be loaded from the artifact")
	}
	text := pack.Text()
	if !strings.Contains(text, "(1234 chars)") {
		t.Errorf("pack text should show the real size:\n%s", text)
	}
	if !strings.Contains(text, "artifact summary for art_001") {
		t.Errorf("pack text should show the artifact summary:\n%s", text)
	}
}

func TestResolveArtifactRefWithoutProbe(t *testing.T) {
	pack := ResolveRefs("board", []string{"artifact:art_001"}, nil, ResolveOptions{})
	if len(pack.Entries) != 1 || pack.Entries[0].Kind != EntryArtifact {
		t.Fatalf("nil ArtifactInfoFn must keep the old pass-through behaviour, got %+v", pack.Entries)
	}
}

func TestContextPackText(t *testing.T) {
	t.Setenv("YAGENT_NOTES_ROOT", t.TempDir())

	noteRef, _ := WriteNote(NoteInput{
		BoardID: "board", Agent: "repo_agent", Task: "t", Summary: "found 3 issues",
		Body: "detailed analysis here", Status: "ok",
	})

	pack := ResolveRefs("board", []string{"note:" + noteRef, "artifact:art_999"}, existsAll, ResolveOptions{})
	text := pack.Text()

	// 表头只要求"第一行是一段标题"——具体措辞给人读，没有代码解析。
	firstLine := text
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		firstLine = text[:i]
	}
	if !strings.HasPrefix(firstLine, "## ") {
		t.Errorf("pack text should open with a header line, got %q", firstLine)
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
	if !strings.Contains(text, "detailed analysis here") {
		t.Error("text should inline a within-budget body verbatim")
	}
	if strings.Contains(text, "正文已节选") {
		t.Error("within-budget note must not advertise truncation")
	}
}

func TestContextPackTextEmpty(t *testing.T) {
	pack := ContextPack{}
	if pack.Text() != "" {
		t.Errorf("empty pack text should be empty, got %q", pack.Text())
	}
}

func TestHeadTailExcerpt(t *testing.T) {
	s := strings.Repeat("头", 100) + strings.Repeat("尾", 100)

	if got := headTailExcerpt(s, 1000); got != s {
		t.Error("under-budget input must pass through unchanged")
	}
	got := headTailExcerpt(s, 60)
	if n := runeLen(got); n > 60 {
		t.Errorf("excerpt length = %d, want <= 60", n)
	}
	if !strings.Contains(got, "中间省略") {
		t.Errorf("marker missing: %q", got)
	}
	if !strings.HasPrefix(got, "头") || !strings.HasSuffix(got, "尾") {
		t.Errorf("excerpt must keep both ends: %q", got)
	}
	if got := headTailExcerpt(s, 0); got != "" {
		t.Errorf("zero budget → %q, want empty", got)
	}
	// 极小预算不能 panic，也不能吐出超长内容。
	if got := headTailExcerpt(s, 3); runeLen(got) > 8 {
		t.Errorf("tiny budget produced %d runes: %q", runeLen(got), got)
	}
}

func TestResolveOptionsDefaults(t *testing.T) {
	o := ResolveOptions{}.withDefaults()
	if o.TotalChars != BudgetTotalChars || o.NoteInline != BudgetNoteInline ||
		o.AgentMaxNotes != BudgetAgentMaxNotes || o.MaxRefs != BudgetMaxRefs ||
		o.SummaryRunes != DefaultRenderRunes {
		t.Errorf("withDefaults() = %+v, want the documented budget constants", o)
	}

	custom := ResolveOptions{TotalChars: 100, SummaryRunes: 50}.withDefaults()
	if custom.TotalChars != 100 || custom.SummaryRunes != 50 {
		t.Errorf("explicit values must survive: %+v", custom)
	}
}
