package director

// 只读工具并发调度（第二阶段）—— planner 侧测试：
// 与 agents.RunAgentLoop 同一 toolbatch 调度（Plan 分段 + Run 并发执行 +
// join 后原序 commit）在本站点的行为验证：
//
//  1. TestPlannerReadOnlyBatchParallelism（director 层并发 delegate 测试）：
//     连续两个 mock 只读 delegate 并发执行（原子计数器证明最大并发 ≥2），
//     且两个 tool 消息按原 tool_calls 顺序落账（mem 的 tool 消息序 +
//     tool_call_result 事件序），tool_call_start 事件先于对应 result。
//  2. TestPlannerMixedBatchSegmentation：混合批 [只读, 写类, 只读] ——
//     写类元素执行窗口内没有任何只读元素在飞（前只读组 join 落账后才执行
//     写类，后只读组等写类 commit 后才启动），事件/落账按原序。
//  3. TestPlannerParallelKillSwitch：并行度 1（kill-switch）→ 严格串行。

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yagent/internal/llm"
	"yagent/internal/memory"
)

// ─── 测试用 fake ─────────────────────────────────────────────────────────────

// seqConcurrentLLM 按序弹出响应的 LLMClient fake（GenerateContent 只被
// planner 主线程调用，slice + 计数即可）。
type seqConcurrentLLM struct {
	responses []*llm.Response
	n         int
}

func (m *seqConcurrentLLM) GenerateContent(_ context.Context, _ []llm.Message, _ []llm.ToolDef, _ *llm.CallOptions) (*llm.Response, error) {
	if m.n >= len(m.responses) {
		return nil, fmt.Errorf("seqConcurrentLLM: unexpected call #%d (only %d responses configured)", m.n, len(m.responses))
	}
	resp := m.responses[m.n]
	m.n++
	return resp, nil
}

func (m *seqConcurrentLLM) Model() string { return "mock-model-director" }

// seqCallSpyPublisher 记录 (event, tool_call_id) 序列的 EventPublisher fake：
// 用于断言 start/result 事件配对与 result 落账原序。
type seqCallSpyPublisher struct {
	mu    sync.Mutex
	calls []string // 形如 "tool_call_start:ID" / "tool_call_result:ID" / "event"
}

func (s *seqCallSpyPublisher) Publish(event string, payload interface{}, _ string) error {
	if m, ok := payload.(map[string]interface{}); ok {
		if id, _ := m["tool_call_id"].(string); id != "" {
			s.mu.Lock()
			s.calls = append(s.calls, event+":"+id)
			s.mu.Unlock()
			return nil
		}
	}
	s.mu.Lock()
	s.calls = append(s.calls, event)
	s.mu.Unlock()
	return nil
}

func (s *seqCallSpyPublisher) PublishWithMetadata(event string, payload interface{}, _ string, _ map[string]interface{}) error {
	return s.Publish(event, payload, "")
}

func (s *seqCallSpyPublisher) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

// concurrentSpyToolRunner 并发感知 ToolRunner fake：
//   - readOnly 集元素并发递增 cur/maxCur，进入时发现写类在飞则计 overlap；
//   - 写类元素进入时记录只读在飞数（>0 即重叠）；
//   - 互斥标志 writing 表示写类在飞。
type concurrentSpyToolRunner struct {
	readOnly map[string]bool
	state    *RunState // 模拟门面 wrapper：delegate_* 调用更新统计（HasDelegated→免强制委派提醒）

	cur     atomic.Int64 // 只读元素当前在飞数
	maxCur  atomic.Int64 // 只读元素最大并发峰值
	writing atomic.Bool  // 写类元素在飞标志
	overlap atomic.Int64 // 只读/写类重叠计数（必须为 0）
}

func (t *concurrentSpyToolRunner) Specs() []llm.ToolDef {
	defs := make([]llm.ToolDef, 0, len(t.readOnly)+2)
	for name := range t.readOnly {
		defs = append(defs, llm.ToolDef{Type: "function", Function: llm.FunctionDef{Name: name}})
	}
	defs = append(defs,
		llm.ToolDef{Type: "function", Function: llm.FunctionDef{Name: "write_tool"}},
		llm.ToolDef{Type: "function", Function: llm.FunctionDef{Name: "agent_exit"}},
	)
	return defs
}

