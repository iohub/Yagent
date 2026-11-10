package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"yagent/internal/config"
	"yagent/internal/llm"
	"yagent/internal/llm/scripted"
	"yagent/internal/plasma"
	"yagent/internal/toolspec"
)

// 本文件钉住 P11 在 CLI 层的三条结论：
//   1. `compile --behavioral` 且 5a+5b 全过 ⇒ grade=verified，并落盘；
//   2. 5b 红 ⇒ **停在 provisional** 且逐个点名失败夹具（不许静默升档）；
//   3. `evolve_enable=false` ⇒ evolve 子命令拒绝执行。
//
// 为什么需要这个文件而不是只靠 agents 侧的用例：5b 的判据在 runner、**升档在 Ratchet**、
// 落盘在 CLI。三处任何一处接错，agents 侧的用例都仍然是绿的。

// p11BehavioralIR 是一份**完整 IR**（不是最小 spec）：只有完整 IR 才携带 fixtures，
// 而最小 spec 的 minimalSpec 结构里没有这个字段（elaborate.go:45-51）。
//
// runtime.stop_on_finish=true 是硬要求：agent_exit 不是终止信号时，循环会把它当
// "又执行了一次工具"继续烧步数，5b 的 stop_kind 断言于是永远对不上。
func p11BehavioralIR(fixtureID, needle string) string {
	return `{"schema_version":1,"name":"p11_behavioral_agent",` +
		`"display_name":"P11 Behavioral Agent","version":"0.1.0",` +
		`"prompt":{"system":"You inspect code and then call agent_exit with reason and a handoff conclusion.","lang":"en"},` +
		`"tools":["read_file"],` +
		`"policy":{"workspace_scoped":true},` +
		`"runtime":{"max_steps":6,"stop_on_finish":true,"llm_timeout":"30s",` +
		`"compression":{"enable":false},"max_parallel_readonly_tools":2},` +
		`"contract":{"must_exit_with":["reason"],"thought_format":false},` +
		`"fixtures":{"provenance":{"author":"author-fixtures","lineage":"author-fixtures:p11"},` +
		`"behavioral":[{"id":"` + fixtureID + `","input":"check the code",` +
		`"expect":{"stop_kind":"agent_exit","deliverable_contains":["` + needle + `"]},` +
		`"rubric":["结论要有可核对的依据"]}]}}`
}

// p11StubEngine 把 5b 的引擎换成 scripted：真 provider 在单测里不可复现。
// 返回的 restore 必须在用例结束时调用（接缝是包级变量）。
func p11StubEngine(t *testing.T, body string) func() {
	t.Helper()
	engine := scripted.NewForIdentity("p11_behavioral_agent", []scripted.Step{
		scripted.TextAndToolCall(body, toolspec.ExitToolName, map[string]interface{}{
			"reason": "done",
			"handoff": map[string]interface{}{
				"conclusion": "p11 behavioral fixture completed",
				"status":     "ok",
			},
		}),
	})
	prev := plasmaEngineFor
	plasmaEngineFor = func(*config.Config) (llm.Engine, error) { return engine, nil }
	return func() { plasmaEngineFor = prev }
}

