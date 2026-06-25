package agents

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"codeactor/internal/messaging"
)

// ============================================================================
// AgentType 定义 — 标识不同 Agent 类型的字符串标签
// ============================================================================

// AgentType 标识 Agent 类型的字符串标签
type AgentType string

// String 返回 AgentType 的字符串表示
func (t AgentType) String() string {
	return string(t)
}

const (
	AgentTypeConductor AgentType = "conductor"
	AgentTypeRepo      AgentType = "repo"
	AgentTypeCoding    AgentType = "coding"
	AgentTypeChat      AgentType = "chat"
	AgentTypeMeta      AgentType = "meta"
	AgentTypeDevOps    AgentType = "devops"
	AgentTypeBrowser   AgentType = "browser"
	AgentTypeCustom    AgentType = "custom"
)

// ============================================================================
// Message 路由消息结构 — 用于 Agent 间通信
// ============================================================================

// Message 路由消息（用于 Agent 间通信）
type Message struct {
	ID            string                 // 唯一消息 ID
	Type          string                 // "request", "response", "event"
	SourceAgent   string                 // 来源 Agent ID（Name() 返回值）
	TargetAgent   string                 // 目标 Agent ID（Name() 返回值），为空时按规则匹配
	Content       string                 // 消息内容
	Metadata      map[string]interface{} // 附加元数据
	CorrelationID string                 // 用于请求-响应配对
	Priority      int                    // 0=normal, 1=high
	Timestamp     time.Time
}

// init 初始化默认时间戳
func (m *Message) init() {
	if m.Timestamp.IsZero() {
		m.Timestamp = time.Now()
	}
	if m.ID == "" {
		m.ID = fmt.Sprintf("msg-%d", time.Now().UnixNano())
	}
	if m.Priority == 0 {
		m.Priority = 0 // normal
	}
}

// ============================================================================
// RouteRule 路由规则
// ============================================================================

// RouteRule 定义消息路由规则
type RouteRule struct {
	Name      string             // 规则名称（用于日志和调试）
	Source    AgentType          // 来源 Agent 类型，空表示不限
	Target    AgentType          // 目标 Agent 类型
	Condition func(Message) bool // 自定义条件函数
	Priority  int                // 优先级，数字越大越优先
}

// ============================================================================
// RouteDecision 路由决策结果
// ============================================================================

// RouteDecision 路由决策结果
type RouteDecision struct {
	Target   AgentType   // 目标 Agent 类型
	ViaP2P   bool        // 是否通过 P2P 直连
	Endpoint interface{} // 端点信息（Phase 3: *AgentEndpoint）
	Reason   string      // 决策原因
}

// ============================================================================
// AgentRouter 消息路由器
// ============================================================================

// AgentRouter 负责 Agent 间的消息路由
//
// 支持两种模式：
// 1. Conductor 中转（现有模式，向后兼容）
// 2. P2P 直连（预留接口，Phase 3 启用）
type AgentRouter struct {
	// Agent 注册表
	agents map[AgentType]Agent

	// 路由规则列表
	rules []RouteRule

	// 兜底处理器（通常为 ConductorAgent）
	fallback Agent

	// 消息分发器（用于发布路由事件）
	dispatcher *messaging.MessageDispatcher

	// Phase 3: P2P 地址簿
	endpoints interface{} // *AddressBook

	// 并发控制
	mu sync.RWMutex

	// 路由统计
	routeCount    int64
	p2pCount      int64
	fallbackCount int64
}

// ============================================================================
// 构造函数
// ============================================================================

// NewRouter 创建新的 Agent 路由器
func NewRouter(dispatcher *messaging.MessageDispatcher) *AgentRouter {
	return &AgentRouter{
		agents:     make(map[AgentType]Agent),
		dispatcher: dispatcher,
		rules:      make([]RouteRule, 0),
	}
}

// ============================================================================
// Agent 注册/注销
// ============================================================================

