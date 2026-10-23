package app

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"yagent/internal/config"
	"yagent/internal/mcp"
	"yagent/internal/tools"
	"yagent/internal/toolspec"
)

// codeseekCuratedBridges 是"服务端工具名 → yagent 静态工具名"的手写桥对照表。
// curated 的服务端工具不能同时以 mcp__codeseek__<name> 再注册一次 —— 同一个能力两个名字，
// 模型会随机选一个，而两边的结果形状不同（桥接做的是整形与降级话术，不是透传）。
//
// 只此一处声明：`TestCuratedBridgesPointAtRegisteredTools` 断言每个目标都真的注册过，
// 名单烂掉（改名/删桥）必须红，否则它会静静地把一个真实工具排除掉。
var codeseekCuratedBridges = map[string]string{
	"codeseek_search":    "semantic_search",
	"codeseek_skeleton":  "query_code_skeleton",
	"codeseek_snippet":   "query_code_snippet",
	"codeseek_callees":   "find_function_callee",
	"codeseek_callers":   "find_function_caller",
	"codeseek_callgraph": "query_call_graph",
}

// codeseekCuratedTools 由对照表派生（键即服务端工具名）。
var codeseekCuratedTools = func() map[string]bool {
	out := make(map[string]bool, len(codeseekCuratedBridges))
	for serverTool := range codeseekCuratedBridges {
		out[serverTool] = true
	}
	return out
}()

// mcpServerConfigs 把 [codeseek] 与 [[mcp.servers]] 翻译成 Manager 的启动参数。
// Manager 自己不读 config（依赖方向：app → mcp），所以这里是唯一的翻译点。
func (ca *Yagent) mcpServerConfigs(codeseekBinaryPath, workDir string) map[string]mcp.ServerConfig {
	out := make(map[string]mcp.ServerConfig)
	global := config.GlobalMCP()

	if codeseekBinaryPath != "" {
		args := []string{"serve", "--mcp"}
		requestTimeout := 0
		if ca.config != nil {
			if len(ca.config.CodeSeek.MCPArgs) > 0 {
				args = ca.config.CodeSeek.MCPArgs
			}
			requestTimeout = ca.config.CodeSeek.RequestTimeout
		}
		out["codeseek"] = mcp.ServerConfig{
			Name:       "codeseek",
			Command:    codeseekBinaryPath,
			Args:       args,
			WorkingDir: workDir,
			// 兜底只在 mcp.NewMCPClient 一处（<=0 → 30s），这里不再抄一个 30。
			RequestTimeout: time.Duration(requestTimeout) * time.Second,
			Enabled:        true,
			MaxTools:       global.MaxToolsPerServer,
			Curated:        codeseekCuratedTools,
		}
	}

	for _, s := range global.Servers {
		name := strings.TrimSpace(s.Name)
		if name == "" {
			continue // 校验已经拒收过；这里防御性跳过，不让空名字进 map
		}
		out[name] = mcp.ServerConfig{
			Name:           name,
			Command:        s.Command,
			Args:           s.Args,
			Env:            s.Env,
			WorkingDir:     s.WorkingDir,
			RequestTimeout: time.Duration(s.RequestTimeout) * time.Second,
			Enabled:        s.IsEnabled(),
			AllowWrite:     s.AllowWrite,
			ReadOnly:       s.ReadOnly,
			Tools:          s.Tools,
			Exclude:        s.Exclude,
			MaxTools:       global.MaxToolsPerServer,
		}
	}
	return out
}

