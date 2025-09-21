package util

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
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

// LogErrorWithContext logs an error with full context and call stack
func LogErrorWithContext(ctx context.Context, err error, message string) {
	errWithCtx := NewErrorWithContext(ctx, err, message)

	// Log the error with structured logging
	logger := log.Error().
		Err(err).
		Str("message", message).
		Str("context", errWithCtx.Context).
		Time("time", errWithCtx.Time)

	// Add call stack as structured data
	for i, frame := range errWithCtx.CallStack {
		logger = logger.Str(fmt.Sprintf("stack_%d_function", i), frame.Function).
			Str(fmt.Sprintf("stack_%d_file", i), frame.File).
			Int(fmt.Sprintf("stack_%d_line", i), frame.Line).
			Str(fmt.Sprintf("stack_%d_time", i), frame.Time)
	}

	logger.Msg("Error occurred with context")

	// Also print a human-readable backtrace
	printBacktrace(errWithCtx)
}

// printBacktrace prints a human-readable backtrace
func printBacktrace(err *ErrorWithContext) {
	fmt.Printf("\n=== ERROR BACKTRACE ===\n")
	fmt.Printf("Time: %s\n", err.Time.Format(time.RFC3339))
	fmt.Printf("Message: %s\n", err.Message)
	fmt.Printf("Context: %s\n", err.Context)
	fmt.Printf("Error: %v\n", err.Err)
	fmt.Printf("\nCall Stack:\n")

	for i, frame := range err.CallStack {
		// Extract just the filename from the full path
		parts := strings.Split(frame.File, "/")
		filename := frame.File
		if len(parts) > 0 {
			filename = parts[len(parts)-1]
		}

		fmt.Printf("  %d. %s\n", i+1, frame.Function)
		fmt.Printf("      at %s:%d\n", filename, frame.Line)
		fmt.Printf("      time: %s\n", frame.Time)
	}
	fmt.Printf("=== END BACKTRACE ===\n\n")
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

// ExecuteWithContext executes a function with context and error handling
func ExecuteWithContext(ctx context.Context, fn func(context.Context) error, operation string) error {
	if ctx == nil {
		ctx = context.Background()
	}

	err := fn(ctx)
	if err != nil {
		LogErrorWithContext(ctx, err, operation)
		return WrapError(ctx, err, operation)
	}

	return nil
}

// ExecuteWithContextAndResult executes a function with context and returns both result and error
func ExecuteWithContextAndResult[T any](ctx context.Context, fn func(context.Context) (T, error), operation string) (T, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	result, err := fn(ctx)
	if err != nil {
		LogErrorWithContext(ctx, err, operation)
		return result, WrapError(ctx, err, operation)
	}

	return result, nil
}
