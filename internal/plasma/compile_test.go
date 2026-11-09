package plasma_test

import (
	"context"
	"sort"
	"testing"
	"time"

	"yagent/internal/plasma"
	"yagent/internal/toolspec"
)

// testPalette 是只读族 + 收尾 + 取回冷工具，形状与 P1 的 PlasmaPalette 一致。
// P0 刻意不在包里定义 PlasmaPalette：palette 由调用方注入，这里注入的就是测试画像。
var testPalette = toolspec.EffectFSRead | toolspec.EffectSearchRegex |
	toolspec.EffectCodeGraph | toolspec.EffectFlow | toolspec.EffectToolLoad

func cleanBundle() *plasma.Bundle {
	return &plasma.Bundle{
		SchemaVersion: plasma.SchemaVersion,
		Name:          "security_auditor",
		DisplayName:   "Security Auditor",
		Prompt:        plasma.Prompt{System: "You are a security auditor.", Lang: "Chinese"},
		Tools:         []string{"read_file", "search_by_regex", "find_function_caller"},
		Policy:        plasma.Policy{Deny: []string{"fs_write", "shell"}, WorkspaceScoped: true},
		Runtime: plasma.Runtime{
			MaxSteps: 15, StopOnFinish: true, LLMTimeout: 120 * time.Second,
			Tier: "balanced", MaxParallelReadOnlyTools: 4,
		},
		Contract: plasma.Contract{MustExitWith: []string{"reason"}, ThoughtFormat: true},
		Quality:  plasma.Quality{Grade: plasma.GradeProvisional},
	}
}

func codes(r *plasma.Report) []string {
	out := make([]string, 0, len(r.Diagnostics))
	for _, d := range r.Diagnostics {
		out = append(out, d.Code)
	}
	sort.Strings(out)
	return out
}

func equalCodes(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// 每条规则都必须能被触发，且**只**触发它自己 —— 后半句是防止规则互相误响
// （照 toolspec 的 TestValidate_ReportsEachRule + _CleanSpecReportsNothingForItself 取向）。
func TestCompile_ReportsEachRule(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*plasma.Bundle)
		palette toolspec.Effect
		want    []string
	}{
		{
			name:   "干净 bundle 一条都不报",
			mutate: func(*plasma.Bundle) {},
			want:   nil,
		},
		{
			name:   "schema_version 不认",
			mutate: func(b *plasma.Bundle) { b.SchemaVersion = 2 },
			want:   []string{plasma.CodeSchemaVersion},
		},
		{
			name:   "name 不是 snake_case",
			mutate: func(b *plasma.Bundle) { b.Name = "Security Auditor" },
			want:   []string{plasma.CodeName},
		},
		{
			name:   "name 为空",
			mutate: func(b *plasma.Bundle) { b.Name = "" },
			want:   []string{plasma.CodeName},
		},
		{
			name:   "system prompt 为空",
			mutate: func(b *plasma.Bundle) { b.Prompt.System = "" },
			want:   []string{plasma.CodePromptEmpty},
		},
		{
			name:   "tools 整份为空",
			mutate: func(b *plasma.Bundle) { b.Tools = nil },
			want:   []string{plasma.CodeToolsEmpty},
		},
		{
			name:   "幻觉工具名",
			mutate: func(b *plasma.Bundle) { b.Tools = []string{"read_filez"} },
			want:   []string{plasma.CodeUnknownTool},
		},
		{
			name:   "工具超出 palette",
			mutate: func(b *plasma.Bundle) { b.Tools = append(b.Tools, "run_bash") },
			want:   []string{plasma.CodeOutsidePalette, plasma.CodeEscalation},
		},
		{
			name:    "palette 不含 flow 就没有收尾通道",
			mutate:  func(*plasma.Bundle) {},
			palette: testPalette &^ toolspec.EffectFlow,
			want:    []string{plasma.CodeNoExit},
		},
		{
			name:   "max_steps 越界",
			mutate: func(b *plasma.Bundle) { b.Runtime.MaxSteps = 900 },
			want:   []string{plasma.CodeMaxSteps},
		},
		{
			name:   "max_steps 为零",
			mutate: func(b *plasma.Bundle) { b.Runtime.MaxSteps = 0 },
			want:   []string{plasma.CodeMaxSteps},
		},
		{
			name:   "llm_timeout 越界",
			mutate: func(b *plasma.Bundle) { b.Runtime.LLMTimeout = 2 * time.Hour },
			want:   []string{plasma.CodeLLMTimeout},
		},
		{
			name:   "并发上限越界",
			mutate: func(b *plasma.Bundle) { b.Runtime.MaxParallelReadOnlyTools = 9 },
			want:   []string{plasma.CodeParallelTools},
		},
		{
			name:    "选进来的工具与 deny 冲突",
			mutate:  func(b *plasma.Bundle) { b.Tools = append(b.Tools, "create_file") },
			palette: testPalette | toolspec.EffectFSWrite,
			want:    []string{plasma.CodeDenyConflict},
		},
		{
			name:   "deny 里的能力名拼错",
			mutate: func(b *plasma.Bundle) { b.Policy.Deny = []string{"fs_wirte"} },
			want:   []string{plasma.CodeUnknownEffect},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := cleanBundle()
			tc.mutate(b)
			pal := testPalette
			if tc.palette != 0 {
				pal = tc.palette
			}
			c := &plasma.Compiler{Palette: pal}
			r := c.Compile(context.Background(), b)
			got := codes(r)
			want := append([]string(nil), tc.want...)
			sort.Strings(want)
			if !equalCodes(got, want) {
				t.Errorf("诊断码 = %v, want %v\n%s", got, want, r.Text())
			}
			if len(want) == 0 && r.HasErrors() {
				t.Errorf("干净 bundle 不该有 error:\n%s", r.Text())
			}
			if len(want) > 0 && !r.HasErrors() {
				t.Errorf("期望 %v 但 HasErrors()=false（规则没响）", want)
			}
		})
	}
}

