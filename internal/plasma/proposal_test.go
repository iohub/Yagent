package plasma_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"yagent/internal/plasma"
)

// 提案的落盘契约（plasma/propose.py 写、Go 读并判决）。
//
// 最重要的一条在 TestSetProposalStatusLeavesPromptFilesUntouched：审批**只**改提案
// JSON 的 status 字段，绝不改写任何 `*.prompt.md`。内建 Agent 的 prompt 与 Go 代码是
// "改行为要同时改两处"的耦合（根 AGENTS.md §4），auto-apply 会把两者改成不一致 ——
// 那种不一致编译得过、测试也绿，只在行为上慢慢漂。

const proposalPromptBody = "你是一个什么都不做的 agent。\n"

// writeProposal 造一份与 propose.py 同形的提案，返回它的路径。
func writeProposal(t *testing.T, dir, id, status string, extra map[string]any) string {
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
		"created_at":      "2026-11-10T00:00:00Z",
		"rule_ids":        []string{"R1"},
		"evidence":        []string{"failures=3"},
		"editable_fields": []string{"prompt.system"},
		"prompt_path":     "internal/agents/coding.prompt.md",
	}
	for k, v := range extra {
		doc[k] = v
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

func readRawJSON(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return doc
}

func sha256Of(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func TestListProposalsSortsAndMarksBroken(t *testing.T) {
	dir := t.TempDir()
	writeProposal(t, dir, "coding-20261110T010000Z", plasma.ProposalStatusPending, nil)
	writeProposal(t, dir, "repo-20261110T020000Z", plasma.ProposalStatusPending,
		map[string]any{"created_at": "2026-11-10T02:00:00Z"})
	if err := os.WriteFile(filepath.Join(dir, "coding-broken.json"), []byte("{ 坏"), 0o600); err != nil {
		t.Fatalf("write broken: %v", err)
	}
	// 非 .json 文件必须被忽略（propose.py 同时写一份 .md）。
	if err := os.WriteFile(filepath.Join(dir, "coding-20261110T010000Z.md"), []byte("# x\n"), 0o600); err != nil {
		t.Fatalf("write md: %v", err)
	}

	entries, err := plasma.ListProposals(dir)
	if err != nil {
		t.Fatalf("ListProposals: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("条目数 = %d, want 3（两份好的一份坏的；.md 不算）: %+v", len(entries), entries)
	}
	// created_at 降序：新的在前。
	if entries[0].ID != "repo-20261110T020000Z" {
		t.Errorf("首条 = %q，want 最新的 repo 提案（按 created_at 降序）", entries[0].ID)
	}
	broken := entries[len(entries)-1]
	if broken.ID != "coding-broken" || broken.Err == nil {
		t.Errorf("坏文件没有被标记：%+v", broken)
	}
	if broken.Proposal != nil {
		t.Errorf("坏文件不该有解析结果：%+v", broken.Proposal)
	}
}

// 目录不存在不是错误：还没跑过 propose 的机器上"还没有提案"是正确答案。
func TestListProposalsMissingDir(t *testing.T) {
	entries, err := plasma.ListProposals(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("ListProposals(不存在) 返回错误：%v", err)
	}
	if len(entries) != 0 {
		t.Errorf("条目数 = %d, want 0", len(entries))
	}
}

// 审批只改 status：其余字段（包括 Python 侧将来新加的未知字段）必须原样带过去。
// 按 struct 回写会把未知字段全丢掉，而丢掉的字段没有任何报错。
func TestSetProposalStatusOnlyChangesStatus(t *testing.T) {
	dir := t.TempDir()
	path := writeProposal(t, dir, "coding-20261110T010000Z", plasma.ProposalStatusPending,
		map[string]any{"future_field": map[string]any{"n": 3}})
	before := readRawJSON(t, path)

	p, err := plasma.SetProposalStatus(dir, "coding-20261110T010000Z", plasma.ProposalStatusApproved)
	if err != nil {
		t.Fatalf("SetProposalStatus: %v", err)
	}
	if p.Status != plasma.ProposalStatusApproved {
		t.Errorf("返回的 status = %q, want approved", p.Status)
	}

	after := readRawJSON(t, path)
	if got := string(after["status"]); got != `"approved"` {
		t.Errorf("落盘的 status = %s, want \"approved\"", got)
	}
	// 除 status 外逐字段相同（键序无所谓，比的是值）。
	delete(before, "status")
	delete(after, "status")
	if !reflect.DeepEqual(before, after) {
		t.Errorf("除了 status 还有别的字段被改了：\nbefore=%v\nafter=%v", before, after)
	}
	if _, ok := after["future_field"]; !ok {
		t.Error("未知字段 future_field 被丢掉了")
	}

	// 落盘形态仍是 propose.py 的 2 空格缩进 + 结尾换行（人看 diff 时不抖）。
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.HasSuffix(string(raw), "\n") || !strings.Contains(string(raw), "\n  \"") {
		t.Errorf("落盘缩进/结尾换行变了：\n%s", raw)
	}
}

// 判决是终态：再判一次必须报错且**不改文件**（否则审计上无法回答"当初批的是什么"）。
func TestSetProposalStatusRejectsNonPending(t *testing.T) {
	dir := t.TempDir()
	path := writeProposal(t, dir, "coding-20261110T010000Z", plasma.ProposalStatusApproved, nil)
	digest := sha256Of(t, path)

	if _, err := plasma.SetProposalStatus(dir, "coding-20261110T010000Z", plasma.ProposalStatusRejected); err == nil {
		t.Fatal("已批准的提案被改成 rejected 了")
	} else if !strings.Contains(err.Error(), "pending") {
		t.Errorf("错误文本没说明只接受 pending：%v", err)
	}
	if sha256Of(t, path) != digest {
		t.Error("失败的判决改了文件字节")
	}
}

func TestSetProposalStatusRejectsBadTarget(t *testing.T) {
	dir := t.TempDir()
	writeProposal(t, dir, "coding-20261110T010000Z", plasma.ProposalStatusPending, nil)
	for _, to := range []string{plasma.ProposalStatusPending, "APPROVED", ""} {
		if _, err := plasma.SetProposalStatus(dir, "coding-20261110T010000Z", to); err == nil {
			t.Errorf("判决目标 %q 被接受了", to)
		}
	}
}

// 最核心的一条：审批前后，任何 `*.prompt.md` 的字节都不许变。
// 变异：让 SetProposalStatus 顺手写一次 prompt_path ⇒ 本用例立刻红。
func TestSetProposalStatusLeavesPromptFilesUntouched(t *testing.T) {
	dir := t.TempDir()
	work := t.TempDir()
	promptPath := filepath.Join(work, "coding.prompt.md")
	if err := os.WriteFile(promptPath, []byte(proposalPromptBody), 0o600); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	id := "coding-20261110T010000Z"
	jsonPath := writeProposal(t, dir, id, plasma.ProposalStatusPending,
		map[string]any{"prompt_path": promptPath})
	mdPath := filepath.Join(dir, id+".md")
	if err := os.WriteFile(mdPath, []byte("# 提案说明\n"), 0o600); err != nil {
		t.Fatalf("write md: %v", err)
	}

	promptDigest := sha256Of(t, promptPath)
	mdDigest := sha256Of(t, mdPath)

	if _, err := plasma.SetProposalStatus(dir, id, plasma.ProposalStatusApproved); err != nil {
		t.Fatalf("SetProposalStatus: %v", err)
	}

	if got := sha256Of(t, promptPath); got != promptDigest {
		t.Fatalf("审批改了 %s 的字节（%s → %s）：内建 Agent 的 prompt 与 Go 代码必须同时改，"+
			"自动 apply 会把两者改成不一致", promptPath, promptDigest, got)
	}
	if got := sha256Of(t, mdPath); got != mdDigest {
		t.Errorf("审批改了提案的 .md 说明（%s → %s）", mdDigest, got)
	}
	if got := string(readRawJSON(t, jsonPath)["status"]); got != `"approved"` {
		t.Errorf("status = %s, want \"approved\"", got)
	}
}

// id 来自用户输入（`/proposals show <id>`）：必须挡住目录穿越。
func TestProposalIDRejectsTraversal(t *testing.T) {
	outside := t.TempDir()
	dir := filepath.Join(outside, "proposals")
	writeProposal(t, dir, "coding-20261110T010000Z", plasma.ProposalStatusPending, nil)
	// 在提案目录之外放一份同名文件，穿越成功就能读到/改到它。
	secret := filepath.Join(outside, "secret.json")
	if err := os.WriteFile(secret, []byte(`{"status":"pending"}`), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}

	for _, id := range []string{"../secret", "sub/x", "..", ".", "", "a\\b", "x/../../secret"} {
		if _, err := plasma.LoadProposal(dir, id); err == nil {
			t.Errorf("LoadProposal(%q) 接受了非法 id", id)
		}
		if _, err := plasma.SetProposalStatus(dir, id, plasma.ProposalStatusApproved); err == nil {
			t.Errorf("SetProposalStatus(%q) 接受了非法 id", id)
		}
	}
	if got := string(readRawJSON(t, secret)["status"]); got != `"pending"` {
		t.Errorf("目录外的文件被改了：status = %s", got)
	}
}

// 符号链接一律拒：approve 会**写**这个文件，跟随链接等于让一次审批写到别处去。
func TestSetProposalStatusRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "real.json")
	writeProposal(t, filepath.Dir(target), "real", plasma.ProposalStatusPending, nil)
	if err := os.Symlink(target, filepath.Join(dir, "coding-link.json")); err != nil {
		t.Skipf("本机不支持符号链接：%v", err)
	}
	digest := sha256Of(t, target)
	if _, err := plasma.SetProposalStatus(dir, "coding-link", plasma.ProposalStatusApproved); err == nil {
		t.Fatal("符号链接被当成普通提案接受了")
	}
	if sha256Of(t, target) != digest {
		t.Error("链接目标被改了")
	}
}

func TestLoadProposalMissingIsSentinel(t *testing.T) {
	_, err := plasma.LoadProposal(t.TempDir(), "nope-20261110T010000Z")
	if !errors.Is(err, plasma.ErrProposalNotFound) {
		t.Fatalf("err = %v, want ErrProposalNotFound", err)
	}
}
