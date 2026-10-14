package agents

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"yagent/internal/blackboard"
	"yagent/internal/llm"
	"yagent/internal/memory"
)

// 跨委派交接是 Director"记住上一次委派"的唯一机制：
//
//	子 Agent agent_exit{handoff} → deliverableText() 取正文 → 写便签（正文=交付物全文）
//	→ 下一轮从磁盘重扫出 Board Index，注入 Director 的每次 LLM 调用与每次委派的任务前缀
//	→ 超预算的结果另存 artifact，Director 按 ID 用 read_artifact 取全文
//
// 这条链上有两个 AGENTS.md 明确记录过的坑：
//   - 索引必须是**瞬态**的：只进这次调用的 messages，绝不写回 ConversationMemory/rollout，
//     否则压缩预算会把目录算进正文，紧急压缩还会把它当成一条用户输入折叠进摘要；
//   - 交付物不在 AgentResult.Text 里（那条路径的 Text 是 {"finished":true,...}），
//     便签正文必须走 deliverableText()。
//
// 现有 fullchain 用例不调 SetBoardScope，boardID 恒空 —— 这两个坑在测试里根本不会重现。
// 这里跑真实 Director + 真实子 Agent + 真实便签盘（$HOME 已被 TestMain 隔离），只换掉 LLM。

// exitWithHandoff 生成"文本 + agent_exit(带 handoff)"这一轮回复。
// 文本就是交付物正文；agent_exit 的那坨 JSON 不是。
func exitWithHandoff(text, conclusion string, facts ...[2]string) *llm.Response {
	handoff := map[string]interface{}{"conclusion": conclusion, "status": "ok"}
	if len(facts) > 0 {
		list := make([]map[string]string, 0, len(facts))
		for _, f := range facts {
			list = append(list, map[string]string{"claim": f[0], "where": f[1]})
		}
		handoff["facts"] = list
	}
	args, _ := json.Marshal(map[string]interface{}{"reason": "任务完成", "handoff": handoff})
	return &llm.Response{Choices: []llm.Choice{{
		Content: text,
		ToolCalls: []llm.ToolCall{{
			ID:       "call_exit",
			Type:     "function",
			Function: llm.FunctionCall{Name: "agent_exit", Arguments: string(args)},
		}},
	}}}
}

func marshalArgs(v map[string]interface{}) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

var noteRefRe = regexp.MustCompile(`note:\s*([A-Za-z0-9_\-./]+)`)
var artifactIDRe = regexp.MustCompile(`artifact:\s*([0-9]{8}-[0-9]{6}-[0-9a-f]{8})`)

func mustNoteRef(t *testing.T, text string) string {
	t.Helper()
	m := noteRefRe.FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("委派结果头部没有 note: 字段: %s", truncateForFailure(text))
	}
	return strings.TrimRight(m[1], ".,) ")
}

func mustArtifactID(t *testing.T, text string) string {
	t.Helper()
	m := artifactIDRe.FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("委派结果里没有 artifact id: %s", truncateForFailure(text))
	}
	return m[1]
}

// subAgentResult 从会话记忆里取某次委派的工具回执（头部是 [Sub-Agent Result: <tool>]）。
func subAgentResult(t *testing.T, history []memory.ChatMessage, toolName string) string {
	t.Helper()
	needle := "[Sub-Agent Result: " + toolName + "]"
	last := ""
	for _, m := range history {
		if strings.Contains(m.Content, needle) {
			last = m.Content
		}
	}
	if last == "" {
		t.Fatalf("会话记忆里没有 %s 的回执（共 %d 条消息）", needle, len(history))
	}
	return last
}

