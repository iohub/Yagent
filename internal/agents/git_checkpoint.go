package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"yagent/internal/config"
)

// GitCheckpointConfig holds configuration for the git checkpoint mechanism.
// (Duplicate of config.GitCheckpointConfig for use within agents package.)
type GitCheckpointConfig struct {
	Enabled               bool
	SquashOnExit          bool
	GenerateCommitMessage bool
	AgentBranchPrefix     string
	CheckpointTagPrefix   string
	StashDirtyWorktree    bool
	CleanupAgentBranch    bool
	CleanupCheckpointTags bool
	AutoMergeOnExit       bool
}

// DefaultGitCheckpointConfig returns a GitCheckpointConfig with sensible defaults.
func DefaultGitCheckpointConfig() GitCheckpointConfig {
	return GitCheckpointConfig{
		Enabled:               true,
		SquashOnExit:          true,
		GenerateCommitMessage: true,
		AgentBranchPrefix:     "agent",
		CheckpointTagPrefix:   "checkpoint/coding",
		StashDirtyWorktree:    true,
		CleanupAgentBranch:    true,
		CleanupCheckpointTags: true,
		AutoMergeOnExit:       false,
	}
}

// ConvertConfig converts config.GitCheckpointConfig to agents GitCheckpointConfig.
func ConvertConfig(c *config.GitCheckpointConfig) GitCheckpointConfig {
	if c == nil {
		return DefaultGitCheckpointConfig()
	}
	return GitCheckpointConfig{
		Enabled:               c.Enabled,
		SquashOnExit:          c.SquashOnExit,
		GenerateCommitMessage: c.GenerateCommitMessage,
		AgentBranchPrefix:     c.AgentBranchPrefix,
		CheckpointTagPrefix:   c.CheckpointTagPrefix,
		StashDirtyWorktree:    c.StashDirtyWorktree,
		CleanupAgentBranch:    c.CleanupAgentBranch,
		CleanupCheckpointTags: c.CleanupCheckpointTags,
		AutoMergeOnExit:       c.AutoMergeOnExit,
	}
}

// SessionState is persisted to .git/yagent_session.json across the agent lifecycle.
//
// 今天**没有任何代码读这个文件**（只有 persistState 写、clearState 删）。它是一份
// 给人看的证据：一次被中止的 run 留下了哪个分支、哪些 tag、为什么停。
// docs/pause-resume-design.md 的 P2 阶段会把它接进恢复侧。
type SessionState struct {
	SessionID   string    `json:"session_id"`
	UserBranch  string    `json:"user_branch"`
	AgentBranch string    `json:"agent_branch"`
	StepCount   int       `json:"step_count"`
	StashRef    string    `json:"stash_ref,omitempty"`
	StartTime   time.Time `json:"start_time"`
	TaskSummary string    `json:"task_summary"`
	Completed   bool      `json:"completed"`
	// Interrupted 与 Completed 互斥且都必须写：只写 Completed=false 分不出
	// "干净跑完但没提交"与"半途被打断"，而只有后者值得恢复。
	Interrupted bool     `json:"interrupted,omitempty"`
	StopReason  string   `json:"stop_reason,omitempty"`
	Checkpoints []string `json:"checkpoints"`
}

// StepInfo holds information about a single agent step.
type StepInfo struct {
	StepNumber int
	ToolName   string
	ToolInput  map[string]interface{}
	Success    bool
}

// CheckpointInfo holds metadata about a single checkpoint.
type CheckpointInfo struct {
	Tag     string
	Hash    string
	Date    string
	Message string
}

// GitCheckpointManager manages git checkpoints during agent execution.
type GitCheckpointManager struct {
	config             GitCheckpointConfig
	projectPath        string
	session            SessionState
	mu                 sync.Mutex
	ended              bool
	llmCommitMsgFn     func(ctx context.Context, diff string, taskSummary string) (string, error)
	checkpointMessages []string
}

// NewGitCheckpointManager creates a new GitCheckpointManager.
func NewGitCheckpointManager(
	cfg GitCheckpointConfig,
	projectPath string,
	taskSummary string,
	llmFn func(ctx context.Context, diff string, taskSummary string) (string, error),
) *GitCheckpointManager {
	return &GitCheckpointManager{
		config:      cfg,
		projectPath: projectPath,
		session: SessionState{
			SessionID:   fmt.Sprintf("coding-%s", time.Now().Format("20060102150405")),
			StartTime:   time.Now(),
			TaskSummary: taskSummary,
			Checkpoints: []string{},
		},
		llmCommitMsgFn:     llmFn,
		checkpointMessages: []string{},
	}
}

