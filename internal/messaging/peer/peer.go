package peer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"codeactor/internal/messaging/bus"
)

// AgentPeer 赋予每个 Agent P2P 通信能力。
type AgentPeer interface {
	ID() string
	Subscribe(topic string, handler bus.EventHandler) (bus.Subscription, error)
	SubscribeWithFilter(topic string, filter bus.EventFilter, handler bus.EventHandler) (bus.Subscription, error)
	Publish(ctx context.Context, topic string, payload []byte, opts ...PublishOption) error
	Request(ctx context.Context, topic string, targetID string, payload []byte, timeout time.Duration) ([]byte, error)
	RegisterRequestHandler(topic string, handler RequestHandler) error
	Respond(ctx context.Context, correlationID string, payload []byte) error
	Close() error
}

// RequestHandler 处理接收到的请求并返回响应 payload
type RequestHandler func(ctx context.Context, event *bus.Event) ([]byte, error)

// 确保 *agentPeerImpl 实现 AgentPeer
var _ AgentPeer = (*agentPeerImpl)(nil)

// pendingRequest 跟踪同步请求
type pendingRequest struct {
	responseCh chan *bus.Event
	createdAt  time.Time
	targetID   string
}

type agentPeerImpl struct {
	id      string
	bus     *bus.EventBus

	reqMu     sync.Mutex
	pending   map[string]*pendingRequest

	handlerMu   sync.RWMutex
	reqHandlers map[string]RequestHandler

	subMu         sync.Mutex
	subscriptions []bus.Subscription

	versionCounter atomic.Int64
	closed         atomic.Bool
}

// NewAgentPeer 创建绑定到 EventBus 的 Agent Peer
func NewAgentPeer(id string, b *bus.EventBus) (AgentPeer, error) {
	if id == "" {
		return nil, errors.New("peer: id cannot be empty")
	}
	if b == nil {
		return nil, errors.New("peer: eventbus cannot be nil")
	}
	p := &agentPeerImpl{
		id:          id,
		bus:         b,
		pending:     make(map[string]*pendingRequest),
		reqHandlers: make(map[string]RequestHandler),
	}

	// 订阅响应通道
	respTopic := fmt.Sprintf("_response.%s", id)
	respSub, err := b.Subscribe(respTopic, id+":resp", 128, nil, p.handleResponse)
	if err != nil {
		return nil, fmt.Errorf("peer: subscribe response topic: %w", err)
	}
	p.subMu.Lock()
	p.subscriptions = append(p.subscriptions, respSub)
	p.subMu.Unlock()

	// 订阅请求通道
	reqTopic := fmt.Sprintf("_request.%s", id)
	reqSub, err := b.Subscribe(reqTopic, id+":req", 64, nil, p.handleRequest)
	if err != nil {
		respSub.Unsubscribe()
		return nil, fmt.Errorf("peer: subscribe request topic: %w", err)
	}
	p.subMu.Lock()
	p.subscriptions = append(p.subscriptions, reqSub)
	p.subMu.Unlock()

	return p, nil
}

func (p *agentPeerImpl) ID() string { return p.id }

func (p *agentPeerImpl) Subscribe(topic string, handler bus.EventHandler) (bus.Subscription, error) {
	subID := p.id + ":" + topic
	sub, err := p.bus.Subscribe(topic, subID, 64, nil, handler)
	if err != nil {
		return nil, err
	}
	p.subMu.Lock()
	p.subscriptions = append(p.subscriptions, sub)
	p.subMu.Unlock()
	return sub, nil
}

func (p *agentPeerImpl) SubscribeWithFilter(topic string, filter bus.EventFilter, handler bus.EventHandler) (bus.Subscription, error) {
	subID := p.id + ":" + topic + ":filtered"
	sub, err := p.bus.Subscribe(topic, subID, 64, filter, handler)
	if err != nil {
		return nil, err
	}
	p.subMu.Lock()
	p.subscriptions = append(p.subscriptions, sub)
	p.subMu.Unlock()
	return sub, nil
}

func (p *agentPeerImpl) Publish(ctx context.Context, topic string, payload []byte, opts ...PublishOption) error {
	if p.closed.Load() {
		return errors.New("peer: closed")
	}
	cfg := &PublishConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	ev := &bus.Event{
		ID:            generateID(),
		CorrelationID: cfg.CorrelationID,
		Topic:         topic,
		Source:        p.id,
		Target:        cfg.Target,
		Payload:       payload,
		Headers:       cfg.Headers,
		Timestamp:     time.Now(),
		Type:          bus.EventPublish,
		Version:       cfg.Version,
	}
	if ev.Version == 0 {
		ev.Version = p.versionCounter.Add(1)
	}
	return p.bus.Publish(ctx, ev)
}

