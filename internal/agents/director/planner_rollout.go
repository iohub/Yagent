package director

// planner_rollout.go — Director 的落盘记录：终止事件、检查点、压缩快照。
//
// 这些原本是 planner_run.go 里的闭包。主循环那个文件按行数冻结
//（scripts/check_file_sizes.sh 的 PENDING），而 P1 要在循环顶部加暂停闸门、
// P2 要在终止路径上加检查点 —— 两段逻辑都不属于循环体，放在这里更合适，
// 也让"记录"这件事有一个唯一的地方可以改。
//
// 检查点的写法与子 Agent 侧共用 memory.WriteCheckpoint：两个写入包不能互相
// import（scripts/check_no_agents_import.sh），各抄一份的后果是父子检查点在
// 字段语义上慢慢分叉，而分叉的表现是恢复时某一边的判据永远不成立。

import (
	"context"

	"yagent/internal/config"
	"yagent/internal/llm"
	"yagent/internal/memory"
)

// directorJournal 把这一轮的结局写进 Director 自己的 rollout。
type directorJournal struct {
	ctx context.Context
	p   *Planner
	rw  *memory.RolloutWriter
}

func (j directorJournal) enabled() bool { return j.rw != nil && j.rw.Enabled() }

// runState 投影当前步数与停止原因，供终止/压缩记录使用。
// HasDelegated 一并带上：恢复侧靠它区分"什么也没干就被停住"和"干到一半被停住"。
func (p *Planner) runState(stopReason string) memory.RunState {
	return memory.RunState{
		Steps:        p.state.Step,
		MaxSteps:     p.cfg.MaxSteps,
		StopReason:   stopReason,
		HasDelegated: p.state.HasDelegated,
	}
}

func (j directorJournal) complete(stopReason string) {
	if !j.enabled() {
		return
	}
	j.rw.WriteRunComplete(j.p.runState(stopReason))
}

// aborted 写一条 turn_aborted，并在同一路径上落检查点。
//
// 检查点只在终止路径上写：正常跑完的会话没什么可恢复，多写一条只会让恢复侧
// 去分辨"这条是哪次停止的"。
func (j directorJournal) aborted(reason, stopReason string) {
	if !j.enabled() {
		return
	}
	j.rw.WriteRunAborted(reason, j.p.runState(stopReason))
	j.checkpoint(stopReason)
}

// checkpoint 落一条 Director 的停止坐标。
func (j directorJournal) checkpoint(stopReason string) {
	if !config.ResumeEnabled() || !j.enabled() {
		return
	}
	model := ""
	if j.p.cfg.LLM != nil {
		model = j.p.cfg.LLM.Model()
	}
	tier := ""
	if t, ok := llm.TierFrom(j.ctx); ok {
		tier = t.String()
	}
	memory.WriteCheckpoint(j.ctx, j.rw, memory.CheckpointInput{
		AgentName:        "director",
		Model:            model,
		Tier:             tier,
		StopKind:         stopReason,
		Steps:            j.p.state.Step,
		MaxSteps:         j.p.cfg.MaxSteps,
		CollectWorkspace: config.GlobalResume().CheckpointWorkspace,
	})
}

// kind 与 savedTokens 是给 /resume 重建 ⧉ 压缩段用的；Summary 的文案不动
// （历史恢复与人工排查都依赖它）。
func (j directorJournal) compacted(kind, summary string, tokensBefore, tokensAfter, savedTokens int) {
	if !j.enabled() {
		return
	}
	_ = j.rw.WriteCompacted(memory.CompactedPayload{
		Summary:      summary,
		TokensBefore: tokensBefore,
		TokensAfter:  tokensAfter,
		Kind:         kind,
		SavedTokens:  savedTokens,
	})
}
