package tools

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"yagent/internal/supervise"
	"yagent/internal/util"
)

// runBashWaitDelay 是"命令已退出、但输出流还被子孙进程握着"这道等待的上限。
// 它是测试用的 seam（与 agents.defaultToolTimeout 同理）：不改它的话每条用例都要付 2 秒。
var runBashWaitDelay = 2 * time.Second

// maxShellOutputBytes 是前台 run_bash 的输出封顶（stdout+stderr 合计）。
// 它表达的是"排空缓冲别无限长"，与 read_file 的单次输出预算是两件事，
// 所以这里自带字面量，不复用文件读取的常量（共用一个数字 = 改一个动另一个）。
// 写成 var 而不是 const 是为了让用例能把上限调到几 KB —— 见 TestExecuteRunBash_OutputCapKeepsDraining。
var maxShellOutputBytes = 10 * 1024 * 1024 // 10 MB

// SystemOperationsTool 实现系统操作相关工具
type SystemOperationsTool struct {
	workingDir string
}

func NewSystemOperationsTool(workingDir string) *SystemOperationsTool {
	return &SystemOperationsTool{
		workingDir: workingDir,
	}
}

// ExecuteRunBash 实现run_bash工具
func (t *SystemOperationsTool) ExecuteRunBash(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	command, ok := params["command"].(string)
	if !ok {
		return nil, util.WrapError(ctx, fmt.Errorf("command parameter must be a string"), "executeRunTerminalCmd")
	}

	isBackground, _ := params["is_background"].(bool)

	if isBackground {
		// 后台执行
		// 使用 context.Background() 确保命令在请求上下文取消后继续运行
		cmd := exec.Command("bash", "-c", command)
		cmd.Dir = t.workingDir
		// 独立进程组是下面那句"寿命比这次委派长"的机制保证：不设的话这条命令和 yagent
		// 同组，终端的一次 Ctrl+C 就会把它一起带走。它也不登记进 liveGroups ——
		// 交给用户的东西不归我们杀（见 KillLiveProcessGroups）。
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

		if err := cmd.Start(); err != nil {
			return map[string]interface{}{
				"success": false,
				"error":   fmt.Sprintf("failed to start background command: %v", err),
			}, nil
		}

		// 在后台等待进程结束以避免僵尸进程。
		// 挂在进程根域而不是任务域上：后台命令（`npm run dev`、`go test`）的寿命比这次委派长，
		// 挂任务域会让委派结束时的 Drain 卡在等它。根域只在退出时按预算等一次，
		// 等不到就把这个名字报出来 —— "还有后台命令在跑"是要让用户看见的事实。
		supervise.Root().Go(fmt.Sprintf("run_bash/reap-pid%d", cmd.Process.Pid),
			func(context.Context) { _ = cmd.Wait() })

		return map[string]interface{}{
			"success": true,
			"status":  "started_background",
			"pid":     cmd.Process.Pid,
			"command": command,
		}, nil
	}

	// 前台执行
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = t.workingDir
	out := &util.CappedBuffer{Max: maxShellOutputBytes}
	cmd.Stdout = out
	cmd.Stderr = out
	// 直接子进程一退出，exec 的 ctx 看门狗就不再管这件事（Cancel 只在"进程还活着时 ctx 结束"
	// 才触发），于是 Wait 唯一在等的是管道 EOF —— 而 `cmd &` 的 EOF 由那个后台子 shell 决定。
	// 没有 WaitDelay，一次 `nohup … &` 就能把整条委派永久卡住，超时和取消都打在空气上。
	cmd.WaitDelay = runBashWaitDelay
	// 独立进程组 + 结束信号打整个组：exec 默认只杀得到直接子进程，于是 `make`、`node`
	// 这些子孙留在机器上占端口（2026-10-26 那次事故留下的就是一个还在 LISTEN 的 node）。
	// exec 只在"进程还活着时 ctx 结束"才调 Cancel，所以命令自己退完（走下面 ErrWaitDelay
	// 那条路）之后不会再被杀：故意 `&` 出去的服务因此能活下来 ——
	// TestExecuteRunBash_ReturnedCallSurvivesLaterCancel 钉的就是这个不对称。
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }

	// Start/Wait 而不是 Run：硬退出用的登记表需要 pid，而它只有 Start 之后才存在。
	var runErr error
	if runErr = cmd.Start(); runErr == nil {
		pgid := cmd.Process.Pid
		registerProcessGroup(pgid)
		runErr = cmd.Wait()
		unregisterProcessGroup(pgid)
	}
	result := map[string]interface{}{"output": out.String()}
	if out.Over() {
		result["truncated"] = true
	}

	switch {
	case runErr == nil:
		result["success"] = true
	case errors.Is(runErr, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Exited():
		// 命令自己退了（真 exit code 就在 ProcessState 里），只是子孙进程还握着输出流。
		code := cmd.ProcessState.ExitCode()
		result["success"] = code == 0
		result["exit_code"] = code
		result["note"] = detachedOutputNote
	case ctx.Err() != nil:
		// 超时与取消要分开说：模型对这两件事的下一步动作不同。
		reason := "cancelled"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			reason = "timed out"
		}
		result["success"] = false
		result["error"] = fmt.Sprintf("command %s and was terminated; output holds what it printed before that", reason)
	default:
		result["success"] = false
		result["error"] = runErr.Error()
	}
	return result, nil
}

