package agents

// planner_panic_test.go — D1：Director 侧一次 panic 的工具执行不许被当成"任务完成"收尾。
//
// 缺陷形状：execPlannerToolCall 自己不 recover，工具里的 panic 由 toolbatch 的
// guardedExec 兜住（internal/agents/toolbatch/run.go），而 guardedExec 是泛型
// Run[T any]，它只能把 panic 折成 T 的**零值** —— plannerToolOutcome{result:"", failed:false}。
// 于是收尾判据 `name == "agent_exit" && !out.failed` 成立，一次崩溃在账本上变成
// writeComplete(StopKindAgentExit) + PlanResult{Text:"Task completed successfully"}。
// 修复落在业务侧：exec 闭包自己 recover 成 "Error: panic: %v" 的**失败**回执，
// 判据不再成立，循环把这条错误交回模型继续跑（与 executor.go 的并行闭包同形）。
//
// 载体必须是生产那一条 newDirectorToolRunner（参数解析、工具 deadline、委派格子都在它身上），
// 绕过它就等于测了一份生产里不存在的接线。
//
// 回退修复会怎么红：去掉 execPlannerToolCall 的 defer recover 之后，第一次 agent_exit 就被
// 冒领成收尾 —— StopReason = "agent_exit"、Text = "Task completed successfully"、
// 终止事件变成 task_complete(agent_exit)（成功类）、只跑了 1 步、tool 回执是空串。
// 下面四处断言全部红。

import (
	"strings"
	"testing"

	"yagent/internal/llm"
	"yagent/internal/memory"
)

func TestPlanner_AgentExitPanicNotTerminal(t *testing.T) {
	c := plannerExitCase{
		name:       "agent_exit_panic",
		maxSteps:   3,
		toolNames:  []string{"agent_exit"},
		panicTool:  "agent_exit",
		script:     []*llm.Response{makeToolCallResponse("agent_exit", `{"reason":"done"}`)},
		repeatLast: true,
		// 期望：panic 三步都没被当收尾，跑满步数后以**中止**记账。
		wantKind: "turn_aborted", wantStop: memory.StopKindMaxSteps,
	}

	res, runErr, events, mem := runPlannerExit(t, c)

	// ① 本条测试的核心：panic 的那一次 agent_exit 不许成为终止原因。
	if res.StopReason == "agent_exit" {
		t.Errorf("StopReason = %q, want 非 agent_exit：一次 panic 的工具执行被冒领成任务完成", res.StopReason)
	}
	if res.Text == "Task completed successfully" {
		t.Errorf("Text = %q, want 模型真正给出的答案（硬编的成功文案不该由崩溃路径交回）", res.Text)
	}

	// ② 终止事件有且仅有一条。
	if len(events) != 1 {
		t.Fatalf("终止事件 = %d 条, want 恰好 1 条: %+v", len(events), events)
	}
	got := events[0]

	// ③ 而且它不是成功类。
	if isSuccessKind(got.stopReason) {
		t.Errorf("rollout stop_reason = %q, want 失败类（panic 不该记成成功）", got.stopReason)
	}
	if got.kind != c.wantKind || got.stopReason != c.wantStop {
		t.Errorf("终止事件 = %s/%s, want %s/%s（runErr=%v, res=%+v）",
			got.kind, got.stopReason, c.wantKind, c.wantStop, runErr, res)
	}

	// ④ panic 必须变成一条**点名 panic** 的失败回执交回模型，而不是空串：
	//    零值回执让模型以为 agent_exit 成功执行了。
	receipts := toolResults(mem)
	if len(receipts) != c.maxSteps {
		t.Fatalf("tool 回执数 = %d, want %d（每一步的 panic 都该各有一条配对回执）：%q",
			len(receipts), c.maxSteps, receipts)
	}
	for i, r := range receipts {
		if !strings.Contains(r, "Error: panic: boom") {
			t.Errorf("第 %d 条 tool 回执 = %q, want 含 \"Error: panic: boom\"", i, r)
		}
	}
}
