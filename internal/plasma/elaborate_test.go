package plasma_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"yagent/internal/plasma"
)

const minimalSpecJSON = `{
  "thinking": "需要只读审计能力",
  "agent_name": "Security Auditor",
  "agent_design": "You are a security auditor. Use ` + "`read_file`" + ` and ` + "`search_by_regex`" + ` to inspect code. When done call ` + "`agent_exit`" + ` with reason and a handoff conclusion.\n<thought>\ngoal: 审计注入风险\nreason: 先读代码再判断\nplan:\n[>] 读取文件\n[ ] 报告发现\n</thought>",
  "tools_used": ["read_file", "search_by_regex"],
  "task_for_agent": "审计 internal/foo 的注入风险"
}`

func TestElaborate_MinimalSpec(t *testing.T) {
	b, err := plasma.Elaborate([]byte(minimalSpecJSON), plasma.DefaultDefaults())
	if err != nil {
		t.Fatalf("Elaborate: %v", err)
	}
	if b.Name != "security_auditor" {
		t.Errorf("Name = %q, want security_auditor（展示名要归一化成工具名）", b.Name)
	}
	if b.DisplayName != "Security Auditor" {
		t.Errorf("DisplayName = %q, want 原样保留", b.DisplayName)
	}
	if b.SchemaVersion != plasma.SchemaVersion {
		t.Errorf("SchemaVersion = %d", b.SchemaVersion)
	}
	if b.Prompt.System == "" || b.Prompt.Lang != "Chinese" {
		t.Errorf("Prompt = %+v", b.Prompt)
	}
	if b.TaskForAgent != "审计 internal/foo 的注入风险" {
		t.Errorf("TaskForAgent = %q（要交给执行路径）", b.TaskForAgent)
	}
	if b.Quality.Grade != plasma.GradeProvisional {
		t.Errorf("Grade = %q, want provisional", b.Quality.Grade)
	}
	if b.Contract.ThoughtFormat != true || len(b.Contract.MustExitWith) != 1 || b.Contract.MustExitWith[0] != "reason" {
		t.Errorf("Contract = %+v, want thought_format=true 且 must_exit_with=[reason]（agent_exit schema 的 required）", b.Contract)
	}
	if !b.Policy.WorkspaceScoped {
		t.Error("WorkspaceScoped 应为 true")
	}
}

// 能力清单从注册表派生，不让生成侧自己填一份会漂的清单。
func TestElaborate_DerivesEffectsFromRegistry(t *testing.T) {
	b, err := plasma.Elaborate([]byte(minimalSpecJSON), plasma.DefaultDefaults())
	if err != nil {
		t.Fatalf("Elaborate: %v", err)
	}
	want := []string{"fs_read", "search_regex"}
	if len(b.Policy.EffectsRequired) != len(want) {
		t.Fatalf("EffectsRequired = %v, want %v", b.Policy.EffectsRequired, want)
	}
	for i := range want {
		if b.Policy.EffectsRequired[i] != want[i] {
			t.Errorf("EffectsRequired[%d] = %q, want %q", i, b.Policy.EffectsRequired[i], want[i])
		}
	}
}

// DefaultDefaults 的每个值都必须真的被用到（坑 35：文档写一个数、六处手写另一个数，
// 那个文档里的数从来没生效过）。
func TestElaborate_DefaultsAreActuallyUsed(t *testing.T) {
	d := plasma.DefaultDefaults()
	b, err := plasma.Elaborate([]byte(minimalSpecJSON), d)
	if err != nil {
		t.Fatalf("Elaborate: %v", err)
	}
	if b.Runtime.MaxSteps != d.MaxSteps {
		t.Errorf("MaxSteps = %d, want DefaultDefaults 的 %d", b.Runtime.MaxSteps, d.MaxSteps)
	}
	if b.Runtime.StopOnFinish != d.StopOnFinish {
		t.Errorf("StopOnFinish = %v, want %v", b.Runtime.StopOnFinish, d.StopOnFinish)
	}
	if b.Runtime.LLMTimeout != d.LLMTimeout {
		t.Errorf("LLMTimeout = %v, want %v", b.Runtime.LLMTimeout, d.LLMTimeout)
	}
	if b.Runtime.Tier != d.Tier {
		t.Errorf("Tier = %q, want %q", b.Runtime.Tier, d.Tier)
	}
	if b.Runtime.MaxParallelReadOnlyTools != d.MaxParallelReadOnlyTools {
		t.Errorf("MaxParallelReadOnlyTools = %d, want %d", b.Runtime.MaxParallelReadOnlyTools, d.MaxParallelReadOnlyTools)
	}
	if b.Runtime.Compression.Enable != d.CompressionEnabled {
		t.Errorf("Compression.Enable = %v, want %v", b.Runtime.Compression.Enable, d.CompressionEnabled)
	}
	if b.Runtime.Compression.KeepToolResultTokens != d.KeepToolResultTokens {
		t.Errorf("KeepToolResultTokens = %d, want %d", b.Runtime.Compression.KeepToolResultTokens, d.KeepToolResultTokens)
	}
	if b.Prompt.Lang != d.Lang {
		t.Errorf("Lang = %q, want %q", b.Prompt.Lang, d.Lang)
	}
	if len(b.Policy.Deny) != len(d.Deny) {
		t.Errorf("Deny = %v, want %v", b.Policy.Deny, d.Deny)
	}
	// 覆盖值必须生效，否则"注入 Defaults"这条通道是装饰。
	custom := d
	custom.MaxSteps = 42
	custom.Lang = "English"
	b2, err := plasma.Elaborate([]byte(minimalSpecJSON), custom)
	if err != nil {
		t.Fatalf("Elaborate(custom): %v", err)
	}
	if b2.Runtime.MaxSteps != 42 || b2.Prompt.Lang != "English" {
		t.Errorf("注入的 Defaults 没生效: MaxSteps=%d Lang=%q", b2.Runtime.MaxSteps, b2.Prompt.Lang)
	}
}