// 5b 全过 ⇒ grade=verified，且 store 里落了一份 verified 的 bundle。
func TestRunPlasmaCompileBehavioralVerified(t *testing.T) {
	p7Isolate(t)
	defer p11StubEngine(t, "分析完成：结论是这份 prompt 能收尾")()

	bundle := p7Write(t, "bundle.json", p11BehavioralIR("behavioral-exit", "结论"))

	var out bytes.Buffer
	if err := runPlasmaCompile(bundle, true, true, &out); err != nil {
		t.Fatalf("5a+5b 全过时 compile 不该失败: %v\n%s", err, out.String())
	}
	var got struct {
		Grade        string `json:"grade"`
		Behavioral   []struct {
			FixtureID string `json:"fixture_id"`
			Passed    bool   `json:"passed"`
			StopKind  string `json:"stop_kind"`
		} `json:"behavioral"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("compile --json 不是合法 JSON: %v\n%s", err, out.String())
	}
	if got.Grade != string(plasma.GradeVerified) {
		t.Errorf("grade = %q, want verified（5a+5b 全过 + 无越权）：\n%s", got.Grade, out.String())
	}
	if len(got.Behavioral) != 1 || !got.Behavioral[0].Passed {
		t.Fatalf("行为夹具应当跑过并通过: %+v", got.Behavioral)
	}
	if got.Behavioral[0].StopKind != plasma.FixtureStopKindAgentExit {
		t.Errorf("stop_kind = %q, want agent_exit", got.Behavioral[0].StopKind)
	}

	// 落盘：verified 必须写进 store —— 不落盘等于每次都要重跑一遍真 LLM。
	list := plasma.NewStore(config.PlasmaStoreDir()).List()
	if len(list) != 1 {
		t.Fatalf("store 里应有 1 份 bundle，得到 %d 份", len(list))
	}
	if list[0].Meta.Grade != plasma.GradeVerified {
		t.Errorf("落盘的 grade = %q, want verified", list[0].Meta.Grade)
	}
}

// 5b 红 ⇒ 停在 provisional 且**逐个点名**失败夹具。
func TestRunPlasmaCompileBehavioralStaysProvisional(t *testing.T) {
	p7Isolate(t)
	// 交付里没有那条必须出现的关键结论 ⇒ EvaluateExpect 判红。
	defer p11StubEngine(t, "分析完成：给出结论")()

	bundle := p7Write(t, "bundle.json", p11BehavioralIR("behavioral-must-fail", "必须出现的关键结论"))

	var out bytes.Buffer
	err := runPlasmaCompile(bundle, true, true, &out)
	if err == nil {
		t.Fatalf("5b 红必须让 compile 失败（否则 verified 是白送的）:\n%s", out.String())
	}
	if !strings.Contains(err.Error(), "behavioral-must-fail") {
		t.Errorf("失败信息要点名夹具 ID，得到: %v", err)
	}
	if !strings.Contains(err.Error(), "provisional") {
		t.Errorf("失败信息要说明 grade 停在哪，得到: %v", err)
	}

	var got struct {
		Grade      string `json:"grade"`
		Behavioral []struct {
			FixtureID string `json:"fixture_id"`
			Passed    bool   `json:"passed"`
			Reason    string `json:"reason"`
		} `json:"behavioral"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("compile --json 不是合法 JSON: %v\n%s", err, out.String())
	}
	if got.Grade != string(plasma.GradeProvisional) {
		t.Errorf("grade = %q, want provisional（5b 红不许升档）", got.Grade)
	}
	if len(got.Behavioral) != 1 || got.Behavioral[0].Passed {
		t.Fatalf("行为夹具应当跑过且判红: %+v", got.Behavioral)
	}
	if !strings.Contains(got.Behavioral[0].Reason, "必须出现的关键结论") {
		t.Errorf("失败原因要点名缺什么，得到: %q", got.Behavioral[0].Reason)
	}
}

// 进化默认关闭：evolve 必须立即拒绝（§6.8：它是唯一会在后台改系统行为的开关）。
func TestRunPlasmaEvolveRejectsWhenDisabled(t *testing.T) {
	p7Isolate(t)

	var out bytes.Buffer
	err := runPlasmaEvolve(plasmaEvolveOptions{}, &out)
	if err == nil {
		t.Fatal("evolve_enable=false 时 evolve 必须拒绝执行")
	}
	if !strings.Contains(err.Error(), "evolve_enable") {
		t.Errorf("拒绝理由要点名开关，得到: %v", err)
	}
	if !strings.Contains(err.Error(), "默认关闭") {
		t.Errorf("拒绝理由要说清\"默认关闭\"（不是用法错），得到: %v", err)
	}
}
