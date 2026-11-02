package toolspec

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

type Spec struct {
	Name   string
	Brief  string
	Effect Effect

	Frequency    Frequency
	Mutates      bool
	Serial       bool
	BlocksOnUser bool

	NeedsAuth  bool
	PathParams []string

	Trunc     TruncTier
	Display   DisplayMode
	ModelSlot string

	ResultPaths string

	Audience []string

	Available func(*Deps) (bool, string)
	Describe  func(*Deps) string
	Inline    *Schema
	Build     func(*Deps) (ToolFunc, error)
}

func (s *Spec) IsReadOnly() bool { return !s.Mutates && !s.Serial }

func (s *Spec) tier(p Profile) Frequency { return p.resolveTier(s) }

var (
	registryMu sync.RWMutex
	registry   = map[string]*Spec{}
)

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
	cp := spec
	registry[spec.Name] = &cp
	return nil
}

func MustRegister(spec Spec) {
	if err := Register(spec); err != nil {
		panic(err)
	}
}

func Get(name string) (*Spec, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	s, ok := registry[name]
	return s, ok
}

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

func Names() []string {
	specs := Specs()
	out := make([]string, 0, len(specs))
	for _, s := range specs {
		out = append(out, s.Name)
	}
	return out
}

func ResetRegistry() {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = map[string]*Spec{}
}

type SkippedTool struct {
	Name   string
	Reason string
}

const maxBriefRunes = 40

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

		if s.Effect.Has(writeEffects) && !s.Mutates {
			report("%s: 能力 %v 有副作用但 Mutates=false", s.Name, s.Effect.Names())
		}
		if s.Effect.Has(authEffects) && !s.NeedsAuth {
			report("%s: 能力 %v 需要用户授权但 NeedsAuth=false", s.Name, s.Effect.Names())
		}
		if s.NeedsAuth && len(s.PathParams) > 0 && hasSchema {
			for _, p := range s.PathParams {
				if !schema.HasProperty(p) {
					report("%s: PathParams 声明了 %q，但 schemas.json 的 properties 里没有", s.Name, p)
				}
			}
		}
		if s.BlocksOnUser && (!s.Serial || s.Frequency != Hot) {
			report("%s: BlocksOnUser 要求 Serial=true 且 Frequency=Hot", s.Name)
		}
		if s.Effect.Has(flowEffects) && s.Frequency != Hot {
			report("%s: 流程控制/元工具（%v）不允许 deferred，必须 Hot", s.Name, s.Effect.Names())
		}
		if s.Trunc == TruncProtected && s.Mutates {
			report("%s: TruncProtected 与 Mutates=true 冲突", s.Name)
		}
	}

	for _, name := range SchemaNames() {
		if _, ok := Get(name); !ok {
			problems = append(problems, fmt.Sprintf("%s: schemas.json 有声明但未注册 Spec", name))
		}
	}
	sort.Strings(problems)
	return problems
}

var dynamicNamePattern = regexp.MustCompile(`^mcp__[A-Za-z0-9_-]{1,59}$`)

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

func ValidateProfile(p Profile) []string {
	specs := Match(p, p.testDeps())
	var problems []string
	if len(specs) == 0 {
		problems = append(problems, fmt.Sprintf("profile %q 匹配到 0 个工具", p.Agent))
	}
	if p.RequiresExit {
		found := false
		for _, s := range specs {
			if s.Name == ExitToolName {
				found = true
				break
			}
		}
		if !found {
			problems = append(problems, fmt.Sprintf("profile %q 要求 %s，但被 Grant/Deny 排除", p.Agent, ExitToolName))
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

const ExitToolName = "agent_exit"

const (
	ResultPathsJSON    = ""
	ResultPathsBare    = "bare"
	ResultPathsRipgrep = "rg"
)

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

func (p Profile) EffectSummary() string {
	var b strings.Builder
	b.WriteString("grant=[")
	b.WriteString(strings.Join(p.Grant.Names(), ","))
	b.WriteString("] deny=[")
	b.WriteString(strings.Join(p.Deny.Names(), ","))
	b.WriteString("]")
	return b.String()
}
