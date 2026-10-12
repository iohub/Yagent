package tools

import (
	"context"
	"strings"
	"testing"

	"yagent/internal/llm"
)

// designMockEngine 是 DesignTool 测试用的最小 Engine：
// 记录收到的 messages 与 CallOptions，返回固定内容。
type designMockEngine struct {
	gotMessages []llm.Message
	gotOpts     *llm.CallOptions
	calls       int
}

func (m *designMockEngine) GenerateContent(ctx context.Context, messages []llm.Message, tools []llm.ToolDef, opts *llm.CallOptions) (*llm.Response, error) {
	m.calls++
	m.gotMessages = messages
	m.gotOpts = opts
	return &llm.Response{Choices: []llm.Choice{{Content: "design plan"}}}, nil
}

func (m *designMockEngine) Model() string         { return "design-mock-model" }
func (m *designMockEngine) CloseIdleConnections() {}

func TestDesignTool_Execute_MissingContext(t *testing.T) {
	engine := &designMockEngine{}
	tool := NewDesignTool(engine)

	if _, err := tool.Execute(context.Background(), map[string]interface{}{
		"goal": "produce a design",
	}); err == nil {
		t.Fatal("missing context should return an error")
	}
	if engine.calls != 0 {
		t.Errorf("LLM should not be called: calls = %d", engine.calls)
	}
}

func TestDesignTool_Execute_MissingGoal(t *testing.T) {
	engine := &designMockEngine{}
	tool := NewDesignTool(engine)

	if _, err := tool.Execute(context.Background(), map[string]interface{}{
		"context": "some context",
	}); err == nil {
		t.Fatal("missing goal should return an error")
	}
	if engine.calls != 0 {
		t.Errorf("LLM should not be called: calls = %d", engine.calls)
	}
}

func TestDesignTool_Execute_EmptyParams(t *testing.T) {
	engine := &designMockEngine{}
	tool := NewDesignTool(engine)

	// 空串视同缺失：两个参数同理
	if _, err := tool.Execute(context.Background(), map[string]interface{}{
		"context": "",
		"goal":    "produce a design",
	}); err == nil {
		t.Fatal("empty context should return an error")
	}
	if _, err := tool.Execute(context.Background(), map[string]interface{}{
		"context": "some context",
		"goal":    "",
	}); err == nil {
		t.Fatal("empty goal should return an error")
	}
	// 非字符串类型同样拒绝
	if _, err := tool.Execute(context.Background(), map[string]interface{}{
		"context": 42,
		"goal":    "produce a design",
	}); err == nil {
		t.Fatal("non-string context should return an error")
	}
	if engine.calls != 0 {
		t.Errorf("LLM should not be called: calls = %d", engine.calls)
	}
}

func TestDesignTool_Execute_BuildsMessages(t *testing.T) {
	engine := &designMockEngine{}
	tool := NewDesignTool(engine)

	got, err := tool.Execute(context.Background(), map[string]interface{}{
		"context": "the task context with file paths",
		"goal":    "the design goal",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if engine.calls != 1 {
		t.Fatalf("calls = %d, want 1", engine.calls)
	}
	if len(engine.gotMessages) != 2 {
		t.Fatalf("messages = %d, want 2", len(engine.gotMessages))
	}

	sys := engine.gotMessages[0]
	if sys.Role != llm.RoleSystem || sys.Content != getDesignSystemPrompt() {
		t.Error("first message should be the design system prompt")
	}

	user := engine.gotMessages[1]
	if user.Role != llm.RoleUser {
		t.Error("second message should have user role")
	}
	for _, want := range []string{"# Task Context", "the task context with file paths", "# Design Goal", "the design goal"} {
		if !strings.Contains(user.Content, want) {
			t.Errorf("user message missing %q:\n%s", want, user.Content)
		}
	}

	if got != "design plan" {
		t.Errorf("result = %q, want %q", got, "design plan")
	}
}

func TestDesignTool_Execute_StreamHandlerForwarded(t *testing.T) {
	engine := &designMockEngine{}
	tool := NewDesignTool(engine)

	chunks := make(chan []byte, 8)
	tool.StreamHandler = func(ctx context.Context, chunk []byte) error {
		chunks <- chunk
		return nil
	}

	if _, err := tool.Execute(context.Background(), map[string]interface{}{
		"context": "ctx",
		"goal":    "goal",
	}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if engine.gotOpts == nil || engine.gotOpts.StreamHandler == nil {
		t.Fatal("StreamHandler should be forwarded via CallOptions")
	}
	// 主动调用一次验证 handler 被透传且可用
	if err := engine.gotOpts.StreamHandler(context.Background(), []byte("x")); err != nil {
		t.Fatalf("forwarded StreamHandler: %v", err)
	}
	select {
	case c := <-chunks:
		if string(c) != "x" {
			t.Errorf("chunk = %q, want %q", string(c), "x")
		}
	default:
		t.Error("handler not invoked")
	}

	// 对照：handler 为空时 CallOptions 不携带 StreamHandler
	engine2 := &designMockEngine{}
	plain := NewDesignTool(engine2)
	if _, err := plain.Execute(context.Background(), map[string]interface{}{
		"context": "ctx",
		"goal":    "goal",
	}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if engine2.gotOpts == nil || engine2.gotOpts.StreamHandler != nil {
		t.Error("StreamHandler should be nil when not set")
	}
}
