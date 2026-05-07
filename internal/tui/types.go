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
