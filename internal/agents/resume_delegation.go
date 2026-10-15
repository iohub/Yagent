package agents

// resume_delegation.go — B 层（检查点重放）与 C 层（交接兜底）的执行侧。
//
// 一个"继续"动作，按可用性依次降级（§1）：
//   A 进程还活着且停在闸门上 → 直接放开（internal/pause）
//   B 进程没了 / 换了机器    → 按 delegation_id 找到子 Agent 的 rollout，重建消息重放
//   C 重放不可行            → 合成一份真实报告交回 Director，由它重新委派
//
// 三段共用同一份检查点、同一个 delegation_id、同一个入口。
//
// C 不是"罕见的兜底"：换机器场景下用户 pull 之后 git HEAD 必然变（§6.4 判据 5），
// 所以判据 5 的默认策略是 warn 而不是 block。降级必须显式 —— 每一句都写进结果头部，
// 让 Director 和用户都知道这次不是精确重放（AGENTS.md §11 的头号病是错了不报错）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"yagent/internal/blackboard"
	"yagent/internal/compression"
	"yagent/internal/config"
	"yagent/internal/llm"
	"yagent/internal/memory"
)

// DelegationStop 是"这一轮停在一次委派上"的落点。
type DelegationStop struct {
	DirectorRolloutPath string
	ChildRolloutPath    string // 空 = 找不到子 Agent 的 rollout（判据 1）
	DelegationID        string
	ToolName            string
	AgentName           string
	OriginalArgs        string // 重放必须原样复用参数：重新生成参数等于换了个任务
	Step                int
	MaxSteps            int
	StopKind            string
	// Replayable / Reasons 是判据的结论。理由必须能显示出来 —— "不能继续"而不说为什么，
	// 用户只能猜，那是另一种静默。
	Replayable bool
	Reasons    []string
}

// ScanDelegationStops 扫一个 rollout 目录，找出"停在某次委派上、且有对应子 Agent 文件"的落点。
//
// 顺序沿用 ListRolloutSessions（mtime 新→旧），所以调用方拿到的第一条就是最近一次。
func ScanDelegationStops(root string) ([]DelegationStop, error) {
	sessions, err := memory.ListRolloutSessions(root, 0)
	if err != nil {
		return nil, fmt.Errorf("resume: list rollout dir %s: %w", root, err)
	}

	// 先按 delegation_id 把子 Agent 的文件索引起来：一份 Director 会话可能对应多个
	// 同角色的子文件（同一个 Agent 在一个任务里会被委派多次），只能靠 id 配对。
	children := map[string]string{}
	for _, s := range sessions {
		if s.DelegationID != "" {
			children[s.DelegationID] = s.Path
		}
	}

	var stops []DelegationStop
	for _, s := range sessions {
		if s.AgentName != "" {
			continue // 只看 Director 那份（子 Agent 文件的 AgentName 非空）
		}
		replay, err := memory.ReadRolloutForReplay(s.Path, memory.ReplayOptions{})
		if err != nil && replay == nil {
			continue
		}
		dangling, ok := replay.LastDangling()
		if !ok || !strings.HasPrefix(dangling.Name, "delegate_") {
			continue // 没停在委派上：要么正常结束，要么停在别的工具上
		}
		stop := DelegationStop{
			DirectorRolloutPath: s.Path,
			DelegationID:        dangling.ID,
			ToolName:            dangling.Name,
			AgentName:           strings.TrimPrefix(dangling.Name, "delegate_"),
			OriginalArgs:        replay.DanglingCallArgs(dangling.ID),
			StopKind:            replay.CheckpointKind(),
		}
		if child, found := children[dangling.ID]; found {
			stop.ChildRolloutPath = child
			if cr, cerr := memory.ReadRolloutForReplay(child, memory.ReplayOptions{}); cerr == nil && cr.Checkpoint != nil {
				stop.Step = cr.Checkpoint.Step
				stop.MaxSteps = cr.Checkpoint.MaxSteps
				stop.StopKind = cr.Checkpoint.StopKind
			}
		}
		stop.Replayable, stop.Reasons = assessReplay(stop)
		stops = append(stops, stop)
	}
	return stops, nil
}

