package agents

import (
	"log/slog"
	"strings"

	"yagent/internal/plasma"
)

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

	// —— P9b：账本需要的坐标与状态，全部**当场可得**：
	// bundle_hash / grade / escalated 来自加载期登记的 plasmaAgentInfo，note_ref 来自
	// 写便签的返回值，degraded 来自 agent_exit 采集的 handoff。写账本时不回查 store ——
	// 那会把一次纯追加变成一次可能失败的 IO，而失败路径上再叠失败最难查。
	BundleHash string
	Grade      plasma.Grade
	NoteRef    string
	Degraded   bool
	// Escalated 表示这次委派用到了一个**已获批准（或被 YOLO 放行）**的越权能力位。
	// 未批准时根本走不到执行（升级门先拦），所以它等价于"升级门这次放行了"。
	Escalated bool
	// FixtureRun 由**调用方**标记：夹具执行（5a/5b）为 true，生产为 false。
	// 评分只统计 false 的记录（§6.7），所以默认值必须是"生产"。
	FixtureRun bool
	// ErrText 是失败原因的人话版本（成功时空串）。只记 Err 布尔值会让"超时"与
	// "provider 报错"在统计上无法区分，而两者的修法完全不同。
	ErrText string
}

// SetDelegationMetricsHook 安装遥测钩子；nil 表示关闭。装配层（app）在 P9b 接线到
// `PlasmaTelemetrySink`（写 `~/.yagent/agents/telemetry.jsonl`）。
func (a *DirectorAgent) SetDelegationMetricsHook(h func(DelegationMetrics)) {
	a.onDelegationMetrics = h
}

// PlasmaTelemetrySink 把一次委派的可观测指标追加成 telemetry.jsonl 的一行（ADR-6）。
//
// 实现在 agents 而不是 app，是为了让 e2e 与生产**用同一份映射**：两份字段映射必然漂移，
// 而漂移的症状是"某个字段一直是零" —— §1.3 第 9 条要求 steps/tokens_in/stop_reason
// 都非零值，正是为了让这种漂移当场可见。
//
// 三个取向：
//   - 写入失败**不**阻断委派，但必须留可见账目（坑 43）：只 slog.Debug 等于没有账目；
//   - `fixture_run=false`：生产委派由这里写，夹具运行由 PlasmaFixtureRunner 强制标 true；
//   - 一行一次 `AppendTelemetry`（内部 O_APPEND）：进程被 kill -9 时丢的是"最后一次委派"，
//     而不是整个会话的账本。
func PlasmaTelemetrySink(root string) func(DelegationMetrics) {
	return func(m DelegationMetrics) {
		rec := plasma.TelemetryRecord{
			Agent:        m.Agent,
			BundleHash:   m.BundleHash,
			Grade:        m.Grade,
			DelegationID: m.DelegationID,
			StopReason:   m.StopReason,
			Steps:        m.Steps,
			TokensIn:     m.TokensIn,
			TokensOut:    m.TokensOut,
			Degraded:     m.Degraded,
			Escalated:    m.Escalated,
			NoteRef:      m.NoteRef,
			Err:          m.ErrText,
			FixtureRun:   m.FixtureRun,
		}
		if err := plasma.AppendTelemetry(root, rec); err != nil {
			slog.Warn("plasma: 遥测写入失败（委派继续，账本缺一行）",
				"agent", m.Agent, "delegation_id", m.DelegationID, "error", err,
				"修复", "检查 "+root+" 的写权限与磁盘空间")
		}
	}
}

// emitDelegationMetrics 把这个承载体的观测交给钩子。
//
// agent 优先取 outcome 记下的**执行者**（custom 分支）：`delegate_meta` 的工具名后缀是
// "meta"，但真正烧步数与 token 的是它刚生成的 Agent；按工具名反推会让账本里出现一条
// 语义错误的 "meta" 样本。空串（内建委派）才回落到工具名。
func (a *DirectorAgent) emitDelegationMetrics(toolName, callID, noteRef, agent string, err error, r AgentResult) {
	if a.onDelegationMetrics == nil {
		return
	}
	if strings.TrimSpace(agent) == "" {
		agent = strings.TrimPrefix(toolName, "delegate_")
	}
	m := DelegationMetrics{
		Tool:         toolName,
		Agent:        agent,
		DelegationID: callID,
		StopReason:   r.StopKind,
		Steps:        r.Steps,
		TokensIn:     r.Usage.PromptTokens,
		TokensOut:    r.Usage.CompletionTokens,
		HadResult:    r.Steps > 0 || r.Text != "",
		Err:          err != nil,
		Degraded:     r.Handoff != nil && r.Handoff.Degraded != "",
		NoteRef:      noteRef,
	}
	if err != nil {
		m.ErrText = err.Error()
	}
	// 坐标只在"这是 plasma 产物"时才有意义：内建委派没有 bundle，也不该在账本里
	// 冒充一份 hash（空串在 JSONL 里靠 omitempty 消失，读侧一眼能分辨）。
	if info, ok := a.plasmaAgentInfoOf(m.Agent); ok {
		m.BundleHash = info.Hash
		m.Grade = info.Grade
		m.Escalated = info.EscalationRequired && info.EscalationApproved
	}
	a.onDelegationMetrics(m)
}
