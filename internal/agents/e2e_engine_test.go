package agents

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"yagent/internal/llm"
)

// agentScriptedEngine 是 E2E 链路测试的模型替身。
//
// 它按"这是哪一位 Agent 的调用"分派脚本，而不是按全局调用序号 ——
// 全局序号会被循环里的补偿动作打断（纯文本回复触发 NoActionRetryLimit 提醒、
// Director 未委派触发强制委派提醒、agent_exit 之后 Planner 还会再问一轮），
// 于是一个无关的 +1 就让整条脚本错位，测试失败的原因看上去像产品 bug。
//
// 每台 Agent 的脚本用完之后回落到 exhaust（默认一句纯文本），
// 让循环自己收尾，而不是返回 error —— error 会触发 LLM 重试，
// 重试 again 消耗调用次数，把错位放大成超时。
type agentScriptedEngine struct {
	mu      sync.Mutex
	steps   map[string][]scriptStep
	exhaust map[string]func() *llm.Response
	calls   []scriptedCall
}

type scriptStep func(messages []llm.Message, tools []llm.ToolDef) *llm.Response

type scriptedCall struct {
	agent    string
	messages []llm.Message
	tools    []llm.ToolDef
}

// agentIdentity 用各家 System Prompt 的首行措辞认人。
// 这些串是从 prompt.md 里抄的固定开头；prompt 改了这里会失败在"认不出是谁"，
// 而不是悄悄把脚本发给错误的 Agent。
func agentIdentity(messages []llm.Message) string {
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

func newScripted(steps map[string][]scriptStep) *agentScriptedEngine {
	return &agentScriptedEngine{steps: steps}
}

// GenerateContent 先看 ctx：真实引擎在 ctx 取消时会返回错误。
// 忽略 ctx 的 mock 会把"取消没传播"这类缺陷直接掩盖掉 —— 生命周期用例依赖这一点。
func (e *agentScriptedEngine) GenerateContent(ctx context.Context, messages []llm.Message, tools []llm.ToolDef, _ *llm.CallOptions) (*llm.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	e.mu.Lock()
	agent := agentIdentity(messages)
	cp := append([]llm.Message(nil), messages...)
	e.calls = append(e.calls, scriptedCall{agent: agent, messages: cp, tools: tools})

	idx := len(e.callsForLocked(agent)) - 1
	var run func() *llm.Response
	switch list := e.steps[agent]; {
	case idx < len(list):
		run = func() *llm.Response { return list[idx](cp, tools) }
	default:
		if fn, ok := e.exhaust[agent]; ok {
			run = fn
		} else {
			run = func() *llm.Response {
				return textResponse(fmt.Sprintf("[%s] 脚本已用尽，纯文本收尾", agent))
			}
		}
	}
	e.mu.Unlock()

	// 步骤必须在锁外跑：脚本里常要回看引擎的记录（callTextContaining / lastCallFor），
	// 而 sync.Mutex 不可重入 —— 在锁内调用就是自锁死。
	return run(), nil
}

func (e *agentScriptedEngine) Model() string         { return "scripted-mock" }
func (e *agentScriptedEngine) CloseIdleConnections() {}

func (e *agentScriptedEngine) setExhaust(agent string, fn func() *llm.Response) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.exhaust == nil {
		e.exhaust = map[string]func() *llm.Response{}
	}
	e.exhaust[agent] = fn
}

// callsForLocked 假定调用者已持 e.mu。
func (e *agentScriptedEngine) callsForLocked(agent string) []scriptedCall {
	var out []scriptedCall
	for _, c := range e.calls {
		if c.agent == agent {
			out = append(out, c)
		}
	}
	return out
}

// CallsFor 返回该 Agent 的全部调用记录（按发生顺序）。
func (e *agentScriptedEngine) CallsFor(agent string) []scriptedCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.callsForLocked(agent)
}

// lastCallFor 取该 Agent 最近一次调用；没有就报出来，别让用例拿零值继续跑。
func (e *agentScriptedEngine) lastCallFor(t *testing.T, agent string) scriptedCall {
	t.Helper()
	calls := e.CallsFor(agent)
	if len(calls) == 0 {
		t.Fatalf("没有任何一次调用属于 %q（识别到的 Agent：%s）", agent, e.seenAgents())
	}
	return calls[len(calls)-1]
}

func (e *agentScriptedEngine) seenAgents() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	seen := map[string]bool{}
	var order []string
	for _, c := range e.calls {
		if !seen[c.agent] {
			seen[c.agent] = true
			order = append(order, c.agent)
		}
	}
	return strings.Join(order, ",")
}

// callTextContaining 返回第一次"消息里出现 needle"的调用全文。
// 找不到时把全部调用打出来 —— 链路测试失败最常见的形态是"文本没送到"，
// 没有现场的话只能靠猜。
func (e *agentScriptedEngine) callTextContaining(t *testing.T, needle string) string {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, c := range e.calls {
		text := messagesText(c.messages)
		if strings.Contains(text, needle) {
			return text
		}
	}
	var b strings.Builder
	for i, c := range e.calls {
		fmt.Fprintf(&b, "--- 第 %d 次调用（识别为 %s）---\n%s\n", i+1, c.agent, truncateForFailure(messagesText(c.messages)))
	}
	t.Fatalf("没有一次调用包含 %q，实际调用如下：\n%s", needle, b.String())
	return ""
}

func textResponse(content string) *llm.Response {
	return &llm.Response{Choices: []llm.Choice{{Content: content}}}
}

func toolCallStep(tool string, args map[string]interface{}) scriptStep {
	return func([]llm.Message, []llm.ToolDef) *llm.Response {
		b, err := marshalArgs(args)
		if err != nil {
			panic(err)
		}
		return makeToolCallResponse(tool, b)
	}
}

func fixed(resp *llm.Response) scriptStep {
	return func([]llm.Message, []llm.ToolDef) *llm.Response { return resp }
}

// messagesText 把一次调用的全部消息拼成一串，供"这段链路有没有把 X 送到"式断言。
func messagesText(msgs []llm.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(string(m.Role))
		b.WriteByte(':')
		b.WriteString(m.Content)
		b.WriteByte('\n')
	}
	return b.String()
}

func truncateForFailure(s string) string {
	r := []rune(s)
	if len(r) > 1500 {
		return string(r[:1500]) + "…（截断）"
	}
	return s
}
