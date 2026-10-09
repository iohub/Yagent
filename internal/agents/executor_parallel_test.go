package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yagent/internal/llm"
	"yagent/internal/memory"
	"yagent/internal/tools"
)

// ═════════════════════════════════════════════════════════════════════════════
// 只读工具并发执行(顺序保持的分段并发)集成测试
// ═════════════════════════════════════════════════════════════════════════════

// ─── 事件 Spy ────────────────────────────────────────────────────────────────

type spyEvent struct {
	evType  string
	content interface{}
}

type spyPublisher struct {
	mu     sync.Mutex
	events []spyEvent
}

func (s *spyPublisher) Publish(evType string, content interface{}, from string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, spyEvent{evType: evType, content: content})
	return nil
}

func (s *spyPublisher) PublishWithMetadata(evType string, content interface{}, from string, metadata map[string]interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, spyEvent{evType: evType, content: content})
	return nil
}

func (s *spyPublisher) snapshot() []spyEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]spyEvent(nil), s.events...)
}

// spyIndexOf 返回指定事件类型+tool_call_id 首次出现的下标(-1=不存在)
func spyIndexOf(events []spyEvent, evType, callID string) int {
	for i, e := range events {
		if e.evType != evType {
			continue
		}
		if m, ok := e.content.(map[string]interface{}); ok {
			if id, _ := m["tool_call_id"].(string); id == callID {
				return i
			}
		}
	}
	return -1
}

// ─── 工具构造 ────────────────────────────────────────────────────────────────

// newReadOnlyTestAdapter 具名只读测试工具:记录并发峰值、可选按序取消 ctx
func newReadOnlyTestAdapter(t *testing.T, name string, delay time.Duration, ret string, cur, maxCur *atomic.Int64, cancelFn context.CancelFunc) *tools.Adapter {
	t.Helper()
	return tools.NewAdapter(name, "read-only test tool", func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		if cur != nil && maxCur != nil {
			c := cur.Add(1)
			for {
				old := maxCur.Load()
				if c <= old || maxCur.CompareAndSwap(old, c) {
					break
				}
			}
			defer cur.Add(-1)
		}
		if name == "ro_first" && cancelFn != nil {
			cancelFn()
		}
		time.Sleep(delay)
		return ret, nil
	}).WithReadOnly(true)
}

// newBlockingReadOnly 阻塞直到 ctx 取消的只读工具
func newBlockingReadOnly(name string, cancelFn context.CancelFunc) *tools.Adapter {
	return tools.NewAdapter(name, "blocking read-only", func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		if cancelFn != nil {
			cancelFn()
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}).WithReadOnly(true)
}

// ─── Mock 响应构造 ───────────────────────────────────────────────────────────

func newToolResponse(calls ...llm.ToolCall) *llm.Response {
	return &llm.Response{Choices: []llm.Choice{{ToolCalls: calls}}}
}

func newTextResponse(s string) *llm.Response {
	return &llm.Response{Choices: []llm.Choice{{Content: s}}}
}

func makeCall(id, name string) llm.ToolCall {
	return llm.ToolCall{
		ID:       id,
		Type:     "function",
		Function: llm.FunctionCall{Name: name, Arguments: `{}`},
	}
}

// toolMessagesFromHistory 提取 history 中所有 RoleTool 消息
func toolMessagesFromHistory(history []llm.Message) []llm.Message {
	var out []llm.Message
	for _, m := range history {
		if m.Role == llm.RoleTool {
			out = append(out, m)
		}
	}
	return out
}

// ─── Rollout 顺序断言 ────────────────────────────────────────────────────────

// rolloutToolCallOrder 读取 rollout JSONL,按写入顺序返回
// function_call_output 的 call_id 序列
func rolloutToolCallOrder(t *testing.T, w *memory.RolloutWriter) []string {
	t.Helper()
	data, err := os.ReadFile(w.FilePath())
	if err != nil {
		t.Fatalf("read rollout file: %v", err)
	}
	var ids []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var env struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			continue
		}
		if env.Type != "response_item" {
			continue
		}
		var item struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
		}
		if err := json.Unmarshal(env.Payload, &item); err != nil {
			continue
		}
		if item.Type == "function_call_output" && item.CallID != "" {
			ids = append(ids, item.CallID)
		}
	}
	return ids
}

