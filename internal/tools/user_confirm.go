package tools

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"codeactor/internal/messaging"
	"codeactor/internal/protocol"

	"github.com/google/uuid"
)

// UserConfirmManager manages user confirmation/interaction flows.
// It bridges agent tool calls with the interactive UI by publishing
// user_help_needed events and waiting for user_help_response events.
// Multiple agents share one manager instance registered as a MessageConsumer.
type UserConfirmManager struct {
	mu          sync.Mutex
	pending     map[string]chan string
	pendingHelp map[string]chan *protocol.UserHelpResponseData
	publisher   *messaging.MessagePublisher
}

// NewUserConfirmManager creates a new UserConfirmManager.
func NewUserConfirmManager() *UserConfirmManager {
	return &UserConfirmManager{
		pending:     make(map[string]chan string),
		pendingHelp: make(map[string]chan *protocol.UserHelpResponseData),
	}
}

// SetPublisher sets the message publisher used to publish user_help_needed events.
func (m *UserConfirmManager) SetPublisher(p *messaging.MessagePublisher) {
	m.publisher = p
}

// RequestConfirmation publishes a user_help_needed event and blocks until
// a user_help_response is received, or the context is cancelled, or timeout.
// Extra fields (tool_name, reason) are also included for structured dialog rendering.
func (m *UserConfirmManager) RequestConfirmation(ctx context.Context, question string, options string, extraFields ...map[string]interface{}) (string, error) {
	if m.publisher == nil {
		return "", fmt.Errorf("UserConfirmManager: publisher not set")
	}

	requestID := uuid.New().String()
	ch := make(chan string, 1)

	m.mu.Lock()
	m.pending[requestID] = ch
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		delete(m.pending, requestID)
		m.mu.Unlock()
	}()

	// Build content with question and optional extra fields
	content := map[string]interface{}{
		"question":   question,
		"options":    options,
		"request_id": requestID,
	}
	if len(extraFields) > 0 {
		for k, v := range extraFields[0] {
			content[k] = v
		}
	}

	m.publisher.Publish("user_help_needed", content, "Agent")

	slog.Info("UserConfirmManager waiting for user response", "request_id", requestID, "question", question)

	select {
	case response := <-ch:
		slog.Info("UserConfirmManager received response", "request_id", requestID)
		return response, nil
	case <-ctx.Done():
		return "", fmt.Errorf("user confirmation cancelled: %w", ctx.Err())
	}
}

// RequestUserHelp 发布一个扩展的用户帮助请求并阻塞等待用户响应。
// 与 RequestConfirmation 不同，它使用 protocol.UserHelpNeededData 作为参数，
// 支持三种交互模式（confirm/select/input）。
func (m *UserConfirmManager) RequestUserHelp(ctx context.Context, data *protocol.UserHelpNeededData) (*protocol.UserHelpResponseData, error) {
	if m.publisher == nil {
		return nil, fmt.Errorf("UserConfirmManager: publisher not set")
	}
	if data.RequestID == "" {
		data.RequestID = uuid.New().String()
	}
	ch := make(chan *protocol.UserHelpResponseData, 1)
	return m.requestUserHelpInternal(ctx, data, ch)
}

// requestUserHelpInternal 内部实现，发布扩展的帮助请求并等待响应
func (m *UserConfirmManager) requestUserHelpInternal(ctx context.Context, data *protocol.UserHelpNeededData, ch chan *protocol.UserHelpResponseData) (*protocol.UserHelpResponseData, error) {
	m.mu.Lock()
	m.pendingHelp[data.RequestID] = ch
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		delete(m.pendingHelp, data.RequestID)
		m.mu.Unlock()
	}()

	// 发布事件
	content := map[string]interface{}{
		"question":          data.Question,
		"context":           data.Context,
		"interaction_type":  string(data.InteractionType),
		"options":           data.Options,
		"default_value":     data.DefaultValue,
		"placeholder":       data.Placeholder,
		"allow_custom":      data.AllowCustom,
		"request_id":        data.RequestID,
	}

	m.publisher.Publish("user_help_needed", content, "Agent")

	slog.Info("UserConfirmManager waiting for user help response",
		"request_id", data.RequestID,
		"interaction_type", data.InteractionType,
		"question", data.Question,
	)

	select {
	case response := <-ch:
		slog.Info("UserConfirmManager received help response", "request_id", data.RequestID)
		return response, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("user help cancelled: %w", ctx.Err())
	}
}

// OnUserResponse delivers a user response to the waiting request channel.
func (m *UserConfirmManager) OnUserResponse(requestID, response string) {
	m.mu.Lock()
	ch, ok := m.pending[requestID]
	m.mu.Unlock()

	if ok {
		select {
		case ch <- response:
		default:
			slog.Warn("UserConfirmManager response channel full", "request_id", requestID)
		}
	} else {
		slog.Warn("UserConfirmManager no pending request for response", "request_id", requestID)
	}
}

// Consume implements messaging.MessageConsumer to receive user_help_response events
// from the message dispatcher and route them to the correct pending request.
func (m *UserConfirmManager) Consume(event *messaging.MessageEvent) error {
	if event.Type != "user_help_response" {
		return nil
	}

	content, ok := event.Content.(map[string]interface{})
	if !ok {
		return nil
	}

	// 提取所有字段（response 可能为空，取消时 response="" 但 cancelled=true）
	response, _ := content["response"].(string)
	cancelled, _ := content["cancelled"].(bool)
	isCustom, _ := content["is_custom"].(bool)
	interactionType, _ := content["interaction_type"].(string)

	// 提取 requestID
	requestID := ""
	if event.Metadata != nil {
		if id, ok := event.Metadata["request_id"].(string); ok {
			requestID = id
		}
	}
	if requestID == "" {
		if id, ok := content["request_id"].(string); ok {
			requestID = id
		}
	}

	if requestID == "" {
		return nil
	}

	// Bug 1 修复：加锁保护 m.pendingHelp 的读写
	m.mu.Lock()
	ch, ok := m.pendingHelp[requestID]
	m.mu.Unlock()

	if ok {
		respData := &protocol.UserHelpResponseData{
			Response:        response,
			InteractionType: protocol.InteractionType(interactionType),
			IsCustom:        isCustom,
			Cancelled:       cancelled,
			RequestID:       requestID,
		}

		select {
		case ch <- respData:
		default:
			slog.Warn("UserConfirmManager pendingHelp channel full", "request_id", requestID)
		}
		return nil
	}

	// 旧流程：用原有的 pending 匹配逻辑
	m.OnUserResponse(requestID, response)
	return nil
}
