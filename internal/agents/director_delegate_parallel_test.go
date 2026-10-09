package agents

// 只读工具并发调度（第二阶段）—— agents 门面侧测试：
//
//  1. TestRunAgentLoop_DelegateReadOnlyParallelGroup：executor 路径放开
//     delegate 并发后的直接验证 —— 两个 mock 只读 delegate_*（WithReadOnly(true)）
//     在同一只读游程组内并发执行（原子计数器证明最大并发 ≥2），且两个 tool
//     消息按原 tool_calls 顺序落账（ExecutorResult.History 中 RoleTool 序）。
//  2. TestDirectorSwitchPathMixedBatchViaPlanner：director switch 分派路径
//     （directorToolRunner.Call —— 原 run() 工具分发段，含 120s/delegate 空闲/
//     交互豁免的 switch 超时分派）与 planner 分段并发调度的链路整合验证 ——
//     混合批 [只读, 写类, 只读] 时写类与前后只读组不重叠执行、落账按原序。

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"yagent/internal/agents/director"
	"yagent/internal/llm"
	"yagent/internal/memory"
	"yagent/internal/tools"
)

// ─── 并发/重叠计数器（闭包共享状态，atomic 三件套） ──────────────────────────

type concurrencyProbe struct {
	cur     atomic.Int64 // 只读元素当前在飞数
	maxCur  atomic.Int64 // 只读元素最大并发峰值
	writing atomic.Bool  // 写类元素在飞标志
	overlap atomic.Int64 // 只读/写类重叠计数（必须为 0）
}

// readonlyTool 返回只读 mock 工具适配器（WithReadOnly(true)）。
func (p *concurrencyProbe) readonlyTool(name string, delay time.Duration) *tools.Adapter {
	return tools.NewAdapter(name, "read-only mock tool", func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		if p.writing.Load() {
			p.overlap.Add(1) // 只读进入时写类不应在飞
		}
		c := p.cur.Add(1)
		for {
			old := p.maxCur.Load()
			if c <= old || p.maxCur.CompareAndSwap(old, c) {
				break
			}
		}
		defer p.cur.Add(-1)
		select {
		case <-time.After(delay):
			return "ok:" + name, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}).WithReadOnly(true)
}

// writeTool 返回写类 mock 工具适配器（不标记只读 → 原位串行段）。
func (p *concurrencyProbe) writeTool(name string, delay time.Duration) *tools.Adapter {
	return tools.NewAdapter(name, "write mock tool", func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		if p.cur.Load() > 0 {
			p.overlap.Add(p.cur.Load()) // 写类进入时只读组必须已 join
		}
		p.writing.Store(true)
		time.Sleep(delay)
		p.writing.Store(false)
		return "written:" + name, nil
	})
}

// ─── 1) executor 路径：两个 mock 只读 delegate 并发 ≥2 + 原序落账 ────────────

func TestRunAgentLoop_DelegateReadOnlyParallelGroup(t *testing.T) {
	var probe concurrencyProbe
	// 模拟 delegate 闭包形态（name=delegate_*，WithReadOnly(true) 标记依据见
	// 源码 director.go 审计注释）。两个元素共用 probe 证明同组并发。
	mockRepoDelegate := tools.NewAdapter("delegate_repo", "mock delegate", func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		if probe.writing.Load() {
			probe.overlap.Add(1)
		}
		c := probe.cur.Add(1)
		for {
			old := probe.maxCur.Load()
			if c <= old || probe.maxCur.CompareAndSwap(old, c) {
				break
			}
		}
		defer probe.cur.Add(-1)
		time.Sleep(60 * time.Millisecond) // delegate 形态的重负载窗口
		return `{"summary":"repo result"}`, nil
	}).WithReadOnly(true)

	mockChatDelegate := tools.NewAdapter("delegate_chat", "mock delegate", func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		if probe.writing.Load() {
			probe.overlap.Add(1)
		}
		c := probe.cur.Add(1)
		for {
			old := probe.maxCur.Load()
			if c <= old || probe.maxCur.CompareAndSwap(old, c) {
				break
			}
		}
		defer probe.cur.Add(-1)
		time.Sleep(60 * time.Millisecond)
		return "chat result", nil
	}).WithReadOnly(true)

	exitTool := tools.NewAdapter("agent_exit", "exit", func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		return "exiting", nil
	})

	cfg := DefaultExecutorConfig()
	cfg.SystemPrompt = "sys"
	cfg.UserInput = "task"
	cfg.Adapters = []*tools.Adapter{mockRepoDelegate, mockChatDelegate, exitTool}
	cfg.LLM = &mockLLM{responses: []*llm.Response{
		newToolResponse(
			llm.ToolCall{ID: "call_ro_1", Type: "function", Function: llm.FunctionCall{Name: "delegate_repo", Arguments: `{}`}},
			llm.ToolCall{ID: "call_ro_2", Type: "function", Function: llm.FunctionCall{Name: "delegate_chat", Arguments: `{}`}},
		),
		newToolResponse(llm.ToolCall{ID: "call_exit", Type: "function", Function: llm.FunctionCall{Name: "agent_exit", Arguments: `{}`}}),
	}}
	cfg.MaxSteps = 3
	cfg.StopOnFinish = true
	cfg.MaxParallelReadOnlyTools = 4 // 显式放开（=1 时串行 kill-switch，不得触发本断言）

	res, err := RunAgentLoop(context.Background(), cfg)
	if err != nil {
		t.Fatalf("RunAgentLoop: %v", err)
	}

	if got := probe.maxCur.Load(); got < 2 {
		t.Fatalf("max concurrent read-only delegates = %d, want >= 2", got)
	}
	if got := probe.overlap.Load(); got != 0 {
		t.Fatalf("read/write overlap detected %d times, want 0", got)
	}

	// 两个 tool 消息按原 tool_calls 顺序落账（history 内 RoleTool 序；
	// agent_exit 的消息也在 StopOnFinish 返回前落账，校验前两条原序即可）
	var ids []string
	for _, m := range toolMessagesFromHistory(res.History) {
		ids = append(ids, m.ToolCallID)
	}
	want := []string{"call_ro_1", "call_ro_2"}
	if len(ids) < len(want) {
		t.Fatalf("tool message count = %d (%v), want >= %v", len(ids), ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("tool message order = %v, want %v", ids, want)
		}
	}
}

