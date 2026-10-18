package agents

// 委派并发（C6-c，docs/structured-concurrency-design.md §8.2）：
// Director 一条回复里发多个 delegate_* 时，同时跑几个子 Agent 由
// [agent] max_parallel_delegations 决定，YAGENT_CONCURRENCY=0 一键退回串行。
// 池子建在**这一轮的 toolRunner** 上，所以嵌套委派各用各的池 ——
// "父占一格、子在同一格里排队"这种死锁形状从结构上就不存在。

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"yagent/internal/agents/director"
	"yagent/internal/config"
	"yagent/internal/llm"
	"yagent/internal/memory"
	"yagent/internal/tools"
)

// 委派名用 delegate_alpha/beta/gamma：真实的 delegate_* 在注册表里各有各的
// 能力标签，而这里要测的是**池子**，不是准入 —— 前缀才是这一层认的东西。
var delegateNames = []string{"delegate_alpha", "delegate_beta", "delegate_gamma"}

// directorDelegateRun 用真实 planner + directorToolRunner 跑一轮：一条回复里发
// n 个 delegate_*，工具体各睡 delay。返回并发峰值、实际执行次数与整轮墙钟。
func directorDelegateRun(t *testing.T, n, delegationCap int, delay time.Duration, honorCtx bool) (peak int64, executions int64, elapsed time.Duration) {
	t.Helper()

	var cur, maxCur, ran atomic.Int64
	mkTool := func(name string) *tools.Adapter {
		return tools.NewAdapter(name, "mock delegate", func(ctx context.Context, _ map[string]interface{}) (interface{}, error) {
			ran.Add(1)
			c := cur.Add(1)
			for {
				old := maxCur.Load()
				if c <= old || maxCur.CompareAndSwap(old, c) {
					break
				}
			}
			defer cur.Add(-1)
			if honorCtx {
				select {
				case <-time.After(delay):
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			} else {
				// 不响应取消的子 Agent：真实形状是"卡在一次性进程调用里"。
				// 只有它占着格子，才看得出排在后面的那一发到底有没有替 ctx 让路。
				time.Sleep(delay)
			}
			return "ok:" + name, nil
		}).WithReadOnly(true)
	}

	adapters := make([]*tools.Adapter, 0, n)
	calls := make([]llm.ToolCall, 0, n)
	for i := 0; i < n; i++ {
		adapters = append(adapters, mkTool(delegateNames[i]))
		calls = append(calls, llm.ToolCall{
			ID:       "call_del_" + delegateNames[i],
			Type:     "function",
			Function: llm.FunctionCall{Name: delegateNames[i], Arguments: `{}`},
		})
	}

	agent := &DirectorAgent{
		surface:                  tools.NewSurfaceFromAdapters("test-agent", adapters...),
		maxParallelReadOnlyTools: NormalizeMaxParallelReadOnlyTools(4),
		maxParallelDelegations:   delegationCap,
		delegateIdleTimeout:      10 * time.Second,
	}
	state := &director.RunState{MaxSteps: 4}
	toolRunner := newDirectorToolRunner(agent, state)

	mem := memory.NewConversationMemory(128)
	mem.AddHumanMessage("task input")

	cfg := director.PlannerConfig{
		LLM: &mockLLM{responses: []*llm.Response{
			{Choices: []llm.Choice{{ToolCalls: calls}}},
			{Choices: []llm.Choice{{Content: "done"}}},
		}},
		Publisher:                &spyPublisher{},
		Tools:                    toolRunner,
		Prompts:                  func(_ context.Context, _ director.PromptInput) (string, error) { return "sys", nil },
		MaxSteps:                 4,
		LLMTimeout:               time.Minute,
		ToolTimeout:              30 * time.Second,
		MaxParallelReadOnlyTools: agent.maxParallelReadOnlyTools,
		IsParallelizableTool:     agent.isToolParallelizable,
	}

	start := time.Now()
	res, err := director.NewPlanner(cfg, state).Run(context.Background(), director.PlanInput{
		Input: "task input",
		Mem:   mem,
	})
	elapsed = time.Since(start)
	if err != nil {
		t.Fatalf("Planner.Run: %v", err)
	}
	if res.StopReason != "plain_text" {
		t.Fatalf("StopReason = %q, want plain_text", res.StopReason)
	}
	// 配对不变量：每个 tool_call 都要有一条 tool 结果，少一条就是 provider 400。
	if got := len(toolResults(mem)); got != n {
		t.Fatalf("tool 结果数 = %d, want %d —— 缺一条就是配对破了", got, n)
	}
	return maxCur.Load(), ran.Load(), elapsed
}

func toolResults(mem *memory.ConversationMemory) []string {
	var out []string
	for _, m := range mem.GetMessages() {
		if m.Type == memory.MessageTypeTool {
			out = append(out, m.Content)
		}
	}
	return out
}

// 三臂：上限 1 → 峰值 1 且墙钟 ≥N×delay；上限 2 → 峰值恰为 2；上限 4 而只有
// 3 发 → 峰值 3。后两臂是对照臂 —— 没有它们，"峰值 1"可能只是假工具自己起不来。
func TestDelegatePoolCapsConcurrentDelegations(t *testing.T) {
	const delay = 80 * time.Millisecond

	for _, arm := range []struct {
		name       string
		n          int
		cap        int
		wantPeak   int64
		wantSerial bool
	}{
		{"上限 1 → 排队", 3, 1, 1, true},
		{"上限 2 → 最多两路", 3, 2, 2, false},
		{"上限 4 → 有几分发多少", 3, 4, 3, false},
	} {
		t.Run(arm.name, func(t *testing.T) {
			peak, executions, elapsed := directorDelegateRun(t, arm.n, arm.cap, delay, true)
			if peak != arm.wantPeak {
				t.Errorf("子 Agent 并发峰值 = %d, want %d（上限 %d、%d 发委派）", peak, arm.wantPeak, arm.cap, arm.n)
			}
			if executions != int64(arm.n) {
				t.Errorf("实际执行 %d 次, want %d 次 —— 有发委派被池子吞了", executions, arm.n)
			}
			if arm.wantSerial {
				if want := time.Duration(arm.n) * delay; elapsed < want {
					t.Errorf("串行臂墙钟 %v < %v —— 峰值与墙钟必须一起对上", elapsed, want)
				}
			}
		})
	}
}

// 急停开关：配置写的是 4，YAGENT_CONCURRENCY=0 也只能一发一发来。
func TestConcurrencyKillSwitchForcesSerialDelegations(t *testing.T) {
	t.Setenv("YAGENT_CONCURRENCY", "0")

	const delay = 60 * time.Millisecond
	peak, executions, elapsed := directorDelegateRun(t, 2, 4, delay, true)
	if peak != 1 {
		t.Errorf("急停开关之后并发峰值 = %d, want 1", peak)
	}
	if executions != 2 {
		t.Errorf("实际执行 %d 次, want 2 次", executions)
	}
	if elapsed < 2*delay {
		t.Errorf("墙钟 %v < %v，两发并没有真的排队", elapsed, 2*delay)
	}
}

// 同一个判据也管住只读工具批（G1）：两个循环都只经 NormalizeMaxParallelReadOnlyTools
// 取值，所以判据写在这一处就是两处。
func TestConcurrencyKillSwitchForcesSerialToolBatches(t *testing.T) {
	if got := NormalizeMaxParallelReadOnlyTools(4); got != 4 {
		t.Errorf("没设开关时 NormalizeMaxParallelReadOnlyTools(4) = %d, want 4", got)
	}
	if got := NormalizeMaxParallelDelegations(0); got != DefaultMaxParallelDelegations {
		t.Errorf("没配时委派上限 = %d, want 默认 %d", got, DefaultMaxParallelDelegations)
	}

	t.Setenv("YAGENT_CONCURRENCY", "0")
	if got := NormalizeMaxParallelReadOnlyTools(4); got != 1 {
		t.Errorf("YAGENT_CONCURRENCY=0 时只读批并行度 = %d, want 1（严格串行）", got)
	}
	if got := NormalizeMaxParallelDelegations(4); got != 1 {
		t.Errorf("YAGENT_CONCURRENCY=0 时委派上限 = %d, want 1", got)
	}
}

// 键要从配置一路走到池子。这一条钉的是 NewDirectorAgent 里那句赋值 ——
// 漏掉它的表现不是崩溃，是"写了 max_parallel_delegations = 1，委派照样并发"。
func TestDirectorAgentTakesDelegationCapFromConfig(t *testing.T) {
	gctx := newTestGlobalCtx(t.TempDir())

	for _, arm := range []struct {
		name       string
		configured int
		want       int
	}{
		{"没配 → 默认", 0, DefaultMaxParallelDelegations},
		{"写 1 → 串行", 1, 1},
		{"写 3", 3, 3},
		{"写 99 → 钳到 8", 99, MaxMaxParallelDelegations},
	} {
		t.Run(arm.name, func(t *testing.T) {
			cfg := config.Config{}
			cfg.Agent.MaxParallelDelegations = arm.configured
			a := newDirectorAgentForTest(gctx, &mockEngine{}, nil, nil, nil, nil, nil, nil, 10, nil, 3, cfg, nil)
			if got := cap(newDelegatePool(a.maxParallelDelegations)); got != arm.want {
				t.Errorf("配置 %d → 池子容量 = %d, want %d", arm.configured, got, arm.want)
			}
		})
	}
}

// 开关拉到串行时，配置写多少都归 1（判据在 Normalize 里，构造期就走）。
func TestKillSwitchReachesDirectorAgentPool(t *testing.T) {
	t.Setenv("YAGENT_CONCURRENCY", "0")

	gctx := newTestGlobalCtx(t.TempDir())
	cfg := config.Config{}
	cfg.Agent.MaxParallelDelegations = 4
	a := newDirectorAgentForTest(gctx, &mockEngine{}, nil, nil, nil, nil, nil, nil, 10, nil, 3, cfg, nil)
	if got := cap(newDelegatePool(a.maxParallelDelegations)); got != 1 {
		t.Errorf("急停开关之后池子容量 = %d, want 1", got)
	}
}

// 排在池子后面的那一发必须替 ctx 让路：它不该真的开始执行子 Agent。
// 判据用"执行次数"而不是墙钟 —— toolbatch 会 join 整批，占着格子的不响应取消时
// 两臂的墙钟一样长，只有"第二发到底有没有被执行"能分辨。
func TestQueuedDelegationYieldsToCancelAndKeepsPairing(t *testing.T) {
	const delay = 300 * time.Millisecond

	var ran atomic.Int64
	var cur atomic.Int64
	mkTool := func(name string) *tools.Adapter {
		return tools.NewAdapter(name, "mock delegate", func(context.Context, map[string]interface{}) (interface{}, error) {
			ran.Add(1)
			cur.Add(1)
			defer cur.Add(-1)
			// 不响应取消：真实形状是子 Agent 卡在一次性进程调用里。
			time.Sleep(delay)
			return "ok:" + name, nil
		}).WithReadOnly(true)
	}

	agent := &DirectorAgent{
		surface:                  tools.NewSurfaceFromAdapters("test-agent", mkTool(delegateNames[0]), mkTool(delegateNames[1])),
		maxParallelReadOnlyTools: NormalizeMaxParallelReadOnlyTools(4),
		maxParallelDelegations:   1,
		delegateIdleTimeout:      10 * time.Second,
	}
	state := &director.RunState{MaxSteps: 4}
	toolRunner := newDirectorToolRunner(agent, state)

	mem := memory.NewConversationMemory(128)
	mem.AddHumanMessage("task input")

	cfg := director.PlannerConfig{
		LLM: &mockLLM{responses: []*llm.Response{
			{Choices: []llm.Choice{{ToolCalls: []llm.ToolCall{
				{ID: "call_a", Type: "function", Function: llm.FunctionCall{Name: delegateNames[0], Arguments: `{}`}},
				{ID: "call_b", Type: "function", Function: llm.FunctionCall{Name: delegateNames[1], Arguments: `{}`}},
			}}}},
			{Choices: []llm.Choice{{Content: "done"}}},
		}},
		Publisher:                &spyPublisher{},
		Tools:                    toolRunner,
		Prompts:                  func(_ context.Context, _ director.PromptInput) (string, error) { return "sys", nil },
		MaxSteps:                 4,
		LLMTimeout:               time.Minute,
		ToolTimeout:              30 * time.Second,
		MaxParallelReadOnlyTools: agent.maxParallelReadOnlyTools,
		IsParallelizableTool:     agent.isToolParallelizable,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := director.NewPlanner(cfg, state).Run(ctx, director.PlanInput{Input: "task input", Mem: mem})
	if err != nil && !strings.Contains(err.Error(), "context") {
		t.Fatalf("Planner.Run err = %v, want 与取消有关或没有", err)
	}

	results := toolResults(mem)
	if len(results) != 2 {
		t.Fatalf("tool 结果 = %q, want 两条 —— 每个 tool_call 必有对应产物（配对不变量）", results)
	}
	if got := ran.Load(); got != 1 {
		t.Errorf("子 Agent 被执行 %d 次, want 1 —— 排在池子上的那一发必须替 ctx 让路，不该真开始跑", got)
	}
	var errored bool
	for _, r := range results {
		if strings.HasPrefix(r, "Error:") {
			errored = true
		}
	}
	if !errored {
		t.Errorf("两条结果里没有一条是错误 —— 被取消的那发该留下 Error: 而不是静默消失：%q", results)
	}
}