// 编译失败时 Grade 必须清空：留着一个"provisional"会让调用方以为可以装配。
func TestCompile_FailureClearsGrade(t *testing.T) {
	b := cleanBundle()
	b.Tools = []string{"read_filez"}
	r := (&plasma.Compiler{Palette: testPalette}).Compile(context.Background(), b)
	if !r.HasErrors() {
		t.Fatal("前置条件不成立：幻觉工具名必须报错")
	}
	if r.Grade != "" {
		t.Errorf("编译失败后 Grade = %q, want 空", r.Grade)
	}
}

func TestCompile_CleanBundleIsProvisional(t *testing.T) {
	r := (&plasma.Compiler{Palette: testPalette}).Compile(context.Background(), cleanBundle())
	if r.HasErrors() {
		t.Fatalf("干净 bundle 报错:\n%s", r.Text())
	}
	if r.Grade != plasma.GradeProvisional {
		t.Errorf("Grade = %q, want provisional（P0 没有 5a/5b，不可能更高）", r.Grade)
	}
}

// palette 忘配时应当拒掉一切，而不是放行一切。
func TestCompile_ZeroPaletteRejectsEverything(t *testing.T) {
	r := (&plasma.Compiler{}).Compile(context.Background(), cleanBundle())
	if !r.HasErrors() {
		t.Fatal("零 palette 必须拒掉工具，否则忘配等于全放行")
	}
	if !equalCodes(codes(r), []string{plasma.CodeOutsidePalette, plasma.CodeOutsidePalette, plasma.CodeOutsidePalette, plasma.CodeNoExit, plasma.CodeEscalation}) {
		t.Errorf("零 palette 的诊断 = %v\n%s", codes(r), r.Text())
	}
}

func TestCompile_NilBundle(t *testing.T) {
	r := (&plasma.Compiler{Palette: testPalette}).Compile(context.Background(), nil)
	if !r.HasErrors() {
		t.Fatal("nil bundle 必须报错而不是 panic")
	}
}

// agent_exit 由 MatchNames 自动追加，生成侧不需要显式写它。
func TestCompile_ExitIsAutoAppended(t *testing.T) {
	b := cleanBundle()
	for _, tool := range b.Tools {
		if tool == toolspec.ExitToolName {
			t.Fatal("前置条件不成立：夹具不该显式含 agent_exit")
		}
	}
	r := (&plasma.Compiler{Palette: testPalette}).Compile(context.Background(), b)
	if r.HasErrors() {
		t.Fatalf("没显式写 agent_exit 也该通过（MatchNames 自动追加）:\n%s", r.Text())
	}
}

// did-you-mean 必须指向真实存在的工具，且并列时确定（不依赖 map 迭代）。
func TestCompile_UnknownToolSuggestion(t *testing.T) {
	b := cleanBundle()
	b.Tools = []string{"read_filez"}
	r := (&plasma.Compiler{Palette: testPalette}).Compile(context.Background(), b)
	var note string
	for _, d := range r.Diagnostics {
		if d.Code == plasma.CodeUnknownTool {
			note = d.Note
		}
	}
	if note != `did you mean "read_file"?` {
		t.Errorf("note = %q, want 指向 read_file", note)
	}
	if _, ok := toolspec.Get("read_file"); !ok {
		t.Error("建议的工具不在注册表里")
	}
	// 同一输入跑两次必须逐字相同（前缀缓存与 golden 都靠它）。
	again := (&plasma.Compiler{Palette: testPalette}).Compile(context.Background(), b)
	if again.Text() != r.Text() {
		t.Error("两次编译的诊断文本不一致")
	}
}

// 诊断必须指向原始 tools 的下标，而不是 MatchNames 去重/追加后的下标。
func TestCompile_DiagnosticPointsAtOriginalIndex(t *testing.T) {
	b := cleanBundle()
	b.Tools = []string{"read_file", "read_file", "totally_bogus_name"}
	r := (&plasma.Compiler{Palette: testPalette}).Compile(context.Background(), b)
	var path string
	for _, d := range r.Diagnostics {
		if d.Code == plasma.CodeUnknownTool {
			path = d.Path
		}
	}
	if path != "bundle.tools[2]" {
		t.Errorf("path = %q, want bundle.tools[2]（重复项去重后下标会错位）", path)
	}
}
