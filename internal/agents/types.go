package agents

import (
	"context"
	"errors"

	"codeactor/internal/llm"
	"codeactor/internal/memory"
	"codeactor/internal/messaging"
	"codeactor/internal/messaging/bus"
	"codeactor/internal/messaging/peer"
)

// AgentResult 封装 sub-agent 的完整执行结果
type AgentResult struct {
	Text   string                // 最终文本输出（给 Conductor 作为 tool_result）
	Memory []memory.ChatMessage  // sub-agent 的完整内部对话历史（IsSubAgent=true，GroupID/ParentID 待 Conductor 填入）
}

// Agent defines the interface for all agents in the system.
type Agent interface {
	Name() string
	Run(ctx context.Context, input string) (AgentResult, error)
}

// BaseAgent holds common dependencies for agents.
type BaseAgent struct {
	LLM              llm.Engine
	Publisher        *messaging.MessagePublisher
	Peer             peer.AgentPeer        // 新增：P2P 通信能力
	LayeredMem       *memory.LayeredMemory // 分层记忆（Local + Shared）
	BlackboardAccess interface{
		Post(region string, author string, content map[string]interface{}, tags []string, references []string) (string, error)
		Read(region string, filter map[string]interface{}) ([]map[string]interface{}, error)
		Get(entryID string) (map[string]interface{}, bool)
	} // 黑板访问接口（为 nil 表示黑板未启用）

	// P2PSupplementEnabled 是否启用角色化 P2P Supplement
	// 开启后，sub-agent 的 system prompt 中会注入角色化的协作能力描述
	// 由 app.go 根据 EnhancedCommanderConfig.EnableP2PSupplement 设置
	P2PSupplementEnabled bool
}

// InitPeer 在共享 EventBus 上初始化 Agent 的 P2P 身份。
// 必须在 Agent 参与任何 P2P 通信前调用。
func (b *BaseAgent) InitPeer(id string, eventBus *bus.EventBus) error {
	if id == "" {
		return errors.New("baseagent: id must not be empty")
	}
	if eventBus == nil {
		return nil // nil bus = P2P 禁用，静默跳过
	}
	p, err := peer.NewAgentPeer(id, eventBus)
	if err != nil {
		return err
	}
	b.Peer = p
	return nil
}

// ClosePeer 释放 Peer 资源
func (b *BaseAgent) ClosePeer() error {
	if b.Peer != nil {
		return b.Peer.Close()
	}
	return nil
}

// FillCollaborationConfig 注入协作相关字段到 ExecutorConfig。
// 每个子 Agent 的 Run() 方法中应调用此方法。
func (ba *BaseAgent) FillCollaborationConfig(cfg *ExecutorConfig, agentName string) {
	cfg.Peer = ba.Peer
	cfg.AgentID = agentName
	cfg.BlackboardAccess = ba.BlackboardAccess
	cfg.EnableCollaboration = ba.Peer != nil
	// 新增：传递 P2P Supplement 标志
	cfg.P2PSupplementEnabled = ba.P2PSupplementEnabled
}
