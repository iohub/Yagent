// Package scripted 是确定性的 llm.Engine 替身：按"这是哪一位 Agent 的调用"回放预设响应。
//
// 它从 internal/agents 的 e2e 测试里提升出来（ADR-12），因为 5a 结构夹具必须在**生产路径**
// 上跑（在线编译、`yagent plasma test`），而测试文件里的类型生产用不了。测试与生产共用同一个
// 载体是硬约束：两份实现各自漂走的后果是"八条用例全绿而生产一次都没跑过"（坑 33）。
//
// 三条纪律逐字保留自原实现：
//   - 按 Agent 身份分派而不是全局调用序号（序号会被循环里的补偿动作打断）；
//   - GenerateContent **先看 ctx**（忽略 ctx 的 mock 会掩盖"取消没传播"）；
//   - 脚本步骤在锁外执行（脚本常要回看引擎的调用记录，sync.Mutex 不可重入，锁内回调=自锁死）。
package scripted

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"yagent/internal/llm"
)

// Step 产出一条响应；入参是本次调用的消息与工具声明，便于脚本按内容分支。
type Step func(messages []llm.Message, tools []llm.ToolDef) *llm.Response

// Call 是一次被记录的调用。
type Call struct {
	Agent    string
	Messages []llm.Message
	Tools    []llm.ToolDef
}

// Engine 实现 llm.Engine。
type Engine struct {
	mu       sync.Mutex
	steps    map[string][]Step
	exhaust  map[string]func() *llm.Response
	calls    []Call
	fixed    string
	resolver func([]llm.Message) string
}

// New 用默认的身份解析器（按 System Prompt 首行措辞嗅探内建 Agent）。
func New(steps map[string][]Step) *Engine {
	return &Engine{steps: steps, resolver: sniffIdentity}
}

// NewWithResolver 注入自定义身份解析器（e2e 保留嗅探；需要别名的场景可覆盖）。
func NewWithResolver(resolver func([]llm.Message) string, steps map[string][]Step) *Engine {
	return &Engine{steps: steps, resolver: resolver}
}

// NewForIdentity 构造**单一 Agent** 的引擎：5a 结构夹具只需要一个固定身份，
// 生成出来的 prompt 首行不可预测，嗅探必然落到 unknown。
func NewForIdentity(identity string, steps []Step) *Engine {
	return &Engine{
		steps: map[string][]Step{identity: steps},
		fixed: identity,
	}
}

// GenerateContent 先看 ctx，再按身份分派。身份优先级：固定身份 > CallOptions.AgentName
// 归一化后命中的脚本 > 解析器嗅探 > "unknown"。
func (e *Engine) GenerateContent(ctx context.Context, messages []llm.Message, tools []llm.ToolDef, opts *llm.CallOptions) (*llm.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	e.mu.Lock()
	agent := e.identityLocked(messages, opts)
	cp := append([]llm.Message(nil), messages...)
	e.calls = append(e.calls, Call{Agent: agent, Messages: cp, Tools: tools})

	idx := len(e.callsForLocked(agent)) - 1
	var run func() *llm.Response
	switch list := e.steps[agent]; {
	case idx < len(list):
		step := list[idx]
		run = func() *llm.Response { return step(cp, tools) }
	default:
		if fn, ok := e.exhaust[agent]; ok {
			run = fn
		} else {
			run = func() *llm.Response {
				return TextResponse(fmt.Sprintf("[%s] 脚本已用尽，纯文本收尾", agent))
			}
		}
	}
	e.mu.Unlock()

	// 步骤必须在锁外跑：脚本常要回看引擎的记录，而 sync.Mutex 不可重入。
	return run(), nil
}

func (e *Engine) identityLocked(messages []llm.Message, opts *llm.CallOptions) string {
	if e.fixed != "" {
		return e.fixed
	}
	if opts != nil && opts.AgentName != "" {
		if n := NormalizeIdentity(opts.AgentName); e.hasScript(n) {
			return n
		}
	}
	if e.resolver != nil {
		return e.resolver(messages)
	}
	return "unknown"
}

func (e *Engine) hasScript(agent string) bool {
	if len(e.steps[agent]) > 0 {
		return true
	}
	_, ok := e.exhaust[agent]
	return ok
}

func (e *Engine) Model() string         { return "scripted-mock" }
func (e *Engine) CloseIdleConnections() {}