// expectInt64 断言取值(减少样板)
func expectInt64(t *testing.T, got, want int64, desc string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %d, want %d", desc, got, want)
	}
}

// expectOrder 断言字符串切片逐项相等
func expectOrder(t *testing.T, got, want []string, desc string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %v, want %v", desc, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s: got %v, want %v", desc, got, want)
		}
	}
}

// ═══ T2 并发实证:3 个 sleep 假读工具,原子计数器最大并发 ≥2 ═══

func TestRunAgentLoop_ReadOnlyBatch_ParallelEvidence(t *testing.T) {
	// 共享并发计量器:三个工具都经闭包引用同一组 atomic
	var cur, maxCur atomic.Int64
	ads := []*tools.Adapter{
		newReadOnlyTestAdapter(t, "ro_a", 60*time.Millisecond, "a", &cur, &maxCur, nil),
		newReadOnlyTestAdapter(t, "ro_b", 60*time.Millisecond, "b", &cur, &maxCur, nil),
		newReadOnlyTestAdapter(t, "ro_c", 60*time.Millisecond, "c", &cur, &maxCur, nil),
	}

	mockResponses := []*llm.Response{
		newToolResponse(makeCall("c1", "ro_a"), makeCall("c2", "ro_b"), makeCall("c3", "ro_c")),
		newTextResponse("done"),
	}

	publisher := &spyPublisher{}
	cfg := ExecutorConfig{
		SystemPrompt:             "test",
		UserInput:                "run read-only batch",
		Adapters:                 ads,
		LLM:                      &mockLLM{responses: mockResponses},
		MaxSteps:                 3,
		Publisher:                publisher,
		AgentName:                "parallel-evidence",
		MaxParallelReadOnlyTools: 4,
	}

	_, err := RunAgentLoop(context.Background(), cfg)
	if err != nil {
		t.Fatalf("RunAgentLoop: %v", err)
	}

	if m := maxCur.Load(); m < 2 {
		t.Fatalf("expected max concurrency >= 2 for read-only batch, got %d", m)
	}

	ev := publisher.snapshot()
	for _, id := range []string{"c1", "c2", "c3"} {
		if spyIndexOf(ev, "tool_call_start", id) < 0 {
			t.Errorf("missing tool_call_start for %s", id)
		}
		if spyIndexOf(ev, "tool_call_result", id) < 0 {
			t.Errorf("missing tool_call_result for %s", id)
		}
	}
}

// ═══ T3 顺序保持:完成顺序错乱时 messages 序 / writeRollout 序 / OnToolResult 序 = 原序 ═══

func TestRunAgentLoop_ReadOnlyBatch_OrderPreserved(t *testing.T) {
	// 注意:delay 使完成顺序与提交顺序错乱(c1 最慢、c3 最快)
	ads := []*tools.Adapter{
		newReadOnlyTestAdapter(t, "ro_search", 90*time.Millisecond, "s-result", nil, nil, nil),
		newReadOnlyTestAdapter(t, "ro_list", 45*time.Millisecond, "l-result", nil, nil, nil),
		newReadOnlyTestAdapter(t, "ro_tree", 5*time.Millisecond, "t-result", nil, nil, nil),
	}

	w, err := memory.NewRolloutWriter("order-test-agent", "task-order-test", "parallel-readonly-test")
	if err != nil || !w.Enabled() {
		t.Skipf("rollout writer unavailable: %v", err)
	}
	defer w.Close()
	ctx := memory.WithRolloutWriter(context.Background(), w)

	mockResponses := []*llm.Response{
		newToolResponse(makeCall("c1", "ro_search"), makeCall("c2", "ro_list"), makeCall("c3", "ro_tree")),
		newTextResponse("done"),
	}
	publisher := &spyPublisher{}
	var onToolCalls []string
	var mu sync.Mutex

	cfg := ExecutorConfig{
		SystemPrompt:             "test",
		UserInput:                "order test",
		Adapters:                 ads,
		LLM:                      &mockLLM{responses: mockResponses},
		MaxSteps:                 3,
		Publisher:                publisher,
		AgentName:                "order-test",
		MaxParallelReadOnlyTools: 4,
		OnToolResult: func(name string, result string) {
			mu.Lock()
			defer mu.Unlock()
			onToolCalls = append(onToolCalls, name)
		},
	}

	result, runErr := RunAgentLoop(ctx, cfg)
	if runErr != nil {
		t.Fatalf("RunAgentLoop: %v", runErr)
	}

	// 1) messages 序(history 同步 append,即 executor 内部 messages 尾部)
	toolMsgs := toolMessagesFromResult(result)
	expectOrder(t,
		[]string{toolMsgs[0].ToolCallID, toolMsgs[1].ToolCallID, toolMsgs[2].ToolCallID},
		[]string{"c1", "c2", "c3"},
		"tool message order in history")

	// 2) OnToolResult 回调序
	mu.Lock()
	gotOnTool := append([]string(nil), onToolCalls...)
	mu.Unlock()
	expectOrder(t, gotOnTool, []string{"ro_search", "ro_list", "ro_tree"}, "OnToolResult callback order")

	// 3) writeRollout 序(真实 RolloutWriter 落盘,解析 function_call_output 序)
	expectOrder(t, rolloutToolCallOrder(t, w), []string{"c1", "c2", "c3"}, "rollout function_call_output order")
}

