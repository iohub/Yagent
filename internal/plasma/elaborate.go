package plasma

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"yagent/internal/toolspec"
	"yagent/internal/util"
)

// Defaults 是扩充最小 spec 时的取值来源。plasma 是叶子包、不 import config，
// 所以配置值由装配层读出来后作为值传进来（与 blueprint 的预算/projectID 同一取向）。
type Defaults struct {
	Lang                     string
	MaxSteps                 int
	StopOnFinish             bool
	LLMTimeout               time.Duration
	Tier                     string
	CompressionEnabled       bool
	KeepToolResultTokens     int
	MaxParallelReadOnlyTools int
	Deny                     []string
}

// DefaultDefaults 是兜底常数的单一来源。调用方从配置覆盖它；
// 没有任何调用方覆盖时这些值就是生效值（所以它们必须真的被用到，不是文档装饰）。
func DefaultDefaults() Defaults {
	return Defaults{
		Lang:                     "Chinese",
		MaxSteps:                 15,
		StopOnFinish:             true,
		LLMTimeout:               120 * time.Second,
		Tier:                     "balanced",
		CompressionEnabled:       true,
		KeepToolResultTokens:     200,
		MaxParallelReadOnlyTools: 4,
		Deny:                     []string{"fs_write", "shell"},
	}
}

// minimalSpec 是 Meta 今天产出的 5 字段形状（meta.prompt.md 的 Output Format）。
// 沿用它让 Python 侧首版可以直接拿那份 prompt 当策略种子。
type minimalSpec struct {
	Thinking     string   `json:"thinking"`
	AgentName    string   `json:"agent_name"`
	AgentDesign  string   `json:"agent_design"`
	ToolsUsed    []string `json:"tools_used"`
	TaskForAgent string   `json:"task_for_agent"`
}

// Elaborate 认两种输入形态并归一化成完整 IR：最小 spec（走扩充）与完整 IR（走补齐）。
// 它只做形状与默认值，不做权限/注册表判定 —— 那是 Compile 的阶段 2-3。
func Elaborate(raw []byte, d Defaults) (*Bundle, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, &DiagError{Diag: Diagnostic{
			Code: CodeBadJSON, Severity: SeverityError, Path: "bundle",
			Message: fmt.Sprintf("不是合法 JSON: %v", err),
		}}
	}
	if _, ok := probe["agent_name"]; ok {
		return elaborateMinimal(raw, d)
	}
	if _, ok := probe["name"]; !ok {
		return nil, &DiagError{Diag: Diagnostic{
			Code: CodeBadJSON, Severity: SeverityError, Path: "bundle",
			Message: "既不是最小 spec（缺 agent_name）也不是完整 IR（缺 name）",
		}}
	}
	return elaborateFull(raw, d)
}

func elaborateMinimal(raw []byte, d Defaults) (*Bundle, error) {
	var m minimalSpec
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, &DiagError{Diag: Diagnostic{
			Code: CodeBadJSON, Severity: SeverityError, Path: "bundle",
			Message: fmt.Sprintf("最小 spec 解析失败: %v", err),
		}}
	}

	b := &Bundle{
		SchemaVersion: SchemaVersion,
		Name:          util.SnakeCase(m.AgentName),
		DisplayName:   m.AgentName,
		Version:       "0.1.0",
		Lineage:       &Lineage{Mutation: "initial-generation"},
		Prompt:        Prompt{System: m.AgentDesign, Lang: d.Lang},
		Tools:         append([]string(nil), m.ToolsUsed...),
		Policy: Policy{
			EffectsRequired: deriveEffects(m.ToolsUsed),
			Deny:            append([]string(nil), d.Deny...),
			WorkspaceScoped: true,
		},
		Runtime: Runtime{
			MaxSteps:                 d.MaxSteps,
			StopOnFinish:             d.StopOnFinish,
			LLMTimeout:               d.LLMTimeout,
			LLMTimeoutText:           d.LLMTimeout.String(),
			Tier:                     d.Tier,
			Compression:              CompressionOptions{Enable: d.CompressionEnabled, KeepToolResultTokens: d.KeepToolResultTokens},
			MaxParallelReadOnlyTools: d.MaxParallelReadOnlyTools,
		},
		Contract:     Contract{MustExitWith: []string{"reason"}, ThoughtFormat: true},
		Quality:      Quality{Grade: GradeProvisional},
		TaskForAgent: m.TaskForAgent,
	}
	return b, nil
}

func elaborateFull(raw []byte, d Defaults) (*Bundle, error) {
	var b Bundle
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, &DiagError{Diag: Diagnostic{
			Code: CodeBadJSON, Severity: SeverityError, Path: "bundle",
			Message: fmt.Sprintf("完整 IR 解析失败: %v", err),
		}}
	}

	if b.Runtime.LLMTimeoutText != "" {
		dur, err := time.ParseDuration(b.Runtime.LLMTimeoutText)
		if err != nil {
			return nil, &DiagError{Diag: Diagnostic{
				Code: CodeLLMTimeout, Severity: SeverityError, Path: "bundle.runtime.llm_timeout",
				Message: fmt.Sprintf("%q 不是合法 duration", b.Runtime.LLMTimeoutText),
				Note:    `形如 "120s" / "2m"`,
			}}
		}
		b.Runtime.LLMTimeout = dur
	} else {
		b.Runtime.LLMTimeout = d.LLMTimeout
		b.Runtime.LLMTimeoutText = d.LLMTimeout.String()
	}

	// 零值补齐：完整 IR 可以省略旋钮，省略即用默认，而不是让 0 生效
	//（max_steps=0 会让循环一步都不跑）。
	if b.Runtime.MaxSteps == 0 {
		b.Runtime.MaxSteps = d.MaxSteps
	}
	if b.Runtime.MaxParallelReadOnlyTools == 0 {
		b.Runtime.MaxParallelReadOnlyTools = d.MaxParallelReadOnlyTools
	}
	if b.Runtime.Tier == "" {
		b.Runtime.Tier = d.Tier
	}
	if b.Prompt.Lang == "" {
		b.Prompt.Lang = d.Lang
	}
	if b.DisplayName == "" {
		b.DisplayName = b.Name
	}
	if len(b.Policy.EffectsRequired) == 0 {
		b.Policy.EffectsRequired = deriveEffects(b.Tools)
	}
	if b.Quality.Grade == "" {
		b.Quality.Grade = GradeProvisional
	}
	return &b, nil
}

// deriveEffects 从注册表派生能力名，而不是让生成侧自己填一份会漂的清单。
// 未注册的工具贡献 0（Resolve 阶段会点名它），这里不重复报错。
func deriveEffects(tools []string) []string {
	var union toolspec.Effect
	for _, name := range tools {
		union |= toolspec.EffectOf(name)
	}
	names := union.Names()
	sort.Strings(names)
	return names
}
