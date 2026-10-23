package app

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"

	"yagent/internal/config"
	"yagent/internal/llm"
	"yagent/internal/memory"
	"yagent/internal/messaging"
	"yagent/internal/toolspec"
)

// app 包是一次性装配点，跑起来要真 LLM、真 MCP、真浏览器，不适合在这里做全流程测试。
// 但它有几段"纯逻辑却决定别人能不能拿到自己那份上下文"的代码，出了错全是静默失效：
// 任务请求的装配、引擎包装层、被禁用 Agent 的解析。

// stubEngine 只够满足 llm.Engine 接口。
type stubEngine struct{ model string }

func (e stubEngine) Model() string         { return e.model }
func (e stubEngine) CloseIdleConnections() {}
func (e stubEngine) GenerateContent(context.Context, []llm.Message, []llm.ToolDef, *llm.CallOptions) (*llm.Response, error) {
	return &llm.Response{}, nil
}

// agentEngine 必须始终返回 *llm.RoutingEngine 包装。
// 直接返回底层引擎的那次改动（commit 0c87c8a）让 auto-routing 静默失效：
// 界面照样能跑，只是每一次调用都固定打在 baseline 档位上。
//
// 只断言类型，不驱动 resolve()：后者要读 client.Config 里真实生效的 provider，
// 那属于 llm 包自己的测试范围。
func TestAgentEngineAlwaysWrapsWithRoutingEngine(t *testing.T) {
	base := stubEngine{model: "bare"}
	ca := &Yagent{client: &llm.Client{Engine: base}}

	got := ca.agentEngine("coding", base)
	if _, ok := got.(*llm.RoutingEngine); !ok {
		t.Fatalf("agentEngine 返回 %T, want *llm.RoutingEngine（丢了包装 = auto-routing 静默失效）", got)
	}
	// 每个角色都要拿到绑定到自己名字的包装，否则档位基线会串到别的 Agent 上。
	for _, agent := range []string{"director", "coding", "repo", "chat", "devops", "browser", "meta"} {
		re, ok := ca.agentEngine(agent, base).(*llm.RoutingEngine)
		if !ok {
			t.Errorf("agent %q → %T, want *llm.RoutingEngine", agent, re)
		}
	}

	// 没有 client 时（未经 NewYagent 构造的极端路径）必须回落到传入的引擎而不是 panic。
	noClient := &Yagent{}
	if got := noClient.agentEngine("coding", base); got != llm.Engine(base) {
		t.Errorf("无 client 时 = %T, want 原样回落到底层引擎", got)
	}
}

func TestGetRouteSnapshotWithoutClient(t *testing.T) {
	snap := (&Yagent{}).GetRouteSnapshot()
	if snap.Mode != "fixed" {
		t.Errorf("Mode = %q, want fixed（没有 client 时不该宣称在自动路由）", snap.Mode)
	}
	if snap.Reason == "" {
		t.Error("Reason 为空, want 说明 client 不可用（TUI 直接显示这句）")
	}
}

// NewTaskRequest + With* 是每一轮对话唯一的参数入口。少接一个 With，
// 表现是"这一轮看不见历史"或"便签板换了一块"，而不会有任何报错。
func TestTaskRequestBuilders(t *testing.T) {
	ctx := context.Background()
	mem := memory.NewConversationMemory(10)
	pub := messaging.NewMessagePublisher(nil)
	cb := func(string, string) {}

	r := NewTaskRequest(ctx, "task-1").
		WithProjectDir("/tmp/proj").
		WithTaskDesc("修个 bug").
		WithMemory(mem).
		WithWSCallback(cb).
		WithMessagePublisher(pub).
		WithUserMessage("继续").
		WithResumed(true).
		WithSessionID("sess-9")

	if r.ctx != ctx || r.taskID != "task-1" {
		t.Errorf("ctx/taskID 绑定错: %+v", r)
	}
	if r.projectDir != "/tmp/proj" || r.taskDesc != "修个 bug" {
		t.Errorf("projectDir/taskDesc = %q/%q", r.projectDir, r.taskDesc)
	}
	if r.memory != mem {
		t.Error("memory 没接上（Director 会丢掉整段历史）")
	}
	if r.publisher != pub {
		t.Error("publisher 没接上（事件流断，界面无输出）")
	}
	if r.wsCallback == nil {
		t.Error("wsCallback 没接上")
	}
	if r.userMessage != "继续" {
		t.Errorf("userMessage = %q", r.userMessage)
	}
	if !r.resumed {
		t.Error("resumed 没接上（续跑第一轮不会重注入项目上下文）")
	}
	if r.sessionID != "sess-9" {
		t.Errorf("sessionID = %q, want sess-9（便签板会退回按 taskID 分块）", r.sessionID)
	}

	// 每个 With 必须返回同一个对象：链式写法依赖它，返回副本会丢掉前面的设置。
	base := NewTaskRequest(ctx, "t")
	if got := base.WithProjectDir("/x"); got != base {
		t.Error("WithProjectDir 返回了新的 TaskRequest")
	}
	if base.projectDir != "/x" {
		t.Error("With 修改的是副本，原对象没被设置")
	}

	// 缺省形态：--prompt 单发没有 sessionID，boardScope 由调用方回落到 taskID。
	bare := NewTaskRequest(ctx, "only-task")
	if bare.sessionID != "" || bare.resumed {
		t.Errorf("新建请求的缺省 = %+v, want sessionID 空、resumed false", bare)
	}
}

