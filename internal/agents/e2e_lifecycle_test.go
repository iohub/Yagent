package agents

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yagent/internal/blackboard"
	"yagent/internal/llm"
)

// 委派的生命周期链路：Director 的一次 delegate_* 调用要能在
//   - 子 Agent 卡住不动（idle 超时）
//   - 子 Agent 超过总预算（total 超时）
//   - 用户中途取消（父 ctx cancel）
// 三种情况下**按时返回错误**、把失败留痕成一张 status=error 的便签，并且不留下
// 还在跑的 goroutine。这三件事各自有单测，但串起来没人测过：
// 表现是"取消之后界面还在转"或"失败的委派下一轮什么都看不到，于是原样再卡一次"。

// hangEngine 会一直卡在 LLM 调用里，直到 ctx 结束。
// entered/exited 让用例能确定性地观察到"确实进去了 / 确实被解出来了"。
type hangEngine struct {
	modelName string
	entered   chan struct{}
	once      sync.Once
	exited    atomic.Int64
	err       error
}

func newHangEngine(name string) *hangEngine {
	return &hangEngine{modelName: name, entered: make(chan struct{})}
}

func (e *hangEngine) GenerateContent(ctx context.Context, _ []llm.Message, _ []llm.ToolDef, _ *llm.CallOptions) (*llm.Response, error) {
	e.once.Do(func() { close(e.entered) })
	defer e.exited.Add(1)
	<-ctx.Done()
	if e.err != nil {
		return nil, e.err
	}
	return nil, ctx.Err()
}

func (e *hangEngine) Model() string         { return e.modelName }
func (e *hangEngine) CloseIdleConnections() {}

// stuckDelegationDirector 造一个"子 Agent 一进去就卡住"的 Director：
// Director 自己的脚本正常应答，委派出去的那一位用 hangEngine。
func stuckDelegationDirector(t *testing.T, workDir string, stuck llm.Engine) (*DirectorAgent, *agentScriptedEngine) {
	t.Helper()

	engine := newScripted(map[string][]scriptStep{
		"director": {
			toolCallStep("delegate_repo", map[string]interface{}{"task": "STUCK-TASK"}),
		},
	})
	director := newFullChainDirector(t, workDir, engine)
	// 把卡住的子 Agent 换成 hangEngine：newFullChainDirector 让所有 Agent 共用一台引擎，
	// 这里单独把 repo 的换掉，Director 继续走脚本。
	director.RepoAgent.LLM = stuck
	return director, engine
}

// TestE2E_DelegateIdleTimeoutAbortsAndLeavesANote 钉住"idle 超时 → 错误带得出台账 →
// 失败也留一张便签"。没有便签的话，下一轮同类任务会原样再卡一次。
func TestE2E_DelegateIdleTimeoutAbortsAndLeavesANote(t *testing.T) {
	workDir := t.TempDir()
	if err := writeFile(t, workDir, "main.go", "package main\n"); err != nil {
		t.Fatalf("write: %v", err)
	}

	stuck := newHangEngine("stuck-repo")
	director, engine := stuckDelegationDirector(t, workDir, stuck)

	const sessionID = "e2e-session-timeout"
	director.SetBoardScope(sessionID, "e2e-task-timeout")
	// 真实默认是 max(10m, llm_timeout+5m)；这里压到几十毫秒，
	// 才能在一个用例的时间预算里观察到超时分支。
	director.delegateIdleTimeout = 40 * time.Millisecond
	director.delegateTotalTimeout = 0

	done := make(chan error, 1)
	go func() {
		_, err := director.Run(context.Background(), "让 Repo 卡住")
		done <- err
	}()

	select {
	case <-stuck.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("子 Agent 没有进入 LLM 调用，用例前提不成立")
	}

	select {
	case err := <-done:
		// 超时不该把整个 Run 变成 error：Director 会收到一条工具失败并重试/收尾。
		t.Logf("Run 返回 err=%v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("委派超时后 Director.Run 没有返回（idle 闸门没起作用）")
	}

	// 卡住的引擎必须被解栈，而不是留一个还在等 ctx 的 goroutine。
	deadline := time.Now().Add(2 * time.Second)
	for stuck.exited.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if stuck.exited.Load() == 0 {
		t.Fatal("子 Agent 的 LLM 调用没有被超时解栈（goroutine 泄漏）")
	}

	// 失败的委派不会带上 [Sub-Agent Result] 头部（applyEnhancedCommander 在 err != nil
	// 时直接回吐错误），所以这里断言的是 Director 看到的错误文本必须点明是 idle ——
	// 只说 "timed out" 的话，模型分不清该重试还是该换路子。
	text := engine.callTextContaining(t, "timed out")
	if !strings.Contains(text, "timed out (idle)") {
		t.Errorf("委派错误没标明是 idle 超时: %s", truncateForFailure(text))
	}

	// 失败的委派也要上板（coding.go:213 的注释就是这条契约）。
	notes, err := blackboard.ListNotes(blackboard.SanitizeBoardID(sessionID), "repo_agent", 10)
	if err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	if len(notes) == 0 {
		t.Fatal("超时失败的委派没留下便签：下一轮同类任务会从头再卡一遍")
	}
	if notes[0].Status != blackboard.StatusError {
		t.Errorf("便签 status = %q, want %q", notes[0].Status, blackboard.StatusError)
	}
}

// TestE2E_UserCancelReleasesDelegation 钉取消传播：用户在子 Agent 还在跑时按取消，
// 整条链必须立即解开 —— 不返回成功、不留下在跑的引擎 goroutine。
func TestE2E_UserCancelReleasesDelegation(t *testing.T) {
	workDir := t.TempDir()
	stuck := newHangEngine("stuck-repo")
	director, _ := stuckDelegationDirector(t, workDir, stuck)
	director.SetBoardScope("e2e-session-cancel", "e2e-task-cancel")
	// 给足超时预算：这里要证明的是"取消"能解开，而不是超时兜底。
	director.delegateIdleTimeout = 30 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := director.Run(ctx, "跑到一半用户取消")
		done <- err
	}()

	select {
	case <-stuck.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("子 Agent 没有进入 LLM 调用，用例前提不成立")
	}

	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Error("父 ctx 已取消，Director.Run 却报告成功")
		}
		if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "cancel") {
			t.Errorf("err = %v, want 一路带出 context.Canceled，否则上层分不清失败与被取消", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("取消后 Director.Run 没有返回")
	}

	deadline := time.Now().Add(2 * time.Second)
	for stuck.exited.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if stuck.exited.Load() == 0 {
		t.Fatal("取消没有传进子 Agent 的 LLM 调用（在飞的调用无人可解栈）")
	}
}

