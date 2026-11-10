package plasma_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"yagent/internal/llm"
	"yagent/internal/plasma"
)

// 本文件是**跨语言契约的反漂移闸门**（P10）。
//
// 两侧各写一份常量是不可避免的（Python 在 zipapp 里，Go 在编译产物里），但
// "两份同形实现各自漂走"是本仓最贵的一类缺陷（坑 33）：这里漂的表现不是崩溃，
// 而是**子进程 exit 0 但 Go 找不到产物**，或者更糟——Python 读了一个 Go 不写的
// 文件名。所以闸门做两件事：
//
//  1. 读 `plasma/contract.py` 的**源码文本**，用正则提取字面量与 Go 常量逐条对账；
//  2. 用两侧共用的样例（`plasma/testdata/contract/*.json`）反序列化进**真实 Go
//     结构体**，断言关键字段非零 —— 样例只有一份，形状不可能分叉。
//
// 第 1 条刻意只认 `NAME = <literal>` 一行一条：Python 侧若改成计算式/多行拼接，
// 这里会**提取不到**并直接报红（而不是安静地跳过一条断言）。

var pyLiteral = regexp.MustCompile(`(?m)^([A-Z][A-Z0-9_]*)[ \t]*=[ \t]*(?:"([^"]*)"|([0-9]+))[ \t]*$`)

// contractPyPath 是契约镜像的位置（测试 cwd = internal/plasma）。
const contractPyPath = "../../plasma/contract.py"

func pyLiterals(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(contractPyPath)
	if err != nil {
		t.Fatalf("读不到 Python 契约镜像 %s：%v（它是 P10 的反漂移闸门的唯一输入）", contractPyPath, err)
	}
	out := make(map[string]string)
	for _, m := range pyLiteral.FindAllStringSubmatch(string(raw), -1) {
		value := m[2]
		if value == "" && m[3] != "" {
			value = m[3]
		}
		if _, dup := out[m[1]]; dup {
			t.Fatalf("plasma/contract.py 里 %s 定义了两次：两处同名常量的表现是后一处静默生效", m[1])
		}
		out[m[1]] = value
	}
	return out
}

// TestPythonContractConstantsMatchGo 是闸门主体：文件名 / 退出码 / schema_version
// 逐条与 Go 常量相等。少一条常量 = 红（防止有人把常量删掉却不改另一侧）。
func TestPythonContractConstantsMatchGo(t *testing.T) {
	got := pyLiterals(t)

	strWant := map[string]string{
		"FILE_REQ":      plasma.FileReq,
		"FILE_MANIFEST": plasma.FileManifest,
		"FILE_STATE":    plasma.FileState,
		"FILE_DIAG":     plasma.FileDiag,
		"FILE_BUNDLE":   plasma.FileBundle,
		"WORKDIR_PREFIX": func() string {
			// workdirPrefix 未导出：用 NewWorkdir 的实际命名反推前缀，
			// 而不是在 Go 里再加一个只为测试存在的导出常量。
			w, err := plasma.NewWorkdir(t.TempDir())
			if err != nil {
				t.Fatalf("NewWorkdir: %v", err)
			}
			defer func() { _ = w.Cleanup() }()
			base := filepath.Base(w.Path())
			for i := 0; i < len(base); i++ {
				if base[i] == '-' && i+1 < len(base) {
					return base[:i+1]
				}
			}
			return ""
		}(),
	}
	for key, want := range strWant {
		value, ok := got[key]
		if !ok {
			t.Errorf("plasma/contract.py 缺 %s（要求一行一条 `NAME = \"literal\"`）", key)
			continue
		}
		if value != want {
			t.Errorf("%s = %q，Go 侧是 %q", key, value, want)
		}
	}

	intWant := map[string]int{
		"EXIT_OK":       plasma.ExitOK,
		"EXIT_FAILED":   plasma.ExitFailed,
		"EXIT_USAGE":    plasma.ExitUsage,
		"EXIT_TIMEOUT":  plasma.ExitTimeout,
		"EXIT_SAMPLING": plasma.ExitSampling,
		// manifest 的 schema_version 是 Python 只读契约的一部分：漂了会让
		// 生成器按旧能力清单选工具。
		"MANIFEST_SCHEMA_VERSION": plasma.ManifestSchemaVersion,
	}
	for key, want := range intWant {
		value, ok := got[key]
		if !ok {
			t.Errorf("plasma/contract.py 缺 %s（要求一行一条 `NAME = <number>`）", key)
			continue
		}
		n, err := strconv.Atoi(value)
		if err != nil {
			t.Errorf("%s = %q 不是整数", key, value)
			continue
		}
		if n != want {
			t.Errorf("%s = %d，Go 侧是 %d", key, n, want)
		}
	}

	// 序号命名必须与 Go 的 Workdir.LLMReq/LLMResp 渲染结果逐字一致（%03d，从 000 起）。
	reqPattern, ok := got["LLM_REQ_PATTERN"]
	if !ok {
		t.Fatalf("plasma/contract.py 缺 LLM_REQ_PATTERN")
	}
	respPattern, ok := got["LLM_RESP_PATTERN"]
	if !ok {
		t.Fatalf("plasma/contract.py 缺 LLM_RESP_PATTERN")
	}
	w, err := plasma.NewWorkdir(t.TempDir())
	if err != nil {
		t.Fatalf("NewWorkdir: %v", err)
	}
	defer func() { _ = w.Cleanup() }()
	for _, seq := range []int{0, 7, 42, 1234} {
		if want := filepath.Base(w.LLMReq(seq)); fmt.Sprintf(reqPattern, seq) != want {
			t.Errorf("LLM_REQ_PATTERN(%d) = %q，Go 渲染 %q", seq, fmt.Sprintf(reqPattern, seq), want)
		}
		if want := filepath.Base(w.LLMResp(seq)); fmt.Sprintf(respPattern, seq) != want {
			t.Errorf("LLM_RESP_PATTERN(%d) = %q，Go 渲染 %q", seq, fmt.Sprintf(respPattern, seq), want)
		}
	}
}

