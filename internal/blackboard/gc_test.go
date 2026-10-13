package blackboard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// seedBoardWithFile 手工铺一个 board，因为 WriteNote 的文件名取自 time.Now，
// 而 TTL/条数测试需要能精确控制新旧。
func seedBoardWithFile(t *testing.T, boardID, agent, stem string) string {
	t.Helper()
	dir := filepath.Join(BoardDir(boardID), agent)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, stem+noteFileExt)
	data, err := encodeNote(Note{
		Version: noteSchemaVersion, NoteRef: agent + "/" + stem, BoardID: boardID, Agent: agent,
		Type: "deliverable", CreatedAt: stem, Summary: "s", BodySize: 1, Status: StatusOK,
		Body: "正文",
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func mustExist(t *testing.T, path string, want bool) {
	t.Helper()
	_, err := os.Stat(path)
	if got := err == nil; got != want {
		t.Errorf("exists(%s) = %v, want %v (err=%v)", path, got, want, err)
	}
}

func TestGCNoopWithoutOptions(t *testing.T) {
	t.Setenv("YAGENT_NOTES_ROOT", t.TempDir())
	seedBoardWithFile(t, "b1", "repo_agent", "20261013T000001Z_aaaa")

	removed, deleted, err := GC(GCOptions{}, nil)
	if err != nil || removed != 0 || deleted != 0 {
		t.Errorf("GC(no options) = (%d,%d,%v), want (0,0,nil)", removed, deleted, err)
	}
	mustExist(t, BoardDir("b1"), true)
}

func TestGCWithMissingRoot(t *testing.T) {
	t.Setenv("YAGENT_NOTES_ROOT", filepath.Join(t.TempDir(), "does-not-exist"))
	if removed, deleted, err := GC(GCOptions{TTL: time.Hour}, nil); err != nil || removed != 0 || deleted != 0 {
		t.Errorf("GC over a missing root = (%d,%d,%v), want (0,0,nil)", removed, deleted, err)
	}
}

func TestGCTTLRemovesNestedDirs(t *testing.T) {
	root := t.TempDir()
	t.Setenv("YAGENT_NOTES_ROOT", root)

	seedBoardWithFile(t, "old", "repo_agent", "20261001T000001Z_aaaa")
	// 这正是"只删 *.md"会永久泄漏的内容：agent 目录里长出一个嵌套目录。
	if err := os.MkdirAll(filepath.Join(BoardDir("old"), "repo_agent", "extra"), 0o700); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	// mtime 拨到 30 天前。
	old := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(BoardDir("old"), old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	removed, deleted, err := GC(GCOptions{TTL: 168 * time.Hour}, nil)
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if removed != 1 || deleted != 1 {
		t.Errorf("GC = (%d,%d), want (1,1)", removed, deleted)
	}
	mustExist(t, BoardDir("old"), false)
}

// 未过期的 board 绝不能被 RemoveAll：那会连带删掉正在写的便签。
func TestGCCountCapKeepsBoardDirs(t *testing.T) {
	root := t.TempDir()
	t.Setenv("YAGENT_NOTES_ROOT", root)

	stems := []string{"20261013T000001Z_aaaa", "20261013T000002Z_bbbb", "20261013T000003Z_cccc"}
	for _, s := range stems {
		seedBoardWithFile(t, "b1", "repo_agent", s)
	}

	removed, boards, err := GC(GCOptions{MaxNotesPerBoard: 2, TTL: time.Hour}, nil)
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	if boards != 0 {
		t.Errorf("boards_removed = %d, want 0（条数淘汰不该删掉活着的 board）", boards)
	}
	mustExist(t, BoardDir("b1"), true)
	// 淘汰的是最旧的那条，留下最新两条。
	mustExist(t, filepath.Join(BoardDir("b1"), "repo_agent", stems[0]+noteFileExt), false)
	mustExist(t, filepath.Join(BoardDir("b1"), "repo_agent", stems[1]+noteFileExt), true)
	mustExist(t, filepath.Join(BoardDir("b1"), "repo_agent", stems[2]+noteFileExt), true)
	// 条数淘汰不回收目录：b1 的 agent 目录与 board 目录都该还在。
	mustExist(t, filepath.Join(BoardDir("b1"), "repo_agent"), true)

	// max_notes_per_board <= 0 且没给 TTL：什么都不该动。
	untouched := seedBoardWithFile(t, "b2", "devops_agent", "20261013T000001Z_dddd")
	removed2, deleted2, err := GC(GCOptions{MaxNotesPerBoard: 0}, nil)
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if removed2 != 0 || deleted2 != 0 {
		t.Errorf("GC with no options set = (%d,%d), want (0,0)", removed2, deleted2)
	}
	mustExist(t, untouched, true)
}

// 条数上限按时间排序，而时间藏在文件名里：路径会先按 agent 目录分组，所以排序键必须是名字。
func TestGCCountCapSortsAcrossAgentDirs(t *testing.T) {
	root := t.TempDir()
	t.Setenv("YAGENT_NOTES_ROOT", root)

	seedBoardWithFile(t, "b1", "repo_agent", "20261013T000009Z_oldest")
	seedBoardWithFile(t, "b1", "coding_agent", "20261013T000010Z_newest")

	if removed, _, err := GC(GCOptions{MaxNotesPerBoard: 1}, nil); err != nil || removed != 1 {
		t.Fatalf("GC = (%d, %v), want 1 removed", removed, err)
	}
	mustExist(t, filepath.Join(BoardDir("b1"), "repo_agent", "20261013T000009Z_oldest.md"), false)
	mustExist(t, filepath.Join(BoardDir("b1"), "coding_agent", "20261013T000010Z_newest.md"), true)
}

// 中间态与未知内容都不参与计数，也不被删。
func TestGCIgnoresTmpAndUnkownEntries(t *testing.T) {
	root := t.TempDir()
	t.Setenv("YAGENT_NOTES_ROOT", root)

	seedBoardWithFile(t, "b1", "repo_agent", "20261013T000001Z_aaaa")
	keep := filepath.Join(BoardDir("b1"), "repo_agent", "20261013T000002Z_bbbb.md.tmp")
	if err := os.WriteFile(keep, []byte("half written"), 0o600); err != nil {
		t.Fatalf("write tmp: %v", err)
	}
	stray := filepath.Join(BoardDir("b1"), "README.md")
	if err := os.WriteFile(stray, []byte("hand written note"), 0o600); err != nil {
		t.Fatalf("write stray: %v", err)
	}

	if _, _, err := GC(GCOptions{MaxNotesPerBoard: 1}, nil); err != nil {
		t.Fatalf("GC: %v", err)
	}
	mustExist(t, keep, true)
	mustExist(t, stray, true)
	mustExist(t, filepath.Join(BoardDir("b1"), "repo_agent", "20261013T000001Z_aaaa.md"), true)
}

func TestGCSkipsActiveBoard(t *testing.T) {
	root := t.TempDir()
	t.Setenv("YAGENT_NOTES_ROOT", root)
	seedBoardWithFile(t, "live", "repo_agent", "20261013T000001Z_aaaa")

	old := time.Now().Add(-400 * 24 * time.Hour)
	if err := os.Chtimes(BoardDir("live"), old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	removed, deleted, err := GC(GCOptions{TTL: 168 * time.Hour}, []string{"live"})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if removed != 0 || deleted != 0 {
		t.Errorf("active board must never be collected, got (%d,%d)", removed, deleted)
	}
	mustExist(t, BoardDir("live"), true)
}

// notes 根目录里的 symlink 必须被留在原地：RemoveAll 跟着符号链接删人是不可接受的。
func TestGCSkipsSymlinkInRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("YAGENT_NOTES_ROOT", root)

	target := t.TempDir()
	link := filepath.Join(root, "linked_board")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Chtimes(link, time.Now().Add(-900*time.Hour), time.Now().Add(-900*time.Hour)); err != nil {
		t.Skipf("cannot chtimes a symlink: %v", err)
	}

	removed, deleted, err := GC(GCOptions{TTL: time.Hour}, nil)
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if removed != 0 || deleted != 0 {
		t.Errorf("GC = (%d,%d), want (0,0) — a symlink must not be RemoveAll'd through", removed, deleted)
	}
	mustExist(t, link, true)
	// 目标目录必须完好无损。
	inside := filepath.Join(target, "keep.txt")
	if err := os.WriteFile(inside, []byte("x"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if _, _, err := GC(GCOptions{TTL: time.Hour}, nil); err != nil {
		t.Fatalf("GC: %v", err)
	}
	data, err := os.ReadFile(inside)
	if err != nil || !strings.EqualFold(string(data), "x") {
		t.Errorf("symlink target was modified: %q err=%v", data, err)
	}
}
