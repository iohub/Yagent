package tui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yagent/internal/config"
	"yagent/internal/plasma"
)

// 本文件钉住 `/proposals` 的四条命令。
//
// 两条不变量比功能更重要：
//  1. 判决**只**改提案 JSON 的 status 字段 —— 任何 `*.prompt.md` 的字节不许变；
//  2. 参数错误（id 不存在 / 缺参数 / 未知子命令）只打可读提示，不 panic、不退出 TUI。

func isolateProposalHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := config.PlasmaProposalDir()
	if !strings.HasPrefix(dir, home) {
		t.Fatalf("提案目录 %q 不在临时 HOME（%s）下，用例会写到真实目录", dir, home)
	}
	return dir
}

// writeProposalDoc 造一份与 propose.py 同形的提案（字段一个不少：面板要逐项显示）。
func writeProposalDoc(t *testing.T, dir, id, status, promptPath string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	doc := map[string]any{
		"schema_version":  1,
		"id":              id,
		"agent":           "coding",
		"status":          status,
		"base_hash":       strings.Repeat("b", 64),
		"rationale":       "给 coding 的提案，依据是最近的生产遥测。",
		"diff":            "--- a/internal/agents/coding.prompt.md\n+++ b/internal/agents/coding.prompt.md\n@@ R1 @@\n+## Finish Checklist\n",
		"created_at":      "2026-11-10T01:00:00Z",
		"rule_ids":        []string{"R1"},
		"evidence":        []string{"failures=3"},
		"editable_fields": []string{"prompt.system"},
		"prompt_path":     promptPath,
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	path := filepath.Join(dir, id+".json")
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		t.Fatalf("write proposal: %v", err)
	}
	return path
}

