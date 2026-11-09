package plasma

import (
	"context"
	"fmt"
	"sort"
	"time"

	"yagent/internal/toolspec"
)

// CustomAgentView / ExecutorConfigView 是 plasma 侧的纯数据：它不认识 agents 包，
// 由 agents 侧把它们转成 director.CustomAgent 与 agents.ExecutorConfig。
// 这条分工是叶子约束的必然结果，也是"plasma 决定要哪些工具、agents 决定怎么装配"的落点。
type CustomAgentView struct {
	Name         string
	DisplayName  string
	SystemPrompt string
	Tools        []string
	Description  string
}

type ExecutorConfigView struct {
	MaxSteps                 int
	StopOnFinish             bool
	LLMTimeout               time.Duration
	Tier                     string
	CompressionEnable        bool
	KeepToolResultTokens     int
	MaxParallelReadOnlyTools int
}

// LinkPlan 是加载期产物：编译器把 bundle 复核成一份"可以直接装配"的计划。
type LinkPlan struct {
	Agent   CustomAgentView
	Profile toolspec.Profile
	Config  ExecutorConfigView
	Hash    string
	Grade   Grade
}

// Link 对**活注册表**重跑阶段 1-4，然后产出装配计划。
//
// 为什么加载期要重跑而不是信 store 里的 report：bundle 落盘时对着的注册表可能已经变了
// （工具改名/删除/能力位调整）。hash 只保证"文件没被改"，不保证"它依赖的东西还在"。
// 加载期是防磁盘篡改与注册表漂移的最后一道闸。
func (c *Compiler) Link(b *Bundle, hash string) (*LinkPlan, error) {
	if b == nil {
		return nil, fmt.Errorf("nil bundle")
	}
	r := c.Compile(context.Background(), b) // 阶段 1-4 是纯计算，不读 ctx
	if r.HasErrors() {
		return nil, fmt.Errorf("link %s@%s 失败:\n%s", b.Name, hash, r.Text())
	}
	selected, _, _ := toolspec.MatchNames(b.Tools, c.effective(), nil)
	tools := make([]string, 0, len(selected))
	for _, s := range selected {
		tools = append(tools, s.Name)
	}
	// Profile.Names 用**解析后**的名单，不是原始的 b.Tools：
	//   - MatchNames 已去重、剔除越权项、并自动追加 agent_exit（只此一份规则）；
	//   - 而 Surface 的按名通道（toolspec.matchByNames）**只认清单、不补收尾工具**，
	//     沿用原始清单装配出来的 Agent 会无法收尾，只能烧到 max_steps。
	// Palette/Grant 也在这里落位：`VerifyStructural` 的 palette-containment 要与
	// 阶段 3 的 typecheck 用同一个 effective 判据，否则夹具就成了第二套能力数学。
	profile := toolspec.Profile{
		Agent:        b.Name,
		Grant:        c.effective(),
		Palette:      c.effective(),
		Names:        append([]string(nil), tools...),
		RequiresExit: true,
	}
	grade := r.Grade
	if r.EscalationRequired && grade != "" && grade != GradeProvisional {
		grade = GradeProvisional // 升级必须有夹具证据，不能凭报告拿高等级
	}
	return &LinkPlan{
		Agent: CustomAgentView{
			Name:         b.Name,
			DisplayName:  b.DisplayName,
			SystemPrompt: b.Prompt.System,
			Tools:        tools,
			Description:  fmt.Sprintf("Plasma agent %s", b.DisplayName),
		},
		Profile: profile,
		Config: ExecutorConfigView{
			MaxSteps:                 b.Runtime.MaxSteps,
			StopOnFinish:             b.Runtime.StopOnFinish,
			LLMTimeout:               b.Runtime.LLMTimeout,
			Tier:                     b.Runtime.Tier,
			CompressionEnable:        b.Runtime.Compression.Enable,
			KeepToolResultTokens:     b.Runtime.Compression.KeepToolResultTokens,
			MaxParallelReadOnlyTools: b.Runtime.MaxParallelReadOnlyTools,
		},
		Hash:  hash,
		Grade: grade,
	}, nil
}

// Skipped 是"这份 bundle 没被加载"的可读原因。绝不静默跳过：
// 一个 Agent 从工具面消失而没人知道原因，与它从来没存在过无法区分（坑 43）。
type Skipped struct {
	Name   string
	Reason string
}

// Loaded 是加载成功的一项。
type Loaded struct {
	Plan *LinkPlan
	Meta Meta
}

// LoadAll 按 name 字典序加载 store 里所有 Agent。
//
// 字典序不是审美：agents 侧的 customAgentPromptSection 拼在 blueprint/skills/mcp 之前，
// 顺序抖动会让后面全部前缀缓存失效。
// max <= 0 用 DefaultMaxLoadedAgents；超预算按 grade→score→name 截断并返回 omitted 条数，
// 调用方必须把省略写出来（静默少一个 Agent = 模型以为自己没有这个能力）。
func LoadAll(s *Store, c *Compiler, max int) (loaded []Loaded, skipped []Skipped, omitted int) {
	if max <= 0 {
		max = DefaultMaxLoadedAgents
	}
	entries := s.List()
	// List 已按 name 排序；这里再按 grade→score 排是错的（会打乱字典序）。
	// 预算截断按"高价值优先"，但**保持被选中项的字典序**由 List 的起点保证。
	ordered := rankForBudget(entries)
	picked := ordered
	if len(picked) > max {
		omitted = len(picked) - max
		picked = picked[:max]
	}
	// 恢复字典序呈现：预算只决定"留哪些"，不决定"按什么顺序给模型看"。
	keep := map[string]bool{}
	for _, e := range picked {
		keep[e.Name] = true
	}
	for _, e := range entries { // entries 已按 name
		if !keep[e.Name] {
			continue
		}
		b, meta, err := s.Load(e.Name, e.Hash)
		if err != nil {
			skipped = append(skipped, Skipped{Name: e.Name, Reason: err.Error()})
			continue
		}
		plan, err := c.Link(b, e.Hash)
		if err != nil {
			skipped = append(skipped, Skipped{Name: e.Name, Reason: err.Error()})
			continue
		}
		loaded = append(loaded, Loaded{Plan: plan, Meta: *meta})
	}
	return loaded, skipped, omitted
}

// DefaultMaxLoadedAgents 是加载预算的兜底值（配置可覆盖）。
const DefaultMaxLoadedAgents = 12

func gradeRank(g Grade) int {
	switch g {
	case GradePromoted:
		return 0
	case GradeVerified:
		return 1
	default:
		return 2
	}
}

// rankForBudget 按 grade→score→name 选出"预算内优先保留"的集合。
// 结果只用于决定留哪些，呈现顺序仍回字典序。
func rankForBudget(entries []Entry) []Entry {
	out := append([]Entry(nil), entries...)
	sort.SliceStable(out, func(i, j int) bool {
		gi, gj := gradeRank(out[i].Meta.Grade), gradeRank(out[j].Meta.Grade)
		if gi != gj {
			return gi < gj
		}
		if out[i].Meta.Score != out[j].Meta.Score {
			return out[i].Meta.Score > out[j].Meta.Score
		}
		return out[i].Name < out[j].Name
	})
	return out
}
