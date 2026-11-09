package agents

import (
	"fmt"
	"strings"
	"testing"

	"yagent/internal/llm"
	"yagent/internal/llm/scripted"
)

// 脚本引擎已提升到 internal/llm/scripted（ADR-12）：5a 结构夹具必须在**生产路径**上跑，
// 而测试文件里的类型生产用不了。测试与生产共用同一个载体（坑 33），所以本文件只剩
// 类型别名与"接受 *testing.T 的取值助手"这层薄壳 —— 实现一份，没有第二份可漂。
//
// 别名不是"新旧并存"：scripted.Engine 就是唯一实现，下面这些名字只是既有调用点的本地叫法。

type (
	scriptStep          = scripted.Step
	scriptedCall        = scripted.Call
	agentScriptedEngine = scripted.Engine
)

func newScripted(steps map[string][]scriptStep) *agentScriptedEngine {
	return scripted.New(steps)
}

func textResponse(content string) *llm.Response { return scripted.TextResponse(content) }

func fixed(resp *llm.Response) scriptStep { return scripted.Fixed(resp) }

func toolCallStep(tool string, args map[string]interface{}) scriptStep {
	return scripted.ToolCall(tool, args)
}

func messagesText(msgs []llm.Message) string { return scripted.MessagesText(msgs) }

func truncateForFailure(s string) string { return scripted.TruncateForFailure(s) }

// lastCallFor 取该 Agent 最近一次调用；没有就报出来，别让用例拿零值继续跑。
func lastCallFor(t *testing.T, e *agentScriptedEngine, agent string) scriptedCall {
	t.Helper()
	calls := e.CallsFor(agent)
	if len(calls) == 0 {
		t.Fatalf("没有任何一次调用属于 %q（识别到的 Agent：%s）", agent, seenAgents(e))
	}
	return calls[len(calls)-1]
}

// callTextContaining 返回第一次"消息里出现 needle"的调用全文。
// 找不到时把全部调用打出来 —— 链路测试失败最常见的形态是"文本没送到"。
func callTextContaining(t *testing.T, e *agentScriptedEngine, needle string) string {
	t.Helper()
	for _, c := range e.Calls() {
		text := messagesText(c.Messages)
		if strings.Contains(text, needle) {
			return text
		}
	}
	var b strings.Builder
	for i, c := range e.Calls() {
		fmt.Fprintf(&b, "--- 第 %d 次调用（识别为 %s）---\n%s\n",
			i+1, c.Agent, truncateForFailure(messagesText(c.Messages)))
	}
	t.Fatalf("没有一次调用包含 %q，实际调用如下：\n%s", needle, b.String())
	return ""
}

func seenAgents(e *agentScriptedEngine) string {
	return strings.Join(e.SeenAgents(), ",")
}
