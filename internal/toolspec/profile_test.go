package toolspec

import (
	"strings"
	"testing"
)

// 画像匹配是"谁能干什么"的唯一表达：Grant 白名单 → Deny 黑名单 → Audience
// 受众 → Available 运行期探针。这四步的先后次序一旦改动，表现是某个 Agent
// 悄悄多或少了一个工具 —— 而不是编译错误。
//
// 真实画像的解析结果由 internal/agents 的 golden（testdata/tool_surfaces.golden）
// 钉住；这里钉的是**匹配算法本身**，用合成 Spec，这样每一条规则都能被单独证伪。

// fixture 构造一份注册用的 Spec；只填匹配算法关心的字段。
type fixture struct {
	name     string
	effect   Effect
	audience []string
	availOK  bool // Available 的返回值（availReason 为 ""）
	freq     Frequency
	brief    string
}

func (f fixture) spec() Spec {
	s := Spec{
		Name:      f.name,
		Effect:    f.effect,
		Frequency: f.freq,
		Brief:     f.brief,
		Audience:  f.audience,
		Build:     func(*Deps) (ToolFunc, error) { return noopToolFunc, nil },
	}
	if f.availOK {
		s.Available = func(*Deps) (bool, string) { return true, "" }
	} else {
		s.Available = func(*Deps) (bool, string) { return false, "依赖缺失" }
	}
	return s
}

func names(specs []*Spec) []string {
	out := make([]string, 0, len(specs))
	for _, s := range specs {
		out = append(out, s.Name)
	}
	return out
}

func joined(specs []*Spec) string { return strings.Join(names(specs), ",") }

func TestMatch_GrantWhitelistAndDenyOrder(t *testing.T) {
	installRegistry(t,
		fixture{name: "read_file", effect: EffectFSRead, availOK: true}.spec(),
		fixture{name: "create_file", effect: EffectFSWrite, availOK: true}.spec(),
		fixture{name: "run_bash", effect: EffectShell, availOK: true}.spec(),
	)

	// Grant 之外的能力一个都不进工具面。
	readOnly := Profile{Agent: "probe", Grant: EffectFSRead}
	if got := joined(Match(readOnly, nil)); got != "read_file" {
		t.Errorf("Grant=fs_read → %q, want read_file", got)
	}

	// Deny 在 Grant 之后生效：同族里排除整类。
	writeDeny := Profile{Agent: "probe", Grant: EffectFSRead | EffectFSWrite | EffectShell, Deny: EffectShell}
	if got := joined(Match(writeDeny, nil)); got != "create_file,read_file" {
		t.Errorf("Grant=读+写+Shell Deny=shell → %q, want create_file,read_file", got)
	}

	// Deny 命中 Grant 之外的能力时不该有任何额外效果。
	noopDeny := Profile{Agent: "probe", Grant: EffectFSRead | EffectFSWrite, Deny: EffectBrowserEval}
	if got := joined(Match(noopDeny, nil)); got != "create_file,read_file" {
		t.Errorf("Deny 了本来没 Grant 的能力 → %q, want create_file,read_file", got)
	}
}

// Audience 是注册表里唯一允许出现 Agent 名字的地方，作用是"新增委派工具不会
// 静默扩权"。它必须在 Grant 之后生效，且空 Audience = 不限受众。
func TestMatch_AudienceRestrictsButEmptyMeansEveryone(t *testing.T) {
	installRegistry(t,
		fixture{name: "delegate_repo", effect: EffectDelegate, audience: []string{"director"}, availOK: true}.spec(),
		fixture{name: "delegate_browser", effect: EffectDelegate, audience: []string{"director", "coding"}, availOK: true}.spec(),
		fixture{name: "read_file", effect: EffectFSRead, availOK: true}.spec(),
	)

	grant := EffectDelegate | EffectFSRead

	dir := Match(Profile{Agent: "director", Grant: grant}, nil)
	if got := joined(dir); got != "delegate_browser,delegate_repo,read_file" {
		t.Errorf("director 的工具面 = %q, want 三个都在", got)
	}

	cod := Match(Profile{Agent: "coding", Grant: grant}, nil)
	if got := joined(cod); got != "delegate_browser,read_file" {
		t.Errorf("coding 的工具面 = %q, want 只拿到 audience 列了 coding 的 delegate_browser", got)
	}

	// 未列进 Audience 的画像一个委派工具都拿不到 —— 这就是"不会静默扩权"。
	br := Match(Profile{Agent: "browser", Grant: grant}, nil)
	if got := joined(br); got != "read_file" {
		t.Errorf("browser 的工具面 = %q, want 只有不受受众限制的工具", got)
	}
}

