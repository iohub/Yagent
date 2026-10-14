// Package gitinfo 读取一个仓库的三项身份事实：HEAD 的 sha、当前分支名、origin URL。
//
// 它住在这里而不是 internal/agents：rollout 的两个 session_meta 写入点分属
// internal/agents（executor）与 internal/agents/director（planner），而后者不得
// import 前者（scripts/check_no_agents_import.sh）。放在 memory 里又会让持久化
// 包依赖进程调用，所以两边都只依赖这个只出不进的叶子包。
//
// 这三项事实是给"暂停后在另一台机器上继续"用的判据：恢复时必须能回答
// "这份检查点属于哪个仓库、当时停在哪个提交上"，否则重放会把一个已经对不上号的
// 上下文喂回模型。
package gitinfo

import (
	"bytes"
	"context"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// probeBudget 是**整批**探测共用的时间上限。session_meta 每份 rollout 都要写一次
// （一次委派一份），所以这里绝不能无界：陈旧的 index.lock、裸仓库、会走网络的
// credential helper 都能把一条 git 命令拖到几十秒，表现成一个"卡死的 Agent"。
const probeBudget = 2 * time.Second

// singleCommandBudget 是 IsRepo 这类单次探测的上限。
const singleCommandBudget = 1 * time.Second

// Info 是仓库身份。字段为空表示"没问到"，不等于"仓库里没有"。
type Info struct {
	SHA       string
	Branch    string
	OriginURL string
}

// Empty 报告三项事实是否全部缺失——调用点用它决定要不要写进 rollout。
func (i Info) Empty() bool {
	return i.SHA == "" && i.Branch == "" && i.OriginURL == ""
}

// IsRepo 报告 dir 是否落在一个 git 工作树内。
// 用 rev-parse --git-dir，因此子模块与 worktree 也算数。
func IsRepo(dir string) bool {
	if dir == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), singleCommandBudget)
	defer cancel()
	_, err := run(ctx, dir, "rev-parse", "--git-dir")
	return err == nil
}

// Collect 尽力读取三项事实，失败的那几项留空。
//
// 这里刻意用 context.WithoutCancel(ctx) 派生超时：调用点之一是取消路径
// （rollout 的终止事件、以及恢复时的校验）。直接拿已取消的 ctx 去跑 git
// 只会得到一份"全空"的身份并且无人报错——git_checkpoint.go 的 OnAgentExit
// 就是这么干了很久，见 docs/pause-resume-design.md §2 R6/R8。
func Collect(ctx context.Context, dir string) Info {
	if !IsRepo(dir) {
		return Info{}
	}
	probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), probeBudget)
	defer cancel()

	info := Info{}
	var shaErr, branchErr error
	info.SHA, shaErr = run(probeCtx, dir, "rev-parse", "HEAD")
	info.Branch, branchErr = run(probeCtx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	// 没有 origin 是本地仓库的常态，不是故障：只有真跑失败（超时、锁）才降级到 debug 之外。
	if origin, err := run(probeCtx, dir, "remote", "get-url", "origin"); err == nil {
		info.OriginURL = origin
	} else if probeCtx.Err() != nil {
		slog.Debug("gitinfo: origin probe abandoned with the shared budget", "dir", dir, "error", err)
	}

	if shaErr != nil || branchErr != nil {
		slog.Warn("gitinfo: repository identity incomplete",
			"dir", dir, "sha_err", shaErr, "branch_err", branchErr)
	}
	return info
}

// run 执行一条 git 命令并返回去尾空白的 stdout。
func run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return strings.TrimSpace(stdout.String()), nil
}
