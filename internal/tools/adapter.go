package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tmc/langchaingo/llms"
)

// ToolFunc is a function type that matches the tool execution signature
type ToolFunc func(ctx context.Context, params map[string]interface{}) (interface{}, error)

// Adapter wraps a function to implement the langchaingo tools.Tool interface
type Adapter struct {
	name        string
	description string
	fn          ToolFunc
	schema      map[string]interface{}
	guard       *WorkspaceGuard
}

func NewAdapter(name, description string, fn ToolFunc) *Adapter {
	// Default schema if none provided: just a string input or generic object
	// For better results, we should provide actual schema.
	// Here we use a generic catch-all schema for simplicity if not provided.
	defaultSchema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"input": map[string]interface{}{
				"type": "string",
				"description": "Input for the tool",
			},
		},
	}
	
	return &Adapter{
		name:        name,
		description: description,
		fn:          fn,
		schema:      defaultSchema,
	}
}

// WithSchema allows setting a custom schema
func (a *Adapter) WithSchema(schema map[string]interface{}) *Adapter {
	a.schema = schema
	return a
}

func (a *Adapter) Name() string {
	return a.name
}

func (a *Adapter) Description() string {
	return a.description
}

// SetGuard sets the workspace guard for this adapter.
func (a *Adapter) SetGuard(guard *WorkspaceGuard) {
	a.guard = guard
}

func (a *Adapter) Call(ctx context.Context, input string) (string, error) {
	var params map[string]interface{}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		// Try to treat as single "input" param if JSON fails
		params = map[string]interface{}{"input": input}
	}

	// Check workspace guard before executing dangerous operations
	if a.guard != nil {
		needsAuth, reason := a.guard.Check(a.name, params)
		if needsAuth {
			if err := a.guard.RequestAuth(ctx, a.name, reason); err != nil {
				return "", err
			}
		}
	}

	result, err := a.fn(ctx, params)
	if err != nil {
		return "", err
	}

	resBytes, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("failed to marshal result: %v", err)
	}
	return string(resBytes), nil
}

// SetGuardOnAdapters sets the workspace guard on a slice of adapters.
func SetGuardOnAdapters(adapters []*Adapter, guard *WorkspaceGuard) {
	for _, ad := range adapters {
		ad.SetGuard(guard)
	}
}

// ToLLMSTool converts the adapter to an llms.Tool definition
func (a *Adapter) ToLLMSTool() llms.Tool {
	return llms.Tool{
		Type: "function",
		Function: &llms.FunctionDefinition{
			Name:        a.name,
			Description: a.description,
			Parameters:  a.schema,
		},
	}
}
