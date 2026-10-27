package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"unicode/utf8"

	"yagent/internal/llm"
	"yagent/internal/toolspec"
)

// maxFallbackRawLogBytes 是兜底日志里原始入参的字节上限：一次被 max_tokens 截断的
// write_file 参数可能有几十 KB，全量进日志会把这条线索本身埋掉。
const maxFallbackRawLogBytes = 500

// truncateRawForLog 按字节预算截原始入参，并回退到最后一个完整码点：
// 按字节硬切会把中文劈成两半，日志里就是乱码（与 truncateRunes 同一个顾虑）。
func truncateRawForLog(s string) string {
	if len(s) <= maxFallbackRawLogBytes {
		return s
	}
	cut := s[:maxFallbackRawLogBytes]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return fmt.Sprintf("%s…[truncated %d of %d bytes]", cut, len(s)-len(cut), len(s))
}

// ToolFunc is a function type that matches the tool execution signature.
// 别名到 toolspec.ToolFunc：注册表与适配器共用同一个函数类型，
// 否则 Spec.Build 的返回值要在这里做一次手工转换（转换点就是漂移点）。
type ToolFunc = toolspec.ToolFunc

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
	// brief 是目录里的一句话简介。动态工具（外部 MCP）在静态注册表里查不到，
	// catalogBrief 回落找不到 Brief 时只能截英文描述 —— 所以这里留一份。
	brief string
}

func NewAdapter(name, description string, fn ToolFunc) *Adapter {
	// Default schema if none provided: just a string input or generic object
	// For better results, we should provide actual schema.
	// Here we use a generic catch-all schema for simplicity if not provided.
	defaultSchema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"input": map[string]interface{}{
				"type":        "string",
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

// WithBrief 设置目录里的一句话简介（deferred 目录的冷工具行）。
func (a *Adapter) WithBrief(brief string) *Adapter {
	a.brief = brief
	return a
}

// Brief 返回目录简介；空串表示没有单独设置。
func (a *Adapter) Brief() string { return a.brief }

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
	// 步骤 1/4（AGENTS.md 硬约束 9）：JSON 解析。解析失败**不报错**，回落成单参数
	// {"input": 原始串} —— 非 JSON 入参的工具靠这一步活着。
	//
	// 触发条件用 llm.UsableToolArguments 而不是"unmarshal 报错"：字面量 "null" 能
	// unmarshal 成功却得到 nil map，旧写法让它带着一个空参数表往下走，于是必填参数
	// 静默缺失、报错信息完全指不到真相。判据与 Choice.ToolCallNote 共用同一个函数
	// （internal/llm/AGENTS.md 硬约束 13：两侧必须同形）。
	var params map[string]interface{}
	if !llm.UsableToolArguments(input) {
		// 兜底本身按契约保留，但必须留下痕迹：一次"参数整个丢了"的执行如果无声无息，
		// 事后从日志里根本分不出是模型没给参数还是工具没收到参数。
		slog.Warn("tool arguments are not a usable JSON object; falling back to single input param",
			"tool", a.name, "raw_len", len(input), "raw", truncateRawForLog(input))
		params = map[string]interface{}{"input": input}
	} else if err := json.Unmarshal([]byte(input), &params); err != nil {
		// 理论上不可达：UsableToolArguments 刚用同一个输入、同一个 map 类型解成功过。
		// 留着是为了不让兜底依赖"两处解析必然同结果"这个隐含前提。
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