// RegisterAgent 注册 Agent 到路由器
func (r *AgentRouter) RegisterAgent(agentType AgentType, agent Agent) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if agent == nil {
		return fmt.Errorf("cannot register nil agent for type %s", agentType)
	}

	// 如果已存在，记录警告
	if existing, exists := r.agents[agentType]; exists {
		slog.Warn("Replacing existing agent",
			"type", agentType,
			"old_name", existing.Name(),
			"new_name", agent.Name(),
		)
	}

	r.agents[agentType] = agent
	slog.Info("Agent registered in router", "type", agentType, "name", agent.Name())
	return nil
}

// UnregisterAgent 从路由器注销 Agent
func (r *AgentRouter) UnregisterAgent(agentType AgentType) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if agent, exists := r.agents[agentType]; exists {
		slog.Info("Agent unregistered from router", "type", agentType, "name", agent.Name())
	}
	delete(r.agents, agentType)
}

// ============================================================================
// 路由决策
// ============================================================================

// ResolveTarget 解析消息的目标 Agent 类型
//
// 解析策略（按优先级）：
// 1. 如果消息指定了目标 Agent ID，直接查找匹配
// 2. 按规则匹配（优先级降序）
// 3. 默认策略：根据消息类型/内容选择 Agent
// 4. 无匹配则返回空决策（走兜底）
func (r *AgentRouter) ResolveTarget(msg Message) (RouteDecision, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// 1. 如果消息指定了目标 Agent ID，直接查找
	if msg.TargetAgent != "" {
		for agentType, agent := range r.agents {
			if agent.Name() == msg.TargetAgent {
				return RouteDecision{
					Target: agentType,
					ViaP2P: false,
					Reason: "direct target match",
				}, nil
			}
		}
		// 没找到指定 Agent，返回空决策（由 Route 方法处理兜底）
		return RouteDecision{
			ViaP2P: false,
			Reason: fmt.Sprintf("target agent %q not found, fallback to conductor", msg.TargetAgent),
		}, nil
	}

	// 2. 按规则匹配（优先级降序）
	sortedRules := r.sortedRules()
	for _, rule := range sortedRules {
		// 检查来源类型是否匹配
		if rule.Source != "" && rule.Source != r.guessSourceType(msg) {
			continue
		}
		// 检查目标类型是否已注册
		if _, exists := r.agents[rule.Target]; !exists {
			continue
		}
		// 检查自定义条件
		if rule.Condition != nil && !rule.Condition(msg) {
			continue
		}
		return RouteDecision{
			Target: rule.Target,
			ViaP2P: false,
			Reason: fmt.Sprintf("rule: %s", rule.Name),
		}, nil
	}

	// 3. 默认策略：根据消息类型选择 Agent
	agentType := r.defaultRouting(msg)
	if agentType != "" {
		if _, exists := r.agents[agentType]; exists {
			return RouteDecision{
				Target: agentType,
				ViaP2P: false,
				Reason: "default routing",
			}, nil
		}
	}

	// 4. 无匹配，走兜底
	return RouteDecision{
		ViaP2P: false,
		Reason: "no route rule matched, fallback to conductor",
	}, nil
}

// Route 异步路由消息到目标 Agent
func (r *AgentRouter) Route(ctx context.Context, msg Message) error {
	// 初始化消息
	msg.init()

	// 解析目标
	decision, err := r.ResolveTarget(msg)
	if err != nil {
		return fmt.Errorf("route resolution failed: %w", err)
	}

	// 更新统计
	r.mu.Lock()
	r.routeCount++
	if decision.ViaP2P {
		r.p2pCount++
	} else {
		r.fallbackCount++
	}
	r.mu.Unlock()

	// 发布路由事件
	if r.dispatcher != nil {
		r.publishRouteEvent(msg, decision)
	}

	// Conductor 兜底路径
	if !decision.ViaP2P && r.fallback != nil {
		// 通用兜底：直接调用 Agent 的 Run 方法
		// 注意：ConductorAgent 的 Run 签名与 Agent 接口不一致（多一个 *memory.ConversationMemory 参数），
		// 所以这里不能直接使用类型断言。如果需要 Conductor 中转路由，
		// 应在 ConductorAgent 拆分后通过额外的 RouterHandler 接口实现。
		//
		// 当前实现：直接调用已注册目标 Agent 的 Run 方法
		r.mu.RLock()
		agent, exists := r.agents[decision.Target]
		r.mu.RUnlock()
		if exists {
			_, err := agent.Run(ctx, msg.Content)
			return err
		}
	}

	// P2P 路径（预留，Phase 3）
	if decision.ViaP2P {
		return fmt.Errorf("P2P routing not yet implemented")
	}

	return ErrNoRouteAvailable
}

