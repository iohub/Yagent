package plasma_test

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yagent/internal/plasma"
	"yagent/internal/toolspec"
)

// goldenUpdate 让维护者在确认变更合理后显式重写基线：
//
//	go test ./internal/plasma -run TestDiagnosticsGolden -update
var goldenUpdate = flag.Bool("update", false, "rewrite the plasma diagnostics golden file")

// corpusCase 是 golden 的一条用例。verify 为空时只跑"Elaborate → Compile"的老路径
// （输出必须逐字不变）；非空时在编译之后追加一次 5a 验证 —— 5xx 只在验证阶段产生，
// 不给 corpus 这条通道，TestCorpusCoversEveryCode 对新码就永远为假。
type corpusCase struct {
	name       string
	raw        string
	palette    toolspec.Effect
	escalation toolspec.Effect
	verify     []plasma.Fixture
}

// corpus 走完整路径（raw JSON → Elaborate → Compile），所以 golden 同时钉住扩充与判定。
// 顺序是契约的一部分：诊断按发射顺序渲染， corpus 顺序变了 golden 就该变。
var corpus = []corpusCase{
	{
		name: "clean-minimal-spec",
		raw:  minimalSpecJSON,
	},
	{
		name: "bad-json",
		raw:  `{oops`,
	},
	{
		name: "unknown-shape",
		raw:  `{"foo":1}`,
	},
	{
		name: "unsupported-schema-version",
		raw:  `{"schema_version":99,"name":"auditor","prompt":{"system":"s"},"tools":["read_file"]}`,
	},
	{
		name: "name-not-snake-case",
		raw:  `{"schema_version":1,"name":"Security Auditor","prompt":{"system":"s"},"tools":["read_file"]}`,
	},
	{
		name: "empty-system-prompt",
		raw:  `{"schema_version":1,"name":"auditor","prompt":{"system":""},"tools":["read_file"]}`,
	},
	{
		name: "empty-tools",
		raw:  `{"schema_version":1,"name":"auditor","prompt":{"system":"s"},"tools":[]}`,
	},
	{
		name: "hallucinated-tool-name",
		raw:  `{"schema_version":1,"name":"auditor","prompt":{"system":"s"},"tools":["read_filez","search_by_regex"]}`,
	},
	{
		name: "tool-outside-palette",
		raw:  `{"schema_version":1,"name":"auditor","prompt":{"system":"s"},"tools":["read_file","run_bash"]}`,
	},
	{
		name:    "no-exit-channel",
		raw:     `{"schema_version":1,"name":"auditor","prompt":{"system":"s"},"tools":["read_file"]}`,
		palette: testPalette &^ toolspec.EffectFlow,
	},
	{
		name: "max-steps-out-of-range",
		raw:  `{"schema_version":1,"name":"auditor","prompt":{"system":"s"},"tools":["read_file"],"runtime":{"max_steps":900}}`,
	},
	{
		name: "llm-timeout-out-of-range",
		raw:  `{"schema_version":1,"name":"auditor","prompt":{"system":"s"},"tools":["read_file"],"runtime":{"llm_timeout":"2h"}}`,
	},
	{
		name: "parallel-tools-out-of-range",
		raw:  `{"schema_version":1,"name":"auditor","prompt":{"system":"s"},"tools":["read_file"],"runtime":{"max_parallel_readonly_tools":9}}`,
	},
	{
		name:    "deny-conflict",
		raw:     `{"schema_version":1,"name":"auditor","prompt":{"system":"s"},"tools":["read_file","create_file"],"policy":{"deny":["fs_write"]}}`,
		palette: testPalette | toolspec.EffectFSWrite,
	},
	{
		name: "unknown-effect-name-in-deny",
		raw:  `{"schema_version":1,"name":"auditor","prompt":{"system":"s"},"tools":["read_file"],"policy":{"deny":["fs_wirte"]}}`,
	},
	{
		name:       "escalation-approved-still-accounted",
		raw:        `{"schema_version":1,"name":"auditor","prompt":{"system":"s"},"tools":["read_file","create_file"]}`,
		escalation: toolspec.EffectFSWrite,
	},
	{
		name: "consistency-no-exit-contract",
		raw:  `{"schema_version":1,"name":"auditor","prompt":{"system":"Use read_file and search_by_regex and find_function_caller."},"tools":["read_file","search_by_regex","find_function_caller"],"contract":{"thought_format":false}}`,
	},
	{
		name:       "consistency-param-mismatch",
		raw:        `{"schema_version":1,"name":"auditor","prompt":{"system":"Use ` + "`read_file`" + `, ` + "`search_by_regex`" + `, ` + "`find_function_caller`" + `, ` + "`search_replace_in_file`" + ` then ` + "`agent_exit`" + `.\nEdit with ` + "`search_replace_in_file`" + ` and ` + "`target_file`" + `."},"tools":["read_file","search_by_regex","find_function_caller","search_replace_in_file"],"contract":{"thought_format":false},"policy":{}}`,
		palette:    testPalette | toolspec.EffectFSWrite,
		escalation: toolspec.EffectFSWrite,
	},
	{
		name: "consistency-normal-prose-spared",
		raw:  `{"schema_version":1,"name":"auditor","prompt":{"system":"Use ` + "`read_file`" + `, ` + "`search_by_regex`" + `, ` + "`find_function_caller`" + ` then ` + "`agent_exit`" + `.\nInspect the ` + "`注入风险`" + ` carefully."},"tools":["read_file","search_by_regex","find_function_caller"],"contract":{"thought_format":false}}`,
	},
	{
		name: "consistency-thought-template-missing",
		raw:  `{"schema_version":1,"name":"auditor","prompt":{"system":"Use read_file, search_by_regex, find_function_caller then agent_exit."},"tools":["read_file","search_by_regex","find_function_caller"],"contract":{"thought_format":true}}`,
	},
	{
		name: "consistency-dead-selection",
		raw:  `{"schema_version":1,"name":"auditor","prompt":{"system":"Use read_file, search_by_regex, find_function_caller then agent_exit."},"tools":["read_file","search_by_regex","find_function_caller","list_dir"],"contract":{"thought_format":false}}`,
	},
	{
		name: "consistency-denied-mention",
		raw:  `{"schema_version":1,"name":"auditor","prompt":{"system":"Use read_file, search_by_regex, find_function_caller then agent_exit. You may use run_bash."},"tools":["read_file","search_by_regex","find_function_caller"],"contract":{"thought_format":false},"policy":{"deny":["shell"]}}`,
	},
	{
		name: "consistency-prompt-budget",
		raw:  `{"schema_version":1,"name":"auditor","prompt":{"system":"Use read_file, search_by_regex, find_function_caller then agent_exit. ` + strings.Repeat("audit carefully. ", 3000) + `"},"tools":["read_file","search_by_regex","find_function_caller"],"contract":{"thought_format":false}}`,
	},
	{
		name: "multiple-errors-in-one-pass",
		raw:  `{"schema_version":1,"name":"Bad Name","prompt":{"system":""},"tools":["read_filez","run_bash"],"runtime":{"max_steps":0,"llm_timeout":"3h"},"policy":{"deny":["nope"]}}`,
	},
	// 5xx 验证族：每条用一份干净 bundle + 一条作者夹具，由 corpusStubRunner 判失败。
	// 五个码各来一条 —— 只钉 "诊断文本渲染成什么样"，不在这里重复 5a 的语义判定
	//（那是 fixture_test.go 的活）。
	{
		name:   "verify-exit-contract",
		raw:    corpusVerifyRaw,
		verify: []plasma.Fixture{{ID: plasma.FixtureExitContract, Input: "corpus"}},
	},
	{
		name:   "verify-step-budget",
		raw:    corpusVerifyRaw,
		verify: []plasma.Fixture{{ID: plasma.FixtureStepBudget, Input: "corpus"}},
	},
	{
		name:   "verify-tool-containment",
		raw:    corpusVerifyRaw,
		verify: []plasma.Fixture{{ID: plasma.FixtureToolContainment, Input: "corpus"}},
	},
	{
		name:   "verify-palette-containment",
		raw:    corpusVerifyRaw,
		verify: []plasma.Fixture{{ID: plasma.FixturePaletteContainment, Input: "corpus"}},
	},
	{
		name:   "verify-unclassified-fixture",
		raw:    corpusVerifyRaw,
		verify: []plasma.Fixture{{ID: "author-made", Input: "corpus"}},
	},
}

