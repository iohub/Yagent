package toolspec

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// 本包是工具元数据的单一事实来源。真实 Spec 在 internal/tools 的 init() 里注册，
// 而 toolspec 是 leaf 包、不 import tools，所以本包的测试进程里注册表一开始是空的
// —— 这正是 Register/ResetRegistry 存在的原因：隔离场景只能靠合成夹具构造。
//
// internal/agents/registry_gate_test.go 已经用**真实**注册表跑过一遍 Validate()
// （"全都干净"那一半）。这里补的是另一半：每条规则都必须被证明会响。
// 只测"生产注册表没有 problems"的话，谁删掉一条规则都不会有人发现 ——
// 因为今天恰好没有任何工具违反它。

func noopToolFunc(context.Context, map[string]interface{}) (interface{}, error) { return nil, nil }

// installRegistry 换成一份合成注册表，并在用例结束后恢复原状。
// 恢复必须做严：同包后续用例（含 effect_test.go 遍历 Names() 的那段）看到的
// 必须是装配期注册表，而不是上一个用例的残局。
func installRegistry(t *testing.T, specs ...Spec) {
	t.Helper()
	prev := Specs()
	ResetRegistry()
	for _, s := range specs {
		if err := Register(s); err != nil {
			t.Fatalf("Register(%q): %v", s.Name, err)
		}
	}
	t.Cleanup(func() {
		ResetRegistry()
		for _, sp := range prev {
			if err := Register(*sp); err != nil {
				t.Errorf("恢复注册表 %q 失败: %v", sp.Name, err)
			}
		}
	})
}

// baseSpec 是一份"什么都不违反"的声明，用例只改动自己要测的那一个字段。
// 名字必须落在 schemas.json 里，否则会先撞上"有 Spec 无声明"那条规则。
func baseSpec(name string) Spec {
	return Spec{
		Name:      name,
		Effect:    EffectFSRead,
		Frequency: Hot,
		Build:     func(*Deps) (ToolFunc, error) { return noopToolFunc, nil },
	}
}

// problemsFor 取出某个工具的问题（Validate 的报告以 "name:" 开头）。
func problemsFor(problems []string, name string) []string {
	var out []string
	for _, p := range problems {
		if strings.HasPrefix(p, name+":") {
			out = append(out, p)
		}
	}
	return out
}

func TestRegister_NameRequired(t *testing.T) {
	installRegistry(t)
	err := Register(Spec{Effect: EffectFSRead, Build: func(*Deps) (ToolFunc, error) { return noopToolFunc, nil }})
	if err == nil {
		t.Fatal("Register with empty Name = nil error, want an error")
	}
	if !strings.Contains(err.Error(), "Name is required") {
		t.Errorf("err = %v, want it to name the missing field", err)
	}
	if len(Names()) != 0 {
		t.Errorf("registry has %d entries after a rejected registration, want 0", len(Names()))
	}
}

// 重名必须拒绝并保留先注册的那一份。覆盖语义会让"两个文件各声明同名工具"变成
// 后注册者悄悄赢，而 init 顺序在 Go 里不是显式契约。
func TestRegister_DuplicateIsRejectedAndKeepsFirst(t *testing.T) {
	installRegistry(t)

	if err := Register(baseSpec("read_file")); err != nil {
		t.Fatalf("first Register: %v", err)
	}

	second := baseSpec("read_file")
	second.Effect = EffectShell
	err := Register(second)
	if err == nil {
		t.Fatal("Register(duplicate) = nil error, want an error")
	}
	if !strings.Contains(err.Error(), "already registered") {
		t.Errorf("err = %v, want an 'already registered' message", err)
	}

	got, ok := Get("read_file")
	if !ok {
		t.Fatal("Get(read_file) missed after registration")
	}
	if got.Effect != EffectFSRead {
		t.Errorf("Effect = %v after a rejected duplicate, want the first registration (fs_read)", got.Effect)
	}
}

