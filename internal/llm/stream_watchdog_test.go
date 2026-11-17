package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yagent/internal/config"
)

// 这组测试钉住"流式读循环不会永久阻塞"这件事本身。
//
// 判据取自三处，缺一条都可能是假通过：
//  1. 错误里有 `stream stalled`（否则上层看到的又是一句 context canceled）；
//  2. errors.Is(err, ErrStreamStalled)（否则分不出"静默"与"用户取消"）；
//  3. 假服务端**自己**观察到请求 ctx 结束（否则只是客户端放弃读，连接还挂在那里）。
//
// 阈值一律在用例里设成几百毫秒 —— 不许出现真的等 90s 的测试。

const (
	// stallIdle 是测试用空闲阈值；远小于生产默认 90s，也远大于本机调度抖动。
	stallIdle = 200 * time.Millisecond
	// stallBudget 是总预算：它必须比阈值大得多，否则"预算先到"会掩盖看门狗没生效。
	stallBudget = 20 * time.Second
)

// silentAfterSSE 返回一个"先吐 prefix 个 SSE 事件，然后永久静默"的假服务端，
// 并记录自己是否观察到请求 ctx 结束（handler 返回即代表客户端断开了）。
func silentAfterSSE(t *testing.T, body string) (*httptest.Server, *atomic.Bool) {
	t.Helper()
	var clientGone atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("假服务端不支持 Flush，流式断言无法成立")
			return
		}
		_, _ = io.WriteString(w, body)
		flusher.Flush()

		// 静默：不再写一个字节，只看客户端会不会主动走开。
		select {
		case <-r.Context().Done():
			clientGone.Store(true)
		case <-time.After(10 * time.Second):
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &clientGone
}

func collectStream() (*strings.Builder, StreamHandler) {
	var b strings.Builder
	return &b, func(_ context.Context, chunk []byte) error {
		b.Write(chunk)
		return nil
	}
}

// 核心用例：OpenAI 协议下"3 个 chunk 之后永久静默"必须在阈值附近被判死。
func TestOpenAIEngine_StreamIdleWatchdogStalls(t *testing.T) {
	srv, clientGone := silentAfterSSE(t, sseChunks(t,
		`{"choices":[{"index":0,"delta":{"content":"He"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"llo"}}]}`,
		`{"choices":[{"index":0,"delta":{"reasoning_content":"想"}}]}`,
	))

	e := openAIEngine(srv.URL, config.LLMConfig{StreamIdleTimeout: stallIdle})
	streamed, handler := collectStream()

	ctx, cancel := context.WithTimeout(context.Background(), stallBudget)
	defer cancel()

	start := time.Now()
	_, err := e.GenerateContent(ctx, []Message{{Role: RoleUser, Content: "x"}}, nil,
		&CallOptions{StreamHandler: handler})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("静默的流必须报错，而不是让调用方一直等")
	}
	if !strings.Contains(err.Error(), "stream stalled") {
		t.Errorf("错误文本 = %q, want 含 stream stalled（否则用户看到的只是一句 context canceled）", err)
	}
	if !errors.Is(err, ErrStreamStalled) {
		t.Errorf("errors.Is(err, ErrStreamStalled) = false；上层因此分不出静默与用户取消：%v", err)
	}
	if !strings.Contains(err.Error(), stallIdle.String()) {
		t.Errorf("错误文本 = %q, want 含阈值 %s（日志里要能对上配置值）", err, stallIdle)
	}
	// 阈值附近判死：等满 20s 预算就是没修好，秒回则说明根本没等看门狗。
	if elapsed < stallIdle/2 {
		t.Errorf("只用了 %v 就失败：看门狗不该这么快触发（阈值 %s）", elapsed, stallIdle)
	}
	if elapsed > 5*time.Second {
		t.Errorf("用了 %v 才判死，want ≈%s（说明仍在等总预算）", elapsed, stallIdle)
	}
	if got := streamed.String(); got != "Hello" {
		t.Errorf("handler 收到的增量 = %q, want Hello（判死前的内容不该丢）", got)
	}

	// 请求必须真的被中止，而不是客户端单方面放弃读。
	deadline := time.Now().Add(3 * time.Second)
	for !clientGone.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !clientGone.Load() {
		t.Error("假服务端没有观察到请求 ctx 结束：看门狗没有真正中止这次请求")
	}
}

