package util

import (
	"context"
	"fmt"
	"runtime"
	"time"
)

// CallStack represents a function call stack frame
type CallStack struct {
	Function string `json:"function"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Time     string `json:"time"`
}

// ErrorWithContext represents an error with additional context and call stack
type ErrorWithContext struct {
	Err       error       `json:"error"`
	Message   string      `json:"message"`
	Context   string      `json:"context"`
	CallStack []CallStack `json:"call_stack"`
	Time      time.Time   `json:"time"`
}

// Error returns the error message with context
func (e *ErrorWithContext) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("%s: %v", e.Message, e.Err)
	}
	return e.Err.Error()
}

// Unwrap returns the underlying error
func (e *ErrorWithContext) Unwrap() error {
	return e.Err
}

// GetCallStack captures the current call stack
func GetCallStack(skip int) []CallStack {
	var stack []CallStack
	for i := skip; ; i++ {
		pc, file, line, ok := runtime.Caller(i)
		if !ok {
			break
		}

		// Get function name
		fn := runtime.FuncForPC(pc)
		var funcName string
		if fn != nil {
			funcName = fn.Name()
		} else {
			funcName = "unknown"
		}

		stack = append(stack, CallStack{
			Function: funcName,
			File:     file,
			Line:     line,
			Time:     time.Now().Format(time.RFC3339),
		})
	}
	return stack
}

// NewErrorWithContext creates a new error with context and call stack
func NewErrorWithContext(ctx context.Context, err error, message string) *ErrorWithContext {
	contextStr := "unknown"
	if ctx != nil {
		if deadline, ok := ctx.Deadline(); ok {
			contextStr = fmt.Sprintf("deadline: %v", deadline)
		} else {
			contextStr = "no deadline"
		}
	}

	return &ErrorWithContext{
		Err:       err,
		Message:   message,
		Context:   contextStr,
		CallStack: GetCallStack(2), // Skip this function and the caller
		Time:      time.Now(),
	}
}

// WrapError wraps an error with context if it's not already wrapped
func WrapError(ctx context.Context, err error, message string) error {
	if err == nil {
		return nil
	}

	// Check if it's already our custom error type
	if _, ok := err.(*ErrorWithContext); ok {
		return err
	}

	return NewErrorWithContext(ctx, err, message)
}
