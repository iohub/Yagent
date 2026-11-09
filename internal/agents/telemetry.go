package agents

import "strings"

// DelegationMetrics 是一次委派结束后可观测的指标切片（ADR-6 + ADR-13）。
//
// 写入点是 directorToolRunner.Call 的 outcome 汇聚处 —— meta / custom / builtin 三条分支
// 唯一的共同出口。applyEnhancedCommander 与 recordDelegationOutcome 都只在 builtin 分支跑，
// 而 plasma Agent 走的是 custom 分支（delegateTo 提前 return），所以选那里会漏掉它。
type DelegationMetrics struct {
	Tool         string
	Agent        string
	DelegationID string
	StopReason   string
	Steps        int
	TokensIn     int64
	TokensOut    int64
	HadResult    bool
	Err          bool
}

// SetDelegationMetricsHook 安装遥测钩子；nil 表示关闭。装配层（app）在 P9 接线。
func (a *DirectorAgent) SetDelegationMetricsHook(h func(DelegationMetrics)) {
	a.onDelegationMetrics = h
}

func (a *DirectorAgent) emitDelegationMetrics(toolName, callID string, err error, r AgentResult) {
	if a.onDelegationMetrics == nil {
		return
	}
	a.onDelegationMetrics(DelegationMetrics{
		Tool:         toolName,
		Agent:        strings.TrimPrefix(toolName, "delegate_"),
		DelegationID: callID,
		StopReason:   r.StopKind,
		Steps:        r.Steps,
		TokensIn:     r.Usage.PromptTokens,
		TokensOut:    r.Usage.CompletionTokens,
		HadResult:    r.Steps > 0 || r.Text != "",
		Err:          err != nil,
	})
}
