package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"yagent/internal/toolspec"

	"yagent/internal/browserctl"

	"yagent/internal/util"
)

// WorkspaceGuard checks tool calls for potentially dangerous operations
// (modifications outside the workspace, system-changing commands) and
// requests user authorization before allowing them to proceed.
type WorkspaceGuard struct {
	// mu 保护下面四个授权状态：sessionAllowed / sessionAllAllowed /
	// projectAuthorized / yoloMode。一个 guard 是「一次 Init 一个实例、任务内全部
	// Agent 共享」，而 Director 会在一步里并行 delegate_repo/delegate_chat
	// （internal/agents/specs.go:71 那对 mutates:false 的委派工具），于是同一个 map
	// 一边被 Check 读、一边被 RequestAuth 写 —— 无锁时是 runtime throw
	// （fatal error: concurrent map read and map write），recover 拦不住。
	//
	// 加锁的铁律：**绝不包住 confirmMgr.RequestConfirmation**。那一调用阻塞到用户
	// 作答或 ctx 取消，持锁等待等于把整条委派链排在这把锁上（同包先例
	// user_confirm.go:50 也是「先取锁登记、锁外 select 等答复」）。
	mu                sync.RWMutex
	workspacePath     string
	confirmMgr        *UserConfirmManager
	sessionAllowed    map[string]bool // tools granted session-wide authorization
	sessionAllAllowed bool            // 会话内所有工具全部授权
	projectAuthorized bool            // 项目永久授权（从 settings.json 加载）
	yoloMode          bool            // YOLO模式：跳过所有授权检查
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

// SetYoloMode enables or disables YOLO mode. When enabled, all authorization
// checks are automatically bypassed — equivalent to auto-approving every operation.
func (g *WorkspaceGuard) SetYoloMode(enabled bool) {
	g.mu.Lock()
	g.yoloMode = enabled
	g.mu.Unlock()
}

// 危险工具与它们的路径参数名不再在这里各写一张表：判据来自注册表
// （Spec.NeedsAuth / Spec.PathParams），分派按能力标签而不是工具名。
//
// 这修掉了历史上的一类静默失效：往 dangerousTools 里加一个名字、但忘了在
// Check 的 switch 里加 case，结果那个工具"看着受保护、其实什么都不检查"。
// 现在 Validate() 保证 authEffects 与 NeedsAuth 一致，分派又按 Effect 走，
// 三者里任意一处漏了都会在启动自检里报错。

// authBypassed 在**一次 RLock 内**读完全部四个 bypass 状态（yolo / 项目永久 /
// 会话全部 / 单工具会话），Check 与 RequestAuth 都调它 —— 同源判定，不许两处各写一份。
//
// 为什么必须一次锁而不是四个 flag 各加一次锁：并行委派下逐 flag 加锁允许
// "yolo 刚被关掉、sessionAll 又被打开" 这种拼出来的假时刻，判据不再是任何一个
// 真实存在的状态快照。
func (g *WorkspaceGuard) authBypassed(toolName string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.yoloMode || g.projectAuthorized || g.sessionAllAllowed || g.sessionAllowed[toolName]
}

// Check determines if a tool call requires user authorization.
// Returns (needsAuth, reason).
func (g *WorkspaceGuard) Check(toolName string, params map[string]interface{}) (bool, string) {
	if g == nil || g.confirmMgr == nil {
		return false, ""
	}

	if !toolspec.NeedsAuth(toolName) {
		return false, ""
	}

	// 四个 bypass（YOLO / 项目永久授权 / 会话全部授权 / 该工具已会话授权）
	// 全部返回 (false, "")。yolo 历史上写在 NeedsAuth 之前，两个分支返回值逐字相同，
	// 换位不改变任何判定 —— 换来的是不需要授权的工具连锁都不用取。
	if g.authBypassed(toolName) {
		return false, ""
	}

	// 分派按能力标签：写文件类查路径，shell 查命令 token，浏览器 eval 查敏感 JS。
	// 下面三个 check 只依赖不可变的 workspacePath，不需要持锁。
	switch effect := toolspec.EffectOf(toolName); {
	case effect.Has(toolspec.EffectShell):
		return g.checkBash(params)

	case effect.Has(toolspec.EffectFSWrite):
		return g.checkFileOp(toolName, params)

	case effect.Has(toolspec.EffectBrowserEval):
		return g.checkBrowserEval(params)
	}

	return false, ""
}

// checkBrowserEval 只在 JS 触及敏感能力时要求授权：读写 cookie/存储、发起网络请求、
// 提交表单、动态执行代码。良性逃生口（location.reload()、history.back()、派发 mouseover）
// 不打断流程——否则模型会把 eval 当禁区，遇到死路时只能空转。
func (g *WorkspaceGuard) checkBrowserEval(params map[string]interface{}) (bool, string) {
	js, _ := params["js"].(string)
	if strings.TrimSpace(js) == "" {
		return false, ""
	}
	risky, reason := browserctl.RiskyJS(js)
	if !risky {
		return false, ""
	}
	return true, fmt.Sprintf("这段 JS 会在浏览器当前页面执行，且会%s:\n```js\n%s\n```", reason, js)
}

// RequestAuth blocks until the user approves or denies the operation.
// Returns an error if denied, timed out, or cancelled.
func (g *WorkspaceGuard) RequestAuth(ctx context.Context, toolName string, reason string) error {
	if g == nil || g.confirmMgr == nil {
		return nil
	}

	// YOLO mode 以及任何已成立的 bypass：直接放行，不再打扰用户。
	// 并行委派下两个 goroutine 可能同时通过 Check（各自的读都早于对方的写），
	// 少了这道再检查就是同一件事弹两次授权框。
	if g.authBypassed(toolName) {
		return nil
	}

	// 向后兼容：保留 question 字段（旧版 TUI 仍可解析）
	question := fmt.Sprintf(
		"⚠️ **授权请求** — 工具 `%s`\n\n%s",
		toolName, reason,
	)

	// 结构化数据供 TUI 渲染（避免中英双语混合）
	extraFields := map[string]interface{}{
		"tool_name": toolName,
		"reason":    reason,
	}

	options := "allow / deny"

	// 这一次等待必须在**锁外**：RequestConfirmation 阻塞到用户作答或 ctx 取消，
	// 持锁等会把同任务里所有并行委派的 Agent 排在这把锁上。
	response, err := g.confirmMgr.RequestConfirmation(ctx, question, options, extraFields)
	if err != nil {
		return fmt.Errorf("授权请求失败: %w", err)
	}

	response = strings.TrimSpace(strings.ToLower(response))

	// Session-wide authorization: grant for all subsequent calls of this tool
	if response == "allow_session" {
		g.mu.Lock()
		g.sessionAllowed[toolName] = true
		g.mu.Unlock()
		return nil
	}

	// 会话内全部工具授权
	if response == "allow_all_session" {
		g.mu.Lock()
		g.sessionAllAllowed = true
		g.mu.Unlock()
		return nil
	}

	// 项目永久授权
	if response == "allow_all_project" {
		g.mu.Lock()
		g.projectAuthorized = true
		g.mu.Unlock()
		// 落盘放在 Unlock 之后：它是磁盘 IO，且只读不可变的 workspacePath，
		// 没有任何一个字段需要在锁内被它看到。
		g.saveProjectAuth()
		return nil
	}

	if response != "allow" && response != "yes" && response != "y" && response != "允许" {
		return fmt.Errorf("用户拒绝了操作: %s", toolName)
	}

	return nil
}

func (g *WorkspaceGuard) checkFileOp(toolName string, params map[string]interface{}) (bool, string) {
	pathNames := toolspec.PathParams(toolName)
	if len(pathNames) == 0 {
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

// loadProjectAuth 从 ~/.yagent/settings.json 加载项目授权状态
func (g *WorkspaceGuard) loadProjectAuth() {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return
	}
	settingsPath := filepath.Join(homeDir, ".yagent", "settings.json")

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
		// 构造期调用，本不需要锁；加锁只为与其它写点同源（同一状态只有一种写法）。
		g.mu.Lock()
		g.projectAuthorized = true
		g.mu.Unlock()
	}
}

// saveProjectAuth 将当前项目路径保存到 ~/.yagent/settings.json
func (g *WorkspaceGuard) saveProjectAuth() {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return
	}
	settingsPath := filepath.Join(homeDir, ".yagent", "settings.json")

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
	// 原子落盘（全仓唯一的 tmp+rename 内核，棘轮见 scripts/check_no_inline_atomic_write.sh）：
	// 裸 os.WriteFile 会让读者看到半份 JSON，下一次 loadProjectAuth 的 Unmarshal
	// 失败即静默返回，永久授权就"存在过但没生效"。目录创建交给 DirPerm，
	// FilePerm 保持原来的 0644。这里的读-改-写仍是 last-writer-wins：并行委派的
	// 两个 goroutine 可以同时答 allow_all_project 并各自重写整份文件，但同一任务里
	// 写的是同一个 workspacePath，覆盖不丢信息；真会丢的是两个 yagent 进程各自
	// 授权不同工作区 —— 那需要跨进程的 settings.json 锁，不在本守卫的职责内（mu 是进程内的）。
	_ = util.AtomicWrite(settingsPath, data, util.AtomicWriteOptions{
		DirPerm:  0755,
		FilePerm: 0644,
		Label:    "workspace auth settings",
	})
}
