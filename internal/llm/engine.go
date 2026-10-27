package llm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// Role represents the role of a message in a conversation.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message represents a single message in a conversation.
type Message struct {
	Role             Role              `json:"role"`
	Content          string            `json:"content,omitempty"`
	ToolCalls        []ToolCall        `json:"tool_calls,omitempty"`
	ToolCallID       string            `json:"tool_call_id,omitempty"`
	ToolName         string            `json:"name,omitempty"`
	Reasoning        string            `json:"reasoning_content,omitempty"` // thinking/reasoning from models that support it
	IsAnchored       bool              `json:"-"`                           // 锚定标记，true 表示此消息永不压缩
	TruncationMarker *TruncationMarker `json:"-"`                           // 截断标记，记录工具结果被截断的信息
}

// ToolDef defines a tool available to the LLM.
type ToolDef struct {
	Type     string      `json:"type"` // "function"
	Function FunctionDef `json:"function"`
}

// FunctionDef defines a function tool's signature.
//
// ⚠️ IMPORTANT: The current implementation relies on encoding/json's deterministic
// sorting of map keys (alphabetical order). If migrating to sonic, jsoniter, or
// another JSON library in the future, ensure SortMapKeys is enabled to maintain
// deterministic key ordering and prevent prompt cache fragmentation.
type FunctionDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// ToolCall represents a tool call from the LLM.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // "function"
	Function FunctionCall `json:"function"`
}

// FunctionCall represents the function name and arguments in a tool call.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// TokenUsage contains token usage information returned by the LLM API.
type TokenUsage struct {
	PromptTokens             int64 `json:"prompt_tokens"`
	CompletionTokens         int64 `json:"completion_tokens"`
	TotalTokens              int64 `json:"total_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens,omitempty"`
	// TotalInputTokens 是 provider 口径下的输入 token 总数（含缓存命中部分）。
	// 用于正确计算 cache 命中率：OpenAI 的 prompt_tokens 已包含 cached_tokens，
	// 而 Anthropic 的 input_tokens 不含 cache，需要手动累加。
	TotalInputTokens int64 `json:"total_input_tokens,omitempty"`
}

// Response represents the LLM's response to a GenerateContent call.
type Response struct {
	Choices []Choice    `json:"choices"`
	Usage   *TokenUsage `json:"usage,omitempty"`
}

