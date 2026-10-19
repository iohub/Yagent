package agents

import (
	"context"
	"errors"
	"testing"
	"time"

	"yagent/internal/config"
	"yagent/internal/globalctx"
	"yagent/internal/llm"
	"yagent/internal/messaging"
)

// [llm] step_retries 过去只被 Director 和 Meta 读取：ExecutorConfig.StepRetries 在全仓
// 没有任何写入点，六个子 Agent 恒为 0 —— 于是"step_retries = 3"的用户以为子 Agent 会重试，
// 实际一次都不重试，而且没有任何提示。这里钉住它真的下发了。

// flakyEngine 前 failsAllowed 次调用返回错误，之后正常给出**以 agent_exit 收尾**的一轮。
//
// 那一句收尾不是可有可无：Repo 现在停在纯文本时会收到一次收尾提醒
// （repo.go 的 NoActionRetryLimit=1，见 §11 坑 40），于是一次"成功"的文本回复会再多烧一次
// LLM 调用 —— 那次调用属于提醒机制，不该记在只管 step_retries 的这个用例账上。
// 提醒本身由 TestPlainTextReminder* 与 TestE2EPlainTextRepoRunIsNudgedIntoSubmitting 钉。
type flakyEngine struct {
	failsAllowed int
	calls        int
}

func (e *flakyEngine) GenerateContent(context.Context, []llm.Message, []llm.ToolDef, *llm.CallOptions) (*llm.Response, error) {
	e.calls++
	if e.calls <= e.failsAllowed {
		return nil, errors.New("simulated upstream 5xx")
	}
	return makeTextThenExitResponse("调查完成：目录里只有 main.go。"), nil
}

func (e *flakyEngine) Model() string         { return "flaky-mock" }
func (e *flakyEngine) CloseIdleConnections() {}

func newRetryRepoAgent(t *testing.T, workDir string, engine llm.Engine, stepRetries int) *RepoAgent {
	t.Helper()
	gctx := newTestGlobalCtx(workDir)
	gctx.FullYoloMode = true
	publisher := messaging.NewMessagePublisher(nil)
	gctx.Publisher = publisher

	return NewRepoAgent(RepoAgentParams{
		Formatter:   gctx,
		Publisher:   publisher,
		Env:         globalctx.NewEnvView(gctx),
		Files:       gctx.FileOps,
		Search:      gctx.SearchOps,
		RepoOps:     gctx.RepoOps,
		Flow:        gctx.FlowOps,
		DeepThinker: gctx.DeepThinkingTool,
		Guard:       gctx.Guard,
		LLM:         engine,
		MaxSteps:    5,
		StepRetries: stepRetries,
	})
}

// 关掉时不重试、直接失败：这条断言让上一条不可能靠"重试逻辑恒开"蒙过去。
func TestSubAgentStepRetriesOffFailsFirstCall(t *testing.T) {
	restore := withStepRetryBackoff(t, time.Millisecond)
	defer restore()

	engine := &flakyEngine{failsAllowed: 1}
	agent := newRetryRepoAgent(t, t.TempDir(), engine, 0)

	if _, err := agent.Run(context.Background(), "这个仓库里有什么文件？"); err == nil {
		t.Fatal("step_retries=0 时第一次 LLM 失败就被吞掉了 —— 子 Agent 不该自己编一个结果")
	}
	if engine.calls != 1 {
		t.Errorf("调用次数 = %d, want 1（没开重试就不该有第二次）", engine.calls)
	}
}

func TestSubAgentStepRetriesWired(t *testing.T) {
	restore := withStepRetryBackoff(t, time.Millisecond)
	defer restore()

	engine := &flakyEngine{failsAllowed: 1}
	agent := newRetryRepoAgent(t, t.TempDir(), engine, 1)

	result, err := agent.Run(context.Background(), "这个仓库里有什么文件？")
	if err != nil {
		t.Fatalf("step_retries=1 时一次瞬时失败就该被吸收: %v", err)
	}
	if engine.calls != 2 {
		t.Errorf("调用次数 = %d, want 2（第一次失败 + 一次重试）", engine.calls)
	}
	if result.Text == "" {
		t.Error("重试成功后交付物是空的")
	}
}

// 重试次数耗尽仍然是失败，而不是"随便给个结果"。
func TestSubAgentStepRetriesExhausted(t *testing.T) {
	restore := withStepRetryBackoff(t, time.Millisecond)
	defer restore()

	engine := &flakyEngine{failsAllowed: 5}
	agent := newRetryRepoAgent(t, t.TempDir(), engine, 2)

	if _, err := agent.Run(context.Background(), "这个仓库里有什么文件？"); err == nil {
		t.Fatal("重试耗尽后应当报错")
	}
	if engine.calls != 3 {
		t.Errorf("调用次数 = %d, want 3（首发 + 2 次重试）", engine.calls)
	}
}

// withStepRetryBackoff 把退避基数调到毫秒级并负责还原：
// 测试的耗时必须由测试自己负责（AGENTS.md §8.3），1 秒的默认基数会让这三个用例白付 3 秒。
func withStepRetryBackoff(t *testing.T, d time.Duration) func() {
	t.Helper()
	prev := stepRetryBackoffBase
	stepRetryBackoffBase = d
	return func() { stepRetryBackoffBase = prev }
}

// 配置默认值本身是 0（关）：上面的接线不能改变没人配置时的行为。
func TestStepRetriesDefaultsToOff(t *testing.T) {
	if config.DefaultLLMConfig().StepRetries != 0 {
		t.Errorf("DefaultLLMConfig().StepRetries = %d, want 0", config.DefaultLLMConfig().StepRetries)
	}
}
