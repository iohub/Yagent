package plasma_test

import (
	"context"
	"strings"
	"testing"

	"yagent/internal/plasma"
	"yagent/internal/toolspec"
)

func hasWarn(r *plasma.Report, code string) bool {
	for _, d := range r.Warnings() {
		if d.Code == code {
			return true
		}
	}
	return false
}

// 阶段 4 全是 warning：它指出"会静默坏掉"的形状，但不阻断装配。
func TestConsistency_WarningsDoNotBlock(t *testing.T) {
	b := cleanBundle()
	b.Prompt.System = "You are an auditor." // 缺 agent_exit / 缺 thought / 三个工具都没提及
	b.Contract.ThoughtFormat = false
	r := (&plasma.Compiler{Palette: testPalette}).Compile(context.Background(), b)
	if r.HasErrors() {
		t.Fatalf("warning 不该阻断装配:\n%s", r.Text())
	}
	if r.Grade != plasma.GradeProvisional {
		t.Errorf("只有 warning 时 grade 应仍是 provisional，得到 %q", r.Grade)
	}
	if len(r.Warnings()) == 0 {
		t.Error("期望有 warning，得到 0（规则没响）")
	}
}

func TestConsistency_ExitContract(t *testing.T) {
	b := cleanBundle()
	b.Prompt.System = strings.ReplaceAll(goodPrompt, "`agent_exit`", "the exit tool")
	r := (&plasma.Compiler{Palette: testPalette}).Compile(context.Background(), b)
	if !hasWarn(r, plasma.CodeNoExitContract) {
		t.Errorf("提示不提 agent_exit 应报 PLS-301:\n%s", r.Text())
	}
	// 合规提示不该报。
	clean := (&plasma.Compiler{Palette: testPalette}).Compile(context.Background(), cleanBundle())
	if hasWarn(clean, plasma.CodeNoExitContract) {
		t.Errorf("合规提示误报 PLS-301:\n%s", clean.Text())
	}
}

// PLS-302 抓坑 38 的形状：提示让模型传一个该工具 schema 里没有的键，调用会静默丢掉它。
// 实测参数名：read_file/create_file 用 target_file，search_replace_in_file 用 file_path。
func TestConsistency_ParamMismatch(t *testing.T) {
	b := cleanBundle()
	b.Tools = append(b.Tools, "search_replace_in_file")
	b.Policy.Deny = nil
	b.Prompt.System = goodPrompt + "\nUse `search_replace_in_file` with `target_file` to edit."
	r := (&plasma.Compiler{
		Palette:            testPalette | toolspec.EffectFSWrite,
		EscalationApproved: toolspec.EffectFSWrite,
	}).Compile(context.Background(), b)
	if !hasWarn(r, plasma.CodeParamMismatch) {
		t.Errorf("target_file 不是 search_replace_in_file 的参数，应报 PLS-302:\n%s", r.Text())
	}
}

// 坑 42 的纪律：按形状识别的校验，夹具里必须放一条**与违规串同形的正常散文**。
// 这一行同样有"反引号工具名 + 反引号 token"，但那个 token 不是任何工具的参数名 ⇒ 必须放过。
func TestConsistency_ParamMismatchSparesNormalProse(t *testing.T) {
	b := cleanBundle()
	b.Prompt.System = goodPrompt + "\nUse `read_file` to inspect the `注入风险` in that module."
	r := (&plasma.Compiler{Palette: testPalette}).Compile(context.Background(), b)
	if hasWarn(r, plasma.CodeParamMismatch) {
		t.Errorf("同形正常散文被误判为参数错配:\n%s", r.Text())
	}
}

// 正确的参数名不该报。
func TestConsistency_CorrectParamNameIsQuiet(t *testing.T) {
	b := cleanBundle()
	b.Prompt.System = goodPrompt + "\nUse `read_file` with `target_file` set to the path."
	r := (&plasma.Compiler{Palette: testPalette}).Compile(context.Background(), b)
	if hasWarn(r, plasma.CodeParamMismatch) {
		t.Errorf("target_file 确实是 read_file 的参数，不该报:\n%s", r.Text())
	}
}

