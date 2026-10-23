package toolspec

import (
	"context"
	"sort"
	"strings"
)

// Profile 描述一个 Agent 的工具面：它能拿到哪些能力、明确排除哪些、
// 以及（Director 专用）可以发放给 Meta 设计的 Custom Agent 的上限。
type Profile struct {
	// Agent 是画像名，也是 Audience 比对的取值（"director"/"coding"/"browser"…）。
	Agent string
	// Grant 是能力白名单：Effect 与之有交集的工具才会进入工具面。
	Grant Effect
	// Deny 是能力黑名单，在 Grant 之后生效（同族里排除整类时用）。
	Deny Effect
	// Palette 是 Custom Agent 可选能力的上限。仅 Director 的画像有意义。
	Palette Effect
	// Promote/Demote 按工具名覆盖频率档，用于"该 Agent 的主力工具"这类偏差。
	Promote []string
	Demote  []string
	// Names 非空时走"按名挑选"而不是能力匹配：Meta 设计的 Custom Agent 交来的是
	// 一份工具名清单（LLM 运行期产出），不是能力集合。能力数学在那种输入下会失真
	// ——按 Effect 展开会把同族工具一并发给它。
	// 调用方必须先用 Palette 校验过这份名单（MatchNames），这里只负责装配。
	Names []string
	// RequiresExit 为真时，ValidateProfile 会断言 agent_exit 确实在工具面里。
	// 关掉它的唯一合理场景是 Chat 这种"纯问答即收尾"的画像。
	RequiresExit bool
}

// testDeps 返回一个"能力齐备"的探针 Deps：只让 Available 的 nil 检查通过，
// 不注入任何真实实现。供 ValidateProfile 做静态形状校验用。
func (p Profile) testDeps() *Deps {
	return &Deps{
		AgentName:   p.Agent,
		ProjectPath: "/tmp/yagent-probe",
		BoardID:     "probe-board",
		Files:       probeFileOps{},
		Search:      probeSearchOps{},
		Sys:         probeSysOps{},
		Edit:        probeEditOps{},
		Repo:        probeRepoOps{},
		Flow:        probeFlowOps{},
		Thinker:     probeThinker{},
		Micro:       probeMicro{},
		Deep:        probeDeep{},
		Design:      probeDesign{},
		Browser:     probeBrowser{},
		Delegate: func(context.Context, string, map[string]interface{}) (interface{}, error) {
			return nil, nil
		},
		CheckpointList: probeToolFunc, CheckpointCreate: probeToolFunc, CheckpointRollback: probeToolFunc,
		LoadTool: probeToolFunc,
		// Blueprint 恒绑：生产里 depsFor 永远给它一个非 nil 执行器（没有蓝图时
		// 工具负责回答"为什么没有"），探针必须与生产同形 —— 否则 golden 会代表
		// 一条生产不存在的路径（§11 坑 33 就是这么发现的）。
		Blueprint: probeToolFunc,
		// Skills 同理：生产恒绑。use_skill 的 Available 是 nil（没有技能也要在场并解释），
		// 所以少了这次绑定它不会从 golden 消失，而是让探针回答"没有技能" ——
		// 探针与生产不同形的后果从"少一个工具"变成"一条永远为真的路径"（坑 39 的变体）。
		Skills: probeSkills{},
		// 探针按默认配置（[tools.deferred] enabled=true）建模，否则 golden 里
		// 每个画像都会少掉 load_tool。
		DeferSchemas:  true,
		ResolveTarget: func(string) bool { return true },
	}
}

func probeToolFunc(context.Context, map[string]interface{}) (interface{}, error) { return nil, nil }

// probeSkills 是 SkillSource 的探针：只让 "d.Skills != nil" 成立，
// 不提供任何真实技能（Available 为 nil 的工具不看它的返回值）。
type probeSkills struct{}

func (probeSkills) Catalog(int) string { return "" }

func (probeSkills) Body(string, int, int) (SkillBody, error) { return SkillBody{}, ErrSkillNotFound }

