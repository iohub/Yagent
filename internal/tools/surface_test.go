package tools_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"yagent/internal/tools"
	"yagent/internal/toolspec"
)

func codingDeps() *toolspec.Deps {
	deps := toolspec.ProfileCoding.ProbeDeps()
	deps.AgentName = "coding"
	return deps
}

func deferredSurface(t *testing.T, budget int) *tools.Surface {
	t.Helper()
	return tools.NewSurface(toolspec.ProfileCoding, codingDeps(), tools.SurfaceOptions{
		Deferred:     true,
		BudgetTokens: budget,
	})
}

// TestSurface_DefersColdTools 是 deferred schema 的主路径：
// Cold 工具不在每步声明里，而是出现在目录中，取回后才进入声明。
func TestSurface_DefersColdTools(t *testing.T) {
	s := deferredSurface(t, 0)

	if s.Tier("print_dir_tree") != toolspec.Cold {
		t.Fatal("fixture moved: print_dir_tree 不再是 Cold 档")
	}
	if hasDef(t, s, "print_dir_tree") {
		t.Error("Cold 工具不应出现在完整声明里")
	}
	if !strings.Contains(s.Catalog(), "print_dir_tree") {
		t.Error("冷工具必须出现在 deferred 目录里")
	}
	if !hasDef(t, s, "load_tool") {
		t.Error("有未取回的冷工具时，load_tool 必须在场")
	}

	out, err := s.Materialize([]string{"print_dir_tree"})
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if !strings.Contains(out, `"print_dir_tree"`) || !strings.Contains(out, "dir_path") {
		t.Errorf("Materialize 应返回完整 schema，得到：%s", out)
	}
	if !hasDef(t, s, "print_dir_tree") {
		t.Error("取回后应进入完整声明")
	}
	if strings.Contains(s.Catalog(), "print_dir_tree") {
		t.Error("取回后不该继续留在目录里")
	}
}

// 所有冷工具都取回后，目录与 load_tool 一起消失 —— 不能留一个空工具在那儿。
func TestSurface_DropsLoadToolWhenCatalogEmptied(t *testing.T) {
	s := deferredSurface(t, 0)
	var cold []string
	for _, name := range s.Names() {
		if s.Tier(name) == toolspec.Cold {
			cold = append(cold, name)
		}
	}
	if len(cold) == 0 {
		t.Fatal("fixture has no cold tools")
	}
	if _, err := s.Materialize(cold); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if hasDef(t, s, "load_tool") {
		t.Error("没有待取回的冷工具时不应再发 load_tool")
	}
	if got := s.Catalog(); got != "" {
		t.Errorf("目录应为空，得到：%q", got)
	}
}

// 模型跳过 load_tool 直接调冷工具时，工具面必须报 ColdKnown 而不是 Unknown，
// 执行循环据此"先 teach 一次"，而不是把一次正常调用变成死路。
func TestSurface_LookupStates(t *testing.T) {
	s := deferredSurface(t, 0)

	if _, state := s.Lookup("read_file"); state != tools.StateReady {
		t.Errorf("read_file state = %v, want Ready", state)
	}
	if _, state := s.Lookup("deepthinking"); state != tools.StateColdKnown {
		t.Errorf("冷工具 state = %v, want ColdKnown", state)
	}
	if _, state := s.Lookup("print_dir_tree"); state != tools.StateColdKnown {
		t.Errorf("print_dir_tree（Coding 的冷工具）state = %v, want ColdKnown", state)
	}
	if _, state := s.Lookup("totally_made_up"); state != tools.StateUnknown {
		t.Errorf("未注册名字 state = %v, want Unknown", state)
	}
	// 取回后同一工具转 Ready。
	if _, err := s.Materialize([]string{"deepthinking"}); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if _, state := s.Lookup("deepthinking"); state != tools.StateReady {
		t.Errorf("取回后 state = %v, want Ready", state)
	}
}

