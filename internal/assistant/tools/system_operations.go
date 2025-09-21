package tools

import (
	"context"
	"fmt"
	"io/ioutil"
	"os/exec"
	"path/filepath"
	"strings"

	"codeactor/internal/util"
)

// SystemOperationsTool 实现系统操作相关工具
type SystemOperationsTool struct {
	workingDir string
}

func NewSystemOperationsTool(workingDir string) *SystemOperationsTool {
	return &SystemOperationsTool{
		workingDir: workingDir,
	}
}

// ExecuteRunTerminalCmd 实现run_terminal_cmd工具
func (t *SystemOperationsTool) ExecuteRunTerminalCmd(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	command, ok := params["command"].(string)
	if !ok {
		return nil, util.WrapError(ctx, fmt.Errorf("command parameter must be a string"), "executeRunTerminalCmd")
	}

	isBackground, _ := params["is_background"].(bool)

	if isBackground {
		// 后台执行
		go func() {
			cmd := exec.CommandContext(ctx, "bash", "-c", command)
			cmd.Dir = t.workingDir
			cmd.Run()
		}()
		return map[string]interface{}{
			"status":  "started_background",
			"command": command,
		}, nil
	}

	// 前台执行
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = t.workingDir

	output, err := cmd.CombinedOutput()
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   err.Error(),
			"output":  string(output),
		}, nil
	}

	return map[string]interface{}{
		"success": true,
		"output":  string(output),
	}, nil
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