// Register 存的是副本，不是指针：调用方（各 specs_*.go 的 init）后续复用同一个
// 局部变量时不能把已注册的声明改掉。
func TestRegister_StoresACopy(t *testing.T) {
	installRegistry(t)

	spec := baseSpec("read_file")
	spec.Brief = "原文"
	if err := Register(spec); err != nil {
		t.Fatalf("Register: %v", err)
	}
	spec.Brief = "被改写了"
	spec.Effect = EffectShell

	got, ok := Get("read_file")
	if !ok {
		t.Fatal("Get(read_file) missed")
	}
	if got.Brief != "原文" {
		t.Errorf("Brief = %q, want 原文 —— 注册表存的是指针而不是副本", got.Brief)
	}
	if got.Effect != EffectFSRead {
		t.Errorf("Effect = %v, want fs_read —— 同上", got.Effect)
	}
}

func TestMustRegister_PanicsOnDuplicate(t *testing.T) {
	installRegistry(t, baseSpec("read_file"))

	defer func() {
		r := recover()
		if r == nil {
			t.Error("MustRegister(duplicate) did not panic, want a panic at init time")
			return
		}
		if !strings.Contains(fmt.Sprint(r), "already registered") {
			t.Errorf("panic value = %v, want the duplicate-name error", r)
		}
	}()
	MustRegister(baseSpec("read_file"))
}

func TestSpecsAndNamesAreSortedByName(t *testing.T) {
	installRegistry(t,
		baseSpec("read_file"),
		baseSpec("agent_exit"),
		baseSpec("create_file"),
	)

	want := []string{"agent_exit", "create_file", "read_file"}
	if got := strings.Join(Names(), ","); got != strings.Join(want, ",") {
		t.Errorf("Names() = %s, want %s（排序与发给模型的 toolDefs 顺序一致）", got, strings.Join(want, ","))
	}
	specs := Specs()
	if len(specs) != len(want) {
		t.Fatalf("Specs() len = %d, want %d", len(specs), len(want))
	}
	for i, s := range specs {
		if s.Name != want[i] {
			t.Errorf("Specs()[%d] = %q, want %q", i, s.Name, want[i])
		}
	}
}

func TestGet_MissReturnsFalse(t *testing.T) {
	installRegistry(t, baseSpec("read_file"))
	if _, ok := Get("no_such_tool"); ok {
		t.Error("Get(no_such_tool) = ok, want a miss")
	}
}

// ---------- Validate：每条规则都要被证明会响 ----------