// 关掉 deferred 就是整体退回历史行为：所有工具都带完整声明，没有目录也没有 load_tool。
// 这是留给排障的开关，必须有测试保证它真的等价。
func TestSurface_DeferredDisabledIsLegacyBehavior(t *testing.T) {
	s := tools.NewSurface(toolspec.ProfileCoding, codingDeps(), tools.SurfaceOptions{})
	for _, name := range s.Names() {
		if !hasDef(t, s, name) {
			t.Errorf("%s 缺完整声明（deferred 已关闭）", name)
		}
	}
	if hasDef(t, s, "load_tool") {
		t.Error("deferred 关闭时不应有 load_tool")
	}
	if got := s.Catalog(); got != "" {
		t.Errorf("deferred 关闭时目录应为空，得到 %q", got)
	}
	if _, err := s.Materialize([]string{"design"}); err == nil {
		t.Error("deferred 关闭时 Materialize 应报错，避免静默无效")
	}
}

// Materialize 是 append-only：取回过的工具不能被重新藏起来。
// 工具列表来回抖动会让 provider 端前缀缓存整段失效，比多带几个声明更贵。
func TestSurface_MaterializeIsAppendOnly(t *testing.T) {
	s := deferredSurface(t, 0)
	if _, err := s.Materialize([]string{"design"}); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	second, err := s.Materialize([]string{"design"})
	if err != nil {
		t.Fatalf("re-Materialize: %v", err)
	}
	if !strings.Contains(second, "Already available") {
		t.Errorf("重复取回应提示已在场：%s", second)
	}
	if !hasDef(t, s, "design") {
		t.Error("design 必须继续留在完整声明里")
	}
}

// 未知名字不报错而是回吐可用清单：LLM 拼错工具名时应当能自纠。
func TestSurface_MaterializeUnknownNames(t *testing.T) {
	s := deferredSurface(t, 0)
	out, err := s.Materialize([]string{"nope"})
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if !strings.Contains(out, "Unknown: nope") {
		t.Errorf("应列出未知名字：%s", out)
	}
	if !strings.Contains(out, "Deferred tools available to load:") {
		t.Errorf("应回吐可加载清单：%s", out)
	}
}

// 预算降级：预算极紧时把 Warm 工具降成 Cold，但绝不碰 Hot。
func TestSurface_BudgetDemotesWarmOnly(t *testing.T) {
	before := tools.NewSurface(toolspec.ProfileCoding, codingDeps(), tools.SurfaceOptions{Deferred: true})
	warm := map[string]bool{}
	for _, name := range before.Names() {
		if before.Tier(name) == toolspec.Warm {
			warm[name] = true
		}
	}
	if len(warm) == 0 {
		// 画像里一个 Warm 工具都没有，降级路径就没人覆盖了 —— 静默跳过比失败更糟。
		t.Fatal("ProfileCoding 里没有 Warm 工具：本测试失去意义，请更新 fixture 或删除它")
	}

	after := deferredSurface(t, 1) // 1 token 预算，必然触发降级

	demoted := 0
	for name := range warm {
		switch after.Tier(name) {
		case toolspec.Cold:
			demoted++
		case toolspec.Hot:
			t.Errorf("%s 从 Warm 被提到 Hot，不该发生", name)
		}
	}
	if demoted == 0 {
		t.Errorf("预算=1 时没有任何 Warm 工具被降级（Warm 共 %d 个）", len(warm))
	}

	// 降级只动 Warm：原本 Hot 的必须还是 Hot。
	for _, name := range after.Names() {
		if before.Tier(name) != toolspec.Hot && after.Tier(name) == toolspec.Hot {
			t.Errorf("%s 从 %v 被提到 Hot，不该发生", name, before.Tier(name))
		}
	}
	// 流程控制与元工具必须仍是 Hot。
	for _, name := range []string{"agent_exit", "ask_user_for_help", "load_tool", "run_bash"} {
		if after.Tier(name) != toolspec.Hot {
			t.Errorf("%s 被降级成 %v：收尾/写操作工具不能依赖额外一次往返", name, after.Tier(name))
		}
	}
}

