package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"yagent/internal/config"
)

// 这些断言钉住引擎层曾经的一处静默收窄：timeout 取到全局/局部值之后**无条件**套一层
// WithTimeout，于是调用方给的更长预算（deepthinking 的 [tools.llm.deepthinking].timeout）
// 会被二次砍断，慢速模型永远跑不完一次长分析。

func TestApplyCallTimeout_NoDeadlineGetsConfigured(t *testing.T) {
	ctx, cancel := ApplyCallTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	d, ok := ctx.Deadline()
	if !ok {
		t.Fatal("没有 deadline 的 ctx 必须被套上配置值（否则普通 step 的推理没有上限）")
	}
	if remain := time.Until(d); remain < 29*time.Minute || remain > 31*time.Minute {
		t.Errorf("剩余预算 = %v, want ≈30m", remain)
	}
}

// 引擎层修复的回归保护：调用方预算 30m + 全局默认 10m → 必须是 30m。
func TestApplyCallTimeout_CallerDeadlineWins(t *testing.T) {
	parent, cancelParent := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancelParent()

	ctx, cancel := ApplyCallTimeout(parent, 10*time.Minute)
	defer cancel()

	if ctx != parent {
		t.Error("调用方已有 deadline 时必须原样返回那个 ctx（二次 WithTimeout 就是收窄预算）")
	}
	d, _ := ctx.Deadline()
	if remain := time.Until(d); remain < 29*time.Minute || remain > 31*time.Minute {
		t.Errorf("剩余预算 = %v, want ≈30m（被 10m 收紧即复现原缺陷）", remain)
	}
}

// 更短的调用方 deadline 也不许被拉长：配置值是上限，不是"至少"。
func TestApplyCallTimeout_ShorterCallerDeadlineKept(t *testing.T) {
	parent, cancelParent := context.WithTimeout(context.Background(), time.Second)
	defer cancelParent()

	ctx, cancel := ApplyCallTimeout(parent, time.Hour)
	defer cancel()

	d, _ := ctx.Deadline()
	if remain := time.Until(d); remain > 2*time.Second {
		t.Errorf("剩余预算 = %v, want ≈1s", remain)
	}
}

// configured ≤0 且调用方没给预算 = 不设超时；cancel 仍必须可安全调用（调用方直接 defer）。
func TestApplyCallTimeout_NoBudgetAnywhere(t *testing.T) {
	ctx, cancel := ApplyCallTimeout(context.Background(), 0)
	if _, ok := ctx.Deadline(); ok {
		t.Fatal("configured=0 且无调用方 deadline 时不该有超时")
	}
	cancel() // 不许 panic：返回值恒为可调用的 cancel
	cancel()
}

// 慢一步的假 provider：delay 内被取消就退出，否则 delay 之后回 body。
func slowLLMServer(t *testing.T, delay time.Duration, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// requireNoGlobalTimeout：本组用例的判据是"局部配置不得收紧调用方 deadline"，
// 而全局值优先级高于局部配置；被别的用例污染成非 0 时判据就不成立，宁可当场报出来。
func requireNoGlobalTimeout(t *testing.T) {
	t.Helper()
	if g := config.GlobalLLMTimeout(); g > 0 {
		t.Fatalf("全局 LLM 超时 = %v（被其它用例设置过）：本用例无法证明调用方 deadline 优先", g)
	}
}

// 服务端 200ms 才回，局部配置 40ms，调用方给 5s：
// 修复前请求会在 40ms 处被砍断（err != nil），修复后必须拿到成功响应。
func TestOpenAIEngine_CallerDeadlineBeatsConfiguredTimeout(t *testing.T) {
	requireNoGlobalTimeout(t)
	const serverDelay = 200 * time.Millisecond
	srv := slowLLMServer(t, serverDelay, okCompletion)
	e := openAIEngine(srv.URL, config.LLMConfig{Timeout: 40 * time.Millisecond})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	resp, err := e.GenerateContent(ctx, []Message{{Role: RoleUser, Content: "x"}}, nil, nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("GenerateContent: %v（被 40ms 的配置超时砍断 = 引擎没尊重调用方 5s 预算）", err)
	}
	if elapsed < serverDelay {
		t.Errorf("耗时 %v < 服务端延迟 %v：响应来源可疑", elapsed, serverDelay)
	}
	if resp.Choices[0].Content != "hello" {
		t.Errorf("content = %q, want hello", resp.Choices[0].Content)
	}
}

func TestAnthropicEngine_CallerDeadlineBeatsConfiguredTimeout(t *testing.T) {
	requireNoGlobalTimeout(t)
	const serverDelay = 200 * time.Millisecond
	srv := slowLLMServer(t, serverDelay, okAnthropic)
	e := anthropicEngine(srv.URL, config.LLMConfig{Timeout: 40 * time.Millisecond})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	resp, err := e.GenerateContent(ctx, []Message{{Role: RoleUser, Content: "x"}}, nil, nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("GenerateContent: %v（被 40ms 的配置超时砍断 = 引擎没尊重调用方 5s 预算）", err)
	}
	if elapsed < serverDelay {
		t.Errorf("耗时 %v < 服务端延迟 %v：响应来源可疑", elapsed, serverDelay)
	}
	if resp.Choices[0].Content != "hello" {
		t.Errorf("content = %q, want hello", resp.Choices[0].Content)
	}
}

// 反方向的一半：调用方给 20ms、配置 1h，请求必须立刻以 DeadlineExceeded 结束，
// 而不是干等那一小时。两个引擎各一条，防止"尊重调用方"被写成"忽略调用方"。
// 服务端延迟取 300ms（= 调用方预算的 15 倍）：修复缺席时这条会等满它，断言 2s 上限即红。
func TestEngines_RespectShortCallerDeadline(t *testing.T) {
	requireNoGlobalTimeout(t)
	const configured = time.Hour
	srv := slowLLMServer(t, 300*time.Millisecond, okCompletion)

	for _, arm := range []struct {
		name   string
		engine Engine
	}{
		{"openai", openAIEngine(srv.URL, config.LLMConfig{Timeout: configured})},
		{"anthropic", anthropicEngine(srv.URL, config.LLMConfig{Timeout: configured})},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		start := time.Now()
		_, err := arm.engine.GenerateContent(ctx, []Message{{Role: RoleUser, Content: "x"}}, nil, nil)
		elapsed := time.Since(start)
		cancel()

		if err == nil {
			t.Fatalf("%s: 调用方 deadline 过期却返回成功", arm.name)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("%s: err = %v, want 带 context.DeadlineExceeded", arm.name, err)
		}
		if elapsed > 2*time.Second {
			t.Errorf("%s: 耗时 %v，want 立刻返回（被配置的 1h 拖住即复现原缺陷）", arm.name, elapsed)
		}
	}
}