func TestElaborate_FullIRNormalizesZeroKnobs(t *testing.T) {
	raw := `{
      "schema_version": 1, "name": "auditor",
      "prompt": {"system": "audit"},
      "tools": ["read_file"],
      "runtime": {"stop_on_finish": true}
    }`
	b, err := plasma.Elaborate([]byte(raw), plasma.DefaultDefaults())
	if err != nil {
		t.Fatalf("Elaborate: %v", err)
	}
	d := plasma.DefaultDefaults()
	if b.Runtime.MaxSteps != d.MaxSteps {
		t.Errorf("省略 max_steps 应补默认 %d，得到 %d（0 会让循环一步都不跑）", d.MaxSteps, b.Runtime.MaxSteps)
	}
	if b.Runtime.MaxParallelReadOnlyTools != d.MaxParallelReadOnlyTools {
		t.Errorf("省略并发数应补默认，得到 %d", b.Runtime.MaxParallelReadOnlyTools)
	}
	if b.Runtime.LLMTimeout != d.LLMTimeout {
		t.Errorf("省略 llm_timeout 应补默认，得到 %v", b.Runtime.LLMTimeout)
	}
	if b.DisplayName != "auditor" {
		t.Errorf("省略 display_name 应回落 name，得到 %q", b.DisplayName)
	}
	if b.Quality.Grade != plasma.GradeProvisional {
		t.Errorf("省略 grade 应回落 provisional，得到 %q", b.Quality.Grade)
	}
	if len(b.Policy.EffectsRequired) != 1 || b.Policy.EffectsRequired[0] != "fs_read" {
		t.Errorf("省略 effects_required 应从注册表派生，得到 %v", b.Policy.EffectsRequired)
	}
}

func TestElaborate_FullIRParsesDuration(t *testing.T) {
	raw := `{"schema_version":1,"name":"a","prompt":{"system":"s"},"tools":["read_file"],
             "runtime":{"max_steps":5,"llm_timeout":"90s","max_parallel_readonly_tools":2}}`
	b, err := plasma.Elaborate([]byte(raw), plasma.DefaultDefaults())
	if err != nil {
		t.Fatalf("Elaborate: %v", err)
	}
	if b.Runtime.LLMTimeout != 90*time.Second {
		t.Errorf("LLMTimeout = %v, want 90s", b.Runtime.LLMTimeout)
	}
	if b.Runtime.MaxSteps != 5 {
		t.Errorf("显式 max_steps 被默认值覆盖了: %d", b.Runtime.MaxSteps)
	}
}

func TestElaborate_RejectsBadShapes(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		code string
	}{
		{"不是 JSON", `{oops`, plasma.CodeBadJSON},
		{"既不是最小 spec 也不是完整 IR", `{"foo":1}`, plasma.CodeBadJSON},
		{"最小 spec 字段类型错", `{"agent_name":"x","tools_used":"not-an-array"}`, plasma.CodeBadJSON},
		{"完整 IR 的 duration 非法", `{"schema_version":1,"name":"a","prompt":{"system":"s"},"tools":["read_file"],"runtime":{"llm_timeout":"soon"}}`, plasma.CodeLLMTimeout},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := plasma.Elaborate([]byte(tc.raw), plasma.DefaultDefaults())
			if err == nil {
				t.Fatal("期望报错，得到 nil（规则没响）")
			}
			var de *plasma.DiagError
			if !errors.As(err, &de) {
				t.Fatalf("错误不是 *DiagError，调用方拿不到结构化诊断: %v", err)
			}
			if de.Diag.Code != tc.code {
				t.Errorf("code = %q, want %q", de.Diag.Code, tc.code)
			}
			if de.Diag.Severity != plasma.SeverityError {
				t.Errorf("severity = %q, want error", de.Diag.Severity)
			}
		})
	}
}

// TaskForAgent 与 thinking 不进落盘 IR：前者是"这次要干什么"，后者是设计过程。
func TestElaborate_TaskIsNotPersisted(t *testing.T) {
	b, err := plasma.Elaborate([]byte(minimalSpecJSON), plasma.DefaultDefaults())
	if err != nil {
		t.Fatalf("Elaborate: %v", err)
	}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := probe["task_for_agent"]; ok {
		t.Error("task_for_agent 落进了 IR（它属于本次执行，不属于 Agent 声明）")
	}
	if _, ok := probe["thinking"]; ok {
		t.Error("thinking 落进了 IR")
	}
}

// 扩充必须幂等：落盘后再读回来不得产生第二份形状（否则每次读写都产出一整片 diff）。
func TestElaborate_Idempotent(t *testing.T) {
	b, err := plasma.Elaborate([]byte(minimalSpecJSON), plasma.DefaultDefaults())
	if err != nil {
		t.Fatalf("Elaborate: %v", err)
	}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	b2, err := plasma.Elaborate(raw, plasma.DefaultDefaults())
	if err != nil {
		t.Fatalf("二次 Elaborate: %v", err)
	}
	raw2, err := json.Marshal(b2)
	if err != nil {
		t.Fatalf("二次 Marshal: %v", err)
	}
	if string(raw) != string(raw2) {
		t.Errorf("二次扩充不幂等:\n--- first ---\n%s\n--- second ---\n%s", raw, raw2)
	}
}
