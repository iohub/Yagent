package agents

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"yagent/internal/agents/director"
	"yagent/internal/artifact"
	"yagent/internal/blackboard"
	"yagent/internal/config"
	"yagent/internal/llm"
)

// 本文件实现"委派返回物分级"基础设施（第二批次接线完成）：
//   - FinalizeResult / FinalizeResultFull：统一构造函数，完整结果落盘为 artifact，
//     生成分级摘要；
//   - FormatForDirector：注入 Director 上下文的统一格式文本（格式单源）。
//
// Text 字段语义（第二批次定案）：完整文本输出，供程序化消费（输出解析、
// RepoSummary、错误消息）；LLM 上下文一律走 FormatForDirector（读
// Summary/ArtifactRef），不依赖 Text。

// projectPathProvider 返回当前项目路径（由 DirectorAgent.env.ProjectPath 提供）。
// computeProjectID() 是 DirectorAgent 私有方法（director.go），包级函数无法访问，
// 因此采用包级提供者注入：下一批次接线时由 DirectorAgent 调用 SetProjectPathProvider
// 注入；未注入时返回空串（director.ComputeProjectID("") == "default"）。
var (
	projectPathMu   sync.RWMutex
	projectPathFunc func() string
)

// SetProjectPathProvider 注入项目路径提供者（并发安全；nil 忽略）。
// 由 DirectorAgent 构造函数调用：SetProjectPathProvider(directorAgent.env.ProjectPath)。
func SetProjectPathProvider(fn func() string) {
	projectPathMu.Lock()
	defer projectPathMu.Unlock()
	projectPathFunc = fn
}

// currentProjectPath 返回当前项目路径（未注入时为空串）。
func currentProjectPath() string {
	projectPathMu.RLock()
	defer projectPathMu.RUnlock()
	if projectPathFunc == nil {
		return ""
	}
	return projectPathFunc()
}

// deliverableText 返回本次委派真正的交付文本。
//
// 以 agent_exit 收尾的 Agent（Coding/Browser/DevOps/Chat 都设了 StopOnFinish），
// ExecutorResult.Text 是 ExecuteAgentExit 返回的那一小段 JSON，
// 真实的分析散在最后一条 assistant 消息里。不取回来的话，便签与 artifact 里
// 就只剩一句 reason —— 下一个 Agent 只能重新委派一次才能拿回已有结论。
func deliverableText(exec ExecutorResult) string {
	if !isAgentExitResult(exec.Text) {
		return exec.Text
	}
	for i := len(exec.History) - 1; i >= 0; i-- {
		m := exec.History[i]
		if m.Role == llm.RoleAssistant && strings.TrimSpace(m.Content) != "" {
			return m.Content
		}
	}
	return exec.Text
}

// isAgentExitResult 认 ExecuteAgentExit 的返回形态（{"finished":true,"reason":"..."}）。
// 判据要够窄：普通交付文本恰好是一段 JSON 并不罕见，误判会把摘要换成无关内容。
func isAgentExitResult(text string) bool {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "{") {
		return false
	}
	var probe map[string]interface{}
	if err := json.Unmarshal([]byte(trimmed), &probe); err != nil {
		return false
	}
	finished, ok := probe["finished"].(bool)
	if !ok || !finished {
		return false
	}
	_, hasReason := probe["reason"]
	return hasReason
}

// collectHandoff 从 agent_exit 的返回值里提取结构化交接。
//
// 返回值 JSON 的形状是 {"finished":true,"reason":"...","handoff":{...}}，与工具参数同构，
// 所以解析器可以直接吃它——采集点因此只有本文件这一处，executor 不必认识 Handoff。
// 非 agent_exit 收尾（纯文本回复、步数耗尽）返回 nil：宁可不结构化，也不编造一个假结论。
func collectHandoff(exec ExecutorResult, full string) *blackboard.Handoff {
	if !isAgentExitResult(exec.Text) {
		return nil
	}
	h := blackboard.ParseHandoff([]byte(exec.Text), reasonFromExitJSON(exec.Text), full)
	return &h
}

func reasonFromExitJSON(text string) string {
	var probe struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &probe); err != nil {
		return ""
	}
	return probe.Reason
}