// ─── 2) director switch 分派路径：混合批分段不重叠（链路经 planner.Run） ─────

func TestDirectorSwitchPathMixedBatchViaPlanner(t *testing.T) {
	var probe concurrencyProbe

	agent := &DirectorAgent{
		Adapters: []*tools.Adapter{
			// 首元素用 delegate_ 前缀（mock readonly delegate）：真实覆盖 switch
			// 的 isDelegate 分支（WithIdleTimeout）+ RunState 统计更新；IsReadOnly
			// 标记使其进入只读并行段
			probe.readonlyTool("delegate_repo", 40*time.Millisecond),
			probe.writeTool("write_tool", 30*time.Millisecond),
			probe.readonlyTool("ro_2", 40*time.Millisecond),
		},
		maxParallelReadOnlyTools: NormalizeMaxParallelReadOnlyTools(4),
		delegateIdleTimeout:      2 * time.Second, // >0：WithIdleTimeout 契约要求
	}
	state := &director.RunState{MaxSteps: 4}
	toolRunner := &directorToolRunner{agent: agent, state: state}

	mem := memory.NewConversationMemory(128)
	mem.AddHumanMessage("task input")
	sp := &spyPublisher{}

	cfg := director.PlannerConfig{
		LLM: &mockLLM{responses: []*llm.Response{
			{Choices: []llm.Choice{{ToolCalls: []llm.ToolCall{
				{ID: "call_ro_1", Type: "function", Function: llm.FunctionCall{Name: "delegate_repo", Arguments: `{}`}},
				{ID: "call_write", Type: "function", Function: llm.FunctionCall{Name: "write_tool", Arguments: `{}`}},
				{ID: "call_ro_2", Type: "function", Function: llm.FunctionCall{Name: "ro_2", Arguments: `{}`}},
			}}}},
			{Choices: []llm.Choice{{Content: "done"}}},
		}},
		Publisher:                sp,
		Tools:                    toolRunner,
		Prompts:                  func(_ context.Context, _ director.PromptInput) (string, error) { return "sys", nil },
		MaxSteps:                 4,
		LLMTimeout:               time.Minute,
		ToolTimeout:              5 * time.Second,
		MaxParallelReadOnlyTools: agent.maxParallelReadOnlyTools,
		IsParallelizableTool:     agent.isToolParallelizable, // 真实门面谓词（denies/交互/IsReadOnly）
	}
	res, err := director.NewPlanner(cfg, state).Run(context.Background(), director.PlanInput{
		Input: "task input",
		Mem:   mem,
	})
	if err != nil {
		t.Fatalf("Planner.Run via directorToolRunner: %v", err)
	}
	if res.StopReason != "plain_text" {
		t.Fatalf("StopReason = %q, want plain_text", res.StopReason)
	}

	if got := probe.overlap.Load(); got != 0 {
		t.Fatalf("read/write overlap detected %d times, want 0", got)
	}
	if got := probe.maxCur.Load(); got > 1 {
		t.Fatalf("max concurrent = %d, want 1 (single-element segments)", got)
	}

	// 落账按原序：前只读 → 写类 → 后只读（mem tool 消息序）
	var ids []string
	for _, m := range mem.GetMessages() {
		if m.Type == memory.MessageTypeTool && m.ToolCallID != nil {
			ids = append(ids, *m.ToolCallID)
		}
	}
	want := []string{"call_ro_1", "call_write", "call_ro_2"}
	if len(ids) != len(want) {
		t.Fatalf("tool message count = %d (%v), want %v", len(ids), ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("tool message order = %v, want %v", ids, want)
		}
	}
}