// RouteSync 同步路由（等待响应）
func (r *AgentRouter) RouteSync(ctx context.Context, msg Message) (Message, error) {
	// 初始化消息
	msg.init()

	// 解析目标
	decision, err := r.ResolveTarget(msg)
	if err != nil {
		return Message{}, err
	}

	if decision.Target == "" {
		return Message{}, fmt.Errorf("no route target available: %s", decision.Reason)
	}

	r.mu.RLock()
	agent, exists := r.agents[decision.Target]
	r.mu.RUnlock()

	if !exists {
		return Message{}, fmt.Errorf("agent %s not found", decision.Target)
	}

	// 执行 Agent
	result, err := agent.Run(ctx, msg.Content)
	if err != nil {
		return Message{}, err
	}

	// 构建响应消息
	return Message{
		ID:            fmt.Sprintf("resp-%d", time.Now().UnixNano()),
		Content:       result.Text,
		SourceAgent:   decision.Target.String(),
		TargetAgent:   msg.SourceAgent,
		Type:          "response",
		CorrelationID: msg.CorrelationID,
		Metadata:      map[string]interface{}{"status": "success"},
	}, nil
}

// ============================================================================
// 规则管理
// ============================================================================

// AddRule 添加路由规则
func (r *AgentRouter) AddRule(rule RouteRule) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.rules = append(r.rules, rule)
	slog.Info("Route rule added", "name", rule.Name, "source", rule.Source, "target", rule.Target, "priority", rule.Priority)
}

// SetFallback 设置兜底处理器
func (r *AgentRouter) SetFallback(fb Agent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fallback = fb
}

// ============================================================================
// Agent 查询
// ============================================================================

// ListAgents 列出所有已注册的 Agent 类型
func (r *AgentRouter) ListAgents() []AgentType {
	r.mu.RLock()
	defer r.mu.RUnlock()

	types := make([]AgentType, 0, len(r.agents))
	for t := range r.agents {
		types = append(types, t)
	}
	return types
}

// GetAgent 获取指定类型的 Agent
func (r *AgentRouter) GetAgent(agentType AgentType) (Agent, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	agent, ok := r.agents[agentType]
	return agent, ok
}

// HasAgent 检查是否已注册指定类型的 Agent
func (r *AgentRouter) HasAgent(agentType AgentType) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.agents[agentType]
	return ok
}

// ============================================================================
// 统计信息
// ============================================================================

// GetStats 获取路由统计信息
func (r *AgentRouter) GetStats() map[string]int64 {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return map[string]int64{
		"route_count":    r.routeCount,
		"p2p_count":      r.p2pCount,
		"fallback_count": r.fallbackCount,
	}
}

// ============================================================================
// 内部辅助方法
// ============================================================================

// sortedRules 按优先级降序返回规则副本
func (r *AgentRouter) sortedRules() []RouteRule {
	if len(r.rules) == 0 {
		return nil
	}

	sorted := make([]RouteRule, len(r.rules))
	copy(sorted, r.rules)

	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Priority > sorted[j].Priority
	})

	return sorted
}

// guessSourceType 从消息判断来源 Agent 类型
func (r *AgentRouter) guessSourceType(msg Message) AgentType {
	for agentType, agent := range r.agents {
		if agent.Name() == msg.SourceAgent {
			return agentType
		}
	}
	return ""
}