// toolMessagesFromResult 提取 ExecutorResult.History 的 tool 消息
func toolMessagesFromResult(r ExecutorResult) []llm.Message {
	return toolMessagesFromHistory(r.History)
}

// ═══ T4 混合批 [R1,R2,W,R3] 事件分段证据 ═══

func TestRunAgentLoop_MixedBatch_EventSegmentation(t *testing.T) {
	roR1 := newReadOnlyTestAdapter(t, "ro_r1", 40*time.Millisecond, "r1", nil, nil, nil)
	roR2 := newReadOnlyTestAdapter(t, "ro_r2", 40*time.Millisecond, "r2", nil, nil, nil)
	// 写类工具:未标记只读 → 原位串行元素
	writeTool := tools.NewAdapter("create_file", "write tool", func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		time.Sleep(20 * time.Millisecond)
		return "w-done", nil
	})
	roR3 := newReadOnlyTestAdapter(t, "ro_r3", 5*time.Millisecond, "r3", nil, nil, nil)

	mockResponses := []*llm.Response{
		newToolResponse(
			makeCall("c1", "ro_r1"),
			makeCall("c2", "ro_r2"),
			makeCall("cw", "create_file"),
			makeCall("c4", "ro_r3"),
		),
		newTextResponse("done"),
	}
	publisher := &spyPublisher{}
	cfg := ExecutorConfig{
		SystemPrompt:             "test",
		UserInput:                "mixed batch",
		Adapters:                 []*tools.Adapter{roR1, roR2, writeTool, roR3},
		LLM:                      &mockLLM{responses: mockResponses},
		MaxSteps:                 3,
		Publisher:                publisher,
		AgentName:                "mixed-batch",
		MaxParallelReadOnlyTools: 4,
	}

	result, err := RunAgentLoop(context.Background(), cfg)
	if err != nil {
		t.Fatalf("RunAgentLoop: %v", err)
	}

	ev := publisher.snapshot()
	iStartR1 := spyIndexOf(ev, "tool_call_start", "c1")
	iStartR2 := spyIndexOf(ev, "tool_call_start", "c2")
	iEndR1 := spyIndexOf(ev, "tool_call_result", "c1")
	iEndR2 := spyIndexOf(ev, "tool_call_result", "c2")
	iStartW := spyIndexOf(ev, "tool_call_start", "cw")
	iEndW := spyIndexOf(ev, "tool_call_result", "cw")
	iStartR3 := spyIndexOf(ev, "tool_call_start", "c4")
	iEndR3 := spyIndexOf(ev, "tool_call_result", "c4")

	for _, idx := range []int{iStartR1, iStartR2, iEndR1, iEndR2, iStartW, iEndW, iStartR3, iEndR3} {
		if idx < 0 {
			t.Fatalf("missing event in sequence: %+v", ev)
		}
	}

	// W(串行写)开始晚于 R1/R2 结束(并发组 flush 后才执行串行元素)
	if !(iEndR1 < iStartW && iEndR2 < iStartW) {
		t.Fatalf("write tool must start after R1 and R2 results: R1end=%d R2end=%d Wstart=%d", iEndR1, iEndR2, iStartW)
	}
	// R3 开始晚于 W 结束,结束晚于自身开始
	if !(iEndW < iStartR3 && iStartR3 < iEndR3) {
		t.Fatalf("R3 must run strictly after write completes: Wend=%d R3start=%d R3end=%d", iEndW, iStartR3, iEndR3)
	}

	// messages 序:R1,R2,W,R3 原序
	toolMsgs := toolMessagesFromResult(result)
	var ids []string
	for _, m := range toolMsgs {
		ids = append(ids, m.ToolCallID)
	}
	expectOrder(t, ids, []string{"c1", "c2", "cw", "c4"}, "tool message order")
}

