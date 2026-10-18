package memory

// rollout_writer_queue_test.go — C2 的闸门（docs/structured-concurrency-design.md §7）。
//
// 落盘从"每条一次 write(2)"改成"入队即返回 + 攒批刷盘"之后，唯一要紧的事是：
//   ① 一条都不能丢（rollout 是 /resume 唯一的事实来源）；
//   ② 顺序不能乱（重放按文件顺序重建消息）；
//   ③ **读回之前必须排干**（检查点指纹由读侧对整份文件重算，队列没空就会少算几条，
//      而少算的那份指纹不会报错 —— 它让本来能重放的会话被判定成损坏、静默降级）。
// 每个用例都自己 isolateHome：AGENTS.md §11 坑 23 记过"扫目录再计数"的测试共享真实 ~/.yagent 的后果。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"yagent/internal/llm"
)

// writeItems 把 msgs 逐条写成 response_item（与两个生产写入点同一套调用形状）。
func writeItems(t *testing.T, w *RolloutWriter, msgs []llm.Message) {
	t.Helper()
	for _, m := range msgs {
		for _, item := range LLMMessageToResponseItems(m, w.NextMessageID()) {
			if err := w.WriteResponseItem(item); err != nil {
				t.Fatalf("write item: %v", err)
			}
		}
	}
}

func userMsgs(n int, format string) []llm.Message {
	out := make([]llm.Message, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, llm.Message{Role: llm.RoleUser, Content: fmt.Sprintf(format, i)})
	}
	return out
}

// 队列容量是 256，这里刻意写到远超它：既测"一条不丢"，也测背压（队满时阻塞生产者而不是丢）。
func TestRolloutQueuePreservesOrderUnderLoad(t *testing.T) {
	isolateHome(t)
	const total = 1200

	w, err := NewRolloutWriter("coding", "task-queue-order", "queue-order-proj")
	if err != nil || !w.Enabled() {
		t.Skipf("rollout writer unavailable: %v", err)
	}
	msgs := userMsgs(total, "item-%04d")
	writeItems(t, w, msgs)

	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	replay, err := ReadRolloutForReplay(w.FilePath(), ReplayOptions{})
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(replay.Messages) != total {
		t.Fatalf("重放重建出 %d 条消息, want %d —— 队列丢记录等于 /resume 的账本造假",
			len(replay.Messages), total)
	}
	for i, m := range replay.Messages {
		want := fmt.Sprintf("item-%04d", i)
		if !strings.Contains(m.Content, want) {
			t.Fatalf("第 %d 条 = %q, want %q —— 顺序不是入队顺序", i, m.Content, want)
		}
	}
}

// 多个生产者同时写（并行委派 + 同域 writer 的真实形状）：一条都不能少，一条都不能重复。
func TestRolloutConcurrentProducersAllLand(t *testing.T) {
	isolateHome(t)
	const producers, perProducer = 8, 60

	w, err := NewRolloutWriter("repo", "task-queue-conc", "queue-conc-proj")
	if err != nil || !w.Enabled() {
		t.Skipf("rollout writer unavailable: %v", err)
	}

	var wg sync.WaitGroup
	for p := 0; p < producers; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			for i := 0; i < perProducer; i++ {
				m := llm.Message{Role: llm.RoleUser, Content: fmt.Sprintf("p%d-i%d", p, i)}
				for _, item := range LLMMessageToResponseItems(m, w.NextMessageID()) {
					if err := w.WriteResponseItem(item); err != nil {
						t.Errorf("producer %d write: %v", p, err)
						return
					}
				}
			}
		}(p)
	}
	wg.Wait()

	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	replay, err := ReadRolloutForReplay(w.FilePath(), ReplayOptions{})
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(replay.Messages) != producers*perProducer {
		t.Fatalf("重建 %d 条, want %d", len(replay.Messages), producers*perProducer)
	}
	seen := map[string]bool{}
	for _, m := range replay.Messages {
		if seen[m.Content] {
			t.Errorf("同一条写了两次: %q", m.Content)
		}
		seen[m.Content] = true
	}
}

