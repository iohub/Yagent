package agents

import (
	"context"

	"yagent/internal/artifact"
	"yagent/internal/blackboard"
	"yagent/internal/blueprint"
	"yagent/internal/llm"
	"yagent/internal/memory"
)

// AgentResult 封装 sub-agent 的完整执行结果
type AgentResult struct {
	Text        string               // 完整文本输出（程序化消费：输出解析/RepoSummary/错误消息）；不注入 Director LLM 上下文
	Summary     string               // 分级摘要（给 Director 上下文；未截断/未落盘时等于交付全文）
	ArtifactRef *artifact.Ref        // 完整结果落盘引用（按 ID 回查）；nil 表示未截断/未落盘
	Memory      []memory.ChatMessage // sub-agent 的完整内部对话历史（IsSubAgent=true，GroupID/ParentID 待 Director 填入）
	Handoff     *blackboard.Handoff  // 结构化交接（agent_exit 采集）；nil = 没走 agent_exit 收尾
	// Deliverable 是交付物全文（agent_exit 收尾时从最后一条 assistant 消息取回）。
	// Text 在超预算路径上是摘要，所以便签正文必须读这个字段，否则修完 R1 又在这儿丢一次。
	Deliverable string
	// Blueprint 是 agent_exit 的 `blueprint[]` 提交（仓库级散文，落盘走 Repo 的完成接缝）。
	// 空 = 这一轮没提交过结论。它刻意**不并进 Handoff**：那张便签的正文已经是交付物，
	// 把同一些句子再写进 front-matter 就是本仓第 N 份"三份真相并行"。
	Blueprint []blueprint.FoldItem
	// EvidenceFiles 是本次 run 真打开过的文件路径（入参侧 ∪ 出参侧，§7.5 第 2 条）。
	// 只有 Blueprint 非空时才计算 —— 绝大多数委派不提交蓝图，不该为它们扫一遍 History。
	EvidenceFiles []string
	// BlueprintAccount 是"这一轮的蓝图写通道发生了什么"的一句人话账目（提交几条 / 落地几条 /
	// 逐条拒收原因 / 本轮读过几片）。它由 Repo 完成接缝上的 fold 生成，只在那里赋值。
	// 走 AgentResult 而不是只进 slog：`agent_exit` 是终止点，拒收若不同行回到 Director，
	// 模型永远不知道自己白交了四条、Director 看到的只是"这次委派成功了"（2026-10-20 实测）。
	// 空串 = 无话可说（蓝图未装配、或这一轮本来就不该有蓝图）。
	BlueprintAccount string
}

// Agent defines the interface for all agents in the system.
type Agent interface {
	Name() string
	Run(ctx context.Context, input string) (AgentResult, error)
}

// BaseAgent holds common dependencies for agents.
type BaseAgent struct {
	LLM       llm.Engine
	Publisher EventBus
}