// Anthropic 协议一侧同一条判据（SSE 是自研解析，不能只测 SDK 那条路）。
func TestAnthropicEngine_StreamIdleWatchdogStalls(t *testing.T) {
	prefix := strings.Join([]string{
		"event: message_start",
		`data: {"type":"message_start","message":{"usage":{"input_tokens":7}}}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"He"}}`,
		"",
		"event: ping",
		`data: {"type":"ping"}`,
		"",
	}, "\n") + "\n"

	srv, clientGone := silentAfterSSE(t, prefix)

	e := anthropicEngine(srv.URL, config.LLMConfig{StreamIdleTimeout: stallIdle})
	streamed, handler := collectStream()

	ctx, cancel := context.WithTimeout(context.Background(), stallBudget)
	defer cancel()

	start := time.Now()
	_, err := e.GenerateContent(ctx, []Message{{Role: RoleUser, Content: "x"}}, nil,
		&CallOptions{StreamHandler: handler})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("静默的流必须报错")
	}
	if !strings.Contains(err.Error(), "stream stalled") || !errors.Is(err, ErrStreamStalled) {
		t.Errorf("错误 = %v, want 含 stream stalled 且 errors.Is(ErrStreamStalled)", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("用了 %v 才判死，want ≈%s", elapsed, stallIdle)
	}
	if got := streamed.String(); got != "He" {
		t.Errorf("handler 收到的增量 = %q, want He", got)
	}

	deadline := time.Now().Add(3 * time.Second)
	for !clientGone.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !clientGone.Load() {
		t.Error("假服务端没有观察到请求 ctx 结束")
	}
}

// 反向的一半：只要还有数据到，看门狗就必须一直续期。
// 总时长（≈360ms）刻意超过阈值（200ms），单次间隔（60ms）刻意小于阈值。
func TestOpenAIEngine_StreamIdleWatchdogKeepsAlive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		for i := 0; i < 6; i++ {
			_, _ = io.WriteString(w, "data: "+`{"choices":[{"index":0,"delta":{"content":"x"}}]}`+"\n\n")
			flusher.Flush()
			time.Sleep(60 * time.Millisecond)
		}
		_, _ = io.WriteString(w, "data: "+`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	e := openAIEngine(srv.URL, config.LLMConfig{StreamIdleTimeout: stallIdle})
	ctx, cancel := context.WithTimeout(context.Background(), stallBudget)
	defer cancel()

	start := time.Now()
	resp, err := e.GenerateContent(ctx, []Message{{Role: RoleUser, Content: "x"}}, nil,
		&CallOptions{StreamHandler: func(context.Context, []byte) error { return nil }})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("持续吐数据的流被误杀：%v（这就是把阈值判成累计时长的表现）", err)
	}
	if elapsed <= stallIdle {
		t.Fatalf("只用了 %v，测试没有跑超过阈值（%s），无法证明续期生效", elapsed, stallIdle)
	}
	if got := resp.Choices[0].Content; got != "xxxxxx" {
		t.Errorf("聚合 content = %q, want xxxxxx", got)
	}
}

// 流式失败 → 非流式回退这一跳必须对用户可见（旧行为：UI 上只是"卡住"，然后超时）。
func TestOpenAIEngine_StreamFallbackIsVisibleToHandler(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		if streaming, _ := body["stream"].(bool); streaming {
			// 流式这一发直接 500：isRetriableError → 回退非流式。
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"message":"boom","type":"server_error"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, okCompletion)
	}))
	defer srv.Close()

	e := openAIEngine(srv.URL, config.LLMConfig{StreamIdleTimeout: stallIdle})
	streamed, handler := collectStream()

	ctx, cancel := context.WithTimeout(context.Background(), stallBudget)
	defer cancel()

	resp, err := e.GenerateContent(ctx, []Message{{Role: RoleUser, Content: "x"}}, nil,
		&CallOptions{StreamHandler: handler})
	if err != nil {
		t.Fatalf("回退非流式应当成功: %v", err)
	}
	if got := resp.Choices[0].Content; got != "hello" {
		t.Errorf("content = %q, want hello（回退后拿到的答案）", got)
	}
	if !strings.Contains(streamed.String(), "已回退到非流式") {
		t.Errorf("handler 收到的内容 = %q，want 含回退提示（否则用户看到的是无声降级）", streamed.String())
	}
	if hits.Load() != 2 {
		t.Errorf("服务端收到 %d 发请求, want 2（1 次流式失败 + 1 次非流式成功）", hits.Load())
	}
}

// 重试也必须可见：退避从 10s 起跳，那段时间没有任何别的信号。
// 这里用极小的总预算把退避钳到 0，避免测试真的 sleep 10s（AGENTS.md 的耗时规矩）。
func TestOpenAIEngine_RetryNoticeIsVisibleToHandler(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"message":"boom","type":"server_error"}}`)
	}))
	defer srv.Close()

	e := openAIEngine(srv.URL, config.LLMConfig{StreamIdleTimeout: stallIdle, MaxRetries: 1})
	streamed, handler := collectStream()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	if _, err := e.GenerateContent(ctx, []Message{{Role: RoleUser, Content: "x"}}, nil,
		&CallOptions{StreamHandler: handler}); err == nil {
		t.Fatal("全 500 的服务端不该返回成功")
	}
	if !strings.Contains(streamed.String(), "[重试 1/1]") {
		t.Errorf("handler 收到的内容 = %q，want 含 [重试 1/1]（否则重试全程不可见）", streamed.String())
	}
}