func (t *concurrentSpyToolRunner) Call(_ context.Context, name string, _ string) (string, error) {
	// 模拟门面 Tools 包装闭包语义：一律记录委派统计（本测试关注分段/落账语义，
	// 统一置 HasDelegated 以跳过 planner 的强制委派提醒控制流；锁方法走并发路径）
	if t.state != nil {
		t.state.RecordDelegation(true)
	}
	if t.readOnly[name] {
		// 重叠检测：只读进入时写类不应在飞（写类走原位串行段）
		if t.writing.Load() {
			t.overlap.Add(1)
		}
		c := t.cur.Add(1)
		for {
			old := t.maxCur.Load()
			if c <= old || t.maxCur.CompareAndSwap(old, c) {
				break
			}
		}
		defer t.cur.Add(-1)
		time.Sleep(40 * time.Millisecond) // 给兄弟元素留出并发窗口
		return "ok:" + name, nil
	}
	// 写类元素（串行段）：进入时前只读组必须已 join 落账（在飞数 0）
	if t.cur.Load() > 0 {
		t.overlap.Add(t.cur.Load())
	}
	t.writing.Store(true)
	time.Sleep(30 * time.Millisecond)
	t.writing.Store(false)
	return "written:" + name, nil
}

// runPlannerBatch 统一装配：fake LLM（一批 tool_calls + 收尾纯文本）、并发
// 调度配置，运行 Planner.Run；返回 mem / publisher 供原序断言。
func runPlannerBatch(t *testing.T, calls []llm.ToolCall, runner *concurrentSpyToolRunner, maxParallel int) (*memory.ConversationMemory, *seqCallSpyPublisher) {
	t.Helper()
	if maxParallel <= 0 {
		maxParallel = 4
	}
	isParallel := func(name string) bool {
		if name == "agent_exit" || name == "delegate_meta" {
			return false // 与门面谓词一致的 deny-list 站点语义
		}
		return runner.readOnly[name]
	}
	mem := memory.NewConversationMemory(128)
	mem.AddHumanMessage("task input")

	sp := &seqCallSpyPublisher{}
	state := &RunState{MaxSteps: 4}
	// runner 挂载 state：模拟门面 wrapper 的 delegate 统计更新（并发路径锁保护）
	runner.state = state
	cfg := PlannerConfig{
		LLM: &seqConcurrentLLM{responses: []*llm.Response{
			{Choices: []llm.Choice{{ToolCalls: calls}}},
			{Choices: []llm.Choice{{Content: "done"}}},
		}},
		Publisher:                sp,
		Tools:                    runner,
		Prompts:                  func(context.Context, PromptInput) (string, error) { return "sys", nil },
		MaxSteps:                 4,
		LLMTimeout:               time.Minute,
		ToolTimeout:              5 * time.Second,
		MaxParallelReadOnlyTools: maxParallel,
		IsParallelizableTool:     isParallel,
	}
	res, err := NewPlanner(cfg, state).Run(context.Background(), PlanInput{Input: "task input", Mem: mem})
	if err != nil {
		t.Fatalf("Planner.Run: %v", err)
	}
	if res.StopReason != "plain_text" {
		t.Fatalf("StopReason = %q, want plain_text", res.StopReason)
	}
	return mem, sp
}

// toolMessageOrder 从 mem 提取 tool 消息落账序（commit 原序断言源）。
func toolMessageOrder(mem *memory.ConversationMemory) []string {
	var ids []string
	for _, m := range mem.GetMessages() {
		if m.Type == memory.MessageTypeTool && m.ToolCallID != nil {
			ids = append(ids, *m.ToolCallID)
		}
	}
	return ids
}

// assertEqualStrings 顺序敏感的 []string 相等断言。
func assertEqualStrings(t *testing.T, got, want []string, label string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s = %v, want %v (index %d)", label, got, want, i)
		}
	}
}

// ─── 1) director 层：连续只读 delegate 并发 ≥2 + 原序落账 ────────────────────