// 承重的那一条：写完不做任何显式 flush，直接走 WriteCheckpoint，
// 检查点里的指纹与消息数必须仍然与整份文件一致。
//
// 这条测试存在的理由就是 §7.3 那个耦合：落盘批量之后，"内存里已经写完"与"盘上看得见"
// 之间出现了窗口；指纹又必须由读侧算（否则触发过压缩的会话一律对不上号），
// 所以窗口里的读回等于把可用会话判成损坏。变异 M2 就是把 flush 那一行删掉。
func TestCheckpointDigestSeesQueuedRecords(t *testing.T) {
	isolateHome(t)

	cwd, _ := os.Getwd()
	w, err := NewRolloutWriter("coding", "task-queue-cp", "queue-cp-proj")
	if err != nil || !w.Enabled() {
		t.Skipf("rollout writer unavailable: %v", err)
	}
	if err := w.WriteSessionMeta(SessionMeta{ID: w.SessionID(), SessionID: w.SessionID(), Cwd: cwd}); err != nil {
		t.Fatalf("meta: %v", err)
	}
	// 200 条：远超攒批阈值(32)与 20ms 定时器，保证其中至少一部分此刻确实还压在队列里。
	writeItems(t, w, userMsgs(200, "queued-%04d"))

	// 不 Flush、不 Close —— 只走生产的写检查点入口。
	WriteCheckpoint(context.Background(), w, CheckpointInput{
		AgentName: "coding", StopKind: StopKindCancelled, Steps: 3, MaxSteps: 10,
	})

	replay, err := ReadRolloutForReplay(w.FilePath(), ReplayOptions{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replay.Checkpoint == nil {
		t.Fatal("文件里没有检查点 —— WriteCheckpoint 没落盘")
	}
	if replay.Checkpoint.Digest != replay.Digest {
		t.Errorf("stored digest %q != rebuilt %q —— 检查点写之前没排干队列，恢复侧会把这个会话判成损坏",
			replay.Checkpoint.Digest, replay.Digest)
	}
	if replay.Checkpoint.MessageCount != len(replay.Messages) {
		t.Errorf("stored MessageCount = %d, rebuilt %d —— 少的那几条就是队列里没落盘的",
			replay.Checkpoint.MessageCount, len(replay.Messages))
	}
	for _, d := range replay.Degraded {
		if strings.Contains(d, "digest") || strings.Contains(d, "count") {
			t.Errorf("重放被判降级: %q", d)
		}
	}

	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// Flush 的语义是"排干并回报落盘错误"：排干之后再读，必须已经看得见全部记录。
func TestFlushMakesQueuedRecordsReadable(t *testing.T) {
	isolateHome(t)

	w, err := NewRolloutWriter("coding", "task-queue-flush", "queue-flush-proj")
	if err != nil || !w.Enabled() {
		t.Skipf("rollout writer unavailable: %v", err)
	}
	writeItems(t, w, userMsgs(5, "flushed-%04d"))

	if err := w.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	data, err := os.ReadFile(w.FilePath())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := strings.Count(string(data), "flushed-"); got != 5 {
		t.Errorf("排干后盘上只有 %d 条, want 5", got)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// Flush 必须尊重 ctx：调用方（取消路径上的收尾）不该被它挂住。
func TestFlushHonoursCancelledContext(t *testing.T) {
	isolateHome(t)

	w, err := NewRolloutWriter("coding", "task-queue-flushctx", "queue-flushctx-proj")
	if err != nil || !w.Enabled() {
		t.Skipf("rollout writer unavailable: %v", err)
	}
	writeItems(t, w, userMsgs(3, "ctx-%04d"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.Flush(ctx); err == nil {
		t.Error("已取消的 ctx 下 Flush 仍然成功返回，说明它没有尊重调用方的取消")
	}
	// 取消 Flush 之后，writer 本身必须还能正常收尾（记录不能因为这次取消就丢）。
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	replay, rerr := ReadRolloutForReplay(w.FilePath(), ReplayOptions{})
	if rerr != nil {
		t.Fatalf("replay: %v", rerr)
	}
	if len(replay.Messages) != 3 {
		t.Errorf("Close 之后重建出 %d 条, want 3 —— 取消一次 Flush 不该带走记录", len(replay.Messages))
	}
}

// Close 之后写入必须安静地不生效：不 panic、不 hang、也不往已关闭的文件里塞东西。
func TestWriteAfterCloseIsInert(t *testing.T) {
	isolateHome(t)

	w, err := NewRolloutWriter("coding", "task-queue-after", "queue-after-proj")
	if err != nil || !w.Enabled() {
		t.Skipf("rollout writer unavailable: %v", err)
	}
	writeItems(t, w, userMsgs(2, "before-%04d"))
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	before, _ := os.ReadFile(w.FilePath())

	// 二次 Close 也必须幂等（生产里 delegateTo 的 defer 与显式收尾可能都调）。
	if err := w.Close(); err != nil {
		t.Errorf("second Close returned %v, want nil", err)
	}
	writeItems(t, w, userMsgs(3, "after-%04d"))
	if err := w.Flush(context.Background()); err != nil {
		t.Errorf("Flush after Close returned %v, want nil", err)
	}

	after, _ := os.ReadFile(w.FilePath())
	if string(before) != string(after) {
		t.Errorf("Close 之后文件又长了 —— 已经关掉的 fd 上不该有任何写入")
	}
}

// 未启用的 writer（目录建不出来 / 打开失败的两条返回路径）必须全程 no-op：
// 它没有队列也没有写循环，任何一次 nil channel 操作都会让调用点 hang。
func TestDisabledWriterIsNoOp(t *testing.T) {
	w := &RolloutWriter{enabled: false, sessionID: "s", agentName: "coding"}

	if err := w.WriteSessionMeta(SessionMeta{ID: "s"}); err != nil {
		t.Errorf("WriteSessionMeta on disabled writer: %v", err)
	}
	if err := w.WriteTurnContext(TurnContext{TurnID: "t"}); err != nil {
		t.Errorf("WriteTurnContext on disabled writer: %v", err)
	}
	if err := w.WriteResponseItem(ResponseItem{Type: "message", Role: "user"}); err != nil {
		t.Errorf("WriteResponseItem on disabled writer: %v", err)
	}
	if err := w.WriteTokenCount(10, 5, 15, 0, 0); err != nil {
		t.Errorf("WriteTokenCount on disabled writer: %v", err)
	}
	if err := w.Flush(context.Background()); err != nil {
		t.Errorf("Flush on disabled writer: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Errorf("Close on disabled writer: %v", err)
	}
	if _, degraded := w.FinalizeCheckpoint(AgentCheckpoint{}); len(degraded) == 0 {
		t.Error("disabled writer 的 FinalizeCheckpoint 必须回报降级理由，不能假装算出了指纹")
	}
}

// R9 的回归：计数器原来被 atomic 与锁两套同步管同一个字段。
// 这条在 -race 下才真正有意义，平时它只验"ID 不重复"。
func TestMessageIDsAreUniqueUnderConcurrency(t *testing.T) {
	isolateHome(t)

	w, err := NewRolloutWriter("coding", "task-queue-ids", "queue-ids-proj")
	if err != nil || !w.Enabled() {
		t.Skipf("rollout writer unavailable: %v", err)
	}
	const perGoroutine = 50
	var mu sync.Mutex
	ids := make(map[string]bool, 8*perGoroutine)

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				id := w.NextMessageID()
				mu.Lock()
				if ids[id] {
					t.Errorf("重复的 message ID %q —— 两套同步管同一个计数器", id)
				}
				ids[id] = true
				mu.Unlock()
				for _, item := range LLMMessageToResponseItems(
					llm.Message{Role: llm.RoleUser, Content: id}, id) {
					if err := w.WriteResponseItem(item); err != nil {
						t.Errorf("write: %v", err)
						return
					}
				}
			}
		}()
	}
	wg.Wait()

	if len(ids) != 8*perGoroutine {
		t.Errorf("拿到 %d 个不同 ID, want %d", len(ids), 8*perGoroutine)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// 这两条基准是给设计稿 §11.3 的 G3（"落盘不在 step 关键路径上"）拿数的：
// Enqueue 是改造后热路径的真实成本，EnqueuePlusFlushEach 近似改造前
// "每条记录一次 write(2)"的行为。差多少就是每条记录从关键路径上省掉多少。
func benchmarkRolloutWriter(b *testing.B) *RolloutWriter {
	b.Helper()
	b.Setenv("HOME", b.TempDir())
	w, err := NewRolloutWriter("bench", "task-bench", "bench-proj")
	if err != nil || !w.Enabled() {
		b.Skipf("rollout writer unavailable: %v", err)
	}
	return w
}

func BenchmarkRolloutEnqueue(b *testing.B) {
	w := benchmarkRolloutWriter(b)
	item := ResponseItem{Type: "message", Role: "assistant"}
	for _, it := range LLMMessageToResponseItems(
		llm.Message{Role: llm.RoleAssistant, Content: strings.Repeat("payload ", 20)}, "msg_bench") {
		item = it
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := w.WriteResponseItem(item); err != nil {
			b.Fatalf("write: %v", err)
		}
	}
	b.StopTimer()
	w.Close()
}

func BenchmarkRolloutEnqueuePlusFlushEach(b *testing.B) {
	w := benchmarkRolloutWriter(b)
	item := ResponseItem{Type: "message", Role: "assistant"}
	for _, it := range LLMMessageToResponseItems(
		llm.Message{Role: llm.RoleAssistant, Content: strings.Repeat("payload ", 20)}, "msg_bench") {
		item = it
	}
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := w.WriteResponseItem(item); err != nil {
			b.Fatalf("write: %v", err)
		}
		if err := w.Flush(ctx); err != nil {
			b.Fatalf("flush: %v", err)
		}
	}
	b.StopTimer()
	w.Close()
}

// 并行委派下多个 goroutine 同时往同一份记忆里追加（Director 的注入跑在工作线程上，
// planner 在主线程上写）。这条在 -race 下才有牙齿：没有锁时它报的是竞态而不是错数。
func TestConversationMemoryConcurrentAppendKeepsEveryMessage(t *testing.T) {
	mem := NewConversationMemory(10000)

	const writers, perWriter = 16, 60
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				mem.Append(ChatMessage{
					Type:      MessageTypeAssistant,
					Content:   fmt.Sprintf("w%d-i%d", w, i),
					Timestamp: time.Now(),
				})
			}
		}(w)
	}
	// 同时有读者：Snapshot 必须是某个完整瞬间，而不是写了一半的底层数组
	stop := make(chan struct{})
	var readers sync.WaitGroup
	for r := 0; r < 4; r++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
					for _, m := range mem.Snapshot() {
						_ = m.Content
					}
				}
			}
		}()
	}
	wg.Wait()
	close(stop)
	readers.Wait()

	if got := mem.Size(); got != writers*perWriter {
		t.Errorf("Size = %d, want %d —— 丢消息就是 /resume 与压缩都少算", got, writers*perWriter)
	}
}

// JSON 往返必须在字段不导出之后仍然成立（{"messages":[…],"max_size":n}）。
func TestConversationMemoryJSONRoundTrip(t *testing.T) {
	mem := NewConversationMemory(42)
	mem.AddSystemMessage("sys")
	mem.AddHumanMessage("human")

	data, err := json.Marshal(mem)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"messages"`) || !strings.Contains(string(data), `"max_size":42`) {
		t.Errorf("线上形态变了: %s", data)
	}

	var back ConversationMemory
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(back.Snapshot()) != 2 || back.Snapshot()[0].Content != "sys" {
		t.Errorf("往返后 = %+v", back.Snapshot())
	}
	if back.MaxSize != 42 {
		t.Errorf("MaxSize = %d, want 42", back.MaxSize)
	}
}
