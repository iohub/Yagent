package memory

// rollout_sessions_test.go — ListRolloutSessions / parseRolloutFilename 单元测试。
//
// fixture 写入 t.TempDir() 构造多 projectID 目录树；用 os.Chtimes 控制 mtime 验证
// 排序与 limit；坏文件/不可读路径验证容错。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeSessionFixture 在指定路径写入 rollout fixture（自动建父目录）。
func writeSessionFixture(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// sessionFileLines 生成一套完整会话 fixture 行。
func sessionFileLines(sessionID, cwd, model, userText string) []string {
	return []string{
		rrLine(rrTs1, "session_meta", rrMeta(sessionID, cwd, "main")),
		rrLine(rrTs2, "turn_context", rrTurnCtx(model, cwd)),
		rrLine(rrTs3, "response_item", rrUserItem("u1", userText)),
		rrLine(rrTs4, "response_item", rrAssistantItem("a1", "answer")),
	}
}

// ─── 主测试：目录树 / mtime 排序 / limit / 项目归属 ───

func TestListRolloutSessions(t *testing.T) {
	root := t.TempDir()

	// projA：三个会话（mtime 递增），一个非 jsonl，一个超深目录文件
	writeSessionFixture(t, filepath.Join(root, "projA", "20250601_100000_sid_oldest.jsonl"),
		sessionFileLines("sess-a-old", "/repo/old", "model-old", "oldest question"))
	writeSessionFixture(t, filepath.Join(root, "projA", "20250601_110000_sid_mid.jsonl"),
		sessionFileLines("sess-a-mid", "/repo/mid", "model-mid", "mid question"))
	writeSessionFixture(t, filepath.Join(root, "projA", "20250601_130000_sid_newest.jsonl"),
		sessionFileLines("sess-a-new", "/repo/new", "model-new", "newest question"))
	writeSessionFixture(t, filepath.Join(root, "projA", "notes.txt"),
		[]string{rrLine(rrTs1, "session_meta", rrMeta("not-jsonl", "/x", ""))}) // 非 jsonl：忽略
	writeSessionFixture(t, filepath.Join(root, "projA", "deep", "20250601_140000_sid_toolong.jsonl"),
		sessionFileLines("sess-deep", "/repo/deep", "m", "too deep")) // 深度 3：忽略

	// projB：一个会话
	writeSessionFixture(t, filepath.Join(root, "projB", "20250601_120000_sid_bproj.jsonl"),
		sessionFileLines("sess-b", "/repo/b", "model-b", "b question"))

	// mtime 设定：oldest=1000, mid=1010, newest=1020, b=1015（无 jsonl 的 notes.txt 也设 mtime）
	base := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	chtimes := []struct {
		path string
		sec  int64
	}{
		{filepath.Join(root, "projA", "20250601_100000_sid_oldest.jsonl"), 1000},
		{filepath.Join(root, "projA", "20250601_110000_sid_mid.jsonl"), 1010},
		{filepath.Join(root, "projA", "20250601_130000_sid_newest.jsonl"), 1020},
		{filepath.Join(root, "projB", "20250601_120000_sid_bproj.jsonl"), 1015},
		{filepath.Join(root, "projA", "notes.txt"), 1025},
	}
	for _, ct := range chtimes {
		if err := os.Chtimes(ct.path, base.Add(time.Duration(ct.sec)*time.Second), base.Add(time.Duration(ct.sec)*time.Second)); err != nil {
			t.Fatalf("chtimes %s: %v", ct.path, err)
		}
	}

	// ── 全量（默认 limit）：4 个 jsonl，mtime 倒序 ──
	sessions, err := ListRolloutSessions(root, 0)
	if err != nil {
		t.Fatalf("ListRolloutSessions: %v", err)
	}
	if len(sessions) != 4 {
		t.Fatalf("sessions = %d, want 4", len(sessions))
	}
	wantOrder := []string{
		"sess-a-new",  // 1020
		"sess-b",      // 1015
		"sess-a-mid",  // 1010
		"sess-a-old",  // 1000
	}
	for i, s := range sessions {
		if s.SessionID != wantOrder[i] {
			t.Fatalf("sessions[%d].SessionID = %q, want %q", i, s.SessionID, wantOrder[i])
		}
	}
	// newest 会话字段完整性
	newest := sessions[0]
	if newest.ProjectID != "projA" || newest.Model != "model-new" || newest.CWD != "/repo/new" ||
		newest.GitBranch != "main" || newest.Preview != "newest question" {
		t.Fatalf("newest = %+v", newest)
	}
	if newest.StartedAt.IsZero() || !newest.StartedAt.Equal(time.Date(2025, 6, 1, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("newest.StartedAt = %v, want rollout meta ts", newest.StartedAt)
	}
	if newest.SizeBytes <= 0 {
		t.Fatalf("newest.SizeBytes = %d", newest.SizeBytes)
	}
	// projB 归属
	if sessions[1].ProjectID != "projB" {
		t.Fatalf("sessions[1].ProjectID = %q, want projB", sessions[1].ProjectID)
	}

	// ── limit=2：仅最新两条 ──
	two, err := ListRolloutSessions(root, 2)
	if err != nil {
		t.Fatalf("ListRolloutSessions(limit=2): %v", err)
	}
	if len(two) != 2 || two[0].SessionID != "sess-a-new" || two[1].SessionID != "sess-b" {
		t.Fatalf("limited = %+v", two)
	}

	// ── 不存在的根目录 → 报错 ──
	if _, err := ListRolloutSessions(filepath.Join(root, "no-such-dir"), 0); err == nil {
		t.Fatalf("missing root must error")
	}
}

// ─── Preview 截断（100 rune） ───

func TestListRolloutSessions_PreviewTruncation(t *testing.T) {
	root := t.TempDir()
	longText := strings.Repeat("长", 150) // 150 rune
	writeSessionFixture(t, filepath.Join(root, "projP", "s.jsonl"),
		sessionFileLines("sess-p", "/r", "m", longText))

	sessions, err := ListRolloutSessions(root, 1)
	if err != nil {
		t.Fatalf("ListRolloutSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d", len(sessions))
	}
	if got := len([]rune(sessions[0].Preview)); got != 100 {
		t.Fatalf("preview rune count = %d, want 100", got)
	}
	if sessions[0].Preview != strings.Repeat("长", 100) {
		t.Fatalf("preview content mismatch: %q", sessions[0].Preview)
	}
}

// ─── 文件名回退解析（无 meta / agentName 段 / 畸形名） ───

func TestListRolloutSessions_FilenameFallback(t *testing.T) {
	root := t.TempDir()

	// 无 meta、无 turn_context：身份与 Preview 全部来自文件名/空
	writeSessionFixture(t, filepath.Join(root, "projF", "20250601_120000_20250601_115500_feedc0de_reviewer.jsonl"),
		[]string{rrLine(rrTs3, "response_item", rrUserItem("u1", "fallback preview"))})
	// 畸形文件名（无下划线分隔）
	writeSessionFixture(t, filepath.Join(root, "projF", "weird-session-name.jsonl"),
		[]string{rrLine(rrTs3, "response_item", rrUserItem("u1", "weird"))})
	// 完全无内容的坏 jsonl（身份回退，不 panic）
	writeSessionFixture(t, filepath.Join(root, "projF", "20250601_120000_20250601_115511_feedc123.jsonl"),
		[]string{"not json at all", "{{{", ""})

	sessions, err := ListRolloutSessions(root, 0)
	if err != nil {
		t.Fatalf("ListRolloutSessions: %v", err)
	}
	if len(sessions) != 3 {
		t.Fatalf("sessions = %d, want 3: %+v", len(sessions), sessions)
	}
	byFile := map[string]RolloutSessionInfo{}
	for _, s := range sessions {
		byFile[filepath.Base(s.Path)] = s
	}

	// agentName 段解析：len>=6 → sessionID 3 段 + agentName
	coder := byFile["20250601_120000_20250601_115500_feedc0de_reviewer.jsonl"]
	if coder.SessionID != "20250601_115500_feedc0de" || coder.AgentName != "reviewer" {
		t.Fatalf("fallback parse = %q / %q", coder.SessionID, coder.AgentName)
	}
	if coder.ProjectID != "projF" {
		t.Fatalf("fallback projectID = %q", coder.ProjectID)
	}
	// 坏文件：身份回退但 Preview 为空；meta 解析失败不致命
	bad := byFile["20250601_120000_20250601_115511_feedc123.jsonl"]
	if bad.SessionID != "20250601_115511_feedc123" || bad.AgentName != "" || bad.Preview != "" {
		t.Fatalf("bad file entry = %+v", bad)
	}
	if bad.StartedAt.IsZero() {
		t.Fatalf("bad file StartedAt should fall back to modTime")
	}
	if bad.SizeBytes <= 0 {
		t.Fatalf("bad file SizeBytes = %d", bad.SizeBytes)
	}
	// 畸形名整段当 sessionID
	weird := byFile["weird-session-name.jsonl"]
	if weird.SessionID != "weird-session-name" {
		t.Fatalf("weird sessionID = %q", weird.SessionID)
	}
	if weird.Preview != "weird" {
		t.Fatalf("weird preview = %q", weird.Preview)
	}
	// 非 meta 行的时间戳也用于 StartedAt（fallback preview 行有合法 ts）
	if !weird.StartedAt.Equal(time.Date(2025, 6, 1, 10, 0, 2, 0, time.UTC)) {
		t.Fatalf("weird StartedAt = %v", weird.StartedAt)
	}
}

// ─── parseRolloutFilename 直接表测 ───

func TestParseRolloutFilename(t *testing.T) {
	cases := []struct {
		stem              string
		wantSession       string
		wantAgent         string
	}{
		// writer 形状：文件戳(2段) + sessionID(3段)
		{"20250601_120000_20250601_115500_abc12345", "20250601_115500_abc12345", ""},
		// 带 agentName
		{"20250601_120000_20250601_115500_abc12345_coder", "20250601_115500_abc12345", "coder"},
		// agentName 自含下划线
		{"20250601_120000_20250601_115500_abc12345_sub_agent_x", "20250601_115500_abc12345", "sub_agent_x"},
		// 缺 hex 段：sessionID 为剩余段整体
		{"20250601_120000_onlysid", "onlysid", ""},
		// 畸形：无时间戳
		{"plain", "plain", ""},
	}
	for _, c := range cases {
		gotSession, gotAgent := parseRolloutFilename(c.stem)
		if gotSession != c.wantSession || gotAgent != c.wantAgent {
			t.Fatalf("parseRolloutFilename(%q) = %q/%q, want %q/%q", c.stem, gotSession, gotAgent, c.wantSession, c.wantAgent)
		}
	}
}

// ─── 不可读文件跳过 + mtime 相同的确定性排序 ───

func TestListRolloutSessions_UnreadableAndTieBreak(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 不受权限限制，跳过不可读路径用例")
	}
	root := t.TempDir()

	// 同一 mtime 的两个文件 → 路径字典序 tie-break（确定性）
	sameTime := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	for _, name := range []string{"a_first.jsonl", "b_second.jsonl"} {
		writeSessionFixture(t, filepath.Join(root, "projT", name),
			sessionFileLines("sess-"+name, "/r", "m", name))
		if err := os.Chtimes(filepath.Join(root, "projT", name), sameTime, sameTime); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
	}

	// 不可读文件：walk/scan 需容错（不 error、不 panic）
	unreadable := filepath.Join(root, "projT", "z_locked.jsonl")
	writeSessionFixture(t, unreadable, sessionFileLines("sess-z", "/r", "m", "locked"))
	if err := os.Chmod(unreadable, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	defer os.Chmod(unreadable, 0o644) // 允许 TempDir 清理

	sessions, err := ListRolloutSessions(root, 0)
	if err != nil {
		t.Fatalf("unreadable files must not fail listing: %v", err)
	}
	// 至少列出可读的两个；不可读文件（stat 失败）缺席或身份回退均可，但不得 error/panic
	if len(sessions) < 2 {
		t.Fatalf("sessions = %d, want >= 2 readable", len(sessions))
	}
	// 同 mtime tie-break：路径字典序
	if sessions[0].Path > sessions[1].Path {
		t.Fatalf("tie-break must be path ascending: %q > %q", sessions[0].Path, sessions[1].Path)
	}
}