func TestPlannerReadOnlyBatchParallelism(t *testing.T) {
	runner := &concurrentSpyToolRunner{readOnly: map[string]bool{"delegate_repo": true, "delegate_chat": true}}
	mem, sp := runPlannerBatch(t, []llm.ToolCall{
		{ID: "call_ro_1", Type: "function", Function: llm.FunctionCall{Name: "delegate_repo", Arguments: `{}`}},
		{ID: "call_ro_2", Type: "function", Function: llm.FunctionCall{Name: "delegate_chat", Arguments: `{}`}},
	}, runner, 4)

	if got := runner.maxCur.Load(); got < 2 {
		t.Fatalf("max concurrent read-only delegates = %d, want >= 2", got)
	}
	if got := runner.overlap.Load(); got != 0 {
		t.Fatalf("read/write overlap detected %d times, want 0", got)
	}
	// tool 消息按原 tool_calls 顺序落账（join 后 commit）
	assertEqualStrings(t, toolMessageOrder(mem), []string{"call_ro_1", "call_ro_2"}, "tool message order")
	// tool_call_result 事件按原序（start/result 配对各一次；并发组内 start 的
	// 发布顺序不承诺原序 —— 与 executor 站点语义一致，result 承诺原序）
	var results, starts []string
	for _, c := range sp.snapshot() {
		if strings.HasPrefix(c, "tool_call_result:") {
			results = append(results, c)
		}
		if strings.HasPrefix(c, "tool_call_start:") {
			starts = append(starts, c)
		}
	}
	assertEqualStrings(t, results, []string{"tool_call_result:call_ro_1", "tool_call_result:call_ro_2"}, "result event order")
	// start/result 配对：每个 tool_call_id 各 start、result 一次（start 发布顺序
	// 在并发组内不承诺原序，只校验配对不丢事件）
	sort.Strings(starts)
	assertEqualStrings(t, starts, []string{"tool_call_start:call_ro_1", "tool_call_start:call_ro_2"}, "start events sorted (pairing)")
}

// ─── 2) director 层：混合批 [只读, 写类, 只读] 分段不重叠 ─────────────────────

func TestPlannerMixedBatchSegmentation(t *testing.T) {
	runner := &concurrentSpyToolRunner{readOnly: map[string]bool{"ro_1": true, "ro_2": true}}
	mem, _ := runPlannerBatch(t, []llm.ToolCall{
		{ID: "call_ro_1", Type: "function", Function: llm.FunctionCall{Name: "ro_1", Arguments: `{}`}},
		{ID: "call_write", Type: "function", Function: llm.FunctionCall{Name: "write_tool", Arguments: `{}`}},
		{ID: "call_ro_2", Type: "function", Function: llm.FunctionCall{Name: "ro_2", Arguments: `{}`}},
	}, runner, 4)

	if got := runner.overlap.Load(); got != 0 {
		t.Fatalf("read/write overlap detected %d times, want 0", got)
	}
	if got := runner.maxCur.Load(); got > 1 {
		// 两个只读元素各自成单元素段（被写类打断），组内不发生并发
		t.Fatalf("max concurrent = %d, want 1 (single-element segments)", got)
	}
	// 落账按原序：前只读 → 写类 → 后只读
	assertEqualStrings(t, toolMessageOrder(mem), []string{"call_ro_1", "call_write", "call_ro_2"}, "tool message order")
}

// ─── 3) kill-switch：并行度 1 → 严格串行（与逐元素串行一致） ──────────────────

func TestPlannerParallelKillSwitch(t *testing.T) {
	runner := &concurrentSpyToolRunner{readOnly: map[string]bool{"delegate_repo": true, "delegate_chat": true}}
	_, _ = runPlannerBatch(t, []llm.ToolCall{
		{ID: "call_ro_1", Type: "function", Function: llm.FunctionCall{Name: "delegate_repo", Arguments: `{}`}},
		{ID: "call_ro_2", Type: "function", Function: llm.FunctionCall{Name: "delegate_chat", Arguments: `{}`}},
	}, runner, 1)

	if got := runner.maxCur.Load(); got != 1 {
		t.Fatalf("max concurrent with kill-switch = %d, want 1 (strict serial)", got)
	}
}
