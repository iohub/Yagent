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

// TimelineKind 表示时间线条目的种类
type TimelineKind int

const (
	TimelineKindTool    TimelineKind = iota // 工具调用
	TimelineKindLLMCall                     // LLM 调用
	TimelineKindContextEvent                // 上下文事件（压缩、commit加载等）
)

// TimelineEntry 表示时间线面板中的一个执行条目
type TimelineEntry struct {
	ID        string        // 工具调用 ID 或合成 ID
	Kind      TimelineKind  // 条目种类
	Timestamp time.Time     // 事件发生时间
	Status    ToolStatus    // 工具状态
	Name      string        // 名称，如 "read_file", "llm_call", "context_compressed"
	Detail    string        // 详细信息（文件路径、命令摘要等）
	Duration  time.Duration // 执行耗时（完成后设置）
	IsError   bool          // 是否出错
}
