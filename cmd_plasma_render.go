package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"yagent/internal/llm"
	"yagent/internal/plasma"
)

// 本文件是 plasma 子命令的渲染层。分成 JSON 与人读两条路，但**判据只有一份**：
// JSON 是同一批字段的机器可读形态，不是另一套计算。人读模式也不许省略关键信息 ——
// 省略账（grade / 未执行的行为夹具 / 未升档的原因）必须写出来（硬约束：静默是缺陷）。

// plasmaOutcomeJSON / plasmaDiagnosticJSON 是跨语言形态。
// 不复用 plasma.Outcome / plasma.Diagnostic 的默认序列化：那两个类型的字段名是
// Go 导出名（FixtureID / Severity），而 CLI 的 --json 是给脚本与 Python 看的契约，
// 用 snake_case 与 manifest 保持一致；同时也避免为了 CLI 去改 P6 的既有类型。
type plasmaOutcomeJSON struct {
	FixtureID string `json:"fixture_id"`
	Passed    bool   `json:"passed"`
	Reason    string `json:"reason,omitempty"`
	Steps     int    `json:"steps"`
	TokensIn  int    `json:"tokens_in,omitempty"`
	TokensOut int    `json:"tokens_out,omitempty"`
	StopKind  string `json:"stop_kind,omitempty"`
}

type plasmaDiagnosticJSON struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Path     string `json:"path"`
	Message  string `json:"message"`
	Note     string `json:"note,omitempty"`
}

type plasmaCompileJSON struct {
	Bundle             string                 `json:"bundle"`
	Hash               string                 `json:"hash,omitempty"`
	Grade              string                 `json:"grade"`
	EscalationRequired bool                   `json:"escalation_required"`
	EscalationApproved bool                   `json:"escalation_approved"`
	Diagnostics        []plasmaDiagnosticJSON `json:"diagnostics,omitempty"`
	Structural         []plasmaOutcomeJSON    `json:"structural,omitempty"`
	Behavioral         []plasmaOutcomeJSON    `json:"behavioral,omitempty"`
	BehavioralAttempts int                    `json:"behavioral_attempted,omitempty"`
	// BehavioralRequested 让 --json 的读者也能区分"没请求"与"请求了但没有夹具"。
	BehavioralRequested bool `json:"behavioral_requested,omitempty"`
}

func toOutcomesJSON(in plasma.Outcomes) []plasmaOutcomeJSON {
	out := make([]plasmaOutcomeJSON, 0, len(in))
	for _, o := range in {
		out = append(out, plasmaOutcomeJSON{
			FixtureID: o.FixtureID, Passed: o.Passed, Reason: o.Reason,
			Steps: o.Steps, TokensIn: o.TokensIn, TokensOut: o.TokensOut, StopKind: o.StopKind,
		})
	}
	return out
}

func toDiagnosticsJSON(in []plasma.Diagnostic) []plasmaDiagnosticJSON {
	out := make([]plasmaDiagnosticJSON, 0, len(in))
	for _, d := range in {
		out = append(out, plasmaDiagnosticJSON{
			Code: d.Code, Severity: string(d.Severity), Path: d.Path, Message: d.Message, Note: d.Note,
		})
	}
	return out
}

