package agents

import (
	"fmt"
	"strings"
	"sync"

	"yagent/internal/agents/director"
	"yagent/internal/artifact"
)

// 本文件实现"委派返回物分级"基础设施（Phase：第一批次，纯加法）：
//   - FinalizeResult：统一构造函数，完整结果落盘为 artifact，生成分级摘要；
//   - FormatForDirector：注入 Director 上下文的统一格式文本（格式单源）。
//
// 旧 AgentResult.Text 字段本批次仍在：FinalizeResult 同时设置 Text = Summary，
// 保证现有消费点行为不变；下一批次（原子切换）删除 Text 后自动收敛。

// projectPathProvider 返回当前项目路径（由 DirectorAgent.env.ProjectPath 提供）。
// computeProjectID() 是 DirectorAgent 私有方法（director.go），包级函数无法访问，
// 因此采用包级提供者注入：下一批次接线时由 DirectorAgent 调用 SetProjectPathProvider
// 注入；未注入时返回空串（director.ComputeProjectID("") == "default"）。
var (
	projectPathMu   sync.RWMutex
	projectPathFunc func() string
)

// SetProjectPathProvider 注入项目路径提供者（并发安全；nil 忽略）。
// 供下一批次接线：SetProjectPathProvider(directorAgent.env.ProjectPath)。
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

// FinalizeResult 统一构造函数：完整结果落盘为 artifact，生成分级摘要。
// Save 失败时降级为全文摘要（信息完整性优先，绝不静默丢内容，不返回 error、不阻断委派）。
func FinalizeResult(agentName, task string, exec ExecutorResult) AgentResult {
	mem := ConvertLLMHistoryToMemory(exec.History)

	// kill-switch 或空文本：回退旧行为，不落盘。
	if artifact.Disabled() || exec.Text == "" {
		return AgentResult{
			Text:    exec.Text,
			Summary: exec.Text,
			Memory:  mem,
		}
	}

	// 预算内：全文即摘要，零行为变化，不落盘。
	if artifact.EstTokens(exec.Text) <= artifact.DefaultBudgetTokens {
		return AgentResult{
			Text:    exec.Text,
			Summary: exec.Text,
			Memory:  mem,
		}
	}

	// 超预算：先落盘完整结果拿 Ref，再 Reduce 生成带 ID 的分级摘要。
	projectID := director.ComputeProjectID(currentProjectPath())
	store := artifact.DefaultStore()
	ref, err := store.Save(projectID, agentName, task, "", exec.Text)
	if err != nil {
		// Save 失败：降级为全文摘要，不阻断委派。
		return AgentResult{
			Text:    exec.Text,
			Summary: exec.Text,
			Memory:  mem,
		}
	}

	summary, _ := artifact.Reduce(exec.Text, ref.ID, artifact.DefaultBudgetTokens)
	// 把带 ID 的摘要回写进 artifact（Load -> 改 Summary -> 覆盖写回同路径）。
	// 失败仅影响 artifact 内 summary 元数据完整性，不影响返回值，不阻断委派。
	_ = store.UpdateSummary(projectID, ref.ID, summary)

	return AgentResult{
		Text:        summary, // 临时兼容字段（= Summary），下一批次删除后自动收敛
		Summary:     summary,
		ArtifactRef: &ref,
		Memory:      mem,
	}
}

// FormatForDirector 生成注入 Director 上下文的统一格式文本（两处消费点共用，格式单源）。
//   - 无 ArtifactRef：保持旧格式 "[Sub-Agent Result: {toolName}]\n{Text}"（与
//     director.go injectSubAgentMemory 现有格式一致）；
//   - 有 ArtifactRef：头部 + artifact 元信息 + 摘要 + read_artifact 取回提示。
func FormatForDirector(toolName string, r AgentResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Sub-Agent Result: %s]\n", toolName)
	if r.ArtifactRef == nil {
		b.WriteString(r.Summary)
		return b.String()
	}
	fmt.Fprintf(&b, "artifact: %s (%d chars total)\n", r.ArtifactRef.ID, r.ArtifactRef.CharCount)
	// TrimRight：Summary 尾部可能自带换行（Reduce 标记行 \n 结尾），
	// 统一去除后接单空行，保证格式确定性、不产生多余空行。
	b.WriteString(strings.TrimRight(r.Summary, "\n"))
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "[Output truncated to fit context. Full result stored as artifact %s. Call read_artifact(id=\"%s\") if you need details beyond the summary above.]",
		r.ArtifactRef.ID, r.ArtifactRef.ID)
	return b.String()
}