// Match 与 MatchAll 的不对称是本表最容易被"顺手统一掉"的地方：
// Surface 需要 MatchAll（含不可用项）才能把"因为依赖没装配而消失的工具"记录下来。
func TestMatch_VsMatchAll_AvailabilityFiltering(t *testing.T) {
	installRegistry(t,
		fixture{name: "read_file", effect: EffectFSRead, availOK: true}.spec(),
		fixture{name: "semantic_search", effect: EffectCodeSemantic, availOK: false}.spec(),
	)

	deps := &Deps{AgentName: "probe"}
	grant := EffectFSRead | EffectCodeSemantic

	got := joined(Match(Profile{Agent: "probe", Grant: grant}, deps))
	if want := "read_file"; got != want {
		t.Errorf("Match() = %q, want %q（运行期不可用的必须被过滤）", got, want)
	}
	all := joined(MatchAll(Profile{Agent: "probe", Grant: grant}, deps))
	if want := "read_file,semantic_search"; all != want {
		t.Errorf("MatchAll() = %q, want %q（不可用项必须保留，否则跳过原因无从记录）", all, want)
	}
}

// Available 拿到的必须是调用方传进来的 Deps —— 便签类工具靠 deps.HasBoard()
// 判定，浏览器靠 deps.Browser，画像探针靠 deps 的默认配置。传错等于工具面
// 与当前委派脱钩。
func TestMatch_AvailableReceivesCallersDeps(t *testing.T) {
	var seenBoard string
	installRegistry(t, Spec{
		Name:      "read_note",
		Effect:    EffectContext,
		Frequency: Hot,
		Build:     func(*Deps) (ToolFunc, error) { return noopToolFunc, nil },
		Available: func(d *Deps) (bool, string) {
			seenBoard = d.BoardID
			return d.HasBoard(), "没有便签板"
		},
	})

	grant := EffectContext
	withBoard := Match(Profile{Agent: "probe", Grant: grant}, &Deps{BoardID: "b-1"})
	if got := joined(withBoard); got != "read_note" {
		t.Errorf("有板时工具面 = %q, want read_note", got)
	}
	if seenBoard != "b-1" {
		t.Errorf("Available 看到的 BoardID = %q, want b-1", seenBoard)
	}

	withoutBoard := Match(Profile{Agent: "probe", Grant: grant}, &Deps{})
	if got := joined(withoutBoard); got != "" {
		t.Errorf("无板时工具面 = %q, want 空", got)
	}
	if seenBoard != "" {
		t.Errorf("Available 看到的 BoardID = %q, want 空（必须随 Deps 变）", seenBoard)
	}
}

// AvailableFor(nil deps) 不能 panic：调用方（tools.NewSurface 之外的路径）
// 可能压根没装配 Deps，而 Available 的写法清一色是解引用 d。
// 实现把 nil 折成"空的 &Deps{}"再交给 hook —— 所以 hook 看到的永远是可解引用的，
// 只是所有能力字段都是零值，于是判定自然落到"不可用"。
func TestAvailableFor_NilDepsAndNilHook(t *testing.T) {
	plain := baseSpec("read_file")
	if ok, reason := plain.AvailableFor(nil); !ok || reason != "" {
		t.Errorf("nil Available hook → (%v,%q), want (true,\"\")", ok, reason)
	}

	seen := "unset"
	hooked := baseSpec("read_file")
	hooked.Available = func(d *Deps) (bool, string) {
		seen = d.BoardID // 直接解引用：deps 为 nil 时这里就 panic 了
		return d.HasBoard(), "没有便签板"
	}
	if ok, reason := hooked.AvailableFor(nil); ok || reason != "没有便签板" {
		t.Errorf("AvailableFor(nil) → (%v,%q), want (false,\"没有便签板\")", ok, reason)
	}
	if seen != "" {
		t.Errorf("hook 看到的 BoardID = %q, want 空串（nil deps 被折成空 Deps 而不是原样传入）", seen)
	}
}