func (p *agentPeerImpl) Request(ctx context.Context, topic string, targetID string, payload []byte, timeout time.Duration) ([]byte, error) {
	if p.closed.Load() {
		return nil, errors.New("peer: closed")
	}
	if targetID == "" {
		return nil, errors.New("peer: targetID required for Request")
	}

	correlationID := generateID()
	respCh := make(chan *bus.Event, 1)

	p.reqMu.Lock()
	p.pending[correlationID] = &pendingRequest{
		responseCh: respCh,
		createdAt:  time.Now(),
		targetID:   targetID,
	}
	p.reqMu.Unlock()

	defer func() {
		p.reqMu.Lock()
		delete(p.pending, correlationID)
		p.reqMu.Unlock()
	}()

	// 发布请求到目标的 _request.<targetID> topic
	reqTopic := fmt.Sprintf("_request.%s", targetID)
	ev := &bus.Event{
		ID:            generateID(),
		CorrelationID: correlationID,
		Topic:         reqTopic,
		Source:        p.id,
		Target:        targetID,
		Payload:       payload,
		Headers: map[string]string{
			"reply-to":       fmt.Sprintf("_response.%s", p.id),
			"original-topic": topic,
		},
		Timestamp: time.Now(),
		Type:      bus.EventRequest,
	}

	if err := p.bus.Publish(ctx, ev); err != nil {
		return nil, fmt.Errorf("peer: publish request: %w", err)
	}

	// 等待响应
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case resp := <-respCh:
		if resp.Type == bus.EventError {
			return nil, fmt.Errorf("peer: remote error: %s", string(resp.Payload))
		}
		return resp.Payload, nil
	case <-timer.C:
		return nil, fmt.Errorf("peer: request timeout after %v (target=%s, topic=%s)", timeout, targetID, topic)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// RegisterRequestHandler 注册请求处理函数
func (p *agentPeerImpl) RegisterRequestHandler(topic string, handler RequestHandler) error {
	p.handlerMu.Lock()
	defer p.handlerMu.Unlock()
	if _, exists := p.reqHandlers[topic]; exists {
		return fmt.Errorf("peer: handler already registered for topic %q", topic)
	}
	p.reqHandlers[topic] = handler
	return nil
}

// Respond 发送响应
func (p *agentPeerImpl) Respond(ctx context.Context, correlationID string, payload []byte) error {
	p.reqMu.Lock()
	pending, ok := p.pending[correlationID]
	p.reqMu.Unlock()
	if !ok {
		return fmt.Errorf("peer: no pending request for correlation %s", correlationID)
	}
	resp := &bus.Event{
		ID:            generateID(),
		CorrelationID: correlationID,
		Topic:         fmt.Sprintf("_response.%s", pending.targetID),
		Source:        p.id,
		Target:        pending.targetID,
		Payload:       payload,
		Timestamp:     time.Now(),
		Type:          bus.EventResponse,
	}
	return p.bus.Publish(ctx, resp)
}

func (p *agentPeerImpl) handleResponse(ctx context.Context, ev *bus.Event) error {
	p.reqMu.Lock()
	pr, ok := p.pending[ev.CorrelationID]
	p.reqMu.Unlock()
	if !ok {
		return nil // 过期的响应，静默丢弃
	}
	select {
	case pr.responseCh <- ev:
	default:
	}
	return nil
}

func (p *agentPeerImpl) handleRequest(ctx context.Context, ev *bus.Event) error {
	origTopic := ev.Headers["original-topic"]
	if origTopic == "" {
		return nil
	}

	p.handlerMu.RLock()
	handler, ok := p.reqHandlers[origTopic]
	p.handlerMu.RUnlock()

	if !ok {
		return p.sendResponse(ev, nil, fmt.Errorf("no handler for topic %q", origTopic))
	}

	// 异步处理，避免阻塞投递 goroutine
	go func() {
		reqCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		payload, err := handler(reqCtx, ev)
		if err != nil {
			_ = p.sendResponse(ev, nil, err)
			return
		}
		_ = p.sendResponse(ev, payload, nil)
	}()
	return nil
}

func (p *agentPeerImpl) sendResponse(reqEv *bus.Event, payload []byte, err error) error {
	respType := bus.EventResponse
	if err != nil {
		respType = bus.EventError
		payload = []byte(err.Error())
	}
	resp := &bus.Event{
		ID:            generateID(),
		CorrelationID: reqEv.CorrelationID,
		Topic:         reqEv.Headers["reply-to"],
		Source:        p.id,
		Target:        reqEv.Source,
		Payload:       payload,
		Timestamp:     time.Now(),
		Type:          respType,
	}
	return p.bus.Publish(context.Background(), resp)
}

// Close 关闭 Peer
func (p *agentPeerImpl) Close() error {
	if !p.closed.CompareAndSwap(false, true) {
		return nil
	}
	p.subMu.Lock()
	for _, sub := range p.subscriptions {
		_ = sub.Unsubscribe()
	}
	p.subscriptions = nil
	p.subMu.Unlock()

	p.reqMu.Lock()
	for _, pr := range p.pending {
		close(pr.responseCh)
	}
	p.pending = make(map[string]*pendingRequest)
	p.reqMu.Unlock()
	return nil
}

func generateID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