// Choice represents a single choice in the LLM response.
type Choice struct {
	Content   string     `json:"content,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	Reasoning string     `json:"reasoning_content,omitempty"` // thinking/reasoning content
	// FinishReason 是归一化后的停止原因。没有它的时候（老 provider、被网关吃掉的字段）
	// 是空串，含义是"不知道为什么停"，与 FinishStop（"模型说完了"）必须区分开。
	FinishReason FinishReason `json:"finish_reason,omitempty"`
}

// FinishReason is why the provider stopped generating. The two protocols spell the
// same event differently, so the mapping happens at the engine boundary.
type FinishReason string

const (
	FinishStop          FinishReason = "stop"           // end_turn / stop
	FinishLength        FinishReason = "length"         // hit max_tokens: the turn is incomplete
	FinishToolCalls     FinishReason = "tool_calls"     // tool_use
	FinishContentFilter FinishReason = "content_filter" // refusal
	FinishStopSequence  FinishReason = "stop_sequence"
	FinishPauseTurn     FinishReason = "pause_turn" // anthropic: thinking hit the budget, resume with a follow-up
)

// Truncated reports an output cut off by max_tokens. Unrecognized values read as
// complete: dropping a usable answer costs more than a slightly short one.
func (f FinishReason) Truncated() bool { return f == FinishLength }

// Refused reports that the provider withheld the answer for policy reasons.
func (f FinishReason) Refused() bool { return f == FinishContentFilter }

// normalizeFinishReason folds both vocabularies onto the shared constants — openai
// stop/length/tool_calls/content_filter, anthropic end_turn/max_tokens/tool_use/refusal.
// Unrecognized values stay verbatim: visible in the log, guessed at nowhere.
func normalizeFinishReason(raw string) FinishReason {
	switch strings.ToLower(raw) {
	case "":
		return ""
	case "stop", "end_turn":
		return FinishStop
	case "length", "max_tokens":
		return FinishLength
	case "tool_calls", "function_call", "tool_use":
		return FinishToolCalls
	case "content_filter", "refusal":
		return FinishContentFilter
	case "stop_sequence":
		return FinishStopSequence
	case "pause_turn":
		return FinishPauseTurn
	default:
		return FinishReason(strings.ToLower(raw))
	}
}

// ErrEmptyCompletion is a 200 that carries nothing to act on — the shape of a
// gateway swallowing an upstream error into `{"choices":[]}`.
var ErrEmptyCompletion = errors.New("provider returned no completion choice")

// ErrRefusedCompletion is a policy refusal: not an empty answer, and the loop must
// not report the model's silence as one.
var ErrRefusedCompletion = errors.New("provider refused to answer (content filter)")

// FirstChoice is the only way to read the turn a loop must act on: a nil Response
// and a choiceless 200 are errors here, not an index panic mid-task.
func (r *Response) FirstChoice() (Choice, error) {
	if r == nil || len(r.Choices) == 0 {
		return Choice{}, ErrEmptyCompletion
	}
	c := r.Choices[0]
	if c.FinishReason.Refused() {
		return Choice{}, fmt.Errorf("%w: finish_reason=%q", ErrRefusedCompletion, string(c.FinishReason))
	}
	return c, nil
}

// truncationNotice is appended to an assistant turn the provider cut off: without
// it the model reads its own truncated sentence as a completed one.
const truncationNotice = "\n\n[response truncated: this turn hit max_tokens and the text above is incomplete — continue it, in smaller pieces]"

// toolArgsTruncatedNotice goes into the result of a call whose arguments were cut off.
const toolArgsTruncatedNotice = "\n[arguments incomplete: the previous turn was cut off at max_tokens — re-issue this call with a smaller payload]"

// validateResponse is the engine-boundary gate for every non-error Response: a
// refusal becomes an error, a max_tokens cut gets one visible log line.
func validateResponse(model string, r *Response) error {
	c, err := r.FirstChoice()
	if err != nil {
		return err
	}
	if c.FinishReason.Truncated() {
		slog.Warn("llm response truncated at max_tokens", "model", model,
			"content_chars", len(c.Content), "tool_calls", len(c.ToolCalls))
	}
	return nil
}

// AssistantMessage renders the choice as the assistant turn. Both loops build their
// turn here so the marker lands identically in messages, history, memory and rollout.
func (c Choice) AssistantMessage() Message {
	return Message{
		Role:      RoleAssistant,
		Content:   c.ContentWithNotice(),
		Reasoning: c.Reasoning,
		ToolCalls: c.ToolCalls,
	}
}

// ContentWithNotice is Content plus the truncation marker when the turn was cut off.
// Anything handed back to a model goes through it: an invisible cut reads as finished.
func (c Choice) ContentWithNotice() string {
	if c.FinishReason.Truncated() {
		return c.Content + truncationNotice
	}
	return c.Content
}

// ToolCallNote returns the note for this call's result, or "" when there is nothing
// to explain. The completeness test is the same predicate the tool layer uses to pick
// its raw-string fallback (Adapter.Call → UsableToolArguments): anything not usable
// as an object reaches the tool as a bogus single "input" param, so a cut-off payload
// must be named here too. AGENTS.md 硬约束 13 — 判据只有一份，两侧必须同形。
func (c Choice) ToolCallNote(tc ToolCall) string {
	if !c.FinishReason.Truncated() {
		return ""
	}
	if UsableToolArguments(tc.Function.Arguments) {
		return ""
	}
	return toolArgsTruncatedNotice
}

// CallOptions holds optional parameters for LLM calls.
// 零值表示不覆盖，改用 provider 的同名默认值。
type CallOptions struct {
	MaxTokens     int
	Temperature   float64
	StreamHandler StreamHandler
	// ReasoningEffort overrides the provider-level reasoning effort for this call.
	// Non-empty value ("high" or "max") enables DeepSeek thinking mode.
	ReasoningEffort string
}

// StreamHandler is called for each chunk during streaming.
type StreamHandler func(ctx context.Context, chunk []byte) error

// Engine is the core LLM abstraction.
type Engine interface {
	// GenerateContent sends messages and tools to the LLM and returns the response.
	GenerateContent(ctx context.Context, messages []Message, tools []ToolDef, opts *CallOptions) (*Response, error)

	// Model returns the model name this engine is configured to use.
	Model() string

	// CloseIdleConnections closes idle HTTP connections in the underlying transport.
	// Useful for accelerating connection release during task cancellation.
	CloseIdleConnections()
}

// TruncationMarker 记录工具结果被截断的信息
type TruncationMarker struct {
	ToolName       string // 工具名称，如 "run_bash", "read_file"
	OriginalLen    int    // 原始内容长度（字节）
	OmittedLen     int    // 被省略的字节数
	TruncationPass int    // 截断次数（0=首次截断, 1+=再次截断）
}