// Names 非空 = Custom Agent 的按名装配路径：不做能力展开，所以 Grant/Deny 完全不参与；
// 但运行期可用性仍然算 —— 否则给它一个必然失败的工具。
func TestMatch_ByNamesIgnoresGrantButHonorsAvailability(t *testing.T) {
	installRegistry(t,
		fixture{name: "read_file", effect: EffectFSRead, availOK: true}.spec(),
		fixture{name: "run_bash", effect: EffectShell, availOK: true}.spec(),
		fixture{name: "browser_click", effect: EffectBrowserAct, availOK: false}.spec(),
	)

	// Grant 是只读，但清单里点了 run_bash —— 按名路径照给。
	// （权限由 MatchNames 的 Palette 校验负责，不在这里。）
	p := Profile{Agent: "custom", Grant: EffectFSRead, Names: []string{"run_bash", "read_file", "browser_click", "no_such", " ", "read_file"}}
	got := joined(Match(p, &Deps{}))
	if want := "read_file,run_bash"; got != want {
		t.Errorf("按名装配 = %q, want %q（Grant 不参与；不可用项与未注册名与空白项被剔除；重复被去掉）", got, want)
	}

	// MatchAll 同样保留不可用项，供 Surface 记录跳过原因。
	if got := joined(MatchAll(p, &Deps{})); got != "browser_click,read_file,run_bash" {
		t.Errorf("按名 MatchAll = %q, want 含不可用项", got)
	}
}

func TestMatch_ResultIsSorted(t *testing.T) {
	installRegistry(t,
		fixture{name: "zeta", effect: EffectFSRead, availOK: true}.spec(),
		fixture{name: "alpha", effect: EffectFSRead, availOK: true}.spec(),
	)
	if got := joined(Match(Profile{Agent: "probe", Grant: EffectFSRead}, nil)); got != "alpha,zeta" {
		t.Errorf("Match() = %q, want 按名字排序（与 toolDefs 顺序一致）", got)
	}
}

// ---------- MatchNames：Meta 设计的 Custom Agent ----------

func TestMatchNames_PaletteGating(t *testing.T) {
	installRegistry(t,
		fixture{name: "read_file", effect: EffectFSRead, availOK: true}.spec(),
		fixture{name: "run_bash", effect: EffectShell, availOK: true}.spec(),
		fixture{name: "agent_exit", effect: EffectFlow, availOK: true}.spec(),
	)

	palette := EffectFSRead | EffectShell | EffectFlow
	sel, unknown, rejected := MatchNames([]string{"run_bash", "ghost_tool", "read_file"}, palette, nil)
	// palette 含 flow → 收尾工具被自动补上。
	if got := joined(sel); got != "agent_exit,read_file,run_bash" {
		t.Errorf("selected = %q, want palette 命中的两项 + 自动补上的 agent_exit", got)
	}
	if strings.Join(unknown, ",") != "ghost_tool" {
		t.Errorf("unknown = %v, want [ghost_tool]（过去是 slog.Warn 后静默丢弃）", unknown)
	}
	if len(rejected) != 0 {
		t.Errorf("rejected = %v, want 空", rejected)
	}

	// palette 不含 Shell：run_bash 进 rejected，Custom Agent 拿不到 Director 没有的能力。
	sel, _, rejected = MatchNames([]string{"run_bash", "read_file"}, EffectFSRead|EffectFlow, nil)
	if strings.Join(rejected, ",") != "run_bash" {
		t.Errorf("rejected = %v, want [run_bash]", rejected)
	}
	if got := joined(sel); got != "agent_exit,read_file" {
		t.Errorf("selected = %q, want 只含 read_file 与自动补上的 agent_exit", got)
	}
}

