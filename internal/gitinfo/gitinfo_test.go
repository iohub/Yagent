package gitinfo

// gitinfo_test.go — P0.3 的闸门（docs/pause-resume-design.md §2 R6/R8、§5.2）。
//
// 这个包存在的唯一理由是"取消路径上也要拿得到仓库身份"：git_checkpoint.go 的
// OnAgentExit 曾拿着已取消的 ctx 跑 git，每条命令都失败、错误被丢弃，于是
// session 被记成"已完成"而状态文件被删。所以本文件最重要的是
// TestCollect_SurvivesCancelledContext —— 去掉 Collect 里的 WithoutCancel，
// 它必须变红；没有它，"best-effort"就只是注释。

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

var hex40 = regexp.MustCompile(`^[0-9a-f]{40}$`)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git binary not available: %v", err)
	}
}

// gitRun 在 dir 里跑一条 git 命令，失败即 fatal。
// 屏蔽用户全局配置：测试不能因为本机配了 core.hooksPath / credential helper /
// init.defaultBranch 而改变行为。
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
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

func emptyRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init", "--quiet")
	return dir
}

func repoWithCommit(t *testing.T) string {
	t.Helper()
	dir := emptyRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	gitRun(t, dir, "add", "main.go")
	gitRun(t, dir, "commit", "--quiet", "-m", "initial")
	return dir
}

func TestIsRepo(t *testing.T) {
	requireGit(t)

	if IsRepo("") {
		t.Error(`IsRepo("") = true, want false`)
	}
	if IsRepo(t.TempDir()) {
		t.Error("IsRepo(plain dir) = true, want false")
	}

	dir := repoWithCommit(t)
	if !IsRepo(dir) {
		t.Error("IsRepo(repo) = false, want true")
	}
	// 子目录里也要认得出仓库：rollout 的 cwd 可能落在项目根之外。
	sub := filepath.Join(dir, "internal")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	if !IsRepo(sub) {
		t.Error("IsRepo(subdir of repo) = false, want true")
	}
}

func TestCollect_ReadsHeadBranchAndOrigin(t *testing.T) {
	requireGit(t)
	dir := repoWithCommit(t)

	info := Collect(context.Background(), dir)
	if !hex40.MatchString(info.SHA) {
		t.Errorf("SHA = %q, want 40 hex chars", info.SHA)
	}
	if info.SHA != gitRun(t, dir, "rev-parse", "HEAD") {
		t.Errorf("SHA = %q, want the actual HEAD", info.SHA)
	}
	if info.Branch == "" || info.Branch == "HEAD" {
		t.Errorf("Branch = %q, want a branch name (detached gives \"HEAD\")", info.Branch)
	}
	// 本地仓库没有 origin 是常态，不是错误。
	if info.OriginURL != "" {
		t.Errorf("OriginURL = %q, want empty for a repo without a remote", info.OriginURL)
	}

	const origin = "https://example.invalid/yagent-test.git"
	gitRun(t, dir, "remote", "add", "origin", origin)
	if got := Collect(context.Background(), dir).OriginURL; got != origin {
		t.Errorf("OriginURL after adding a remote = %q, want %q", got, origin)
	}
}

// TestCollect_SurvivesCancelledContext 是本包存在的理由。
//
// session_meta 与终止事件都可能在 ctx 已取消时写；直接拿这个 ctx 去 spawn git
// 得到的是"全空身份 + 无人报错"，恢复侧就失去了判断"这份检查点属于哪个仓库、
// 当时停在哪个提交"的唯一凭据。去掉 Collect 里的 context.WithoutCancel，
// 这条必须变红。
func TestCollect_SurvivesCancelledContext(t *testing.T) {
	requireGit(t)
	dir := repoWithCommit(t)
	want := gitRun(t, dir, "rev-parse", "HEAD")

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	info := Collect(cancelled, dir)
	if info.SHA != want {
		t.Errorf("SHA on a cancelled ctx = %q, want %q (WithoutCancel is load-bearing)", info.SHA, want)
	}

	// 已过期（不只是取消）也必须安全返回，而不是永久阻塞。
	dead, cancelDead := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelDead()
	start := time.Now()
	if got := Collect(dead, dir); got.SHA == "" {
		t.Errorf("SHA on an expired ctx = empty, want best-effort or empty-within-budget")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("Collect on an expired ctx took %s, must stay bounded", elapsed)
	}
}

func TestCollect_BestEffortShapes(t *testing.T) {
	requireGit(t)

	// 非仓库 → 零值，且不打扰用户（一次探测失败不是故障）。
	if got := Collect(context.Background(), t.TempDir()); !got.Empty() {
		t.Errorf("Collect(non-repo) = %+v, want zero Info", got)
	}
	// 空 dir 同样。
	if got := Collect(context.Background(), ""); !got.Empty() {
		t.Errorf(`Collect("") = %+v, want zero Info`, got)
	}

	// 仓库里还没有 commit：rev-parse HEAD 必然失败，SHA 要留空而不是编一个。
	info := Collect(context.Background(), emptyRepo(t))
	if info.SHA != "" {
		t.Errorf("SHA in a repo with no commit = %q, want empty", info.SHA)
	}
}

func TestInfoEmpty(t *testing.T) {
	if !(Info{}).Empty() {
		t.Error("Info{}.Empty() = false, want true")
	}
	for name, info := range map[string]Info{
		"sha only":    {SHA: "abc"},
		"branch only": {Branch: "main"},
		"origin only": {OriginURL: "https://example.invalid"},
	} {
		if info.Empty() {
			t.Errorf("%s: Empty() = true, want false (any field set means we learned something)", name)
		}
	}
}