// SetExhaust 设置脚本耗尽后的回落（默认一句纯文本让循环自己收尾，而不是返回 error ——
// error 会触发 LLM 重试，把错位放大成超时）。
func (e *Engine) SetExhaust(agent string, fn func() *llm.Response) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.exhaust == nil {
		e.exhaust = map[string]func() *llm.Response{}
	}
	e.exhaust[agent] = fn
}

// CallsFor 返回该 Agent 的全部调用记录（按发生顺序）。
func (e *Engine) CallsFor(agent string) []Call {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.callsForLocked(agent)
}

// Calls 返回全部调用记录（按发生顺序）。
func (e *Engine) Calls() []Call {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Call(nil), e.calls...)
}

// SeenAgents 按首次出现顺序返回识别到的身份，供失败现场用。
func (e *Engine) SeenAgents() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	seen := map[string]bool{}
	var order []string
	for _, c := range e.calls {
		if !seen[c.Agent] {
			seen[c.Agent] = true
			order = append(order, c.Agent)
		}
	}
	return order
}

func (e *Engine) callsForLocked(agent string) []Call {
	var out []Call
	for _, c := range e.calls {
		if c.Agent == agent {
			out = append(out, c)
		}
	}
	return out
}

// NormalizeIdentity 把 ExecutorConfig.AgentName（显示名，如 "Repo-Agent"）归一成身份键
// （"repo"）。两者不是一回事：身份键用于脚本分派，显示名用于日志与 UI。
func NormalizeIdentity(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	n = strings.TrimSuffix(n, "-agent")
	n = strings.TrimSpace(n)
	return n
}

// sniffIdentity 用各家 System Prompt 的首行措辞认人。这些串是从 prompt.md 抄的固定开头；
// prompt 改了这里会失败在"认不出是谁"，而不是悄悄把脚本发给错误的 Agent。
func sniffIdentity(messages []llm.Message) string {
	if len(messages) == 0 {
		return "unknown"
	}
	head := messages[0].Content
	switch {
	case strings.Contains(head, "You are the **Director**"):
		return "director"
	case strings.Contains(head, "You are the Repo-Agent"):
		return "repo"
	case strings.Contains(head, "Autonomous software engineering agent"):
		return "coding"
	case strings.Contains(head, "You are the Chat"):
		return "chat"
	default:
		return "unknown"
	}
}

// TextResponse 构造一条纯文本响应。
func TextResponse(content string) *llm.Response {
	return &llm.Response{Choices: []llm.Choice{{Content: content}}}
}

// Fixed 是一个恒定返回同一响应的步骤。
func Fixed(resp *llm.Response) Step {
	return func([]llm.Message, []llm.ToolDef) *llm.Response { return resp }
}

// ToolCall 构造一个"调用 tool 并带 args"的步骤。args 序列化失败时 panic ——
// 这只发生在测试夹具写错时，静默返回空响应会让用例以误导的方式失败。
func ToolCall(tool string, args map[string]interface{}) Step {
	return func([]llm.Message, []llm.ToolDef) *llm.Response {
		raw, err := json.Marshal(args)
		if err != nil {
			panic(fmt.Sprintf("scripted.ToolCall(%s): %v", tool, err))
		}
		return &llm.Response{Choices: []llm.Choice{{
			ToolCalls: []llm.ToolCall{{
				// ID 与既有的 makeToolCallResponse 逐字一致（call_<tool>）：配对修复按
				// last-wins 处理重复 ID，改格式会让一批链路用例以误导的方式失败。
				ID:   "call_" + tool,
				Type: "function",
				Function: llm.FunctionCall{
					Name:      tool,
					Arguments: string(raw),
				},
			}},
		}}}
	}
}

// MessagesText 把一次调用的全部消息拼成一串，供"这段链路有没有把 X 送到"式断言。
func MessagesText(msgs []llm.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(string(m.Role))
		b.WriteByte(':')
		b.WriteString(m.Content)
		b.WriteByte('\n')
	}
	return b.String()
}

// TruncateForFailure 截断失败现场，避免把整段对话倒进测试输出。
func TruncateForFailure(s string) string {
	r := []rune(s)
	if len(r) > 1500 {
		return string(r[:1500]) + "…（截断）"
	}
	return s
}