// 首字节（ResponseHeaderTimeout）只能落在流式那份 transport 上。
//
// 这一条钉的是一个真实会流血的取舍：非流式响应要**等整轮生成完才开始写响应头**，
// 把同一个 60s 套到非流式上，等于给"整轮生成"设了 60s 上限 —— 一条正常跑几分钟的
// 调用会在 60s 被杀，而错误里说的是"等响应头超时"，方向都指错。
func TestOpenAIEngine_FirstByteTimeoutOnlyAppliesToStreaming(t *testing.T) {
	const headerDelay = 400 * time.Millisecond
	const firstByte = 150 * time.Millisecond

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		streaming, _ := body["stream"].(bool)

		// 无论流式与否，都晚 headerDelay 才写响应头（掩盖住"非流式更快"的差别）。
		time.Sleep(headerDelay)

		w.Header().Set("Content-Type", "application/json")
		if streaming {
			w.Header().Set("Content-Type", "text/event-stream")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, okCompletion)
	}))
	defer srv.Close()

	t.Run("流式：首字节超时先到，回退非流式后成功", func(t *testing.T) {
		e := openAIEngine(srv.URL, config.LLMConfig{
			StreamIdleTimeout: stallIdle,
			FirstByteTimeout:  firstByte,
		})
		streamed, handler := collectStream()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		start := time.Now()
		resp, err := e.GenerateContent(ctx, []Message{{Role: RoleUser, Content: "x"}}, nil,
			&CallOptions{StreamHandler: handler})
		elapsed := time.Since(start)

		if err != nil {
			t.Fatalf("首字节超时后应当回退非流式并成功: %v", err)
		}
		if resp.Choices[0].Content != "hello" {
			t.Errorf("content = %q, want hello", resp.Choices[0].Content)
		}
		if !strings.Contains(streamed.String(), "已回退到非流式") {
			t.Errorf("handler 收到的 = %q，want 含回退提示", streamed.String())
		}
		// 若流式那份没有首字节超时，第一次尝试会等满 headerDelay 才拿到头（不可能
		// 回退），总耗时就会 ≥ headerDelay×2。
		if elapsed >= 2*headerDelay {
			t.Errorf("用了 %v：流式尝试没有被首字节超时砍断（want ≈%s 后回退）", elapsed, firstByte)
		}
	})

	t.Run("非流式：同样晚到 400ms 的响应头必须照常成功", func(t *testing.T) {
		e := openAIEngine(srv.URL, config.LLMConfig{FirstByteTimeout: firstByte})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		resp, err := e.GenerateContent(ctx, []Message{{Role: RoleUser, Content: "x"}}, nil, nil)
		if err != nil {
			t.Fatalf("非流式请求被首字节超时误杀（把 ResponseHeaderTimeout 套到非流式上就是这个表现）: %v", err)
		}
		if resp.Choices[0].Content != "hello" {
			t.Errorf("content = %q, want hello", resp.Choices[0].Content)
		}
	})
}

