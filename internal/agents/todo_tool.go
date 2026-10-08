// todo_tool.go 提供 TodoWrite 工具的执行函数与适配器构造（方案C·批次1）：
// 将任务计划/进度从「LLM 自由文本 + 正则回读」的隐式信道迁移到
// 「tool call + JSON Schema」的原生结构化信道。
//
// 执行流程（对齐设计文档 5.4 写入链路时序）：
//
//	LLM tool_call(TodoWrite, args)
//	  → 解析 params["todos"] + 手工校验（与 Store 校验一致，先校验后写）
//	  → store.SetTodos(items, step)
//	  → 事件发布 "todo_update"（全量快照 + revision，TUI 无需 diff）
//	  → 返回紧凑摘要文本（tool result）
//
// 工具名固定为 TodoWrite（与 Claude Code 对齐）；语义为全量替换当前任务清单
// 快照（空数组=清空；未列出的条目视为删除）。
// 闭包持有 thinklink.Store 与事件发布器：store 为 nil 时工具返回错误；
// publisher 为 nil 时降级不发布事件。coding 与 director 两侧复用本构造。
package agents

import (
	"context"
	"encoding/json"
	"fmt"

	"yagent/internal/thinklink"
	"yagent/internal/tools"
)

// TodoWriteToolName TodoWrite 工具名（与 Claude Code 对齐）。
const TodoWriteToolName = "TodoWrite"

// TodoWriteToolDescription TodoWrite 工具的兜底描述（正常路径下以 tools.json 为准）。
const TodoWriteToolDescription = "Write the full task list (full replacement semantics): " +
	"each call must provide the complete list; items not listed are treated as deleted; " +
	"an empty array clears the list. Call it when a multi-step task starts, when a step completes, " +
	"or when new work is discovered. Each item requires content (imperative task description), " +
	"active_form (present continuous form shown while in_progress), and status " +
	"(pending | in_progress | completed)."

// TodoUpdateEventType 任务清单更新事件类型：payload 为全量快照（items + revision），
// TUI 无需 diff 逻辑，直接整体刷新。
const TodoUpdateEventType = "todo_update"

// TodoWriteEventSource todo_update 事件的 from 来源标识。
const TodoWriteEventSource = "todo_write"

// NewTodoWriteAdapter 构造 TodoWrite 工具适配器（coding 与 director 两侧复用）。
// description 与 schema 优先取 tools.json 中注册的 TodoWrite 定义（单一 schema 来源），
// 解析失败时回退本文件内的内置兜底。闭包持有 store 与事件发布器（均可 nil 降级）。
func NewTodoWriteAdapter(store *thinklink.Store, publisher EventBus) *tools.Adapter {
	desc, schema := todoWriteDescriptionAndSchema()
	return tools.NewAdapter(TodoWriteToolName, desc, TodoWriteToolFunc(store, publisher)).WithSchema(schema)
}

// TodoWriteToolFunc 返回 TodoWrite 工具执行函数（闭包持有 thinklink 存储与事件发布器）。
//   - 手工校验（与 Store.SetTodos 校验一致，先校验后写，给出可读错误信息）；
//   - store.SetTodos 写入快照（step 暂记 0，真实步数由执行链路接入时注入）；
//   - 发布 "todo_update" 事件（payload：items 完整列表 + revision）；
//   - 返回紧凑摘要文本，如 "Todo list updated: 1 in_progress, 2 pending, 1 completed"
//     （空清单返回 "Todo list cleared"）。
func TodoWriteToolFunc(store *thinklink.Store, publisher EventBus) tools.ToolFunc {
	return func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		if store == nil {
			return nil, fmt.Errorf("TodoWrite is unavailable: thinklink store is not configured")
		}
		items, err := parseTodoItemsParam(params)
		if err != nil {
			return nil, err
		}
		if _, err := store.SetTodos(items, 0); err != nil {
			return nil, err
		}
		publishTodoUpdate(publisher, store, items)
		return buildTodoSummary(items), nil
	}
}

// ─── 参数解析与校验 ──────────────────────────────────────────────────────────

// parseTodoItemsParam 解析并校验 params["todos"]（array of object，
// 字段 content/active_form/status），返回条目列表。
// 校验与 Store.SetTodos 保持一致（单一校验逻辑 thinklink.ValidateTodoItems），
// 先校验后写，错误信息面向 LLM 可读。
func parseTodoItemsParam(params map[string]interface{}) ([]thinklink.TodoItem, error) {
	raw, ok := params["todos"]
	if !ok || raw == nil {
		return nil, fmt.Errorf("todos parameter is required (array of {content, active_form, status} objects); pass an empty array to clear the list")
	}
	rawList, ok := raw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("todos must be an array of {content, active_form, status} objects, got %T", raw)
	}
	items := make([]thinklink.TodoItem, 0, len(rawList))
	for i, r := range rawList {
		obj, ok := r.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("todos[%d] must be an object with content/active_form/status fields, got %T", i, r)
		}
		content, err := todoParamString(obj, "content", i)
		if err != nil {
			return nil, err
		}
		activeForm, err := todoParamString(obj, "active_form", i)
		if err != nil {
			return nil, err
		}
		statusRaw, ok := obj["status"]
		if !ok || statusRaw == nil {
			return nil, fmt.Errorf("todos[%d].status is required (one of %q/%q/%q)", i, thinklink.StatusPending, thinklink.StatusInProgress, thinklink.StatusCompleted)
		}
		status, ok := statusRaw.(string)
		if !ok {
			return nil, fmt.Errorf("todos[%d].status must be a string (one of %q/%q/%q), got %T", i, thinklink.StatusPending, thinklink.StatusInProgress, thinklink.StatusCompleted, statusRaw)
		}
		items = append(items, thinklink.TodoItem{
			Content:    content,
			ActiveForm: activeForm,
			Status:     thinklink.TodoStatus(status),
		})
	}
	if err := thinklink.ValidateTodoItems(items); err != nil {
		return nil, err
	}
	return items, nil
}

