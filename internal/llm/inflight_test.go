package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yagent/internal/config"
)

// 在飞闸门（inflight.go）的测试。判据一律取自**假服务端实际同时接到的请求数**，
// 不是 InFlightBusy() 的读数 —— 后者只证明计数器动过，前者才是"provider 那一侧
// 真的少了一截并发"这件事。
//
// "必须串行"与"必须重叠"两臂共用同一个 handler：进来先计数，然后等一个同行者
// （最多 overlapWindow）。闸门放行时第二发进得来，两臂都在几毫秒内结束；闸门把
// 第二发挡住时，第一个 handler 等满窗口才回 —— 于是失败表现成"峰值 = 1"这样一个
// 明确读数，而不是把测试挂住。

const overlapWindow = 150 * time.Millisecond

type overlapTracker struct {
	mu       sync.Mutex
	cur      int
	peak     int
	peer     chan struct{}
	peerSeen bool
}

func (p *overlapTracker) enter() {
	p.mu.Lock()
	p.cur++
	if p.cur > p.peak {
		p.peak = p.cur
	}
	if p.cur >= 2 && !p.peerSeen {
		p.peerSeen = true
		close(p.peer)
	}
	p.mu.Unlock()
}

func (p *overlapTracker) leave() {
	p.mu.Lock()
	p.cur--
	p.mu.Unlock()
}

func (p *overlapTracker) maxInFlight() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.peak
}

// newOverlapServer 按路径分辨协议，好让两个引擎共用一个服务端、一个计数器。
func newOverlapServer(t *testing.T) (*httptest.Server, *overlapTracker) {
	t.Helper()
	tr := &overlapTracker{peer: make(chan struct{})}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		body := okCompletion
		if strings.HasSuffix(r.URL.Path, "/messages") {
			body = okAnthropic
		}
		tr.enter()
		defer tr.leave()
		select {
		case <-tr.peer:
		case <-time.After(overlapWindow):
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, tr
}

// withLimit 把进程级闸门设成 n，用例结束后还原。
func withLimit(t *testing.T, n int) {
	t.Helper()
	prev := InFlightLimit()
	t.Cleanup(func() { SetInFlightLimit(prev) })
	SetInFlightLimit(n)
}

func oneMessage() []Message {
	return []Message{{Role: RoleUser, Content: "hi"}}
}

// 键改了值，测试必须跟着改 —— 这是 AGENTS.md §11 坑 25 对新配置键的承诺。
// 第三臂（0 = 不限）是对照臂：没有它，"峰值 1"可能只是假服务端自己连不上两发。
func TestInFlightLimitCapsRequestsSeenByProvider(t *testing.T) {
	for _, arm := range []struct {
		name     string
		limit    int
		wantPeak int
	}{
		{"上限 1 → 两发排队", 1, 1},
		{"上限 2 → 两发重叠", 2, 2},
		{"0 = 不限 → 两发重叠", 0, 2},
	} {
		t.Run(arm.name, func(t *testing.T) {
			srv, tr := newOverlapServer(t)
			e := openAIEngine(srv.URL, config.LLMConfig{Timeout: 30 * time.Second})
			withLimit(t, arm.limit)

			var wg sync.WaitGroup
			errs := make([]error, 2)
			for i := range errs {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, errs[i] = e.GenerateContent(context.Background(), oneMessage(), nil, nil)
				}()
			}
			wg.Wait()
			for i, err := range errs {
				if err != nil {
					t.Fatalf("call %d: %v", i, err)
				}
			}
			if got := tr.maxInFlight(); got != arm.wantPeak {
				t.Errorf("假服务端见过的峰值并发 = %d, want %d（上限 %d）", got, arm.wantPeak, arm.limit)
			}
			if got := InFlightBusy(); got != 0 {
				t.Errorf("两发都返回之后仍占着 %d 个格子，want 0（释放漏了）", got)
			}
		})
	}
}

// 一个闸门而不是每引擎一个：真实形状是 Director 与 Coding 各走一个 provider、
// 但打在同一个账号上。每引擎各放 n 格的话 provider 看到的是"引擎数 × n"。
// 不限那一臂同样是对照臂 —— 它证明两个引擎本来会重叠，峰值 1 是闸门挡的。
func TestInFlightGateIsSharedByBothEngines(t *testing.T) {
	for _, arm := range []struct {
		name     string
		limit    int
		wantPeak int
	}{
		{"上限 1 → 跨引擎也只有一个在飞", 1, 1},
		{"0 = 不限 → 两个引擎各发一发", 0, 2},
	} {
		t.Run(arm.name, func(t *testing.T) {
			srv, tr := newOverlapServer(t)
			openAI := openAIEngine(srv.URL, config.LLMConfig{Timeout: 30 * time.Second})
			anthropic := anthropicEngine(srv.URL, config.LLMConfig{Timeout: 30 * time.Second})
			withLimit(t, arm.limit)

			var wg sync.WaitGroup
			errs := make([]error, 2)
			wg.Add(2)
			go func() {
				defer wg.Done()
				_, errs[0] = openAI.GenerateContent(context.Background(), oneMessage(), nil, nil)
			}()
			go func() {
				defer wg.Done()
				_, errs[1] = anthropic.GenerateContent(context.Background(), oneMessage(), nil, nil)
			}()
			wg.Wait()
			for i, err := range errs {
				if err != nil {
					t.Fatalf("call %d: %v", i, err)
				}
			}
			if got := tr.maxInFlight(); got != arm.wantPeak {
				t.Errorf("两个引擎合计的峰值并发 = %d, want %d（上限 %d）", got, arm.wantPeak, arm.limit)
			}
		})
	}
}