// ═══ 泄漏检查辅助 ═══

// assertNoGoroutineLeak 宽松判定:等待后台任务退出后 goroutine 数不增长
func assertNoGoroutineLeak(t *testing.T, before int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before+2 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("goroutine leak suspected: before=%d after=%d", before, runtime.NumGoroutine())
}

// unusedFmt 引用占位(保持 fmt 导入)
var _ = fmt.Sprintf

// ═══ T5 [R1, agent_exit, R2]:R1 落账、exit 落账后立即 return、R2 不落账 ═══

func TestRunAgentLoop_AgentExitStopsAfterFlush(t *testing.T) {
	r1 := newReadOnlyTestAdapter(t, "ro_r1", 10*time.Millisecond, "r1-done", nil, nil, nil)
	exitTool := tools.NewAdapter("agent_exit", "exit", func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		return "exited: task complete", nil
	})
	r2 := newReadOnlyTestAdapter(t, "ro_r2", 10*time.Millisecond, "r2-done", nil, nil, nil)

	mockResponses := []*llm.Response{newToolResponse(
		makeCall("c1", "ro_r1"),
		makeCall("cx", "agent_exit"),
		makeCall("c3", "ro_r2"), // 之后的 tool_call:不执行不落账
	)}
	publisher := &spyPublisher{}
	var onToolCalls []string
	var mu sync.Mutex
	before := runtime.NumGoroutine()

	cfg := ExecutorConfig{
		SystemPrompt:             "test",
		UserInput:                "exit in middle",
		Adapters:                 []*tools.Adapter{r1, exitTool, r2},
		LLM:                      &mockLLM{responses: mockResponses},
		MaxSteps:                 3,
		StopOnFinish:             true,
		Publisher:                publisher,
		AgentName:                "exit-test",
		MaxParallelReadOnlyTools: 4,
		OnToolResult: func(name string, result string) {
			mu.Lock()
			defer mu.Unlock()
			onToolCalls = append(onToolCalls, name)
		},
	}

	result, err := RunAgentLoop(context.Background(), cfg)
	if err != nil {
		t.Fatalf("RunAgentLoop: %v", err)
	}

	// exit 的 tool result 作为最终文本返回(Adapter.Call 既有行为:结果 JSON 序列化,字符串带引号)
	if result.Text != `"exited: task complete"` {
		t.Fatalf("result.Text = %q, want exit tool result", result.Text)
	}

	// R1 与 agent_exit 落账;R2 不落账
	toolMsgs := toolMessagesFromResult(result)
	var ids []string
	for _, m := range toolMsgs {
		ids = append(ids, m.ToolCallID)
	}
	expectOrder(t, ids, []string{"c1", "cx"}, "tool messages must stop after agent_exit")

	mu.Lock()
	got := append([]string(nil), onToolCalls...)
	mu.Unlock()
	expectOrder(t, got, []string{"ro_r1", "agent_exit"}, "OnToolResult must stop after agent_exit")

	// R2 无任何事件
	ev := publisher.snapshot()
	if spyIndexOf(ev, "tool_call_start", "c3") >= 0 {
		t.Fatal("tool_calls after agent_exit must not be announced or executed")
	}

	// 无 goroutine 泄漏(并发组已 join,R2 从未启动)
	assertNoGoroutineLeak(t, before)
}

// ═══ T7 失败隔离:[Rerr, Rok] 两条消息必须齐全 ═══

