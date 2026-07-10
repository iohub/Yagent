package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WorkspaceGuard checks tool calls for potentially dangerous operations
// (modifications outside the workspace, system-changing commands) and
// requests user authorization before allowing them to proceed.
type WorkspaceGuard struct {
	workspacePath    string
	confirmMgr       *UserConfirmManager
	sessionAllowed   map[string]bool // tools granted session-wide authorization
	sessionAllAllowed bool  // 会话内所有工具全部授权
	projectAuthorized bool  // 项目永久授权（从 settings.json 加载）
}

// NewWorkspaceGuard creates a new WorkspaceGuard.
func NewWorkspaceGuard(workspacePath string, confirmMgr *UserConfirmManager) *WorkspaceGuard {
	g := &WorkspaceGuard{
		workspacePath:  filepath.Clean(workspacePath),
		confirmMgr:     confirmMgr,
		sessionAllowed: make(map[string]bool),
	}
	g.loadProjectAuth()
	return g
}

// dangerousTools lists tool names that can modify files or system state.
var dangerousTools = map[string]bool{
	"create_file":            true,
	"search_replace_in_file": true,
	"delete_file":            true,
	"rename_file":            true,
	"run_bash":               true,
}

// pathParamNames maps dangerous tool names to their file-path parameter names.
var pathParamNames = map[string][]string{
	"create_file":            {"target_file"},
	"search_replace_in_file": {"file_path"},
	"delete_file":            {"target_file", "file_paths"},
	"rename_file":            {"file_path", "rename_file_path"},
}

// Check determines if a tool call requires user authorization.
// Returns (needsAuth, reason).
func (g *WorkspaceGuard) Check(toolName string, params map[string]interface{}) (bool, string) {
	if g == nil || g.confirmMgr == nil {
		return false, ""
	}

	if !dangerousTools[toolName] {
		return false, ""
	}

	// 项目级授权：跳过所有检查
	if g.projectAuthorized {
		return false, ""
	}

	// 会话级全部授权：跳过所有检查
	if g.sessionAllAllowed {
		return false, ""
	}

	// Session-wide authorization: skip check if tool was already approved
	if g.sessionAllowed[toolName] {
		return false, ""
	}

	switch toolName {
	case "run_bash":
		return g.checkBash(params)

	case "create_file", "delete_file", "rename_file", "search_replace_in_file":
		return g.checkFileOp(toolName, params)
	}

	return false, ""
}

// RequestAuth blocks until the user approves or denies the operation.
// Returns an error if denied, timed out, or cancelled.
func (g *WorkspaceGuard) RequestAuth(ctx context.Context, toolName string, reason string) error {
	if g == nil || g.confirmMgr == nil {
		return nil
	}

	// 向后兼容：保留 question 字段（旧版 TUI 仍可解析）
	question := fmt.Sprintf(
		"⚠️ **授权请求** — 工具 `%s`\n\n%s",
		toolName, reason,
	)

	// 结构化数据供 TUI 渲染（避免中英双语混合）
	extraFields := map[string]interface{}{
		"tool_name":  toolName,
		"reason":     reason,
	}

	options := "allow / deny"

	response, err := g.confirmMgr.RequestConfirmation(ctx, question, options, extraFields)
	if err != nil {
		return fmt.Errorf("授权请求失败: %w", err)
	}

	response = strings.TrimSpace(strings.ToLower(response))

	// Session-wide authorization: grant for all subsequent calls of this tool
	if response == "allow_session" {
		g.sessionAllowed[toolName] = true
		return nil
	}

	// 会话内全部工具授权
	if response == "allow_all_session" {
		g.sessionAllAllowed = true
		return nil
	}

	// 项目永久授权
	if response == "allow_all_project" {
		g.projectAuthorized = true
		g.saveProjectAuth()
		return nil
	}

	if response != "allow" && response != "yes" && response != "y" && response != "允许" {
		return fmt.Errorf("用户拒绝了操作: %s", toolName)
	}

	return nil
}

func (g *WorkspaceGuard) checkFileOp(toolName string, params map[string]interface{}) (bool, string) {
	pathNames, ok := pathParamNames[toolName]
	if !ok {
		return false, ""
	}

	var outsidePaths []string
	for _, pn := range pathNames {
		switch pn {
		case "file_paths":
			// delete_file can take an array of paths
			if paths, ok := params[pn].([]interface{}); ok {
				for _, p := range paths {
					if pathStr, ok := p.(string); ok {
						if resolved := g.resolvePath(pathStr); !g.isInWorkspace(resolved) {
							outsidePaths = append(outsidePaths, resolved)
						}
					}
				}
			}
		default:
			if pathStr, ok := params[pn].(string); ok && pathStr != "" {
				if resolved := g.resolvePath(pathStr); !g.isInWorkspace(resolved) {
					outsidePaths = append(outsidePaths, resolved)
				}
			}
		}
	}

	if len(outsidePaths) > 0 {
		return true, fmt.Sprintf("目标路径在工作空间外部:\n- %s", strings.Join(outsidePaths, "\n- "))
	}

	return false, ""
}