// TestE2E_Handoff_SecondDelegationSeesFirstNote 钉住整条交接链的六段。
func TestE2E_Handoff_SecondDelegationSeesFirstNote(t *testing.T) {
	workDir := t.TempDir()
	if err := writeFile(t, workDir, "main.go", "package main\n\nfunc main() {}\n"); err != nil {
		t.Fatalf("write main.go: %v", err)
	}

	const (
		deliverable = "仓库里只有一个入口文件，打印后退出。DELIVERABLE-MARKER-7712"
		conclusion  = "CONCLUSION-ONLY-ONE-GO-FILE"
		sessionID   = "e2e-session-A"
	)

	engine := newScripted(map[string][]scriptStep{
		"director": {
			toolCallStep("delegate_repo", map[string]interface{}{"task": "数一下项目里有几个 Go 文件"}),
			toolCallStep("delegate_coding", map[string]interface{}{"task": "CODING-TASK-MARKER 给入口函数加一行注释"}),
			// 之后由 exhaust 兜成纯文本 → Planner 收尾
		},
		"repo": {
			fixed(exitWithHandoff(deliverable, conclusion, [2]string{"只有 main.go", "main.go:1"})),
		},
		"coding": {
			fixed(exitWithHandoff("已加上注释", "注释已写入")),
		},
	})

	director := newFullChainDirector(t, workDir, engine)
	director.SetBoardScope(sessionID, "e2e-task-A")

	mem := memory.NewConversationMemory(300)
	res, err := director.Run(memory.WithConversationMemory(context.Background(), mem), "先数文件，再加注释")
	if err != nil {
		t.Fatalf("Director.Run: %v", err)
	}

	// 1) 便签真的落在这块板上，ref 从 Director 当轮看到的委派结果头部拿得到。
	//    注意取的是**模型实际收到的消息**而不是会话记忆：记忆里那份是
	//    injectSubAgentMemory 生成的摘要，它刻意不带 ref —— ref 是派生态，
	//    每轮从磁盘重扫的 Board Index 才是权威入口（见 pitfall #11）。
	boardID := blackboard.SanitizeBoardID(sessionID)
	ref := mustNoteRef(t, engine.callTextContaining(t, "[Sub-Agent Result: repo]"))
	note, err := blackboard.ReadNote(boardID, ref)
	if err != nil {
		t.Fatalf("ReadNote(%s,%s): %v", boardID, ref, err)
	}

	// 2) 正文是交付物全文，不是 agent_exit 的 JSON（pitfall #12）。
	if !strings.Contains(note.Body, "DELIVERABLE-MARKER-7712") {
		t.Errorf("便签正文里没有交付物: %q", note.Body)
	}
	if strings.Contains(note.Body, `"finished"`) {
		t.Errorf("便签正文写成了 agent_exit 的 JSON: %q", note.Body)
	}

	// 3) 结构化 handoff 进 front-matter：结论、状态、以及带 where 的论断。
	if note.Handoff.Conclusion != conclusion {
		t.Errorf("handoff.conclusion = %q, want %q", note.Handoff.Conclusion, conclusion)
	}
	if note.Handoff.Status != "ok" {
		t.Errorf("handoff.status = %q, want ok（枚举只有 ok|partial|error）", note.Handoff.Status)
	}
	if len(note.Handoff.Facts) != 1 || note.Handoff.Facts[0].Where != "main.go:1" {
		t.Errorf("handoff.facts = %+v, want 一条带 where 的论断", note.Handoff.Facts)
	}

	// 4) 第二次委派的任务前缀带着 Board Index 与上一条结论 ——
	//    子 Agent 不必重新调查一遍，这是这块板存在的理由。
	codingText := engine.callTextContaining(t, "CODING-TASK-MARKER")
	if !strings.Contains(codingText, "## Board Index (") {
		t.Errorf("第二次委派没有 Board Index:\n%s", truncateForFailure(codingText))
	}
	if !strings.Contains(codingText, conclusion) {
		t.Errorf("第二次委派看不到上一条结论:\n%s", truncateForFailure(codingText))
	}

	// 5) Director 自己最后一轮也拿得到索引（注入的是 messages）。
	last := engine.lastCallFor(t, "director")
	if !strings.Contains(messagesText(last.messages), "## Board Index (") {
		t.Error("Director 最后一轮没有 Board Index")
	}

	// 6) 索引是瞬态的：绝不能进会话记忆 —— 一旦漏回，下一步的压缩预算会把目录
	//    算进正文，紧急压缩还会把它当成一条用户输入折叠进摘要（pitfall #11）。
	for _, m := range res.Memory {
		if strings.Contains(m.Content, "## Board Index (") {
			t.Fatalf("Board Index 被写进了会话记忆（它只应存在于这次 LLM 调用的 messages 上）: %s",
				truncateForFailure(m.Content))
		}
	}
}

