package agents

// git_checkpoint_test.go — P0.2 的闸门（docs/pause-resume-design.md §2 R6、§9、§10 G7）。
//
// 全部用**真实临时仓库**：这个类的价值就在于 git 命令真的执行或真的没执行，
// 造 mock 只能测到"代码走到哪个分支"，测不到用户仓库里到底多了什么提交。
//
// 被测的缺陷：executor 在 deferred 块里把**已取消的 ctx**递给 OnAgentExit，
// 而那时每条 git 命令都失败、错误被 `_, _ =` 丢弃，代码却仍然无条件
// Completed=true、清 tag、删状态文件。表现不是报错，是一次半途而废的 run
// 被记成干净收尾，同时它的逐步历史（唯一说明"停在哪"的证据）被抹掉。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yagent/internal/agents/activity"
)

// ─── 真实仓库 fixture ────────────────────────────────────────────────────────

func gitCLI(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(stdout.String())
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git binary not available: %v", err)
	}
}

// newGitCheckpointRepo 建一个有一次提交的临时仓库，返回 (目录, 用户分支名)。
// 分支名从仓库里读出来，不硬编码 main/master —— 否则 git 版本或本机
// init.defaultBranch 一变，整组测试就会因为夹具而不是因为代码失败。
func newGitCheckpointRepo(t *testing.T) (string, string) {
	t.Helper()
	requireGit(t)
	dir := t.TempDir()
	gitCLI(t, dir, "init", "--quiet")
	gitCLI(t, dir, "config", "user.email", "yagent-test@example.invalid")
	gitCLI(t, dir, "config", "user.name", "yagent test")

	seed := filepath.Join(dir, "seed.txt")
	if err := os.WriteFile(seed, []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	gitCLI(t, dir, "add", "seed.txt")
	gitCLI(t, dir, "commit", "--quiet", "-m", "initial")
	branch := gitCLI(t, dir, "rev-parse", "--abbrev-ref", "HEAD")
	if branch == "HEAD" {
		t.Fatal("detached HEAD in a fresh repo; fixture broken")
	}
	return dir, branch
}

// startCodingSession 跑一次真实的 OnAgentStart（建 agent 分支并切过去）。
func startCodingSession(t *testing.T, dir string, cfg GitCheckpointConfig) *GitCheckpointManager {
	t.Helper()
	g := NewGitCheckpointManager(cfg, dir, "add a feature", func(context.Context, string, string) (string, error) {
		return "feat: stubbed squash message", nil
	})
	if err := g.OnAgentStart(context.Background()); err != nil {
		t.Fatalf("OnAgentStart: %v", err)
	}
	if g.session.AgentBranch == "" {
		t.Fatal("OnAgentStart did not record an agent branch")
	}
	return g
}

func touch(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func readState(t *testing.T, dir string) SessionState {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, ".git", "yagent_session.json"))
	if err != nil {
		t.Fatalf("state file missing: %v", err)
	}
	var s SessionState
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	return s
}

func commitsAhead(t *testing.T, dir, userBranch, agentBranch string) int {
	t.Helper()
	out := gitCLI(t, dir, "rev-list", "--count", userBranch+".."+agentBranch)
	var n int
	if _, err := fmt.Sscanf(out, "%d", &n); err != nil {
		t.Fatalf("parse rev-list count %q: %v", out, err)
	}
	return n
}

// ─── 中断路径 ────────────────────────────────────────────────────────────────

