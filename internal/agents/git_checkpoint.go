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

	"codeactor/internal/config"
)

// GitCheckpointConfig holds configuration for the git checkpoint mechanism.
// (Duplicate of config.GitCheckpointConfig for use within agents package.)
type GitCheckpointConfig struct {
	Enabled                bool
	AutoCheckpoint         bool
	CheckpointInterval     int
	MaxCheckpoints         int
	SquashOnExit           bool
	GenerateCommitMessage  bool
	AgentBranchPrefix      string
	CheckpointTagPrefix    string
	StashDirtyWorktree     bool
	CleanupAgentBranch     bool
	CleanupCheckpointTags  bool
}

// DefaultGitCheckpointConfig returns a GitCheckpointConfig with sensible defaults.
func DefaultGitCheckpointConfig() GitCheckpointConfig {
	return GitCheckpointConfig{
		Enabled:                true,
		AutoCheckpoint:         true,
		CheckpointInterval:     1,
		MaxCheckpoints:         50,
		SquashOnExit:           true,
		GenerateCommitMessage:  true,
		AgentBranchPrefix:      "agent/coding",
		CheckpointTagPrefix:    "checkpoint/coding",
		StashDirtyWorktree:     true,
		CleanupAgentBranch:     true,
		CleanupCheckpointTags:  true,
	}
}

// ConvertConfig converts config.GitCheckpointConfig to agents GitCheckpointConfig.
func ConvertConfig(c *config.GitCheckpointConfig) GitCheckpointConfig {
	if c == nil {
		return DefaultGitCheckpointConfig()
	}
	return GitCheckpointConfig{
		Enabled:                c.Enabled,
		AutoCheckpoint:         c.AutoCheckpoint,
		CheckpointInterval:     c.CheckpointInterval,
		MaxCheckpoints:         c.MaxCheckpoints,
		SquashOnExit:           c.SquashOnExit,
		GenerateCommitMessage:  c.GenerateCommitMessage,
		AgentBranchPrefix:      c.AgentBranchPrefix,
		CheckpointTagPrefix:    c.CheckpointTagPrefix,
		StashDirtyWorktree:     c.StashDirtyWorktree,
		CleanupAgentBranch:     c.CleanupAgentBranch,
		CleanupCheckpointTags:  c.CleanupCheckpointTags,
	}
}