// assessReplay 是 §6.4 的八条判据里"不需要真跑一次就能判"的那几条。
// 判据 8（重放过程中 LLM 报错）只能在 resumeDelegation 里现场判定。
func assessReplay(stop DelegationStop) (bool, []string) {
	var reasons []string
	if stop.ChildRolloutPath == "" {
		// 判据 1：没有带这个 delegation_id 的子文件。存量 rollout（本次改动之前写的）
		// 与 ~/.yagent 没同步过去都会落在这里。
		reasons = append(reasons, "1: no sub-agent rollout carries this delegation_id")
	}
	if stop.AgentName == "browser" {
		// 判据 8 之外的第 6 条（D8）：页面、滚动位置、JS 上下文、CDP 会话都不可序列化，
		// 而且 [browser] idle_ttl 默认 15m 会把会话回收掉。硬做的结果是
		// "看起来恢复了，实际上页面早就不是那个页面"。
		reasons = append(reasons, "6: browser delegation state is not serializable (design D8)")
	}
	if stop.StopKind == StopKindMaxSteps {
		reasons = append(reasons, "7: the run exhausted its max steps; replaying it would do the same")
	}
	switch stop.StopKind {
	case StopKindIdleTimeout, StopKindTotalTimeout:
		// 不在 §6.4 的八条里，但同样致命：超时那一步可能已经把失败结果记进了 rollout，
		// 原样重放就是重复执行副作用。宁可交报告，让 Director 重新决定。
		reasons = append(reasons, "extra: stopped on a timeout; the interrupted step may already be recorded in the rollout")
	}
	if len(reasons) > 0 {
		return false, reasons
	}

	replay, err := memory.ReadRolloutForReplay(stop.ChildRolloutPath, memory.ReplayOptions{})
	if err != nil {
		return false, []string{"2: the sub-agent rollout could not be re-read: " + err.Error()}
	}
	if replay.Checkpoint == nil {
		return false, []string{"2: no agent_checkpoint in the sub-agent rollout (written before this feature, or crashed mid-run)"}
	}
	if replay.Checkpoint.Digest != replay.Digest {
		return false, []string{fmt.Sprintf("2: digest mismatch (stored %q, rebuilt %q) — the rollout was truncated or edited",
			replay.Checkpoint.Digest, replay.Digest)}
	}
	if replay.Checkpoint.MessageCount != len(replay.Messages) {
		return false, []string{fmt.Sprintf("3: checkpoint expected %d messages, rebuild produced %d",
			replay.Checkpoint.MessageCount, len(replay.Messages))}
	}

	rc := config.GlobalResume()
	budget := replayTokenBudget(rc, replay.Model)
	if budget > 0 {
		est := EstimateTokens
		total := 0
		for _, m := range replay.Messages {
			total += est(m.Content) + est(m.Reasoning)
		}
		if total > budget {
			// 判据 4：立刻就会触发压缩，重放失去意义 —— 降级成一份短报告反而有用。
			return false, []string{fmt.Sprintf("4: rebuilt context is %d tokens, over the %d budget", total, budget)}
		}
	}

	ws := replay.Checkpoint.Workspace
	if ws.GitSHA == "" {
		// "没记下 sha"与"sha 变了"是两件事：前者是没有判据（比如 --prompt 模式跑在
		// 非仓库目录里），后者才是工作区动了。把前者当成后者，等于任何一份
		// 没有 git 身份的会话都被拒绝重放。
		return true, []string{"5: no git identity recorded in the checkpoint; workspace check skipped"}
	}
	if head, herr := currentGitSHA(); herr == nil && !memory.WorkspaceMatchesHead(ws, head) {
		if rc.DegradeOnWorkspaceChange == config.DegradeWorkspaceBlock {
			return false, []string{fmt.Sprintf("5: workspace moved %s → %s and [resume].degrade_on_workspace_change = \"block\"",
				ws.GitSHA, head)}
		}
		// warn：换机器场景下这条几乎必然命中（用户 pull 之后 HEAD 就变了）。
		// 默认 block 会让 B 层在最主要的场景里失效，所以缺省是"降级说明 + 继续重放"。
		return true, []string{fmt.Sprintf("5: workspace moved %s → %s; replaying anyway ([resume] policy = warn)",
			ws.GitSHA, head)}
	}
	return true, nil
}

// replayTokenBudget 决定"重放后还能不能塞进模型"。
// 显式配置优先；没配置就用模型目录的压缩阈值 —— 超过那个值，第一步就会被压缩改写，
// 重放的意义（模型看到与暂停前相同的世界）当场失效。
func replayTokenBudget(rc config.ResumeConfig, model string) int {
	if rc.ReplayTokenBudget > 0 {
		return rc.ReplayTokenBudget
	}
	return compression.ResolveThreshold(model, 0)
}

