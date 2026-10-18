package blackboard

// 并发写便签的命名唯一性 —— 方案 §8.2 原来担心"两个并发 delegate_repo 写同一目录会互相覆盖"，
// 实测 stem 是 UTC 秒级时间戳 + 随机后缀（GenerateNoteFilename），所以这件事本来就不会发生。
// 这条测试把它钉成契约：以后谁把 stem 改成"按任务文本命名"，这里会先红，
// 而不是等到并行委派的结果互相吃掉才发现。

import (
	"sync"
	"testing"
)

func TestConcurrentWriteNotesNeverCollide(t *testing.T) {
	// 自己拥有便签根目录：本包的其它测试都这么隔离，不隔离就会往真实 ~/.yagent/notes 里堆文件。
	t.Setenv("YAGENT_NOTES_ROOT", t.TempDir())
	board := "board-stem-uniqueness"

	const n = 24
	refs := make([]string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ref, err := WriteNote(NoteInput{
				BoardID: board,
				Agent:   "repo_agent",
				Tool:    "delegate_repo",
				Task:    "同一个任务被并发委派两次",
				Summary: "结论",
				Body:    "正文",
				Status:  "ok",
			})
			if err != nil {
				t.Errorf("WriteNote %d: %v", i, err)
				return
			}
			refs[i] = ref
		}(i)
	}
	wg.Wait()

	seen := map[string]bool{}
	for _, r := range refs {
		if r == "" {
			continue
		}
		if seen[r] {
			t.Errorf("两张便签共用 noteRef %q —— 后写的会把先写的正文吃掉，而没有任何错误", r)
		}
		seen[r] = true
	}
	if len(seen) != n {
		t.Errorf("拿到 %d 个不同 noteRef, want %d", len(seen), n)
	}
}
