package agents

import (
	"context"
	"strings"
	"testing"

	"yagent/internal/blackboard"
	"yagent/internal/llm"
	"yagent/internal/memory"
)

// /resume 的链路（与 internal/tui/tui_resume.go 的 restoreFromRollout 同一步骤序列）：
//
//	跑完一轮 → rollout 同步落盘（session_meta 带 task_id + board_id + cwd）
//	→ ListRolloutSessions 列会话 → ReadRolloutMemory 重建 Memory（修复工具调用配对）
//	→ 接管原来那块便签板 → 下一轮拿着重建出来的记忆接着做
//
// 三个环节各自有单测，串起来没人验过。串错的表现全是"静默降级"：
//   - 板没接管 → 恢复出来的任务看不见已调查过的结论，只能重做一遍；
//   - 记忆没重建 → 续跑那一轮看不到上一轮的提问与委派回执，等于重开一个新任务；
//   - 配对没修好 → 下一轮请求被 provider 以"tool 消息没有对应的 tool_calls"整条拒掉。
//
// 判据在 2026-11 换过一次：这里原先用"工作区根 AGENTS.md 的哨兵出现在续跑轮的 prompt 里"
// 证明 resumed 标志传到了（那是 PromptInput.ForceProjectContext 唯一可见的效果）。那条注入
// 已整体删除 —— Director 不再预置任何根知识文件，于是哨兵**必须不出现**（夹具真的在盘上写了
// 那份文件，所以这条不是恒真：有人把根文件加回运行时上下文，它立刻红）。"续跑确实接上了"改由
// 三条仍然可观察的事实负责：便签板被接管（结论可见）、记忆被重建（上一轮的提问与回执在 prompt
// 里）、续跑轮自己写出了 rollout。

const (
	resumeProjectMarker  = "RESUME-PROJECT-CONTEXT-MARKER"
	resumeConclusionMark = "RESUME-FIRST-CONCLUSION"
)