// corpusVerifyRaw 是 5xx 用例的干净底稿：prompt 点名全部工具 + 带合法 <thought> 模板，
// 所以编译阶段零诊断，golden 段里只会有验证阶段那一条。
const corpusVerifyRaw = `{"schema_version":1,"name":"auditor",` +
	`"prompt":{"system":"You are a security auditor.\nUse ` + "`read_file`" + `, ` + "`search_by_regex`" + ` and ` + "`find_function_caller`" + `, then call ` + "`agent_exit`" + `.\n<thought>\ngoal: g\nreason: r\nplan:\n[>] a\n[ ] b\n</thought>"},` +
	`"tools":["read_file","search_by_regex","find_function_caller"],"contract":{"thought_format":true}}`

// corpusStubRunner 让 5xx 用例真的失败：它不执行任何东西，Reason 是一句固定的可读文本。
// 换它成真 runner 会让 golden 变成"跑一遍 5a 的快照"——那是夹具测试的职责，不是诊断基线。
type corpusStubRunner struct{}

func (corpusStubRunner) RunStructural(_ context.Context, _ *plasma.LinkPlan, f plasma.Fixture) (plasma.Outcome, error) {
	return plasma.Outcome{FixtureID: f.ID, Passed: false, Reason: "corpus stub: fixture forced to fail"}, nil
}

