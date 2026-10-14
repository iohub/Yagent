package datamanager

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"yagent/internal/memory"
)

// ~/.yagent/tasks/{taskID}.jsonl 是任务记忆的落盘副本（rollout 才是断点续跑的
// 事实来源，见 AGENTS.md §6.7）。这里的契约是"写下去的字节 = 内存里的消息序列"：
// 增量追加不能变全量、条数变少必须全量重写、防抖 timer 不能被删除后的任务复活。

// newTestDataManager 直接构造实例而不走 NewDataManager：后者把目录钉在真实
// 用户主目录上，还会异步 reconcile 别人的历史任务。
func newTestDataManager(t *testing.T) *DataManager {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "tasks") // indexPath() 取父目录，模拟 .yagent 布局
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	dm := &DataManager{
		dataDir:   dir,
		writers:   make(map[string]*taskWriterState),
		persistCh: make(chan struct{}, 1),
		stopCh:    make(chan struct{}),
	}
	t.Cleanup(func() { dm.Stop() })
	return dm
}

// shrinkDebounce 把防抖窗口缩到几十毫秒：耗时由测试自己负责，
// 而不是让整套套件等 5 秒 timer。
func shrinkDebounce(t *testing.T, d time.Duration) {
	t.Helper()
	prev := taskFlushDebounce
	taskFlushDebounce = d
	t.Cleanup(func() { taskFlushDebounce = prev })
}

func human(content string, ts time.Time) memory.ChatMessage {
	return memory.ChatMessage{Type: memory.MessageTypeHuman, Content: content, Timestamp: ts}
}

func ai(content string, ts time.Time) memory.ChatMessage {
	return memory.ChatMessage{Type: memory.MessageTypeAssistant, Content: content, Timestamp: ts}
}

func mem(msgs ...memory.ChatMessage) *memory.ConversationMemory {
	return &memory.ConversationMemory{Messages: msgs}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024), 10*1024*1024)
	for sc.Scan() {
		if line := sc.Text(); line != "" {
			out = append(out, line)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
	return out
}

func contents(t *testing.T, path string) []string {
	t.Helper()
	var out []string
	for _, line := range readLines(t, path) {
		var m memory.ChatMessage
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("第 %d 行不是合法 JSONL: %v (%s)", len(out)+1, err, line)
		}
		out = append(out, m.Content)
	}
	return out
}

// ---------- 写入模式 ----------

// 追加语义：第二次 SaveTaskMemory 只能追加新消息，不能重写已有行。
// 写成全量重写的实现同样"看起来对"，但会丢掉前一次 flush 之后由别的进程写入的内容。
func TestSaveTaskMemory_AppendsOnlyNewMessages(t *testing.T) {
	dm := newTestDataManager(t)
	path := dm.GetTaskMemoryPath("t1")
	base := time.Now()

	if err := dm.SaveTaskMemory("t1", mem(human("q1", base), ai("a1", base.Add(time.Second)))); err != nil {
		t.Fatalf("SaveTaskMemory: %v", err)
	}
	if err := dm.FlushTaskMemory("t1"); err != nil {
		t.Fatalf("FlushTaskMemory: %v", err)
	}
	if got := strings.Join(contents(t, path), ","); got != "q1,a1" {
		t.Fatalf("第一轮之后 = %q, want q1,a1", got)
	}

	// 同一条消息序列里长出的一条新消息：只应追加它。
	if err := dm.SaveTaskMemory("t1", mem(
		human("q1", base), ai("a1", base.Add(time.Second)), human("q2", base.Add(2*time.Second)))); err != nil {
		t.Fatalf("SaveTaskMemory: %v", err)
	}
	if err := dm.FlushTaskMemory("t1"); err != nil {
		t.Fatalf("FlushTaskMemory: %v", err)
	}
	if got := strings.Join(contents(t, path), ","); got != "q1,a1,q2" {
		t.Errorf("追加之后 = %q, want q1,a1,q2（重复写入或漏写都说明增量游标坏了）", got)
	}

	// 没有新消息时不该再写一行（重复行会污染 /history 的 MessageCount）。
	if err := dm.SaveTaskMemory("t1", mem(
		human("q1", base), ai("a1", base.Add(time.Second)), human("q2", base.Add(2*time.Second)))); err != nil {
		t.Fatalf("SaveTaskMemory: %v", err)
	}
	if err := dm.FlushTaskMemory("t1"); err != nil {
		t.Fatalf("FlushTaskMemory: %v", err)
	}
	if n := len(readLines(t, path)); n != 3 {
		t.Errorf("重复保存之后有 %d 行, want 3", n)
	}

	// 落盘内容必须能被 LoadTaskMemory 原样读回。
	back, err := dm.LoadTaskMemory("t1")
	if err != nil {
		t.Fatalf("LoadTaskMemory: %v", err)
	}
	if len(back.Messages) != 3 || back.Messages[2].Content != "q2" {
		t.Errorf("读回 = %+v, want 3 条且末条是 q2", back.Messages)
	}
}

