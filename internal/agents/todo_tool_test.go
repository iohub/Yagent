package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"yagent/internal/thinklink"
)

// ─── 测试替身 ────────────────────────────────────────────────────────────────

// todoMockPublisher EventBus 测试替身：捕获发布的事件供断言。
type todoMockPublisher struct {
	mu     sync.Mutex
	events []todoCapturedEvent
}

type todoCapturedEvent struct {
	eventType string
	content   interface{}
	from      string
}

func (m *todoMockPublisher) Publish(eventType string, content interface{}, from string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, todoCapturedEvent{eventType: eventType, content: content, from: from})
	return nil
}

func (m *todoMockPublisher) PublishWithMetadata(eventType string, content interface{}, from string, _ map[string]interface{}) error {
	return m.Publish(eventType, content, from)
}

// ─── 参数构造辅助 ────────────────────────────────────────────────────────────

// todoItemParam 构造单条 todo 参数（字段名与 JSON tag 一致）。
func todoItemParam(content, activeForm, status string) map[string]interface{} {
	return map[string]interface{}{"content": content, "active_form": activeForm, "status": status}
}

// todoParams 构造 params["todos"]。
func todoParams(todos ...map[string]interface{}) map[string]interface{} {
	list := make([]interface{}, 0, len(todos))
	for _, item := range todos {
		list = append(list, item)
	}
	return map[string]interface{}{"todos": list}
}

// manyValidTodoParams 构造 n 条合法 todo 参数。
func manyValidTodoParams(n int) []map[string]interface{} {
	items := make([]map[string]interface{}, n)
	for i := range items {
		items[i] = todoItemParam(fmt.Sprintf("task %d", i), fmt.Sprintf("doing %d", i), "pending")
	}
	return items
}

// ─── 合法调用：摘要、store 状态、事件 payload 三者一致 ───────────────────────

func TestTodoWriteValidCalls(t *testing.T) {
	cases := []struct {
		name         string
		params       map[string]interface{}
		wantSummary  string
		wantItems    int
		wantStatuses map[string]int // status → 计数
	}{
		{
			name:         "single pending",
			params:       todoParams(todoItemParam("task A", "doing A", "pending")),
			wantSummary:  "Todo list updated: 0 in_progress, 1 pending, 0 completed",
			wantItems:    1,
			wantStatuses: map[string]int{"pending": 1},
		},
		{
			name: "mixed statuses",
			params: todoParams(
				todoItemParam("task A", "doing A", "completed"),
				todoItemParam("task B", "doing B", "in_progress"),
				todoItemParam("task C", "doing C", "pending"),
				todoItemParam("task D", "doing D", "pending"),
			),
			wantSummary:  "Todo list updated: 1 in_progress, 2 pending, 1 completed",
			wantItems:    4,
			wantStatuses: map[string]int{"completed": 1, "in_progress": 1, "pending": 2},
		},
		{
			name:         "empty array clears",
			params:       todoParams(),
			wantSummary:  "Todo list cleared",
			wantItems:    0,
			wantStatuses: map[string]int{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := thinklink.NewStore(0)
			pub := &todoMockPublisher{}
			fn := TodoWriteToolFunc(store, pub)

			res, err := fn(context.Background(), tc.params)
			if err != nil {
				t.Fatalf("valid call must succeed, got error: %v", err)
			}
			summary, ok := res.(string)
			if !ok {
				t.Fatalf("result must be a string, got %T", res)
			}
			if summary != tc.wantSummary {
				t.Fatalf("summary = %q, want %q", summary, tc.wantSummary)
			}

			// store 状态与摘要一致
			cur := store.CurrentTodos()
			if len(cur) != tc.wantItems {
				t.Fatalf("CurrentTodos = %d items, want %d", len(cur), tc.wantItems)
			}
			counts := map[string]int{}
			for _, it := range cur {
				counts[string(it.Status)]++
			}
			for status, want := range tc.wantStatuses {
				if counts[status] != want {
					t.Fatalf("status %q count = %d, want %d (all: %v)", status, counts[status], want, counts)
				}
			}

			// 事件 payload（items + revision）
			pub.mu.Lock()
			events := append([]todoCapturedEvent(nil), pub.events...)
			pub.mu.Unlock()
			if len(events) != 1 {
				t.Fatalf("exactly 1 event must be published, got %d", len(events))
			}
			ev := events[0]
			if ev.eventType != "todo_update" {
				t.Fatalf("event type = %q, want %q", ev.eventType, "todo_update")
			}
			if ev.from != "todo_write" {
				t.Fatalf("event from = %q, want %q", ev.from, "todo_write")
			}
			payload, ok := ev.content.(map[string]interface{})
			if !ok {
				t.Fatalf("event payload must be map[string]interface{}, got %T", ev.content)
			}
			items, ok := payload["items"].([]map[string]interface{})
			if !ok {
				t.Fatalf("payload.items must be []map[string]interface{}, got %T", payload["items"])
			}
			if len(items) != tc.wantItems {
				t.Fatalf("payload.items = %d entries, want %d", len(items), tc.wantItems)
			}
			for _, item := range items {
				for _, key := range []string{"content", "active_form", "status"} {
					if _, ok := item[key]; !ok {
						t.Fatalf("payload item must expose %q key, got %v", key, item)
					}
				}
			}
			rev, ok := payload["revision"].(int)
			if !ok {
				t.Fatalf("payload.revision must be int, got %T", payload["revision"])
			}
			if rev != 1 {
				t.Fatalf("payload.revision = %d, want 1", rev)
			}
		})
	}
}

