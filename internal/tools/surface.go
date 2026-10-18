package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"yagent/internal/llm"
	"yagent/internal/toolspec"
)

// Surface 是一个 Agent 在一次运行中的工具面：
//   - 按画像解析出该当有的工具并构造适配器（取代 5 个 lookupToolFunc switch）；
//   - 承载 deferred schema 状态：Cold 工具只在目录里出现 name+Brief，
//     模型调 load_tool（或被我们兜底 teach 一次）后才带完整参数声明；
//   - materialize 是 append-only 的，绝不回收，避免工具列表来回抖动。
//
// 每步都要重新取 Defs()：这是 deferred 生效的前提，也顺带修掉了
// "本回合注册的 Custom Agent 要到下一回合才可见"的老限制。
type Surface struct {
	mu sync.Mutex

	profile  toolspec.Profile
	deps     *toolspec.Deps
	opts     SurfaceOptions
	adapters map[string]*Adapter
	tiers    map[string]toolspec.Frequency
	// advertised 是缓存的完整声明列表；materialize/Add 之后失效。
	advertised []llm.ToolDef
	catalog    string
	// materialized 记录已取回完整 schema 的 Cold 工具。
	materialized map[string]bool
	skipped      []toolspec.SkippedTool
}

// SurfaceOptions 控制 deferred 行为。Deferred=false 时所有工具一律带完整声明，
// 相当于把本次重构的行为退回历史，是留給排障的开关。
type SurfaceOptions struct {
	Deferred     bool
	BudgetTokens int
}

// NewSurface 按画像与依赖构造工具面。deps 里的能力缺失不会报错：
// 对应工具会被跳过并记进 Skipped()，由装配自检汇总打印。
func NewSurface(profile toolspec.Profile, deps *toolspec.Deps, opts SurfaceOptions) *Surface {
	if deps == nil {
		deps = &toolspec.Deps{AgentName: profile.Agent}
	}
	s := &Surface{
		profile:      profile,
		deps:         deps,
		opts:         opts,
		adapters:     make(map[string]*Adapter),
		tiers:        make(map[string]toolspec.Frequency),
		materialized: make(map[string]bool),
	}
	// deferred 的开关归 Surface 所有：调用方（或测试探针）预填的这两个字段不能被
	// 当成"已开启"，否则 opts.Deferred=false 也关不干净。
	if opts.Deferred {
		// load_tool 的执行器就是 Surface 自己；必须在 Match 之前挂上，
		// 否则它的 Available 判定会把自己排除掉。
		deps.LoadTool = s.executeLoadTool
		deps.DeferSchemas = true
	} else {
		deps.LoadTool = nil
		deps.DeferSchemas = false
	}
	s.rebuild()
	return s
}

// NewSurfaceFromAdapters 组装一个只装给定适配器的工具面：不查注册表、不分 deferred
// （opts 零值 ⇒ hidden() 恒 false ⇒ Lookup 一律 StateReady）。
//
// 它存在的理由不是省事，而是**不让测试自带第二个载体**：执行循环问"这个工具有没有、
// 只读吗、声明是什么"只能经 Surface 回答之后，测试也必须用 Surface 表达"这个 Agent
// 有哪些工具"。C3 之前测试往 ExecutorConfig.Adapters 里塞、生产只填 Surface，于是
// 并行谓词扫着一条生产永远为空的列表 —— 测试全绿而并行一次都没发生（设计稿 R1）。
func NewSurfaceFromAdapters(agentDisplay string, adapters ...*Adapter) *Surface {
	s := &Surface{
		profile:      toolspec.Profile{Agent: agentDisplay},
		deps:         &toolspec.Deps{AgentName: agentDisplay},
		adapters:     make(map[string]*Adapter, len(adapters)),
		tiers:        make(map[string]toolspec.Frequency, len(adapters)),
		materialized: make(map[string]bool),
	}
	for _, ad := range adapters {
		if ad == nil || ad.Name() == "" {
			continue
		}
		s.adapters[ad.Name()] = ad
		s.tiers[ad.Name()] = toolspec.Hot
	}
	s.invalidate()
	return s
}

