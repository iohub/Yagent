package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yagent/internal/plasma"
)

// CLI 冒烟走"直接调执行体"这条路（任务 §四.4）：命令层只做 flag 映射与退出码，
// 逻辑全在 runPlasmaXxx 里。真二进制还要单独跑一次（§五.3）—— 两条路验的
// 不是同一件事：这里验逻辑，那里验"生产二进制里注册表真的被 populate 了"。
//
// 每个用例都 t.Setenv("HOME", t.TempDir())：plasma 的 store 根来自 ~/.yagent/agents，
// 不拥有自己的目录就会读到开发机上的真实 store（坑 23）。

// p7SpecJSON 是一份合法的最小 spec：只读、单工具、能收尾。
// 走 Elaborate 的最小 spec 通道，因此也顺带覆盖"defaults 从配置派生"那条路。
const p7SpecJSON = `{"agent_name":"P7 Smoke Auditor",` +
	`"agent_design":"You are a smoke auditor. Use ` + "`read_file`" + ` to inspect code, ` +
	`then call ` + "`agent_exit`" + ` with reason and a handoff conclusion.\n` +
	`<thought>\ngoal: smoke\nreason: cli test\nplan:\n[>] inspect\n[ ] exit\n</thought>",` +
	`"tools_used":["read_file"],"task_for_agent":"run the cli smoke test"}`

// p7BadToolJSON 的 tools_used 里有一个注册表里不存在的工具 ⇒ PLS-101。
const p7BadToolJSON = `{"agent_name":"P7 Bad Tool",` +
	`"agent_design":"You inspect things and then call agent_exit.","tools_used":["read_filez"],` +
	`"task_for_agent":"x"}`