// detachedOutputNote 存在的理由：`&` / `nohup` 写在前台分支里时，模型以为这次失败了，
// 而事实是命令成功了、只是我们不替它等下去（根 AGENTS.md §11 坑 43：被拦下来的那一次要有人看得见）。
const detachedOutputNote = "The command exited, but a descendant still holds the output stream " +
	"(the `&` / `nohup` shape), so output is only what was captured so far. " +
	"For a long-running service, re-run it with is_background=true."

// liveGroups 记着"此刻还在前台跑着"的调用各自的进程组。唯一消费者是第二次信号的硬退出：
// os.Exit 跳过所有 defer，不在那一刻杀一次，用户 Ctrl+C 之后就必然留下孤儿 ——
// 而设了 Setpgid 之后终端信号已经传不到它们。
var (
	liveGroupsMu sync.Mutex
	liveGroups   = make(map[int]struct{})
)

func registerProcessGroup(pgid int) {
	liveGroupsMu.Lock()
	liveGroups[pgid] = struct{}{}
	liveGroupsMu.Unlock()
}

func unregisterProcessGroup(pgid int) {
	liveGroupsMu.Lock()
	delete(liveGroups, pgid)
	liveGroupsMu.Unlock()
}

// KillLiveProcessGroups 杀掉我们自己起的、还没结束的每一个前台进程组。
// 由 main.go 传给 shutdown.Config.OnHardExit —— 本包不认识信号，也不决定退出码。
// 后台分支（is_background=true）不在表里：那条分支的意义就是"把寿命交给用户"。
func KillLiveProcessGroups() {
	liveGroupsMu.Lock()
	pgids := make([]int, 0, len(liveGroups))
	for pgid := range liveGroups {
		pgids = append(pgids, pgid)
	}
	liveGroupsMu.Unlock()

	for _, pgid := range pgids {
		// 负号 = 整个进程组。ESRCH 是"已经没了"而不是失败：并发退出的一路本来就该静默。
		if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
			slog.Warn("硬退出时杀进程组失败", "pgid", pgid, "error", err)
		}
	}
}

// ExecuteListDir 实现list_dir工具
func (t *SystemOperationsTool) ExecuteListDir(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	absolutePath, ok := params["absolute_path"].(string)
	if !ok {
		return nil, util.WrapError(ctx, fmt.Errorf("absolute_path parameter must be a string"), "executeListDir")
	}

	if absolutePath == "" {
		return nil, util.WrapError(ctx, fmt.Errorf("absolute_path cannot be empty"), "executeListDir")
	}

	// 验证路径安全性（防止路径遍历攻击）
	if strings.Contains(absolutePath, "..") || strings.Contains(absolutePath, "~") {
		return nil, util.WrapError(ctx, fmt.Errorf("absolute_path contains invalid characters"), "executeListDir")
	}

	// 确保路径是绝对路径
	if !filepath.IsAbs(absolutePath) {
		return nil, util.WrapError(ctx, fmt.Errorf("absolute_path must be an absolute path"), "executeListDir")
	}

	entries, err := ioutil.ReadDir(absolutePath)
	if err != nil {
		return nil, util.WrapError(ctx, err, "executeListDir::ReadDir")
	}

	var items []map[string]interface{}
	for _, entry := range entries {
		item := map[string]interface{}{
			"name":   entry.Name(),
			"is_dir": entry.IsDir(),
		}
		if !entry.IsDir() {
			item["size"] = entry.Size()
		}
		items = append(items, item)
	}

	return map[string]interface{}{
		"path":  absolutePath,
		"items": items,
	}, nil
}