// rebuild 解析画像 → 评估可用性 → 构造适配器 → 计算档位与目录。
// 落选的工具一律带原因记进 Skipped()，不做静默丢弃。
func (s *Surface) rebuild() {
	specs := toolspec.MatchAll(s.profile, s.deps)
	ordered := make([]string, 0, len(specs))
	for _, spec := range specs {
		if ok, reason := spec.AvailableFor(s.deps); !ok {
			s.skipped = append(s.skipped, toolspec.SkippedTool{Name: spec.Name, Reason: reason})
			continue
		}
		fn, err := spec.Build(s.deps)
		if err != nil {
			s.skipped = append(s.skipped, toolspec.SkippedTool{Name: spec.Name, Reason: err.Error()})
			continue
		}
		ad := NewAdapter(spec.Name, spec.Description(s.deps), fn).
			WithSchema(spec.Parameters()).
			WithReadOnly(spec.IsReadOnly())
		s.adapters[spec.Name] = ad
		s.tiers[spec.Name] = s.profile.Tier(spec)
		ordered = append(ordered, spec.Name)
	}
	// 统一挂 guard：历史上三处 appender 各自记得再调一次，漏一处就是一个不受
	// 工作区边界检查的工具。集中构造之后这个坑不存在了。
	SetGuardOnAdapters(adaptersFor(s.adapters, ordered), guardOf(s.deps))
	s.applyBudget(ordered)
	s.invalidate()
}

// applyBudget 在超出每步声明预算时，把 Warm 工具按声明体积从大到小降级成 Cold。
// 只动 Warm：Hot 是显式选择，Cold 已经在目录里，流程控制类由 Validate() 挡住不许降级。
func (s *Surface) applyBudget(ordered []string) {
	if !s.opts.Deferred || s.opts.BudgetTokens <= 0 {
		return
	}
	for {
		if s.opts.BudgetTokens-s.estimatedTokens(ordered) >= 0 {
			return
		}
		candidate := s.warmestLargest(ordered)
		if candidate == "" {
			return // 预算再紧也不能动 Hot：流程控制与元工具降级会让模型失去收尾能力
		}
		s.tiers[candidate] = toolspec.Cold
	}
}

// warmestLargest 返回当前仍为 Warm 且声明体积最大的工具名；没有候选时返回空串。
func (s *Surface) warmestLargest(ordered []string) string {
	best, bestSize := "", -1
	for _, name := range ordered {
		if s.tiers[name] != toolspec.Warm {
			continue
		}
		if size := s.declarationSize(name); size > bestSize {
			best, bestSize = name, size
		}
	}
	return best
}

// declarationSize 是该工具完整声明的字节数。
func (s *Surface) declarationSize(name string) int {
	ad, ok := s.adapters[name]
	if !ok {
		return 0
	}
	data, err := json.Marshal(ad.ToToolDef())
	if err != nil {
		return 0
	}
	return len(data)
}

// estimatedTokens 估算将要发出的声明总量。
// 用字节数近似而不是 tiktoken：这个数字只用来决定降级顺序，每步都要算，
// 不值得为它引入 tokenizer 的 CPU 开销；误差对"谁先降级"的判断无影响。
func (s *Surface) estimatedTokens(ordered []string) int {
	total := 0
	for _, name := range ordered {
		if s.tiers[name] == toolspec.Cold && !s.materialized[name] {
			total += len(name) + len(toolspec.Brief(name)) + 8 // 目录里一行
			continue
		}
		total += s.declarationSize(name) / 4
	}
	return total
}

func (s *Surface) invalidate() {
	s.advertised = nil
	s.catalog = ""
}

