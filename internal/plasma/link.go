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
	// EscalationRequired 是"这份 bundle 用到了 PlasmaPalette 之外的能力"（ADR-4 升级门）。
	// 它为真而 EscalationApproved 为假时：工具面里那些能力位的工具**不在** Agent.Tools 里
	// （阶段 2 已按未批准的上限拒掉了），grade 也钉在 provisional —— 首次委派必须先问用户。
	EscalationRequired bool
	// EscalationApproved 由加载期从 `{name}/{hash}/meta.json` 读出（按 hash 记）。
	// 它是"这一次装配能不能把越权能力发出去"的唯一判据，bundle 本身不认识它（写进 bundle 会改 hash）。
	EscalationApproved bool
}

// Link 对**活注册表**重跑阶段 1-4，然后产出装配计划。
//
// 为什么加载期要重跑而不是信 store 里的 report：bundle 落盘时对着的注册表可能已经变了
// （工具改名/删除/能力位调整）。hash 只保证"文件没被改"，不保证"它依赖的东西还在"。
// 加载期是防磁盘篡改与注册表漂移的最后一道闸。
func (c *Compiler) Link(b *Bundle, hash string) (*LinkPlan, error) {
	r := c.Compile(context.Background(), b) // 阶段 1-4 是纯计算，不读 ctx
	return c.planFrom(b, hash, r, false)
}

// LoadLink 是**加载期**的复核入口：只跑阶段 1–3（§6.1「加载期不跑阶段 4」）。
//
// 与 Link 的差别有两处，都是加载期特有的：
//   - 阶段集合：跳过阶段 4（token 计数）与结构夹具补齐（后者会改 bundle 字节）；
//   - 容忍"只因为越权而被拒"的那一份：`errorsOnlyEscalation` 为真时照样产出计划，
//     只是 `Agent.Tools` 里没有那些越权工具、grade 钉 provisional。理由见该函数。
//
// plan 的构造与 Link 是同一份（planFrom）——两条路径各自拼一份的后果是"编译期装出来的
// Agent 与加载期装出来的不是同一套工具面"，而那要等到某个工具在某一侧消失才会暴露。
func (c *Compiler) LoadLink(b *Bundle, hash string) (*LinkPlan, error) {
	r := c.loadReport(b)
	return c.planFrom(b, hash, r, true)
}

// planFrom 把一份复核报告投影成装配计划。Link 与 LoadLink 的**唯一**共同出口。
//
// allowEscalationOnly=true 是加载期语义：越权导致的那几条 error 不阻断装配，
// 因为升级门（P9b）需要在"Agent 已注册"的前提下才有机会问用户 —— 不注册就没有首次委派，
// 也就永远走不到批准那一步。真正的越权工具仍然不在工具面里（阶段 2 已拒）。
func (c *Compiler) planFrom(b *Bundle, hash string, r *Report, allowEscalationOnly bool) (*LinkPlan, error) {
	if b == nil {
		return nil, fmt.Errorf("nil bundle")
	}
	if r.HasErrors() {
		if !allowEscalationOnly || !r.errorsOnlyEscalation() {
			return nil, fmt.Errorf("link %s@%s 失败:\n%s", b.Name, hash, r.Text())
		}
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
	if grade == "" && r.EscalationRequired {
		// 加载期容忍的那一类：报告里没有 grade（有 error），但升级门接管后它仍然是
		// "可人工点名委派、不可自动委派"的 provisional。
		grade = GradeProvisional
	}
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
		Hash:               hash,
		Grade:              grade,
		EscalationRequired: r.EscalationRequired,
		EscalationApproved: c.EscalationApproved != 0,
	}, nil
}

// Skipped 是"这份 bundle 没被加载"的可读原因。绝不静默跳过：
// 一个 Agent 从工具面消失而没人知道原因，与它从来没存在过无法区分（坑 43）。
//
// Hash 一并带上：同一个 name 在 store 里可能有多个 hash，只说 name 的账目让人无法判断
// "当前指针指向的那一份"出了什么问题。
type Skipped struct {
	Name   string
	Hash   string
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
			skipped = append(skipped, Skipped{Name: e.Name, Hash: e.Hash, Reason: err.Error()})
			continue
		}
		// 批准**按 hash** 逐个注入：LoadAll 用一个共享 Compiler 的话，A 的批准会漏到 B 上。
		// 先克隆再改，是为了让调用方传进来的 Compiler 保持"用户给定的上限"这一语义
		// （它可能是 CLI 里带 --approve 的那个实例）。
		cc := *c
		if meta != nil && meta.EscalationApproved {
			cc.EscalationApproved = EscalationEffects(b, cc.Palette)
		} else {
			cc.EscalationApproved = 0
		}
		plan, err := cc.LoadLink(b, e.Hash)
		if err != nil {
			// PLS-702：hash 对得上但活注册表复核不过（工具被改名/删除/能力位变了）。
			// 与 PLS-701（磁盘被改）分开，是因为修法完全不同：一个是重装/回滚，
			// 一个是改注册表或重新生成。错误正文已逐条点名是哪个工具。
			skipped = append(skipped, Skipped{
				Name: e.Name, Hash: e.Hash,
				Reason: fmt.Sprintf("%s 加载期复核失败（工具被改名/删除？）：%v", CodeLoadDrift, err),
			})
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
