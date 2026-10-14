package agents

// e2e_pause_test.go — 活暂停（A 层）的闸门（docs/pause-resume-design.md §10 G1）。
//
// 这里钉的是两条各自都会"看起来没事"的性质：
//   - 暂停必须落在 step 边界（D2）。落在批次中间就会留下"run_bash 到底跑完没有"
//     这种不可判定的副作用状态，而恢复侧无法知道。
//   - 停住之后恢复，那个批次的工具结果必须已经在上下文里。否则恢复后模型看到的
//     世界与暂停前不同，重放就是重复执行副作用。
//
// 脚本引擎按 Agent 身份分派、且先看 ctx（AGENTS.md §8.3）—— 一个忽略 ctx 的 mock
// 会把"取消没传播"直接掩盖掉。

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"yagent/internal/llm"
	"yagent/internal/pause"
	"yagent/internal/tools"
)

// codingSystemPrompt 用 Coding 的 prompt 首行措辞，好让 agentIdentity 认出"coding"。
const codingSystemPrompt = "Autonomous software engineering agent. You own the file system."

// stepBlocker 是一个"进去之后要外部放行才出来"的工具：用它把执行停在
// 一个批次的中间，才能观察暂停到底落在哪里。
type stepBlocker struct {
	ad      *tools.Adapter
	started chan struct{}
	release chan struct{}
	once    sync.Once
	calls   int
	mu      sync.Mutex
}

func newStepBlockingAdapter(t *testing.T) *stepBlocker {
	t.Helper()
	b := &stepBlocker{started: make(chan struct{}), release: make(chan struct{})}
	b.ad = tools.NewAdapter("slow_tool", "blocks until released", func(ctx context.Context, _ map[string]interface{}) (interface{}, error) {
		b.mu.Lock()
		b.calls++
		n := b.calls
		b.mu.Unlock()
		if n == 1 {
			b.once.Do(func() { close(b.started) })
			select {
			case <-b.release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return "first-batch-committed", nil
		}
		return "second-call", nil
	})
	return b
}

// TestPauseStopsAtStepBoundary 是 §10 的 G1。
//
// 时间线：step 1 的工具在飞 → Pause → 放行该工具 → 批次提交完毕、循环走到下一个
// step 边界 → 停住。断言"停住时 LLM 只被调用过 1 次"，也就是没有多跑一步；
// 再断言恢复后的第一次 LLM 调用看得到那条已提交的工具结果。
func TestPauseStopsAtStepBoundary(t *testing.T) {
	g := pause.NewGate()
	ctx := pause.WithGate(context.Background(), g)
	blocker := newStepBlockingAdapter(t)

	engine := newScripted(map[string][]scriptStep{
		"coding": {
			toolCallStep("slow_tool", map[string]interface{}{}),
			toolCallStep("slow_tool", map[string]interface{}{}),
		},
	})

	done := make(chan error, 1)
	go func() {
		_, err := RunAgentLoop(ctx, ExecutorConfig{
			SystemPrompt: codingSystemPrompt,
			UserInput:    "run the slow tool twice",
			Adapters:     []*tools.Adapter{blocker.ad},
			LLM:          engine,
			MaxSteps:     6,
			AgentName:    "coding",
		})
		done <- err
	}()

	<-blocker.started
	g.Pause("/pause")
	close(blocker.release) // 让在飞的这个工具跑完：中途停是不被允许的

	waitUntil(t, "loop parked at the step boundary", func() bool { return g.Blocked() })

	if calls := len(engine.CallsFor("coding")); calls != 1 {
		t.Fatalf("暂停后又发起了 %d 次 LLM 调用：闸门没落在 step 边界上", calls)
	}
	if blocker.calls != 1 {
		t.Fatalf("slow_tool 被调了 %d 次，暂停越过 step 边界多跑了一步", blocker.calls)
	}

	g.Resume()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("恢复后 RunAgentLoop 返回错误：%v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Resume 之后循环没有跑完")
	}

	calls := engine.CallsFor("coding")
	if len(calls) < 2 {
		t.Fatalf("恢复后只有 %d 次调用，没有接着往下跑", len(calls))
	}
	// 恢复后模型看到的第一件事必须是那条已提交的结果 —— 否则"停住"丢东西了。
	if text := messagesText(calls[1].messages); !strings.Contains(text, "first-batch-committed") {
		t.Errorf("恢复后的调用里没有已提交的工具结果：\n%s", truncateForFailure(text))
	}
}