func TestE2E_Resume_RestoresBoardAndMemory(t *testing.T) {
	workDir := t.TempDir()
	// 工作区根知识文件：方案 B 之后它**不该**出现在任何运行时上下文里 —— 夹具真的在盘上写下
	// 带哨兵的这一份，好让下面那条"必须没影"的断言不是恒真。
	if err := writeFile(t, workDir, "AGENTS.md",
		"# 项目约定\n\n"+resumeProjectMarker+"：所有写操作必须走 delegate_coding。\n"); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	const (
		sessionID = "resume-session-1"
		task1     = "resume-task-1"
		task2     = "resume-task-2"
	)

	// ── 第一轮：正常跑一次委派，产出便签 + rollout ──
	engine1 := newScripted(map[string][]scriptStep{
		"director": {toolCallStep("delegate_repo", map[string]interface{}{"task": "RESEARCH-TASK-1"})},
		"repo":     {fixed(exitWithHandoff("仓库结构已查清，入口在 main.go", resumeConclusionMark))},
	})
	director1 := newFullChainDirector(t, workDir, engine1)
	director1.SetBoardScope(sessionID, task1)

	mem1 := memory.NewConversationMemory(300)
	if _, err := director1.Run(memory.WithConversationMemory(context.Background(), mem1), "先查清仓库"); err != nil {
		t.Fatalf("第一轮 Director.Run: %v", err)
	}

	// ── 找回那次会话的 rollout ──
	sessions, err := memory.ListRolloutSessions(memory.RolloutRootDir(), 0)
	if err != nil {
		t.Fatalf("ListRolloutSessions: %v", err)
	}
	var session *memory.RolloutSessionInfo
	for i := range sessions {
		if sessions[i].TaskID == task1 && (sessions[i].AgentName == "" || sessions[i].AgentName == "director") {
			session = &sessions[i]
			break
		}
	}
	if session == nil {
		var got []string
		for _, s := range sessions {
			got = append(got, s.TaskID+"/"+s.AgentName)
		}
		t.Fatalf("没找到 task %q 的 director rollout（找到：%s）", task1, strings.Join(got, ", "))
	}

	// rollout 必须记着这块板：board 是 session 级的，只有 session_meta.board_id 能续上它。
	if want := blackboard.SanitizeBoardID(sessionID); session.BoardID != want {
		t.Errorf("session_meta.board_id = %q, want %q（丢了就只能重做已完成的调查）", session.BoardID, want)
	}
	if session.CWD == "" {
		t.Error("session_meta.cwd 为空，/resume 后项目目录会退化成当前目录")
	}

	// ── 重建 Memory ──
	restored, info, err := memory.ReadRolloutMemory(session.Path, memory.DefaultRolloutReadOptions())
	if err != nil {
		t.Fatalf("ReadRolloutMemory: %v", err)
	}
	if restored == nil || len(restored.Snapshot()) == 0 {
		t.Fatal("重建出来的记忆是空的")
	}
	// 工具调用配对必须完整：role=tool 的每条都要有对应的 assistant tool_calls，
	// 否则 provider 会整条请求拒掉（这就是 ReadRolloutMemory 做修复的原因）。
	if dangling := len(info.DanglingToolCalls); dangling != 0 {
		t.Errorf("DanglingToolCalls = %d, want 0（重建出来的序列发不出去）", dangling)
	}
	if got := assertPairedToolMessages(t, restored.Snapshot()); got != "" {
		t.Errorf("重建出来的记忆里有孤儿工具消息: %s", got)
	}
	// 重建出来的必须含上一轮的交付正文与 note: 引用。分工要注意：工具回执存的是
	// **正文**，handoff 结论落在便签上 —— 所以"结论有没有活下来"由下面 resumed
	// 那一轮断言负责，这里管的是回执本身与回读入口。
	joined := joinContents(restored.Snapshot())
	if !strings.Contains(joined, "仓库结构已查清") {
		t.Errorf("重建的记忆丢了上一轮的委派回执: %s", truncateForFailure(joined))
	}
	if !strings.Contains(joined, "note: repo_agent/") {
		t.Errorf("回执里的 note: 引用没活过重建（便签的回读入口就此丢失）: %s", truncateForFailure(joined))
	}
	if !strings.Contains(joined, "先查清仓库") {
		t.Errorf("重建的记忆丢了用户输入: %s", truncateForFailure(joined))
	}

	// ── 第二轮：接管板 + 重建记忆，续跑 ──
	engine2 := newScripted(map[string][]scriptStep{
		"director": {func(msgs []llm.Message, _ []llm.ToolDef) *llm.Response {
			text := messagesText(msgs)
			// 1) 便签板续上了：上一轮的 handoff 结论只存在于便签上，看得见才算接管成功
			if !strings.Contains(text, resumeConclusionMark) {
				t.Errorf("续跑之后看不见上一轮的结论（便签板没接管）:\n%s", truncateForFailure(text))
			}
			// 2) 记忆续上了：本轮输入只有"继续"，所以上一轮的提问与委派回执只能来自重建出来的 memory
			if !strings.Contains(text, "先查清仓库") || !strings.Contains(text, "仓库结构已查清") {
				t.Errorf("续跑轮看不到上一轮的记忆（等于重开了一个新任务）:\n%s", truncateForFailure(text))
			}
			// 3) 工作区根 AGENTS.md 不再进运行时上下文：盘上那份带哨兵的文件必须没影
			if strings.Contains(text, resumeProjectMarker) {
				t.Errorf("根知识文件又被预置进 Director 的上下文了:\n%s", truncateForFailure(text))
			}
			return textResponse("接手后继续")
		}},
	})
	director2 := newFullChainDirector(t, workDir, engine2)
	director2.SetBoardScope(sessionID, task2)
	// 续跑旗照生产路径一路置位（TUI → app → DirectorAgent），但它现在不再改变 prompt。
	director2.SetResumedTask(true)

	if _, err := director2.Run(memory.WithConversationMemory(context.Background(), restored), "继续"); err != nil {
		t.Fatalf("续跑轮 Director.Run: %v", err)
	}

	// 续跑轮必须自己落一份 rollout：这是"那一轮真的跑了"的外部证据，不是模型的回显。
	sessionsAfterResume, listErr := memory.ListRolloutSessions(memory.RolloutRootDir(), 0)
	if listErr != nil {
		t.Fatalf("ListRolloutSessions（续跑后）: %v", listErr)
	}
	ranResumeRound := false
	for i := range sessionsAfterResume {
		if sessionsAfterResume[i].TaskID == task2 {
			ranResumeRound = true
			break
		}
	}
	if !ranResumeRound {
		t.Errorf("续跑轮 task %q 没写下自己的 rollout（那一轮其实没跑起来）", task2)
	}

	// ── 对照组：**同一份恢复记忆**、同一面续跑旗，只换一块全新的板。
	//    上面第 1 条断言（结论可见）只有在这里看不见结论时才是因果的：若"反正都看得见"，
	//    那条断言就永远绿，板没接管也测不出来 —— 与 e2e_handoff_test.go 的对照组同一个形状。
	engine3 := newScripted(map[string][]scriptStep{
		"director": {func(msgs []llm.Message, _ []llm.ToolDef) *llm.Response {
			text := messagesText(msgs)
			if strings.Contains(text, resumeConclusionMark) {
				t.Error("对照组（另一块板）也看见了结论 —— 上面那条「便签板接管」的断言因此是空断言")
			}
			// 记忆仍然在：差异只能来自便签板，不能来自"这轮根本没跑"。
			if !strings.Contains(text, "先查清仓库") {
				t.Errorf("对照组丢了重建出来的记忆:\n%s", truncateForFailure(text))
			}
			return textResponse("对照组结束")
		}},
	})
	director3 := newFullChainDirector(t, workDir, engine3)
	director3.SetBoardScope("resume-session-fresh-board", "resume-task-3")
	director3.SetResumedTask(true)
	// 重新读一份，避免上一轮的 Run 改写过 restored。
	restoredAgain, _, err2 := memory.ReadRolloutMemory(session.Path, memory.DefaultRolloutReadOptions())
	if err2 != nil {
		t.Fatalf("对照组 ReadRolloutMemory: %v", err2)
	}
	if _, err := director3.Run(memory.WithConversationMemory(context.Background(), restoredAgain), "继续"); err != nil {
		t.Fatalf("对照组 Director.Run: %v", err)
	}
}

// assertPairedToolMessages 返回第一条"没有对应 tool_calls 出处"的工具消息内容；没有则空串。
func assertPairedToolMessages(t *testing.T, msgs []memory.ChatMessage) string {
	t.Helper()
	pending := map[string]bool{}
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			if tc.ID != "" {
				pending[tc.ID] = true
			}
		}
	}
	for _, m := range msgs {
		if m.Type != memory.MessageTypeTool || m.ToolCallID == nil {
			continue
		}
		if !pending[*m.ToolCallID] {
			return *m.ToolCallID
		}
	}
	return ""
}

func joinContents(msgs []memory.ChatMessage) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(string(m.Type))
		b.WriteByte(':')
		b.WriteString(m.Content)
		b.WriteByte('\n')
	}
	return b.String()
}