// agent_exit 必须自动补上（收尾工具），但 palette 排除 flow 时不能补 ——
// 否则等于给 Custom Agent 一个它没有权限的流程控制工具。
func TestMatchNames_AutoAppendsExitUnlessPaletteExcludesIt(t *testing.T) {
	installRegistry(t,
		fixture{name: "read_file", effect: EffectFSRead, availOK: true}.spec(),
		fixture{name: "agent_exit", effect: EffectFlow, availOK: true}.spec(),
	)

	sel, _, _ := MatchNames([]string{"read_file"}, EffectFSRead|EffectFlow, nil)
	if got := joined(sel); got != "agent_exit,read_file" {
		t.Errorf("palette 含 flow → %q, want agent_exit 被补上", got)
	}

	sel, _, _ = MatchNames([]string{"read_file"}, EffectFSRead, nil)
	if got := joined(sel); got != "read_file" {
		t.Errorf("palette 不含 flow → %q, want 不补 agent_exit", got)
	}

	// 清单里已经点了 agent_exit：不能重复出现两次。
	sel, _, _ = MatchNames([]string{"agent_exit", "read_file"}, EffectFSRead|EffectFlow, nil)
	if got := names(sel); len(got) != 2 || got[0] != "agent_exit" || got[1] != "read_file" {
		t.Errorf("selected = %v, want 恰好两项（agent_exit 不重复补挂）", got)
	}
}

// MatchNames 不做运行期可用性判定（那是 Surface 的活），混进来的话
// "权限不够"和"这个 Agent 没接 Shell"就分不开，排障时会误判。
func TestMatchNames_DoesNotFilterOnAvailability(t *testing.T) {
	installRegistry(t,
		fixture{name: "semantic_search", effect: EffectCodeSemantic, availOK: false}.spec(),
	)
	sel, unknown, rejected := MatchNames([]string{"semantic_search"}, EffectCodeSemantic, nil)
	if len(unknown) != 0 || len(rejected) != 0 {
		t.Errorf("unknown=%v rejected=%v, want 都为空（不可用不是权限问题）", unknown, rejected)
	}
	if got := joined(sel); got != "semantic_search" {
		t.Errorf("selected = %q, want 保留不可用项", got)
	}
}

func TestMatchNames_TrimsAndDedupes(t *testing.T) {
	installRegistry(t,
		fixture{name: "read_file", effect: EffectFSRead, availOK: true}.spec(),
		fixture{name: "list_dir", effect: EffectFSRead, availOK: true}.spec(),
	)

	sel, unknown, _ := MatchNames([]string{" read_file ", "", "  ", "read_file", "list_dir"}, EffectFSRead, nil)
	if got := joined(sel); got != "list_dir,read_file" {
		t.Errorf("selected = %q, want 去空白、去重后的两项", got)
	}
	if len(unknown) != 0 {
		t.Errorf("unknown = %v, want 空（空白项不该被当成未知工具记一笔）", unknown)
	}
}

// ---------- 频率档与 deferred 目录 ----------

// Promote/Demote 是画像级的调档：Promote 先查，所以同名同时出现在两个列表时
// Promote 赢 —— 而 ValidateProfile 会把这种自相矛盾的调档报成问题。
func TestTierResolution_PromoteBeatsDemote(t *testing.T) {
	installRegistry(t,
		fixture{name: "read_file", effect: EffectFSRead, availOK: true, freq: Cold, brief: "读文件"}.spec(),
		fixture{name: "list_dir", effect: EffectFSRead, availOK: true, freq: Hot}.spec(),
	)

	p := Profile{Agent: "probe", Grant: EffectFSRead, Promote: []string{"read_file"}, Demote: []string{"read_file", "list_dir"}}
	rf, _ := Get("read_file")
	ld, _ := Get("list_dir")
	if got := p.Tier(rf); got != Hot {
		t.Errorf("read_file tier = %v, want Hot（Promote 先于 Demote）", got)
	}
	if got := p.Tier(ld); got != Cold {
		t.Errorf("list_dir tier = %v, want Cold", got)
	}
	if got := ValidateProfile(p); !containsProblem(got, "同时/重复调档") {
		t.Errorf("ValidateProfile() = %q, want 指出同一工具被重复调档", got)
	}
}

