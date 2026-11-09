package agents

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"yagent/internal/plasma"
	"yagent/internal/tools"
	"yagent/internal/toolspec"
)

// 本文件是重构的三道闸门之二（第一道是 toolspec 自己的单元测试）。
//
// TestRegistryMatchesSchemas 把背景文档 §8 的头号特征——"名字对不上就静默少一个工具"——
// 变成测试失败：声明文件、注册表、画像三者必须互相对账。
// TestAgentSurfacesGolden 则把"能力画像自动匹配"的实际结果钉进
// testdata/tool_surfaces.golden。改 Spec 或改画像都会让它变红，
// 那个 diff 就是评审物：新增/移除工具落到哪个 Agent 上，一目了然。

func TestRegistryMatchesSchemas(t *testing.T) {
	if err := toolspec.SchemasError(); err != nil {
		t.Fatalf("schemas.json 解析失败：%v", err)
	}
	problems := toolspec.Validate()
	if len(problems) == 0 {
		return
	}
	t.Fatalf("注册表自检失败（%d 项）：\n%s", len(problems), strings.Join(problems, "\n"))
}

func TestBuiltinProfilesValidate(t *testing.T) {
	for _, p := range toolspec.BuiltinProfiles() {
		for _, problem := range toolspec.ValidateProfile(p) {
			t.Errorf("profile %q: %s", p.Agent, problem)
		}
	}
}

// goldenUpdate 让维护者在确认变更合理后显式重写基线：
//
//	go test ./internal/agents -run TestAgentSurfacesGolden -update
var goldenUpdate = flag.Bool("update", false, "rewrite the golden surface file")