// holdFirstCall 让第一次转给内层引擎的调用停在一个 LLM 请求中间。
//
// 为什么需要它：要断言"暂停发生在委派在飞时、而停住发生在其后的 step 边界"，
// 就必须有一个由测试控制的时机 —— 先进入 LLM 调用、由测试按下暂停、再放行。
// 靠 sleep 猜时机写出的是一个有时序窗口的假闸门。
type holdFirstCall struct {
	inner   llm.Engine
	entered chan struct{}
	hold    chan struct{}
	calls   int
	mu      sync.Mutex
	once    sync.Once
}

func (h *holdFirstCall) GenerateContent(ctx context.Context, messages []llm.Message, tools []llm.ToolDef, opts *llm.CallOptions) (*llm.Response, error) {
	h.mu.Lock()
	h.calls++
	first := h.calls == 1
	h.mu.Unlock()
	if first {
		h.once.Do(func() { close(h.entered) })
		select {
		case <-h.hold:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return h.inner.GenerateContent(ctx, messages, tools, opts)
}

func (h *holdFirstCall) Model() string { return h.inner.Model() }

func (h *holdFirstCall) CloseIdleConnections() { h.inner.CloseIdleConnections() }

// TestPauseMidDelegationKeepsTheDelegationWhole 钉住 A 层最要紧的对比：
// 暂停落在一次委派中间时，Director 侧那次 `delegate_*` 调用**从未返回过**，
// 所以上下文里不会有 §2 R1 那条 `"Error: context canceled"` ——
// 恢复后交回去的是真实结果，Director 看到的是"一次成功的委派"。
//
// 这就是 A 层与"cancel + 重建"方案的差别所在：后者会把停住记成失败，
// Director 于是自行决定重做、换 Agent 或收尾。
func TestPauseMidDelegationKeepsTheDelegationWhole(t *testing.T) {
	workDir := t.TempDir()
	g := pause.NewGate()
	ctx := pause.WithGate(context.Background(), g)

	engine := newScripted(map[string][]scriptStep{
		"director": {
			toolCallStep("delegate_coding", map[string]interface{}{"task": "跑一次慢调用"}),
			fixed(textResponse("任务完成")),
		},
		"coding": {
			fixed(makeToolCallResponse("list_dir", `{"dir_path": ".", "max_depth": 3}`)),
			fixed(makeTextThenExitResponse("coding 交付：批次已提交")),
		},
	})

	director := newFullChainDirector(t, workDir, engine)
	held := &holdFirstCall{
		inner:   engine,
		entered: make(chan struct{}),
		hold:    make(chan struct{}),
	}
	director.CodingAgent.LLM = held

	done := make(chan error, 1)
	go func() {
		_, err := director.Run(ctx, "让 coding 停一下")
		done <- err
	}()

	<-held.entered // Coding 正卡在一次 LLM 调用里
	g.Pause("/pause")
	close(held.hold) // 放行：这次调用完成后，循环会在边界上停住

	waitUntil(t, "coding parked at the step boundary", func() bool { return g.Blocked() })
	if calls := len(engine.CallsFor("coding")); calls != 1 {
		t.Fatalf("暂停后 Coding 又发起了 %d 次 LLM 调用：闸门没有拦住", calls)
	}

	g.Resume()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("恢复后 Director.Run 返回错误：%v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("恢复后 Director 没有跑完")
	}

	// 委派返回之后 Director 的那次调用里，必须是真实结果而不是取消噪声。
	calls := engine.CallsFor("director")
	if len(calls) < 2 {
		t.Fatalf("Director 只被调用 %d 次，委派结果没有回到上下文", len(calls))
	}
	text := messagesText(calls[len(calls)-1].messages)
	if strings.Contains(text, "context canceled") {
		t.Errorf("恢复后的委派里混进了取消文本（§2 R1 的降级又出现了）：\n%s", truncateForFailure(text))
	}
	if !strings.Contains(text, "coding 交付") {
		tail := text
		if r := []rune(tail); len(r) > 1600 {
			tail = "…" + string(r[len(r)-1600:])
		}
		t.Errorf("委派的真实结果没有回到 Director 上下文：\n%s\n识别到的 Agent：%s",
			tail, engine.seenAgents())
	}
}