// defaultRouting 默认路由策略
//
// 基于消息关键字或类型推断目标 Agent
func (r *AgentRouter) defaultRouting(msg Message) AgentType {
	// 基于消息内容关键字判断（简单启发式）
	content := msg.Content

	// 检查是否为代码相关任务
	if containsKeywords(content, []string{"code", "program", "implement", "function", "class", "file", "edit", "create file", "write code"}) {
		if _, ok := r.agents[AgentTypeCoding]; ok {
			return AgentTypeCoding
		}
	}

	// 检查是否为 repo 分析相关任务
	if containsKeywords(content, []string{"repo", "repository", "codebase", "analyze", "explore", "structure", "architecture", "dependency"}) {
		if _, ok := r.agents[AgentTypeRepo]; ok {
			return AgentTypeRepo
		}
	}

	// 检查是否为运维相关任务
	if containsKeywords(content, []string{"deploy", "server", "process", "log", "monitor", "disk", "network", "config", "install", "package"}) {
		if _, ok := r.agents[AgentTypeDevOps]; ok {
			return AgentTypeDevOps
		}
	}

	// 检查是否为浏览器相关任务
	if containsKeywords(content, []string{"browser", "web", "url", "website", "screenshot", "scrape", "navigate", "login"}) {
		if _, ok := r.agents[AgentTypeBrowser]; ok {
			return AgentTypeBrowser
		}
	}

	// 检查是否为对话/问答
	if containsKeywords(content, []string{"explain", "what is", "how to", "why", "tell me", "help me understand", "question"}) {
		if _, ok := r.agents[AgentTypeChat]; ok {
			return AgentTypeChat
		}
	}

	return AgentType("")
}

// containsKeywords 检查文本是否包含任一关键词（不区分大小写）
func containsKeywords(text string, keywords []string) bool {
	lower := lowercase(text)
	for _, kw := range keywords {
		if contains(lower, lowercase(kw)) {
			return true
		}
	}
	return false
}

// 简单的字符串函数，避免引入 strings 包重复导入（已在上层导入）
func lowercase(s string) string {
	result := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		result[i] = c
	}
	return string(result)
}

func contains(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	if len(substr) > len(s) {
		return false
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// publishRouteEvent 发布路由事件到消息分发器
func (r *AgentRouter) publishRouteEvent(msg Message, decision RouteDecision) {
	if r.dispatcher == nil {
		return
	}

	eventData := map[string]interface{}{
		"message_id":   msg.ID,
		"message_type": msg.Type,
		"source":       msg.SourceAgent,
		"target":       decision.Target.String(),
		"via_p2p":      decision.ViaP2P,
		"reason":       decision.Reason,
	}

	r.dispatcher.PublishCompat(&messaging.Event{
		Type:      "agent_routed",
		Source:    "agent_router",
		Content:   fmt.Sprintf("Routed %s to %s: %s", msg.SourceAgent, decision.Target, decision.Reason),
		Priority:  convertPriority(msg.Priority),
		Metadata:  eventData,
		Timestamp: msg.Timestamp,
	})
}

// ============================================================================
// 错误定义
// ============================================================================

// ErrNoRouteAvailable 无可用的路由
var ErrNoRouteAvailable = fmt.Errorf("no route available for message")

// ============================================================================
// 工具函数
// ============================================================================

// convertPriority 将内部优先级（0=normal, 1=high）转换为 messaging.Priority
func convertPriority(priority int) messaging.Priority {
	switch priority {
	case 1:
		return messaging.PriorityHigh
	default:
		return messaging.PriorityNormal
	}
}

// ============================================================================
// ConductorAgent 扩展方法（通过类型断言调用）
// ============================================================================

// handleRoutedMessage 是 ConductorAgent 的路由消息处理方法
//
// 注意：此方法不在此文件中实现，而是需要在 ConductorAgent 中通过
// 类型断言或接口扩展方式提供。Router 通过类型断言 *ConductorAgent
// 来调用此方法。
//
// 签名约定：
//   func (a *ConductorAgent) handleRoutedMessage(ctx context.Context, msg Message, target AgentType) error
//
// 该方法在 ConductorAgent 拆分完成后实现。