// ─── 非法调用：确定性校验，先校验后写 ────────────────────────────────────────

func TestTodoWriteInvalidCalls(t *testing.T) {
	cases := []struct {
		name            string
		params          map[string]interface{}
		wantErrContains string
	}{
		{
			name:            "missing todos param",
			params:          map[string]interface{}{},
			wantErrContains: "todos parameter is required",
		},
		{
			name:            "nil todos param",
			params:          map[string]interface{}{"todos": nil},
			wantErrContains: "todos parameter is required",
		},
		{
			name:            "todos not an array",
			params:          map[string]interface{}{"todos": "oops"},
			wantErrContains: "todos must be an array",
		},
		{
			name:            "invalid status",
			params:          todoParams(todoItemParam("task A", "doing A", "done")),
			wantErrContains: "status must be one of",
		},
		{
			name: "two in_progress",
			params: todoParams(
				todoItemParam("task A", "doing A", "in_progress"),
				todoItemParam("task B", "doing B", "in_progress"),
			),
			wantErrContains: "at most 1 todo item can be",
		},
		{
			name:            "blank content",
			params:          todoParams(todoItemParam("   ", "doing A", "pending")),
			wantErrContains: "content must be a non-empty string",
		},
		{
			name:            "blank active form",
			params:          todoParams(todoItemParam("task A", "  ", "pending")),
			wantErrContains: "active_form must be a non-empty string",
		},
		{
			name:            "missing content field",
			params:          todoParams(map[string]interface{}{"active_form": "doing", "status": "pending"}),
			wantErrContains: "todos[0].content is required",
		},
		{
			name:            "missing status field",
			params:          todoParams(map[string]interface{}{"content": "task", "active_form": "doing"}),
			wantErrContains: "todos[0].status is required",
		},
		{
			name:            "content not a string",
			params:          todoParams(map[string]interface{}{"content": 42, "active_form": "doing", "status": "pending"}),
			wantErrContains: "todos[0].content must be a string",
		},
		{
			name:            "too many items",
			params:          todoParams(manyValidTodoParams(thinklink.MaxTodoItems + 1)...),
			wantErrContains: fmt.Sprintf("at most %d items", thinklink.MaxTodoItems),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := thinklink.NewStore(0)
			fn := TodoWriteToolFunc(store, nil)

			res, err := fn(context.Background(), tc.params)
			if err == nil {
				t.Fatalf("expected error, got result %v", res)
			}
			if !strings.Contains(err.Error(), tc.wantErrContains) {
				t.Fatalf("error %q must contain %q", err.Error(), tc.wantErrContains)
			}
			// 先校验后写：非法输入不产生任何写入
			if store.Len() != 0 {
				t.Fatalf("invalid input must not write to store, entries = %d", store.Len())
			}
		})
	}
}

// ─── 降级路径：publisher 为 nil 不 panic；store 为 nil 返回错误 ──────────────

func TestTodoWriteNilPublisherNoPanic(t *testing.T) {
	store := thinklink.NewStore(0)
	fn := TodoWriteToolFunc(store, nil)
	res, err := fn(context.Background(), todoParams(todoItemParam("task A", "doing A", "pending")))
	if err != nil {
		t.Fatalf("nil publisher must degrade silently, got error: %v", err)
	}
	if res != "Todo list updated: 0 in_progress, 1 pending, 0 completed" {
		t.Fatalf("summary = %v", res)
	}
	if len(store.CurrentTodos()) != 1 {
		t.Fatalf("store must still be updated, got %d items", len(store.CurrentTodos()))
	}
}

func TestTodoWriteNilStoreReturnsError(t *testing.T) {
	fn := TodoWriteToolFunc(nil, &todoMockPublisher{})
	if _, err := fn(context.Background(), todoParams(todoItemParam("task A", "doing A", "pending"))); err == nil {
		t.Fatal("nil store must return an error")
	}
}

// ─── 多次调用：store 快照、台账、事件 revision 一致性（集成冒烟） ────────────