func TestDeferredAndAdvertiseSpecs(t *testing.T) {
	installRegistry(t,
		fixture{name: "read_file", effect: EffectFSRead, availOK: true, freq: Cold, brief: "读文件"}.spec(),
		fixture{name: "list_dir", effect: EffectFSRead, availOK: true, freq: Hot}.spec(),
		fixture{name: "print_dir_tree", effect: EffectFSRead, availOK: true, freq: Cold, brief: "目录树"}.spec(),
	)

	p := Profile{Agent: "probe", Grant: EffectFSRead, Promote: []string{"print_dir_tree"}}
	deps := &Deps{}

	if got := joined(p.DeferredSpecs(deps, nil)); got != "read_file" {
		t.Errorf("DeferredSpecs = %q, want 只有未提档、未取回的 read_file", got)
	}
	if got := joined(p.AdvertiseSpecs(deps, nil)); got != "list_dir,print_dir_tree" {
		t.Errorf("AdvertiseSpecs = %q, want 每步带全量声明的两个", got)
	}

	// 取回 schema 之后 Cold 转为每步可见（materialize 是 append-only 的）。
	mat := map[string]bool{"read_file": true}
	if got := joined(p.DeferredSpecs(deps, mat)); got != "" {
		t.Errorf("materialized 后 DeferredSpecs = %q, want 空", got)
	}
	if got := joined(p.AdvertiseSpecs(deps, mat)); got != "list_dir,print_dir_tree,read_file" {
		t.Errorf("materialized 后 AdvertiseSpecs = %q, want 三个都在", got)
	}
}

// Deferred/Advertise 必须只覆盖 Match 的结果：不可用的 Cold 工具不该出现在
// 目录里骗模型去 load_tool。
func TestDeferredSpecs_SkipsUnavailable(t *testing.T) {
	installRegistry(t,
		fixture{name: "read_file", effect: EffectFSRead, availOK: true, freq: Cold, brief: "读文件"}.spec(),
		fixture{name: "semantic_search", effect: EffectCodeSemantic, availOK: false, freq: Cold, brief: "语义检索"}.spec(),
	)
	p := Profile{Agent: "probe", Grant: EffectFSRead | EffectCodeSemantic}
	if got := joined(p.DeferredSpecs(&Deps{}, nil)); got != "read_file" {
		t.Errorf("DeferredSpecs = %q, want 只含可用的冷工具", got)
	}
}

// ---------- ValidateProfile ----------

func TestValidateProfile(t *testing.T) {
	installRegistry(t,
		fixture{name: "read_file", effect: EffectFSRead, availOK: true}.spec(),
		fixture{name: "agent_exit", effect: EffectFlow, availOK: true}.spec(),
	)

	ok := Profile{Agent: "probe", Grant: EffectFSRead | EffectFlow, RequiresExit: true}
	if got := ValidateProfile(ok); len(got) != 0 {
		t.Errorf("合规画像报出 %q, want 无问题", got)
	}

	empty := Profile{Agent: "probe", Grant: EffectBrowserEval, RequiresExit: true}
	if got := ValidateProfile(empty); !containsProblem(got, "匹配到 0 个工具") {
		t.Errorf("匹配不到任何工具的画像 = %q, want 报 0 个工具", got)
	}

	// RequiresExit 但被 Deny 排除掉 flow：收尾工具不在场 → 该 Agent 永远跑不完一轮。
	noExit := Profile{Agent: "probe", Grant: EffectFSRead | EffectFlow, Deny: EffectFlow, RequiresExit: true}
	if got := ValidateProfile(noExit); !containsProblem(got, "agent_exit") {
		t.Errorf("Deny 掉收尾工具 = %q, want 指出 agent_exit 被排除", got)
	}

	// 不要求收尾工具时不该报（Chat 之外也可能有人这么配）。
	if got := ValidateProfile(Profile{Agent: "probe", Grant: EffectFSRead}); len(got) != 0 {
		t.Errorf("RequiresExit=false 的画像报出 %q, want 无问题", got)
	}

	if got := ValidateProfile(Profile{Agent: "probe", Grant: EffectFSRead, Promote: []string{"ghost"}}); !containsProblem(got, "未注册的工具") {
		t.Errorf("调档未注册工具 = %q, want 报未注册", got)
	}
	if got := ValidateProfile(Profile{Agent: "probe", Grant: EffectFSRead, Demote: []string{"ghost", "ghost"}}); !containsProblem(got, "重复调档") {
		t.Errorf("重复调档 = %q, want 报重复", got)
	}
}