// Match 返回画像应得的全部 Spec：Grant 命中 → Deny 排除 → Audience 允许 → Available 通过。
// 结果按名字排序，与发给模型的 toolDefs 顺序一致。
func Match(p Profile, deps *Deps) []*Spec {
	return match(p, deps, false)
}

func match(p Profile, deps *Deps, ignoreAvailability bool) []*Spec {
	if deps == nil {
		deps = p.testDeps()
	}
	if len(p.Names) > 0 {
		return matchByNames(p, deps, ignoreAvailability)
	}
	var out []*Spec
	for _, s := range Specs() {
		if !s.Effect.Has(p.Grant) || s.Effect.Has(p.Deny) {
			continue
		}
		if !audienceAllows(s, p.Agent) {
			continue
		}
		if !ignoreAvailability && s.Available != nil {
			if ok, _ := s.Available(deps); !ok {
				continue
			}
		}
		out = append(out, s)
	}
	return out
}

// matchByNames 是 Custom Agent 的装配路径：只认清单，不做能力展开。
// Audience 在这里不适用（同名工具是否可发给该 Agent 已由 Palette 校验决定），
// 但运行期可用性仍然算——否则会给它一个必然失败的工具。
func matchByNames(p Profile, deps *Deps, ignoreAvailability bool) []*Spec {
	var out []*Spec
	seen := map[string]bool{}
	for _, raw := range p.Names {
		name := strings.TrimSpace(raw)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		s, ok := Get(name)
		if !ok {
			continue
		}
		if !ignoreAvailability && s.Available != nil {
			if available, _ := s.Available(deps); !available {
				continue
			}
		}
		out = append(out, s)
	}
	sortSpecsByName(out)
	return out
}

// audienceAllows 校验 Audience 白名单。空 Audience = 由 Effect 决定受众，一律放行。
func audienceAllows(s *Spec, agent string) bool {
	if len(s.Audience) == 0 {
		return true
	}
	for _, a := range s.Audience {
		if a == agent {
			return true
		}
	}
	return false
}

// resolveTier 应用画像级调档（Promote/Demote）。预算降级在 Surface 里做，不在这里。
func (p Profile) resolveTier(s *Spec) Frequency {
	for _, name := range p.Promote {
		if name == s.Name {
			return Hot
		}
	}
	for _, name := range p.Demote {
		if name == s.Name {
			return Cold
		}
	}
	return s.Frequency
}

// MatchNames 按**名字**挑选已注册的工具，用于 Meta 设计的 Custom Agent：
// 那是一份 LLM 在运行期产出的名单，不是能力集合，所以这里必须是名字语义。
//
// 三道校验替代了历史的 getToolFunc + toolDefMap 双查找：
//   - 名字没注册 → 记进 unknown（过去是 slog.Warn 后静默丢弃）；
//   - 能力不在 palette 内 → 记进 rejected，Custom Agent 拿不到 Director 没有的能力；
//   - agent_exit 总是补上（收尾工具），除非它本身被 palette 排除。
func MatchNames(names []string, palette Effect, deps *Deps) (selected []*Spec, unknown, rejected []string) {
	_ = deps
	seen := map[string]bool{}
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		s, ok := Get(name)
		if !ok {
			unknown = append(unknown, name)
			continue
		}
		if !s.Effect.Has(palette) {
			rejected = append(rejected, name)
			continue
		}
		// 运行期可用性不在这里判定：那属于装配（Surface 会记录跳过原因），
		// 混进来的话"权限不够"和"这个 Agent 没接 Shell"就分不开，排障时会误判。
		selected = append(selected, s)
	}
	if exit, ok := Get(exitToolName); ok && !seen[exitToolName] && exit.Effect.Has(palette) {
		selected = append(selected, exit)
	}
	sortSpecsByName(selected)
	return selected, unknown, rejected
}

func sortSpecsByName(specs []*Spec) {
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
}
