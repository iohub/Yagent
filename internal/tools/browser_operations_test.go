package tools_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"yagent/internal/browserctl"
	"yagent/internal/tools"
	"yagent/internal/toolspec"
)

// fakeRunner 实现 browserctl.Runner：记录 argv 并按命令回放预设输出。
type fakeRunner struct {
	dir      string
	calls    [][]string
	response func(argv []string) (*browserctl.Result, error)
}

func (f *fakeRunner) Run(_ context.Context, command string, args ...string) (*browserctl.Result, error) {
	argv := append([]string{command}, args...)
	f.calls = append(f.calls, argv)
	if f.response != nil {
		return f.response(argv)
	}
	return &browserctl.Result{Command: command, Args: args, Stdout: "ok", ExitCode: 0}, nil
}

func (f *fakeRunner) Dir() string { return f.dir }
func (f *fakeRunner) Release()    {}

// fakeController 实现 browserctl.Controller：只发一次同一个 Runner。
type fakeController struct {
	ok     bool
	reason string
	runner *fakeRunner
	ids    []string
}

func (c *fakeController) Probe() (bool, string) { return c.ok, c.reason }

func (c *fakeController) Acquire(_ context.Context, delegationID string) (browserctl.Runner, error) {
	c.ids = append(c.ids, delegationID)
	return c.runner, nil
}

func newToolSet(t *testing.T) (*tools.BrowserOperationsTool, *fakeController, *fakeRunner) {
	t.Helper()
	cfg := browserctl.DefaultConfig()
	runner := &fakeRunner{dir: t.TempDir()}
	ctrl := &fakeController{ok: true, runner: runner}
	return tools.NewBrowserOperationsTool(ctrl, cfg), ctrl, runner
}

