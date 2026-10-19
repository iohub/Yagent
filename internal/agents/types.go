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
	Text        string               // 完整文本输出（程序化消费：输出解析/consolidation/错误消息）；不注入 Director LLM 上下文
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