// -d/--disable-agents 的解析：多一个空项、少一次 trim，整个 Agent 就"以为被禁用了"
// 或者反过来仍然被注册进 Director 的工具面。
func TestParseDisabledAgents(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"repo", []string{"repo"}},
		{"repo,coding", []string{"coding", "repo"}},
		{" repo , coding ", []string{"coding", "repo"}},
		{"repo,,coding,", []string{"coding", "repo"}},
		{",,,", nil},
		{"Repo", []string{"Repo"}}, // 大小写由调用方比对，这里不做归一
	}
	for _, tc := range cases {
		got := parseDisabledAgents(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("parseDisabledAgents(%q) = %v, want %v", tc.in, keysOf(got), tc.want)
			continue
		}
		for _, name := range tc.want {
			if !got[name] {
				t.Errorf("parseDisabledAgents(%q) 漏了 %q", tc.in, name)
			}
		}
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// 运行中换 provider：SwitchProvider 在 UI 线程写 ca.engine，而任务线程每轮开头读它
// （ProcessCodingTask* → ca.Init(ca.engine, …)）。去掉 engineMu，这条用例只在 -race 下红。
func TestEngineFieldSurvivesConcurrentProviderSwitch(t *testing.T) {
	cfg := &config.Config{Global: config.TopLevelConfig{LLM: &config.GlobalLLMConfig{
		UseProvider: "alpha",
		Providers: map[string]config.ProviderConfig{
			"alpha": {Model: "alpha-model", APIBaseURL: "http://127.0.0.1:1/v1", APIKey: "k"},
			"beta":  {Model: "beta-model", APIBaseURL: "http://127.0.0.1:1/v1", APIKey: "k"},
		},
	}}}
	ca, err := NewYagent(&llm.Client{Config: cfg, Engine: stubEngine{model: "alpha-model"}})
	if err != nil {
		t.Fatalf("NewYagent: %v", err)
	}

	switched := make(chan struct{})
	go func() {
		defer close(switched)
		for j := 0; j < 40; j++ {
			want := "beta"
			if j%2 == 0 {
				want = "alpha"
			}
			if err := ca.SwitchProvider(want); err != nil {
				t.Errorf("SwitchProvider(%q): %v", want, err)
				return
			}
		}
	}()
	for j := 0; j < 40; j++ {
		if e := ca.defaultEngine(); e == nil {
			t.Fatal("defaultEngine() = nil，任务线程下一刻就会拿它当回落引擎")
		}
	}
	<-switched

	if e := ca.defaultEngine(); e == nil {
		t.Fatal("defaultEngine() = nil")
	} else if got := e.Model(); got != "alpha-model" && got != "beta-model" {
		t.Errorf("defaultEngine().Model() = %q, want the last switched model", got)
	}
}

// 启动自检必须真的有人调，而 Validate 的问题无法从外部注入 —— 所以拆成两条：
// 这里钉日志路径（喂假 validator），下一条钉生产路径真的会走到它。
//
// 不调 Init：那要真 config、真 MCP、真浏览器，属于"全流程"而非这段纯逻辑。
func TestCheckRegistryLogsProblems(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError}))

	checkRegistry(func() []string { return nil }, logger)
	if buf.Len() != 0 {
		t.Fatalf("干净注册表不该产生日志，得到 %q", buf.String())
	}

	buf.Reset()
	checkRegistry(func() []string { return []string{"a: 坏", "b: 也坏"} }, logger)
	if out := buf.String(); !strings.Contains(out, "tool registry self-check failed") || !strings.Contains(out, "count=2") {
		t.Fatalf("自检没打日志或没带条数：%q", out)
	}

	if problems := toolspec.Validate(); len(problems) > 0 {
		t.Errorf("生产注册表自检失败：%v", problems)
	}
}

// Init 必须调用注册表自检。上一条只证明"日志路径能响"，删掉 Init 里那行它照样绿 ——
// 而 spec.go 的 Validate 注释"装配期调用"正是靠这一行才是真话。
// Init 需要真 config/MCP/浏览器，没法在单测里跑，所以这里读源码钉调用点。
func TestInitRunsRegistrySelfCheck(t *testing.T) {
	src, err := os.ReadFile("app.go")
	if err != nil {
		t.Fatalf("read app.go: %v", err)
	}
	if !strings.Contains(string(src), "checkRegistry(toolspec.Validate") {
		t.Fatal("app.Init 没有调用 checkRegistry(toolspec.Validate)：注册表自检又会退回'只有测试调它'")
	}
}