// 依赖缺失的工具被跳过并带上原因，而不是注册一个调用即 panic 的适配器。
//
// 注意链接可见性：Spec 靠各包的 init() 自注册，所以本文件（package tools_test）
// 只看得见 tools 包注册的那批。git_checkpoint_*/read_note/delegate_* 由 agents 包
// 注册，它们的跳过路径在 internal/agents/registry_gate_test.go 里覆盖。
func TestSurface_SkipsUnboundTools(t *testing.T) {
	deps := codingDeps()
	deps.Design = nil
	deps.Repo = nil
	s := tools.NewSurface(toolspec.ProfileCoding, deps, tools.SurfaceOptions{Deferred: true})

	skipped := map[string]string{}
	for _, item := range s.Skipped() {
		skipped[item.Name] = item.Reason
	}
	for _, name := range []string{"design", "semantic_search", "query_call_graph"} {
		if _, ok := skipped[name]; !ok {
			t.Errorf("%s 应被跳过（执行器未绑定）", name)
		}
	}
	for _, name := range s.Names() {
		if reason, bad := skipped[name]; bad {
			t.Errorf("%s 同时出现在工具面和跳过清单里（原因 %q）", name, reason)
		}
	}
	if reason := skipped["semantic_search"]; !strings.Contains(reason, "codeseek") {
		t.Errorf("跳过原因应说明缺什么，得到 %q", reason)
	}
	// 被跳过的工具在画像里仍然是能力匹配对象，所以必须能在 Lookup 里被识别为 Unknown，
	// 否则执行循环会把"没装配"当成"模型幻觉"来纠正。
	if _, state := s.Lookup("semantic_search"); state != tools.StateUnknown {
		t.Errorf("未装配的工具 state = %v, want Unknown", state)
	}
}

// FullYolo 在注册期摘掉 ask_user_for_help（历史上由 6 个 Agent 各自判断）。
func TestSurface_FullYoloRemovesAskUser(t *testing.T) {
	deps := codingDeps()
	deps.FullYolo = true
	s := tools.NewSurface(toolspec.ProfileCoding, deps, tools.SurfaceOptions{Deferred: true})
	for _, name := range s.Names() {
		if name == "ask_user_for_help" {
			t.Fatal("FULL-YOLO 下不应注册 ask_user_for_help")
		}
	}
}

// 运行期追加（Meta 的 Custom Agent）：立即可执行，且下一步就对模型可见。
// 这修掉了"本回合注册、下一回合才看得见"的老限制。
func TestSurface_AddIsVisibleNextStep(t *testing.T) {
	s := deferredSurface(t, 0)
	spec := &toolspec.Spec{
		Name:      "delegate_security_auditor",
		Effect:    toolspec.EffectDelegate,
		Frequency: toolspec.Hot,
		Mutates:   true,
		Build: func(*toolspec.Deps) (toolspec.ToolFunc, error) {
			return func(context.Context, map[string]interface{}) (interface{}, error) { return "done", nil }, nil
		},
	}
	if got := s.Add(spec); len(got) != 0 {
		t.Fatalf("Add 返回跳过项：%v", got)
	}
	if !hasDef(t, s, "delegate_security_auditor") {
		t.Error("追加的工具应立刻进入声明")
	}
	ad, state := s.Lookup("delegate_security_auditor")
	if state != tools.StateReady || ad == nil {
		t.Errorf("追加的工具应可执行，得到 state=%v ad=%v", state, ad != nil)
	}
	// 重复 Add 同名工具必须被拒绝并记录，而不是悄悄换掉正在使用的执行器。
	dup := s.Add(spec)
	if len(dup) != 1 || dup[0].Name != spec.Name {
		t.Errorf("重复 Add 应记录 1 个跳过项，得到 %v", dup)
	}
}

// 并发 Materialize 与并发 Defs 不能撕坏内部缓存（执行循环里工具可能并行跑）。
func TestSurface_ConcurrentMaterializeAndDefs(t *testing.T) {
	s := deferredSurface(t, 0)
	cold := []string{"design", "deepthinking", "print_dir_tree", "query_call_graph", "query_code_skeleton"}

	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%3 == 0 {
				_, _ = s.Materialize([]string{cold[i%len(cold)]})
				return
			}
			defs := s.Defs()
			for _, d := range defs {
				if d.Function.Name == "" {
					t.Error("空工具名进入声明")
				}
			}
			_ = s.Catalog()
		}(i)
	}
	wg.Wait()
}