// TestE2E_Handoff_OversizedResultRoundTripsThroughArtifact 钉住两条叠在一起的链：
//
//	超预算委派 → 上下文只留摘要 + artifact id → Director 按 id 调 read_artifact
//	→ read_artifact 是 Cold 工具，第一次调用不执行、而是把完整 schema 回吐给模型
//	→ 模型重试一次才拿到全文；同时它必须从"下一步的 toolDefs"里出现。
//
// 这条链的失败模式全是静默的：摘要没接住全文 = 上下文爆量；
// deferred 的"教学回执"没写清参数 = 模型反复重试同一个调用；
// materialize 后没进 toolDefs = 工具永远取不回来。
func TestE2E_Handoff_OversizedResultRoundTripsThroughArtifact(t *testing.T) {
	workDir := t.TempDir()
	if err := writeFile(t, workDir, "big.txt", strings.Repeat("长报告的一行内容。\n", 200)); err != nil {
		t.Fatalf("write: %v", err)
	}

	// 交付物要明显超出 artifact 预算（默认 500 token），尾部放唯一标记，
	// 用来证明"全文是被单独取回来的"，而不是一开始就在上下文里。
	longDeliverable := strings.Repeat("报告正文行，用于把结果顶出上下文预算。\n", 300) + "\nTAIL-MARKER-44771"
	var artifactID string

	engine := newScripted(map[string][]scriptStep{
		"director": {
			// 1) 委派，拿到"摘要 + id"
			toolCallStep("delegate_repo", map[string]interface{}{"task": "生成一份长报告"}),
			// 2) 按 id 调 read_artifact（它是 Cold 的，这轮只会被"教"一次）
			func(msgs []llm.Message, _ []llm.ToolDef) *llm.Response {
				artifactID = mustArtifactID(t, messagesText(msgs))
				return makeToolCallResponse("read_artifact",
					mustMarshal(t, map[string]interface{}{"id": artifactID, "start_line": 1}))
			},
			// 3) 教学回执必须把参数形状说清，模型才能重试；同时 read_artifact
			//    必须已经出现在这一轮的 toolDefs 里（materialize 是 append-only）。
			func(msgs []llm.Message, tools []llm.ToolDef) *llm.Response {
				echo := ""
				for _, m := range msgs {
					if m.Role == llm.RoleTool && m.ToolName == "read_artifact" {
						echo = m.Content
					}
				}
				if !strings.Contains(echo, "schema for read_artifact was not loaded") {
					t.Errorf("第一次调用 Cold 工具没有拿到 schema 教学回执, got %q", truncateForFailure(echo))
				}
				if !strings.Contains(echo, `"id"`) {
					t.Errorf("教学回执没给出必填参数 id: %s", truncateForFailure(echo))
				}
				if !hasToolNamed(tools, "read_artifact") {
					t.Errorf("取回 schema 之后 read_artifact 没进下一步的 toolDefs: %v", toolNamesOf(tools))
				}
				return makeToolCallResponse("read_artifact",
					mustMarshal(t, map[string]interface{}{"id": artifactID, "start_line": 1}))
			},
			// 4) 全文必须已经回到上下文里。
			func(msgs []llm.Message, _ []llm.ToolDef) *llm.Response {
				if !strings.Contains(messagesText(msgs), "TAIL-MARKER-44771") {
					t.Errorf("重试 read_artifact 仍没取回全文:\n%s", truncateForFailure(messagesText(msgs)))
				}
				return textResponse("已读到全文并确认结尾")
			},
		},
		"repo": {
			fixed(exitWithHandoff(longDeliverable, "CONCLUSION-BIG-REPORT")),
		},
	})

	director := newFullChainDirector(t, workDir, engine)
	director.SetBoardScope("e2e-session-B", "e2e-task-B")

	mem := memory.NewConversationMemory(300)
	res, err := director.Run(memory.WithConversationMemory(context.Background(), mem), "生成并复读长报告")
	if err != nil {
		t.Fatalf("Director.Run: %v", err)
	}

	// 留在记忆里的必须是摘要：artifact 机制的意义就是别把整份报告塞进上下文。
	delegateResult := subAgentResult(t, res.Memory, "delegate_repo")
	if !strings.Contains(delegateResult, "artifact:") {
		t.Errorf("委派结果头部缺 artifact 字段: %s", truncateForFailure(delegateResult))
	}
	if strings.Contains(delegateResult, "TAIL-MARKER-44771") {
		t.Errorf("委派结果把全文塞进了上下文，artifact 没有生效: %s", truncateForFailure(delegateResult))
	}
}