func writeJSONValue(w io.Writer, v interface{}) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// renderPlasmaCompile 输出一次编译/单夹具的结果。
func renderPlasmaCompile(w io.Writer, res *plasmaCompileResult, asJSON bool) error {
	if res == nil {
		return nil
	}
	if asJSON {
		return writeJSONValue(w, plasmaCompileJSON{
			Bundle: res.Bundle, Hash: res.Hash, Grade: string(res.Grade),
			EscalationRequired: res.Escalation, EscalationApproved: res.Approved,
			Diagnostics:         toDiagnosticsJSON(res.Diagnostics),
			Structural:          toOutcomesJSON(res.Outcomes),
			Behavioral:          toOutcomesJSON(res.Behavioral),
			BehavioralAttempts:  res.BehavioralRuns,
			BehavioralRequested: res.BehavioralRequested,
		})
	}
	fmt.Fprintf(w, "bundle: %s\n", res.Bundle)
	if res.Hash != "" {
		fmt.Fprintf(w, "hash:   %s\n", res.Hash)
	}
	// 诊断文本只有一份实现（Report.Text），这里不另写渲染。
	if text := (&plasma.Report{Diagnostics: res.Diagnostics}).Text(); text != "" {
		fmt.Fprint(w, text)
	}
	if res.Escalation {
		fmt.Fprintf(w, "escalation: required (approved=%v)\n", res.Approved)
	}
	if len(res.Outcomes) > 0 {
		fmt.Fprintln(w, "structural:")
		for _, o := range res.Outcomes {
			fmt.Fprintf(w, "  %s  %s  steps=%d", o.FixtureID, passLabel(o.Passed), o.Steps)
			if o.StopKind != "" {
				fmt.Fprintf(w, "  stop=%s", o.StopKind)
			}
			fmt.Fprintln(w)
			if !o.Passed {
				fmt.Fprintf(w, "    reason: %s\n", o.Reason)
			}
		}
	}
	if res.BehavioralRuns > 0 {
		fmt.Fprintln(w, "behavioral:")
		for _, o := range res.Behavioral {
			fmt.Fprintf(w, "  %s  %s  %s\n", o.FixtureID, passLabel(o.Passed), o.Reason)
		}
	} else if res.BehavioralRequested {
		// 给了 --behavioral 却一条都没跑：原因是 bundle 里没有 behavior fixtures，
		// 不能说成"--behavioral 才跑"（用户明明给了）——两种"没跑"的原因不同，
		// 混成一句就是把"我请求了但它没跑"伪装成"我没请求"。
		fmt.Fprintln(w, "behavioral: 未执行（--behavioral 已给，但 bundle 里没有 behavior fixtures；5b 属 P11）")
	} else {
		// 如实写"没跑"：--behavioral 没给 = 5b 没有证据，不能让人误以为验过。
		fmt.Fprintln(w, "behavioral: 未执行（--behavioral 才跑；5b 属 P11）")
	}
	switch {
	case res.Grade != "":
		fmt.Fprintf(w, "grade:  %s\n", res.Grade)
	case res.SingleFixture:
		// 单条夹具不参与 Ratchet（它要四条都有才有意义）—— 如实说，
		// 别让 "(none)" 看起来像"这份 bundle 掉档了"。
		fmt.Fprintln(w, "grade:  n/a（只跑了单条夹具，未算 grade）")
	default:
		fmt.Fprintf(w, "grade:  (none)\n")
	}
	return nil
}

func passLabel(ok bool) string {
	if ok {
		return "PASS"
	}
	return "FAIL"
}

// renderPlasmaSample 输出引擎的原始响应。
func renderPlasmaSample(w io.Writer, model string, resp *llm.Response, asJSON bool) error {
	if asJSON {
		return writeJSONValue(w, resp)
	}
	if resp == nil {
		fmt.Fprintln(w, "model:  "+model)
		fmt.Fprintln(w, "response: (nil)")
		return nil
	}
	fmt.Fprintf(w, "model:   %s\n", model)
	fmt.Fprintf(w, "choices: %d\n", len(resp.Choices))
	if resp.Usage != nil {
		fmt.Fprintf(w, "usage:   prompt=%d completion=%d total=%d\n",
			resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens)
	}
	for i, c := range resp.Choices {
		fmt.Fprintf(w, "--- choice[%d] finish=%s tool_calls=%d\n", i, c.FinishReason, len(c.ToolCalls))
		if strings.TrimSpace(c.Content) != "" {
			fmt.Fprintln(w, c.Content)
		}
	}
	return nil
}

// renderPlasmaManifest 输出能力清单。
func renderPlasmaManifest(w io.Writer, m *plasma.Manifest, asJSON bool) error {
	if m == nil {
		return nil
	}
	if asJSON {
		return writeJSONValue(w, m)
	}
	fmt.Fprintf(w, "schema_version:  %d\n", m.SchemaVersion)
	fmt.Fprintf(w, "capability_lock: %s\n", m.CapabilityLock)
	fmt.Fprintf(w, "palette:         %s\n", strings.Join(m.Palette, ", "))
	fmt.Fprintf(w, "tools:           %d\n", len(m.Tools))
	fmt.Fprintf(w, "codes:           %d\n", len(m.Codes))
	d := m.Defaults
	fmt.Fprintf(w, "defaults:        lang=%s max_steps=%d stop_on_finish=%v llm_timeout=%s tier=%s\n",
		d.Lang, d.MaxSteps, d.StopOnFinish, d.LLMTimeout, d.Tier)
	fmt.Fprintf(w, "                 compression=%v keep_tool_result_tokens=%d parallel_readonly=%d deny=%s\n",
		d.CompressionEnabled, d.KeepToolResultTokens, d.MaxParallelReadOnlyTools, strings.Join(d.Deny, ","))
	return nil
}
