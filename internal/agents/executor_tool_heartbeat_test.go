package agents

import (
	"context"
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