func containsName(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// browserSurface 用真实注册表路径装配浏览器工具面：
// Spec 提供能力标签与 schema，argv/render 仍来自 browserToolSpecs。
// 取代原来的 BuildAdapters —— 那是第二条注册路径，与 Surface 平行存在时迟早漂移。
func browserSurface(t *testing.T, set *tools.BrowserOperationsTool) *tools.Surface {
	t.Helper()
	deps := toolspec.ProfileBrowser.ProbeDeps()
	deps.Browser = set
	return tools.NewSurface(toolspec.ProfileBrowser, deps, tools.SurfaceOptions{})
}

// adapterByName 用同一套 fake Controller/Runner 构建适配器并返回，保证断言能看到真实调用。
func adapterByName(t *testing.T, name string) (*tools.Adapter, *fakeController, *fakeRunner) {
	t.Helper()
	set, ctrl, runner := newToolSet(t)
	for _, a := range browserSurface(t, set).Adapters() {
		if a.Name() == name {
			return a, ctrl, runner
		}
	}
	t.Fatalf("adapter %s not built", name)
	return nil, nil, nil
}

// TestBrowserSpecsMatchRegistry 是"声明了但没注册"这类静默失效的闸门：
// browserToolSpecs 里的每个 argv 实现都必须有对应的注册表 Spec，
// 且该 Spec 必须能把执行器解析出来。
func TestBrowserSpecsMatchRegistry(t *testing.T) {
	set, _, _ := newToolSet(t)
	surface := browserSurface(t, set)
	built := map[string]bool{}
	for _, a := range surface.Adapters() {
		built[a.Name()] = true
	}
	names := tools.BrowserToolNames()
	for _, name := range names {
		if _, ok := toolspec.Get(name); !ok {
			t.Errorf("%s 已在 browserToolSpecs 实现，但注册表没有它的 Spec", name)
		}
		if !built[name] {
			t.Errorf("%s 没有被装配进浏览器工具面", name)
		}
	}
	// 不比对总数：浏览器画像还含流程控制与便签回读族，数量断言只会制造无意义脆弱性。
	// 逐个名字的存在性检查已在上面的循环里完成。
	for name := range built {
		if !strings.HasPrefix(name, "browser_") {
			continue
		}
		if !containsName(names, name) {
			t.Errorf("装配出 %s，但 browserToolSpecs 未实现", name)
		}
	}
}

// TestBrowserToolsAreNeverReadOnly：current-tab 与 ref 是磁盘共享状态，
// 并行观察会读到错页，所以任何 browser 工具都不得标记为只读。
func TestBrowserToolsAreNeverReadOnly(t *testing.T) {
	set, _, _ := newToolSet(t)

	for _, a := range browserSurface(t, set).Adapters() {
		if a.IsReadOnly() {
			t.Errorf("%s must not be marked read-only", a.Name())
		}
	}
}

func TestBrowserClick_BuildsArgvWithDefaults(t *testing.T) {
	adapter, _, runner := adapterByName(t, "browser_click")

	probeAndRun(t, adapter, `{"target":"@e3"}`)

	if len(runner.calls) != 1 {
		t.Fatalf("expected one CLI call, got %v", runner.calls)
	}
	got := strings.Join(runner.calls[0], " ")
	// 默认注入 --wait 3：晚渲染元素不至于立刻失败
	if !strings.Contains(got, "click @e3") || !strings.Contains(got, "--wait 3") {
		t.Errorf("argv = %q", got)
	}
}

func TestBrowserFill_PassesFrameWhenGiven(t *testing.T) {
	adapter, _, runner := adapterByName(t, "browser_fill")

	probeAndRun(t, adapter, `{"target":"#email","value":"a@b.c","frame":"checkout","wait":5}`)

	got := strings.Join(runner.calls[0], " ")
	if !strings.Contains(got, "fill #email a@b.c") || !strings.Contains(got, "--frame checkout") {
		t.Errorf("argv = %q", got)
	}
}

func TestBrowserInspect_ComposesPageInfoAndInspect(t *testing.T) {
	adapter, _, runner := adapterByName(t, "browser_inspect")
	runner.response = func(argv []string) (*browserctl.Result, error) {
		if argv[0] == "inspect" {
			return &browserctl.Result{
				Command:  "inspect",
				Stdout:   `{"url":"https://a.test","title":"T","readyState":"complete","text":"hello","headings":["H"],"elements":[{"ref":"e1","kind":"link","text":"Go"}]}`,
				ExitCode: 0,
			}, nil
		}
		return &browserctl.Result{Command: argv[0], Stdout: `{"url":"https://a.test","title":"T"}`, ExitCode: 0}, nil
	}

	out := probeAndRun(t, adapter, `{"query":"hello","limit":5}`)

	if len(runner.calls) != 2 || runner.calls[0][0] != "page-info" || runner.calls[1][0] != "inspect" {
		t.Fatalf("expected page-info + inspect, got %v", runner.calls)
	}
	argv := strings.Join(runner.calls[1], " ")
	if !strings.Contains(argv, "--json") || !strings.Contains(argv, "-q hello") || !strings.Contains(argv, "--limit 5") {
		t.Errorf("inspect argv = %q", argv)
	}
	if !strings.Contains(out, "url: https://a.test") || !strings.Contains(out, `@e1 link "Go"`) {
		t.Errorf("rendered inspect output missing sections:\n%s", out)
	}
}

func TestBrowserTabs_RequiresAction(t *testing.T) {
	adapter, _, runner := adapterByName(t, "browser_tabs")

	if _, err := adapter.Call(context.Background(), `{}`); err == nil {
		t.Fatal("action is required")
	}
	if len(runner.calls) != 0 {
		t.Errorf("no CLI call should happen on invalid params, got %v", runner.calls)
	}
}

func TestBrowserTabs_NewTabGuardsURL(t *testing.T) {
	adapter, _, runner := adapterByName(t, "browser_tabs")

	if _, err := adapter.Call(context.Background(), `{"action":"new","url":"file:///etc/passwd"}`); err == nil {
		t.Fatal("file:// must be rejected")
	}
	if len(runner.calls) != 0 {
		t.Errorf("rejected URL must not reach the browser, got %v", runner.calls)
	}
}

func TestBrowserScreenshot_ConfinesPathToSessionWorkspace(t *testing.T) {
	adapter, _, runner := adapterByName(t, "browser_screenshot")

	if _, err := adapter.Call(context.Background(), `{"path":"../../escape.png"}`); err == nil {
		t.Fatal("path traversal in screenshot must be rejected")
	}

	out := probeAndRun(t, adapter, `{"full":true}`)
	if !strings.Contains(out, runner.dir) {
		t.Errorf("result should carry the absolute artifact path, got %s", out)
	}
	argv := strings.Join(runner.calls[0], " ")
	if !strings.Contains(argv, runner.dir) || !strings.Contains(argv, "--full") {
		t.Errorf("screenshot argv = %q", argv)
	}
}

func TestBrowserWait_ElementModeRequiresSelector(t *testing.T) {
	adapter, _, runner := adapterByName(t, "browser_wait")

	if _, err := adapter.Call(context.Background(), `{"mode":"element"}`); err == nil {
		t.Fatal("selector is required for mode=element")
	}

	probeAndRun(t, adapter, `{"mode":"element","selector":"#ready","seconds":7,"visible":true}`)
	got := strings.Join(runner.calls[0], " ")
	if !strings.Contains(got, "wait-element #ready 7") || !strings.Contains(got, "--visible") {
		t.Errorf("argv = %q", got)
	}
}

func TestBrowserWait_ClampsSeconds(t *testing.T) {
	adapter, _, runner := adapterByName(t, "browser_wait")

	probeAndRun(t, adapter, `{"mode":"load","seconds":9999}`)
	got := strings.Join(runner.calls[0], " ")
	if !strings.Contains(got, "wait load 60") {
		t.Errorf("seconds must be clamped to the upstream-safe maximum, got %q", got)
	}
}

func TestBrowserFailure_AppendsActionableHint(t *testing.T) {
	adapter, _, runner := adapterByName(t, "browser_click")
	runner.response = func(argv []string) (*browserctl.Result, error) {
		return &browserctl.Result{
			Command:  argv[0],
			Stderr:   "not found: @e3",
			ExitCode: 1,
		}, nil
	}

	_, err := adapter.Call(context.Background(), `{"target":"@e3"}`)
	if err == nil {
		t.Fatal("upstream failure must surface as an error")
	}
	if !strings.Contains(err.Error(), "not found: @e3") {
		t.Errorf("upstream text must be preserved: %v", err)
	}
	if !strings.Contains(err.Error(), "browser_snapshot") {
		t.Errorf("error should tell the model to re-snapshot: %v", err)
	}
}

func TestBrowserStatus_ToleratesMissingPage(t *testing.T) {
	adapter, _, runner := adapterByName(t, "browser_status")
	runner.response = func(argv []string) (*browserctl.Result, error) {
		if argv[0] == "page-info" {
			return &browserctl.Result{Command: "page-info", Stderr: "no page", ExitCode: 1}, nil
		}
		return &browserctl.Result{Command: "doctor", Stdout: "[ok  ] cdp endpoint — ws://x", ExitCode: 0}, nil
	}

	out := probeAndRun(t, adapter, `{}`)
	if !strings.Contains(out, "cdp endpoint") {
		t.Errorf("doctor output should survive a missing page:\n%s", out)
	}
	if !strings.Contains(out, "browser_open") {
		t.Errorf("should suggest opening a page:\n%s", out)
	}
}

func TestBrowserTool_UnavailableCapability(t *testing.T) {
	cfg := browserctl.DefaultConfig()
	runner := &fakeRunner{dir: t.TempDir()}
	ctrl := &fakeController{ok: false, reason: "未找到 browser-control", runner: runner}
	set := tools.NewBrowserOperationsTool(ctrl, cfg)
	adapters := browserSurface(t, set).Adapters()

	var click *tools.Adapter
	for _, a := range adapters {
		if a.Name() == "browser_click" {
			click = a
		}
	}
	if click == nil {
		t.Fatal("browser_click adapter missing")
	}
	_, err := click.Call(context.Background(), `{"target":"@e1"}`)
	if err == nil || !strings.Contains(err.Error(), "浏览器能力不可用") {
		t.Fatalf("unavailable capability must fail fast, got %v", err)
	}
	if len(runner.calls) != 0 {
		t.Errorf("no CLI call should happen when unavailable")
	}
}

func TestBrowserTool_UsesDelegationIDFromContext(t *testing.T) {
	adapter, ctrl, runner := adapterByName(t, "browser_snapshot")

	ctx := browserctl.WithDelegationID(context.Background(), "board-42")
	if _, err := adapter.Call(ctx, `{}`); err != nil {
		t.Fatalf("call: %v", err)
	}
	if len(ctrl.ids) != 1 || ctrl.ids[0] != "board-42" {
		t.Errorf("delegation id not propagated: %v", ctrl.ids)
	}
	if len(runner.calls) != 1 {
		t.Errorf("expected one CLI call, got %v", runner.calls)
	}
}

// probeAndRun 调用工具并断言成功，返回渲染后的 output 文本。
func probeAndRun(t *testing.T, adapter *tools.Adapter, input string) string {
	t.Helper()
	raw, err := adapter.Call(context.Background(), input)
	if err != nil {
		t.Fatalf("call %s: %v", adapter.Name(), err)
	}
	var payload struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("unmarshal result %q: %v", raw, err)
	}
	return payload.Output
}