// OnAgentStart is called once before the agent loop begins.
// It creates/stashes and switches to an agent branch.
func (g *GitCheckpointManager) OnAgentStart(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.session.Completed {
		return nil // Already completed, skip
	}

	// 1. Get current branch
	userBranch, err := g.getCurrentBranch(ctx)
	if err != nil {
		return fmt.Errorf("git_checkpoint: failed to get current branch: %w", err)
	}
	g.session.UserBranch = userBranch

	// 2. Stash dirty worktree if needed
	dirty, err := g.isWorktreeDirty(ctx)
	if err != nil {
		return fmt.Errorf("git_checkpoint: failed to check dirty status: %w", err)
	}

	if dirty && g.config.StashDirtyWorktree {
		stashRef := fmt.Sprintf("yagent-stash-%s", g.session.SessionID)
		slog.Info("Git Checkpoint: Stashing dirty worktree", "stash", stashRef)
		_, err := g.runGitCommand(ctx, "stash", "push", "-m", stashRef, "--include-untracked")
		if err != nil {
			slog.Warn("Git Checkpoint: stash push failed", "error", err)
		} else {
			g.session.StashRef = stashRef
		}
	}

	// 3. Create and switch to agent branch
	agentBranch := fmt.Sprintf("%s/%s", g.config.AgentBranchPrefix, g.session.SessionID)
	g.session.AgentBranch = agentBranch

	if err := g.createAndSwitchBranch(ctx, agentBranch); err != nil {
		// Try to restore stash before returning error
		if g.session.StashRef != "" {
			g.stashPop(ctx)
		}
		return fmt.Errorf("git_checkpoint: failed to create agent branch: %w", err)
	}

	// 4. Pop stash if we stashed
	if g.session.StashRef != "" {
		_, err := g.runGitCommand(ctx, "stash", "pop")
		if err != nil {
			slog.Warn("Git Checkpoint: stash pop failed", "error", err)
		}
	}

	// 5. Persist state
	if err := g.persistState(); err != nil {
		slog.Warn("Git Checkpoint: failed to persist state", "error", err)
	}

	slog.Info("Git Checkpoint: Agent started", "session", g.session.SessionID, "branch", agentBranch)
	return nil
}

// exitCleanupBudget 是收尾的总时限。context.WithoutCancel 会把父 ctx 的 deadline
// 一起丢掉（否则取消路径上一条命令都跑不了），所以必须自己上一个界：无限期的收尾
// 会变成"停止之后进程不退"。
const exitCleanupBudget = 2 * time.Minute

// OnAgentExit is called once after the agent loop ends.
// It performs squash merge, cleanup, and final commit.
//
// **递进来的 ctx 很可能已经被取消了** —— executor.go 是在 deferred 块里调用它的
// （executor.go:333）。以前这些 git 命令直接跑在那个死 ctx 上：每条都失败、错误被
// `_, _ =` 丢弃，然而代码仍然无条件 `Completed = true`、清 tag、删状态文件。
// 表现不是报错，是"一个半途而废的 run 被记成干净收尾，而它的中间历史已经被抹掉"。
// 所以这里先换一个干净的 ctx，再把中断与完成分成两条路走。
func (g *GitCheckpointManager) OnAgentExit(ctx context.Context, agentErr error) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), exitCleanupBudget)
	defer cancel()

	interrupted := ctx.Err() != nil || agentErr != nil

	g.mu.Lock()
	if g.ended {
		g.mu.Unlock()
		return nil // Prevent double-processing
	}
	g.ended = true
	g.session.Completed = !interrupted
	g.session.Interrupted = interrupted
	g.session.StopReason = exitReason(ctx, agentErr)
	g.mu.Unlock()

	if interrupted {
		return g.finalizeInterrupted(cleanupCtx)
	}
	return g.finalizeCompleted(cleanupCtx)
}

