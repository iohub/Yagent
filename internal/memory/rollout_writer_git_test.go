package memory

// rollout_writer_git_test.go — P0.3 的闸门（docs/pause-resume-design.md §2 R8、§5.2）。
//
// SessionMeta.Git 这个字段在 rollout_types.go 里声明了好几年，两个写入点
//（executor 的子 Agent 会话、planner 的 Director 会话）都没填过，全仓唯一构造
// GitInfo 的地方是本包自己的测试。于是恢复侧读到的永远是"不知道仓库是哪来的"。
//
// 补齐动作放在 WriteSessionMeta 的兜底里（与 task_id / board_id 同一套约定），
// 所以本测试断言的是"调用点什么都没传，盘上却有身份"——它同时钉住 writer 的兜底
// 与 reader 的回读，缺一半都测不到恢复路径真正需要的东西。

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"yagent/internal/llm"
)

// isolateHomeForRolloutTest 把 $HOME 指到临时目录，避免把测试文件写进真实的
// ~/.yagent/data/rollout（本包没有 TestMain 做这件事，见文件末注释）。
func isolateHomeForRolloutTest(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git binary not available: %v", err)
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=yagent-test",
		"GIT_AUTHOR_EMAIL=yagent-test@example.invalid",
		"GIT_COMMITTER_NAME=yagent-test",
		"GIT_COMMITTER_EMAIL=yagent-test@example.invalid",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(stdout.String())
}

// testRepo 建一个有一次提交、带 origin 的仓库，返回 (目录, HEAD sha)。
func testRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	gitIn(t, dir, "init", "--quiet")
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	gitIn(t, dir, "add", "main.go")
	gitIn(t, dir, "commit", "--quiet", "-m", "initial")
	gitIn(t, dir, "remote", "add", "origin", "https://example.invalid/pause-resume.git")
	return dir, gitIn(t, dir, "rev-parse", "HEAD")
}

func TestWriteSessionMeta_BackfillsGitIdentity(t *testing.T) {
	isolateHomeForRolloutTest(t)
	repo, sha := testRepo(t)

	w, err := NewRolloutWriter("coding", "task-git-backfill", "git-backfill-proj")
	if err != nil || !w.Enabled() {
		t.Skipf("rollout writer unavailable: %v", err)
	}
	// 调用点只传 Cwd —— 生产里两个写入点正是如此。
	if err := w.WriteSessionMeta(SessionMeta{
		ID:        w.SessionID(),
		SessionID: w.SessionID(),
		Cwd:       repo,
	}); err != nil {
		t.Fatalf("WriteSessionMeta: %v", err)
	}
	items := LLMMessageToResponseItems(llm.Message{Role: llm.RoleUser, Content: "resume me"}, w.NextMessageID())
	for _, item := range items {
		if werr := w.WriteResponseItem(item); werr != nil {
			t.Fatalf("WriteResponseItem: %v", werr)
		}
	}
	path := w.FilePath()
	if cerr := w.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}

	// 盘上直接看 envelope，确认写出的键名就是 reader 认的那些。
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read rollout: %v", err)
	}
	var env RolloutEnvelope
	if err := json.Unmarshal([]byte(firstLine(t, string(raw))), &env); err != nil {
		t.Fatalf("unmarshal first envelope: %v", err)
	}
	if env.Type != "session_meta" {
		t.Fatalf("first envelope type = %q, want session_meta", env.Type)
	}
	payload, _ := json.Marshal(env.Payload)
	var back SessionMeta
	if err := json.Unmarshal(payload, &back); err != nil {
		t.Fatalf("re-decode payload: %v", err)
	}
	if back.Git.SHA != sha {
		t.Errorf("written git.sha = %q, want %q", back.Git.SHA, sha)
	}
	if back.Git.Branch == "" || back.Git.Branch == "HEAD" {
		t.Errorf("written git.branch = %q, want a branch name", back.Git.Branch)
	}
	if back.Git.OriginURL == "" {
		t.Errorf("written git.origin_url = empty, want the remote (cross-machine resume keys on it)")
	}

	// 读回：恢复侧真正消费的是 RolloutInfo 的三个字段。
	_, info, err := ReadRolloutMemory(path, DefaultRolloutReadOptions())
	if err != nil {
		t.Fatalf("ReadRolloutMemory: %v", err)
	}
	if info.GitSHA != sha {
		t.Errorf("RolloutInfo.GitSHA = %q, want %q", info.GitSHA, sha)
	}
	if info.GitBranch == "" {
		t.Error("RolloutInfo.GitBranch = empty, want the branch back")
	}
	if info.GitOrigin == "" {
		t.Error("RolloutInfo.GitOrigin = empty, want the origin back")
	}
}