// rebuildExternalSpecs 按当前发现重新构造动态规格并换掉缓存。
// Manager 的工具集只在 list_changed / 重连后变，所以调用点是那两处 + 启动一次，
// 而不是每次 depsFor（那会把 sha256 与校验跑在每一次委派上）。
func (ca *Yagent) rebuildExternalSpecs() {
	mgr := ca.mcpMgr
	if mgr == nil {
		ca.setExternalSpecs(nil, tools.ExternalAccount{})
		return
	}

	health := func(server string) (bool, string) {
		h := mgr.Status(server)
		if h.State == mcp.StateReady || h.State == mcp.StateDegraded {
			return true, ""
		}
		reason := strings.TrimSpace(h.Error)
		if reason == "" {
			reason = h.State.String()
		}
		return false, fmt.Sprintf("server %s is %s: %s — its tools are unavailable this run", server, h.State, reason)
	}

	specs, account := tools.BuildExternalSpecs(tools.ExternalInput{
		Tools:  mgr.DiscoveredTools(),
		Caller: mgr,
		Config: ca.mcpConfigs,
		Health: health,
	})
	if problems := toolspec.ValidateDynamic(specs); len(problems) > 0 {
		slog.Error("external tool specs failed self-check", "count", len(problems), "problems", problems)
	}
	ca.setExternalSpecs(specs, account)
	slog.Info("mcp tools", "line", account.String())
}

func (ca *Yagent) setExternalSpecs(specs []*toolspec.Spec, account tools.ExternalAccount) {
	ca.specMu.Lock()
	ca.externalSpecs = specs
	ca.externalAccount = account
	ca.specMu.Unlock()
}

// ExternalSpecs 返回外部工具规格快照（拷贝，调用方不可改内部状态）。
// 它是 agents.ExternalProvider 的实现：六个 Agent 每轮重建工具面时问这一处。
func (ca *Yagent) ExternalSpecs() []*toolspec.Spec {
	if ca == nil {
		return nil
	}
	ca.specMu.RLock()
	defer ca.specMu.RUnlock()
	return append([]*toolspec.Spec(nil), ca.externalSpecs...)
}

// ExternalAccount 是"发现多少 / 注册多少 / 为什么没进"的一行账（/mcp 面板用）。
func (ca *Yagent) ExternalAccount() tools.ExternalAccount {
	if ca == nil {
		return tools.ExternalAccount{}
	}
	ca.specMu.RLock()
	defer ca.specMu.RUnlock()
	return ca.externalAccount
}

// MCPServers 返回各 server 的健康快照（按名字排序）。
func (ca *Yagent) MCPServers() []mcp.ServerHealth {
	if ca == nil || ca.mcpMgr == nil {
		return nil
	}
	return ca.mcpMgr.Health()
}

// MCPStatusLines 是状态账的唯一渲染出口（启动日志 / /mcp 面板 / system prompt 共用）。
func (ca *Yagent) MCPStatusLines() string {
	if ca == nil || ca.mcpMgr == nil {
		return ""
	}
	return ca.mcpMgr.StatusLines()
}

// publishMCPStatus 是 Manager 状态迁移的出口：一条可读日志 + 一条事件。
// payload 与 StatusLines 取自同一份 Health 快照，所以界面、日志、模型三处不会互相矛盾。
func (ca *Yagent) publishMCPStatus(h mcp.ServerHealth) {
	payload := map[string]interface{}{
		"server":       h.Name,
		"state":        h.State.String(),
		"tools":        h.Tools,
		"curated":      h.Curated,
		"attempt":      h.Attempt,
		"max_attempts": h.MaxAttempts,
	}
	if h.Error != "" {
		payload["error"] = h.Error
	}
	if h.Instructions != "" {
		payload["instructions"] = h.Instructions
	}
	// 走 globalCtx.Publisher（atomic 指针）而不是 ca.dispatcher：后者由
	// IntegrateMessaging 在每个任务上重写，而这里跑在 Manager 的 goroutine 上。
	if ca.globalCtx != nil && ca.globalCtx.Publisher != nil {
		_ = ca.globalCtx.Publisher.Publish("mcp_server_status", payload, "mcp")
	}
	slog.Info("mcp server status",
		"server", h.Name,
		"state", h.State.String(),
		"tools", h.Tools,
		"curated", h.Curated,
		"attempt", h.Attempt,
		"max_attempts", h.MaxAttempts,
		"error", h.Error,
	)
}