func fileDigest(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func proposalField(t *testing.T, path, field string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return string(doc[field])
}

// 空目录不报错：还没跑过 propose 的机器上"还没有"是正确答案。
func TestProposalsCommandListEmptyDir(t *testing.T) {
	isolateProposalHome(t)
	m := chromeTestModel(t, 120, 40)

	m.processCommand("/proposals")

	panel := topTextPanel(t, m, proposalsPanelID)
	if !panelContains(panel.Lines(), "尚无提案") {
		t.Errorf("空目录面板 = %v，want 含「尚无提案」", panel.Lines())
	}
	if m.infoMsg != "" {
		t.Errorf("空目录报了错：infoMsg = %q", m.infoMsg)
	}
}

// 列表只列 pending，但已判决的与坏掉的都要如实点名（"队列是空的"不能错报）。
func TestProposalsCommandListShowsPending(t *testing.T) {
	dir := isolateProposalHome(t)
	writeProposalDoc(t, dir, "coding-20261110T010000Z", plasma.ProposalStatusPending, "")
	writeProposalDoc(t, dir, "repo-20261110T000000Z", plasma.ProposalStatusApproved, "")
	if err := os.WriteFile(filepath.Join(dir, "coding-broken.json"), []byte("{ 坏"), 0o600); err != nil {
		t.Fatalf("write broken: %v", err)
	}
	m := chromeTestModel(t, 120, 40)

	m.processCommand("/proposals")

	lines := topTextPanel(t, m, proposalsPanelID).Lines()
	if !panelContains(lines, "coding-20261110T010000Z") {
		t.Errorf("待审批的提案没列出来：%v", lines)
	}
	if !panelContains(lines, "另有 1 份已判决") {
		t.Errorf("已判决的那份被藏起来了：%v", lines)
	}
	if !panelContains(lines, "coding-broken") {
		t.Errorf("读不动的提案被藏起来了：%v", lines)
	}
}

// `/proposals show <id>` 打印 diff（人读；不执行任何东西）。
func TestProposalsCommandShowPrintsDiff(t *testing.T) {
	dir := isolateProposalHome(t)
	writeProposalDoc(t, dir, "coding-20261110T010000Z", plasma.ProposalStatusPending,
		"internal/agents/coding.prompt.md")
	m := chromeTestModel(t, 120, 40)

	m.processCommand("/proposals show coding-20261110T010000Z")

	lines := topTextPanel(t, m, proposalsPanelID).Lines()
	if !panelContains(lines, "+++ b/internal/agents/coding.prompt.md") {
		t.Errorf("面板里没有 diff：%v", lines)
	}
	if !panelContains(lines, "status: pending") {
		t.Errorf("面板里没有 status：%v", lines)
	}
	// 路径只显示，不许读也不许写 —— 文案必须把这层意思说出来。
	if !panelContains(lines, "只显示路径") {
		t.Errorf("prompt 路径旁边没有说明：%v", lines)
	}
}

// 不存在的 id 只提示，不 panic、不开面板。
func TestProposalsCommandShowMissingID(t *testing.T) {
	isolateProposalHome(t)
	m := chromeTestModel(t, 120, 40)

	m.processCommand("/proposals show nope-20261110T010000Z")

	if m.dialogStack.Len() != 0 {
		t.Error("不存在的 id 开了面板")
	}
	if !strings.Contains(m.infoMsg, "不存在") {
		t.Errorf("infoMsg = %q，want 可读提示", m.infoMsg)
	}

	// 缺参数与未知子命令同样只提示。
	m.processCommand("/proposals show")
	if !strings.Contains(m.infoMsg, "用法") {
		t.Errorf("缺参数 infoMsg = %q，want 用法提示", m.infoMsg)
	}
	m.processCommand("/proposals bogus")
	if !strings.Contains(m.infoMsg, "未知子命令") {
		t.Errorf("未知子命令 infoMsg = %q，want 可读提示", m.infoMsg)
	}
}

// 最核心的一条：approve 只把 status 从 pending 改成 approved，
// 而 prompt_path 指向的 `*.prompt.md` 与提案的 .md 说明**逐字节不变**。
// 变异：让 decideProposal 顺手写一次 prompt 文件 ⇒ 本用例立刻红。
func TestProposalsCommandApproveOnlyTouchesStatus(t *testing.T) {
	dir := isolateProposalHome(t)
	promptDir := t.TempDir()
	promptPath := filepath.Join(promptDir, "coding.prompt.md")
	if err := os.WriteFile(promptPath, []byte("你是一个 agent。\n"), 0o600); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	id := "coding-20261110T010000Z"
	jsonPath := writeProposalDoc(t, dir, id, plasma.ProposalStatusPending, promptPath)
	mdPath := filepath.Join(dir, id+".md")
	if err := os.WriteFile(mdPath, []byte("# 提案说明\n"), 0o600); err != nil {
		t.Fatalf("write md: %v", err)
	}
	promptDigest := fileDigest(t, promptPath)
	mdDigest := fileDigest(t, mdPath)

	m := chromeTestModel(t, 120, 40)
	m.processCommand("/proposals approve " + id)

	if got := proposalField(t, jsonPath, "status"); got != `"approved"` {
		t.Errorf("提案 status = %s, want \"approved\"", got)
	}
	if got := fileDigest(t, promptPath); got != promptDigest {
		t.Fatalf("%s 的字节变了（%s → %s）：审批只许改 status —— 内建 Agent 的 prompt 与 "+
			"Go 代码必须同时改，自动 apply 会把两者改成不一致", promptPath, promptDigest, got)
	}
	if got := fileDigest(t, mdPath); got != mdDigest {
		t.Errorf("提案的 .md 说明被改了（%s → %s）", mdDigest, got)
	}

	// 回显：状态行 + 详情面板（diff 仍在，status 已翻）。
	statusLine := ""
	for _, e := range m.logEntries {
		if strings.Contains(e.content, "已批准") {
			statusLine = e.content
		}
	}
	if !strings.Contains(statusLine, "未改任何 *.prompt.md") {
		t.Errorf("状态行 = %q，want 明说没改 prompt", statusLine)
	}
	lines := topTextPanel(t, m, proposalsPanelID).Lines()
	if !panelContains(lines, "status: approved") {
		t.Errorf("面板里的 status 没翻：%v", lines)
	}
	if !panelContains(lines, "+++ b/internal/agents/coding.prompt.md") {
		t.Errorf("面板里没有回显 diff：%v", lines)
	}

	// 判决是终态：再判一次只提示，文件不动。
	beforeReject := fileDigest(t, jsonPath)
	m.dialogStack.Pop()
	m.processCommand("/proposals reject " + id)
	if !strings.Contains(m.infoMsg, "处理失败") {
		t.Errorf("二次判决 infoMsg = %q，want 可读提示", m.infoMsg)
	}
	if fileDigest(t, jsonPath) != beforeReject {
		t.Error("二次判决改了文件")
	}
}

// reject 是另一条判决分支（value 必须真写进去，不是只翻个提示）。
func TestProposalsCommandReject(t *testing.T) {
	dir := isolateProposalHome(t)
	id := "coding-20261110T020000Z"
	jsonPath := writeProposalDoc(t, dir, id, plasma.ProposalStatusPending, "")
	m := chromeTestModel(t, 120, 40)

	m.processCommand("/proposals reject " + id)

	if got := proposalField(t, jsonPath, "status"); got != `"rejected"` {
		t.Errorf("提案 status = %s, want \"rejected\"", got)
	}
	if !panelContains(topTextPanel(t, m, proposalsPanelID).Lines(), "status: rejected") {
		t.Error("面板里的 status 没翻成 rejected")
	}
}
