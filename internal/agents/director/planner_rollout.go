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
	"log/slog"
	"os"

	"yagent/internal/config"
	"yagent/internal/llm"
	"yagent/internal/memory"
)

// directorJournal 把这一轮的结局写进 Director 自己的 rollout。
//
// 指针接收者是必需的，不是风格问题：terminal 是"这一轮有没有出口认领过终止事件"
// 的唯一账本，值接收者会把置位落在副本上，兜底 defer 于是永远以为没人写过 ——
// 每个正常收尾的会话都多补一条 unaccounted，比原缺陷更难读。
type directorJournal struct {
	ctx context.Context
	p   *Planner
	rw  *memory.RolloutWriter
	// terminal 标记本轮已有出口认领终止事件（task_complete / turn_aborted）。
	// 由 complete/aborted 置位，只被 abortUnclaimed 读。
	terminal bool
}

func (j *directorJournal) enabled() bool { return j.rw != nil && j.rw.Enabled() }

// writeMessage 把一条对话消息落进 Director 的 rollout（首条消息会顺带补齐
// session_meta / turn_context / task_started）。它原本是 planner_run.go 里的闭包，
// 属于"记录"而不属于循环体，所以与终止事件放在同一个文件里。
func (j *directorJournal) writeMessage(msg llm.Message) {
	if !j.enabled() {
		return
	}
	if !j.rw.SessionMetaWritten() {
		cwd, _ := os.Getwd()
		j.rw.WriteSessionMeta(memory.SessionMeta{
			ID:          j.rw.SessionID(),
			SessionID:   j.rw.SessionID(),
			Cwd:         cwd,
			Originator:  "yagent",
			Source:      "cli",
			HistoryMode: "standard",
		})
		turnID := j.rw.NextTurn()
		directorModel := ""
		if j.p.cfg.LLM != nil {
			directorModel = j.p.cfg.LLM.Model()
		}
		j.rw.WriteTurnContext(memory.TurnContext{
			TurnID:            turnID,
			Cwd:               cwd,
			Model:             directorModel,
			Effort:            "medium",
			CollaborationMode: "director",
		})
		j.rw.WriteEventMsg(memory.EventMsg{
			Type: "task_started",
		})
	}
	msgID := j.rw.NextMessageID()
	items := memory.LLMMessageToResponseItems(msg, msgID)
	for _, item := range items {
		if err := j.rw.WriteResponseItem(item); err != nil {
			slog.Warn("Rollout: failed to write director message", "error", err)
		}
	}
}

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

// complete 写一条 task_complete。
//
// terminal 置位在 enabled() 判定**之前**，这是有意的：「这一轮有没有结局」与
// 「这一轮的账本能不能落盘」是两件事。后者不该把前者拖成兜底补写 —— 没有 writer
// 的运行本来就没有 rollout 可写，兜底去写也只能是空转，而把认领记在置位之后会让
// 语义变成"只有写成功才算有结局"。
func (j *directorJournal) complete(stopReason string) {
	j.terminal = true
	if !j.enabled() {
		return
	}
	j.rw.WriteRunComplete(j.p.runState(stopReason))
}

// aborted 写一条 turn_aborted，并在同一路径上落检查点。
//
// 检查点只在终止路径上写：正常跑完的会话没什么可恢复，多写一条只会让恢复侧
// 去分辨"这条是哪次停止的"。
//
// 幂等：与 complete 共用同一个 terminal 标记，所以一轮 Run 里终止事件至多一条
// —— 先到先得，后来的出口（含兜底）不再写第二份。恢复侧读的是**第一条**停点，
// 重复写会让后到的、更晚的 step 游标盖掉真正该续跑的那一步。
func (j *directorJournal) aborted(reason, stopReason string) {
	j.terminal = true
	if !j.enabled() {
		return
	}
	j.rw.WriteRunAborted(reason, j.p.runState(stopReason))
	j.checkpoint(stopReason)
}

// abortUnclaimed 是 Planner.Run 的终止收口兜底：本轮没有任何出口认领终止事件时
// 补写一条 turn_aborted。
//
// 存在的理由不是"多写一条记录"，而是把"有人新加了 return 却忘了记终止事件"从
// **静默丢失**变成**可见的异常取值**：没有这条兜底时 rollout 里根本没有终止事件，
// 恢复侧只能猜会话是不是被切断；有了它则明确写着 unaccounted。
// 取值用 memory.StopKindUnaccounted —— 与子 Agent 侧 stopKindTracker.kindOf()
// 的兜底同一个常量（词表住在 memory，两个写入包共用），不新造词表值。
func (j *directorJournal) abortUnclaimed() {
	if j.terminal {
		return
	}
	j.aborted("planner exited without a terminal event", memory.StopKindUnaccounted)
}

// checkpoint 落一条 Director 的停止坐标。
func (j *directorJournal) checkpoint(stopReason string) {
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
func (j *directorJournal) compacted(kind, summary string, tokensBefore, tokensAfter, savedTokens int) {
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