// 条数变少（压缩/重置）必须触发全量重写。留着追加语义的话，磁盘上会留下
// 已被裁掉的消息，下一次 /resume 就重建出一个"记忆比实际长"的会话。
func TestSaveTaskMemory_RewritesWhenMessageCountShrinks(t *testing.T) {
	dm := newTestDataManager(t)
	path := dm.GetTaskMemoryPath("t2")
	base := time.Now()

	full := mem(human("q1", base), ai("a1", base), human("q2", base), ai("a2", base))
	if err := dm.SaveTaskMemory("t2", full); err != nil {
		t.Fatalf("SaveTaskMemory: %v", err)
	}
	if err := dm.FlushTaskMemory("t2"); err != nil {
		t.Fatalf("FlushTaskMemory: %v", err)
	}
	if n := len(readLines(t, path)); n != 4 {
		t.Fatalf("基线 = %d 行, want 4", n)
	}

	// 压缩后 memory 只剩 [system, one user]。
	compacted := mem(human("摘要：此前讨论了 q1/q2", base))
	if err := dm.SaveTaskMemory("t2", compacted); err != nil {
		t.Fatalf("SaveTaskMemory after compaction: %v", err)
	}
	if err := dm.FlushTaskMemory("t2"); err != nil {
		t.Fatalf("FlushTaskMemory: %v", err)
	}

	got := contents(t, path)
	if len(got) != 1 {
		t.Fatalf("重写之后 = %q, want 只剩摘要那一条（旧消息必须被截掉）", got)
	}
	if !strings.Contains(got[0], "摘要") {
		t.Errorf("内容 = %q, want 摘要行", got[0])
	}

	// 重写之后增量游标必须重新对准：再追加一条应该得到 2 行。
	if err := dm.SaveTaskMemory("t2", mem(
		human("摘要：此前讨论了 q1/q2", base), ai("a3", base))); err != nil {
		t.Fatalf("SaveTaskMemory: %v", err)
	}
	if err := dm.FlushTaskMemory("t2"); err != nil {
		t.Fatalf("FlushTaskMemory: %v", err)
	}
	if got := strings.Join(contents(t, path), ","); got != "摘要：此前讨论了 q1/q2,a3" {
		t.Errorf("重写后继续追加 = %q, want 摘要,a3", got)
	}
}

// 防抖：一次任务里高频保存不该每次都落盘，但最终必须落盘且内容完整。
func TestSaveTaskMemory_DebouncesAndFlushesEventually(t *testing.T) {
	shrinkDebounce(t, 40*time.Millisecond)
	dm := newTestDataManager(t)
	path := dm.GetTaskMemoryPath("t3")
	base := time.Now()

	for i := 0; i < 20; i++ {
		msgs := make([]memory.ChatMessage, 0, i+1)
		for j := 0; j <= i; j++ {
			msgs = append(msgs, human(fmt.Sprintf("m%d", j), base))
		}
		if err := dm.SaveTaskMemory("t3", mem(msgs...)); err != nil {
			t.Fatalf("SaveTaskMemory(%d): %v", i, err)
		}
	}

	waitForFile(t, path, 20)
	got := contents(t, path)
	if len(got) != 20 {
		t.Fatalf("防抖之后 = %d 行, want 20", len(got))
	}
	if got[0] != "m0" || got[19] != "m19" {
		t.Errorf("内容 = %q, want m0..m19 顺序完整（防抖把中间状态丢了就会缺行）", got)
	}
}