func TestRunAgentLoop_FailureIsolation(t *testing.T) {
	rerr := tools.NewAdapter("ro_err", "failing read-only", func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		time.Sleep(10 * time.Millisecond)
		return nil, fmt.Errorf("boom")
	}).WithReadOnly(true)
	rok := newReadOnlyTestAdapter(t, "ro_ok", 5*time.Millisecond, "ok-result", nil, nil, nil)

	mockResponses := []*llm.Response{
		newToolResponse(makeCall("c1", "ro_err"), makeCall("c2", "ro_ok")),
		newTextResponse("done"),
	}
	cfg := ExecutorConfig{
		SystemPrompt:             "test",
		UserInput:                "failure isolation",
		Adapters:                 []*tools.Adapter{rerr, rok},
		LLM:                      &mockLLM{responses: mockResponses},
		MaxSteps:                 3,
		AgentName:                "failure-isolation",
		MaxParallelReadOnlyTools: 4,
	}

	result, err := RunAgentLoop(context.Background(), cfg)
	if err != nil {
		t.Fatalf("RunAgentLoop: %v", err)
	}

	toolMsgs := toolMessagesFromResult(result)
	if len(toolMsgs) != 2 {
		t.Fatalf("both tool messages must exist, got %d: %+v", len(toolMsgs), toolMsgs)
	}
	if toolMsgs[0].ToolCallID != "c1" || toolMsgs[0].Content != "Error: boom" {
		t.Fatalf("failed tool message: id=%s content=%q", toolMsgs[0].ToolCallID, toolMsgs[0].Content)
	}
	if toolMsgs[1].ToolCallID != "c2" || toolMsgs[1].Content != `"ok-result"` {
		t.Fatalf("sibling tool message: id=%s content=%q", toolMsgs[1].ToolCallID, toolMsgs[1].Content)
	}
}

// ═══ T8 父 ctx 取消:每个 tool_call 都有对应错误消息 ═══

func TestRunAgentLoop_ParentCancel_AllCallsAccounted(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 6 个阻塞只读工具(依赖 ctx.Done 返回);限流 2 → 后 4 个必然在取消后排队
	names := []string{"ro_a", "ro_b", "ro_c", "ro_d", "ro_e", "ro_f"}

	// 首个工具内部触发取消:全部组内调用将以已取消 ctx 分批启动
	var cancelOnce sync.Once
	var ads []*tools.Adapter
	var calls []llm.ToolCall
	for idx, n := range names {
		calls = append(calls, makeCall(fmt.Sprintf("c%d", idx+1), n))
		if idx == 0 {
			fn := func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
				cancelOnce.Do(cancel)
				<-ctx.Done()
				return nil, ctx.Err()
			}
			ads = append(ads, tools.NewAdapter(n, "blocking", fn).WithReadOnly(true))
		} else {
			ads = append(ads, newBlockingReadOnly(n, nil))
		}
	}

	mockResponses := []*llm.Response{
		newToolResponse(calls...),
		newTextResponse("unreachable"),
	}
	cfg := ExecutorConfig{
		SystemPrompt:             "test",
		UserInput:                "parent cancel",
		Adapters:                 ads,
		LLM:                      &mockLLM{responses: mockResponses},
		MaxSteps:                 3,
		AgentName:                "cancel-test",
		MaxParallelReadOnlyTools: 2,
	}

	result, err := RunAgentLoop(parent, cfg)
	if err != nil {
		t.Fatalf("RunAgentLoop must not return ctx error (tools handle it): %v", err)
	}

	toolMsgs := toolMessagesFromResult(result)
	if len(toolMsgs) != 6 {
		t.Fatalf("every tool_call must have a corresponding tool message, got %d", len(toolMsgs))
	}
	for k, m := range toolMsgs {
		if m.Content == "" {
			t.Fatalf("tool message %d has empty content (expected error result)", k)
		}
	}
}

// ═══ T9 超时文案与改造前逐字一致 ═══