// todoParamString 提取并校验 obj[field] 为 string（缺失/类型错误返回可读错误）。
func todoParamString(obj map[string]interface{}, field string, idx int) (string, error) {
	v, ok := obj[field]
	if !ok || v == nil {
		return "", fmt.Errorf("todos[%d].%s is required", idx, field)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("todos[%d].%s must be a string, got %T", idx, field, v)
	}
	return s, nil
}

// ─── 事件发布与结果摘要 ──────────────────────────────────────────────────────

// publishTodoUpdate 发布 "todo_update" 事件：payload 为全量快照
// （items 完整列表 + revision），TUI 无需 diff。publisher 为 nil 时降级不发布。
func publishTodoUpdate(publisher EventBus, store *thinklink.Store, items []thinklink.TodoItem) {
	if publisher == nil {
		return
	}
	payload := map[string]interface{}{
		"items":    todoItemsPayload(items),
		"revision": store.TodoRevision(),
	}
	_ = publisher.Publish(TodoUpdateEventType, payload, TodoWriteEventSource)
}

// todoItemsPayload 将条目列表转为显式 map payload（key 与 JSON tag 名一致，
// 事件载荷完全自描述，TUI 侧无需依赖 thinklink 类型）。
func todoItemsPayload(items []thinklink.TodoItem) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(items))
	for _, it := range items {
		out = append(out, map[string]interface{}{
			"content":     it.Content,
			"active_form": it.ActiveForm,
			"status":      string(it.Status),
		})
	}
	return out
}

// buildTodoSummary 生成紧凑摘要文本（tool result）：
// 非空清单返回 "Todo list updated: N in_progress, N pending, N completed"；
// 空清单返回 "Todo list cleared"。
func buildTodoSummary(items []thinklink.TodoItem) string {
	if len(items) == 0 {
		return "Todo list cleared"
	}
	var inProgress, pending, completed int
	for _, it := range items {
		switch it.Status {
		case thinklink.StatusInProgress:
			inProgress++
		case thinklink.StatusPending:
			pending++
		case thinklink.StatusCompleted:
			completed++
		}
	}
	return fmt.Sprintf("Todo list updated: %d in_progress, %d pending, %d completed", inProgress, pending, completed)
}

// ─── schema 加载 ─────────────────────────────────────────────────────────────

// todoWriteDescriptionAndSchema 从 tools.json 读取 TodoWrite 的描述与 schema；
// 未找到或解析失败时回退内置兜底（单一 schema 来源，保证两侧一致）。
func todoWriteDescriptionAndSchema() (string, map[string]interface{}) {
	var defs []tools.ToolDefinition
	if err := json.Unmarshal(ToolsJSON, &defs); err == nil {
		for i := range defs {
			if defs[i].Name == TodoWriteToolName {
				desc := TodoWriteToolDescription
				if defs[i].Description != "" {
					desc = defs[i].Description
				}
				schema := builtinTodoWriteSchema()
				if defs[i].Parameters != nil {
					schema = defs[i].Parameters
				}
				return desc, schema
			}
		}
	}
	return TodoWriteToolDescription, builtinTodoWriteSchema()
}

// builtinTodoWriteSchema 内置兜底 schema（与 tools.json 中 TodoWrite 定义一致）：
// todos 必填 array，items 要求 {content: string 必填, active_form: string 必填,
// status: 枚举 pending|in_progress|completed 必填}。
func builtinTodoWriteSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"todos": map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"content": map[string]interface{}{
							"type":        "string",
							"description": "The task description in imperative form (required).",
						},
						"active_form": map[string]interface{}{
							"type":        "string",
							"description": "The present continuous form shown while the item is in_progress (required).",
						},
						"status": map[string]interface{}{
							"type":        "string",
							"enum":        []string{"pending", "in_progress", "completed"},
							"description": "Task status (required).",
						},
					},
					"required": []string{"content", "active_form", "status"},
				},
				"description": "The FULL replacement task list. Provide the complete list on every call; items not listed are treated as deleted; an empty array clears the list.",
			},
		},
		"required": []string{"todos"},
	}
}