func TestAgentSurfacesGolden(t *testing.T) {
	got := renderSurfaces()
	path := filepath.Join("testdata", "tool_surfaces.golden")

	if *goldenUpdate {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Skipf("golden rewritten at %s", path)
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (%s): %v — 首次生成用 -update", path, err)
	}
	if string(want) != got {
		t.Errorf("工具面与 golden 基线不一致。确认这是 docs/tool-registry-design.md §9 签过字的变更后，用 -update 重写基线。\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// renderSurfaces 输出每个内建画像的工具面，含频率档与并发/授权属性。
func renderSurfaces() string {
	var b strings.Builder
	b.WriteString("# 各 Agent 的工具面（由 toolspec 画像解析得出，-update 重写）\n")
	b.WriteString("# tier: hot|warm|cold  ro: 可并发  auth: 需用户授权  serial: 必须串行\n")
	for _, p := range toolspec.BuiltinProfiles() {
		deps := p.ProbeDeps()
		specs := toolspec.Match(p, deps)
		sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })

		fmt.Fprintf(&b, "\n[%s] %d tools  %s\n", p.Agent, len(specs), p.EffectSummary())
		for _, s := range specs {
			flags := []string{}
			if s.IsReadOnly() {
				flags = append(flags, "ro")
			}
			if s.Serial {
				flags = append(flags, "serial")
			}
			if s.NeedsAuth {
				flags = append(flags, "auth")
			}
			if s.BlocksOnUser {
				flags = append(flags, "blocks")
			}
			fmt.Fprintf(&b, "  %-24s tier=%-4s %s\n",
				s.Name, p.Tier(s).String(), strings.Join(flags, ","))
		}
	}
	return b.String()
}

// TestDerivedMetadataMatchesLegacyTables 是 Step 8 的验收闸门。
//
// 下面这些表是从 HEAD 逐字抄来的（knownReadOnlyTools / nonParallelToolNames /
// dangerousTools + pathParamNames / toolTruncationPriority / isInteractiveUserTool /
// skipBodyTools）。注册表派生出的值必须与它们一致，唯一允许的偏差是
// docs/tool-registry-design.md §9.7 签字过的"并发面扩大"，这里把它枚举出来，
// 免得变成一条没人 notice 的静默行为变更。
func TestDerivedMetadataMatchesLegacyTables(t *testing.T) {
	legacyNeedsAuth := []string{"create_file", "search_replace_in_file", "delete_file", "rename_file", "run_bash", "browser_eval"}
	legacyPathParams := map[string][]string{
		"create_file":            {"target_file"},
		"search_replace_in_file": {"file_path"},
		"delete_file":            {"target_file", "file_paths"},
		"rename_file":            {"file_path", "rename_file_path"},
	}
	legacyInteractive := []string{"ask_user_for_help"}
	legacyCompactDisplay := []string{"read_file", "delete_file", "rename_file", "list_dir", "print_dir_tree", "search_by_regex", "semantic_search"}
	legacyTruncFirst := []string{
		"create_file", "search_replace_in_file", "read_file", "run_bash",
		"semantic_search", "query_code_skeleton", "query_code_snippet",
		"print_dir_tree", "search_by_regex", "query_call_graph",
		"find_function_callee", "find_function_caller",
		"browser_snapshot", "browser_inspect", "browser_eval", "browser_status",
	}
	legacyTruncProtected := []string{"deepthinking", "design"}
	// legacy 的"可并发"白名单只有 7 项，与语义无关（背景文档 §8.6）。
	// legacy 的"可并发"来源有两处：knownReadOnlyTools 那 7 项白名单，
	// 以及 Director 里显式 WithReadOnly(true) 的两个委派（背景文档 §5.4）。
	legacyReadOnly := []string{"read_file", "list_dir", "print_dir_tree", "search_by_regex",
		"semantic_search", "query_code_skeleton", "query_code_snippet",
		"delegate_repo", "delegate_chat"}
	// §9.7 签字过的扩大：这些工具本来就无副作用，只是没被写进那 7 项里。
	signedReadOnlyAdditions := map[string]bool{
		"find_function_callee": true, "find_function_caller": true, "query_call_graph": true,
		"read_note": true, "read_artifact": true,
		"thinking": true, "micro_agent": true, "deepthinking": true, "design": true,
		// read_blueprint 与 read_note 同族：只读一次派生快照 + 一份 markdown，
		// 没有副作用、不串行（设计稿 §7.1 的 Spec 判定）。
		"read_blueprint": true,
		// use_skill 只读本地技能文件：没有共享写端，也不碰 MCP 的 stdin。
		"use_skill": true,
	}

	// NeedsAuth 必须与历史 dangerousTools 完全一致。
	for _, name := range legacyNeedsAuth {
		if !toolspec.NeedsAuth(name) {
			t.Errorf("%s: 历史需要授权，派生结果不需要", name)
		}
	}
	for _, name := range toolspec.Names() {
		if toolspec.NeedsAuth(name) && !contains(legacyNeedsAuth, name) {
			t.Errorf("%s: 派生出新的高温授权工具，历史里没有", name)
		}
	}

	// PathParams 必须是历史集合的子集，且不能指向 schema 里不存在的参数。
	for name, legacy := range legacyPathParams {
		got := toolspec.PathParams(name)
		for _, p := range got {
			if !contains(legacy, p) {
				t.Errorf("%s: PathParams 多出历史没有的 %q", name, p)
			}
			schema, ok := toolspec.GetSchema(name)
			if !ok || !schema.HasProperty(p) {
				t.Errorf("%s: PathParams %q 在 schemas.json 里不存在", name, p)
			}
		}
	}

	for _, name := range legacyInteractive {
		if !toolspec.IsInteractive(name) {
			t.Errorf("%s: 历史豁免超时，派生结果不豁免", name)
		}
	}
	for _, name := range toolspec.Names() {
		if toolspec.IsInteractive(name) && !contains(legacyInteractive, name) {
			t.Errorf("%s: 新工具被豁免 180s 超时，需要显式评审", name)
		}
	}

	// newTools 是本次重构引入的工具，没有历史基线可比，只需确认它们没被误标。
	// list_notes：只读枚举本地便签板的 front-matter，无共享写端（不像 codeseek 桥接
	// 那批要抢 MCP 的 stdin），也无外部进程 —— 并行安全不依赖任何前置修复。
	newTools := map[string]bool{"load_tool": true, "list_notes": true}

	for _, name := range legacyCompactDisplay {
		if !toolspec.IsCompactDisplay(name) {
			t.Errorf("%s: 历史上 TUI 不展开正文，派生结果会展开", name)
		}
	}
	for _, name := range toolspec.Names() {
		if toolspec.IsCompactDisplay(name) && !contains(legacyCompactDisplay, name) && !newTools[name] {
			t.Errorf("%s: TUI 正文折叠范围扩大，历史里没有", name)
		}
	}

	for _, name := range legacyTruncFirst {
		if got := toolspec.TruncationPriority(name); got != 0 {
			t.Errorf("%s: 截断优先级 = %d, 历史为 0（最先截断）", name, got)
		}
	}
	for _, name := range legacyTruncProtected {
		if got := toolspec.TruncationPriority(name); got != -1 {
			t.Errorf("%s: 截断优先级 = %d, 历史为 -1（受保护）", name, got)
		}
	}
	if got := toolspec.TruncationPriority("some_unregistered_tool"); got != 1 {
		t.Errorf("未注册名字的截断优先级 = %d, 历史 default 分支为 1", got)
	}

	// 并发面：legacy 必须是派生结果的子集，多出来的必须逐条签字。
	// 注意 legacy nonParallelToolNames 里的 5 项靠 Serial 同样成立，
	// 而 browser/checkpoint 的"当年串行"是因为它们压根不在只读白名单里 —— 派生后仍串行。
	for _, name := range legacyReadOnly {
		if !toolspec.IsReadOnly(name) {
			t.Errorf("%s: 历史可并发，派生结果不可并发（回归）", name)
		}
	}
	for _, name := range toolspec.Names() {
		if toolspec.IsReadOnly(name) && !contains(legacyReadOnly, name) && !signedReadOnlyAdditions[name] && !newTools[name] {
			t.Errorf("%s: 新增可并发工具未在 §9.7 签字", name)
		}
	}
	for _, name := range []string{"agent_exit", "delegate_meta", "browser_open", "browser_tabs", "browser_eval"} {
		if !toolspec.IsSerial(name) {
			t.Errorf("%s: 历史 nonParallelToolNames 里有它，派生结果必须仍串行", name)
		}
	}
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// 注册表里每个画像的工具面都必须只含已注册工具。
func TestProfilesOnlyReferenceRegisteredTools(t *testing.T) {
	registered := map[string]bool{}
	for _, name := range toolspec.Names() {
		registered[name] = true
	}
	for _, p := range toolspec.BuiltinProfiles() {
		for _, name := range append(append([]string(nil), p.Promote...), p.Demote...) {
			if !registered[name] {
				t.Errorf("profile %q 调档了未注册的工具 %q", p.Agent, name)
			}
		}
	}
}

// Meta 设计的 Custom Agent 不能拿到 Director 自己都没有的能力，也不能再委派。
func TestCustomPaletteStaysInsideDirector(t *testing.T) {
	deps := toolspec.ProfileDirector.ProbeDeps()
	wanted := []string{"read_file", "run_bash", "create_file", "deepthinking", "delegate_coding", "不存在的工具"}

	selected, unknown, rejected := toolspec.MatchNames(wanted, toolspec.ProfileDirector.Palette, deps)

	got := map[string]bool{}
	for _, s := range selected {
		got[s.Name] = true
		if !s.Effect.Has(toolspec.ProfileDirector.Palette) {
			t.Errorf("%s 的能力 %v 不在 Director 的 Palette 里", s.Name, s.Effect.Names())
		}
	}
	want := []string{"agent_exit", "create_file", "deepthinking", "read_file", "run_bash"}
	if len(got) != len(want) {
		t.Errorf("selected = %v, want %v", got, want)
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("missing %s", name)
		}
	}
	if got["delegate_coding"] {
		t.Error("Custom Agent 拿到了 delegate_coding：会形成委派环")
	}
	if len(unknown) != 1 || unknown[0] != "不存在的工具" {
		t.Errorf("unknown = %v, want [不存在的工具]", unknown)
	}
	if len(rejected) != 1 || rejected[0] != "delegate_coding" {
		t.Errorf("rejected = %v, want [delegate_coding]", rejected)
	}
}

// TestPlasmaToolPoolMatchesPalette 保证"广告的工具"与"发得出去的工具"是同一份清单。
// 历史上 meta.prompt.md 手写的表里有 semantic_search/query_code_*，而 director.go 的
// getToolFunc 没有对应 case —— 模型照表选工具，注册时静默丢弃。
//
// P9a 之后广告面搬到两处，但都归同一个判据（plasma.PlasmaPalette，ADR-4）：
//   - manifest（Go 写、Python 只读）：manifestTools 用 `Effect.Has(palette)`；
//   - 生成策略种子（plasma/prompts/generate_seed.md）：只留 {{TOOL_POOL_TABLE}} 占位符，
//     工具表由 Python 从 manifest 渲染 —— Go 侧不再渲染第二份表（第二份必然漂移）。
//
// 准入侧 runCustomAgent 用同一个常量：只收窄一边等于没收窄。
func TestPlasmaToolPoolMatchesPalette(t *testing.T) {
	manifest := (&plasma.Compiler{Palette: plasma.PlasmaPalette}).Manifest()
	pool := make(map[string]bool, len(manifest.Tools))
	for _, tool := range manifest.Tools {
		pool[tool.Name] = true
	}
	if len(pool) == 0 {
		t.Fatal("manifest 里一个工具都没有，生成器无从选工具")
	}

	palette := plasma.PlasmaPalette
	for _, s := range toolspec.Specs() {
		granted := s.Effect.Has(palette)
		if granted && !pool[s.Name] {
			t.Errorf("%s 在 PlasmaPalette 内却没进 manifest 工具池（广告与准入又将不一致）", s.Name)
		}
		if !granted && pool[s.Name] {
			t.Errorf("%s 不在 PlasmaPalette 内却出现在 manifest 工具池（广告了发不出去）", s.Name)
		}
	}

	// 写/shell 必须既不在广告里、也不被准入接受 —— 这是 ADR-4 的安全底线。
	for _, name := range []string{"run_bash", "create_file"} {
		if pool[name] {
			t.Errorf("%s 出现在 manifest 里：生成式 Agent 默认只读", name)
		}
		selected, _, rejected := toolspec.MatchNames([]string{name}, palette, nil)
		// MatchNames 会自动追加 agent_exit，所以 selected 非空是正常的；
		// 要断言的是**那个写工具本身**没被选进来。
		for _, s := range selected {
			if s.Name == name {
				t.Errorf("%s 被 PlasmaPalette 放行了：生成式 Agent 默认只读", name)
			}
		}
		if len(rejected) != 1 || rejected[0] != name {
			t.Errorf("%s 应进 rejected，得到 %v", name, rejected)
		}
	}
	// load_tool 必须在：缺它则 Cold 工具永远取不回（ProfileDirector.Palette 今天的 bug）。
	if !pool["load_tool"] {
		t.Error("load_tool 不在工具池里，Cold 工具将无法取回")
	}

	// 种子文件只许留占位符：手写工具表正是当年漂移的成因。
	seedPath := filepath.Join("..", "..", "plasma", "prompts", "generate_seed.md")
	raw, err := os.ReadFile(seedPath)
	if err != nil {
		t.Fatalf("读不到生成策略种子 %s：%v（P9a 的迁移产物，不许丢）", seedPath, err)
	}
	seed := string(raw)
	if !strings.Contains(seed, "{{TOOL_POOL_TABLE}}") {
		t.Error("种子丢了 {{TOOL_POOL_TABLE}} 占位符，Python 侧将拿不到工具池")
	}
	for _, s := range toolspec.Specs() {
		if strings.Contains(seed, "| `"+s.Name+"` |") {
			t.Errorf("%s 被硬编码进种子的表格里：工具池只能由 manifest 派生", s.Name)
		}
	}
}

// TestMaterializedCarriesAcrossDirectorTurns：Surface 每轮重建，已取回的冷工具必须
// 跨轮保留，否则第二轮起又得为同一个工具再付一次 load_tool。
func TestMaterializedCarriesAcrossDirectorTurns(t *testing.T) {
	agent := newTestDirectorAgent(t, t.TempDir())

	first := agent.rebuildSurface()
	cold := ""
	for _, name := range first.Names() {
		if first.Tier(name) == toolspec.Cold && name != "load_tool" {
			cold = name
			break
		}
	}
	if cold == "" {
		t.Skip("Director 画像下没有冷工具，档位表改了的话这条测试需要跟着调整")
	}
	if _, err := first.Materialize([]string{cold}); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	agent.carryOverMaterialized()

	second := agent.rebuildSurface()
	if second.Tier(cold) != toolspec.Cold {
		t.Fatalf("档位不该被 carry-over 改掉，得到 %v", second.Tier(cold))
	}
	if _, state := second.Lookup(cold); state != tools.StateReady {
		t.Errorf("跨轮后 %s 应已可见，state=%v", cold, state)
	}
}

// 外部 MCP 工具必须走 Deps.ExternalSpecs 这条动态通道，绝不进静态注册表。
// 塞进去会同时打穿三处：golden 变成运行期状态的函数、Meta 的工具池被第三方能力
// 污染（Palette 的意义是"Custom Agent 拿不到 Director 没有的能力"）、Validate
// 的双向 1:1 会对每个外部工具报"schemas.json 里没有对应声明"。
func TestStaticRegistryNeverCarriesExternalTools(t *testing.T) {
	for _, name := range toolspec.Names() {
		if strings.HasPrefix(name, "mcp__") {
			t.Errorf("%s: 外部工具出现在静态注册表里（应走 Deps.ExternalSpecs）", name)
		}
	}
}
