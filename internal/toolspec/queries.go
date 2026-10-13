package toolspec

// 本文件是"按工具名查询派生元数据"的入口，用来替代原先散在 8 处的名字表：
// knownReadOnlyTools / nonParallelToolNames / dangerousTools+pathParamNames /
// toolTruncationPriority / isInteractiveUserTool / skipBodyTools / [tools.llm] 槽。
//
// 未注册的名字一律返回 fail-safe 零值（不可并发、不需授权、常规截断优先级），
// 与这些表的历史默认行为一致。

// IsReadOnly 报告该工具能否与其他只读工具并发。未注册名字返回 false。
func IsReadOnly(name string) bool {
	s, ok := Get(name)
	if !ok {
		return false
	}
	return s.IsReadOnly()
}

// IsSerial 报告该工具是否必须串行执行。
func IsSerial(name string) bool {
	s, ok := Get(name)
	if !ok {
		return false
	}
	return s.Serial
}

// IsInteractive 报告该工具是否会阻塞等待用户响应（因此不能套工具级超时）。
func IsInteractive(name string) bool {
	s, ok := Get(name)
	if !ok {
		return false
	}
	return s.BlocksOnUser
}

// NeedsAuth 报告该工具是否需要 WorkspaceGuard 授权。
func NeedsAuth(name string) bool {
	s, ok := Get(name)
	if !ok {
		return false
	}
	return s.NeedsAuth
}

// PathParams 返回参与工作区边界检查的参数名。
func PathParams(name string) []string {
	s, ok := Get(name)
	if !ok {
		return nil
	}
	return s.PathParams
}

// EffectOf 返回该工具声明的能力标签；未注册返回 0。
func EffectOf(name string) Effect {
	s, ok := Get(name)
	if !ok {
		return 0
	}
	return s.Effect
}

// MatchAll 返回画像能力匹配到的全部 Spec，**不做**运行期可用性过滤。
// Surface 需要它：只看 Match 的话，"因为依赖没装配而消失的工具"会变成又一次
// 静默失效 —— 正是本次重构要消灭的头号特征。调用方必须逐个评估 Available 并把
// 落选原因记录下来。
func MatchAll(p Profile, deps *Deps) []*Spec {
	return match(p, deps, true)
}

// Available 评估该 Spec 在当前依赖下是否可用。nil Available 视为可用。
func (s *Spec) AvailableFor(deps *Deps) (bool, string) {
	if s.Available == nil {
		return true, ""
	}
	if deps == nil {
		deps = &Deps{}
	}
	return s.Available(deps)
}

// TruncationPriority 返回压缩层的截断优先级（0=先截 1=常规 -1=受保护）。
// 未注册的名字返回 1，与历史 toolTruncationPriority 的 default 分支一致。
func TruncationPriority(name string) int {
	s, ok := Get(name)
	if !ok {
		return 1
	}
	return s.Trunc.ToPriority()
}

// IsCompactDisplay 报告 TUI 是否应省略该工具结果的正文。
func IsCompactDisplay(name string) bool {
	s, ok := Get(name)
	if !ok {
		return false
	}
	return s.Display == DisplayCompact
}

// ModelSlot 返回该工具可绑定独立 provider 的配置槽名；空串表示沿用所属 Agent 引擎。
func ModelSlot(name string) string {
	s, ok := Get(name)
	if !ok {
		return ""
	}
	return s.ModelSlot
}

// Brief 返回一句话介绍（deferred 目录与降级文案用）。
func Brief(name string) string {
	s, ok := Get(name)
	if !ok {
		return ""
	}
	return s.Brief
}

// Tier 返回该工具在指定画像下的频率档。
func (p Profile) Tier(s *Spec) Frequency { return p.resolveTier(s) }

// DeferredSpecs 返回还没取回过完整 schema 的 Cold 工具 —— 它们只出现在目录里。
// materialized 为 Surface 当前已 materialize 的集合（可为 nil）。
func (p Profile) DeferredSpecs(deps *Deps, materialized map[string]bool) []*Spec {
	var out []*Spec
	for _, s := range Match(p, deps) {
		if p.resolveTier(s) != Cold {
			continue
		}
		if materialized[s.Name] {
			continue
		}
		out = append(out, s)
	}
	return out
}

// AdvertiseSpecs 返回每步应当带完整声明的工具：非 Cold，或已被 materialize 的 Cold。
func (p Profile) AdvertiseSpecs(deps *Deps, materialized map[string]bool) []*Spec {
	var out []*Spec
	for _, s := range Match(p, deps) {
		if p.resolveTier(s) == Cold && !materialized[s.Name] {
			continue
		}
		out = append(out, s)
	}
	return out
}