// currentGitSHA 取当前工作区的 HEAD。取不到（不在仓库里）返回 err，调用方据此跳过判据 5，
// 而不是把"不知道"当成"没变"。
func currentGitSHA() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	ws := memory.CollectWorkspace(context.Background(), cwd)
	if ws.GitSHA == "" {
		return "", errors.New("no git identity for the current workspace")
	}
	return ws.GitSHA, nil
}

// resumeDelegation 执行一次重放（B），失败时退回交接报告（C）。
//
// 返回的文本就是要交回 Director 的那条 tool result；degraded 是这一路降级的事实，
// 由调用方显示在便签与 UI 上。
func (a *DirectorAgent) resumeDelegation(ctx context.Context, stop DelegationStop) (string, []string) {
	replayable, reasons := assessReplay(stop)
	if !replayable {
		return a.degradedHandoff(stop, reasons)
	}

	opts := memory.ReplayOptions{MaxMessages: config.GlobalResume().MaxReplayMessages}
	replay, err := memory.ReadRolloutForReplay(stop.ChildRolloutPath, opts)
	if err != nil {
		// ErrReplayTooLarge 也要拿到已重建的内容：报告要用它。
		return a.degradedHandoff(stop, append(reasons, "4: "+err.Error()))
	}

	target := stop.AgentName
	params := map[string]interface{}{}
	if stop.OriginalArgs != "" {
		if uerr := json.Unmarshal([]byte(stop.OriginalArgs), &params); uerr != nil {
			return a.degradedHandoff(stop, []string{"8: the original delegation arguments could not be re-read: " + uerr.Error()})
		}
	}
	task, _ := params["task"].(string)

	// 重放的 ctx：delegation_id 让子文件能被回连，ResumeState 是执行状态的接缝，
	// 新的 writer 会产生第二份 _{agent}.jsonl —— 靠 delegation_id 与第一份串起来。
	runCtx := memory.WithDelegationID(ctx, stop.DelegationID)
	runCtx = WithResumeState(runCtx, &ResumeState{
		Messages:     replay.Messages,
		StartStep:    replay.Step,
		Materialized: replay.Materialized,
		ThinkLink:    replay.ThinkLink,
		DelegationID: stop.DelegationID,
		Digest:       replay.Digest,
	})
	if rw := a.createRolloutWriter(runCtx, target, task); rw != nil {
		defer rw.Close()
		runCtx = memory.WithRolloutWriter(runCtx, rw)
	}

	result, rerr := a.delegateTo(runCtx, target, params)
	if rerr != nil {
		// 判据 8：重放过程中就失败了（provider 变了、模型下线、参数在新工作区不成立）。
		// 原样报错等于把一次恢复失败当成"委派失败"，Director 会去重做整段 —— 交回报告。
		return a.degradedHandoff(stop, []string{"8: replay failed: " + rerr.Error()})
	}

	text := stringifyResult(result)
	var degraded []string
	degraded = append(degraded, reasons...)
	if len(replay.Degraded) > 0 {
		degraded = append(degraded, replay.Degraded...)
	}
	if len(degraded) > 0 {
		text = degradedHeader(degraded) + "\n" + text
	}
	a.writeResumedNote(stop, text, degraded)
	return text, degraded
}

// writeResumedNote 把恢复出来的交付落到便签板上。
//
// 为什么必须落：这次的结果交回 Director 之后，它的正文会进 memory、也可能被压缩改写掉；
// 而"上次到底交付了什么"是后续委派唯一能复核的事实。降级的那些理由尤其要留痕 ——
// 没有便签上的 degraded 标记，下一次委派就会把这份报告当成精确结论继续往上盖。
func (a *DirectorAgent) writeResumedNote(stop DelegationStop, body string, degraded []string) string {
	if !blackboardEnabled() || a.boardID == "" {
		return ""
	}
	status := blackboard.StatusOK
	if len(degraded) > 0 {
		status = blackboard.StatusDegraded
	}
	handoff := blackboard.Handoff{
		Conclusion: firstLine(body),
		Status:     status,
	}
	if len(degraded) > 0 {
		handoff.Degraded = strings.Join(degraded, "; ")
	}
	noteRef, err := blackboard.WriteNote(blackboard.NoteInput{
		BoardID: a.boardID,
		Agent:   stop.AgentName + "_agent",
		Tool:    stop.ToolName,
		Task:    argString(stop.OriginalArgs, "task"),
		Summary: handoff.Conclusion,
		Body:    body,
		Status:  status,
		Handoff: handoff,
	})
	if err != nil {
		slog.Warn("resume: failed to write the handoff note", "delegation", stop.DelegationID, "error", err)
		return ""
	}
	slog.Debug("resume: note written", "delegation", stop.DelegationID, "noteRef", noteRef, "status", status)
	return noteRef
}

