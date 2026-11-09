// Package plasma 是生成式 Agent 的编译器与 IR。
//
// 边界：本包只做"一份 bundle 是否可被信任地装配成 Agent"的判定，不执行 Agent、
// 不调 LLM、不碰工作区。它是叶子包（scripts/check_plasma_leaf.sh）：需要的外部能力
// 一律由调用方作为值或窄接口传进来 —— palette 由调用方注入（P1 起是 PlasmaPalette），
// 配置默认值由 Defaults 注入，夹具执行由 FixtureRunner 注入（P6）。
//
// 设计稿：docs/plasma-architecture.md。
package plasma

import "time"

// SchemaVersion 是 IR 的版本。读侧遇到不认得的版本必须拒载而不是尽力解析。
const SchemaVersion = 1

// normalizeRuntime 从落盘文本还原只在内存里存在的字段。
// Runtime.LLMTimeout 是 time.Duration 且标了 json:"-"（序列化用 LLMTimeoutText，保证可读），
// 所以**每一次从磁盘读回都必须调它** —— 漏掉的表现是加载期把 0s 判成越界（P4 实测踩到）。
func (b *Bundle) normalizeRuntime() error {
	if b.Runtime.LLMTimeoutText == "" {
		return nil
	}
	d, err := time.ParseDuration(b.Runtime.LLMTimeoutText)
	if err != nil {
		return err
	}
	b.Runtime.LLMTimeout = d
	return nil
}

// Grade 是质量棘轮的三档（ADR-9）。只有 verified 以上可被自动委派。
type Grade string

const (
	GradeProvisional Grade = "provisional"
	GradeVerified    Grade = "verified"
	GradePromoted    Grade = "promoted"
)

// Bundle 是一份 Agent 的完整声明。运行期字段全部落进既有 Go 类型
// （CustomAgent / toolspec.Profile / ExecutorConfig），policy/contract/fixtures/quality
// 只在编译期与加载期活着，不进运行时权限来源。
type Bundle struct {
	SchemaVersion  int       `json:"schema_version"`
	Name           string    `json:"name"`
	DisplayName    string    `json:"display_name"`
	Version        string    `json:"version,omitempty"`
	CapabilityLock string    `json:"capability_lock,omitempty"`
	Lineage        *Lineage  `json:"lineage,omitempty"`
	Prompt         Prompt    `json:"prompt"`
	Tools          []string  `json:"tools"`
	Policy         Policy    `json:"policy"`
	Runtime        Runtime   `json:"runtime"`
	Contract       Contract  `json:"contract"`
	Fixtures       *Fixtures `json:"fixtures,omitempty"`
	Quality        Quality   `json:"quality"`

	// TaskForAgent 来自最小 spec，交给执行路径用；不落盘（IR 里没有"这次要干什么"）。
	TaskForAgent string `json:"-"`
}

type Prompt struct {
	System string `json:"system"`
	Lang   string `json:"lang,omitempty"`
}

// Policy 是编译器的输入，不是运行时的授权依据：运行时只认 Effect 位掩码 +
// WorkspaceGuard + 授权回路。Deny 用能力名（toolspec.Effect.Names 的词表）。
type Policy struct {
	EffectsRequired []string `json:"effects_required,omitempty"`
	Deny            []string `json:"deny,omitempty"`
	WorkspaceScoped bool     `json:"workspace_scoped"`
}

type Runtime struct {
	MaxSteps                 int                `json:"max_steps"`
	StopOnFinish             bool               `json:"stop_on_finish"`
	LLMTimeout               time.Duration      `json:"-"`
	LLMTimeoutText           string             `json:"llm_timeout,omitempty"`
	Tier                     string             `json:"tier,omitempty"`
	Compression              CompressionOptions `json:"compression"`
	MaxParallelReadOnlyTools int                `json:"max_parallel_readonly_tools"`
}

type CompressionOptions struct {
	Enable               bool `json:"enable"`
	KeepToolResultTokens int  `json:"keep_tool_result_tokens,omitempty"`
}

// Contract 的 MustExitWith 对齐 agent_exit 的 schema：required 只有 reason，
// handoff.conclusion 是可选但强烈推荐（走 lint 而不是 required）。
type Contract struct {
	MustExitWith  []string `json:"must_exit_with,omitempty"`
	ThoughtFormat bool     `json:"thought_format"`
}

type Lineage struct {
	ParentHash string     `json:"parent_hash,omitempty"`
	Mutation   string     `json:"mutation,omitempty"`
	Rejected   []Rejected `json:"rejected,omitempty"`
}

// Rejected 是一次编译失败的变异：免费的负样本，下一轮生成时作为反例喂回去。
type Rejected struct {
	Mutation string `json:"mutation"`
	Diag     string `json:"diag"`
}

type Fixtures struct {
	Provenance Provenance `json:"provenance"`
	Structural []Fixture  `json:"structural,omitempty"`
	Behavioral []Fixture  `json:"behavioral,omitempty"`
}

// Provenance 记出题者谱系：同一 lineage 的变异体不得由自己的出题动作验证（ADR-5，防 Goodhart）。
type Provenance struct {
	Author  string `json:"author"`
	Lineage string `json:"lineage"`
}

// Fixture 的执行在 P6（FixtureRunner）。P0 只承载形状。
type Fixture struct {
	ID        string       `json:"id"`
	Workspace string       `json:"workspace,omitempty"`
	Input     string       `json:"input"`
	Script    []ScriptStep `json:"script,omitempty"`
	Expect    Expect       `json:"expect"`
	Rubric    []string     `json:"rubric,omitempty"`
}

type ScriptStep struct {
	Tool string                 `json:"tool"`
	Args map[string]interface{} `json:"args,omitempty"`
	Text string                 `json:"text,omitempty"`
}

type Expect struct {
	MustCall            []string `json:"must_call,omitempty"`
	MustNotCall         []string `json:"must_not_call,omitempty"`
	MaxSteps            int      `json:"max_steps,omitempty"`
	ExitShape           []string `json:"exit_shape,omitempty"`
	DeliverableContains []string `json:"deliverable_contains,omitempty"`
	DeliverableEquals   string   `json:"deliverable_equals,omitempty"`
	StopKind            string   `json:"stop_kind,omitempty"`
}

type Quality struct {
	Grade                Grade    `json:"grade"`
	Score                float64  `json:"score"`
	Runs                 int      `json:"runs"`
	Lint                 []string `json:"lint,omitempty"`
	BehavioralVerifiedAt string   `json:"behavioral_verified_at,omitempty"`
	BehavioralTokens     int      `json:"behavioral_tokens,omitempty"`
}
