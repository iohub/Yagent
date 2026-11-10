package agents

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"yagent/internal/agents/activity"
	"yagent/internal/llm"
	"yagent/internal/supervise"
	"yagent/internal/tools"
)

// restoreToolHeartbeatInterval 把工具执行期心跳间隔临时调短，让"期间有没有心跳"
// 在一秒内可观测（生产值 30s）。恢复交给 t.Cleanup，与 restoreToolTimeout 同风格。
func restoreToolHeartbeatInterval(t *testing.T, d time.Duration) {
	t.Helper()
	old := toolHeartbeatInterval
	toolHeartbeatInterval = d
	t.Cleanup(func() { toolHeartbeatInterval = old })
}

// 一次认知类工具调用（deepthinking 的预算常见 10-30 分钟）期间不产生任何 step，
// 而 delegate 的空闲看门狗只看"有没有心跳"。这条用例钉住工具执行期的心跳：
// 去掉 startToolHeartbeat 后，下面的工具会在 idle 阈值处被取消，err 变为非 nil。
func TestRunToolCallKeepsIdleWatchdogAlive(t *testing.T) {
	restoreToolHeartbeatInterval(t, 10*time.Millisecond)

	const (
		idleTimeout  = 300 * time.Millisecond
		toolDuration = 900 * time.Millisecond
	)

	// 假工具：只看墙钟与自己的 ctx。没被取消说明 idle 一直被续上。
	slow := tools.NewAdapter("deepthinking", "slow cognitive tool",
		func(ctx context.Context, _ map[string]interface{}) (interface{}, error) {
			select {
			case <-time.After(toolDuration):
				return "analysis", nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		})

	grp, base := supervise.New(context.Background(), "test/tool-heartbeat")
	ctx, cancel := activity.WithIdleTimeout(base, activity.Options{
		IdleTimeout:   idleTimeout,
		CheckInterval: 10 * time.Millisecond,
	})

	cfg := &ExecutorConfig{
		Surface:   tools.NewSurfaceFromAdapters("test-agent", slow),
		AgentName: "heartbeat-test",
	}
	call := llm.ToolCall{
		ID:       "call_heartbeat",
		Type:     "function",
		Function: llm.FunctionCall{Name: "deepthinking", Arguments: "{}"},
	}

	start := time.Now()
	res, found, err := runToolCall(ctx, cfg, call)
	elapsed := time.Since(start)

	if !found {
		t.Fatal("工具没被 Surface 解析出来，用例没跑到被测分支")
	}
	if err != nil {
		t.Fatalf("工具执行 %v 后被空闲看门狗打断：%v（工具其实只跑了 %v，心跳没续上 idle）",
			elapsed, err, toolDuration)
	}
	if elapsed < toolDuration {
		t.Errorf("耗时 %v < %v：工具被提前放行，断言失去意义", elapsed, toolDuration)
	}
	if !strings.Contains(res, "analysis") {
		t.Errorf("res = %q, want 含 analysis", res)
	}

	// 心跳 goroutine 由 runToolCall 自己等退出；再 Drain 一次作为泄漏证据
	// （先 cancel 才能让 activity 的监视器一起退出）。
	cancel()
	if leaked := grp.Drain(time.Second); len(leaked) > 0 {
		t.Errorf("残留 goroutine：%v（工具返回后心跳必须已经退出）", supervise.LeaseNames(leaked))
	}
}

// 间隔 <= 0 是合法的"关闭心跳"语义（不是配置错误）：守卫在起 goroutine 之前就返回 noop，
// 于是既没有 ticker、也没有心跳，工具该被 idle 看门狗取消的时候照旧被取消。
//
// 反证（去掉守卫）：time.NewTicker(0) 在 supervise 起的 goroutine 里 panic，被 recover 成一条
// ERR 计入 Group.Failures —— 表现与"压根没配心跳"逐字相同，唯一的证据就是那条日志。
// 所以断言分三层：没有心跳 lease、Failures 为空、调用照常返回（noop stop 可调用、不卡住）。
func TestRunToolCallWithHeartbeatDisabledIsNotKeptAlive(t *testing.T) {
	restoreToolHeartbeatInterval(t, 0)

	const idleTimeout = 120 * time.Millisecond

	started := make(chan struct{})
	// 只靠 ctx 退出：心跳关掉后它必然在 idle 阈值处被取消；若仍被保活则会一直卡着。
	blocking := tools.NewAdapter("deepthinking", "blocking cognitive tool",
		func(ctx context.Context, _ map[string]interface{}) (interface{}, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		})

	grp, base := supervise.New(context.Background(), "test/tool-heartbeat-off")
	ctx, cancel := activity.WithIdleTimeout(base, activity.Options{
		IdleTimeout:   idleTimeout,
		CheckInterval: 5 * time.Millisecond,
	})
	defer cancel()

	cfg := &ExecutorConfig{
		Surface:   tools.NewSurfaceFromAdapters("test-agent", blocking),
		AgentName: "heartbeat-off-test",
	}
	call := llm.ToolCall{
		ID:       "call_no_heartbeat",
		Type:     "function",
		Function: llm.FunctionCall{Name: "deepthinking", Arguments: "{}"},
	}

	type result struct {
		res   string
		found bool
		err   error
	}
	done := make(chan result, 1)
	go func() {
		res, found, err := runToolCall(ctx, cfg, call)
		done <- result{res: res, found: found, err: err}
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("工具没被调用，用例没跑到被测分支")
	}

	// 第一层证据：此刻域里没有任何心跳 lease（activity 的监视器是唯一合法的常驻成员，
	// 所以只按名字排除心跳，不许断言"整个域为空"）。
	for _, lease := range grp.Pending() {
		if strings.HasPrefix(lease.Name, "agents/tool-heartbeat") {
			t.Fatalf("间隔 0 时不该起心跳 goroutine，此刻还在跑：%s", lease)
		}
	}

	var got result
	select {
	case got = <-done:
	case <-time.After(5 * time.Second):
		// 守卫若写成"返回 nil stop"，runToolCall 的 defer 会在调用它时 panic；写成"返回一个
		// 永不返回的 stop"则卡死在这里 —— 两种坏法都被这条超时兜住。
		t.Fatal("runToolCall 没返回：间隔 0 的 noop stop 必须可调用且立即返回")
	}

	if !got.found {
		t.Fatal("工具没被 Surface 解析出来，用例没跑到被测分支")
	}
	if got.err == nil {
		t.Fatalf("心跳关闭后工具仍被保活：err=nil, res=%q（空闲看门狗本该在 %v 处取消它）",
			got.res, idleTimeout)
	}
	var idleErr *activity.IdleTimeoutError
	if cause := context.Cause(ctx); !errors.As(cause, &idleErr) {
		t.Errorf("ctx 取消原因 = %v, want *activity.IdleTimeoutError（被保活会是别的原因或永不取消）", cause)
	}
	// 第二层证据：没有 panic 被 supervise 悄悄 recover 掉。
	if fails := grp.Failures(); len(fails) != 0 {
		t.Errorf("出现了被 recover 的 panic：%v（间隔 0 必须在起 goroutine 之前返回 noop）", fails)
	}

	cancel()
	if leaked := grp.Drain(time.Second); len(leaked) > 0 {
		t.Errorf("残留 goroutine：%v", supervise.LeaseNames(leaked))
	}
}