func TestProfileEffectSummary(t *testing.T) {
	p := Profile{Agent: "probe", Grant: EffectFSRead | EffectShell, Deny: EffectFSWrite}
	want := "grant=[fs_read,shell] deny=[fs_write]"
	if got := p.EffectSummary(); got != want {
		t.Errorf("EffectSummary() = %q, want %q", got, want)
	}
	// 空画像也必须给出可解析的形状（golden 文件按这两个方括号做 diff）。
	if got := (Profile{}).EffectSummary(); got != "grant=[] deny=[]" {
		t.Errorf("空画像 EffectSummary() = %q, want grant=[] deny=[]", got)
	}
}

// ProbeDeps 是 golden 的输入：它把 Available 的运行期变量固定下来。
// 一旦某个字段没填，对应工具就会从 golden 里蒸发，而 diff 看起来像"画像改了"。
func TestProbeDepsIsCapabilityComplete(t *testing.T) {
	deps := Profile{Agent: "coding"}.ProbeDeps()
	if deps == nil {
		t.Fatal("ProbeDeps() = nil")
	}
	if deps.AgentName != "coding" || deps.ProjectPath == "" || deps.BoardID == "" {
		t.Errorf("身份字段 = %+v, want AgentName/ProjectPath/BoardID 都已填", deps)
	}
	if deps.Files == nil || deps.Search == nil || deps.Sys == nil || deps.Edit == nil ||
		deps.Repo == nil || deps.Flow == nil || deps.Thinker == nil || deps.Micro == nil ||
		deps.Deep == nil || deps.Design == nil || deps.Browser == nil {
		t.Error("ProbeDeps 缺少某个执行器依赖，golden 会静默少一类工具")
	}
	if deps.Delegate == nil || deps.LoadTool == nil {
		t.Error("ProbeDeps 缺少按 Agent 绑定的执行器")
	}
	if deps.CheckpointList == nil || deps.CheckpointCreate == nil || deps.CheckpointRollback == nil {
		t.Error("ProbeDeps 缺少 git 快照执行器")
	}
	if !deps.DeferSchemas {
		t.Error("ProbeDeps.DeferSchemas = false, want true（探针按默认配置建模）")
	}
	if deps.ResolveTarget == nil || !deps.ResolveTarget("anything") {
		t.Error("ProbeDeps.ResolveTarget 必须恒真，否则委派工具全部落到不可用")
	}
	if deps.IsDisabled("repo") {
		t.Error("ProbeDeps 不该把任何子 Agent 标成禁用")
	}
	if !deps.HasBoard() {
		t.Error("ProbeDeps.HasBoard() = false, want true（否则 read_note 从 golden 消失）")
	}
}

// 探针 Deps 必须能走通所有 Available：它不注入真实实现，只让 nil 检查通过。
// 若某个工具的 Available 要求探针没提供的字段，Match 会当场少一个工具。
func TestProbeDepsPassesEveryAvailabilityHook(t *testing.T) {
	probe := Profile{Agent: "probe"}.ProbeDeps()

	// 便签类与浏览器类都靠 Deps 字段判定，这里用注册表里真实存在的形状复现。
	board := baseSpec("read_note")
	board.Effect = EffectContext
	board.Available = func(d *Deps) (bool, string) { return d.HasBoard(), "无板" }

	browser := baseSpec("browser_click")
	browser.Effect = EffectBrowserAct
	browser.Mutates = true
	browser.NeedsAuth = false
	browser.Available = func(d *Deps) (bool, string) { return d.Browser != nil, "无浏览器" }

	cog := baseSpec("deepthinking")
	cog.Effect = EffectCogDeep
	cog.Available = func(d *Deps) (bool, string) { return d.Deep != nil, "无引擎" }

	del := baseSpec("delegate_repo")
	del.Effect = EffectDelegate
	del.Available = func(d *Deps) (bool, string) { return d.Delegate != nil && !d.IsDisabled("repo"), "禁用" }

	installRegistry(t, board, browser, cog, del)
	p := Profile{Agent: "probe", Grant: EffectContext | EffectBrowserAct | EffectCogDeep | EffectDelegate}
	if got := joined(Match(p, probe)); got != "browser_click,deepthinking,delegate_repo,read_note" {
		t.Errorf("探针 Deps 下工具面 = %q, want 四项都在（缺字段就是 golden 少工具）", got)
	}
}

func containsProblem(problems []string, sub string) bool {
	for _, p := range problems {
		if strings.Contains(p, sub) {
			return true
		}
	}
	return false
}
