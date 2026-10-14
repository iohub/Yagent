package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yagent/internal/globalctx"
	"yagent/internal/llm"
	"yagent/internal/memory"
	"yagent/internal/messaging"
)

// app 包是一次性装配点，跑起来要真 LLM、真 MCP、真浏览器，不适合在这里做全流程测试。
// 但它有几段"纯逻辑却决定别人能不能拿到自己那份上下文"的代码，出了错全是静默失效：
// 任务请求的装配、引擎包装层、被禁用 Agent 的解析、项目标识的推导。

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

// getProjectID 是共享记忆与 rollout 目录的主键：同一个仓库在不同进程里必须算出
// 同一个 ID，否则 /resume 与长期记忆会散成多份互不可见的目录。
func TestGetProjectID(t *testing.T) {
	id := getProjectID("/home/user/my-app")
	if !strings.HasPrefix(id, "my_app_") {
		t.Errorf("id = %q, want 以 my_app_ 开头（可读前缀 + 短哈希）", id)
	}
	if len(id) != len("my_app_")+12 {
		t.Errorf("id 长度 = %d, want 前缀 + 12 位哈希: %q", len(id), id)
	}

	// 稳定性：同一路径每次都相同。
	if again := getProjectID("/home/user/my-app"); again != id {
		t.Errorf("同一路径两次算出 %q 与 %q", id, again)
	}
	// 唯一性：路径不同必须不同（哪怕 basename 相同）。
	a := getProjectID("/home/alice/app")
	b := getProjectID("/home/bob/app")
	if a == b {
		t.Errorf("不同目录同名算出同一个 ID: %q", a)
	}
	if !strings.HasPrefix(a, "app_") || !strings.HasPrefix(b, "app_") {
		t.Errorf("可读前缀丢失: %q / %q", a, b)
	}

	// 文件系统安全：非法字符一律换成下划线，绝不出现分隔符。
	for _, p := range []string{`/tmp/we"ird name`, "/tmp/a/b/c", "/tmp/../etc/passwd"} {
		got := getProjectID(p)
		if strings.ContainsAny(got, `/"\ `) {
			t.Errorf("getProjectID(%q) = %q, 含路径分隔符或空格", p, got)
		}
		if len(filepath.Base(got)) != len(got) {
			t.Errorf("%q 会被当成多层路径", got)
		}
	}

	// 根目录与相对路径的退化输入不能产出空名。
	for _, p := range []string{"/", ".", ""} {
		if got := getProjectID(p); got == "" || !strings.Contains(got, "_") {
			t.Errorf("getProjectID(%q) = %q, want 仍然可用", p, got)
		}
	}

	// 单个目录名components 受 255 字节限制：长名字必须截断而不是产出超长组件。
	long := "/" + strings.Repeat("x", 400)
	if got := getProjectID(long); len(got) > 255 {
		t.Errorf("超长目录名的 ID 长度 = %d, want ≤255", len(got))
	}
}

// getSharedMemoryPath 是仓库级长期记忆的位置。它必须由 ProjectPath 推导，
// 否则两个项目的记忆会写进同一个文件。
func TestGetSharedMemoryPathIsPerProject(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("os.UserHomeDir: %v", err)
	}

	ca := &Yagent{globalCtx: &globalctx.GlobalCtx{ProjectPath: "/tmp/proj-one"}}
	first := ca.getSharedMemoryPath()
	if !strings.HasSuffix(first, filepath.Join("shared_memory", getProjectID("/tmp/proj-one"), "shared_memory.json")) {
		t.Errorf("路径 = %q, want $HOME/.yagent/data/shared_memory/{projectID}/shared_memory.json", first)
	}
	if !strings.HasPrefix(first, home) {
		t.Errorf("路径 %q 不在 home 下", first)
	}

	other := &Yagent{globalCtx: &globalctx.GlobalCtx{ProjectPath: "/tmp/proj-two"}}
	if second := other.getSharedMemoryPath(); second == first {
		t.Error("两个不同项目算出了同一个共享记忆路径")
	}
}
