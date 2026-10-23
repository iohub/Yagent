package toolspec

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Spec 是一个工具的全部声明。名字、能力标签、频率档、授权要求、执行器工厂
// 只在这里出现一次；description 与 parameters 来自 schemas.json（见 schemas.go）。
//
// 声明应写在实现同一个文件里的 init()，这样"实现了但没注册"从根上不可能发生。
type Spec struct {
	// Name 是注册表主键，也等于模型看到的 function name。
	Name string
	// Brief 是一句话介绍（≤40 rune）。Frequency=Cold 时必填：它替代完整 schema
	// 出现在 system prompt 的 deferred 目录里。
	Brief string
	// Effect 是能力标签。必填非零。
	Effect Effect

	// Frequency 决定该工具是否每步都发完整 schema。
	Frequency Frequency
	// Mutates 声明该工具会改变共享状态（文件/进程/网络/浏览器/知识库）。
	Mutates bool
	// Serial 声明即使无副作用也必须串行（例如会改动被并发读取的会话状态）。
	Serial bool
	// BlocksOnUser 声明该工具会阻塞等待用户响应 → 豁免工具级超时，且必须串行、必须 Hot。
	BlocksOnUser bool

	// NeedsAuth 声明调用前需要 WorkspaceGuard 授权。
	NeedsAuth bool
	// PathParams 是 NeedsAuth 时参与工作区边界检查的参数名。
	PathParams []string

	// Trunc 决定压缩层截断该工具结果的先后。零值 = 常规。
	Trunc TruncTier
	// Display 决定 TUI 是否展开结果正文。
	Display DisplayMode
	// ModelSlot 是该工具可绑定独立 provider 的槽名（[tools.llm.<slot>]）。
	// 空串表示沿用所属 Agent 的引擎。
	ModelSlot string

	// ResultPaths 声明"这个工具的结果里文件路径以什么形态出现"。
	//
	// 它存在的理由和整个注册表一样：谁的结果算"读过这个文件"是**每个工具自己的事实**，
	// 不该由消费方 switch 工具名 —— 那正是 check_no_manual_lookup.sh 守的东西。
	// 蓝图的证据集（§7.5 第 2 条的出参侧）是第一个需要它的地方。
	ResultPaths string

	// Audience 限制该工具只发给哪些 Agent（取值同 Profile.Agent）。
	// 空 = 由 Effect 决定受众。这是注册表里唯一允许出现 Agent 名字的地方，
	// 目的是新增委派类工具时必须显式声明受众，不会静默扩权。
	Audience []string

	// Available 是运行期可用性；返回 false 时 reason 会成为工具描述里的降级文案。
	// nil = 恒可用。
	Available func(*Deps) (bool, string)
	// Describe 覆盖 schemas.json 里的 description（用于能力降级等动态文案）。nil = 用静态文案。
	Describe func(*Deps) string
	// Inline 是动态注册（外部 MCP 工具）自带的模型接口声明。
	// 静态工具一律留空 —— 它们的 description/parameters 只在 schemas.json 里，
	// 而 schemas.json 必须与静态注册表双向 1:1（Validate 会当场报错）。
	Inline *Schema
	// Build 产出执行器。必填。返回 error 表示依赖缺失，该工具被跳过。
	Build func(*Deps) (ToolFunc, error)
}

// IsReadOnly 报告该工具能否与其他只读工具并发。
// 规则单一：无副作用且不要求串行 ⇒ 可并发。未声明的档位一律按有副作用处理（fail-safe）。
func (s *Spec) IsReadOnly() bool { return !s.Mutates && !s.Serial }

// tier 解析该工具在当前 Deps 下的实际频率档。
func (s *Spec) tier(p Profile) Frequency { return p.resolveTier(s) }

// registry 是进程级单一注册表。init() 期写入，之后只读；
// 用锁是因为测试会并发调用 Register/Reset 做隔离场景。
var (
	registryMu sync.RWMutex
	registry   = map[string]*Spec{}
)