// heldServer 的 handler 收到请求后不返回，直到测试放行。
type heldServer struct {
	srv       *httptest.Server
	arrived   chan struct{}
	release   chan struct{}
	hits      atomic.Int32
	once      sync.Once
	closeCall func()
}

func newHeldServer(t *testing.T) *heldServer {
	t.Helper()
	h := &heldServer{arrived: make(chan struct{}), release: make(chan struct{})}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		if h.hits.Add(1) == 1 {
			close(h.arrived)
		}
		select {
		case <-h.release:
		case <-time.After(5 * time.Second):
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, okCompletion)
	}))
	h.closeCall = func() { h.once.Do(func() { close(h.release) }) }
	t.Cleanup(func() { h.closeCall(); h.srv.Close() })
	return h
}

func (h *heldServer) releaseNow() { h.closeCall() }

// 排在闸门上的那一发必须响应 ctx，而且响应的是**排队**而不是请求 ——
// 两者的区别就在这条错误文本上：少了 "in-flight"，说明它是发出去之后才被打断的，
// 那 provider 那边已经收到了一次真实请求。
func TestQueuedRequestHonoursContextWithoutTakingSlot(t *testing.T) {
	h := newHeldServer(t)
	e := openAIEngine(h.srv.URL, config.LLMConfig{Timeout: 30 * time.Second})
	withLimit(t, 1)

	firstErr := make(chan error, 1)
	go func() {
		_, err := e.GenerateContent(context.Background(), oneMessage(), nil, nil)
		firstErr <- err
	}()
	select {
	case <-h.arrived:
	case <-time.After(5 * time.Second):
		t.Fatalf("第一发没能到达假服务端")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := e.GenerateContent(ctx, oneMessage(), nil, nil)
	if err == nil {
		t.Fatalf("闸门上没有格子时第二发竟然成功了（服务端共收到 %d 发）", h.hits.Load())
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want errors.Is(context.DeadlineExceeded)", err)
	}
	if !strings.Contains(err.Error(), "in-flight") {
		t.Errorf("err = %v, want 点名是在等在飞格子 —— 否则它其实是发出去之后才被打断的", err)
	}
	if got := h.hits.Load(); got != 1 {
		t.Errorf("服务端收到 %d 发, want 1 —— 排队的那一发不该被发出去", got)
	}
	if got := InFlightBusy(); got != 1 {
		t.Errorf("第二发排队失败后占用格子数 = %d, want 1（只有第一发）", got)
	}

	h.releaseNow()
	if err := <-firstErr; err != nil {
		t.Errorf("第一发返回错误: %v", err)
	}
	if got := InFlightBusy(); got != 0 {
		t.Errorf("全部返回之后占用格子数 = %d, want 0", got)
	}
}

// 运行中换 provider 会重建引擎，而装配点会再调一次 SetInFlightLimit（同一个值）。
// 若每次都新建 sem，那一瞬间在飞的请求就不在旧闸门上了，第二发能直接挤出去。
func TestSetInFlightLimitSameValueDoesNotResetGate(t *testing.T) {
	h := newHeldServer(t)
	e := openAIEngine(h.srv.URL, config.LLMConfig{Timeout: 30 * time.Second})
	withLimit(t, 1)

	firstErr := make(chan error, 1)
	go func() {
		_, err := e.GenerateContent(context.Background(), oneMessage(), nil, nil)
		firstErr <- err
	}()
	select {
	case <-h.arrived:
	case <-time.After(5 * time.Second):
		t.Fatalf("第一发没能到达假服务端")
	}

	SetInFlightLimit(1) // 第二次装配，值没变

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := e.GenerateContent(ctx, oneMessage(), nil, nil); err == nil {
		h.releaseNow()
		<-firstErr
		t.Fatalf("重复设置同一个上限把闸门清空了：服务端共收到 %d 发, want 1", h.hits.Load())
	}

	h.releaseNow()
	if err := <-firstErr; err != nil {
		t.Errorf("第一发返回错误: %v", err)
	}
	if got := h.hits.Load(); got != 1 {
		t.Errorf("服务端收到 %d 发, want 1", got)
	}
}

// 键写了要有地方读它：配置 → 进程级闸门这一条链由这里钉住（LoadConfig 与
// NewClient 两个装配点共用同一句 SetInFlightLimit(cfg.LLM.MaxInFlightRequests)）。
func TestLoadConfigAppliesInFlightLimit(t *testing.T) {
	for _, arm := range []struct {
		name string
		body string
		want int
	}{
		{"写了 3", "max_in_flight_requests = 3", 3},
		{"写了 0 = 不限", "max_in_flight_requests = 0", 0},
		{"没写 → 默认", "", config.DefaultMaxInFlightRequests},
	} {
		t.Run(arm.name, func(t *testing.T) {
			prev := InFlightLimit()
			t.Cleanup(func() { SetInFlightLimit(prev) })

			path := filepath.Join(t.TempDir(), "config.toml")
			text := `
[global.llm]
use_provider = "main"

[global.llm.providers.main]
model = "m-main"
api_base_url = "http://127.0.0.1:1"
api_key = "k"

[llm]
` + arm.body + "\n"
			if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			if _, err := LoadConfig(path); err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
			if got := InFlightLimit(); got != arm.want {
				t.Errorf("配置 %q → InFlightLimit() = %d, want %d", arm.body, got, arm.want)
			}
		})
	}
}