func TestWriteSessionMeta_KeepsExplicitGit(t *testing.T) {
	isolateHomeForRolloutTest(t)
	repo, _ := testRepo(t)

	w, err := NewRolloutWriter("coding", "task-git-explicit", "git-explicit-proj")
	if err != nil || !w.Enabled() {
		t.Skipf("rollout writer unavailable: %v", err)
	}
	defer w.Close()

	// 调用点显式传了值就必须原样落盘：兜底不能把"我告诉你的"覆盖成"我自己猜的"。
	want := GitInfo{SHA: "deadbeef", Branch: "detached-probe", OriginURL: "ssh://explicit"}
	if err := w.WriteSessionMeta(SessionMeta{Cwd: repo, Git: want}); err != nil {
		t.Fatalf("WriteSessionMeta: %v", err)
	}
	raw, err := os.ReadFile(w.FilePath())
	if err != nil {
		t.Fatalf("read rollout: %v", err)
	}
	var env RolloutEnvelope
	if err := json.Unmarshal([]byte(firstLine(t, string(raw))), &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	payload, _ := json.Marshal(env.Payload)
	var back SessionMeta
	if err := json.Unmarshal(payload, &back); err != nil {
		t.Fatalf("re-decode payload: %v", err)
	}
	if back.Git != want {
		t.Errorf("git = %+v, want the caller's %+v unchanged", back.Git, want)
	}
}

func TestWriteSessionMeta_NonRepoCwdStaysEmpty(t *testing.T) {
	isolateHomeForRolloutTest(t)

	w, err := NewRolloutWriter("coding", "task-git-nonrepo", "git-nonrepo-proj")
	if err != nil || !w.Enabled() {
		t.Skipf("rollout writer unavailable: %v", err)
	}
	defer w.Close()

	// 非仓库工作区是合法运行形态（yagent 不要求 git）。既不能报错，也不能编造身份。
	if err := w.WriteSessionMeta(SessionMeta{Cwd: t.TempDir()}); err != nil {
		t.Fatalf("WriteSessionMeta on a non-repo cwd returned an error: %v", err)
	}
	raw, err := os.ReadFile(w.FilePath())
	if err != nil {
		t.Fatalf("read rollout: %v", err)
	}
	var env RolloutEnvelope
	if err := json.Unmarshal([]byte(firstLine(t, string(raw))), &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	payload, _ := json.Marshal(env.Payload)
	var back SessionMeta
	if err := json.Unmarshal(payload, &back); err != nil {
		t.Fatalf("re-decode payload: %v", err)
	}
	if !back.Git.Empty() {
		t.Errorf("git = %+v for a non-repo cwd, want every field omitted", back.Git)
	}
}

// TestWriteSessionMeta_MissingCwdProbesNothing 钉住"context.WithoutCancel + 子进程"
// 不会在缺少 cwd 时悄悄跑一次探测。Cwd 为空时 os/exec 会在**测试二进制自己的**
// 工作目录里执行 git —— 那会把 yagent 仓库的身份写进一份本该没有身份的 rollout。
func TestWriteSessionMeta_MissingCwdProbesNothing(t *testing.T) {
	isolateHomeForRolloutTest(t)

	w, err := NewRolloutWriter("coding", "task-git-nocwd", "git-nocwd-proj")
	if err != nil || !w.Enabled() {
		t.Skipf("rollout writer unavailable: %v", err)
	}
	defer w.Close()

	if err := w.WriteSessionMeta(SessionMeta{}); err != nil {
		t.Fatalf("WriteSessionMeta: %v", err)
	}
	if _, info, _ := ReadRolloutMemory(w.FilePath(), DefaultRolloutReadOptions()); info.GitSHA != "" {
		t.Errorf("GitSHA = %q with no cwd in the meta, want empty (probed the wrong directory)", info.GitSHA)
	}
}

func firstLine(t *testing.T, content string) string {
	t.Helper()
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) != "" {
			return line
		}
	}
	t.Fatalf("rollout file has no non-empty line: %q", content)
	return ""
}