// Defs 返回本步要发给模型的完整工具声明（按名排序，保持 provider 端前缀稳定）。
func (s *Surface) Defs() []llm.ToolDef {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.advertised != nil {
		return s.advertised
	}

	defs := make([]llm.ToolDef, 0, len(s.adapters))
	for name, ad := range s.adapters {
		if s.hidden(name) {
			continue
		}
		defs = append(defs, ad.ToToolDef())
	}
	// 没有任何待取回的冷工具时，load_tool 只是噪音。
	if _, pending := s.pendingCatalog(); !pending {
		defs = dropToolByName(defs, "load_tool")
	}
	SortToolDefs(defs)
	s.advertised = defs
	return defs
}

// Catalog 是注入 system prompt 的冷工具目录；无冷工具时返回空串。
func (s *Surface) Catalog() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.catalog != "" {
		return s.catalog
	}
	lines, ok := s.catalogLines()
	if !ok {
		s.catalog = ""
		return ""
	}
	s.catalog = strings.Join(lines, "\n")
	return s.catalog
}

// catalogLines 生成目录正文。目录只在有冷工具时才存在，此时 load_tool 也必须在场。
func (s *Surface) catalogLines() ([]string, bool) {
	names, pending := s.pendingCatalog()
	if !pending {
		return nil, false
	}
	lines := []string{
		"## Deferred tools",
		"You currently see only their name and one-line summary. Call `load_tool` with the names you need to fetch their full parameter schema, then call them. Batch several names into one call.",
	}
	for _, name := range names {
		lines = append(lines, "- "+name+": "+catalogBrief(s.adapters[name], name))
	}
	return lines, true
}

// pendingCatalog 返回还没取回 schema 的冷工具名（排序后）。
func (s *Surface) pendingCatalog() ([]string, bool) {
	if !s.opts.Deferred {
		return nil, false
	}
	var names []string
	for name, tier := range s.tiers {
		if tier != toolspec.Cold || s.materialized[name] {
			continue
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return nil, false
	}
	sort.Strings(names)
	return names, true
}

// hidden 报告该工具此刻是否不该出现在完整声明里。
// deferred 关闭时没有任何东西是隐藏的 —— 这个开关必须能整体退回历史行为。
func (s *Surface) hidden(name string) bool {
	if !s.opts.Deferred {
		return false
	}
	if name == "load_tool" {
		_, pending := s.pendingCatalog()
		return !pending
	}
	return s.tiers[name] == toolspec.Cold && !s.materialized[name]
}

// ToolState 是 Lookup 的三态结果。
type ToolState int

const (
	// StateReady：已带完整声明，正常执行。
	StateReady ToolState = iota
	// StateColdKnown：注册表里有、但模型还没取回 schema。
	StateColdKnown
	// StateUnknown：这个 Agent 根本没有这个工具。
	StateUnknown
)

// Lookup 按名取适配器与其可见性状态。执行循环据此决定"正常执行 / 先 teach 一次 / 报未找到"。
func (s *Surface) Lookup(name string) (*Adapter, ToolState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ad, ok := s.adapters[name]
	if !ok {
		return nil, StateUnknown
	}
	if s.hidden(name) {
		return ad, StateColdKnown
	}
	return ad, StateReady
}

// Materialize 取回若干冷工具的完整 schema 并让它们从下一步起进入完整声明。
// 返回给模型的文本包含每个工具的 name/description/parameters。
func (s *Surface) Materialize(names []string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.opts.Deferred {
		return "", fmt.Errorf("deferred schema 未启用（[tools.deferred] enabled=false）")
	}
	if len(names) == 0 {
		return "", fmt.Errorf("names 不能为空")
	}
	if len(names) > maxLoadToolNames {
		names = names[:maxLoadToolNames]
	}

	var loaded []string
	var unknown []string
	var notDeferred []string
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		ad, ok := s.adapters[name]
		if !ok {
			unknown = append(unknown, name)
			continue
		}
		if !s.hidden(name) {
			// 已经在完整声明里了（非 Cold，或已取回过）：不再重复登记，只回一句提示。
			notDeferred = append(notDeferred, name)
			continue
		}
		s.materialized[name] = true
		s.invalidate()
		loaded = append(loaded, schemaText(ad, name))
	}

	var b strings.Builder
	for _, l := range loaded {
		b.WriteString(l)
		b.WriteString("\n")
	}
	if len(loaded) > 0 {
		b.WriteString("Now call the tool with these parameters.\n")
	}
	if len(notDeferred) > 0 {
		fmt.Fprintf(&b, "Already available (no need to load): %s\n", strings.Join(notDeferred, ", "))
	}
	if len(unknown) > 0 {
		fmt.Fprintf(&b, "Unknown: %s\n", strings.Join(unknown, ", "))
		if pending, ok := s.pendingCatalog(); ok {
			fmt.Fprintf(&b, "Deferred tools available to load: %s\n", strings.Join(pending, ", "))
		} else {
			b.WriteString("No deferred tools remain in this run.\n")
		}
	}
	if len(loaded) == 0 && len(notDeferred) == 0 && len(unknown) == 0 {
		return "", fmt.Errorf("names 里没有可加载的工具")
	}
	return b.String(), nil
}

