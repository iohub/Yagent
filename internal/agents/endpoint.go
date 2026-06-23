package agents

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// MessageHandler 处理消息并返回响应
type MessageHandler func(ctx context.Context, msg Message) (Message, error)

// AgentEndpoint 提供 Agent 间的直接通信能力
// 每个 Agent 有一个 Endpoint，其他 Agent 可通过 AddressBook 发现并直接通信
type AgentEndpoint struct {
	agentID     string
	agentType   AgentType
	inbox       chan Message           // 异步消息通道
	priority    chan Message           // 高优先级消息通道
	handler     MessageHandler         // 消息处理函数
	addressBook *AddressBook           // 地址簿引用（用于反向查找）
	timeout     time.Duration          // 请求超时（默认 30s）

	// 请求-响应配对
	pending   map[string]chan Message // correlationID → response channel
	pendingMu sync.Mutex

	active atomic.Bool
	wg     sync.WaitGroup
	stopCh chan struct{}

	// 统计
	msgReceived  atomic.Int64
	msgSent      atomic.Int64
	requestCount atomic.Int64
}

// NewAgentEndpoint 创建 Agent 端点
func NewAgentEndpoint(agentID string, agentType AgentType, handler MessageHandler) *AgentEndpoint {
	return &AgentEndpoint{
		agentID:   agentID,
		agentType: agentType,
		handler:   handler,
		timeout:   30 * time.Second,
		pending:   make(map[string]chan Message),
		stopCh:    make(chan struct{}),
	}
}

// Start 启动端点处理循环
func (e *AgentEndpoint) Start(bufferSize int) error {
	if bufferSize <= 0 {
		bufferSize = 100
	}

	if !e.active.CompareAndSwap(false, true) {
		return fmt.Errorf("endpoint %s already started", e.agentID)
	}

	e.inbox = make(chan Message, bufferSize)
	e.priority = make(chan Message, bufferSize/2)

	e.wg.Add(2)
	go e.processLoop()
	go e.processPriorityLoop()

	slog.Info("Agent endpoint started", "agent_id", e.agentID, "type", e.agentType, "buffer", bufferSize)
	return nil
}

// Stop 优雅停止端点
func (e *AgentEndpoint) Stop() error {
	if !e.active.CompareAndSwap(true, false) {
		return nil // 已经停止
	}

	close(e.stopCh)

	// 等待处理循环结束
	done := make(chan struct{})
	go func() {
		e.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		slog.Info("Agent endpoint stopped", "agent_id", e.agentID)
	case <-time.After(5 * time.Second):
		slog.Warn("Agent endpoint stop timeout", "agent_id", e.agentID)
	}

	// 清理所有 pending 请求
	e.pendingMu.Lock()
	for id, ch := range e.pending {
		close(ch)
		delete(e.pending, id)
	}
	e.pendingMu.Unlock()

	return nil
}

// Send 异步发送消息（fire-and-forget）
func (e *AgentEndpoint) Send(msg Message) error {
	if !e.active.Load() {
		return fmt.Errorf("endpoint %s is not active", e.agentID)
	}

	msg.Timestamp = time.Now()

	select {
	case e.inbox <- msg:
		e.msgReceived.Add(1)
		return nil
	default:
		// inbox 满，尝试优先队列
		select {
		case e.priority <- msg:
			e.msgReceived.Add(1)
			return nil
		default:
			return fmt.Errorf("endpoint %s inbox full (%d)", e.agentID, len(e.inbox))
		}
	}
}

// Request 同步请求-响应
func (e *AgentEndpoint) Request(ctx context.Context, msg Message) (Message, error) {
	if !e.active.Load() {
		return Message{}, fmt.Errorf("endpoint %s is not active", e.agentID)
	}

	// 生成 correlation ID
	correlationID := uuid.New().String()
	msg.CorrelationID = correlationID
	msg.Timestamp = time.Now()
	msg.Type = "request"

	// 创建响应通道
	respCh := make(chan Message, 1)

	e.pendingMu.Lock()
	e.pending[correlationID] = respCh
	e.pendingMu.Unlock()

	// 清理函数
	defer func() {
		e.pendingMu.Lock()
		delete(e.pending, correlationID)
		e.pendingMu.Unlock()
	}()

	// 发送消息
	select {
	case e.inbox <- msg:
		e.requestCount.Add(1)
	default:
		select {
		case e.priority <- msg:
			e.requestCount.Add(1)
		default:
			return Message{}, fmt.Errorf("endpoint %s inbox full", e.agentID)
		}
	}

	// 等待响应（带超时）
	timeout := e.timeout
	if deadline, ok := ctx.Deadline(); ok {
		timeout = time.Until(deadline)
	}

	select {
	case resp := <-respCh:
		return resp, nil
	case <-time.After(timeout):
		return Message{}, fmt.Errorf("request to %s timed out after %v", e.agentID, timeout)
	case <-ctx.Done():
		return Message{}, ctx.Err()
	}
}