func TestValidate_ReportsEachRule(t *testing.T) {
	cases := []struct {
		name    string
		dictum  string
		mutate  func(*Spec)
		wantSub string
	}{
		{
			name:    "no schema",
			dictum:  "有 Spec 无声明",
			mutate:  func(s *Spec) { s.Name = "totally_unregistered_tool" },
			wantSub: "已注册但 schemas.json 里没有对应声明",
		},
		{
			name:    "zero effect",
			dictum:  "能力标签必须显式声明",
			mutate:  func(s *Spec) { s.Effect = 0 },
			wantSub: "Effect 未声明（零值）",
		},
		{
			name:    "no build",
			dictum:  "执行器工厂必填",
			mutate:  func(s *Spec) { s.Build = nil },
			wantSub: "Build 未提供",
		},
		{
			name:    "cold without brief",
			dictum:  "Cold 档必须给一句话介绍",
			mutate:  func(s *Spec) { s.Frequency = Cold; s.Brief = "" },
			wantSub: "Cold 档必须提供 Brief",
		},
		{
			// 41 个汉字：按字节算是 123，按 rune 算是 41。用非 ASCII 才能钉住
			// 上限真的是 rune 预算（它是每步都付的 token 成本）。
			name:    "overlong brief",
			dictum:  "Brief 有硬上限",
			mutate:  func(s *Spec) { s.Frequency = Cold; s.Brief = strings.Repeat("汉", maxBriefRunes+1) },
			wantSub: "超过上限",
		},
		{
			name:   "write effect without Mutates",
			dictum: "有副作用必须声明 Mutates",
			mutate: func(s *Spec) {
				s.Effect = EffectFSWrite
				s.Mutates = false
				s.NeedsAuth = true
				s.PathParams = []string{"target_file"}
			},
			wantSub: "有副作用但 Mutates=false",
		},
		{
			name:    "auth effect without NeedsAuth",
			dictum:  "危险能力必须过授权",
			mutate:  func(s *Spec) { s.Effect = EffectShell; s.Mutates = true; s.NeedsAuth = false },
			wantSub: "需要用户授权但 NeedsAuth=false",
		},
		{
			name:   "path param absent from schema",
			dictum: "声称检查的路径必须真的在参数里",
			mutate: func(s *Spec) {
				s.Effect = EffectFSWrite
				s.Mutates = true
				s.NeedsAuth = true
				s.PathParams = []string{"not_a_real_param"}
			},
			wantSub: `PathParams 声明了 "not_a_real_param"`,
		},
		{
			name:   "blocks on user not serial/hot",
			dictum: "等用户的工具不能并发、不能被降级",
			mutate: func(s *Spec) {
				s.Effect = EffectFlow
				s.BlocksOnUser = true
				s.Serial = false
				s.Frequency = Cold
				s.Brief = "问用户"
			},
			wantSub: "BlocksOnUser 要求 Serial=true 且 Frequency=Hot",
		},
		{
			name:    "flow tool deferred",
			dictum:  "流程控制必须 Hot，否则模型无法收尾",
			mutate:  func(s *Spec) { s.Effect = EffectFlow; s.Frequency = Cold; s.Brief = "收尾" },
			wantSub: "流程控制/元工具",
		},
		{
			name:    "tool load deferred",
			dictum:  "load_tool 也属流程能力：降级了模型取不回 schema",
			mutate:  func(s *Spec) { s.Name = "load_tool"; s.Effect = EffectToolLoad; s.Frequency = Cold; s.Brief = "取回" },
			wantSub: "流程控制/元工具",
		},
		{
			name:   "protected result on a writing tool",
			dictum: "受保护的截断档位不能给有副作用的工具",
			mutate: func(s *Spec) {
				s.Effect = EffectFSWrite
				s.Mutates = true
				s.NeedsAuth = true
				s.PathParams = []string{"target_file"}
				s.Trunc = TruncProtected
			},
			wantSub: "TruncProtected 与 Mutates=true 冲突",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			spec := baseSpec("read_file")
			tc.mutate(&spec)
			installRegistry(t, spec)

			problems := problemsFor(Validate(), spec.Name)
			if len(problems) == 0 {
				t.Fatalf("规则 %q 没有报出任何问题 —— 该校验已经失效", tc.dictum)
			}
			if !strings.Contains(strings.Join(problems, "\n"), tc.wantSub) {
				t.Errorf("problems = %q, want one containing %q（规则 %q）", problems, tc.wantSub, tc.dictum)
			}
		})
	}
}

// 一份合规声明必须报告零问题。上面那批用例证明"规则会响"，这一条证明
// "规则不会一律响" —— 少了它，把所有 Spec 都判为非法的实现同样能通过整组用例。
func TestValidate_CleanSpecReportsNothingForItself(t *testing.T) {
	write := baseSpec("create_file")
	write.Effect = EffectFSWrite
	write.Mutates = true
	write.NeedsAuth = true
	write.PathParams = []string{"target_file", "content"}

	cold := baseSpec("read_file")
	cold.Frequency = Cold
	cold.Brief = "按行区间读文件"

	flow := baseSpec("agent_exit")
	flow.Effect = EffectFlow
	flow.Serial = true

	// "无副作用但必须串行"是 Serial 字段文档承诺要支持的声明（例如会改动被并发
	// 读取的会话状态）。它曾经是 Validate 里一条不可触发的"冲突"校验 ——
	// 这里正面断言它合规，防止那条死规则被重新加回来。
	serialReadOnly := baseSpec("list_dir")
	serialReadOnly.Serial = true
	serialReadOnly.Mutates = false

	installRegistry(t, write, cold, flow, serialReadOnly)

	for _, name := range []string{"create_file", "read_file", "agent_exit", "list_dir"} {
		if got := problemsFor(Validate(), name); len(got) != 0 {
			t.Errorf("%s: problems = %q, want none", name, got)
		}
	}
	if got := IsReadOnly("list_dir"); got {
		t.Error("IsReadOnly(list_dir) = true for a Serial spec, want false")
	}
	if got := IsSerial("list_dir"); !got {
		t.Error("IsSerial(list_dir) = false, want true")
	}
}