// SessionState is persisted to .git/codeactor_session.json across the agent lifecycle.
type SessionState struct {
	SessionID    string    `json:"session_id"`
	UserBranch   string    `json:"user_branch"`
	AgentBranch  string    `json:"agent_branch"`
	StepCount    int       `json:"step_count"`
	StashRef     string    `json:"stash_ref,omitempty"`
	StartTime    time.Time `json:"start_time"`
	TaskSummary  string    `json:"task_summary"`
	Completed    bool      `json:"completed"`
	Checkpoints  []string  `json:"checkpoints"`
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
		config:             cfg,
		projectPath:        projectPath,
		session: SessionState{
			SessionID:   fmt.Sprintf("%s-%d", "coding", time.Now().UnixNano()),
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
		stashRef := fmt.Sprintf("codeactor-stash-%s", g.session.SessionID)
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

// OnStepEnd is called after each step's tool calls complete.
// Errors from this hook are logged but do not abort the loop.
//
// Deprecated: Auto-checkpoint creation has been removed. Use CreateManualCheckpoint
// instead. This method only logs a deprecation warning when AutoCheckpoint is true.
func (g *GitCheckpointManager) OnStepEnd(ctx context.Context, stepInfo StepInfo) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.session.Completed || g.ended {
		return nil
	}

	if g.config.AutoCheckpoint {
		slog.Warn(
			"Git Checkpoint: OnStepEnd auto-checkpoint is deprecated and will be removed. Use CreateManualCheckpoint instead.",
			"step", stepInfo.StepNumber,
			"tool", stepInfo.ToolName,
		)
	}

	return nil
}

// OnAgentExit is called once after the agent loop ends.
// It performs squash merge, cleanup, and final commit.
func (g *GitCheckpointManager) OnAgentExit(ctx context.Context, agentErr error) error {
	g.mu.Lock()
	if g.ended {
		g.mu.Unlock()
		return nil // Prevent double-processing
	}
	g.ended = true
	g.session.Completed = true
	g.mu.Unlock()

	slog.Info("Git Checkpoint: Agent exit — performing squash merge and cleanup", "session", g.session.SessionID)

	// 1. Commit any remaining uncommitted changes
	dirty, _ := g.isWorktreeDirty(ctx)
	if dirty {
		_, _ = g.runGitCommand(ctx, "add", "-A")
		_, _ = g.runGitCommand(ctx, "commit", "-m", "checkpoint: uncommitted changes before exit")
	}

	// 2. Perform squash merge
	if g.config.SquashOnExit && g.session.UserBranch != "" && g.session.AgentBranch != "" {
		if err := g.performSquashMerge(ctx); err != nil {
			slog.Error("Git Checkpoint: squash merge failed", "error", err)
			return fmt.Errorf("git_checkpoint: squash merge failed: %w", err)
		}
	}

	// 3. Cleanup agent branch
	if g.config.CleanupAgentBranch {
		_, _ = g.runGitCommand(ctx, "branch", "-D", g.session.AgentBranch)
	}

	// 4. Cleanup checkpoint tags
	if g.config.CleanupCheckpointTags {
		_ = g.cleanupCheckpointTags(ctx)
	}

	// 5. Clear state file
	_, _ = g.clearState()

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

func (g *GitCheckpointManager) stashCreate(ctx context.Context, ref string) error {
	_, err := g.runGitCommand(ctx, "stash", "push", "-m", ref, "--include-untracked")
	return err
}

func (g *GitCheckpointManager) stashPop(ctx context.Context) error {
	_, err := g.runGitCommand(ctx, "stash", "pop")
	return err
}

func (g *GitCheckpointManager) createCheckpoint(ctx context.Context, message string) (string, error) {
	// Stage all changes
	_, err := g.runGitCommand(ctx, "add", "-A")
	if err != nil {
		return "", err
	}

	// Commit
	_, err = g.runGitCommand(ctx, "commit", "-m", message)
	if err != nil {
		return "", err
	}

	// Create tag
	tag := fmt.Sprintf("%s/%s-step-%d", g.config.CheckpointTagPrefix, g.session.SessionID, g.session.StepCount)
	_, err = g.runGitCommand(ctx, "tag", tag)
	if err != nil {
		return "", err
	}

	// Record checkpoint message
	g.checkpointMessages = append(g.checkpointMessages, message)

	return tag, nil
}

func (g *GitCheckpointManager) deleteBranch(ctx context.Context, branch string) error {
	_, err := g.runGitCommand(ctx, "branch", "-D", branch)
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

func (g *GitCheckpointManager) performSquashMerge(ctx context.Context) error {
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

func (g *GitCheckpointManager) fallbackCommitMessage(diff string) string {
	return fmt.Sprintf("coding-agent: apply task changes (diff: %d chars)", len(diff))
}

func (g *GitCheckpointManager) enforceMaxCheckpoints(ctx context.Context) error {
	max := g.config.MaxCheckpoints
	if max <= 0 {
		return nil
	}

	if len(g.session.Checkpoints) <= max {
		return nil
	}

	// Remove oldest checkpoints
	excess := g.session.Checkpoints[:len(g.session.Checkpoints)-max]
	for _, tag := range excess {
		_, _ = g.runGitCommand(ctx, "tag", "-d", tag)
	}
	g.session.Checkpoints = g.session.Checkpoints[len(g.session.Checkpoints)-max:]
	return nil
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
	statePath := filepath.Join(g.projectPath, ".git", "codeactor_session.json")

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
	statePath := filepath.Join(g.projectPath, ".git", "codeactor_session.json")
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

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