func (corpusStubRunner) RunBehavioral(_ context.Context, _ *plasma.LinkPlan, f plasma.Fixture) (plasma.Outcome, error) {
	return plasma.Outcome{FixtureID: f.ID, Passed: false, Reason: "corpus stub: behavioral not run"}, nil
}

// verifyOne 是 renderCorpus 与覆盖测试共用的唯一 5a 路径（两条路不同形就有一侧是假的）。
func verifyOne(tc corpusCase) (plasma.Outcomes, *plasma.Report, bool) {
	if len(tc.verify) == 0 {
		return nil, nil, false
	}
	bundle, err := plasma.Elaborate([]byte(tc.raw), plasma.DefaultDefaults())
	if err != nil {
		return nil, nil, false
	}
	pal := testPalette
	if tc.palette != 0 {
		pal = tc.palette
	}
	c := &plasma.Compiler{Palette: pal, EscalationApproved: tc.escalation}
	// 作者夹具：Compile 不会覆盖非空清单（ensureStructuralFixtures 只在为空时填）。
	bundle.Fixtures = &plasma.Fixtures{Structural: tc.verify}
	plan, err := c.Link(bundle, "corpus")
	if err != nil {
		return nil, nil, false
	}
	outs, rep := c.VerifyStructural(context.Background(), bundle, plan, corpusStubRunner{})
	return outs, rep, true
}

