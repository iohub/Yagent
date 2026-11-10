package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yagent/internal/llm"
	"yagent/internal/plasma"
)

// 本文件是 P10 的**真实跨语言冒烟**：真的 python3、真的 dist/bin/plasma.pyz、
// 真的 ExecSpawner。它证明的不是"策略写得好"，而是**两侧契约真的对得上** ——
// exit 4 的序号、llm_req/llm_resp 的 JSON 形状、bundle.json 的位置与形态。
// 这条测试的价值高于任何单侧单元测试：两侧各自的测试都能全绿而接口是错的。
//
// PATH 上没有 python3（或产物缺失）时用 helper 跳过并说明原因 —— 这是 ADR-15
// 定义的正常降级，不是验证通过。skip 必须在 helper 里（scripts/check_test_hygiene.sh
// 禁止函数体第一层裸 t.Skip*）。

// pythonRuntime 解析解释器与 zipapp 路径；缺任何一个就跳过并说明怎么装。
func pythonRuntime(t *testing.T) (python, pyz string) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("PATH 上没有 python3：跳过真实跨语言冒烟（ADR-15：缺解释器是正常降级）")
	}
	pyz, err = filepath.Abs(filepath.Join("..", "..", "dist", "bin", "plasma.pyz"))
	if err != nil {
		t.Fatalf("解析 zipapp 绝对路径失败：%v", err)
	}
	if _, statErr := os.Stat(pyz); statErr != nil {
		t.Skipf("缺少 %s：先跑 bash scripts/build_plasma.sh（产物未入库时这是预期）", pyz)
	}
	return python, pyz
}

// TestPythonZipappTrampolineRoundTrip 是 P10 的核心验收：exit 4 → Go 写响应 →
// respawn --resume → bundle.json 可被 Elaborate 解析。
func TestPythonZipappTrampolineRoundTrip(t *testing.T) {
	python, pyz := pythonRuntime(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	spawner := &ExecSpawner{Python: python, Pkg: pyz, KillGrace: 5 * time.Second}
	w, err := plasma.NewWorkdir(t.TempDir())
	if err != nil {
		t.Fatalf("建 workdir：%v", err)
	}
	defer func() { _ = w.Cleanup() }()

	if err := w.WriteReq("审查仓库里的鉴权中间件是否把会话令牌写进日志", "只读即可"); err != nil {
		t.Fatalf("写 req.json：%v", err)
	}
	manifest := (&plasma.Compiler{Palette: plasma.PlasmaPalette}).Manifest()
	if err := w.WriteManifest(manifest); err != nil {
		t.Fatalf("写 manifest.json：%v", err)
	}

	code, err := spawner.Spawn(ctx, w.Path(), []string{"generate"})
	if err != nil {
		t.Fatalf("首次 spawn 失败：%v", err)
	}
	if code != plasma.ExitSampling {
		t.Fatalf("首次 spawn exit=%d，期望 %d（需要 LLM 采样）", code, plasma.ExitSampling)
	}

	// ---- 间隔 1：请求必须能解进**生产结构体** llmRequestFile ----
	rawReq, err := os.ReadFile(w.LLMReq(0))
	if err != nil {
		t.Fatalf("Python 没有写 llm_req_000.json：%v", err)
	}
	var req llmRequestFile
	if err := json.Unmarshal(rawReq, &req); err != nil {
		t.Fatalf("llm_req_000.json 解不进 llmRequestFile（两侧契约漂了）：%v", err)
	}
	if len(req.Messages) < 2 {
		t.Fatalf("请求里只有 %d 条 message，期望 system + user", len(req.Messages))
	}
	if !strings.Contains(req.Messages[0].Content, "agent_exit") {
		t.Error("system 提示里没有工具池表格（{{TOOL_POOL_TABLE}} 没被替换）")
	}

	// ---- 由测试扮演 LLM：写一份能产出合法 bundle 的响应 ----
	toolName := firstUsableTool(t, manifest)
	spec := map[string]interface{}{
		"thinking":       "真实跨语言冒烟：只读分析任务，分配只读工具与收尾工具。",
		"agent_name":     "Python Smoke Auditor",
		"agent_design":   "### Role\nYou audit authentication middleware for credential leakage.\n\n### Instructions\n1. Locate the middleware with search tools.\n2. Inspect every logging call site for session tokens or headers.\n3. Report each finding with file:line and a concrete remediation.\n\n### Output\nWrite findings in Markdown, then call agent_exit with a one-line reason.",
		"tools_used":     []string{toolName, "agent_exit"},
		"task_for_agent": "审查鉴权中间件，列出所有可能把会话令牌写进日志的调用点，并给出修复建议。",
	}
	specJSON, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("序列化 spec：%v", err)
	}
	respBody := marshalResponse(t, string(specJSON))
	if err := w.WriteLLMResp(0, respBody); err != nil {
		t.Fatalf("写 llm_resp_000.json：%v", err)
	}

	// ---- 反复 respawn 直到 exit 0（多轮策略每轮再要一次采样时喂同一份合法响应）----
	const maxRounds = 4
	for round := 0; round < maxRounds; round++ {
		code, err = spawner.Spawn(ctx, w.Path(), []string{"generate", "--resume"})
		if err != nil {
			t.Fatalf("第 %d 次 respawn 失败：%v", round+1, err)
		}
		if code == plasma.ExitOK {
			break
		}
		if code != plasma.ExitSampling {
			stderrTail := ""
			if spawnErr, ok := err.(*plasma.SpawnError); ok {
				stderrTail = spawnErr.Stderr
			}
			t.Fatalf("第 %d 次 respawn exit=%d（%s），期望 0 或 4；stderr=%s",
				round+1, code, plasma.ExitCodeName(code), stderrTail)
		}
		if err := w.WriteLLMResp(round+1, respBody); err != nil {
			t.Fatalf("写 llm_resp_%03d.json：%v", round+1, err)
		}
	}
	if code != plasma.ExitOK {
		t.Fatalf("%d 轮后仍未 exit 0（最后一次 exit=%d）", maxRounds, code)
	}

	// ---- 产物必须能被 Go 的 Elaborate 解析（这才是"可装配"的最低门槛）----
	rawBundle, err := w.ReadBundleRaw()
	if err != nil {
		t.Fatalf("exit 0 但没有 bundle.json：%v", err)
	}
	bundle, err := plasma.Elaborate(rawBundle, plasma.DefaultDefaults())
	if err != nil {
		t.Fatalf("Elaborate 解析 Python 产出的 bundle 失败：%v", err)
	}
	if bundle.Name == "" || bundle.DisplayName == "" {
		t.Errorf("bundle 名字为空：name=%q display_name=%q", bundle.Name, bundle.DisplayName)
	}
	if bundle.Prompt.System == "" {
		t.Error("bundle.prompt.system 为空：agent_design 没被带进 IR")
	}
	if !containsString(bundle.Tools, "agent_exit") {
		t.Errorf("bundle.tools = %v，缺 agent_exit（收尾工具必须被保留）", bundle.Tools)
	}
}