// exitReason 给状态文件留一个人能读的一句话。优先 agent 的错误（panic 最具体），
// 其次是 context 的取消原因（activity 的超时错误挂在这里），最后才是裸取消。
func exitReason(ctx context.Context, agentErr error) string {
	if agentErr != nil {
		return agentErr.Error()
	}
	if ctx.Err() == nil {
		return ""
	}
	if cause := context.Cause(ctx); cause != nil && cause != ctx.Err() {
		return cause.Error()
	}
	return ctx.Err().Error()
}

// finalizeInterrupted 保全证据，不宣告完成。
//
// 三条刻意不做的事，每条都比"没收尾干净"更贵：
//   - 不 squash：把逐步提交压成一坨，抹掉的正是"它停在哪一步"这条信息本身；
//   - 不 merge 到用户分支：AutoMergeOnExit 开着时，一次半途而废的 run 会把
//     没做完的改动直接落进用户的工作分支；
//   - 不清 tag、不删状态文件：那是恢复侧唯一的线索。
//
// 提交前先确认 HEAD 还在 agent 分支上。不确认的代价是不可逆的：委派链上任何一处
// 切过分支，`git add -A` 就会把半成品提交进**用户的**分支。
func (g *GitCheckpointManager) finalizeInterrupted(ctx context.Context) error {
	slog.Warn("Git Checkpoint: run interrupted — preserving agent branch, tags and session state",
		"session", g.session.SessionID,
		"agent_branch", g.session.AgentBranch,
		"stop_reason", g.session.StopReason,
	)

	if g.session.AgentBranch == "" {
		// OnAgentStart 没走到建分支那一步（它自己失败就会走到这里）。
		// 此时工作区里的改动归属于用户，绝不能由收尾来提交。
		return fmt.Errorf("git_checkpoint: interrupted before the agent branch was created (%s)", g.session.StopReason)
	}

	head, err := g.getCurrentBranch(ctx)
	if err != nil {
		if perr := g.persistState(); perr != nil {
			return fmt.Errorf("git_checkpoint: interrupted, branch check failed: %w (state persist failed: %v)", err, perr)
		}
		return fmt.Errorf("git_checkpoint: interrupted, cannot verify branch before committing: %w", err)
	}
	if head != g.session.AgentBranch {
		// 分支都不同，绝不能 add -A：那会把别人的工作当成 agent 的产出。
		if perr := g.persistState(); perr != nil {
			return fmt.Errorf("git_checkpoint: HEAD on %q not agent branch %q, and state persist failed: %w",
				head, g.session.AgentBranch, perr)
		}
		slog.Warn("Git Checkpoint: skipped interrupted commit — HEAD is not on the agent branch",
			"head", head, "agent_branch", g.session.AgentBranch)
		return nil
	}

	dirty, derr := g.isWorktreeDirty(ctx)
	if derr != nil {
		if perr := g.persistState(); perr != nil {
			return fmt.Errorf("git_checkpoint: dirty check failed: %w (state persist failed: %v)", derr, perr)
		}
		return fmt.Errorf("git_checkpoint: interrupted, cannot check worktree: %w", derr)
	}
	if !dirty {
		if perr := g.persistState(); perr != nil {
			return fmt.Errorf("git_checkpoint: worktree clean but state persist failed: %w", perr)
		}
		return nil
	}

	if _, err := g.runGitCommand(ctx, "add", "-A"); err != nil {
		_ = g.persistState()
		return fmt.Errorf("git_checkpoint: interrupted commit stage failed: %w", err)
	}
	msg := fmt.Sprintf("checkpoint: interrupted — %s", g.session.StopReason)
	if _, err := g.runGitCommand(ctx, "commit", "-m", msg); err != nil {
		_ = g.persistState()
		return fmt.Errorf("git_checkpoint: interrupted commit failed: %w", err)
	}
	if err := g.persistState(); err != nil {
		return fmt.Errorf("git_checkpoint: committed interrupted work but state persist failed: %w", err)
	}
	slog.Info("Git Checkpoint: interrupted work committed on agent branch",
		"agent_branch", g.session.AgentBranch, "commit_message", msg)
	return nil
}