// degradedHandoff 合成 §7.2 的那份报告并落一张 status=degraded 的便签。
//
// 便签引用要写回报告正文：Director 的提示词让它把 note ref 放进下一次委派的
// context_refs，那样新接手的 Agent 才能直接读到全文；只把 ref 记在日志里的话，
// 这份报告就成了这次交接的一次性文本，下一个 Agent 还得重新调查一遍。
func (a *DirectorAgent) degradedHandoff(stop DelegationStop, reasons []string) (string, []string) {
	text := degradedReport(stop, reasons)
	noteRef := a.writeResumedNote(stop, text, reasons)
	if noteRef != "" {
		text = strings.Replace(text, "delegation: "+stop.DelegationID+"\n",
			"delegation: "+stop.DelegationID+"\nnote: "+noteRef+"\n", 1)
	}
	return text, reasons
}

// degradedReport 是那份交接报告。**不调 LLM**：
// 暂停的时候可能正好没有可用的 provider，而且让模型来总结"上次做到哪"会引入
// 不确定性 —— 报告里每一项都必须是从账本（rollout）里抠出来的事实。
func degradedReport(stop DelegationStop, reasons []string) string {
	child, err := memory.ReadRolloutForReplay(stop.ChildRolloutPath, memory.ReplayOptions{})
	if err != nil {
		// 连子文件都读不到：只能凭 Director 那份的悬空记录说话。
		// err 可能是"超预算"（此时已重建的内容仍然可用），所以 child 未必为 nil。
		if child == nil {
			child = &memory.Replay{}
		}
		reasons = append(reasons, "1: sub-agent rollout could not be re-read: "+err.Error())
	}

	var b strings.Builder
	b.WriteString("[Delegation Resumed as Handoff — 精确重放不可用]\n")
	b.WriteString("delegation: " + stop.DelegationID + "\n")
	b.WriteString("degradation:\n")
	for _, r := range reasons {
		b.WriteString("  - " + r + "\n")
	}
	fmt.Fprintf(&b, "agent: %s   stopped_at: step %d/%d   stop_kind: %s\n",
		stop.AgentName, child.Step, stop.MaxSteps, stop.StopKind)
	if ws := child.CheckpointWorkspace(); ws != nil {
		fmt.Fprintf(&b, "workspace: git %s (%s), dirty=%v\n", shortSHA(ws.GitSHA), ws.GitBranch, ws.Dirty)
		if ws.StatusError != "" {
			fmt.Fprintf(&b, "workspace probe: unknown (%s)\n", ws.StatusError)
		}
	}

	b.WriteString("\n## Completed (verified from the rollout ledger)\n")
	completed := completedLines(child)
	if len(completed) == 0 {
		b.WriteString("- (nothing recorded as completed)\n")
	}
	for _, line := range completed {
		b.WriteString("- " + line + "\n")
	}

	b.WriteString("\n## In flight (side-effect state unknown)\n")
	if len(child.Dangling) == 0 {
		b.WriteString("- (none)\n")
	}
	for _, d := range child.Dangling {
		fmt.Fprintf(&b, "- %s (call_id %s) — its result was never recorded\n", d.Name, d.ID)
	}

	b.WriteString("\n## Remaining plan (verbatim from the last thought block)\n")
	plans := openPlanLines(child)
	if len(plans) == 0 {
		b.WriteString("- (no thought block was recorded)\n")
	}
	for _, p := range plans {
		b.WriteString("- " + p + "\n")
	}

	b.WriteString("\n## Suggested next delegation task\n")
	b.WriteString(suggestedTask(stop, child, plans) + "\n")

	slog.Warn("resume: degraded to handoff", "delegation", stop.DelegationID, "agent", stop.AgentName, "reasons", reasons)
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// completedLines 从"成功的工具输出 + 最后的 assistant 正文"里抠出已完成的事实。
// 判据是账本而不是模型回忆：只有确实拿到了 output 的调用才算做完。
func completedLines(r *memory.Replay) []string {
	var out []string
	answered := false
	for _, m := range r.Messages {
		switch m.Role {
		case llm.RoleTool:
			if m.ToolName == "" {
				continue
			}
			if strings.HasPrefix(m.Content, "Error:") {
				continue
			}
			out = append(out, m.ToolName+": "+firstLine(m.Content))
			answered = true
		case llm.RoleAssistant:
			if txt := strings.TrimSpace(m.Content); txt != "" {
				// 只取第一行：assistant 正文常常就是那个 <thought> 块，整块抄进
				// "已完成"会把没做完的条目也列成已完成 —— 那正是这份报告要避免的误导。
				out = append(out, "assistant: "+firstLine(txt))
				answered = true
			}
		}
	}
	if len(out) > 6 {
		out = out[len(out)-6:]
	}
	if !answered {
		return nil
	}
	return out
}

// openPlanLines 取最后一个 T&P 块里没做完的条目（[ ] 与 [>]），原文照抄。
// 改写它会丢掉模型当时写的措辞，而"接着做下去"恰恰需要那句话本身。
//
// 两个来源，按可靠性排序：thinklink 事件（P0.4 之后子 Agent 也会落盘），
// 以及直接从 assistant 正文里抽 <thought> 块 —— 后者让存量 rollout 与
// 没有建 thinklink store 的那几个子 Agent（repo/chat/devops）也能交出这份报告。
func openPlanLines(r *memory.Replay) []string {
	last := ""
	for _, e := range r.ThinkLink {
		if strings.Contains(e.Kind, "thought") {
			last = e.Content
		}
	}
	if last == "" {
		for _, m := range r.Messages {
			if m.Role != llm.RoleAssistant {
				continue
			}
			if blocks := compression.ExtractThoughtAndPlanBlocks(m.Content); len(blocks) > 0 {
				last = blocks[len(blocks)-1]
			}
		}
	}
	if last == "" {
		return nil
	}
	var out []string
	for _, line := range strings.Split(last, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "- [ ]") || strings.HasPrefix(t, "- [>]") ||
			strings.HasPrefix(t, "[ ]") || strings.HasPrefix(t, "[>]") {
			out = append(out, t)
		}
	}
	return out
}