func waitForFile(t *testing.T, path string, wantLines int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if lines := readLinesQuiet(path); len(lines) >= wantLines {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待 %s 写出 %d 行超时（现有 %d 行）", path, wantLines, len(readLinesQuiet(path)))
}

func readLinesQuiet(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024), 10*1024*1024)
	for sc.Scan() {
		if line := sc.Text(); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// 删除任务后，先前 SaveTaskMemory 排下的 timer 绝不能把文件再写回来
// （/history 里会冒出一个用户已经删掉的会话）。
func TestDeleteTaskMemoryCancelsPendingFlush(t *testing.T) {
	shrinkDebounce(t, 60*time.Millisecond)
	dm := newTestDataManager(t)
	path := dm.GetTaskMemoryPath("t4")

	if err := dm.SaveTaskMemory("t4", mem(human("q", time.Now()))); err != nil {
		t.Fatalf("SaveTaskMemory: %v", err)
	}
	if err := dm.DeleteTaskMemory("t4"); err != nil && !os.IsNotExist(err) {
		t.Fatalf("DeleteTaskMemory: %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(path); err == nil {
		t.Errorf("删除之后 timer 又把 %s 写回来了", path)
	} else if !os.IsNotExist(err) {
		t.Errorf("stat: %v", err)
	}

	// writer 状态也必须一起清掉，否则同名任务会继承旧的 lastFlushedCount。
	dm.mu.Lock()
	_, still := dm.writers["t4"]
	dm.mu.Unlock()
	if still {
		t.Error("删除任务后 writer 状态仍留在 map 里")
	}
}

func TestFlushAll(t *testing.T) {
	dm := newTestDataManager(t)
	base := time.Now()

	for _, id := range []string{"a", "b", "c"} {
		if err := dm.SaveTaskMemory(id, mem(human("q-"+id, base))); err != nil {
			t.Fatalf("SaveTaskMemory(%s): %v", id, err)
		}
	}
	if err := dm.FlushAll(); err != nil {
		t.Fatalf("FlushAll: %v", err)
	}
	for _, id := range []string{"a", "b", "c"} {
		got := contents(t, dm.GetTaskMemoryPath(id))
		if len(got) != 1 || got[0] != "q-"+id {
			t.Errorf("任务 %s = %q, want [q-%s]", id, got, id)
		}
	}

	// 没有待写入内容时 FlushAll 必须无害（空 writers 也不报错）。
	if err := dm.FlushAll(); err != nil {
		t.Errorf("二次 FlushAll: %v", err)
	}
	// 未知任务的 FlushTaskMemory 是 no-op 而不是错误。
	if err := dm.FlushTaskMemory("never-saved"); err != nil {
		t.Errorf("FlushTaskMemory(未知任务) = %v, want nil", err)
	}
}

// 写入失败必须被报告而不是吞掉：任务记忆是 /history 与恢复的数据源。
func TestFlushReportsWriteErrors(t *testing.T) {
	dm := newTestDataManager(t)
	// SaveTaskMemory 在登记时就固化了 filePath，所以必须在保存之前把目录
	// 指向一个不存在的位置 —— 否则 os.Create 会成功，这条错误路径根本没被走到。
	dm.dataDir = filepath.Join(dm.dataDir, "missing", "nested")

	if err := dm.SaveTaskMemory("t", mem(human("q", time.Now()))); err != nil {
		t.Fatalf("SaveTaskMemory 只做内存登记, 不该报错: %v", err)
	}
	if err := dm.FlushTaskMemory("t"); err == nil {
		t.Error("目录不存在时 FlushTaskMemory 返回 nil, want 错误")
	} else if !strings.Contains(err.Error(), "flush task memory failed") {
		t.Errorf("err = %v, want 带 taskID 上下文的说明", err)
	}
}

// ---------- 读取 ----------

// 坏行必须被跳过而不是让整个文件读不出来（进程被杀时最后一行常是半截 JSON）。
func TestLoadTaskMemorySkipsCorruptLines(t *testing.T) {
	dm := newTestDataManager(t)
	path := dm.GetTaskMemoryPath("t5")

	good1, _ := json.Marshal(human("一", time.Now()))
	good2, _ := json.Marshal(ai("二", time.Now()))
	content := string(good1) + "\n" + "{这不是JSON" + "\n\n" + string(good2) + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	mem, err := dm.LoadTaskMemory("t5")
	if err != nil {
		t.Fatalf("LoadTaskMemory: %v", err)
	}
	if len(mem.Messages) != 2 {
		t.Fatalf("读回 %d 条, want 2（坏行与空行被跳过）", len(mem.Messages))
	}
	if mem.Messages[0].Content != "一" || mem.Messages[1].Content != "二" {
		t.Errorf("内容 = %q", []string{mem.Messages[0].Content, mem.Messages[1].Content})
	}

	// 超大行不能把 scanner 撑爆（默认 64KB 上限会让长工具结果整段读丢）。
	big := strings.Repeat("很长", 200000) // 40 万字符
	if err := os.WriteFile(path, []byte(mustMarshalLine(t, human(big, time.Now()))+"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	mem, err = dm.LoadTaskMemory("t5")
	if err != nil {
		t.Fatalf("LoadTaskMemory(大行): %v", err)
	}
	if len(mem.Messages) != 1 || mem.Messages[0].Content != big {
		t.Errorf("大行读回 %d 条 / 长度 %d, want 1 条完整内容", len(mem.Messages), len(mem.Messages[0].Content))
	}

	if _, err := dm.LoadTaskMemory("no-such-task"); err == nil {
		t.Error("读不存在的任务返回了 nil error")
	}
}

func mustMarshalLine(t *testing.T, m memory.ChatMessage) string {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// ---------- 历史列表 ----------

func TestListTaskMemoriesAndHistory(t *testing.T) {
	dm := newTestDataManager(t)
	base := time.Now()

	// 三个任务：正常 / 无人类消息（标题回落文件名）/ 超长标题（按 rune 截断）
	long := strings.Repeat("字", 40)
	if err := dm.SaveTaskMemory("normal", mem(human("普通任务", base), ai("答", base.Add(time.Minute)))); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := dm.SaveTaskMemory("nohuman", mem(ai("只有模型话", base))); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := dm.SaveTaskMemory("long", mem(human(long, base.Add(2*time.Minute)))); err != nil {
		t.Fatalf("save: %v", err)
	}
	// 干扰项：非 jsonl 与子目录必须被忽略。
	if err := os.WriteFile(filepath.Join(dm.dataDir, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dm.dataDir, "sub.jsonl"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := dm.FlushAll(); err != nil {
		t.Fatalf("FlushAll: %v", err)
	}

	ids, err := dm.ListTaskMemories()
	if err != nil {
		t.Fatalf("ListTaskMemories: %v", err)
	}
	joined := strings.Join(ids, ",")
	for _, want := range []string{"normal", "nohuman", "long"} {
		if !strings.Contains(joined, want) {
			t.Errorf("ListTaskMemories = %q, want 含 %s", joined, want)
		}
	}
	if strings.Contains(joined, "notes") || strings.Contains(joined, "sub") {
		t.Errorf("ListTaskMemories 把非任务文件也算进来了: %q", joined)
	}

	items, err := dm.ListTaskHistory(0)
	if err != nil {
		t.Fatalf("ListTaskHistory: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("历史 = %d 条, want 3: %+v", len(items), items)
	}
	// 按 UpdatedAt 倒序：long(2m) > normal(1m) > nohuman(0m)
	if items[0].TaskID != "long" || items[1].TaskID != "normal" || items[2].TaskID != "nohuman" {
		t.Errorf("排序 = %v, want [long normal nohuman]", historyIDs(items))
	}
	if want := string([]rune(long)[:30]) + "…"; items[0].Title != want {
		t.Errorf("超长标题 = %q, want %q（按 rune 截断，按字节切会切断汉字）", items[0].Title, want)
	}
	if items[2].Title != "nohuman.jsonl" {
		t.Errorf("无人类消息的标题 = %q, want 回落文件名 nohuman.jsonl", items[2].Title)
	}
	if items[1].MessageCount != 2 {
		t.Errorf("normal 的 MessageCount = %d, want 2", items[1].MessageCount)
	}

	limited, err := dm.ListTaskHistory(1)
	if err != nil {
		t.Fatalf("ListTaskHistory(1): %v", err)
	}
	if len(limited) != 1 || limited[0].TaskID != "long" {
		t.Errorf("limit=1 = %+v, want 只剩最新那条", limited)
	}
	if neg, err := dm.ListTaskHistory(-1); err != nil || len(neg) != 3 {
		t.Errorf("limit<=0 = %d 条 (err=%v), want 全部 3 条", len(neg), err)
	}
}

func historyIDs(items []TaskHistoryItem) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.TaskID)
	}
	return out
}

// 索引路径与全量扫描必须给出同一份历史：TUI 走索引、降级走扫描，
// 两边不一致的表现是"同一个任务在 /history 里换了名字或条数"。
func TestListTaskHistoryFastMatchesFullScan(t *testing.T) {
	dm := newTestDataManager(t)
	base := time.Now()

	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("task-%d", i)
		if err := dm.SaveTaskMemory(id, mem(
			human(fmt.Sprintf("问题 %d", i), base.Add(time.Duration(i)*time.Minute)),
			ai(fmt.Sprintf("回答 %d", i), base.Add(time.Duration(i)*time.Minute+30*time.Second)),
		)); err != nil {
			t.Fatalf("save %s: %v", id, err)
		}
	}
	if err := dm.FlushAll(); err != nil {
		t.Fatalf("FlushAll: %v", err)
	}

	full, err := dm.ListTaskHistory(0)
	if err != nil {
		t.Fatalf("ListTaskHistory: %v", err)
	}
	fast, err := dm.ListTaskHistoryFast(0)
	if err != nil {
		t.Fatalf("ListTaskHistoryFast: %v", err)
	}
	if len(fast) != len(full) {
		t.Fatalf("索引给出 %d 条, 全量扫描 %d 条", len(fast), len(full))
	}
	for i := range full {
		if fast[i].TaskID != full[i].TaskID {
			t.Errorf("第 %d 位: 索引=%s 扫描=%s", i, fast[i].TaskID, full[i].TaskID)
		}
		if fast[i].Title != full[i].Title {
			t.Errorf("%s: 标题 索引=%q 扫描=%q", full[i].TaskID, fast[i].Title, full[i].Title)
		}
		if fast[i].MessageCount != full[i].MessageCount {
			t.Errorf("%s: MessageCount 索引=%d 扫描=%d", full[i].TaskID, fast[i].MessageCount, full[i].MessageCount)
		}
	}
	if ids := historyIDs(fast); ids[0] != "task-4" {
		t.Errorf("索引排序 = %v, want 最新在前", ids)
	}

	// limit 语义与全量扫描一致。
	limited, err := dm.ListTaskHistoryFast(2)
	if err != nil {
		t.Fatalf("ListTaskHistoryFast(2): %v", err)
	}
	if len(limited) != 2 {
		t.Errorf("limit=2 给出 %d 条", len(limited))
	}
}

// 索引损坏 / 版本不符 / 缺 tasks 时必须重建而不是启动失败。
func TestIndexRebuildsFromDiskWhenUnusable(t *testing.T) {
	cases := map[string]string{
		"坏 JSON":      `{"version":1,"tasks":{`,
		"版本不符":        `{"version":99,"tasks":{}}`,
		"tasks 为 nil": `{"version":1}`,
	}
	for name, body := range cases {
		name, body := name, body
		t.Run(name, func(t *testing.T) {
			dm := newTestDataManager(t)
			base := time.Now()
			if err := dm.SaveTaskMemory("keep", mem(human("留在磁盘上的任务", base))); err != nil {
				t.Fatalf("save: %v", err)
			}
			if err := dm.FlushTaskMemory("keep"); err != nil {
				t.Fatalf("flush: %v", err)
			}
			if err := os.WriteFile(dm.indexPath(), []byte(body), 0o600); err != nil {
				t.Fatalf("write index: %v", err)
			}

			// 重新加载：ensureIndex 必须走重建而不是报错。
			dm.indexMu.Lock()
			dm.indexLoaded = false
			dm.index = nil
			dm.indexMu.Unlock()

			items, err := dm.ListTaskHistoryFast(0)
			if err != nil {
				t.Fatalf("索引不可用时应该重建而不是失败: %v", err)
			}
			if len(items) != 1 || items[0].TaskID != "keep" {
				t.Errorf("重建后的索引 = %+v, want 从磁盘上的 .jsonl 复原出 keep", items)
			}
			if items[0].Title != "留在磁盘上的任务" {
				t.Errorf("重建出的标题 = %q, want 留在磁盘上的任务", items[0].Title)
			}
			// 重建必须把新索引写回磁盘（否则下次启动再走一遍全量扫描）。
			_, err = dm.loadIndexFromDisk()
			if err != nil {
				t.Errorf("重建后的索引没有落盘: %v", err)
			}
		})
	}
}

// 索引里残留已删除任务的条目必须被 reconcile 清掉，否则 /history 会显示幽灵会话。
func TestReconcileDropsEntriesForMissingFiles(t *testing.T) {
	dm := newTestDataManager(t)
	base := time.Now()
	if err := dm.SaveTaskMemory("real", mem(human("真实任务", base))); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := dm.FlushTaskMemory("real"); err != nil {
		t.Fatalf("flush: %v", err)
	}

	if err := dm.ensureIndex(); err != nil {
		t.Fatalf("ensureIndex: %v", err)
	}
	dm.indexMu.Lock()
	dm.index.Tasks["ghost"] = IndexEntry{TaskID: "ghost", Title: "幽灵任务", UpdatedAt: base, MsgCount: 3}
	dm.indexMu.Unlock()

	dm.indexMu.RLock()
	idx := dm.index
	dm.indexMu.RUnlock()
	if err := dm.reconcileIndex(idx); err != nil {
		t.Fatalf("reconcileIndex: %v", err)
	}
	dm.indexMu.RLock()
	_, hasGhost := dm.index.Tasks["ghost"]
	_, hasReal := dm.index.Tasks["real"]
	dm.indexMu.RUnlock()
	if hasGhost {
		t.Error("reconcile 之后幽灵条目仍在索引里")
	}
	if !hasReal {
		t.Error("reconcile 把仍然存在的任务也删了")
	}
}

// ---------- 并发 ----------

// 多个任务并发保存：writer 是按 taskID 分状态的，不能互相污染。
// 同一任务并发保存则由 ws.mu 串行化，落盘必须是完整行（半行会让坏行被跳过=丢消息）。
func TestConcurrentSavesStaySeparated(t *testing.T) {
	dm := newTestDataManager(t)
	base := time.Now()

	const tasks = 8
	const perTask = 12
	var wg sync.WaitGroup
	wg.Add(tasks)
	for i := 0; i < tasks; i++ {
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("task-%d", i)
			for j := 1; j <= perTask; j++ {
				msgs := make([]memory.ChatMessage, 0, j)
				for k := 0; k < j; k++ {
					msgs = append(msgs, human(fmt.Sprintf("t%d-m%d", i, k), base))
				}
				if err := dm.SaveTaskMemory(id, mem(msgs...)); err != nil {
					t.Errorf("SaveTaskMemory(%s): %v", id, err)
					return
				}
			}
		}(i)
	}
	wg.Wait()

	if err := dm.FlushAll(); err != nil {
		t.Fatalf("FlushAll: %v", err)
	}
	for i := 0; i < tasks; i++ {
		id := fmt.Sprintf("task-%d", i)
		got := contents(t, dm.GetTaskMemoryPath(id))
		if len(got) != perTask {
			t.Errorf("%s 有 %d 行, want %d", id, len(got), perTask)
			continue
		}
		for k, line := range got {
			if want := fmt.Sprintf("t%d-m%d", i, k); line != want {
				t.Errorf("%s 第 %d 行 = %q, want %q（串任务或顺序乱了）", id, k, line, want)
			}
		}
	}
}