// finalizeCompleted 是原有的收尾：提交残留改动、按配置 squash、清 tag、删状态文件。
// 与之前的唯一区别是错误不再被丢弃——收尾失败必须有人说出来。
func (g *GitCheckpointManager) finalizeCompleted(ctx context.Context) error {
	slog.Info("Git Checkpoint: Agent exit — processing finalization",
		"session", g.session.SessionID,
		"auto_merge_on_exit", g.config.AutoMergeOnExit,
	)

	// 1. Commit any remaining uncommitted changes
	dirty, err := g.isWorktreeDirty(ctx)
	if err != nil {
		return fmt.Errorf("git_checkpoint: cannot check worktree before final commit: %w", err)
	}
	if dirty {
		if _, err := g.runGitCommand(ctx, "add", "-A"); err != nil {
			return fmt.Errorf("git_checkpoint: staging final changes failed: %w", err)
		}
		if _, err := g.runGitCommand(ctx, "commit", "-m", "checkpoint: uncommitted changes before exit"); err != nil {
			return fmt.Errorf("git_checkpoint: final commit failed: %w", err)
		}
	}

	// 2. Perform squash operation
	if g.config.SquashOnExit && g.session.UserBranch != "" && g.session.AgentBranch != "" {
		if g.config.AutoMergeOnExit {
			// Legacy behavior: squash-merge into user branch
			if err := g.performSquashMerge(ctx); err != nil {
				slog.Error("Git Checkpoint: squash merge failed", "error", err)
				return fmt.Errorf("git_checkpoint: squash merge failed: %w", err)
			}

			// Cleanup agent branch (legacy behavior for auto-merge)
			if g.config.CleanupAgentBranch {
				if _, err := g.runGitCommand(ctx, "branch", "-D", g.session.AgentBranch); err != nil {
					slog.Warn("Git Checkpoint: failed to delete agent branch after merge",
						"agent_branch", g.session.AgentBranch, "error", err)
				}
			}

			slog.Info("Git Checkpoint: Agent work merged into user branch",
				"user_branch", g.session.UserBranch)
		} else {
			// New default behavior: squash on agent branch, preserve it
			if err := g.squashAgentBranch(ctx); err != nil {
				slog.Error("Git Checkpoint: squash on agent branch failed", "error", err)
				return fmt.Errorf("git_checkpoint: squash on agent branch failed: %w", err)
			}

			slog.Info("Git Checkpoint: Agent branch preserved with squashed commit",
				"agent_branch", g.session.AgentBranch,
				"user_branch", g.session.UserBranch,
				"hint", fmt.Sprintf("To merge: git checkout %s && git merge --squash %s",
					g.session.UserBranch, g.session.AgentBranch))
		}
	}

	// 3. Cleanup checkpoint tags (always clean up regardless of path)
	if g.config.CleanupCheckpointTags {
		if err := g.cleanupCheckpointTags(ctx); err != nil {
			slog.Warn("Git Checkpoint: failed to clean up checkpoint tags", "error", err)
		}
	}

	// 4. Clear state file
	if _, err := g.clearState(); err != nil {
		slog.Warn("Git Checkpoint: failed to clear session state", "error", err)
	}

	slog.Info("Git Checkpoint: Agent exit cleanup complete", "session", g.session.SessionID)
	return nil
}

// ListCheckpoints returns all checkpoints for the current session.
func (g *GitCheckpointManager) ListCheckpoints(ctx context.Context) ([]CheckpointInfo, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	return g.getCheckpointInfo(ctx)
}

// GetCheckpointMessages returns a copy of all recorded checkpoint messages.
func (g *GitCheckpointManager) GetCheckpointMessages() []string {
	g.mu.Lock()
	defer g.mu.Unlock()

	result := make([]string, len(g.checkpointMessages))
	copy(result, g.checkpointMessages)
	return result
}

// RollbackToCheckpoint rolls back the working tree to a specific checkpoint.
func (g *GitCheckpointManager) RollbackToCheckpoint(ctx context.Context, tag string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	// Verify the tag belongs to current session
	if !strings.Contains(tag, g.session.SessionID) {
		return fmt.Errorf("git_checkpoint: tag %s does not belong to current session %s", tag, g.session.SessionID)
	}

	// Reset hard to the checkpoint
	_, err := g.runGitCommand(ctx, "reset", "--hard", tag)
	if err != nil {
		return fmt.Errorf("git_checkpoint: reset failed: %w", err)
	}

	// Remove checkpoints after the rolled-back tag
	var newCheckpoints []string
	found := false
	for _, cp := range g.session.Checkpoints {
		if found {
			break
		}
		newCheckpoints = append(newCheckpoints, cp)
		if cp == tag {
			found = true
		}
	}
	g.session.Checkpoints = newCheckpoints

	// Persist state
	if err := g.persistState(); err != nil {
		slog.Warn("Git Checkpoint: failed to persist state after rollback", "error", err)
	}

	slog.Info("Git Checkpoint: Rolled back to checkpoint", "tag", tag)
	return nil
}

