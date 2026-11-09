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

// corpus 走完整路径（raw JSON → Elaborate → Compile），所以 golden 同时钉住扩充与判定。
// 顺序是契约的一部分：诊断按发射顺序渲染， corpus 顺序变了 golden 就该变。
var corpus = []struct {
	name       string
	raw        string
	palette    toolspec.Effect
	escalation toolspec.Effect
}{
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
		name: "multiple-errors-in-one-pass",
		raw:  `{"schema_version":1,"name":"Bad Name","prompt":{"system":""},"tools":["read_filez","run_bash"],"runtime":{"max_steps":0,"llm_timeout":"3h"},"policy":{"deny":["nope"]}}`,
	},
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
	}
	for _, code := range []string{
		plasma.CodeBadJSON, plasma.CodeSchemaVersion, plasma.CodeName,
		plasma.CodePromptEmpty, plasma.CodeToolsEmpty, plasma.CodeUnknownTool,
		plasma.CodeOutsidePalette, plasma.CodeNoExit, plasma.CodeMaxSteps,
		plasma.CodeLLMTimeout, plasma.CodeParallelTools, plasma.CodeDenyConflict,
		plasma.CodeUnknownEffect, plasma.CodeEscalation,
	} {
		if !seen[code] {
			t.Errorf("corpus 没有触发 %s —— 那条规则的诊断文本没被 golden 钉住", code)
		}
	}
}
