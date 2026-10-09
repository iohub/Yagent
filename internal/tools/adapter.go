package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"yagent/internal/llm"
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
	// readOnly 只读/无副作用元数据：声明该工具不修改任何共享状态（文件、进程、
	// 网络、注册表等），可以安全地与其他只读工具并发执行。零值 false（fail-safe：
	// 未显式声明的工具一律视为可能存在副作用，不可参与并发）。
	readOnly bool
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

// WithReadOnly 显式设置只读/无副作用元数据（链式选项，风格对齐 WithSchema；零值 false）。
// readOnly=true 表示该工具无副作用、可安全并发；错误标记具有副作用的工具会导致
// 并发执行的竞态，标记前必须逐个确认其实现语义。
func (a *Adapter) WithReadOnly(enabled bool) *Adapter {
	a.readOnly = enabled
	return a
}

// knownReadOnlyTools 公共共享只读工具白名单（按名自动标记；第一阶段集合，
// 后续阶段可扩展 delegate_*/其他无副作用工具）。
var knownReadOnlyTools = map[string]bool{
	"read_file":           true,
	"list_dir":            true,
	"print_dir_tree":      true,
	"search_by_regex":     true,
	"semantic_search":     true,
	"query_code_skeleton": true,
	"query_code_snippet":  true,
}

// IsKnownReadOnlyTool 报告给定工具名是否属于已知公共只读工具白名单。
func IsKnownReadOnlyTool(name string) bool {
	return knownReadOnlyTools[name]
}

// WithReadOnlyIfKnown 按工具名自动标记已知只读工具（供 tools.json 批量注册点
// 统一调用：名字命中白名单 → WithReadOnly(true)，否则保持默认 false）。
func (a *Adapter) WithReadOnlyIfKnown() *Adapter {
	if knownReadOnlyTools[a.name] {
		a.readOnly = true
	}
	return a
}

// IsReadOnly 返回只读/无副作用元数据。false=不可并行（未声明或显式声明有副作用）。
func (a *Adapter) IsReadOnly() bool {
	return a.readOnly
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
