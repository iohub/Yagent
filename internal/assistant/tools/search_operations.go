package tools

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"codee/internal/util"
)

// SearchOperationsTool 实现搜索相关工具
type SearchOperationsTool struct {
	workingDir string
}

func NewSearchOperationsTool(workingDir string) *SearchOperationsTool {
	return &SearchOperationsTool{
		workingDir: workingDir,
	}
}

// ExecuteGrepSearch 实现grep_search工具
func (t *SearchOperationsTool) ExecuteGrepSearch(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	query, ok := params["query"].(string)
	if !ok {
		return nil, util.WrapError(ctx, fmt.Errorf("query parameter must be a string"), "executeGrepSearch")
	}

	includePattern, _ := params["include_pattern"].(string)
	excludePattern, _ := params["exclude_pattern"].(string)
	caseSensitive, _ := params["case_sensitive"].(bool)

	// 默认排除的非源码目录
	defaultExcludeDirs := []string{
		".git", "node_modules", "target", "build", "dist", "vendor",
		"__pycache__", ".venv", "venv", "env", ".env", "logs",
		"out", "bin", "obj", "pkg", "tmp", "temp", ".tmp",
		".sass-cache", ".next", ".nuxt", ".cache", ".idea",
		".vscode", ".history", "coverage", ".nyc_output",
		".gradle", ".mvn", "mvnw", ".settings", ".project",
		".classpath", "nbproject", ".externalNativeBuild",
		".cxx", ".react", ".expo", ".yarn", "pnpm-lock.json",
		".pnpm-store", ".yarn-cache", ".yarn-integrity",
		".DS_Store", "Thumbs.db", ".directory",
	}

	// 构建grep命令
	args := []string{"-r", "-n"}
	if !caseSensitive {
		args = append(args, "-i")
	}

	// 添加默认排除目录
	for _, dir := range defaultExcludeDirs {
		args = append(args, "--exclude-dir", dir)
	}

	if includePattern != "" {
		args = append(args, "--include", includePattern)
	}
	if excludePattern != "" {
		// 也支持排除目录模式
		args = append(args, "--exclude-dir", excludePattern)
	}
	args = append(args, query, t.workingDir)

	cmd := exec.CommandContext(ctx, "grep", args...)
	output, err := cmd.CombinedOutput()

	if err != nil {
		// grep返回非零退出码是正常的（没有找到匹配）
		if strings.Contains(err.Error(), "exit status 1") {
			return map[string]interface{}{
				"matches": []string{},
				"count":   0,
			}, nil
		}
		return nil, util.WrapError(ctx, err, "executeGrepSearch::CombinedOutput")
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = []string{}
	}

	return map[string]interface{}{
		"matches": lines,
		"count":   len(lines),
	}, nil
}

// ExecuteFileSearch 实现file_search工具
func (t *SearchOperationsTool) ExecuteFileSearch(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	query, ok := params["query"].(string)
	if !ok {
		return nil, util.WrapError(ctx, fmt.Errorf("query parameter must be a string"), "executeFileSearch")
	}

	// 使用fzf进行模糊文件搜索
	// 首先使用find生成文件列表，然后通过fzf进行模糊匹配
	findCmd := exec.CommandContext(ctx, "find", t.workingDir, "-type", "f")
	findOutput, err := findCmd.Output()
	if err != nil {
		return nil, util.WrapError(ctx, err, "executeFileSearch::FindOutput")
	}

	// 使用fzf进行模糊搜索
	fzfCmd := exec.CommandContext(ctx, "fzf", "-f", query, "--print-query", "--no-sort", "--tac")
	fzfCmd.Stdin = strings.NewReader(string(findOutput))
	output, err := fzfCmd.CombinedOutput()

	if err != nil {
		// fzf在没有匹配时返回非零退出码，这是正常的
		if strings.Contains(err.Error(), "exit status 1") || strings.Contains(err.Error(), "exit status 2") {
			return map[string]interface{}{
				"files": []string{},
				"count": 0,
			}, nil
		}
		return nil, util.WrapError(ctx, err, "executeFileSearch::FzfOutput")
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = []string{}
	}

	// 转换为相对路径，过滤掉空行和查询行
	var relativePaths []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, ">") {
			relPath, _ := filepath.Rel(t.workingDir, line)
			relativePaths = append(relativePaths, relPath)
		}
	}

	// 限制结果数量，避免返回过多文件
	if len(relativePaths) > 10 {
		relativePaths = relativePaths[:10]
	}

	return map[string]interface{}{
		"files": relativePaths,
		"count": len(relativePaths),
	}, nil
}