// TestPythonContractPythonIsStdlibOnly 钉住 ADR-8：zipapp 里不许出现第三方 import。
// 判据是**白名单**而不是黑名单：黑名单只能挡住已知的库。
func TestPythonContractPythonIsStdlibOnly(t *testing.T) {
	stdlib := map[string]bool{
		"argparse": true, "os": true, "sys": true, "json": true, "zipfile": true,
		"difflib": true, "tempfile": true, "unittest": true, "io": true,
		"contextlib": true, "hashlib": true, "typing": true, "re": true,
		"contract": true, "manifest": true, "sampling": true, "state": true,
		"generate": true, "explain": true, "author_fixtures": true, "cli": true,
	}
	importLine := regexp.MustCompile(`(?m)^[ \t]*(?:import|from)[ \t]+([A-Za-z_][A-Za-z0-9_]*)`)
	dir := "../../plasma"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读不到 %s：%v", dir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".py" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatalf("读 %s：%v", entry.Name(), err)
		}
		for _, m := range importLine.FindAllStringSubmatch(string(raw), -1) {
			if !stdlib[m[1]] {
				t.Errorf("plasma/%s 引入了非标准库模块 %q（ADR-8：zipapp 必须 stdlib-only）", entry.Name(), m[1])
			}
		}
	}
}

// TestPythonContractSharedSamples 用**两侧共用的样例**证明形状一致：
// 这里的 Go 结构体是生产用的 llm.Response，不是测试自造的影子类型。
func TestPythonContractSharedSamples(t *testing.T) {
	reqRaw, err := os.ReadFile("../../plasma/testdata/contract/llm_req_000.json")
	if err != nil {
		t.Fatalf("读不到共享请求样例：%v", err)
	}
	var reqShape map[string]json.RawMessage
	if err := json.Unmarshal(reqRaw, &reqShape); err != nil {
		t.Fatalf("请求样例不是合法 JSON：%v", err)
	}
	// llmRequestFile 的字段集（trampoline.go）：messages 必填，其余 omitempty。
	allowed := map[string]bool{"messages": true, "tools": true, "max_tokens": true, "temperature": true, "reasoning_effort": true}
	for key := range reqShape {
		if !allowed[key] {
			t.Errorf("请求样例出现 Go 结构体没有的键 %q（llmRequestFile 无此字段）", key)
		}
	}
	if _, ok := reqShape["messages"]; !ok {
		t.Error("请求样例缺 messages（Go 侧会对空 messages 直接报错）")
	}

	var resp llm.Response
	respRaw, err := os.ReadFile("../../plasma/testdata/contract/llm_resp_000.json")
	if err != nil {
		t.Fatalf("读不到共享响应样例：%v", err)
	}
	if err := json.Unmarshal(respRaw, &resp); err != nil {
		t.Fatalf("响应样例解不进 llm.Response：%v", err)
	}
	if len(resp.Choices) == 0 {
		t.Fatal("响应样例没有 choices")
	}
	if resp.Choices[0].Content == "" {
		t.Error("响应样例 choices[0].content 为空：Python 侧靠它取模型输出")
	}
	if resp.Choices[0].FinishReason != llm.FinishStop {
		t.Errorf("finish_reason = %q，期望 %q", resp.Choices[0].FinishReason, llm.FinishStop)
	}
	if resp.Usage == nil || resp.Usage.TotalTokens <= 0 {
		t.Errorf("usage.total_tokens = %v，期望 > 0（少了它成本统计那一栏归零）", resp.Usage)
	}
}
