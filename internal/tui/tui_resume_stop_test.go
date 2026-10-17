package tui

// tui_resume_stop_test.go — 清单侧的续跑判据（R2）。
// 钉三件事：读得到、排得对、"未知"与"不能续"画得不一样。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yagent/internal/memory"

	"charm.land/lipgloss/v2"
)

func stopFixtureFile(t *testing.T, name, terminus string) string {
	t.Helper()
	lines := []string{
		`{"timestamp":"2025-06-01T10:00:00.000Z","type":"session_meta","payload":{"session_id":"s","cwd":"/tmp"}}`,
		`{"timestamp":"2025-06-01T10:00:01.000Z","type":"response_item","payload":{"type":"message","role":"user","id":"u1","content":[{"type":"input_text","text":"任务"}]}}`,
	}
	if terminus != "" {
		lines = append(lines, terminus)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func abortedLine(kind string, step, max int) string {
	return fmt.Sprintf(`{"timestamp":"2025-06-01T10:00:02.000Z","type":"event_msg","payload":`+
		`{"type":"turn_aborted","reason":"stopped","stop_reason":%q,"step_number":%d,"max_steps":%d}}`, kind, step, max)
}

func completeLine() string {
	return `{"timestamp":"2025-06-01T10:00:02.000Z","type":"event_msg","payload":{"type":"task_complete"}}`
}

func TestReadResumeStopFactsIndexesEveryFile(t *testing.T) {
	replayable := stopFixtureFile(t, "a.jsonl", abortedLine(memory.StopKindPaused, 12, 100))
	finished := stopFixtureFile(t, "b.jsonl", completeLine())
	unknown := stopFixtureFile(t, "c.jsonl", "")
	items := []memory.RolloutSessionInfo{{Path: replayable}, {Path: finished}, {Path: unknown}}

	index := readResumeStopFacts(items)
	if len(index) != 3 {
		t.Fatalf("index size = %d, want 3（每个文件都要有条目，读不到也要有）", len(index))
	}
	for path, want := range map[string]memory.ResumeState{
		replayable: memory.ResumeReplayable,
		finished:   memory.ResumeFinished,
		unknown:    memory.ResumeUnknown,
	} {
		f, ok := index[path]
		if !ok {
			t.Fatalf("%s 没进索引", path)
		}
		if got, _ := f.State(); got != want {
			t.Errorf("%s: state = %v, want %v", filepath.Base(path), got, want)
		}
	}
	if n := resumableGroups(index, buildResumeGroups(items)); n != 1 {
		t.Errorf("resumableGroups = %d, want 1", n)
	}
}

func TestReadResumeStopFactsUnreadableFileIsAbsentNotFatal(t *testing.T) {
	good := stopFixtureFile(t, "ok.jsonl", abortedLine(memory.StopKindCancelled, 3, 50))
	items := []memory.RolloutSessionInfo{
		{Path: good},
		{Path: filepath.Join(t.TempDir(), "missing.jsonl")},
	}
	index := readResumeStopFacts(items)
	if _, ok := index[good]; !ok {
		t.Error("the readable file must still be indexed")
	}
	if len(index) != 1 {
		t.Errorf("index size = %d, want 1（读不了的条目由渲染侧画成未知）", len(index))
	}
}

func TestSortResumeGroupsByStatePutsReplayableFirstAndIsStable(t *testing.T) {
	base := time.Now()
	newItem := func(path string, off time.Duration) memory.RolloutSessionInfo {
		return memory.RolloutSessionInfo{Path: path, TaskID: path, StartedAt: base.Add(off), ModTime: base.Add(off)}
	}
	// newest 是 finished，oldest 是 replayable：不排序的话 finished 在前。
	items := []memory.RolloutSessionInfo{
		newItem("/finished.jsonl", 2*time.Hour),
		newItem("/plain.jsonl", 1*time.Hour),
		newItem("/replay.jsonl", 0),
	}
	index := resumeStopIndex{
		"/finished.jsonl": {Found: true, Finished: true},
		"/plain.jsonl":    {},
		"/replay.jsonl":   {Found: true, Aborted: true, StopReason: memory.StopKindPaused},
	}
	groups := sortResumeGroupsByState(buildResumeGroups(items), index)

	got := make([]string, 0, len(groups))
	for _, g := range groups {
		got = append(got, filepath.Base(g.head.Path))
	}
	want := []string{"replay.jsonl", "plain.jsonl", "finished.jsonl"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v（可续最前、未知居中、跑完的沉底）", got, want)
	}

	// 同档内必须保持 mtime 新→旧，不许二次洗牌
	same := []memory.RolloutSessionInfo{newItem("/n.jsonl", time.Minute), newItem("/o.jsonl", 0)}
	sorted := sortResumeGroupsByState(buildResumeGroups(same), resumeStopIndex{
		"/n.jsonl": {Found: true, Aborted: true, StopReason: memory.StopKindCancelled},
		"/o.jsonl": {Found: true, Aborted: true, StopReason: memory.StopKindCancelled},
	})
	if sorted[0].head.Path != "/n.jsonl" {
		t.Errorf("同档内顺序被洗掉了：first = %s, want /n.jsonl（新的在前）", sorted[0].head.Path)
	}
}

func TestGroupRankUsesTheBestMember(t *testing.T) {
	head := memory.RolloutSessionInfo{Path: "/main.jsonl"}
	member := memory.RolloutSessionInfo{Path: "/main_coding.jsonl", AgentName: "coding"}
	index := resumeStopIndex{
		"/main.jsonl":        {Found: true, Finished: true}, // 主会话判成跑完了
		"/main_coding.jsonl": {Found: true, Aborted: true, StopReason: memory.StopKindPaused},
	}
	if r := groupRank(index, head, []memory.RolloutSessionInfo{member}); r != 0 {
		t.Errorf("groupRank = %d, want 0（子文件还带着可重放的停点，这组就值得先看到）", r)
	}
	if r := groupRank(resumeStopIndex{}, head, nil); r != 2 {
		t.Errorf("一条判据都没有时 groupRank = %d, want 2（未知）", r)
	}
}

// 状态符必须让"不知道"（?）与"确实不能原样重放"（○ 交接 / ! 失败）区分开。
// 同形的表现：用户以为存量会话都被判了死刑，于是不再试。
func TestResumeStateGlyphsAreDistinctPerState(t *testing.T) {
	cases := []struct {
		name      string
		f         memory.StopFacts
		wantGlyph string
	}{
		{"replayable", memory.StopFacts{Found: true, Aborted: true, StopReason: memory.StopKindPaused}, "●"},
		{"handoff", memory.StopFacts{Found: true, Aborted: true, StopReason: memory.StopKindMaxSteps}, "○"},
		{"failed", memory.StopFacts{Found: true, Aborted: true, StopReason: memory.StopKindLLMError}, "!"},
		{"finished", memory.StopFacts{Found: true, Finished: true}, "·"},
		{"unknown", memory.StopFacts{}, "?"},
	}
	seen := map[string]string{}
	for _, c := range cases {
		st, _ := c.f.State()
		g := resumeStateGlyph(st)
		if g != c.wantGlyph {
			t.Errorf("%s: glyph = %q, want %q", c.name, g, c.wantGlyph)
		}
		if prev, dup := seen[g]; dup {
			t.Errorf("%s 与 %s 共用字形 %q —— 判据不同的两档不能同形", c.name, prev, g)
		}
		seen[g] = c.name
		// 行首是固定列：字形占几格直接决定整行列会不会平移。
		// 这条断言是"禁 Emoji_Presentation / EA=W 字符"那纸规格的机械版本。
		if w := lipgloss.Width(g); w != 1 {
			t.Errorf("%s 的字形 %q 占 %d 格, want 1", c.name, g, w)
		}
	}
}

// describeStop 是详情抽屉第二行的人话。可原样重放时不许编造原因；
// 不可重放时必须把原因带出来（否则用户只看到一个没有解释的拒绝）。
func TestDescribeStopCarriesTheReasonOnlyWhenThereIsOne(t *testing.T) {
	ok := describeStop(memory.ResumeReplayable, "", memory.StopFacts{
		Found: true, Aborted: true, StopReason: memory.StopKindPaused, Step: 12, MaxSteps: 100})
	if strings.Contains(ok, "unknown") || strings.Contains(ok, "cannot") {
		t.Errorf("可原样重放却写出了问题: %q", ok)
	}
	if !strings.Contains(ok, "replayable") || !strings.Contains(ok, "step 12/100") {
		t.Errorf("describeStop 丢了状态或步数: %q", ok)
	}

	no := describeStop(memory.ResumeHandoff, "timed out: the interrupted step may already be recorded", memory.StopFacts{
		Found: true, Aborted: true, StopReason: memory.StopKindIdleTimeout})
	if !strings.Contains(no, "handoff") || !strings.Contains(no, "timed out") {
		t.Errorf("交接档必须带上原因: %q", no)
	}
}

// 状态符必须让"不知道"（?）与"确实不能原样重放"（○ 交接 / ! 失败）区分开。
// 同形的表现：用户以为存量会话都被判了死刑，于是不再试。
// describeStop 是详情抽屉里的人话行。可原样重放时不许编造问题；
// 不可重放时必须把原因带出来（否则用户只看到一个没有解释的拒绝）。
