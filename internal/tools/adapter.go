package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"codeactor/internal/llm"
)

// ToolFunc is a function type that matches the tool execution signature
type ToolFunc func(ctx context.Context, params map[string]interface{}) (interface{}, error)

// Adapter wraps a ToolFunc with name, description, and schema for LLM function calling
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

// SortToolDefs sorts tool definitions alphabetically by function name.
// This ensures deterministic ordering for LLM prompt cache stability —
// the same set of tools always produces the same byte sequence.
func SortToolDefs(defs []llm.ToolDef) {
	sort.Slice(defs, func(i, j int) bool {
		return defs[i].Function.Name < defs[j].Function.Name
	})
}

// ComputeToolDefsHash returns a hex-encoded SHA256 hash (first 8 bytes) of the
// sorted tool definitions. Used for logging and verifying prompt cache consistency
// across sessions and after custom agent registration.
func ComputeToolDefsHash(defs []llm.ToolDef) string {
	sorted := make([]llm.ToolDef, len(defs))
	copy(sorted, defs)
	SortToolDefs(sorted)
	data, err := json.Marshal(sorted)
	if err != nil {
		return "error"
	}
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:8])
}

// ToToolDef converts the adapter to an llm.ToolDef definition
func (a *Adapter) ToToolDef() llm.ToolDef {
	return llm.ToolDef{
		Type: "function",
		Function: llm.FunctionDef{
			Name:        a.name,
			Description: a.description,
			Parameters:  a.schema,
		},
	}
}
