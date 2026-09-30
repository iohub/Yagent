// project_context.go — 项目上下文加载组件（P0-1 Phase 2a 从 internal/agents/director.go 抽取）。
//
// 职责：读取工作区目录下的项目上下文文件（YAGENT.md、CLAUDE.md、AGENTS.md），
// 并缓存加载结果（同一实例只加载一次）；以及从项目路径计算文件系统安全的 projectID。
//
// 依赖约束：本包不得 import yagent/internal/agents（循环依赖守卫
// scripts/check_no_agents_import.sh）。项目路径通过 projectPathFn 函数注入，
// 保持 director 子包最小依赖面（仅标准库）。

package director

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ProjectContextLoader 负责加载并缓存项目上下文文件（同一会话只加载一次）。
// projectPathFn 在每次加载时求值，返回当前项目路径（保持动态语义，
// 对应原实现每次读取 GlobalCtx.ProjectPath）。
type ProjectContextLoader struct {
	mu            sync.Mutex
	cached        *ProjectContextLoadResult // 缓存项目上下文文件（同一会话只加载一次）
	projectPathFn func() string             // 返回当前项目路径（每次加载时动态求值）
}

// NewProjectContextLoader 创建 ProjectContextLoader。
// projectPathFn 用于获取当前项目路径（典型用法：
// director.NewProjectContextLoader(func() string { return globalCtx.ProjectPath })）。
func NewProjectContextLoader(projectPathFn func() string) *ProjectContextLoader {
	return &ProjectContextLoader{projectPathFn: projectPathFn}
}

// Load 读取工作区目录下的项目上下文文件（YAGENT.md、CLAUDE.md、AGENTS.md），
// 将成功读取的文件内容格式化后组合返回。文件按顺序尝试，不存在或读取失败时忽略。
// 返回加载的文件列表和组合后的内容。
// 如果已经加载过，直接返回缓存（同一实例会话内只加载一次）。
func (l *ProjectContextLoader) Load() *ProjectContextLoadResult {
	l.mu.Lock()
	defer l.mu.Unlock()
	// 如果已经加载过，直接返回缓存（同一 Agent 实例会话内只加载一次）
	if l.cached != nil {
		return l.cached
	}
	result := &ProjectContextLoadResult{
		LoadedFiles: []ProjectContextFile{},
	}
	var sb strings.Builder
	contextFiles := []string{"YAGENT.md", "CLAUDE.md", "AGENTS.md"}

	for _, fname := range contextFiles {
		fullPath := filepath.Join(l.projectPathFn(), fname)
		data, err := os.ReadFile(fullPath)
		if err != nil {
			// 文件不存在或读取失败，忽略并继续尝试下一个
			continue
		}
		if len(data) > 0 {
			result.LoadedFiles = append(result.LoadedFiles, ProjectContextFile{
				FileName: fname,
				Content:  string(data),
			})
			sb.WriteString(fmt.Sprintf("\n### %s\n```\n%s\n```\n", fname, string(data)))
		}
	}
	result.Content = sb.String()
	// 缓存加载结果，避免后续调用重复读取文件
	l.cached = result
	return result
}

// ComputeProjectID 从项目路径计算文件系统安全的 projectID
func ComputeProjectID(projectPath string) string {
	if projectPath == "" {
		return "default"
	}
	base := filepath.Base(projectPath)
	if base == "." || base == "/" {
		base = "root"
	}
	// 保留字母数字，其余替换为下划线
	sanitized := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, base)
	// 限制长度
	if len(sanitized) > 100 {
		sanitized = sanitized[:100]
	}
	// 添加短哈希
	h := sha256.Sum256([]byte(projectPath))
	shortHash := hex.EncodeToString(h[:])[:8]
	return sanitized + "_" + shortHash
}
