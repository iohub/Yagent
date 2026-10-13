package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

// chunkedWriter 模拟管道写端的真实行为：单次 Write 可能只被接受一部分，
// 剩下的要等下一次调用。真实 os.File.Write 在管道缓冲区不足时正是这样。
// 每个 chunk 内部加锁（对应内核单次 write 的原子性），chunk 之间不加锁——
// 于是"整条消息是否原子"就完全取决于 sendJSON 自己有没有串行化。
type chunkedWriter struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	chunk int
}

func (w *chunkedWriter) Write(p []byte) (int, error) {
	written := 0
	for written < len(p) {
		n := min(w.chunk, len(p)-written)
		w.mu.Lock()
		w.buf.Write(p[written : written+n])
		w.mu.Unlock()
		written += n
		// 让出调度窗口，制造并发写交错的机会
		time.Sleep(50 * time.Microsecond)
	}
	return written, nil
}

func (w *chunkedWriter) Close() error { return nil }

func (w *chunkedWriter) Bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.buf.Bytes()...)
}

// 并发只读工具（semantic_search / find_function_* 等）会同时往同一条 stdin 管道
// 写请求。单次 write 只有在 ≤PIPE_BUF 时才由 POSIX 保证原子，大请求并发时会
// 交错成非法 JSON-RPC，readLoop 只能报 "invalid JSON" 并把两个在飞请求一起拖到超时。
func TestSendJSON_ConcurrentWritesStayAtomic(t *testing.T) {
	const (
		writers = 8
		payload = 8192 // > PIPE_BUF(4096)，一条消息必然跨多次底层 write
	)

	w := &chunkedWriter{chunk: 512}
	c := NewMCPClient(MCPClientConfig{})
	c.stdin = w

	var wg sync.WaitGroup
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func(i int) {
			defer wg.Done()
			if err := c.sendJSON(map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      i,
				"method":  "tools/call",
				"params":  strings.Repeat("x", payload),
			}); err != nil {
				t.Errorf("sendJSON(%d): %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	scanner := bufio.NewScanner(bytes.NewReader(w.Bytes()))
	// 缓冲区要给足：写锁失效时多条消息会粘成一行，若撞上 token-too-long
	// 就看不到"JSON 非法"这个真正的诊断了。
	scanner.Buffer(make([]byte, payload*2), 1<<20)

	seen := map[int]bool{}
	lines := 0
	for scanner.Scan() {
		lines++
		var msg struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
			Params string `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			t.Fatalf("line %d is not valid JSON — writes interleaved: %v", lines, err)
		}
		if msg.Method != "tools/call" {
			t.Fatalf("line %d method = %q, want tools/call", lines, msg.Method)
		}
		if len(msg.Params) != payload {
			t.Fatalf("line %d params length = %d, want %d — message corrupted", lines, len(msg.Params), payload)
		}
		if seen[msg.ID] {
			t.Fatalf("duplicate id %d", msg.ID)
		}
		seen[msg.ID] = true
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanner: %v", err)
	}
	if lines != writers {
		t.Errorf("got %d complete messages, want %d", lines, writers)
	}
}

// 响应侧已经按 id 多路复用（doRequest 登记 requests[id]，readLoop 依 resp.ID 派发），
// 所以并发只需要保证 nextID 不重号。
func TestNextID_UniqueUnderConcurrency(t *testing.T) {
	c := NewMCPClient(MCPClientConfig{})

	const n = 64
	ids := make(chan int, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			ids <- int(c.nextID.Add(1))
		}()
	}
	wg.Wait()
	close(ids)

	seen := map[int]bool{}
	for id := range ids {
		if seen[id] {
			t.Fatalf("nextID handed out duplicate id %d", id)
		}
		seen[id] = true
	}
	if len(seen) != n {
		t.Errorf("got %d unique ids, want %d", len(seen), n)
	}
}