func TestTodoWriteStoreEventConsistency(t *testing.T) {
	store := thinklink.NewStore(0)
	pub := &todoMockPublisher{}
	fn := TodoWriteToolFunc(store, pub)

	// 第一次：A pending、B in_progress
	if _, err := fn(context.Background(), todoParams(
		todoItemParam("task A", "doing A", "pending"),
		todoItemParam("task B", "doing B", "in_progress"),
	)); err != nil {
		t.Fatalf("call 1 failed: %v", err)
	}
	// 第二次：A completed（pending → completed，台账迁移）
	if _, err := fn(context.Background(), todoParams(
		todoItemParam("task A", "doing A", "completed"),
		todoItemParam("task B", "doing B", "in_progress"),
	)); err != nil {
		t.Fatalf("call 2 failed: %v", err)
	}

	// 台账：A 由非 completed 变 completed 迁移
	ledger := store.CompletedLedger()
	if len(ledger) != 1 || ledger[0].Content != "task A" {
		t.Fatalf("ledger = %+v, want [task A]", ledger)
	}
	// 当前快照：A completed
	cur := store.CurrentTodos()
	if len(cur) != 2 || cur[0].Status != thinklink.StatusCompleted {
		t.Fatalf("CurrentTodos = %+v", cur)
	}
	// 事件：两次发布，revision 递增 1→2
	pub.mu.Lock()
	defer pub.mu.Unlock()
	if len(pub.events) != 2 {
		t.Fatalf("2 events must be published, got %d", len(pub.events))
	}
	first, ok := pub.events[0].content.(map[string]interface{})
	if !ok {
		t.Fatalf("event payload must be map, got %T", pub.events[0].content)
	}
	second, ok := pub.events[1].content.(map[string]interface{})
	if !ok {
		t.Fatalf("event payload must be map, got %T", pub.events[1].content)
	}
	if got := first["revision"].(int); got != 1 {
		t.Fatalf("first event revision = %d, want 1", got)
	}
	if got := second["revision"].(int); got != 2 {
		t.Fatalf("second event revision = %d, want 2", got)
	}
}

// ─── coding 侧注册路径：lookupToolFunc 的 TodoWrite 分支 ─────────────────────

func TestLookupToolFuncTodoWrite(t *testing.T) {
	store := thinklink.NewStore(0)
	// 非相关依赖传 nil（TodoWrite 分支只使用 todoStore 与 publisher）
	fn := lookupToolFunc("TodoWrite", nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, store, nil)
	if fn == nil {
		t.Fatal("lookupToolFunc(TodoWrite) must return a non-nil ToolFunc")
	}
	// 经返回的 fn 执行一次完整调用，验证闭包持有 store
	res, err := fn(context.Background(), todoParams(todoItemParam("task A", "doing A", "pending")))
	if err != nil {
		t.Fatalf("TodoWrite via lookupToolFunc failed: %v", err)
	}
	if res != "Todo list updated: 0 in_progress, 1 pending, 0 completed" {
		t.Fatalf("summary = %v", res)
	}
	if len(store.CurrentTodos()) != 1 {
		t.Fatalf("store must be updated via closure, got %d items", len(store.CurrentTodos()))
	}
}

// ─── NewTodoWriteAdapter：名称、schema、Call JSON 编码路径 ───────────────────

func TestNewTodoWriteAdapter(t *testing.T) {
	store := thinklink.NewStore(0)
	pub := &todoMockPublisher{}
	adapter := NewTodoWriteAdapter(store, pub)

	if adapter.Name() != "TodoWrite" {
		t.Fatalf("adapter name = %q, want %q", adapter.Name(), "TodoWrite")
	}
	// schema 来自 tools.json（单一来源）：必填 todos，items 三字段
	td := adapter.ToToolDef()
	if td.Function.Name != "TodoWrite" {
		t.Fatalf("tool def name = %q", td.Function.Name)
	}
	schema := td.Function.Parameters
	if schema == nil {
		t.Fatal("schema must not be nil")
	}
	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("schema.properties must be map, got %T", schema["properties"])
	}
	todos, ok := props["todos"].(map[string]interface{})
	if !ok {
		t.Fatalf("schema must define todos, got %T", props["todos"])
	}
	if _, ok := todos["items"]; !ok {
		t.Fatal("todos schema must define items")
	}
	req, ok := schema["required"].([]interface{})
	if !ok {
		t.Fatalf("schema.required must be []interface{} (json.Unmarshal), got %T", schema["required"])
	}
	if len(req) != 1 || req[0] != "todos" {
		t.Fatalf("schema required = %v, want [todos]", req)
	}

	// Adapter.Call 走 JSON 编码路径（tool result 为 JSON 字符串）
	paramsJSON := `{"todos":[{"content":"task A","active_form":"doing A","status":"pending"}]}`
	out, err := adapter.Call(context.Background(), paramsJSON)
	if err != nil {
		t.Fatalf("adapter.Call failed: %v", err)
	}
	var summary string
	if err := json.Unmarshal([]byte(out), &summary); err != nil {
		t.Fatalf("tool result must be a JSON-encoded string, got %q", out)
	}
	if summary != "Todo list updated: 0 in_progress, 1 pending, 0 completed" {
		t.Fatalf("summary = %q", summary)
	}
	if len(store.CurrentTodos()) != 1 {
		t.Fatalf("store must be updated via adapter, got %d items", len(store.CurrentTodos()))
	}
	pub.mu.Lock()
	defer pub.mu.Unlock()
	if len(pub.events) != 1 {
		t.Fatalf("1 event must be published via adapter, got %d", len(pub.events))
	}
}
