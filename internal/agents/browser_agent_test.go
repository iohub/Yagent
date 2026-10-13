package agents

import (
	"context"
	"strings"
	"testing"

	"yagent/internal/browserctl"
	"yagent/internal/config"
	"yagent/internal/tools"
	"yagent/internal/toolspec"
)

type stubRunner struct{ dir string }

func (s *stubRunner) Run(_ context.Context, command string, _ ...string) (*browserctl.Result, error) {
	return &browserctl.Result{Command: command, Stdout: "ok", ExitCode: 0}, nil
}
func (s *stubRunner) Dir() string { return s.dir }
func (s *stubRunner) Release()    {}

type stubController struct {
	ok     bool
	reason string
}

func (c *stubController) Probe() (bool, string) { return c.ok, c.reason }
func (c *stubController) Acquire(_ context.Context, _ string) (browserctl.Runner, error) {
	return &stubRunner{dir: "/tmp/browser-session"}, nil
}

type stubFlow struct{}

func (stubFlow) ExecuteAgentExit(_ context.Context, _ map[string]interface{}) (interface{}, error) {
	return "exit", nil
}
func (stubFlow) ExecuteAskUserForHelp(_ context.Context, _ map[string]interface{}) (interface{}, error) {
	return "ask", nil
}

type stubEnv struct{ fullYolo bool }

func (stubEnv) ProjectPath() string  { return "/tmp" }
func (e stubEnv) FullYoloMode() bool { return e.fullYolo }

func newBrowserAgentWith(ctrl BrowserControl, env Env) *BrowserAgent {
	return NewBrowserAgent(BrowserAgentParams{
		Publisher:  nil,
		Env:        env,
		Flow:       stubFlow{},
		Browser:    ctrl,
		BrowserCfg: config.DefaultBrowserConfig(),
		MaxSteps:   20,
		LLM:        nil,
	})
}

// TestBrowserAgent_AdaptersCoverDeclarations：工具表与 tools.json 声明必须一一对应，
// 且流程控制工具（agent_exit / ask_user_for_help）必须一并注册，否则 Agent 无法正常收尾。
func TestBrowserAgent_AdaptersCoverDeclarations(t *testing.T) {
	agent := newBrowserAgentWith(&stubController{ok: true}, stubEnv{})

	adapters := agent.surfaceFor(context.Background()).Adapters()
	registered := make(map[string]bool, len(adapters))
	for _, a := range adapters {
		registered[a.Name()] = true
	}

	for _, want := range tools.BrowserToolNames() {
		if !registered[want] {
			t.Errorf("browser tool %s not registered", want)
		}
	}
	if !registered["agent_exit"] {
		t.Error("agent_exit must be registered so the agent can finish")
	}
	if !registered["ask_user_for_help"] {
		t.Error("ask_user_for_help must be registered for login/CAPTCHA handoff")
	}
	// 反向也要成立：浏览器 Agent 拿不到写文件/Shell/委派类工具。
	for _, a := range adapters {
		n := a.Name()
		if strings.HasPrefix(n, "browser_") || n == "agent_exit" || n == "ask_user_for_help" ||
			n == "read_note" || n == "read_artifact" || n == "load_tool" {
			continue
		}
		t.Errorf("浏览器工具面混入了不该有的工具：%s", n)
	}
}

func TestBrowserAgent_FullYoloDropsAskUserForHelp(t *testing.T) {
	agent := newBrowserAgentWith(&stubController{ok: true}, stubEnv{fullYolo: true})

	for _, a := range agent.surfaceFor(context.Background()).Adapters() {
		if a.Name() == "ask_user_for_help" {
			t.Fatal("full-yolo mode must drop ask_user_for_help")
		}
	}
}

// TestBrowserAgent_NoAdaptersWithoutExecutionLayer：未装配执行层时不应注册任何浏览器工具，
// 避免把必然失败的入口交给 LLM。
func TestBrowserAgent_NoAdaptersWithoutExecutionLayer(t *testing.T) {
	agent := newBrowserAgentWith(nil, stubEnv{})
	// 没有执行层时整族 browser_* 不注册，但收尾工具必须在，否则该 Agent 无法结束。
	var browserTools int
	for _, a := range agent.surfaceFor(context.Background()).Adapters() {
		if strings.HasPrefix(a.Name(), "browser_") {
			browserTools++
		}
	}
	if browserTools != 0 {
		t.Errorf("expected no browser_* adapters when controller is nil, got %d", browserTools)
	}

	if ok, reason := agent.Capability(); ok || reason == "" {
		t.Errorf("Capability should report unavailable with a reason, got (%v, %q)", ok, reason)
	}
}

// TestBrowserAgent_UnavailableFailsFastWithFix：能力缺失时要给出可行动的修复指引，
// 这段文本会进 Director 上下文，必须能指导用户装上依赖。
func TestBrowserAgent_UnavailableFailsFastWithFix(t *testing.T) {
	agent := newBrowserAgentWith(&stubController{ok: false, reason: "未找到 browser-control"}, stubEnv{})

	_, err := agent.Run(context.Background(), "open example.com")
	if err == nil {
		t.Fatal("Run must fail when the capability is unavailable")
	}
	msg := err.Error()
	for _, want := range []string{"浏览器能力不可用", "未找到 browser-control", "binary_path"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error should contain %q, got %q", want, msg)
		}
	}
}

func TestBrowserDelegateDescription_DegradesWhenUnavailable(t *testing.T) {
	available := browserDelegateDescription(true, "")
	if !strings.Contains(available, "Delegate a browser task") {
		t.Errorf("available description should advertise the capability, got %q", available)
	}
	if strings.Contains(available, "UNAVAILABLE") {
		t.Errorf("available description must not claim unavailability: %q", available)
	}

	unavailable := browserDelegateDescription(false, "未找到 browser-control 二进制")
	for _, want := range []string{"UNAVAILABLE", "未找到 browser-control 二进制", "browser-control-cli"} {
		if !strings.Contains(unavailable, want) {
			t.Errorf("degraded description should contain %q, got %q", want, unavailable)
		}
	}
}

// TestBrowserMutatingToolsStaySerial：会改动共享当前标签页/ref 状态的工具必须串行，
// 且任何 browser 工具都不能被登记为只读（否则 toolbatch 会并发观察，读到错页）。
// 判据已从 nonParallelToolNames 那张名字表迁到注册表的 Spec.Serial。
func TestBrowserMutatingToolsStaySerial(t *testing.T) {
	for _, name := range tools.BrowserToolNames() {
		if !toolspec.IsSerial(name) {
			t.Errorf("%s must be declared Serial on its Spec", name)
		}
		if toolspec.IsReadOnly(name) {
			t.Errorf("%s must not be derived as read-only", name)
		}
	}
}