func TestConsistency_ThoughtContract(t *testing.T) {
	// 没有模板
	b := cleanBundle()
	b.Prompt.System = "You are an auditor. Use `read_file`, `search_by_regex`, `find_function_caller`, then `agent_exit`."
	r := (&plasma.Compiler{Palette: testPalette}).Compile(context.Background(), b)
	if !hasWarn(r, plasma.CodeThoughtContract) {
		t.Errorf("thought_format=true 但无模板，应报 PLS-303:\n%s", r.Text())
	}
	// 模板缺 goal
	b2 := cleanBundle()
	b2.Prompt.System = goodPrompt + "\n<thought>\nreason: 只有理由没有目标\n</thought>"
	r2 := (&plasma.Compiler{Palette: testPalette}).Compile(context.Background(), b2)
	if !hasWarn(r2, plasma.CodeThoughtContract) {
		t.Errorf("模板缺 goal 应报 PLS-303（复用 thinklink.Validate）:\n%s", r2.Text())
	}
	// 关掉契约就不该报
	b3 := cleanBundle()
	b3.Contract.ThoughtFormat = false
	b3.Prompt.System = "You are an auditor. Use `read_file`, `search_by_regex`, `find_function_caller`, then `agent_exit`."
	r3 := (&plasma.Compiler{Palette: testPalette}).Compile(context.Background(), b3)
	if hasWarn(r3, plasma.CodeThoughtContract) {
		t.Errorf("thought_format=false 时不该报 PLS-303:\n%s", r3.Text())
	}
}

func TestConsistency_PromptBudget(t *testing.T) {
	b := cleanBundle()
	b.Prompt.System = goodPrompt + "\n" + strings.Repeat("audit the injection risk carefully. ", 4000)
	r := (&plasma.Compiler{Palette: testPalette}).Compile(context.Background(), b)
	if !hasWarn(r, plasma.CodePromptBudget) {
		t.Errorf("超长提示应报 PLS-304:\n%s", r.Text())
	}
	var note string
	for _, d := range r.Warnings() {
		if d.Code == plasma.CodePromptBudget {
			note = d.Note
		}
	}
	if !strings.Contains(note, "预算") {
		t.Errorf("note 应说明预算，得到 %q", note)
	}
}

func TestConsistency_DeadSelection(t *testing.T) {
	b := cleanBundle()
	b.Tools = append(b.Tools, "list_dir") // 提示里没提及
	r := (&plasma.Compiler{Palette: testPalette}).Compile(context.Background(), b)
	if !hasWarn(r, plasma.CodeDeadSelection) {
		t.Errorf("声明但未提及的工具应报 PLS-306:\n%s", r.Text())
	}
	// 诊断必须指向原始下标
	for _, d := range r.Warnings() {
		if d.Code == plasma.CodeDeadSelection && d.Path != "bundle.tools[3]" {
			t.Errorf("path = %q, want bundle.tools[3]", d.Path)
		}
	}
}

func TestConsistency_DeniedMention(t *testing.T) {
	b := cleanBundle()
	b.Prompt.System = goodPrompt + "\nYou may also use run_bash if needed."
	r := (&plasma.Compiler{Palette: testPalette}).Compile(context.Background(), b)
	if !hasWarn(r, plasma.CodeDeniedMention) {
		t.Errorf("提示点名了被 deny 的 run_bash，应报 PLS-307:\n%s", r.Text())
	}
}

// deny 名字拼错时由 PLS-207 管，PLS-307 不该跟着重复报（一个事实两条诊断是噪声）。
func TestConsistency_DeniedMentionSkipsUnknownEffect(t *testing.T) {
	b := cleanBundle()
	b.Policy.Deny = []string{"fs_wirte"}
	b.Prompt.System = goodPrompt + "\nYou may also use run_bash."
	r := (&plasma.Compiler{Palette: testPalette}).Compile(context.Background(), b)
	if hasWarn(r, plasma.CodeDeniedMention) {
		t.Errorf("deny 名字无效时不该报 PLS-307:\n%s", r.Text())
	}
	// PLS-207 是 error 档，不在 Warnings() 里；确认它确实报了，且只由它负责这个事实。
	found := false
	for _, d := range r.Errors() {
		if d.Code == plasma.CodeUnknownEffect {
			found = true
		}
	}
	if !found {
		t.Errorf("拼错的 deny 名字应由 PLS-207 报出来:\n%s", r.Text())
	}
}

func TestConsistency_WarningsAccessor(t *testing.T) {
	b := cleanBundle()
	b.Prompt.System = "short."
	b.Contract.ThoughtFormat = false
	r := (&plasma.Compiler{Palette: testPalette}).Compile(context.Background(), b)
	ws := r.Warnings()
	if len(ws) == 0 {
		t.Fatal("前置条件不成立：这份 bundle 应该有 warning")
	}
	for _, d := range ws {
		if d.Severity != plasma.SeverityWarning {
			t.Errorf("Warnings() 混进了 error: %+v", d)
		}
	}
	if len(r.Errors()) != 0 {
		t.Errorf("这份 bundle 不该有 error:\n%s", r.Text())
	}
}
