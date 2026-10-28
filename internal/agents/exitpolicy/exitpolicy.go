// Package exitpolicy 是「这次工具调用是否应当终止循环」的唯一判定点。
//
// 为什么存在：两个循环曾经各写一份同形判定——子 Agent 侧
// `cfg.StopOnFinish && tc.Function.Name == "agent_exit" && execErr == nil`
// （internal/agents/executor.go），Director 侧
// `tc.Function.Name == "agent_exit" && !out.failed`
// （internal/agents/director/planner_run.go）。两份写法不一样（`execErr == nil` 对
// `!out.failed`），一份加了执行成败、另一份起初没加，于是「改一处忘另一处」是必然结局，
// 而漂移的表现不是报错，是**一次执行失败的 agent_exit 被冒领成任务完成**（交付静默丢失）。
// 这与根 AGENTS.md §11 坑 33 / internal/agents/AGENTS.md 硬约束 1 是同一个病灶：
// 两份同形实现必有一边漂走，所以判据只能有一份。闸门是
// `TestTerminationDecisionIsNotInlinedInLoops`（internal/agents/termination_gate_test.go），
// 它直接扫两个循环的源码，判定被内联回去就红。
//
// 什么不在这个包里（三条边界，都是刻意的）：
//  1. **词表不在这里。**「这一轮结束的原因是什么」（子 Agent 的 classifyStop、Director 的
//     StopKindOf）依然是两份同形实现，靠 TestStopKindClassifiersAgree 对齐——分层守卫
//     （禁止 director import internal/agents）逼出来的，合并会引入循环依赖，见
//     internal/agents/AGENTS.md 坑 18。本包只回答「要不要停」，不回答「为什么停」。
//  2. **按结果形态的事后判断不在这里。** agents/result.go 的 isAgentExitResult、
//     blueprint_commit.go、blueprint_evidence.go、resume.go 的 isSuccessKind 判断的都是
//     返回 JSON 的形状（`finished:true` + `reason` 键），用来决定「这轮跑完了吗、交付物取哪一段」，
//     是事后分析而不是流程控制。把它们收进来会让「流程判据」和「内容判据」两种语义互相污染。
//  3. **参数形状不在这里。** execFailed 是 bool 而不是 error：子 Agent 侧手上是 error
//     （runToolCall 的第三个返回值），Director 侧手上只有 bool（plannerToolOutcome.failed，
//     panic 由 recover 折叠成 true）。用 bool 让两侧表达同一个语义「这次执行失败了吗」，
//     各自在调用点做一次 `!= nil` 归一，不必为一个参数引入哨兵错误。
//     两侧口径本来也一致：**未注册的工具**在两边都返回 nil error（executor 的 found=false、
//     Director runner 的 "Tool x not found" + nil error），所以「名字对上但工具没注册」在两边
//     都算「执行没失败」——本批没有改变这一点，要改请先写测试。
//
// 唯一的字面量来源是 toolspec.ExitToolName：本包不再自己写 "agent_exit"，
// 工具注册（internal/tools/specs_flow.go）与判据比对的是同一个常量。
package exitpolicy

import "yagent/internal/toolspec"

// TerminateOnAgentExit 判定一次工具调用是否应当终止循环。
// stopOnFinish 为 false 时一律不终止（子 Agent 可按 Agent 关掉该行为，见 ExecutorConfig.StopOnFinish）。
// execFailed 表示这次工具执行是否失败：失败的那一次回执要交回模型、循环继续，由既有 MaxSteps 兜底。
func TerminateOnAgentExit(stopOnFinish bool, toolName string, execFailed bool) bool {
	return stopOnFinish && Terminate(toolName, execFailed)
}

// Terminate 是 stopOnFinish 恒为真的等价形式，供没有该开关的调用方（Director）使用。
func Terminate(toolName string, execFailed bool) bool {
	return toolName == toolspec.ExitToolName && !execFailed
}