// CreateManualCheckpoint creates a checkpoint at the current state.
func (g *GitCheckpointManager) CreateManualCheckpoint(ctx context.Context, message string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.session.Completed || g.ended {
		return "", fmt.Errorf("git_checkpoint: agent session already ended")
	}

	dirty, err := g.isWorktreeDirty(ctx)
	if err != nil {
		return "", fmt.Errorf("git_checkpoint: failed to check dirty status: %w", err)
	}
	if !dirty {
		return "", fmt.Errorf("git_checkpoint: no changes to checkpoint")
	}

	// Commit
	_, err = g.runGitCommand(ctx, "add", "-A")
	if err != nil {
		return "", fmt.Errorf("git_checkpoint: add failed: %w", err)
	}

	commitMsg := fmt.Sprintf("checkpoint: manual — %s", message)
	_, err = g.runGitCommand(ctx, "commit", "-m", commitMsg)
	if err != nil {
		return "", fmt.Errorf("git_checkpoint: commit failed: %w", err)
	}

	// Create tag
	tag := fmt.Sprintf("%s/%s-manual-%d", g.config.CheckpointTagPrefix, g.session.SessionID, time.Now().Unix())
	_, err = g.runGitCommand(ctx, "tag", tag)
	if err != nil {
		return "", fmt.Errorf("git_checkpoint: tag creation failed: %w", err)
	}

	g.session.Checkpoints = append(g.session.Checkpoints, tag)

	// Record checkpoint message
	g.checkpointMessages = append(g.checkpointMessages, commitMsg)

	if err := g.persistState(); err != nil {
		slog.Warn("Git Checkpoint: failed to persist state", "error", err)
	}

	slog.Info("Git Checkpoint: Manual checkpoint created", "tag", tag, "message", message)
	return tag, nil
}

// ─── Internal git operations ───