// TestOnAgentExit_CancelledContextStillPreservesWork 是 R6 的直接回归。
//
// ctx 已取消时收尾必须**真的执行**：半成品提交到 agent 分支、状态文件留下、
// Completed=false / Interrupted=true。改动前的实现里每条 git 命令都失败而被丢弃，
// clearState 不依赖 ctx 所以反而成功了 —— 净效果是"证据被删、历史没保住、还宣布完成"。
func TestOnAgentExit_CancelledContextStillPreservesWork(t *testing.T) {
	cfg := DefaultGitCheckpointConfig()
	dir, userBranch := newGitCheckpointRepo(t)
	g := startCodingSession(t, dir, cfg)
	agentBranch := g.session.AgentBranch

	// 中途手动打一个检查点，制造一个 tag 与一次提交。
	// 顺序很重要：CreateManualCheckpoint 在工作区干净时直接报"no changes"。
	touch(t, dir, "checkpointed.go", "package p\n")
	if _, err := g.CreateManualCheckpoint(context.Background(), "mid-task"); err != nil {
		t.Fatalf("CreateManualCheckpoint: %v", err)
	}
	touch(t, dir, "work_in_progress.go", "package p\n")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := g.OnAgentExit(ctx, nil); err != nil {
		t.Fatalf("OnAgentExit on a cancelled ctx returned an error: %v", err)
	}

	// 状态文件必须还在，并且如实记录"这是被打断的"。
	state := readState(t, dir)
	if state.Completed {
		t.Error("Completed = true after an interrupted run — 这正是本次要修的谎报")
	}
	if !state.Interrupted {
		t.Error("Interrupted = false, want true")
	}
	if state.AgentBranch != agentBranch {
		t.Errorf("AgentBranch = %q, want %q", state.AgentBranch, agentBranch)
	}

	// 半成品必须落在 agent 分支上，并且能从提交里看出是被打断的。
	subject := gitCLI(t, dir, "log", "-1", "--format=%s", agentBranch)
	if !strings.Contains(subject, "interrupted") {
		t.Errorf("agent branch HEAD subject = %q, want it to say the run was interrupted", subject)
	}
	if !strings.Contains(gitCLI(t, dir, "show", "--name-only", "--format=", "HEAD"), "work_in_progress.go") {
		t.Error("the interrupted commit does not contain the work in progress")
	}

	// tag 是"停在哪一步"的坐标，中断路径不能清。
	if tags := gitCLI(t, dir, "tag", "-l"); tags == "" {
		t.Error("checkpoint tags were cleaned up on the interrupted path")
	}

	// 用户分支不能多出一个提交。
	if got := gitCLI(t, dir, "rev-parse", userBranch); got == "" {
		t.Fatal("user branch disappeared")
	}
	if n := commitsAhead(t, dir, userBranch, userBranch); n != 0 {
		t.Errorf("user branch moved ahead of itself: %d", n)
	}
}

// TestOnAgentExit_InterruptedByPanicNeverMergesIntoUserBranch 覆盖更危险的那一半：
// ctx 还活着、但 run 因为 panic 结束。改动前的代码在这条路上会**真的**执行
// squash-merge（AutoMergeOnExit 开着时），把没做完的改动直接落进用户的工作分支。
func TestOnAgentExit_InterruptedByPanicNeverMergesIntoUserBranch(t *testing.T) {
	cfg := DefaultGitCheckpointConfig()
	cfg.AutoMergeOnExit = true // 遗留行为：退出时合并进用户分支
	dir, userBranch := newGitCheckpointRepo(t)
	before := gitCLI(t, dir, "rev-parse", userBranch)

	g := startCodingSession(t, dir, cfg)
	touch(t, dir, "half_done.go", "package p\n")

	// panic 由 executor 转成 agentErr，ctx 本身没被取消。
	panicked := context.Background()
	if err := g.OnAgentExit(panicked, errors.New("agent panic: boom")); err != nil {
		t.Fatalf("OnAgentExit: %v", err)
	}

	if after := gitCLI(t, dir, "rev-parse", userBranch); after != before {
		t.Errorf("user branch moved from %s to %s — 半途而废的 run 被合并进了用户的分支", before, after)
	}
	if strings.Contains(gitCLI(t, dir, "ls-tree", "-r", "--name-only", userBranch), "half_done.go") {
		t.Error("the unfinished file landed on the user branch")
	}
	state := readState(t, dir)
	if state.Completed || !state.Interrupted {
		t.Errorf("state = completed:%v interrupted:%v, want the panic recorded as an interruption",
			state.Completed, state.Interrupted)
	}
	if !strings.Contains(state.StopReason, "panic") {
		t.Errorf("StopReason = %q, want it to name the panic", state.StopReason)
	}
}