func (s *Surface) executeLoadTool(_ context.Context, params map[string]interface{}) (interface{}, error) {
	names := stringSliceParam(params, "names")
	if len(names) == 0 {
		return nil, fmt.Errorf("load_tool requires a non-empty 'names' array")
	}
	return s.Materialize(names)
}

// Add 追加运行期注册的规格（Meta 设计的 Custom Agent 的 delegate_<name>）。
// 追加后立即执行得到支持，且因为 Defs() 每步重算，同一回合的下一步就对模型可见。
func (s *Surface) Add(specs ...*toolspec.Spec) []toolspec.SkippedTool {
	s.mu.Lock()
	defer s.mu.Unlock()

	var skipped []toolspec.SkippedTool
	added := make([]*Adapter, 0, len(specs))
	for _, spec := range specs {
		if spec == nil {
			continue
		}
		if s.adapters[spec.Name] != nil {
			// 重复追加会悄悄换掉执行器（旧适配器仍可能被正在飞行的步骤引用）。
			// 现在只有 Meta 注册路径可能走到这里，同名 Custom Agent 已被
			// metaHandler.Register 拒绝，所以这层是防御性的：记录并忽略。
			skipped = append(skipped, toolspec.SkippedTool{Name: spec.Name, Reason: "同名工具已存在，忽略重复注册"})
			continue
		}
		if ok, reason := spec.AvailableFor(s.deps); !ok {
			skipped = append(skipped, toolspec.SkippedTool{Name: spec.Name, Reason: reason})
			continue
		}
		fn, err := spec.Build(s.deps)
		if err != nil {
			skipped = append(skipped, toolspec.SkippedTool{Name: spec.Name, Reason: err.Error()})
			continue
		}
		ad := NewAdapter(spec.Name, spec.Description(s.deps), fn).
			WithSchema(spec.Parameters()).
			WithReadOnly(spec.IsReadOnly())
		s.adapters[spec.Name] = ad
		s.tiers[spec.Name] = spec.Frequency
		added = append(added, ad)
	}
	if len(added) > 0 {
		SetGuardOnAdapters(added, guardOf(s.deps))
		s.invalidate()
	}
	return skipped
}

// Materialized 返回已取回 schema 的工具名（排序后），供 rollout 持久化。
func (s *Surface) Materialized() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.materialized))
	for name, on := range s.materialized {
		if on {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// RestoreMaterialized 在 /resume 时恢复上次的取回记录，避免续跑重新付一遍 load_tool。
func (s *Surface) RestoreMaterialized(names []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for _, name := range names {
		if _, ok := s.adapters[name]; ok && !s.materialized[name] {
			s.materialized[name] = true
			changed = true
		}
	}
	if changed {
		s.invalidate()
	}
}

// Skipped 返回因依赖缺失而没注册的工具及原因。
func (s *Surface) Skipped() []toolspec.SkippedTool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]toolspec.SkippedTool(nil), s.skipped...)
}