func (g *GitCheckpointManager) getCurrentBranch(ctx context.Context) (string, error) {
	out, err := g.runGitCommand(ctx, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (g *GitCheckpointManager) isWorktreeDirty(ctx context.Context) (bool, error) {
	out, err := g.runGitCommand(ctx, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

func (g *GitCheckpointManager) switchBranch(ctx context.Context, branch string) error {
	_, err := g.runGitCommand(ctx, "checkout", branch)
	return err
}

func (g *GitCheckpointManager) createAndSwitchBranch(ctx context.Context, branch string) error {
	// Check if branch already exists, if so delete it
	existing, err := g.runGitCommand(ctx, "rev-parse", "--verify", branch)
	if err == nil && strings.TrimSpace(existing) != "" {
		_, _ = g.runGitCommand(ctx, "branch", "-D", branch)
	}

	_, err = g.runGitCommand(ctx, "checkout", "-b", branch)
	return err
}

func (g *GitCheckpointManager) stashPop(ctx context.Context) error {
	_, err := g.runGitCommand(ctx, "stash", "pop")
	return err
}

func (g *GitCheckpointManager) cleanupCheckpointTags(ctx context.Context) error {
	// List all tags matching our prefix/session
	out, err := g.runGitCommand(ctx, "tag", "-l", fmt.Sprintf("%s/%s-*", g.config.CheckpointTagPrefix, g.session.SessionID))
	if err != nil {
		return err
	}

	tags := strings.Fields(out)
	for _, tag := range tags {
		_, _ = g.runGitCommand(ctx, "tag", "-d", tag)
	}
	return nil
}

func (g *GitCheckpointManager) getAgentDiff(ctx context.Context) (string, error) {
	out, err := g.runGitCommand(ctx, "diff", g.session.UserBranch+"..."+g.session.AgentBranch)
	if err != nil {
		return "", err
	}
	// Truncate to ~8000 characters
	diffStr := out
	if len(diffStr) > 8000 {
		diffStr = diffStr[:8000] + "\n... [truncated]"
	}
	return diffStr, nil
}

func (g *GitCheckpointManager) getDiffNameStatus(ctx context.Context, base, head string) (string, error) {
	out, err := g.runGitCommand(ctx, "diff", "--name-status", base+"..."+head)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (g *GitCheckpointManager) getDiffStat(ctx context.Context, base, head string) (string, error) {
	out, err := g.runGitCommand(ctx, "diff", "--stat", base+"..."+head)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (g *GitCheckpointManager) getMergeBase(ctx context.Context) (string, error) {
	out, err := g.runGitCommand(ctx, "merge-base", "HEAD", g.session.UserBranch)
	if err != nil {
		return "", fmt.Errorf("failed to find merge-base between HEAD and %s: %w", g.session.UserBranch, err)
	}
	return strings.TrimSpace(out), nil
}

func (g *GitCheckpointManager) hasCommitsToSquash(ctx context.Context) (bool, error) {
	mergeBase, err := g.getMergeBase(ctx)
	if err != nil {
		return false, err
	}
	out, err := g.runGitCommand(ctx, "rev-parse", "HEAD")
	if err != nil {
		return false, fmt.Errorf("failed to get HEAD: %w", err)
	}
	head := strings.TrimSpace(out)
	return head != mergeBase, nil
}

// generateSquashCommitMessage builds an enriched diff and uses the LLM to
// generate a Conventional Commits–style commit message.  This logic is shared
// by both performSquashMerge and squashAgentBranch so that the message
// generation is never duplicated.
func (g *GitCheckpointManager) generateSquashCommitMessage(ctx context.Context) (string, error) {
	// Get diff
	diff, err := g.getAgentDiff(ctx)
	if err != nil {
		slog.Warn("Git Checkpoint: failed to get diff for commit message", "error", err)
		diff = "[diff unavailable]"
	}

	// Gather rich context: name-status, diffstat, checkpoint messages
	var nameStatus, diffStat string
	nameStatus, err = g.getDiffNameStatus(ctx, g.session.UserBranch, g.session.AgentBranch)
	if err != nil {
		slog.Warn("Git Checkpoint: failed to get name-status", "error", err)
	}
	diffStat, err = g.getDiffStat(ctx, g.session.UserBranch, g.session.AgentBranch)
	if err != nil {
		slog.Warn("Git Checkpoint: failed to get diff-stat", "error", err)
	}

	// Build enriched diff with additional context
	var enrichedDiff strings.Builder
	enrichedDiff.WriteString(diff)

	if nameStatus != "" {
		enrichedDiff.WriteString("\n\n")
		enrichedDiff.WriteString("<!-- git diff --name-status (base...head) -->\n")
		enrichedDiff.WriteString(nameStatus)
	}

	if diffStat != "" {
		enrichedDiff.WriteString("\n\n")
		enrichedDiff.WriteString("<!-- git diff --stat (base...head) -->\n")
		enrichedDiff.WriteString(diffStat)
	}

	// Add checkpoint messages if available
	checkpointMessages := g.GetCheckpointMessages()
	if len(checkpointMessages) > 0 {
		enrichedDiff.WriteString("\n\n")
		enrichedDiff.WriteString("<!-- checkpoint messages -->\n")
		for i, msg := range checkpointMessages {
			enrichedDiff.WriteString(fmt.Sprintf("#%d: %s\n", i+1, msg))
		}
	}

	// Generate commit message
	var commitMsg string
	if g.llmCommitMsgFn != nil && g.config.GenerateCommitMessage {
		msg, err := g.llmCommitMsgFn(ctx, enrichedDiff.String(), g.session.TaskSummary)
		if err != nil {
			slog.Warn("Git Checkpoint: LLM commit message failed, using fallback", "error", err)
		} else {
			commitMsg = msg
		}
	}

	if commitMsg == "" {
		commitMsg = g.fallbackCommitMessage(diff)
	}

	return commitMsg, nil
}

func (g *GitCheckpointManager) performSquashMerge(ctx context.Context) error {
	// Generate commit message using shared logic
	commitMsg, err := g.generateSquashCommitMessage(ctx)
	if err != nil {
		return fmt.Errorf("performSquashMerge: failed to generate commit message: %w", err)
	}

	// Switch to user branch
	if err := g.switchBranch(ctx, g.session.UserBranch); err != nil {
		return fmt.Errorf("failed to switch to user branch: %w", err)
	}

	// Squash merge
	_, err = g.runGitCommand(ctx, "merge", "--squash", g.session.AgentBranch)
	if err != nil {
		// Abort merge on conflict
		_, _ = g.runGitCommand(ctx, "merge", "--abort")
		return fmt.Errorf("squash merge conflict: %w", err)
	}

	// Commit with generated message
	_, err = g.runGitCommand(ctx, "commit", "-m", commitMsg)
	if err != nil {
		return fmt.Errorf("failed to commit squash merge: %w", err)
	}

	slog.Info("Git Checkpoint: Squash merge completed", "session", g.session.SessionID)
	return nil
}

// squashAgentBranch squashes all commits on the agent branch into a single
// professional commit.  It generates the commit message BEFORE the soft reset
// so that git log is still available for the LLM to read.
func (g *GitCheckpointManager) squashAgentBranch(ctx context.Context) error {
	// Guard: check if there are commits to squash
	hasChanges, err := g.hasCommitsToSquash(ctx)
	if err != nil {
		return fmt.Errorf("squashAgentBranch: failed to check for changes: %w", err)
	}
	if !hasChanges {
		slog.Info("Git Checkpoint: No agent commits to squash; branch unchanged")
		return nil
	}

	// Generate commit message BEFORE reset (reads git history)
	commitMsg, err := g.generateSquashCommitMessage(ctx)
	if err != nil {
		return fmt.Errorf("squashAgentBranch: failed to generate commit message: %w", err)
	}

	// Find the merge base (fork point from user branch)
	mergeBase, err := g.getMergeBase(ctx)
	if err != nil {
		return fmt.Errorf("squashAgentBranch: failed to find merge base: %w", err)
	}

	// Soft reset to merge base: collapses all commits, keeps changes staged
	_, err = g.runGitCommand(ctx, "reset", "--soft", mergeBase)
	if err != nil {
		return fmt.Errorf("squashAgentBranch: git reset --soft failed: %w", err)
	}

	// Commit all staged changes with generated message
	_, err = g.runGitCommand(ctx, "commit", "-m", commitMsg)
	if err != nil {
		return fmt.Errorf("squashAgentBranch: git commit failed: %w", err)
	}

	slog.Info("Git Checkpoint: Agent branch squashed successfully",
		"branch", g.session.AgentBranch,
		"merge_base", mergeBase,
	)
	return nil
}

func (g *GitCheckpointManager) fallbackCommitMessage(diff string) string {
	return fmt.Sprintf("coding-agent: apply task changes (diff: %d chars)", len(diff))
}

func (g *GitCheckpointManager) getCheckpointInfo(ctx context.Context) ([]CheckpointInfo, error) {
	var results []CheckpointInfo

	for _, tag := range g.session.Checkpoints {
		// Get commit hash
		hashOut, err := g.runGitCommand(ctx, "rev-list", "-1", tag)
		if err != nil {
			continue
		}

		// Get date and message
		format := "%ai %s"
		msgOut, err := g.runGitCommand(ctx, "log", "-1", "--format="+format, tag)
		if err != nil {
			continue
		}

		// Parse: date is first part, message is after
		parts := strings.SplitN(strings.TrimSpace(msgOut), " ", 2)
		date := ""
		message := strings.TrimSpace(msgOut)
		if len(parts) >= 2 {
			date = parts[0]
			message = parts[1]
		}

		results = append(results, CheckpointInfo{
			Tag:     tag,
			Hash:    strings.TrimSpace(hashOut),
			Date:    date,
			Message: message,
		})
	}

	return results, nil
}

func (g *GitCheckpointManager) persistState() error {
	statePath := filepath.Join(g.projectPath, ".git", "yagent_session.json")

	data, err := json.MarshalIndent(g.session, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session state: %w", err)
	}

	// Ensure .git directory exists (it should)
	if err := os.WriteFile(statePath, data, 0644); err != nil {
		return fmt.Errorf("write session state: %w", err)
	}

	return nil
}

func (g *GitCheckpointManager) clearState() (string, error) {
	statePath := filepath.Join(g.projectPath, ".git", "yagent_session.json")
	if err := os.Remove(statePath); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("remove session state: %w", err)
	}
	return "", nil
}

func (g *GitCheckpointManager) runGitCommand(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = g.projectPath
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w\nstderr: %s", strings.Join(args, " "), err, stderr.String())
	}
	return stdout.String(), nil
}