// TestOnAgentExit_InterruptedKeepsStepHistory 钉住"不 squash"：
// 逐步提交是唯一能回答"它跑到哪一步"的东西，压成一坨正好抹掉恢复需要的信息。
//
// 这里只打一个手动检查点而不是两个：CreateManualCheckpoint 的 tag 名取
// `time.Now().Unix()`，同一秒内的第二个检查点会撞 tag 名直接失败
// （那是 git_checkpoint.go 自己的一个既有缺陷，与本条断言无关，见提交说明）。
func TestOnAgentExit_InterruptedKeepsStepHistory(t *testing.T) {
	cfg := DefaultGitCheckpointConfig()
	dir, userBranch := newGitCheckpointRepo(t)
	g := startCodingSession(t, dir, cfg)

	touch(t, dir, "a.txt", "step one\n")
	if _, err := g.CreateManualCheckpoint(context.Background(), "step one"); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	touch(t, dir, "unfinished.txt", "still working\n")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := g.OnAgentExit(ctx, nil); err != nil {
		t.Fatalf("OnAgentExit: %v", err)
	}

	subjects := gitCLI(t, dir, "log", "--format=%s", g.session.AgentBranch)
	if !strings.Contains(subjects, "checkpoint: manual — step one") {
		t.Errorf("the mid-task checkpoint commit is gone; history = %q", subjects)
	}
	if !strings.Contains(subjects, "interrupted") {
		t.Errorf("the interrupted commit is missing; history = %q", subjects)
	}
	// squash 会把上面两条压成一条 stub message 的提交 —— 那正是恢复最怕的丢信息。
	if n := commitsAhead(t, dir, userBranch, g.session.AgentBranch); n < 2 {
		t.Errorf("agent branch is %d commits ahead, want >= 2 (squash 会把它压成 1)", n)
	}
}

// TestOnAgentExit_InterruptedNeverCommitsWhenHeadIsNotOnAgentBranch 是安全阀：
// HEAD 不在 agent 分支上时 `git add -A` 提交的是**别人的**工作。
func TestOnAgentExit_InterruptedNeverCommitsWhenHeadIsNotOnAgentBranch(t *testing.T) {
	cfg := DefaultGitCheckpointConfig()
	dir, userBranch := newGitCheckpointRepo(t)
	g := startCodingSession(t, dir, cfg)
	before := gitCLI(t, dir, "rev-parse", userBranch)

	// 模拟任何让 HEAD 离开 agent 分支的路径。
	gitCLI(t, dir, "checkout", "--quiet", userBranch)
	touch(t, dir, "not_mine.go", "package p\n")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := g.OnAgentExit(ctx, nil); err != nil {
		t.Fatalf("OnAgentExit: %v", err)
	}

	if after := gitCLI(t, dir, "rev-parse", userBranch); after != before {
		t.Errorf("user branch advanced: %s -> %s", before, after)
	}
	if out := gitCLI(t, dir, "status", "--porcelain"); !strings.Contains(out, "not_mine.go") {
		t.Errorf("the user's uncommitted file was consumed; status = %q", out)
	}
	// 状态文件仍然要留：恢复侧需要知道这一轮被打断了。
	if _, err := os.Stat(filepath.Join(dir, ".git", "yagent_session.json")); err != nil {
		t.Errorf("state file missing after an interruption: %v", err)
	}
}

// TestOnAgentExit_WithoutAnAgentBranchReportsFailure 覆盖 OnAgentStart 半路失败的情况：
// 那时既没有分支也没有权限提交，静默返回 nil 等于把用户的脏工作区当成已收尾。
func TestOnAgentExit_WithoutAnAgentBranchReportsFailure(t *testing.T) {
	cfg := DefaultGitCheckpointConfig()
	dir, userBranch := newGitCheckpointRepo(t)
	before := gitCLI(t, dir, "rev-parse", userBranch)

	g := NewGitCheckpointManager(cfg, dir, "task", nil)
	touch(t, dir, "dirty.txt", "x\n")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := g.OnAgentExit(ctx, nil)
	if err == nil {
		t.Fatal("OnAgentExit reported success without ever having an agent branch")
	}
	if after := gitCLI(t, dir, "rev-parse", userBranch); after != before {
		t.Errorf("user branch advanced even though no agent branch existed: %s -> %s", before, after)
	}
}