func p7Write(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func p7Isolate(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("YAGENT_PLASMA", "")
}

// 合法 bundle：编译通过、四条结构夹具全绿、grade 到 provisional。
func TestRunPlasmaCompileSmoke(t *testing.T) {
	p7Isolate(t)
	bundle := p7Write(t, "bundle.json", p7SpecJSON)

	var out bytes.Buffer
	if err := runPlasmaCompile(bundle, false, false, &out); err != nil {
		t.Fatalf("compile 失败: %v\n--- output ---\n%s", err, out.String())
	}
	body := out.String()
	for _, id := range []string{
		plasma.FixtureExitContract, plasma.FixtureStepBudget,
		plasma.FixtureToolContainment, plasma.FixturePaletteContainment,
	} {
		if !strings.Contains(body, id) {
			t.Errorf("输出里没有夹具 %q：\n%s", id, body)
		}
	}
	if strings.Contains(body, "FAIL") {
		t.Errorf("四条结构夹具应当全绿：\n%s", body)
	}
	if !strings.Contains(body, "grade:  provisional") {
		t.Errorf("grade 应为 provisional（5b 未跑）：\n%s", body)
	}
	// 行为夹具必须如实写成"未执行"，不许静默省略、更不许谎报通过。
	if !strings.Contains(body, "behavioral: 未执行") {
		t.Errorf("没有如实写出行为夹具未执行：\n%s", body)
	}
}

// 给了 --behavioral 但 bundle 里没有 behavior fixtures：原因是"没有夹具"，
// 不是"用户没请求"——两种"未执行"必须能区分开（否则这是另一种静默）。
func TestRunPlasmaCompileBehavioralWithoutFixtures(t *testing.T) {
	p7Isolate(t)
	bundle := p7Write(t, "bundle.json", p7SpecJSON)

	var out bytes.Buffer
	if err := runPlasmaCompile(bundle, true, false, &out); err != nil {
		t.Fatalf("compile --behavioral 失败: %v\n%s", err, out.String())
	}
	body := out.String()
	if strings.Contains(body, "--behavioral 才跑") {
		t.Errorf("给了 --behavioral，输出却仍说\"--behavioral 才跑\"：\n%s", body)
	}
	if !strings.Contains(body, "bundle 里没有 behavior fixtures") {
		t.Errorf("没有如实说明\"bundle 里没有 behavior fixtures\"：\n%s", body)
	}
	if !strings.Contains(body, "未执行") {
		t.Errorf("行为夹具必须明确写成未执行（不许静默省略、更不许谎报通过）：\n%s", body)
	}

	// 机读侧同样要能区分：--json 里必须有 behavioral_requested=true。
	out.Reset()
	if err := runPlasmaCompile(bundle, true, true, &out); err != nil {
		t.Fatalf("compile --behavioral --json 失败: %v\n%s", err, out.String())
	}
	var got struct {
		BehavioralRequested bool `json:"behavioral_requested"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("--json 不是合法 JSON: %v\n%s", err, out.String())
	}
	if !got.BehavioralRequested {
		t.Error("--json 里没有 behavioral_requested=true：机读侧也分不清两种\"未执行\"")
	}
}

// 幻觉工具名：编译必须失败，且输出里点名 PLS-101。
func TestRunPlasmaCompileRejectsUnknownTool(t *testing.T) {
	p7Isolate(t)
	bundle := p7Write(t, "bad.json", p7BadToolJSON)

	var out bytes.Buffer
	err := runPlasmaCompile(bundle, false, false, &out)
	if err == nil {
		t.Fatalf("含幻觉工具名的 bundle 编译成功了：\n%s", out.String())
	}
	if !strings.Contains(out.String(), plasma.CodeUnknownTool) {
		t.Errorf("诊断输出里没有 %s：\n%s", plasma.CodeUnknownTool, out.String())
	}
}

// --json 必须是一份可被解析的结果，且字段与文本模式同源。
func TestRunPlasmaCompileJSON(t *testing.T) {
	p7Isolate(t)
	bundle := p7Write(t, "bundle.json", p7SpecJSON)

	var out bytes.Buffer
	if err := runPlasmaCompile(bundle, false, true, &out); err != nil {
		t.Fatalf("compile --json 失败: %v\n%s", err, out.String())
	}
	var got struct {
		Bundle     string `json:"bundle"`
		Grade      string `json:"grade"`
		Structural []struct {
			FixtureID string `json:"fixture_id"`
			Passed    bool   `json:"passed"`
		} `json:"structural"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("--json 输出不是合法 JSON: %v\n%s", err, out.String())
	}
	if got.Grade != string(plasma.GradeProvisional) {
		t.Errorf("grade = %q, want provisional", got.Grade)
	}
	if len(got.Structural) != 4 {
		t.Fatalf("structural 条数 = %d, want 4:\n%s", len(got.Structural), out.String())
	}
	for _, o := range got.Structural {
		if !o.Passed {
			t.Errorf("夹具 %s 未通过", o.FixtureID)
		}
	}
}

// test 子命令：只跑指定那一条；未知 ID 是用法错（exit 2 的判据）。
func TestRunPlasmaTestSelectsOneFixture(t *testing.T) {
	p7Isolate(t)
	bundle := p7Write(t, "bundle.json", p7SpecJSON)

	var out bytes.Buffer
	if err := runPlasmaTest(bundle, plasma.FixtureExitContract, false, &out); err != nil {
		t.Fatalf("test exit-contract 失败: %v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), plasma.FixtureStepBudget) {
		t.Errorf("test 只该跑指定的一条，输出里却出现了 step-budget：\n%s", out.String())
	}

	out.Reset()
	err := runPlasmaTest(bundle, "no-such-fixture", false, &out)
	if err == nil {
		t.Fatal("未知夹具 ID 没有报错")
	}
	var ue *usageError
	if !errors.As(err, &ue) {
		t.Errorf("未知夹具 ID 应当是用法错（exit 2），得到 %T: %v", err, err)
	}
	// 提示必须列出可用夹具 —— "找不到"但不说有哪些等于让人去翻源码。
	if !strings.Contains(err.Error(), plasma.FixtureExitContract) {
		t.Errorf("用法错没有列出可用夹具: %v", err)
	}
}

// manifest：不装配 Agent 也能跑，且 --json 可解析。
func TestRunPlasmaManifest(t *testing.T) {
	p7Isolate(t)
	var out bytes.Buffer
	if err := runPlasmaManifest(true, &out); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	var m plasma.Manifest
	if err := json.Unmarshal(out.Bytes(), &m); err != nil {
		t.Fatalf("manifest --json 不是合法 JSON: %v\n%s", err, out.String())
	}
	if m.CapabilityLock == "" {
		t.Error("CapabilityLock 为空")
	}
	if len(m.Tools) == 0 {
		t.Error("manifest 的工具池为空 —— 注册表没 populate")
	}
	if len(m.Codes) == 0 {
		t.Error("manifest 的码表为空")
	}
	// 人读模式同样要能跑（不 panic、不返回错误）。
	out.Reset()
	if err := runPlasmaManifest(false, &out); err != nil {
		t.Fatalf("manifest（人读）: %v", err)
	}
	if !strings.Contains(out.String(), m.CapabilityLock) {
		t.Errorf("人读输出里没有 capability_lock：\n%s", out.String())
	}
}

// list：空 store 是 exit 0（"还没有"不是错误）。
func TestRunPlasmaListEmptyStore(t *testing.T) {
	p7Isolate(t)
	var out bytes.Buffer
	if err := runPlasmaList(false, &out); err != nil {
		t.Fatalf("空 store 的 list 不该报错: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "尚无 plasma agent") {
		t.Errorf("空 store 没有明确说明：\n%s", out.String())
	}
	out.Reset()
	if err := runPlasmaList(true, &out); err != nil {
		t.Fatalf("空 store 的 list --json 不该报错: %v", err)
	}
	var arr []map[string]interface{}
	if err := json.Unmarshal(out.Bytes(), &arr); err != nil {
		t.Fatalf("list --json 必须是 JSON 数组: %v\n%s", err, out.String())
	}
	if len(arr) != 0 {
		t.Errorf("空 store 返回了 %d 条", len(arr))
	}
}