func suggestedTask(stop DelegationStop, r *memory.Replay, plans []string) string {
	var b strings.Builder
	if task := argString(stop.OriginalArgs, "task"); task != "" {
		fmt.Fprintf(&b, "Continue the delegated task %q. ", truncRunes(task, 200))
	} else {
		b.WriteString("Continue the previously delegated task. ")
	}
	b.WriteString("Do NOT redo what is listed as completed; ")
	if len(plans) > 0 {
		b.WriteString("start from the first open plan item.")
	} else if last := lastAssistant(r); last != "" {
		fmt.Fprintf(&b, "the previous run's last message was: %s", truncRunes(last, 200))
	} else {
		b.WriteString("re-verify the workspace state before writing.")
	}
	return b.String()
}

func lastAssistant(r *memory.Replay) string {
	for i := len(r.Messages) - 1; i >= 0; i-- {
		if r.Messages[i].Role == llm.RoleAssistant && strings.TrimSpace(r.Messages[i].Content) != "" {
			return r.Messages[i].Content
		}
	}
	return ""
}

func degradedHeader(degraded []string) string {
	var b strings.Builder
	b.WriteString("[Resumed from checkpoint — degraded in places]\n")
	for _, d := range degraded {
		b.WriteString("! " + d + "\n")
	}
	return b.String()
}

func argString(argsJSON, key string) string {
	if strings.TrimSpace(argsJSON) == "" {
		return ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &m); err != nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return truncRunes(strings.TrimSpace(s[:i]), 160)
	}
	return truncRunes(strings.TrimSpace(s), 160)
}

func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func shortSHA(sha string) string {
	if len(sha) <= 8 {
		return sha
	}
	return sha[:8]
}

func stringifyResult(result interface{}) string {
	switch v := result.(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(b)
	}
}

// replayStopKind 是 memory.Replay 上取停止原因的便捷读法（没有检查点时为空）。