func TestRunAgentLoop_ToolTimeoutMessage(t *testing.T) {
	// 注入短超时 seam(默认 180s 太长无法在测试内触发)
	old := defaultToolTimeout
	defaultToolTimeout = 80 * time.Millisecond
	defer func() { defaultToolTimeout = old }()

	// 工具阻塞等待 ctx.Done → DeadlineExceeded → 固定文案
	adapter := tools.NewAdapter("slow_ro", "slow read-only", func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}).WithReadOnly(true)

	mockResponses := []*llm.Response{
		newToolResponse(makeCall("c1", "slow_ro")),
		newTextResponse("done"),
	}
	cfg := ExecutorConfig{
		SystemPrompt:             "test",
		UserInput:                "timeout format",
		Adapters:                 []*tools.Adapter{adapter},
		LLM:                      &mockLLM{responses: mockResponses},
		MaxSteps:                 3,
		AgentName:                "timeout-format",
		MaxParallelReadOnlyTools: 4,
	}

	result, err := RunAgentLoop(context.Background(), cfg)
	if err != nil {
		t.Fatalf("RunAgentLoop: %v", err)
	}

	toolMsgs := toolMessagesFromResult(result)
	want := "Error: tool execution timed out after 0 seconds" // int(80ms/1s)=0,格式与改造前逐字一致
	if toolMsgs[0].Content != want {
		t.Fatalf("timeout message = %q, want %q", toolMsgs[0].Content, want)
	}
}

// ═══ T10 并行度限流:6 读 max=2 → 最大并发=2;max=1 → 严格串行 ═══

func TestRunAgentLoop_ThrottleSixReadOnlyMaxTwo(t *testing.T) {
	var cur, maxCur atomic.Int64
	var ads []*tools.Adapter
	names := []string{"ro_a", "ro_b", "ro_c", "ro_d", "ro_e", "ro_f"}
	for _, n := range names {
		ads = append(ads, newReadOnlyTestAdapter(t, n, 15*time.Millisecond, "ok", &cur, &maxCur, nil))
	}
	var calls []llm.ToolCall
	for i, n := range names {
		calls = append(calls, makeCall(fmt.Sprintf("c%d", i+1), n))
	}
	mockResponses := []*llm.Response{
		newToolResponse(calls...),
		newTextResponse("done"),
	}
	cfg := ExecutorConfig{
		SystemPrompt:             "test",
		UserInput:                "throttle",
		Adapters:                 ads,
		LLM:                      &mockLLM{responses: mockResponses},
		MaxSteps:                 3,
		AgentName:                "throttle",
		MaxParallelReadOnlyTools: 2,
	}

	if _, err := RunAgentLoop(context.Background(), cfg); err != nil {
		t.Fatalf("RunAgentLoop: %v", err)
	}
	expectInt64(t, maxCur.Load(), 2, "max concurrency under throttle 2")
}

func TestRunAgentLoop_KillSwitchMaxOneIsFullySerial(t *testing.T) {
	publisher := &spyPublisher{}
	ids := []string{"c1", "c2", "c3", "c4", "c5", "c6"}
	names := []string{"ro_a", "ro_b", "ro_c", "ro_d", "ro_e", "ro_f"}
	var ads []*tools.Adapter
	for _, n := range names {
		ads = append(ads, newReadOnlyTestAdapter(t, n, 5*time.Millisecond, "ok", nil, nil, nil))
	}
	var calls []llm.ToolCall
	for i, id := range ids {
		calls = append(calls, makeCall(id, names[i]))
	}
	mockResponses := []*llm.Response{
		newToolResponse(calls...),
		newTextResponse("done"),
	}
	cfg := ExecutorConfig{
		SystemPrompt:             "test",
		UserInput:                "kill switch",
		Adapters:                 ads,
		LLM:                      &mockLLM{responses: mockResponses},
		MaxSteps:                 3,
		Publisher:                publisher,
		AgentName:                "kill-switch",
		MaxParallelReadOnlyTools: 1, // 严格串行
	}

	if _, err := RunAgentLoop(context.Background(), cfg); err != nil {
		t.Fatalf("RunAgentLoop: %v", err)
	}

	// 事件流必须严格 [s-c1,x-c1, s-c2,x-c2, ...] 交替且按原序
	ev := publisher.snapshot()
	var seq []string
	for _, e := range ev {
		if e.evType != "tool_call_start" && e.evType != "tool_call_result" {
			continue
		}
		id := ""
		if m, ok := e.content.(map[string]interface{}); ok {
			id, _ = m["tool_call_id"].(string)
		}
		prefix := "s"
		if e.evType == "tool_call_result" {
			prefix = "x"
		}
		seq = append(seq, prefix+"-"+id)
	}
	for k, id := range ids {
		want := []string{"s-" + id, "x-" + id}
		got := []string{seq[2*k], seq[2*k+1]}
		if got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("kill-switch event sequence mismatch at %d: got %v want %v (full=%v)", k, got, want, seq)
		}
	}
}