func renderCorpus() string {
	var b strings.Builder
	b.WriteString("# Plasma 诊断基线（raw JSON → Elaborate → Compile，-update 重写）\n")
	b.WriteString("# 顺序即契约：诊断按发射顺序渲染，不排序（排序会抹掉因果线索）。\n")
	for _, tc := range corpus {
		pal := testPalette
		if tc.palette != 0 {
			pal = tc.palette
		}
		fmt.Fprintf(&b, "\n### %s\n", tc.name)

		bundle, err := plasma.Elaborate([]byte(tc.raw), plasma.DefaultDefaults())
		if err != nil {
			fmt.Fprintf(&b, "elaborate: %v\n", err)
			continue
		}
		r := (&plasma.Compiler{Palette: pal, EscalationApproved: tc.escalation}).Compile(context.Background(), bundle)
		// 验证阶段的诊断并进同一份报告：5xx 与 0-3xx 一起进 golden，才看得出
		// "编译过了但夹具红了"这条真实状态。棘轮也在这里跑，grade 行因此是终值。
		if outs, vrep, ok := verifyOne(tc); ok {
			r.Diagnostics = append(r.Diagnostics, vrep.Diagnostics...)
			r.Grade = plasma.Ratchet(r, outs, nil, false)
		}
		if len(r.Diagnostics) == 0 {
			b.WriteString("(no diagnostics)\n")
		} else {
			b.WriteString(r.Text())
		}
		fmt.Fprintf(&b, "grade: %s\n", gradeOrNone(r.Grade))
	}
	return b.String()
}

func gradeOrNone(g plasma.Grade) string {
	if g == "" {
		return "(none)"
	}
	return string(g)
}

func TestDiagnosticsGolden(t *testing.T) {
	got := renderCorpus()
	path := filepath.Join("testdata", "diagnostics.golden")

	if *goldenUpdate {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Skipf("golden rewritten at %s", path)
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (%s): %v — 首次生成用 -update", path, err)
	}
	if string(want) != got {
		t.Errorf("诊断与 golden 基线不一致。确认这是签过字的变更后用 -update 重写。\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// corpus 必须真的覆盖到每个码：否则 golden 看着很长，实际有几条规则从来没被渲染过。
func TestCorpusCoversEveryCode(t *testing.T) {
	seen := map[string]bool{}
	for _, tc := range corpus {
		pal := testPalette
		if tc.palette != 0 {
			pal = tc.palette
		}
		bundle, err := plasma.Elaborate([]byte(tc.raw), plasma.DefaultDefaults())
		if err != nil {
			var de *plasma.DiagError
			if errors.As(err, &de) {
				seen[de.Diag.Code] = true
			} else {
				t.Errorf("%s: Elaborate 返回的错误不带结构化诊断: %v", tc.name, err)
			}
			continue
		}
		r := (&plasma.Compiler{Palette: pal, EscalationApproved: tc.escalation}).Compile(context.Background(), bundle)
		for _, d := range r.Diagnostics {
			seen[d.Code] = true
		}
		// 5xx 只在验证阶段产生，所以覆盖账目也必须走一次验证。
		if _, vrep, ok := verifyOne(tc); ok {
			for _, d := range vrep.Diagnostics {
				seen[d.Code] = true
			}
		}
	}
	for _, code := range []string{
		plasma.CodeBadJSON, plasma.CodeSchemaVersion, plasma.CodeName,
		plasma.CodePromptEmpty, plasma.CodeToolsEmpty, plasma.CodeUnknownTool,
		plasma.CodeOutsidePalette, plasma.CodeNoExit, plasma.CodeMaxSteps,
		plasma.CodeLLMTimeout, plasma.CodeParallelTools, plasma.CodeDenyConflict,
		plasma.CodeUnknownEffect, plasma.CodeEscalation,
		plasma.CodeNoExitContract, plasma.CodeParamMismatch, plasma.CodeThoughtContract,
		plasma.CodePromptBudget, plasma.CodeDeadSelection, plasma.CodeDeniedMention,
		plasma.CodeFixtureFailed, plasma.CodeFixtureExit, plasma.CodeFixtureStepBudget,
		plasma.CodeFixtureToolScope, plasma.CodeFixturePalette,
	} {
		if !seen[code] {
			t.Errorf("corpus 没有触发 %s —— 那条规则的诊断文本没被 golden 钉住", code)
		}
	}
}
