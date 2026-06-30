// Package tui provides tool call and tool result rendering for the Bubble Tea TUI.
package tui

import "time"

// ToolStatus represents the lifecycle state of a tool call.
type ToolStatus int

const (
	ToolStatusPending  ToolStatus = iota // waiting to start
	ToolStatusRunning                    // currently executing
	ToolStatusSuccess                    // completed successfully
	ToolStatusError                      // completed with error
	ToolStatusCanceled                   // user-canceled
)

// ToolCallInfo holds parsed information about a tool call.
type ToolCallInfo struct {
	ID        string
	Name      string
	Arguments string // raw JSON arguments string
	Summary   string // extracted human-readable summary (file path, command, etc.)
}

// ToolResultInfo holds parsed information about a tool result.
type ToolResultInfo struct {
	ToolCallID string
	Name       string
	Content    string // raw result text
	IsError    bool
}

// ToolEntry tracks the complete lifecycle of a single tool call for rendering.
type ToolEntry struct {
	Call      ToolCallInfo
	Result    *ToolResultInfo
	Status    ToolStatus
	Timestamp time.Time

	// Cached rendering
	rendered string
}

// NewToolEntry creates a new ToolEntry in Running state.
func NewToolEntry(call ToolCallInfo) *ToolEntry {
	return &ToolEntry{
		Call:      call,
		Status:    ToolStatusRunning,
		Timestamp: time.Now(),
	}
}

// SetResult updates the entry with a result and sets the appropriate status.
func (e *ToolEntry) SetResult(result ToolResultInfo) {
	e.Result = &result
	if result.IsError {
		e.Status = ToolStatusError
	} else {
		e.Status = ToolStatusSuccess
	}
	e.rendered = "" // invalidate cache
}

// SetCanceled marks the tool as canceled.
func (e *ToolEntry) SetCanceled() {
	e.Status = ToolStatusCanceled
	e.rendered = ""
}

// InvalidateCache clears the cached render.
func (e *ToolEntry) InvalidateCache() {
	e.rendered = ""
}

// Rendered returns the cached render, or empty string if not cached.
func (e *ToolEntry) Rendered() string {
	return e.rendered
}

// SetRendered stores the cached render.
func (e *ToolEntry) SetRendered(r string) {
	e.rendered = r
}

// TimelineKind 表示时间條目目的種類
type TimelineKind int

const (
	TimelineKindTool     TimelineKind = iota // 工具調用
	TimelineKindLLMCall                      // LLM 調用
	TimelineKindThinking                     // Agent 思考内容
	TimelineKindContextEvent                 // 上下文事件（壓縮、commit載等）
)

// TimelineEntry表示時間線面板中的一個執行條目
type TimelineEntry struct {
	ID        string        // 工具調用 ID 或合成 ID
	Kind      TimelineKind  // 條目種類
	Timestamp time.Time     // 事件發生時間
	Status    ToolStatus    // 工具狀態
	Name      string        // 名稱，如 \"read_file\", \"llm_call\", \"context_compressed\"
	Detail    string        // 仔細消息（檔案路徑、命令摘要等）
	Duration  time.Duration // 執行耗時（完成後設定）
	IsError   bool          // 是否出錯
	SubEntries []*TimelineEntry  // 連續相同類型工具調用的子條目
}

// IsMergeableTool判定該工具是否可以被合併到前一條同類條目中。
// 目前只合併 read_file, list_dir, search_by_regex 這三類檔案操作工具。
func IsMergeableTool(name string) bool {
	switch name {
	case "read_file", "list_dir", "search_by_regex", "semantic_search":
		return true
	}
	return false
}

// MergedCount返回合併組的總條目數（自身 + 所有子條目）。
// 如果沒有子條目，返回 1。
func (e *TimelineEntry) MergedCount() int {
	return 1 + len(e.SubEntries)
}

// EffectiveStatus返回合併組的有效狀態。
// 如果沒有子條目，返回自身狀態。
// 如果有子條目：任一子條目為 Running 則整體 Running；
// 任一為 Error 則整體 Error；全部 Success 則整體 Success。
func (e *TimelineEntry) EffectiveStatus() ToolStatus {
	if len(e.SubEntries) == 0 {
		return e.Status
	}
	hasRunning := false
	hasError := false
	allDone := true
	for i := range e.SubEntries {
		sub := e.SubEntries[i]
		if sub.Status == ToolStatusRunning {
			hasRunning = true
		}
		if sub.IsError || sub.Status == ToolStatusError {
			hasError = true
		}
		if sub.Status != ToolStatusSuccess && sub.Status != ToolStatusError {
			allDone = false
		}
	}
	if hasError {
		return ToolStatusError
	}
	if hasRunning {
		return ToolStatusRunning
	}
	if allDone {
		return ToolStatusSuccess
	}
	return e.Status
}