// Resume 恢复：已取回的冷工具不该再付一次 load_tool。
func TestSurface_RestoreMaterialized(t *testing.T) {
	s := deferredSurface(t, 0)
	if hasDef(t, s, "design") {
		t.Fatal("fixture: design 初始应为冷")
	}
	s.RestoreMaterialized([]string{"design", "not_a_tool"})
	if !hasDef(t, s, "design") {
		t.Error("恢复后 design 应进入完整声明")
	}
	if got := s.Materialized(); len(got) != 1 || got[0] != "design" {
		t.Errorf("Materialized() = %v, want [design]", got)
	}
}

func hasDef(t *testing.T, s *tools.Surface, name string) bool {
	t.Helper()
	for _, d := range s.Defs() {
		if d.Function.Name == name {
			return true
		}
	}
	return false
}

func noopToolFunc(_ context.Context, _ map[string]interface{}) (interface{}, error) {
	return "ok", nil
}

// NewSurfaceFromAdapters 是 C3 之后测试唯一的工具面入口。它必须让执行循环问出与生产
// 同一份答案 —— 尤其是"这个工具只读吗"（并行准入）与"这个名字有没有"。
// 保留一条测试专用的第二载体（旧的 ExecutorConfig.Adapters）正是"测试全绿而生产不并行"
// 那种不对称的温床，见 docs/structured-concurrency-design.md R1。
func TestSurfaceFromAdapters_ReportsWhatTheLoopAsks(t *testing.T) {
	ro := tools.NewAdapter("ro_search", "只读搜索", noopToolFunc).WithReadOnly(true)
	rw := tools.NewAdapter("write_file", "可写", noopToolFunc)
	ghost := tools.NewAdapter("", "空名", noopToolFunc)

	s := tools.NewSurfaceFromAdapters("test-agent", ro, rw, nil, ghost)

	ad, state := s.Lookup("ro_search")
	if state != tools.StateReady {
		t.Errorf("ro_search state = %v, want Ready（未开 deferred 时不该有冷工具）", state)
	}
	if ad == nil || !ad.IsReadOnly() {
		t.Error("ro_search 的只读属性丢了 —— 并行准入就靠这一个回答")
	}
	ad, state = s.Lookup("write_file")
	if state != tools.StateReady {
		t.Errorf("write_file state = %v, want Ready", state)
	}
	if ad == nil || ad.IsReadOnly() {
		t.Error("write_file 被报成只读 —— 那它会被放进并行批次")
	}
	if _, state := s.Lookup("totally_made_up"); state != tools.StateUnknown {
		t.Errorf("未注入的名字 state = %v, want Unknown", state)
	}

	if got := s.Adapters(); len(got) != 2 {
		t.Errorf("适配器数 = %d, want 2 —— nil 与空名不该变成幽灵条目", len(got))
	}
	defs := s.Defs()
	if len(defs) != 2 {
		t.Fatalf("声明数 = %d, want 2", len(defs))
	}
	// 按名排序：provider 端的 prompt 前缀缓存依赖它。
	if defs[0].Function.Name != "ro_search" || defs[1].Function.Name != "write_file" {
		t.Errorf("声明顺序 = [%s %s], want 按名排序",
			defs[0].Function.Name, defs[1].Function.Name)
	}
	// load_tool 只在有待取回的冷工具时才出现；这里一个都没有。
	for _, d := range defs {
		if d.Function.Name == "load_tool" {
			t.Error("没有冷工具却声明了 load_tool")
		}
	}
	if c := s.Catalog(); c != "" {
		t.Errorf("未开 deferred 不该有目录，got %q", c)
	}
	if names := s.Names(); len(names) != 2 {
		t.Errorf("Names() = %v, want 2 个", names)
	}
}

// 空工具面是合法形状（有些 Agent 就是没有工具，或测试只关心 LLM 侧行为）。
func TestSurfaceFromAdapters_EmptyIsUsable(t *testing.T) {
	s := tools.NewSurfaceFromAdapters("chat", nil)
	if len(s.Defs()) != 0 {
		t.Errorf("Defs() = %v, want 空", s.Defs())
	}
	if _, state := s.Lookup("anything"); state != tools.StateUnknown {
		t.Errorf("空面上 Lookup = %v, want Unknown", state)
	}
	if len(s.Skipped()) != 0 {
		t.Errorf("手工装配不该凭空报落选: %v", s.Skipped())
	}
}