// 双向对账的另一半：schemas.json 里有声明、却没人注册 Spec。
// 真实注册表由 internal/agents 的 gate 测试全量对账；这里钉的是
// "少注册一个就会报"这个机制本身。
func TestValidate_ReportsSchemaWithoutSpec(t *testing.T) {
	installRegistry(t)

	problems := Validate()
	if len(problems) != len(SchemaNames()) {
		t.Fatalf("空注册表报出 %d 条问题, want 每条 schemas.json 声明一条 (%d)",
			len(problems), len(SchemaNames()))
	}
	hit := ""
	for _, p := range problems {
		if strings.HasPrefix(p, "read_file:") && strings.Contains(p, "schemas.json 有声明但未注册 Spec") {
			hit = p
			break
		}
	}
	if hit == "" {
		t.Error("read_file 的 schemas.json 声明没有对应的\"未注册 Spec\"报告")
	}
}

// 报告必须整体排序：Validate 的输出会进日志与测试失败信息，顺序漂移会让每次
// diff 都噪声一片。
func TestValidate_ProblemsAreSorted(t *testing.T) {
	buildless := baseSpec("agent_exit")
	buildless.Effect = EffectFlow
	buildless.Build = nil

	zeroEffect := baseSpec("create_file")
	zeroEffect.Effect = 0
	zeroEffect.Mutates = true
	zeroEffect.NeedsAuth = true
	zeroEffect.PathParams = []string{"target_file"}

	installRegistry(t, buildless, zeroEffect)

	problems := Validate()
	// 隔离注册表里其余 48 条声明都会报"未注册 Spec"，所以按工具过滤后再断言。
	if got := problemsFor(problems, "agent_exit"); len(got) != 1 || !strings.Contains(got[0], "Build 未提供") {
		t.Errorf("agent_exit problems = %q, want the single Build problem", got)
	}
	if got := problemsFor(problems, "create_file"); len(got) != 1 || !strings.Contains(got[0], "Effect 未声明") {
		t.Errorf("create_file problems = %q, want the single Effect problem", got)
	}
	for i := 0; i < len(problems)-1; i++ {
		if problems[i] > problems[i+1] {
			t.Fatalf("Validate() 输出未排序: %q 排在 %q 之前", problems[i], problems[i+1])
		}
	}
	// 同一工具的多个问题必须聚在同一前缀下、按整条消息排序。
	both := baseSpec("browser_eval")
	both.Effect = EffectBrowserEval
	both.Mutates = false
	both.NeedsAuth = true
	both.Build = nil
	installRegistry(t, both)

	got := problemsFor(Validate(), "browser_eval")
	if len(got) != 2 {
		t.Fatalf("browser_eval problems = %q, want 2 (Build 缺失 + 副作用未声明 Mutates)", got)
	}
	if !strings.Contains(got[0], "Build 未提供") || !strings.Contains(got[1], "有副作用但 Mutates=false") {
		t.Errorf("browser_eval problems = %q, want them grouped under one tool and sorted", got)
	}
}

// ---------- schemas.json 访问器 ----------

// 50 条是与 agents 侧 gate 测试共享的基线：增删工具必然改动这里，
// 改动必须是显式的（数字写死，不做 len() 自比较 —— 那种断言永远不会失败）。
func TestSchemasLoadAndDescribeEveryEntry(t *testing.T) {
	if err := SchemasError(); err != nil {
		t.Fatalf("SchemasError() = %v, want nil —— 嵌入的声明文件解析失败", err)
	}
	names := SchemaNames()
	if len(names) != 50 {
		t.Errorf("SchemaNames() = %d 条, want 50 —— 增删工具请同步这条基线与 agents 侧 gate 测试", len(names))
	}
	for _, n := range names {
		s, ok := GetSchema(n)
		if !ok {
			t.Errorf("GetSchema(%q) missed although listed by SchemaNames()", n)
			continue
		}
		if s.Description == "" {
			t.Errorf("%s: description 为空，模型会拿到一个没有说明的工具", n)
		}
		if s.Parameters == nil {
			t.Errorf("%s: parameters 缺失，Adapter 会把无参数工具发出去", n)
		}
	}
}