// TestPythonZipappOfflineCommands 覆盖离线单向：--help 与 explain --out 都必须 exit 0，
// 且 explain 的 stdout 只有一份 JSON（§7.3）。
func TestPythonZipappOfflineCommands(t *testing.T) {
	python, pyz := pythonRuntime(t)
	dir := t.TempDir()

	helpOut, err := exec.Command(python, pyz, "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("python3 plasma.pyz --help 失败：%v\n%s", err, helpOut)
	}
	if !strings.Contains(string(helpOut), "generate") {
		t.Errorf("--help 输出里没有 generate 子命令：%s", helpOut)
	}

	report := `{"diagnostics":[{"code":"PLS-101","severity":"error","path":"bundle.tools[0]","message":"unknown tool \"read_filez\"","note":"did you mean \"read_file\"?"}],"escalation_required":false}`
	reportPath := filepath.Join(dir, "report.json")
	if err := os.WriteFile(reportPath, []byte(report), 0o600); err != nil {
		t.Fatalf("写诊断夹具：%v", err)
	}
	outPath := filepath.Join(dir, "explain.txt")

	var stdout, stderr bytes.Buffer
	cmd := exec.Command(python, pyz, "explain", "--in", reportPath, "--out", outPath)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("explain 退出非 0：%v；stderr=%s", err, stderr.String())
	}

	text, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("explain 没有写 --out：%v", err)
	}
	for _, want := range []string{"PLS-101", "did you mean", "合计：1 error"} {
		if !strings.Contains(string(text), want) {
			t.Errorf("explain 输出缺 %q：\n%s", want, text)
		}
	}

	var envelope map[string]interface{}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("explain 的 stdout 不是一份 JSON（§7.3）：%v / %q", err, stdout.String())
	}
	if envelope["errors"].(float64) != 1 {
		t.Errorf("explain 信封 errors=%v，期望 1", envelope["errors"])
	}
}

// firstUsableTool 从真实 manifest 里挑一个非 agent_exit 的工具。
// 用 manifest 而不是写死 "read_file"：写死会让这条测试在 palette 收窄后
// 报一个与契约无关的错（"工具不在工具池里"）。
func firstUsableTool(t *testing.T, m *plasma.Manifest) string {
	t.Helper()
	hasExit := false
	for _, tool := range m.Tools {
		if tool.Name == "agent_exit" {
			hasExit = true
			break
		}
	}
	if !hasExit {
		t.Fatal("manifest 里没有 agent_exit：PlasmaPalette 变了，需要人工确认（收尾工具是硬要求）")
	}
	for _, tool := range m.Tools {
		if tool.Name != "" && tool.Name != "agent_exit" {
			return tool.Name
		}
	}
	t.Fatal("manifest 里没有任何可分配的只读工具")
	return ""
}

func marshalResponse(t *testing.T, content string) []byte {
	t.Helper()
	resp := &llm.Response{
		Choices: []llm.Choice{{Content: content, FinishReason: llm.FinishStop}},
		Usage:   &llm.TokenUsage{PromptTokens: 1200, CompletionTokens: 300, TotalTokens: 1500},
	}
	body, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		t.Fatalf("序列化响应：%v", err)
	}
	return append(body, '\n')
}
