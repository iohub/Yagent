package blackboard

// 并发写便签的命名唯一性 —— 方案 §8.2 原来担心"两个并发 delegate_repo 写同一目录会互相覆盖"，
// 当时下的结论是"stem 带随机段，所以不会发生"。**那个结论是错的**：随机段只有 2 字节，
// 同进程 20 个并发写撞名概率约 0.3%，撞上的表现是 rename 静默吃掉先写那张的正文。
// 现在唯一性来自 `GenerateNoteFilename` 里的进程内单调计数器 + pid，随机段只是兜 pid 复用。
// 两条用例分工不同：这条压的是端到端（写盘 + 列举），下面那条把随机性关掉、只留机制。

import (
	"strings"
	"sync"
	"testing"
)

// TestGenerateNoteFilenameSurvivesConstantRandomness 把随机段钉成常量：
// 500 次调用全部落在同一秒、同一个随机值上，名字仍然必须两两不同。
// 删掉计数器（或让它不递增）当场红 —— 这比"跑很多次祈祷不撞"硬。
func TestGenerateNoteFilenameSurvivesConstantRandomness(t *testing.T) {
	old := noteRandom
	noteRandom = func(b []byte) (int, error) {
		for i := range b {
			b[i] = 0xAB
		}
		return len(b), nil
	}
	t.Cleanup(func() { noteRandom = old })

	const n = 500
	seen := make(map[string]bool, n)
	prefix := ""
	for i := 0; i < n; i++ {
		name, err := GenerateNoteFilename()
		if err != nil {
			t.Fatalf("GenerateNoteFilename %d: %v", i, err)
		}
		if i == 0 {
			prefix = name[:strings.Index(name, "-")]
		}
		if !strings.HasPrefix(name, prefix) {
			t.Fatalf("第 %d 条 %q 没带同一个时间戳+随机段 %q：本用例没有把随机性关掉，测的是运气", i, name, prefix)
		}
		if seen[name] {
			t.Fatalf("第 %d 次生成重名 %q：随机段之外没有唯一性来源，写盘时 rename 会吃掉前一张", i, name)
		}
		seen[name] = true
	}
	if len(seen) != n {
		t.Errorf("拿到 %d 个不同名字, want %d", len(seen), n)
	}
}

func TestConcurrentWriteNotesNeverCollide(t *testing.T) {
	// 自己拥有便签根目录：本包的其它测试都这么隔离，不隔离就会往真实 ~/.yagent/notes 里堆文件。
	t.Setenv("YAGENT_NOTES_ROOT", t.TempDir())
	board := "board-stem-uniqueness"

	const n = 200 // 修复前这个数量级下"至少撞一次"的概率约 78%；修复后必须是恒不撞
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