// IsAlive 检查端点是否活跃
func (e *AgentEndpoint) IsAlive() bool {
	return e.active.Load()
}

// AgentID 返回 Agent ID
func (e *AgentEndpoint) AgentID() string {
	return e.agentID
}

// AgentType 返回 Agent 类型
func (e *AgentEndpoint) AgentType() AgentType {
	return e.agentType
}

// GetInfo 返回端点信息
func (e *AgentEndpoint) GetInfo() EndpointInfo {
	inboxLen := 0
	priorityLen := 0
	if e.inbox != nil {
		inboxLen = len(e.inbox)
	}
	if e.priority != nil {
		priorityLen = len(e.priority)
	}

	return EndpointInfo{
		AgentID:       e.agentID,
		AgentType:     e.agentType,
		InboxDepth:    inboxLen,
		PriorityDepth: priorityLen,
		IsActive:      e.active.Load(),
		MsgReceived:   e.msgReceived.Load(),
		MsgSent:       e.msgSent.Load(),
		RequestCount:  e.requestCount.Load(),
	}
}

// SetTimeout 设置请求超时
func (e *AgentEndpoint) SetTimeout(d time.Duration) {
	if d > 0 {
		e.timeout = d
	}
}

// SetAddressBook 设置地址簿引用
func (e *AgentEndpoint) SetAddressBook(ab *AddressBook) {
	e.addressBook = ab
}

// --- 内部方法 ---

// processLoop 主处理循环
func (e *AgentEndpoint) processLoop() {
	defer e.wg.Done()

	for {
		select {
		case <-e.stopCh:
			return
		case msg := <-e.inbox:
			e.processMessage(msg)
		}
	}
}

// processPriorityLoop 优先级消息处理循环
func (e *AgentEndpoint) processPriorityLoop() {
	defer e.wg.Done()

	for {
		select {
		case <-e.stopCh:
			return
		case msg := <-e.priority:
			e.processMessage(msg)
		}
	}
}

// processMessage 处理单条消息
func (e *AgentEndpoint) processMessage(msg Message) {
	// 如果是响应（有匹配的 pending 请求），直接投递到响应通道
	if msg.Type == "response" && msg.CorrelationID != "" {
		e.pendingMu.Lock()
		ch, ok := e.pending[msg.CorrelationID]
		e.pendingMu.Unlock()
		if ok {
			select {
			case ch <- msg:
			default:
				slog.Warn("Response channel full, dropping response", "correlation_id", msg.CorrelationID)
			}
			return
		}
	}

	// 调用消息处理器
	if e.handler == nil {
		slog.Warn("No handler for endpoint", "agent_id", e.agentID)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), e.timeout)
	defer cancel()

	resp, err := e.handler(ctx, msg)
	if err != nil {
		slog.Error("Endpoint handler error", "agent_id", e.agentID, "error", err)
		// 如果有 CorrelationID，返回错误响应
		if msg.CorrelationID != "" {
			errResp := Message{
				Type:          "response",
				CorrelationID: msg.CorrelationID,
				Content:       fmt.Sprintf("Error: %v", err),
				SourceAgent:   e.agentID,
				TargetAgent:   msg.SourceAgent,
				Timestamp:     time.Now(),
			}
			e.sendResponse(msg.SourceAgent, errResp)
		}
		return
	}

	// 如果有 CorrelationID，返回响应
	if msg.CorrelationID != "" {
		resp.Type = "response"
		resp.CorrelationID = msg.CorrelationID
		resp.SourceAgent = e.agentID
		resp.TargetAgent = msg.SourceAgent
		resp.Timestamp = time.Now()
		e.sendResponse(msg.SourceAgent, resp)
	}
}

// sendResponse 发送响应回请求方
func (e *AgentEndpoint) sendResponse(targetAgent string, resp Message) {
	if e.addressBook == nil {
		return
	}

	targetEP, ok := e.addressBook.Lookup(targetAgent)
	if !ok {
		slog.Warn("Cannot send response: target endpoint not found", "target", targetAgent)
		return
	}

	// 直接写入目标端点的 pending 通道（不走 inbox）
	targetEP.pendingMu.Lock()
	ch, ok := targetEP.pending[resp.CorrelationID]
	targetEP.pendingMu.Unlock()
	if ok {
		select {
		case ch <- resp:
		default:
			slog.Warn("Target response channel full", "target", targetAgent)
		}
	}
}

// EndpointInfo 端点信息
type EndpointInfo struct {
	AgentID       string    `json:"agent_id"`
	AgentType     AgentType `json:"agent_type"`
	InboxDepth    int       `json:"inbox_depth"`
	PriorityDepth int       `json:"priority_depth"`
	IsActive      bool      `json:"is_active"`
	MsgReceived   int64     `json:"msg_received"`
	MsgSent       int64     `json:"msg_sent"`
	RequestCount  int64     `json:"request_count"`
}
