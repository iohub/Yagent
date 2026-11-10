package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"yagent/internal/config"
	"yagent/internal/llm"
)

// 认知类工具的预算曾经是硬编码的 `context.WithoutCancel(ctx)` + `10*time.Minute`：
// 预算改不了（任何配置都进不来）、停止键也收不到（WithoutCancel 连 Done 一起丢掉）。
// 这组用例钉住修好之后的三件事：配置值真的到达引擎、父级 180s 看门狗被脱离、
// 取消信号仍然穿透。

// recordingEngine 记录每次调用收到的 ctx 与消息，并立刻返回固定内容。
type recordingEngine struct{ got context.Context }

func (e *recordingEngine) GenerateContent(ctx context.Context, _ []llm.Message, _ []llm.ToolDef, _ *llm.CallOptions) (*llm.Response, error) {
	e.got = ctx
	return &llm.Response{Choices: []llm.Choice{{Content: "analysis"}}}, nil
}

func (e *recordingEngine) Model() string         { return "recording-model" }
func (e *recordingEngine) CloseIdleConnections() {}

// waitCancelEngine 一直等到 ctx 结束才返回：取消信号若被丢掉，它会卡住而不是失败。
type waitCancelEngine struct{ entered chan struct{} }

func (e *waitCancelEngine) GenerateContent(ctx context.Context, _ []llm.Message, _ []llm.ToolDef, _ *llm.CallOptions) (*llm.Response, error) {
	if ctx.Done() == nil {
		// 把"取消信号被丢"变成立刻失败，而不是让用例挂到超时。
		return nil, errors.New("ctx has no Done channel: cancellation was dropped")
	}
	if e.entered != nil {
		close(e.entered)
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (e *waitCancelEngine) Model() string         { return "wait-cancel" }
func (e *waitCancelEngine) CloseIdleConnections() {}

// errEngine 直接返回给定错误，用来钉错误文案。
type errEngine struct{ err error }

func (e *errEngine) GenerateContent(context.Context, []llm.Message, []llm.ToolDef, *llm.CallOptions) (*llm.Response, error) {
	return nil, e.err
}

func (e *errEngine) Model() string         { return "err" }
func (e *errEngine) CloseIdleConnections() {}

// withCogToolBudget 替换装配层注入的预算解析器（restore 交给 t.Cleanup）。
func withCogToolBudget(t *testing.T, fn func(string) time.Duration) {
	t.Helper()
	old := CogToolBudget
	CogToolBudget = fn
	t.Cleanup(func() { CogToolBudget = old })
}

func assertBudgetDeadline(t *testing.T, ctx context.Context, want time.Duration) {
	t.Helper()
	d, ok := ctx.Deadline()
	if !ok {
		t.Fatalf("引擎收到的 ctx 没有 deadline：预算没有传下去")
	}
	remain := time.Until(d)
	if remain < want-time.Minute || remain > want+time.Minute {
		t.Errorf("引擎收到的预算 = %v, want ≈%v", remain, want)
	}
}

func TestDeepThinkingTool_UsesCogToolBudget(t *testing.T) {
	var slot string
	withCogToolBudget(t, func(name string) time.Duration {
		slot = name
		return 37 * time.Minute
	})

	engine := &recordingEngine{}
	tool := NewDeepThinkingTool(engine)
	if _, err := tool.Execute(context.Background(), map[string]interface{}{
		"context": "ctx", "goal": "goal",
	}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// 槽位名必须是 deepthinking：传错名字时装配层按别的槽位解析预算。
	if slot != "deepthinking" {
		t.Errorf("预算查询的槽位 = %q, want deepthinking", slot)
	}
	assertBudgetDeadline(t, engine.got, 37*time.Minute)
}

// executor 给每个工具套 180s 的看门狗；深度分析必须脱离它，否则预算再大也没用。
func TestDeepThinkingTool_DetachesParentDeadline(t *testing.T) {
	withCogToolBudget(t, func(string) time.Duration { return 37 * time.Minute })

	parent, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	engine := &recordingEngine{}
	if _, err := NewDeepThinkingTool(engine).Execute(parent, map[string]interface{}{
		"context": "ctx", "goal": "goal",
	}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// 若退回 WithoutCancel + 硬编码 10m，这里会看到 ≈10m；若只去掉 WithTimeout，
	// 会看到 ≈180s。两种回归都被这条挡住。
	assertBudgetDeadline(t, engine.got, 37*time.Minute)
}

// 另一半：脱离 deadline 不等于脱离取消。用户按停止必须立刻中断这次昂贵调用。
func TestDeepThinkingTool_KeepsCancelSignal(t *testing.T) {
	withCogToolBudget(t, func(string) time.Duration { return 37 * time.Minute })

	engine := &waitCancelEngine{entered: make(chan struct{})}
	tool := NewDeepThinkingTool(engine)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-engine.entered
		cancel()
	}()

	_, err := tool.Execute(ctx, map[string]interface{}{"context": "ctx", "goal": "goal"})
	if err == nil {
		t.Fatal("父 ctx 已取消，却返回了分析结果")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want 带 context.Canceled", err)
	}
	if !strings.Contains(err.Error(), "cancelled by the caller") {
		t.Errorf("err = %v, want 明确说明是被调用方取消（与预算超时要能区分）", err)
	}
}

// 预算不足时的文案必须点名槽位与数值：用户看到的下一句就是"去哪儿改"。
func TestDeepThinkingTool_TimeoutErrorIsDiagnosable(t *testing.T) {
	withCogToolBudget(t, func(string) time.Duration { return 12 * time.Minute })

	_, err := NewDeepThinkingTool(&errEngine{err: context.DeadlineExceeded}).Execute(
		context.Background(), map[string]interface{}{"context": "ctx", "goal": "goal"})
	if err == nil {
		t.Fatal("引擎返回 DeadlineExceeded 时工具不该成功")
	}
	for _, want := range []string{"[tools.llm.deepthinking].timeout", "12m0s", "timed out"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want 含 %q", err, want)
		}
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want 保留 context.DeadlineExceeded 供调用方判定", err)
	}

	// 非超时/非取消的失败仍走原来的兜底文案（不要被改成一概说成超时）。
	_, err = NewDeepThinkingTool(&errEngine{err: errors.New("boom")}).Execute(
		context.Background(), map[string]interface{}{"context": "ctx", "goal": "goal"})
	if err == nil || !strings.Contains(err.Error(), "LLM call failed") {
		t.Errorf("err = %v, want 含 \"LLM call failed\"", err)
	}
}

// 装配层没注入（或注入的解析器返回 ≤0）时必须落到兜底常量，而不是 0 预算。
func TestCogToolBudgetFallsBackToDefault(t *testing.T) {
	old := CogToolBudget
	t.Cleanup(func() { CogToolBudget = old })

	CogToolBudget = nil
	if got := cogToolBudget("deepthinking"); got != config.DefaultCogToolTimeout {
		t.Errorf("未注入时 = %v, want %v", got, config.DefaultCogToolTimeout)
	}

	CogToolBudget = func(string) time.Duration { return 0 }
	if got := cogToolBudget("design"); got != config.DefaultCogToolTimeout {
		t.Errorf("解析器返回 0 时 = %v, want %v", got, config.DefaultCogToolTimeout)
	}
}

// design 走同一套机制，但槽位名必须是 design（两个槽位的预算是分开配的）。
func TestDesignTool_UsesCogToolBudget(t *testing.T) {
	var slot string
	withCogToolBudget(t, func(name string) time.Duration {
		slot = name
		return 22 * time.Minute
	})

	engine := &recordingEngine{}
	if _, err := NewDesignTool(engine).Execute(context.Background(), map[string]interface{}{
		"context": "ctx", "goal": "goal",
	}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if slot != "design" {
		t.Errorf("预算查询的槽位 = %q, want design", slot)
	}
	assertBudgetDeadline(t, engine.got, 22*time.Minute)
}

// design 同样必须保留取消信号（旧实现用 WithoutCancel 两边都丢）。
func TestDesignTool_KeepsCancelSignal(t *testing.T) {
	withCogToolBudget(t, func(string) time.Duration { return 22 * time.Minute })

	engine := &waitCancelEngine{entered: make(chan struct{})}
	tool := NewDesignTool(engine)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-engine.entered
		cancel()
	}()

	_, err := tool.Execute(ctx, map[string]interface{}{"context": "ctx", "goal": "goal"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want 带 context.Canceled（design 也必须能被停止键打断）", err)
	}
}