// Register 登记一个 Spec。重复名字返回错误（不覆盖已登记项）。
func Register(spec Spec) error {
	if spec.Name == "" {
		return fmt.Errorf("toolspec: Spec.Name is required")
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if existing, ok := registry[spec.Name]; ok {
		return fmt.Errorf("toolspec: tool %q already registered (first from effect %v)",
			spec.Name, existing.Effect.Names())
	}
	// 复制一份，防止调用方后续改动 struct 变量导致注册表漂移。
	cp := spec
	registry[spec.Name] = &cp
	return nil
}

// MustRegister 是 Register 的 panic 版，供 init() 使用：
// 声明写错（重名、漏名字）必须在开发期炸掉，而不是等到运行期少一个工具。
func MustRegister(spec Spec) {
	if err := Register(spec); err != nil {
		panic(err)
	}
}

// Get 按名查找 Spec。
func Get(name string) (*Spec, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	s, ok := registry[name]
	return s, ok
}

// Specs 返回全部 Spec，按名字排序（与发给模型的 toolDefs 顺序一致，便于 diff）。
func Specs() []*Spec {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]*Spec, 0, len(registry))
	for _, s := range registry {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Names 返回全部已注册工具名（排序后）。
func Names() []string {
	specs := Specs()
	out := make([]string, 0, len(specs))
	for _, s := range specs {
		out = append(out, s.Name)
	}
	return out
}

// ResetRegistry 清空注册表，仅供测试构造隔离场景使用。
func ResetRegistry() {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = map[string]*Spec{}
}

// SkippedTool 记录"画像想要但没能注册"的工具与原因。
// 装配自检会把它打到日志：过去这类信息散在 6 处 slog.Error/slog.Warn 里，
// 没人汇总，等于静默失效。
type SkippedTool struct {
	Name   string
	Reason string
}

// maxBriefRunes 是一句话介绍的长度上限。目录里每个冷工具一行，长度直接乘以
// 冷工具数量进入每步开销，所以硬性约束比口头约定有效。
const maxBriefRunes = 40

// Validate 自检整个注册表，返回全部问题（不 panic）。
// 装配期由 app.Init 调用：生产环境把问题逐条打进日志（构建期 bug 已由测试拦住，
// 运行期退出只会让"配置坏了"和"代码坏了"混成同一个症状）；测试里直接 t.Fatal。
func Validate() []string {
	var problems []string
	report := func(format string, args ...interface{}) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	for _, s := range Specs() {
		schema, hasSchema := GetSchema(s.Name)
		if !hasSchema {
			report("%s: 已注册但 schemas.json 里没有对应声明", s.Name)
		}

		if s.Effect == 0 {
			report("%s: Effect 未声明（零值）", s.Name)
		}
		if s.Build == nil {
			report("%s: Build 未提供，执行器无从构造", s.Name)
		}
		if s.Frequency == Cold && len([]rune(s.Brief)) == 0 {
			report("%s: Cold 档必须提供 Brief，否则 deferred 目录里无法呈现", s.Name)
		}
		if s.Brief != "" && len([]rune(s.Brief)) > maxBriefRunes {
			report("%s: Brief 长度 %d 超过上限 %d", s.Name, len([]rune(s.Brief)), maxBriefRunes)
		}

		// 写副作用必须声明 Mutates，且必须过授权 —— 否则等于给 Agent 一个不受守卫的写入口。
		if s.Effect.Has(writeEffects) && !s.Mutates {
			report("%s: 能力 %v 有副作用但 Mutates=false", s.Name, s.Effect.Names())
		}
		if s.Effect.Has(authEffects) && !s.NeedsAuth {
			report("%s: 能力 %v 需要用户授权但 NeedsAuth=false", s.Name, s.Effect.Names())
		}
		// 授权工具若声称检查路径，就必须真的在 schema 里有这些参数。
		if s.NeedsAuth && len(s.PathParams) > 0 && hasSchema {
			for _, p := range s.PathParams {
				if !schema.HasProperty(p) {
					report("%s: PathParams 声明了 %q，但 schemas.json 的 properties 里没有", s.Name, p)
				}
			}
		}
		// 阻塞等待用户的工具不能被超时砍断，也不能并发（一次只能问一个问题）。
		if s.BlocksOnUser && (!s.Serial || s.Frequency != Hot) {
			report("%s: BlocksOnUser 要求 Serial=true 且 Frequency=Hot", s.Name)
		}
		// 流程控制与元工具被降级的话，模型会失去收尾/取 schema 的能力。
		if s.Effect.Has(flowEffects) && s.Frequency != Hot {
			report("%s: 流程控制/元工具（%v）不允许 deferred，必须 Hot", s.Name, s.Effect.Names())
		}
		// 受保护的结果不该是有副作用操作的回执（它会把上下文撑满且不可重取）。
		if s.Trunc == TruncProtected && s.Mutates {
			report("%s: TruncProtected 与 Mutates=true 冲突", s.Name)
		}
		// 原先这里还有一条 "Serial 与 IsReadOnly 冲突" 的一致性检查，已删除：
		// IsReadOnly() 的定义就是 !Mutates && !Serial，该条件在数学上不可成立，
		// 而 "无副作用但必须串行"（Serial=true, Mutates=false）恰是 Serial 字段
		// 文档承诺要支持的声明。不可触发的校验只会给出假安全感。
		// 派生关系由 internal/toolspec/queries_test.go 的
		// TestDerivedMetadataIsInternallyConsistent 直接对账。
	}

	// schemas.json 里若有名字没注册 Spec，就是"声明了但没人实现"的静默失效。
	for _, name := range SchemaNames() {
		if _, ok := Get(name); !ok {
			problems = append(problems, fmt.Sprintf("%s: schemas.json 有声明但未注册 Spec", name))
		}
	}
	sort.Strings(problems)
	return problems
}

// dynamicNamePattern 是外部工具名的合法形状：mcp__<server>__<tool>。
// 总长 ≤ 64 是 provider 的 function name 上限；字符集是它允许的 ASCII 集。
var dynamicNamePattern = regexp.MustCompile(`^mcp__[A-Za-z0-9_-]{1,59}$`)

// ValidateDynamic 自检一批运行期动态规格（外部 MCP 工具）。
// 与 Validate() 分开：动态规格不在 schemas.json 里，"声明 ↔ 实现双向 1:1"
// 那条规则对它们不适用，而"必须有 Inline schema"是它们自己的硬要求。
func ValidateDynamic(specs []*Spec) []string {
	var problems []string
	report := func(format string, args ...interface{}) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	for _, s := range specs {
		if s == nil {
			report("动态规格为 nil")
			continue
		}
		label := s.Name
		if label == "" {
			label = "<unnamed>"
		}
		if !dynamicNamePattern.MatchString(s.Name) {
			report("%s: 名字不合法（要求 mcp__ 前缀、[A-Za-z0-9_-]、总长 ≤ 64）", label)
		}
		if _, exists := Get(s.Name); s.Name != "" && exists {
			report("%s: 动态规格不得与静态工具同名", s.Name)
		}
		if s.Effect == 0 {
			report("%s: Effect 未声明（零值）", label)
		}
		if s.Build == nil {
			report("%s: Build 未提供，执行器无从构造", label)
		}
		if s.Inline == nil {
			report("%s: 动态规格必须自带 Inline schema（外部工具没有 schemas.json 兜底）", label)
		} else {
			if strings.TrimSpace(s.Inline.Description) == "" {
				report("%s: Inline.Description 为空，模型会拿到一个没有说明的工具", label)
			}
			if s.Inline.Parameters == nil {
				report("%s: Inline.Parameters 为空，等于声明一个无参工具", label)
			}
		}
		if s.Frequency == Cold && len([]rune(s.Brief)) == 0 {
			report("%s: Cold 档必须提供 Brief，否则 deferred 目录里无法呈现", label)
		}
		if s.Brief != "" && len([]rune(s.Brief)) > maxBriefRunes {
			report("%s: Brief 长度 %d 超过上限 %d", label, len([]rune(s.Brief)), maxBriefRunes)
		}
		if s.Effect.Has(writeEffects) && !s.Mutates {
			report("%s: 能力 %v 有副作用但 Mutates=false", label, s.Effect.Names())
		}
		if s.Effect.Has(authEffects) && !s.NeedsAuth {
			report("%s: 能力 %v 需要用户授权但 NeedsAuth=false", label, s.Effect.Names())
		}
		if s.Trunc == TruncProtected && s.Mutates {
			report("%s: TruncProtected 与 Mutates=true 冲突", label)
		}
	}
	sort.Strings(problems)
	return problems
}

// ValidateProfile 检查单个画像：必须至少匹配到一个工具，且收尾工具在场。
func ValidateProfile(p Profile) []string {
	specs := Match(p, p.testDeps())
	var problems []string
	if len(specs) == 0 {
		problems = append(problems, fmt.Sprintf("profile %q 匹配到 0 个工具", p.Agent))
	}
	if p.RequiresExit {
		found := false
		for _, s := range specs {
			if s.Name == exitToolName {
				found = true
				break
			}
		}
		if !found {
			problems = append(problems, fmt.Sprintf("profile %q 要求 %s，但被 Grant/Deny 排除", p.Agent, exitToolName))
		}
	}
	for _, name := range append(append([]string(nil), p.Promote...), p.Demote...) {
		if _, ok := Get(name); !ok {
			problems = append(problems, fmt.Sprintf("profile %q 调档了未注册的工具 %q", p.Agent, name))
		}
	}
	if dup := duplicated(append(append([]string(nil), p.Promote...), p.Demote...)); dup != "" {
		problems = append(problems, fmt.Sprintf("profile %q 同时/重复调档了 %q", p.Agent, dup))
	}
	return problems
}

const exitToolName = "agent_exit"

// ResultPaths 的三种取值（Spec.ResultPaths 的词典）。
const (
	// ResultPathsJSON：结果是结构化对象，路径在 file_path/path/filename 这类键下。
	// 也是**默认**：没声明的工具按 JSON 尽力找，解析不出来就什么都不收 ——
	// 判据宁缺毋滥（少一块证据只是少一片提交，多一块假证据会把结论挂到错的模块上）。
	ResultPathsJSON = ""
	// ResultPathsBare：结果是不带目录前缀的裸文件名（list_dir / print_dir_tree）。
	// 必须显式排除：把它们当路径映射会落到"当前工作目录那一片"，是假证据。
	ResultPathsBare = "bare"
	// ResultPathsRipgrep：结果是 `path:line:` 命中行。
	ResultPathsRipgrep = "rg"
)

// ResultPathsOf 返回某个工具的结果形态；未注册的名字按 JSON 处理（尽力找、找不到就算没有）。
func ResultPathsOf(name string) string {
	s, ok := Get(name)
	if !ok {
		return ResultPathsJSON
	}
	return s.ResultPaths
}

func duplicated(xs []string) string {
	seen := map[string]bool{}
	for _, x := range xs {
		if seen[x] {
			return x
		}
		seen[x] = true
	}
	return ""
}

// EffectSummary 给日志/golden 用，把画像的 Grant/Deny 渲染成可读串。
func (p Profile) EffectSummary() string {
	var b strings.Builder
	b.WriteString("grant=[")
	b.WriteString(strings.Join(p.Grant.Names(), ","))
	b.WriteString("] deny=[")
	b.WriteString(strings.Join(p.Deny.Names(), ","))
	b.WriteString("]")
	return b.String()
}
