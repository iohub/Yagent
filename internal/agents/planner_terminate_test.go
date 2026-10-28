package agents

// planner_terminate_test.go — D2：Planner.Run 的每条退出路径都必须**恰好**写一条终止事件。
//
// 缺陷形状：planner 的多个 return 点各自手调 writeComplete/writeAborted，既没有子 Agent
// 侧 executor.go 那样唯一的 defer 收口，也没有幂等标记。后果有两半：
//   - 有人新增一条 return 而忘了认领 → rollout 里根本没有终止事件。这不是假设：装配期
//     Prompts 失败那条路径（下表 prompts_error 用例）从来没写过账，恢复侧只看到"会话被
//     切断"，而真实情况是"根本没跑起来"；
//   - 同一轮里两处都认领 → 终止事件写两条，后一条带着更晚的 step 游标，盖掉真正该续跑
//     的那一步。
// 修复给了两半各一道防线：complete/aborted 置 terminal（幂等，先到先得），Run 在 rollout
// writer 一建好就 defer journal.abortUnclaimed()（兜底，补一条 unaccounted 让缺口可见）。
//
// 载体是生产那一条：newDirectorToolRunner + director.NewPlanner().Run()，绕开门面层的
// 装配但不复造工具执行层（仓库硬约束：测试与生产共用同一载体）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"yagent/internal/agents/director"
	"yagent/internal/llm"
	"yagent/internal/memory"
	"yagent/internal/tools"
)

// plannerTerminalEvent 是 rollout 里一条终止事件（event_msg 的 task_complete / turn_aborted）。
type plannerTerminalEvent struct {
	kind       string // task_complete | turn_aborted
	stopReason string // memory.StopKind*
	reason     string // 面向人的文案
}

// readPlannerTerminalEvents 直接读 JSONL 原文数终止事件。
// 不用 memory.ReadRolloutMemory：它把终止折叠成 RolloutInfo 的布尔量（Finished/Aborted），
// 而这一条测试钉的正是**数量**——0 条与 2 条都必须红，只问"有没有"看不见后者。
func readPlannerTerminalEvents(t *testing.T, path string) []plannerTerminalEvent {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 rollout %s: %v", path, err)
	}
	var out []plannerTerminalEvent
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var env struct {
			Type    string `json:"type"`
			Payload struct {
				Type       string `json:"type"`
				Reason     string `json:"reason"`
				StopReason string `json:"stop_reason"`
			} `json:"payload"`
		}
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			t.Fatalf("rollout 行不是合法 JSON: %q err=%v", line, err)
		}
		if env.Type != "event_msg" {
			continue
		}
		if env.Payload.Type != "task_complete" && env.Payload.Type != "turn_aborted" {
			continue
		}
		out = append(out, plannerTerminalEvent{
			kind:       env.Payload.Type,
			stopReason: env.Payload.StopReason,
			reason:     env.Payload.Reason,
		})
	}
	return out
}

// plannerExitEngine 按脚本给出 Director 的回复。
// 脚本用尽之后：repeatLast=true 重复最后一条（跑满步数的用例），否则返回 error
// —— 用例少写了脚本必须响，不能悄悄多跑一步把断言蒙过去。
type plannerExitEngine struct {
	mu         sync.Mutex
	script     []*llm.Response
	repeatLast bool
	err        error // 非 nil：每次调用都失败（llm_error 用例）
	// cancelDuringRun 非 nil：在**第一次** GenerateContent 里触发一次，触发完即清空。
	// 用例用它把"取消"钉进 Planner.Run 的执行期间：进入 LLM 调用时把外层 ctx 打掉，
	// 紧接着下面那条 ctx.Err() 检查就是真实引擎在取消时的行为。
	cancelDuringRun func()
	calls           int
}

// fireCancelOnce 触发并清空登记的取消动作。锁内取、锁外调用：cancel() 本身不阻塞，
// 而清空保证它只作用于第一次调用（第二次再触发等于没有触发——ctx 早已取消）。
func (e *plannerExitEngine) fireCancelOnce() {
	e.mu.Lock()
	hook := e.cancelDuringRun
	e.cancelDuringRun = nil
	e.mu.Unlock()
	if hook != nil {
		hook()
	}
}