// FinalizeResult 统一构造函数：完整结果落盘为 artifact，生成分级摘要。
// Save 失败时降级为全文摘要（信息完整性优先，绝不静默丢内容，不返回 error、不阻断委派）。
func FinalizeResult(agentName, task string, exec ExecutorResult) AgentResult {
	mem := ConvertLLMHistoryToMemory(exec.History)
	full := deliverableText(exec)
	handoff := collectHandoff(exec, full)
	// 蓝图提交与它的证据：采集点与 handoff 同处（executor 不认识蓝图，工具层只回显参数）。
	// 没有提交时返回 nil，于是证据侧的那一遍 History 扫描根本不会发生。
	blueprintItems, evidenceFiles := collectBlueprintSubmission(exec)

	// kill-switch 或没有任何文本：回退旧行为，不落盘。
	if artifact.Disabled() || full == "" {
		return AgentResult{
			Text:          exec.Text,
			Summary:       exec.Text,
			Deliverable:   full,
			Memory:        mem,
			Handoff:       handoff,
			Blueprint:     blueprintItems,
			EvidenceFiles: evidenceFiles,
		}
	}

	// 预算内：全文即摘要，零行为变化，不落盘。
	if artifact.EstTokens(full) <= artifact.DefaultBudgetTokens {
		return AgentResult{
			Text:          exec.Text,
			Summary:       full,
			Deliverable:   full,
			Memory:        mem,
			Handoff:       handoff,
			Blueprint:     blueprintItems,
			EvidenceFiles: evidenceFiles,
		}
	}

	// 超预算：先落盘**完整交付文本**拿 Ref（重构前存的是 exec.Text，
	// 对 agent_exit 收尾的 Agent 那就是一小段退出 JSON）。
	projectID := director.ComputeProjectID(currentProjectPath())
	store := artifact.DefaultStore()
	ref, err := store.Save(projectID, agentName, task, "", full)
	if err != nil {
		// Save 失败：降级为全文摘要，不阻断委派。
		return AgentResult{
			Text:          exec.Text,
			Summary:       full,
			Deliverable:   full,
			Memory:        mem,
			Handoff:       handoff,
			Blueprint:     blueprintItems,
			EvidenceFiles: evidenceFiles,
		}
	}

	// 有合规 handoff 时摘要就是它——结论是字段，不再依赖"前缀里恰好有答案"。
	// 否则逐字回落到 Reduce 前缀，最坏情况等于本次重构之前。
	summary := ""
	if handoff != nil && handoff.Degraded == "" {
		summary = handoff.Render(config.GlobalBlackboard().SummaryMaxRunes)
	}
	if summary == "" {
		summary, _ = artifact.Reduce(full, ref.ID, artifact.DefaultBudgetTokens)
	}
	// 把摘要回写进 artifact（Load -> 改 Summary -> 覆盖写回同路径）。
	// 失败仅影响 artifact 内 summary 元数据完整性，不影响返回值，不阻断委派。
	_ = store.UpdateSummary(projectID, ref.ID, summary)

	// Text 保持重构前的形态（超预算时是摘要），Full 变体再覆盖成 exec.Text；
	// 需要交付全文的下游（便签正文）读 Deliverable。
	return AgentResult{
		Text:          summary,
		Summary:       summary,
		Deliverable:   full,
		ArtifactRef:   &ref,
		Memory:        mem,
		Handoff:       handoff,
		Blueprint:     blueprintItems,
		EvidenceFiles: evidenceFiles,
	}
}

// FinalizeResultFull 是 FinalizeResult 的 Full 变体：Text 恒为 exec.Text 完整输出，
// 供依赖完整输出的程序化消费链路（输出解析、RepoSummary 等）；
// Summary/ArtifactRef 分级逻辑与 FinalizeResult 完全相同（LLM 上下文仍走分级摘要）。
func FinalizeResultFull(agentName, task string, exec ExecutorResult) AgentResult {
	r := FinalizeResult(agentName, task, exec)
	r.Text = exec.Text
	return r
}

// FormatForDirector 生成注入 Director 上下文的统一格式文本（两处消费点共用，格式单源）。
//   - noteRef 为空且 unresolvedCount 为 0 时，输出与旧格式字节级一致；
//   - noteRef 非空时，header 行追加 "note: {noteRef}"；
//   - unresolvedCount > 0 时，header 行追加 "| unresolved refs: N"；
//   - 有 ArtifactRef：头部 + artifact 元信息 + 摘要 + read_artifact 取回提示。
func FormatForDirector(toolName string, r AgentResult, noteRef string, unresolvedCount int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Sub-Agent Result: %s]", toolName)
	if noteRef != "" {
		fmt.Fprintf(&b, " note: %s", noteRef)
	}
	if unresolvedCount > 0 {
		fmt.Fprintf(&b, " | unresolved refs: %d", unresolvedCount)
	}
	b.WriteByte('\n')
	if r.ArtifactRef == nil {
		b.WriteString(r.Summary)
		appendBlueprintAccount(&b, r)
		return b.String()
	}
	fmt.Fprintf(&b, "artifact: %s (%d chars total)\n", r.ArtifactRef.ID, r.ArtifactRef.CharCount)
	// TrimRight：Summary 尾部可能自带换行（Reduce 标记行 \n 结尾），
	// 统一去除后接单空行，保证格式确定性、不产生多余空行。
	b.WriteString(strings.TrimRight(r.Summary, "\n"))
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "[Output truncated to fit context. Full result stored as artifact %s. Call read_artifact(id=\"%s\") if you need details beyond the summary above.]",
		r.ArtifactRef.ID, r.ArtifactRef.ID)
	appendBlueprintAccount(&b, r)
	return b.String()
}

// appendBlueprintAccount 把 Repo 那一轮的蓝图账目附在交付之后。
// 它必须在**返回值正文之外**：正文是交付物（会被便签、artifact、压缩各读一遍），
// 而这一行是"写通道这次成了没有"，只对 Director 的决定有用，对谁都不是内容。
func appendBlueprintAccount(b *strings.Builder, r AgentResult) {
	if r.BlueprintAccount == "" {
		return
	}
	b.WriteString("\n\n[blueprint] ")
	b.WriteString(r.BlueprintAccount)
}
