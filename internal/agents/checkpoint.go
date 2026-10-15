package agents

// checkpoint.go — 子 Agent 停止时落一条 agent_checkpoint 的薄封装。
//
// 真正的写法在 memory.WriteCheckpoint（父子两个写入包不能互相 import，
// 抄一份必然分叉），这里只补上本层才知道的三件事：用哪个模型、走哪一档路由、
// 以及这套机制此刻是否启用。
//
// 关掉这套机制时连写都不写：留着半套开关的后果是"这份 rollout 为什么没有检查点"
// 变成一个新的、没人能回答的问题。

import (
	"context"

	"yagent/internal/config"
	"yagent/internal/llm"
	"yagent/internal/memory"
)

// writeExecutorCheckpoint 是 RunAgentLoop 终止路径上的检查点写入。
// kill-switch 判据与 Director 侧、恢复侧共用 config.ResumeEnabled()，
// 三处必须同进同退 —— 只关写入端的话，恢复侧会一直在找不存在/checkpoint。
func writeExecutorCheckpoint(ctx context.Context, rw *memory.RolloutWriter, cfg ExecutorConfig, stopKind string, steps int) {
	if !config.ResumeEnabled() {
		return
	}
	model := ""
	if cfg.LLM != nil {
		model = cfg.LLM.Model()
	}
	tier := ""
	if t, ok := llm.TierFrom(ctx); ok {
		tier = t.String()
	}
	memory.WriteCheckpoint(ctx, rw, memory.CheckpointInput{
		AgentName:        cfg.AgentName,
		Model:            model,
		Tier:             tier,
		StopKind:         stopKind,
		Steps:            steps,
		MaxSteps:         cfg.MaxSteps,
		CollectWorkspace: config.GlobalResume().CheckpointWorkspace,
	})
}