func (e *plannerExitEngine) GenerateContent(ctx context.Context, _ []llm.Message, _ []llm.ToolDef, _ *llm.CallOptions) (*llm.Response, error) {
	e.fireCancelOnce()

	// 真实引擎在 ctx 取消时返回错误；不尊重 ctx 的替身会把"取消没传播"直接掩盖掉。
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if e.err != nil {
		return nil, e.err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.calls >= len(e.script) {
		if e.repeatLast && len(e.script) > 0 {
			return e.script[len(e.script)-1], nil
		}
		return nil, fmt.Errorf("plannerExitEngine: 脚本只有 %d 条，第 %d 次调用没有回复", len(e.script), e.calls+1)
	}
	resp := e.script[e.calls]
	e.calls++
	return resp, nil
}

func (e *plannerExitEngine) Model() string         { return "planner-exit-mock" }
func (e *plannerExitEngine) CloseIdleConnections() {}

// plannerExitCase 一次 Planner.Run 的输入与期望的终止账。
type plannerExitCase struct {
	name       string
	maxSteps   int
	toolNames  []string // 工具面里有谁（agent_exit / delegate_alpha）
	panicTool  string   // 非空：这个工具的执行体直接 panic（D1 用例）
	script     []*llm.Response
	repeatLast bool
	llmErr     error
	failPrompt bool   // 装配期就拿不到 system prompt
	cancelCtx  bool   // true：取消在 Run 执行期间落地（由引擎第一次调用触发，见 fireCancelOnce）
	wantKind   string // task_complete | turn_aborted
	wantStop   string // memory.StopKind*
}

// runPlannerExit 跑一轮真实 Planner，返回结局、rollout 里的终止事件、以及会话记忆。
// rollout writer 交给 cfg.Rollout，由 Planner.Run 的 defer 关闭，测试里不再关第二次。
func runPlannerExit(t *testing.T, c plannerExitCase) (director.PlanResult, error, []plannerTerminalEvent, *memory.ConversationMemory) {
	t.Helper()

	w, err := memory.NewRolloutWriter("director", "planner-exit-"+c.name, "planner-exit-test")
	if err != nil || !w.Enabled() {
		t.Skipf("rollout writer 不可用: %v", err)
	}

	adapters := make([]*tools.Adapter, 0, len(c.toolNames)+1)
	for _, name := range c.toolNames {
		tool := name
		body := func(context.Context, map[string]interface{}) (interface{}, error) {
			if strings.HasPrefix(tool, "delegate_") {
				return "SUBAGENT_RESULT", nil
			}
			return map[string]interface{}{"finished": true, "reason": "done"}, nil
		}
		if c.panicTool == tool {
			body = func(context.Context, map[string]interface{}) (interface{}, error) { panic("boom") }
		}
		adapters = append(adapters, tools.NewAdapter(tool, "mock "+tool, body))
	}

	agent := &DirectorAgent{
		surface:                  tools.NewSurfaceFromAdapters("test-agent", adapters...),
		maxParallelReadOnlyTools: NormalizeMaxParallelReadOnlyTools(4),
		delegateIdleTimeout:      10 * time.Second,
	}
	state := &director.RunState{MaxSteps: c.maxSteps}

	prompts := func(context.Context, director.PromptInput) (string, error) { return "SYSTEM_PROMPT", nil }
	if c.failPrompt {
		prompts = func(context.Context, director.PromptInput) (string, error) {
			return "", errors.New("prompt template missing")
		}
	}

	engine := &plannerExitEngine{script: c.script, repeatLast: c.repeatLast, err: c.llmErr}

	ctx := context.Background()
	if c.cancelCtx {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
		// 只 defer cancel() 等于"Run **返回之后**才取消"：Run 全程 ctx 从未取消，替身
		// 走进"脚本用尽"分支返回普通错误，分类出来就是 llm_error —— 这条子测试因此
		// 从来没测过取消传播。取消改由引擎进入第一次 LLM 调用时触发，落在 Run 执行
		// 期间，且不需要任何时间窗。
		//
		// 为什么不是 executor_parallel_test.go:514-531 那条 time.AfterFunc 范式：那边
		// 一批 6 个阻塞工具把 Run 撑到取消落地，延迟与耗时之间没有竞争；这里的 Run 只
		// 跟纯内存替身往返一次（≈百微秒），任何延迟都是在和它赛跑。要给 AfterFunc 让
		// Run 活到取消时刻，就得塞一个阻塞夹具，而它只有在"取消没发生"（=变异验证）时
		// 才被真正等满，一次回归检查要挂住十几到几十秒。
		engine.cancelDuringRun = cancel
	}

	cfg := director.PlannerConfig{
		LLM:                      engine,
		Rollout:                  w,
		Tools:                    newDirectorToolRunner(agent, state),
		Prompts:                  prompts,
		MaxSteps:                 c.maxSteps,
		LLMTimeout:               30 * time.Second,
		ToolTimeout:              30 * time.Second,
		MaxParallelReadOnlyTools: agent.maxParallelReadOnlyTools,
		IsParallelizableTool:     agent.isToolParallelizable,
	}

	mem := memory.NewConversationMemory(128)
	mem.AddHumanMessage("task input")

	res, runErr := director.NewPlanner(cfg, state).Run(ctx, director.PlanInput{Input: "task input", Mem: mem})
	return res, runErr, readPlannerTerminalEvents(t, w.FilePath()), mem
}

// TestPlanner_EveryExitPathWritesExactlyOneTerminal 表驱动钉住"每条退出路径恰好一条
// 终止事件"：不多（幂等）、不少（兜底）。
//
// 回退修复会怎么红：
//   - 删掉 complete/aborted 里的 terminal 置位 → 出口写完一条、兜底再补一条 unaccounted，
//     六个用例全部红在"终止事件 2 条，want 1 条"；
//   - 删掉 defer journal.abortUnclaimed() → prompts_error 用例红在"终止事件 0 条"
//     （其余五条仍绿：那 8 个既有 return 点本来就会自己认领）。
//
// cancelled 那一条还顺带钉住"取消真的发生在本轮 Run 里"：取消由引擎的第一次调用触发，
// 所以把 ctx 的取消挪回"Run 返回之后"（=原来的假绿写法）时，替身看不见取消，
// 分类退回 llm_error，用例当场变红。
func TestPlanner_EveryExitPathWritesExactlyOneTerminal(t *testing.T) {
	cases := []plannerExitCase{
		{
			name: "plain_text", maxSteps: 5,
			toolNames: []string{"delegate_alpha"},
			script: []*llm.Response{
				makeToolCallResponse("delegate_alpha", `{"task":"go"}`),
				newTextResponse("FINAL_ANSWER"),
			},
			wantKind: "task_complete", wantStop: memory.StopKindPlainText,
		},
		{
			name: "agent_exit", maxSteps: 5,
			toolNames: []string{"agent_exit"},
			script:    []*llm.Response{makeToolCallResponse("agent_exit", `{"reason":"done"}`)},
			wantKind:  "task_complete", wantStop: memory.StopKindAgentExit,
		},
		{
			name: "max_steps", maxSteps: 2, repeatLast: true,
			toolNames: []string{"delegate_alpha"},
			script:    []*llm.Response{makeToolCallResponse("delegate_alpha", `{"task":"go"}`)},
			wantKind:  "turn_aborted", wantStop: memory.StopKindMaxSteps,
		},
		{
			name: "llm_error", maxSteps: 3,
			llmErr:   errors.New("provider exploded"),
			wantKind: "turn_aborted", wantStop: memory.StopKindLLMError,
		},
		{
			// 取消发生在 Run 执行期间（见 runPlannerExit），替身按真实引擎的样子返回
			// context.Canceled；分类必须回头看 ctx 才认得出这是"用户停了"而不是"provider 挂了"。
			name: "cancelled", maxSteps: 3, cancelCtx: true,
			wantKind: "turn_aborted", wantStop: memory.StopKindCancelled,
		},
		{
			name: "prompts_error", maxSteps: 3, failPrompt: true,
			// 装配期失败没有任何出口认领过原因：兜底必须让它以 unaccounted 现形。
			wantKind: "turn_aborted", wantStop: memory.StopKindUnaccounted,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, runErr, events, _ := runPlannerExit(t, c)

			if len(events) != 1 {
				t.Fatalf("终止事件 = %d 条, want 恰好 1 条: %+v", len(events), events)
			}
			got := events[0]
			if got.kind != c.wantKind {
				t.Errorf("终止事件类型 = %q, want %q", got.kind, c.wantKind)
			}
			if got.stopReason != c.wantStop {
				t.Errorf("stop_reason = %q, want %q（runErr=%v, res=%+v）", got.stopReason, c.wantStop, runErr, res)
			}
		})
	}
}
