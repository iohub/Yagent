package tools

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"yagent/internal/supervise"
	"yagent/internal/util"
)

// runBashWaitDelay 是"命令已退出、但输出流还被子孙进程握着"这道等待的上限。
// 它是测试用的 seam（与 agents.defaultToolTimeout 同理）：不改它的话每条用例都要付 2 秒。
var runBashWaitDelay = 2 * time.Second

// maxShellOutputBytes 复用整读文件的口径，不新造一个"大概这么大"的数字。
// 写成 var 而不是 const 是为了让用例能把上限调到几 KB —— 见 TestExecuteRunBash_OutputCapKeepsDraining。
var maxShellOutputBytes = MaxFileSizeForEntireRead

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

	runErr := cmd.Run()
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