// Adapters 返回全部已构造的适配器（按名排序），供测试与目录展示对齐。
func (s *Surface) Adapters() []*Adapter {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.adapters))
	for name := range s.adapters {
		names = append(names, name)
	}
	sort.Strings(names)
	return adaptersFor(s.adapters, names)
}

// Names 返回工具面里的全部工具名（排序后），golden 测试用。
func (s *Surface) Names() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.adapters))
	for name := range s.adapters {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Tier 报告某工具在当前档位下的实际频率档。deferred 关闭时一律是 Hot ——
// 档位是 deferred 的概念，关掉后不存在"冷工具"。
func (s *Surface) Tier(name string) toolspec.Frequency {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.opts.Deferred {
		return toolspec.Hot
	}
	return s.tiers[name]
}

func adaptersFor(m map[string]*Adapter, names []string) []*Adapter {
	out := make([]*Adapter, 0, len(names))
	for _, n := range names {
		if ad, ok := m[n]; ok {
			out = append(out, ad)
		}
	}
	return out
}

func guardOf(deps *toolspec.Deps) *WorkspaceGuard {
	g, ok := deps.Guard.(*WorkspaceGuard)
	if !ok {
		return nil
	}
	return g
}

func dropToolByName(defs []llm.ToolDef, name string) []llm.ToolDef {
	out := defs[:0]
	for _, d := range defs {
		if d.Function.Name == name {
			continue
		}
		out = append(out, d)
	}
	return out
}

// maxLoadToolNames 与 schemas.json 里 load_tool 的 maxItems 保持一致。
const maxLoadToolNames = 8

func schemaText(ad *Adapter, name string) string {
	payload := map[string]interface{}{
		"name":        name,
		"description": ad.Description(),
		"parameters":  ad.schema,
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return name + ": schema unavailable: " + err.Error()
	}
	return string(data)
}

// catalogBrief 给冷工具目录里的一行简介。
// Cold 档本就在 Validate() 里强制要求 Brief；这里仍兜一层，
// 免得预算降级进来的 Warm 工具（没写 Brief）在目录里显示成空串。
func catalogBrief(ad *Adapter, name string) string {
	if b := toolspec.Brief(name); b != "" {
		return b
	}
	desc := ad.Description()
	if idx := strings.IndexByte(desc, '.'); idx > 0 {
		desc = desc[:idx]
	}
	runes := []rune(strings.TrimSpace(desc))
	if len(runes) > 60 {
		runes = runes[:60]
	}
	return string(runes)
}

func stringSliceParam(params map[string]interface{}, key string) []string {
	raw, ok := params[key].([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

// loadToolSpec 声明取回入口本身。它必须 Hot 且不可被降级，
// 否则模型会连"怎么取回 schema"都得先取回。
func init() {
	toolspec.MustRegister(toolspec.Spec{
		Name:      "load_tool",
		Effect:    toolspec.EffectToolLoad,
		Frequency: toolspec.Hot,
		Serial:    true,
		Trunc:     toolspec.TruncProtected,
		Brief:     "取回 deferred 工具的完整参数声明",
		Display:   toolspec.DisplayCompact,
		Available: func(d *toolspec.Deps) (bool, string) {
			if !d.DeferSchemas || d.LoadTool == nil {
				return false, "deferred schema 未启用（[tools.deferred] enabled=false）"
			}
			return true, ""
		},
		Build: func(d *toolspec.Deps) (toolspec.ToolFunc, error) {
			if d.LoadTool == nil {
				return nil, fmt.Errorf("load_tool 未绑定")
			}
			return d.LoadTool, nil
		},
	})
}