// 看门狗自身的生命周期：续期、release、以及 release 之后不再复活。
func TestIdleWatchdogLifecycle(t *testing.T) {
	t.Run("release 取消 ctx 且不留计时器", func(t *testing.T) {
		ctx, wd := newIdleWatchdog(context.Background(), stallIdle)
		if wd == nil {
			t.Fatal("阈值 > 0 时必须建看门狗（看门狗被关掉就是原缺陷）")
		}
		wd.touch()
		wd.release()

		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
			t.Error("release 之后 ctx 必须已取消（否则请求/连接可能一直挂着）")
		}
		if err := wd.err(); err != nil {
			t.Errorf("正常 release 不该产出 stall 错误: %v", err)
		}
		wd.mu.Lock()
		leaked := wd.timer != nil
		wd.mu.Unlock()
		if leaked {
			t.Error("release 没有清掉计时器引用（Stop 路径没走完）")
		}

		// release 之后再 touch 不许把已停的计时器复活。
		wd.touch()
		time.Sleep(3 * stallIdle)
		if err := wd.err(); err != nil {
			t.Errorf("release 之后看门狗又 fire 了: %v", err)
		}
	})

	t.Run("阈值 <= 0 时是 nil 看门狗且所有方法空操作", func(t *testing.T) {
		parent := context.Background()
		ctx, wd := newIdleWatchdog(parent, 0)
		if wd != nil {
			t.Fatal("阈值 0 时不该建看门狗")
		}
		if ctx != parent {
			t.Error("没有看门狗时必须原样返回父 ctx")
		}
		wd.touch()
		if err := wd.err(); err != nil {
			t.Errorf("nil 看门狗的 err() 必须是 nil: %v", err)
		}
		wd.release()
	})

	t.Run("静默到点会 fire 且只报一次", func(t *testing.T) {
		ctx, wd := newIdleWatchdog(context.Background(), 50*time.Millisecond)
		defer wd.release()

		select {
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
			t.Fatal("静默超时后 ctx 必须被取消")
		}
		err := wd.err()
		if !errors.Is(err, ErrStreamStalled) {
			t.Fatalf("err() = %v, want ErrStreamStalled", err)
		}
		// 不包裹 context.Canceled：否则"用户取消"与"网络静默"会被混成同一类。
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("stall 错误不该暴露 context.Canceled/DeadlineExceeded 语义: %v", err)
		}
	})
}

// withAttemptTimeout：配置值是上限，不是"至少"；0 = 不额外限制。
func TestWithAttemptTimeout(t *testing.T) {
	t.Run("0 = 不额外限制", func(t *testing.T) {
		parent := context.Background()
		ctx, cancel := withAttemptTimeout(parent, 0)
		defer cancel()
		if ctx != parent {
			t.Error("limit=0 时必须原样返回父 ctx")
		}
	})

	t.Run("已有更紧的预算时原样返回", func(t *testing.T) {
		parent, cancelParent := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancelParent()
		ctx, cancel := withAttemptTimeout(parent, time.Minute)
		defer cancel()
		if ctx != parent {
			t.Error("父 ctx 已有更紧 deadline 时必须原样返回（否则等于放宽预算）")
		}
	})

	t.Run("配置值更紧时套一层", func(t *testing.T) {
		parent, cancelParent := context.WithTimeout(context.Background(), time.Hour)
		defer cancelParent()
		ctx, cancel := withAttemptTimeout(parent, 40*time.Millisecond)
		defer cancel()
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("limit > 0 且父预算更宽时必须有自己的 deadline")
		}
		if remain := time.Until(deadline); remain > 39*time.Millisecond+10*time.Millisecond {
			t.Errorf("剩余 %v, want ≈40ms", remain)
		}
	})
}

// 提示文案只在流式通道里出现，且 handler 的错误不能中断流程。
func TestNotifyStreamNeverFailsTheRequest(t *testing.T) {
	var mu sync.Mutex
	var got []string
	handler := func(_ context.Context, chunk []byte) error {
		mu.Lock()
		got = append(got, string(chunk))
		mu.Unlock()
		return errors.New("handler 拒绝")
	}
	notifyStream(context.Background(), handler, "hi")
	notifyStream(context.Background(), nil, "nobody")
	notifyStream(context.Background(), handler, "")

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0] != "hi" {
		t.Errorf("提示只该在有 handler 且文本非空时送一次，got %v", got)
	}
}