// checkBash checks whether a run_bash command needs authorization based on the
// is_dangerous flag set by the LLM.
func (g *WorkspaceGuard) checkBash(params map[string]interface{}) (bool, string) {
	command, _ := params["command"].(string)
	if command == "" {
		return false, ""
	}

	isDangerous, _ := params["is_dangerous"].(bool)
	if isDangerous {
		return true, fmt.Sprintf("危险命令:\n```bash\n%s\n```", command)
	}

	// Also verify by checking if the command references paths outside the workspace
	// (defense in depth — catches cases where the LLM misjudges is_dangerous)
	if g.referencesOutsideWorkspace(command) {
		return true, fmt.Sprintf("\n%s\n", command)
	}

	return false, ""
}

func (g *WorkspaceGuard) resolvePath(filePath string) string {
	if filepath.IsAbs(filePath) {
		return filepath.Clean(filePath)
	}
	return filepath.Join(g.workspacePath, filePath)
}

func (g *WorkspaceGuard) isInWorkspace(resolvedPath string) bool {
	rel, err := filepath.Rel(g.workspacePath, resolvedPath)
	if err != nil {
		return false
	}
	return !strings.HasPrefix(rel, "..") && rel != "."
}

// referencesOutsideWorkspace checks if a command string contains absolute paths
// that fall outside the workspace. Used as a defense-in-depth check when the
// LLM sets is_dangerous=false.
func (g *WorkspaceGuard) referencesOutsideWorkspace(command string) bool {
	// 安全系统路径白名单：这些路径下的引用是只读系统工具/库，不构成危险
	safeSystemPaths := []string{
		"/usr/bin",
		"/usr/local/bin",
		"/bin",
		"/sbin",
		"/usr/sbin",
		"/usr/lib",
		"/usr/lib64",
		"/usr/libexec",
		"/usr/share",
		"/etc",
		"/dev/null",
		"/dev/zero",
		"/dev/random",
		"/dev/urandom",
		"/tmp",
		"/var/tmp",
		"/proc",
		"/sys",
	}

	fields := strings.Fields(command)
	for _, field := range fields {
		field = strings.Trim(field, `'"`)
		if strings.HasPrefix(field, "/") {
			cleaned := filepath.Clean(field)
			if cleaned == "/" {
				return true // 根目录始终危险
			}
			// 检查是否在白名单路径内
			isSafe := false
			for _, safePath := range safeSystemPaths {
				if cleaned == safePath || strings.HasPrefix(cleaned, safePath+"/") {
					isSafe = true
					break
				}
			}
			if isSafe {
				continue // 在白名单内，跳过此token
			}
			if !g.isInWorkspace(cleaned) {
				return true
			}
		}
	}
	return false
}

// loadProjectAuth 从 ~/.codeactor/settings.json 加载项目授权状态
func (g *WorkspaceGuard) loadProjectAuth() {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return
	}
	settingsPath := filepath.Join(homeDir, ".codeactor", "settings.json")

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return // 文件不存在，无需授权
	}

	var settings struct {
		AuthorizedProjects map[string]bool `json:"authorized_projects"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return
	}

	if settings.AuthorizedProjects[g.workspacePath] {
		g.projectAuthorized = true
	}
}

// saveProjectAuth 将当前项目路径保存到 ~/.codeactor/settings.json
func (g *WorkspaceGuard) saveProjectAuth() {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return
	}
	settingsPath := filepath.Join(homeDir, ".codeactor", "settings.json")

	// 确保目录存在
	settingsDir := filepath.Dir(settingsPath)
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		return
	}

	var settings struct {
		AuthorizedProjects map[string]bool `json:"authorized_projects"`
	}

	// 读取已有配置（如果存在）
	if existingData, err := os.ReadFile(settingsPath); err == nil {
		json.Unmarshal(existingData, &settings)
	}

	if settings.AuthorizedProjects == nil {
		settings.AuthorizedProjects = make(map[string]bool)
	}
	settings.AuthorizedProjects[g.workspacePath] = true

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return
	}
	os.WriteFile(settingsPath, data, 0644)
}
