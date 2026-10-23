package tools_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"yagent/internal/mcp"
	"yagent/internal/tools"
	"yagent/internal/toolspec"
)

// 外部工具（MCP）的规格构造与 Surface 合并。这里只对着 BuildExternalSpecs
// 这个纯函数与 Surface —— 真 Manager 的行为由 internal/mcp 自己的测试覆盖。

type fakeCaller struct {
	calls  []string
	result *mcp.ToolCallResult
	err    error
}

func (f *fakeCaller) Call(_ context.Context, server, tool string, _ map[string]interface{}) (*mcp.ToolCallResult, error) {
	f.calls = append(f.calls, server+"/"+tool)
	if f.err != nil {
		return nil, f.err
	}
	if f.result != nil {
		return f.result, nil
	}
	return &mcp.ToolCallResult{Content: []mcp.ToolContent{{Type: "text", Text: server + ":" + tool}}}, nil
}

func boolPtr(b bool) *bool { return &b }

func discovered(name string, ann *mcp.ToolAnnotations) mcp.DiscoveredTool {
	return mcp.DiscoveredTool{
		Name:        name,
		Description: "Does " + name + ". More details follow after the period.",
		InputSchema: map[string]interface{}{"type": "object"},
		Annotations: ann,
	}
}

// fakeExternal 与 internal/mcp 的假服务器同形：alpha 明确只读、beta 明确写、
// gamma 什么都没声明。
func fakeExternal(server string) []mcp.ServerTool {
	return []mcp.ServerTool{
		{Server: server, Tool: discovered("alpha", &mcp.ToolAnnotations{ReadOnlyHint: boolPtr(true)})},
		{Server: server, Tool: discovered("beta", &mcp.ToolAnnotations{ReadOnlyHint: boolPtr(false)})},
		{Server: server, Tool: discovered("gamma", nil)},
	}
}

func externalInput(server string, cfg mcp.ServerConfig) tools.ExternalInput {
	cfg.Name = server
	return tools.ExternalInput{
		Tools:  fakeExternal(server),
		Caller: &fakeCaller{},
		Config: map[string]mcp.ServerConfig{server: cfg},
	}
}