// TestE2E_Handoff_SameBoardAcrossTwoTasks 钉住 board 作用域 = session：
// 同一会话的第二个任务必须接着看同一块板；换会话则完全隔离。
func TestE2E_Handoff_SameBoardAcrossTwoTasks(t *testing.T) {
	workDir := t.TempDir()
	if err := writeFile(t, workDir, "a.txt", "alpha"); err != nil {
		t.Fatalf("write: %v", err)
	}

	const sessionID = "e2e-session-C"
	const firstConclusion = "CONCLUSION-FROM-TASK-1"

	// 任务 1：一次委派，留下一张便签。
	engine1 := newScripted(map[string][]scriptStep{
		"director": {toolCallStep("delegate_repo", map[string]interface{}{"task": "读 a.txt"})},
		"repo":     {fixed(exitWithHandoff("文件内容是 alpha", firstConclusion))},
	})
	d1 := newFullChainDirector(t, workDir, engine1)
	d1.SetBoardScope(sessionID, "task-1")
	if _, err := d1.Run(context.Background(), "任务1"); err != nil {
		t.Fatalf("task1 run: %v", err)
	}

	// 任务 2：同 session、换 taskID —— Director 第一次调用就该看见上一任务的结论。
	engine2 := newScripted(map[string][]scriptStep{
		"director": {func(msgs []llm.Message, _ []llm.ToolDef) *llm.Response {
			if !strings.Contains(messagesText(msgs), firstConclusion) {
				t.Errorf("同一会话的新任务看不见上一任务的结论:\n%s", truncateForFailure(messagesText(msgs)))
			}
			return textResponse("任务2完成")
		}},
	})
	d2 := newFullChainDirector(t, workDir, engine2)
	d2.SetBoardScope(sessionID, "task-2")
	if _, err := d2.Run(context.Background(), "任务2"); err != nil {
		t.Fatalf("task2 run: %v", err)
	}

	// 反例：换个 session 就是另一块板，结论不得跨会话泄漏。
	engine3 := newScripted(map[string][]scriptStep{
		"director": {func(msgs []llm.Message, _ []llm.ToolDef) *llm.Response {
			if strings.Contains(messagesText(msgs), firstConclusion) {
				t.Error("另一个会话的任务看见了本会话的便签（board 作用域串了）")
			}
			return textResponse("任务3完成")
		}},
	})
	d3 := newFullChainDirector(t, workDir, engine3)
	d3.SetBoardScope("e2e-session-D", "task-3")
	if _, err := d3.Run(context.Background(), "任务3"); err != nil {
		t.Fatalf("task3 run: %v", err)
	}
}

// hasToolNamed / toolNamesOf 断言"这一步模型能看见哪些工具声明"。
func hasToolNamed(defs []llm.ToolDef, name string) bool {
	for _, d := range defs {
		if d.Function.Name == name {
			return true
		}
	}
	return false
}

func toolNamesOf(defs []llm.ToolDef) []string {
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		out = append(out, d.Function.Name)
	}
	return out
}

func mustMarshal(t *testing.T, v map[string]interface{}) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return string(b)
}