// ─── 完成路径（不能因为这次改动而退化）────────────────────────────────────────

func TestOnAgentExit_CompletedRunStillCleansUp(t *testing.T) {
	cfg := DefaultGitCheckpointConfig()
	dir, userBranch := newGitCheckpointRepo(t)
	g := startCodingSession(t, dir, cfg)

	touch(t, dir, "done_a.txt", "a\n")
	if _, err := g.CreateManualCheckpoint(context.Background(), "a"); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	touch(t, dir, "done_b.txt", "b\n")

	if err := g.OnAgentExit(context.Background(), nil); err != nil {
		t.Fatalf("OnAgentExit: %v", err)
	}

	// 状态文件应当被清掉（干净收尾）。
	if _, err := os.Stat(filepath.Join(dir, ".git", "yagent_session.json")); err == nil {
		t.Error("session state survived a clean completion")
	}
	// 默认不自动合并：agent 分支保留、压成一个提交。
	if n := commitsAhead(t, dir, userBranch, g.session.AgentBranch); n != 1 {
		t.Errorf("agent branch is %d commits ahead, want 1 after squashing", n)
	}
	if out := gitCLI(t, dir, "rev-parse", userBranch); out == "" {
		t.Error("user branch vanished")
	}
}

// TestOnAgentExit_DoubleCallIsIdempotent 钉住 ended 守卫：第二次调用不得再动仓库。
func TestOnAgentExit_DoubleCallIsIdempotent(t *testing.T) {
	cfg := DefaultGitCheckpointConfig()
	dir, _ := newGitCheckpointRepo(t)
	g := startCodingSession(t, dir, cfg)
	touch(t, dir, "x.txt", "x\n")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := g.OnAgentExit(ctx, nil); err != nil {
		t.Fatalf("first OnAgentExit: %v", err)
	}
	head := gitCLI(t, dir, "rev-parse", g.session.AgentBranch)
	if err := g.OnAgentExit(ctx, nil); err != nil {
		t.Fatalf("second OnAgentExit: %v", err)
	}
	if after := gitCLI(t, dir, "rev-parse", g.session.AgentBranch); after != head {
		t.Errorf("second call advanced the branch: %s -> %s", head, after)
	}
}

// ─── 原因归类 ────────────────────────────────────────────────────────────────

func TestExitReason(t *testing.T) {
	canceled, cancelCanceled := context.WithCancel(context.Background())
	cancelCanceled()
	defer cancelCanceled()

	idleCause := &activity.IdleTimeoutError{IdleFor: time.Minute, Threshold: time.Minute}
	causeCtx, cancelCause := context.WithCancelCause(context.Background())
	defer cancelCause(nil)
	cancelCause(idleCause)

	cases := []struct {
		name    string
		ctx     context.Context
		agent   error
		wantHas string
	}{
		{"活 ctx 无错误", context.Background(), nil, ""}, // 空串：完成路径不写原因
		{"agent 错误优先", canceled, errors.New("agent panic: boom"), "panic"},
		{"裸取消", canceled, nil, "context canceled"},
		{"cause 比裸取消更有信息量", causeCtx, nil, "no activity for"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := exitReason(tc.ctx, tc.agent)
			if tc.wantHas == "" {
				if got != "" {
					t.Errorf("exitReason = %q, want empty", got)
				}
				return
			}
			if !strings.Contains(got, tc.wantHas) {
				t.Errorf("exitReason = %q, want it to contain %q", got, tc.wantHas)
			}
		})
	}
}

// TestExitCleanupBudgetIsBounded 保证收尾不是无限期的：
// WithoutCancel 会把 deadline 一起丢掉，没有自己的预算就成了"停止之后进程不退"。
func TestExitCleanupBudgetIsBounded(t *testing.T) {
	if exitCleanupBudget <= 0 || exitCleanupBudget > 10*time.Minute {
		t.Errorf("exitCleanupBudget = %s, want a bounded but generous value", exitCleanupBudget)
	}
}
