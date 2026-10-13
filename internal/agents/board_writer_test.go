package agents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yagent/internal/blackboard"
)

// 这两条导出方法是 BoardWriter 契约（Coding→Browser 的二级委派靠它们上便签板）。
// 直接测 browserDelegate 需要构造真实 *BrowserAgent（要 LLM 引擎与执行层），
// 成本与收益不成比；契约本身测通了，委派路径就只是它的调用方。
func boardDirector(t *testing.T) *DirectorAgent {
	t.Helper()
	workDir := t.TempDir()
	t.Setenv("YAGENT_NOTES_ROOT", filepath.Join(workDir, "notes"))
	d := newTestDirectorAgent(t, workDir)
	d.SetBoardScope("session-1", "task-1")
	return d
}

func TestBoardWriterPacksIndexForSubDelegation(t *testing.T) {
	d := boardDirector(t)

	// 板上什么都没有：任务原样返回，不注入空块。
	first, unresolved := d.PackDelegateTask("去查登录页的报错", map[string]interface{}{})
	if first != "去查登录页的报错" {
		t.Errorf("packed = %q, want the task unchanged", first)
	}
	if unresolved != 0 {
		t.Errorf("unresolved = %d, want 0", unresolved)
	}

	ref := d.WriteDelegateNote("repo_agent", "delegate_repo", "查报错原因", AgentResult{
		Text:        "摘要",
		Summary:     "摘要",
		Deliverable: "登录页报错来自缺失的 CSRF token，证据在 internal/http/server.go:120",
		Handoff: &blackboard.Handoff{
			Conclusion: "缺 CSRF token",
			Facts:      []blackboard.Fact{{Claim: "校验在 server.go:120", Where: "internal/http/server.go:120"}},
			Status:     blackboard.StatusOK,
		},
	}, nil)
	if ref == "" {
		t.Fatal("WriteDelegateNote returned an empty ref")
	}

	packed, _ := d.PackDelegateTask("再查一次登录页", map[string]interface{}{})
	if !strings.HasPrefix(packed, blackboard.IndexHeaderPrefix) {
		t.Errorf("sub-delegation must carry the board index, got:\n%s", packed)
	}
	if !strings.Contains(packed, "缺 CSRF token") {
		t.Errorf("index lost the conclusion:\n%s", packed)
	}
	if !strings.Contains(packed, ref) {
		t.Errorf("index must carry the retrievable ref:\n%s", packed)
	}
	// 原任务永远排在最后：注入的是参考料，不是新指令。
	if !strings.HasSuffix(packed, "再查一次登录页") {
		t.Errorf("task must remain last:\n%s", packed)
	}
}

// 便签正文必须是交付全文，且 handoff 进 front-matter —— 这正是 R1/R2 的修复点。
func TestBoardWriterNoteCarriesFullDeliverable(t *testing.T) {
	d := boardDirector(t)

	full := "前缀是客套话。\n" + strings.Repeat("证据", 800) + "\n最终结论：改 config/persist.go"
	ref := d.WriteDelegateNote("coding_agent", "delegate_coding", "加配置写回", AgentResult{
		Text:        "摘要摘要",
		Summary:     "摘要摘要",
		Deliverable: full,
		Handoff:     &blackboard.Handoff{Conclusion: "改 config/persist.go", Status: blackboard.StatusOK},
	}, nil)

	path := filepath.Join(blackboard.AgentDir(d.boardID, "coding_agent"), strings.TrimPrefix(ref, "coding_agent/")+".md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("note not written at %s: %v", path, err)
	}
	text := string(data)

	// v1 的做法是"超预算就写摘要"，于是正文只剩原文的三成。
	if !strings.Contains(text, "最终结论：改 config/persist.go") {
		t.Errorf("note body must contain the full deliverable, not a prefix summary")
	}
	if !strings.Contains(text, `"version": 2`) {
		t.Errorf("note should be schema v2:\n%.200s", text)
	}
	if !strings.Contains(text, `"conclusion": "改 config/persist.go"`) {
		t.Errorf("handoff must live in the front-matter:\n%.400s", text)
	}
}

// 没注入写入器时，delegate_browser 必须退回旧行为而不是 panic。
func TestBrowserDelegateWithoutBoardWriter(t *testing.T) {
	a := &CodingAgent{}
	// BrowserAgent 未装配：错误路径也必须是错误，不能是空返回。
	if _, err := a.browserDelegate(nil, "browser", map[string]interface{}{"task": "x"}); err == nil {
		t.Fatal("want an error when Browser-Agent is not wired")
	}
	if _, err := a.browserDelegate(nil, "coding", map[string]interface{}{"task": "x"}); err == nil {
		t.Fatal("want an error for a non-browser target")
	}
	if _, err := a.browserDelegate(nil, "browser", map[string]interface{}{}); err == nil {
		t.Fatal("want an error for a missing task parameter")
	}
}