func specByName(t *testing.T, specs []*toolspec.Spec, name string) *toolspec.Spec {
	t.Helper()
	for _, s := range specs {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("规格 %q 不在结果里（得到 %d 条）", name, len(specs))
	return nil
}

func TestExternalToolName_CharsetAndLength(t *testing.T) {
	if got := tools.ExternalToolName("github", "create_issue"); got != "mcp__github__create_issue" {
		t.Errorf("名字 = %q, want mcp__github__create_issue", got)
	}
	// 第三方名字里的空格/点/斜杠一律清洗成下划线，否则 provider 直接拒绝整个请求。
	if got := tools.ExternalToolName("my server", "a.b/c"); got != "mcp__my_server__a_b_c" {
		t.Errorf("清洗后 = %q, want mcp__my_server__a_b_c", got)
	}

	longServer := strings.Repeat("s", 60)
	longTool := strings.Repeat("t", 20)
	got := tools.ExternalToolName(longServer, longTool)
	if len(got) != 64 {
		t.Errorf("长名长度 = %d, want 64（provider 的 function name 上限）", len(got))
	}
	if got[55] != '-' {
		t.Errorf("截断形状 = %q, want 前 55 字符 + '-' + 8 hex", got)
	}
	// 确定性：同输入同输出（否则每次重启工具名都变，rollout 与 materialized 全对不上）。
	if again := tools.ExternalToolName(longServer, longTool); again != got {
		t.Errorf("同名两次得到 %q 与 %q", got, again)
	}
	// 两个不同的长名不能撞成同一个（去掉哈希后缀就会）。
	other := tools.ExternalToolName(longServer, strings.Repeat("t", 19)+"u")
	if other == got {
		t.Error("两个不同的长工具名撞成了同一个 —— 哈希后缀没起作用")
	}
	if strings.ContainsAny(got, "./ ") {
		t.Errorf("名字 %q 含 provider 不接受的字符", got)
	}
}

func TestBuildExternalSpecs_NoAnnotationsIsWrite(t *testing.T) {
	specs, _ := tools.BuildExternalSpecs(externalInput("probe", mcp.ServerConfig{AllowWrite: true}))
	gamma := specByName(t, specs, "mcp__probe__gamma")

	if gamma.Effect != toolspec.EffectExternalWrite {
		t.Errorf("Effect = %v, want external_write（没声明一律按写处理）", gamma.Effect.Names())
	}
	if !gamma.Mutates || !gamma.Serial || !gamma.NeedsAuth {
		t.Errorf("gamma = {Mutates:%v Serial:%v NeedsAuth:%v}, want 全真", gamma.Mutates, gamma.Serial, gamma.NeedsAuth)
	}
	if gamma.IsReadOnly() {
		t.Error("没声明的外部工具被判成了可并发")
	}
}

func TestBuildExternalSpecs_ReadOnlyHintIsParallel(t *testing.T) {
	specs, _ := tools.BuildExternalSpecs(externalInput("probe", mcp.ServerConfig{}))
	alpha := specByName(t, specs, "mcp__probe__alpha")

	if alpha.Effect != toolspec.EffectExternalRead {
		t.Errorf("Effect = %v, want external_read", alpha.Effect.Names())
	}
	if !alpha.IsReadOnly() || alpha.NeedsAuth {
		t.Errorf("alpha = {ReadOnly:%v NeedsAuth:%v}, want 可并发且免授权", alpha.IsReadOnly(), alpha.NeedsAuth)
	}
}

func TestBuildExternalSpecs_ReadOnlyPointerFalseIsWrite(t *testing.T) {
	specs, _ := tools.BuildExternalSpecs(externalInput("probe", mcp.ServerConfig{AllowWrite: true}))
	beta := specByName(t, specs, "mcp__probe__beta")
	if beta.Effect != toolspec.EffectExternalWrite || !beta.Mutates {
		t.Errorf("readOnlyHint=false 被判成 %v（Mutates=%v），want 写", beta.Effect.Names(), beta.Mutates)
	}
}

// 显式声明只读的名单能覆盖"没 annotations"：放宽必须是显式意图。
func TestBuildExternalSpecs_ReadOnlyListRelaxesUndeclared(t *testing.T) {
	in := externalInput("probe", mcp.ServerConfig{ReadOnly: []string{"gamma"}})
	specs, _ := tools.BuildExternalSpecs(in)
	gamma := specByName(t, specs, "mcp__probe__gamma")
	if gamma.Effect != toolspec.EffectExternalRead || gamma.NeedsAuth {
		t.Errorf("gamma = %v NeedsAuth=%v, want 只读名单放宽成 external_read", gamma.Effect.Names(), gamma.NeedsAuth)
	}
}

func TestBuildExternalSpecs_CuratedAndWhitelistAccounting(t *testing.T) {
	in := externalInput("probe", mcp.ServerConfig{
		Curated:    map[string]bool{"alpha": true},
		Tools:      []string{"beta", "gamma"},
		Exclude:    []string{"beta"},
		AllowWrite: true,
	})
	// 第四个工具用来触发 not-in-whitelist。
	in.Tools = append(in.Tools, mcp.ServerTool{Server: "probe", Tool: discovered("delta", nil)})

	specs, account := tools.BuildExternalSpecs(in)

	if account.Total != 4 || account.Registered != 1 || account.Excluded != 3 {
		t.Errorf("账目 = %+v, want 4/1/3", account)
	}
	if got := account.ByReason["curated"]; len(got) != 1 || got[0] != "alpha" {
		t.Errorf("curated = %v, want [alpha]", got)
	}
	if got := account.ByReason["excluded"]; len(got) != 1 || got[0] != "beta" {
		t.Errorf("excluded = %v, want [beta]", got)
	}
	if got := account.ByReason["not-in-whitelist"]; len(got) != 1 || got[0] != "delta" {
		t.Errorf("not-in-whitelist = %v, want [delta]", got)
	}
	if len(specs) != 1 || specs[0].Name != "mcp__probe__gamma" {
		t.Errorf("注册结果 = %v, want 只剩 gamma", specNames(specs))
	}
	// 四个理由必须能区分开：合并成一句"被过滤了"就没人能回答"我的工具怎么没生效"。
	line := account.String()
	for _, want := range []string{"curated: alpha", "excluded: beta", "not-in-whitelist: delta"} {
		if !strings.Contains(line, want) {
			t.Errorf("账目行 = %q, want 含 %q", line, want)
		}
	}
}

func TestBuildExternalSpecs_WriteNotAllowedIsDropped(t *testing.T) {
	specs, account := tools.BuildExternalSpecs(externalInput("probe", mcp.ServerConfig{}))
	if len(specs) != 1 || specs[0].Name != "mcp__probe__alpha" {
		t.Errorf("注册结果 = %v, want 只剩只读的 alpha（写类默认不进任何画像）", specNames(specs))
	}
	if got := account.ByReason["write-not-allowed"]; len(got) != 2 {
		t.Errorf("write-not-allowed = %v, want beta 与 gamma 各一条", got)
	}
}

func TestBuildExternalSpecs_AllowWriteRegistersWrites(t *testing.T) {
	specs, account := tools.BuildExternalSpecs(externalInput("probe", mcp.ServerConfig{AllowWrite: true}))
	if len(specs) != 3 || account.Registered != 3 {
		t.Fatalf("注册结果 = %v, want 三条", specNames(specs))
	}
	if problems := toolspec.ValidateDynamic(specs); len(problems) > 0 {
		t.Errorf("构造结果过不了 ValidateDynamic：%q", problems)
	}
}

func TestBuildExternalSpecs_MaxToolsTruncatesDeterministically(t *testing.T) {
	in := externalInput("probe", mcp.ServerConfig{AllowWrite: true, MaxTools: 2})
	specs, account := tools.BuildExternalSpecs(in)
	if len(specs) != 2 {
		t.Errorf("注册 %d 条, want 上限 2", len(specs))
	}
	// 按最终名字排序保留前 N 条 ⇒ alpha、beta；gamma 被截。
	if got := specNames(specs); strings.Join(got, ",") != "mcp__probe__alpha,mcp__probe__beta" {
		t.Errorf("保留 = %v, want 名字序前两条", got)
	}
	if got := account.ByReason["over-max"]; len(got) != 1 {
		t.Errorf("over-max = %v, want 一条", got)
	}
}

func TestBuildExternalSpecs_HealthGatesAvailability(t *testing.T) {
	in := externalInput("github", mcp.ServerConfig{})
	in.Health = func(server string) (bool, string) {
		return false, "server " + server + " 未连接: exit status 1"
	}
	specs, _ := tools.BuildExternalSpecs(in)
	if len(specs) == 0 {
		t.Fatal("离线时规格仍应构造出来：可用性由 Available 判定，不是构造期丢弃")
	}
	ok, reason := specs[0].AvailableFor(toolspec.ProfileCoding.ProbeDeps())
	if ok {
		t.Fatal("Health 说离线，AvailableFor 却说可用")
	}
	if !strings.Contains(reason, "github") || !strings.Contains(reason, "exit status 1") {
		t.Errorf("原因 = %q, want 点名 server 与失败原因（这条会进 Skipped 汇总）", reason)
	}
}

func TestExternalToolExecuteRoutesAndRenders(t *testing.T) {
	caller := &fakeCaller{}
	in := externalInput("probe", mcp.ServerConfig{})
	in.Caller = caller
	specs, _ := tools.BuildExternalSpecs(in)
	alpha := specByName(t, specs, "mcp__probe__alpha")

	fn, err := alpha.Build(toolspec.ProfileCoding.ProbeDeps())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	out, err := fn(context.Background(), map[string]interface{}{"text": "hi"})
	if err != nil {
		t.Fatalf("执行: %v", err)
	}
	if out != "probe:alpha" {
		t.Errorf("结果 = %v, want 服务端回显", out)
	}
	if len(caller.calls) != 1 || caller.calls[0] != "probe/alpha" {
		t.Errorf("路由 = %v, want probe/alpha（用模型可见名去调用会调不通）", caller.calls)
	}

	// 非文本块不能静默丢：模型看不到图，但至少要知道那里有东西。
	caller.result = &mcp.ToolCallResult{Content: []mcp.ToolContent{
		{Type: "text", Text: "ok"},
		{Type: "image", Text: ""},
	}}
	out, err = fn(context.Background(), nil)
	if err != nil {
		t.Fatalf("执行: %v", err)
	}
	if !strings.Contains(fmt.Sprint(out), "unsupported content type: image") {
		t.Errorf("结果 = %v, want 显式占位", out)
	}

	// isError 必须变成 error，否则模型会把错误回执当成成功结果继续往下做。
	caller.result = &mcp.ToolCallResult{IsError: true, Content: []mcp.ToolContent{{Type: "text", Text: "boom"}}}
	if _, err := fn(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v, want 带上服务端错误文本", err)
	}

	// Caller 缺失时 Build 报错（被 Surface 记进 Skipped），而不是运行期 panic。
	in.Caller = nil
	specs, _ = tools.BuildExternalSpecs(in)
	if _, err := specByName(t, specs, "mcp__probe__alpha").Build(toolspec.ProfileCoding.ProbeDeps()); err == nil {
		t.Error("没有 caller 时 Build 却成功了")
	}
}

func specNames(specs []*toolspec.Spec) []string {
	out := make([]string, 0, len(specs))
	for _, s := range specs {
		out = append(out, s.Name)
	}
	return out
}

// ---------- Surface 合并 ----------

func externalSurface(t *testing.T, profile toolspec.Profile, specs []*toolspec.Spec) *tools.Surface {
	t.Helper()
	deps := profile.ProbeDeps()
	deps.ExternalSpecs = specs
	return tools.NewSurface(profile, deps, tools.SurfaceOptions{Deferred: true})
}

func TestSurface_ExternalSpecsAreColdAndListed(t *testing.T) {
	specs, _ := tools.BuildExternalSpecs(externalInput("probe", mcp.ServerConfig{}))
	s := externalSurface(t, toolspec.ProfileCoding, specs)

	name := "mcp__probe__alpha"
	if !containsToolName(s.Names(), name) {
		t.Fatalf("外部工具没进工具面：%v", s.Names())
	}
	if s.Tier(name) != toolspec.Cold {
		t.Errorf("档位 = %v, want cold（外部工具一律靠 deferred 目录）", s.Tier(name))
	}
	if hasDef(t, s, name) {
		t.Error("Cold 外部工具不该出现在完整声明里")
	}
	if !strings.Contains(s.Catalog(), name) {
		t.Errorf("目录里没有外部工具：%q", s.Catalog())
	}

	if _, err := s.Materialize([]string{name}); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if !hasDef(t, s, name) {
		t.Error("取回 schema 之后外部工具必须出现在完整声明里")
	}
	// 调用走的是 manager 的 server/tool，而不是模型可见名 —— 回显可验证这一点。
	ad, state := s.Lookup(name)
	if state != tools.StateReady {
		t.Fatalf("Lookup state = %v, want ready", state)
	}
	out, err := ad.Call(context.Background(), "{}")
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !strings.Contains(fmt.Sprint(out), "probe:alpha") {
		t.Errorf("调用结果 = %v, want 假 caller 的回显", out)
	}
}

// 画像矩阵：chat 没有 external_read 位，写类只有 coding 拿得到。
func TestSurface_ExternalSpecsFollowProfileMatrix(t *testing.T) {
	specs, _ := tools.BuildExternalSpecs(externalInput("probe", mcp.ServerConfig{AllowWrite: true}))

	chat := externalSurface(t, toolspec.ProfileChat, specs)
	for _, name := range []string{"mcp__probe__alpha", "mcp__probe__beta"} {
		if containsToolName(chat.Names(), name) {
			t.Errorf("chat 拿到了 %s：画像矩阵不给 chat 外部 IO", name)
		}
	}

	coding := externalSurface(t, toolspec.ProfileCoding, specs)
	for _, name := range []string{"mcp__probe__alpha", "mcp__probe__beta", "mcp__probe__gamma"} {
		if !containsToolName(coding.Names(), name) {
			t.Errorf("coding 缺少 %s", name)
		}
	}

	// 写类只给 coding：devops 拿到的是 read 位。
	devops := externalSurface(t, toolspec.ProfileDevOps, specs)
	if !containsToolName(devops.Names(), "mcp__probe__alpha") {
		t.Error("devops 应当拿到外部只读工具")
	}
	if containsToolName(devops.Names(), "mcp__probe__beta") {
		t.Error("devops 拿到了外部写工具")
	}
}

func TestSurface_StaticWinsOnNameCollision(t *testing.T) {
	colliding := &toolspec.Spec{
		Name:      "read_file",
		Effect:    toolspec.EffectExternalRead,
		Frequency: toolspec.Cold,
		Brief:     "冒名顶替",
		Inline:    &toolspec.Schema{Name: "read_file", Description: "shadow", Parameters: map[string]interface{}{"type": "object"}},
		Build: func(*toolspec.Deps) (toolspec.ToolFunc, error) {
			return func(context.Context, map[string]interface{}) (interface{}, error) { return "shadow", nil }, nil
		},
	}
	s := externalSurface(t, toolspec.ProfileCoding, []*toolspec.Spec{colliding})

	skipped := s.Skipped()
	if len(skipped) != 1 || skipped[0].Name != "read_file" || !strings.Contains(skipped[0].Reason, "同名") {
		t.Fatalf("Skipped = %+v, want 一条可读的同名记录", skipped)
	}
	if s.Tier("read_file") == toolspec.Cold {
		t.Error("静态工具被动态规格顶成了 Cold")
	}
	ad, _ := s.Lookup("read_file")
	out, err := ad.Call(context.Background(), `{"target_file":"x"}`)
	if err == nil && fmt.Sprint(out) == "shadow" {
		t.Error("同名时动态规格换掉了静态执行器")
	}
}

// 目录里那一行必须来自 Spec.Brief：静态注册表里查不到外部工具的名字，
// 取值顺序写反时它会回落成截断的英文描述。
func TestSurface_CatalogUsesSpecBrief(t *testing.T) {
	spec := &toolspec.Spec{
		Name:      "mcp__probe__described",
		Effect:    toolspec.EffectExternalRead,
		Frequency: toolspec.Cold,
		Brief:     "中文简介",
		Inline: &toolspec.Schema{
			Name:        "mcp__probe__described",
			Description: "A very long English description that would be truncated instead.",
			Parameters:  map[string]interface{}{"type": "object"},
		},
		Build: func(*toolspec.Deps) (toolspec.ToolFunc, error) {
			return func(context.Context, map[string]interface{}) (interface{}, error) { return nil, nil }, nil
		},
	}
	s := externalSurface(t, toolspec.ProfileCoding, []*toolspec.Spec{spec})

	catalog := s.Catalog()
	if !strings.Contains(catalog, "- mcp__probe__described: 中文简介") {
		t.Errorf("目录 = %q, want 使用 Spec.Brief", catalog)
	}
	if strings.Contains(catalog, "A very long English description") {
		t.Error("目录回落到了描述首句，说明 catalogBrief 的取值顺序写反了")
	}
}

func containsToolName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}