func TestSchemaHasPropertyAndRequired(t *testing.T) {
	s, ok := GetSchema("create_file")
	if !ok {
		t.Fatal("create_file 不在 schemas.json 里")
	}
	if !s.HasProperty("target_file") {
		t.Error("HasProperty(target_file) = false, want true")
	}
	if s.HasProperty("nope") {
		t.Error("HasProperty(nope) = true, want false")
	}
	if got := strings.Join(s.Required(), ","); got != "target_file,content" {
		t.Errorf("Required() = %s, want target_file,content", got)
	}

	// parameters 里没有 required / properties 时返回空而不是 panic。
	bare := Schema{Parameters: map[string]interface{}{"type": "object"}}
	if len(bare.Required()) != 0 {
		t.Errorf("Required() = %v for a schema without required, want empty", bare.Required())
	}
	if bare.HasProperty("anything") {
		t.Error("HasProperty on a properties-less schema = true, want false")
	}
	// required 里混进非字符串时必须被跳过，而不是把 interface{} 当名字用。
	mixed := Schema{Parameters: map[string]interface{}{"required": []interface{}{"a", 1, "b"}}}
	if got := strings.Join(mixed.Required(), ","); got != "a,b" {
		t.Errorf("Required() = %q, want a,b（非字符串项被跳过）", got)
	}
}

// Description 的三级回落顺序是模型可见文案的唯一来源：
// Spec.Describe（动态降级文案）> schemas.json 静态文案 > 工具名。
func TestSpecDescriptionFallbackChain(t *testing.T) {
	s, ok := GetSchema("create_file")
	if !ok {
		t.Fatal("create_file 不在 schemas.json 里")
	}
	static := baseSpec("create_file")
	if got := static.Description(nil); got != s.Description {
		t.Errorf("Description() = %q, want the schemas.json text %q", got, s.Description)
	}

	dynamic := baseSpec("create_file")
	dynamic.Describe = func(*Deps) string { return "降级：不可用，原因是 X" }
	if got := dynamic.Description(nil); got != "降级：不可用，原因是 X" {
		t.Errorf("Description() = %q, want the Describe() override", got)
	}

	// Describe 返回空串必须回落到静态文案，否则工具描述变成空白。
	empty := baseSpec("create_file")
	empty.Describe = func(*Deps) string { return "" }
	if got := empty.Description(nil); got != s.Description {
		t.Errorf("Description() with an empty override = %q, want the static text", got)
	}

	// 有 Spec 无声明（Validate 会报，但装配期不能 panic）→ 回落到名字。
	orphan := baseSpec("unregistered_for_description")
	if got := orphan.Description(nil); got != "unregistered_for_description" {
		t.Errorf("Description() without a schema = %q, want the tool name", got)
	}
}

func TestSpecParametersNilWithoutSchema(t *testing.T) {
	declared := baseSpec("read_file")
	if got := declared.Parameters(); got == nil {
		t.Error("Parameters() = nil for a declared tool, want the schema map")
	}
	undeclared := baseSpec("no_such_tool")
	if got := undeclared.Parameters(); got != nil {
		t.Errorf("Parameters() = %v for an undeclared tool, want nil", got)
	}
}

// 声明文件自身的结构错误必须是可区分的 error：装配自检靠它把"声明坏了"
// 升级成启动失败，而不是退化成"某个工具凭空消失"。
func TestSchemaStructuralErrorsAreDistinguishable(t *testing.T) {
	if !strings.Contains(errBlankName.Error(), "name 为空") {
		t.Errorf("errBlankName = %q, want a blank-name diagnostic", errBlankName.Error())
	}
	dup := errDuplicateName("read_file")
	if !strings.Contains(dup.Error(), "重复") || !strings.Contains(dup.Error(), "read_file") {
		t.Errorf("errDuplicateName() = %q, want it to name the duplicated tool", dup.Error())
	}
	var je jsonError
	if !errors.As(errBlankName, &je) {
		t.Error("errBlankName 不是 jsonError：调用方无法区分声明文件坏了与解析出空 map")
	}
}