// TestE2E_DelegateTotalTimeoutAlsoFires 与 idle 分开钉：只配总预算、子 Agent 一直
// 有心跳时，idle 永远不会响 —— 只有 total 能收口。反过来（只配 idle）留给上一个用例。
func TestE2E_DelegateTotalTimeoutAlsoFires(t *testing.T) {
	workDir := t.TempDir()

	// 一台"每轮都成功但很慢、而且不肯收尾"的引擎：总预算必须能收口它。
	// 200ms/轮 × MaxSteps 10 = 2s 才会自然跑完；监视器每 activity.DefaultCheckInterval
	// (1s) 才采一次样，所以总预算必须明显大于 1s 粒度、又小于自然结束时间，
	// 否则"没被超时"和"超时没生效"根本分不开。
	slow := &slowEngine{delay: 200 * time.Millisecond}
	engine := newScripted(map[string][]scriptStep{
		"director": {toolCallStep("delegate_repo", map[string]interface{}{"task": "SLOW-TASK"})},
	})
	director := newFullChainDirector(t, workDir, engine)
	director.RepoAgent.LLM = slow
	director.SetBoardScope("e2e-session-total", "e2e-task-total")
	director.delegateIdleTimeout = 30 * time.Second // 明显大于总耗时，排除 idle 分支
	director.delegateTotalTimeout = 700 * time.Millisecond

	done := make(chan error, 1)
	go func() {
		_, err := director.Run(context.Background(), "让它慢慢跑")
		done <- err
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("total 超时没有收口（子 Agent 一直有活动时总预算被绕过）")
	}

	calls := slow.callCount.Load()
	if calls < 2 {
		t.Errorf("子 Agent 只跑了 %d 轮, want 它确实在连着跑（否则 total 分支没被走到）", calls)
	}
	if calls >= 10 {
		t.Errorf("子 Agent 跑满了 MaxSteps(10)（%d 轮），说明 total 超时没有收口，而是让它自然结束", calls)
	}
	text := engine.callTextContaining(t, "timed out")
	if !strings.Contains(text, "timed out (total)") {
		t.Errorf("委派错误没标明是 total 超时: %s", truncateForFailure(text))
	}
}

// slowEngine 每轮都"成功"，但每轮都睡一下并且继续调下一个工具 ——
// 于是它既有活动（idle 不响）又永不收尾（只有 total 能收口）。
type slowEngine struct {
	delay     time.Duration
	callCount atomic.Int64
}

func (e *slowEngine) GenerateContent(ctx context.Context, _ []llm.Message, _ []llm.ToolDef, _ *llm.CallOptions) (*llm.Response, error) {
	e.callCount.Add(1)
	select {
	case <-time.After(e.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return makeToolCallResponse("search_by_regex", `{"query":"go"}`), nil
}

func (e *slowEngine) Model() string         { return "slow-mock" }
func (e *slowEngine) CloseIdleConnections() {}
