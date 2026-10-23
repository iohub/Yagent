package agents

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yagent/internal/globalctx"
	"yagent/internal/llm"
	"yagent/internal/mcp"
	"yagent/internal/messaging"
	"yagent/internal/tools"
	"yagent/internal/toolspec"
)

// 外部 MCP 工具的端到端链路：真 Manager + 真假服务器 + 真 WorkspaceGuard +
// 真 Surface + 脚本化 LLM。验收断言是"结果真的回到对话里"，
// 不是"没报错" —— 这条路最容易的失效形状是工具注册了但调用被路由丢了。

const (
	fakeMCPEnv       = "YAGENT_TEST_AGENTS_FAKE_MCP"
	fakeMCPRecordEnv = "YAGENT_TEST_AGENTS_FAKE_MCP_RECORD"
)

// runFakeAgentsMCPServer 是 agents 包自己的假 MCP server（重新执行测试二进制）。
// 它与 internal/mcp 的那份刻意独立：跨包复用测试替身只能靠导出，而导出测试代码
// 会让生产包多背一层 API。
func runFakeAgentsMCPServer() {
	record := os.Getenv(fakeMCPRecordEnv)
	out := bufio.NewWriter(os.Stdout)
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 1<<25)

	for in.Scan() {
		var req struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(in.Bytes(), &req); err != nil {
			continue
		}
		respond := func(result interface{}) {
			data, _ := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": result})
			_, _ = out.Write(append(data, '\n'))
			_ = out.Flush()
		}

		switch req.Method {
		case "initialize":
			respond(mcp.InitializeResult{
				ProtocolVersion: "2024-11-05",
				ServerInfo:      mcp.ServerInfo{Name: "agents-fake", Version: "test"},
			})
		case "notifications/initialized":
		case "tools/list":
			readOnly := true
			respond(mcp.ListToolsResult{Tools: []mcp.DiscoveredTool{{
				Name:        "alpha",
				Description: "Echo the payload back.",
				InputSchema: map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{"payload": map[string]interface{}{"type": "string"}},
				},
				Annotations: &mcp.ToolAnnotations{ReadOnlyHint: &readOnly},
			}}})
		case "tools/call":
			var p mcp.ToolCallParams
			_ = json.Unmarshal(req.Params, &p)
			payload, _ := p.Arguments["payload"].(string)
			if record != "" {
				f, err := os.OpenFile(record, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
				if err == nil {
					_, _ = fmt.Fprintf(f, "%s|%s\n", p.Name, payload)
					_ = f.Close()
				}
			}
			respond(mcp.ToolCallResult{Content: []mcp.ToolContent{{Type: "text", Text: "fake:" + payload}}})
		case "shutdown":
			respond(map[string]interface{}{"ok": true})
			return
		default:
			data, _ := json.Marshal(map[string]interface{}{
				"jsonrpc": "2.0", "id": req.ID,
				"error": map[string]interface{}{"code": -32601, "message": "method not found"},
			})
			_, _ = out.Write(append(data, '\n'))
			_ = out.Flush()
		}
	}
}

// startFakeMCPManager 起一个真 Manager 对着假服务器，返回规格与账目。
func startFakeMCPManager(t *testing.T) ([]*toolspec.Spec, tools.ExternalAccount, string) {
	t.Helper()
	record := filepath.Join(t.TempDir(), "calls.log")
	cfg := mcp.ServerConfig{
		Name:    "fake",
		Command: os.Args[0],
		Env:     map[string]string{fakeMCPEnv: "on", fakeMCPRecordEnv: record},
		Enabled: true,
	}
	mgr := mcp.NewManager([]mcp.ServerConfig{cfg}, mcp.ManagerOptions{
		StartupTimeout:   5 * time.Second,
		ReconnectBackoff: 5 * time.Millisecond,
		MaxReconnects:    1,
	})
	t.Cleanup(mgr.ForceClose)

	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := mgr.WaitForReady(ctx); err != nil {
		t.Fatalf("WaitForReady: %v", err)
	}
	if h := mgr.Status("fake"); h.State != mcp.StateReady {
		t.Fatalf("假 server 状态 = %+v, want ready", h)
	}

	specs, account := tools.BuildExternalSpecs(tools.ExternalInput{
		Tools:  mgr.DiscoveredTools(),
		Caller: mgr,
		Config: map[string]mcp.ServerConfig{"fake": cfg},
	})
	if len(specs) != 1 {
		t.Fatalf("动态规格 = %d 条, want 1（%s）", len(specs), account.String())
	}
	return specs, account, record
}

func newExternalCodingAgent(t *testing.T, engine llm.Engine, specs []*toolspec.Spec) *CodingAgent {
	t.Helper()
	workDir := t.TempDir()
	gctx := newTestGlobalCtx(workDir)
	publisher := messaging.NewMessagePublisher(nil)

	return NewCodingAgent(CodingAgentParams{
		Formatter:   gctx,
		Publisher:   publisher,
		Env:         globalctx.NewEnvView(gctx),
		Files:       gctx.FileOps,
		Search:      gctx.SearchOps,
		Sys:         gctx.SysOps,
		Edit:        gctx.ReplaceTool,
		RepoOps:     gctx.RepoOps,
		Flow:        gctx.FlowOps,
		Thinker:     gctx.ThinkingTool,
		DeepThinker: gctx.DeepThinkingTool,
		Designer:    gctx.DesignTool,
		MicroAgent:  gctx.MicroAgentTool,
		RepoCtx:     globalctx.NewRepoContextStore(),
		LLM:         engine,
		MaxSteps:    6,
		External: External{
			Specs: func() []*toolspec.Spec { return specs },
		},
	})
}

// 断言链：load_tool 能取回 schema → 模型调用外部工具 → 假服务器真的收到 →
// 结果回到下一条 tool 消息。任一段断开都会红。
func TestE2EExternalToolReachesModel(t *testing.T) {
	specs, account, record := startFakeMCPManager(t)
	if !strings.Contains(account.String(), "1 discovered, 1 registered") {
		t.Errorf("账目 = %q", account.String())
	}

	engine := newScripted(map[string][]scriptStep{
		"coding": {
			toolCallStep("load_tool", map[string]interface{}{"names": []interface{}{"mcp__fake__alpha"}}),
			toolCallStep("mcp__fake__alpha", map[string]interface{}{"payload": "hi"}),
			toolCallStep("agent_exit", map[string]interface{}{"reason": "done"}),
		},
	})
	agent := newExternalCodingAgent(t, engine, specs)
	agent.Run(context.Background(), "调用外部工具")

	// 1) load_tool 的回执里有完整参数声明（否则模型不知道传什么）。
	loadEcho := ""
	for _, call := range engine.CallsFor("coding") {
		for _, m := range call.messages {
			if m.Role == llm.RoleTool && m.ToolName == "load_tool" {
				loadEcho = m.Content
			}
		}
	}
	if !strings.Contains(loadEcho, "mcp__fake__alpha") || !strings.Contains(loadEcho, "payload") {
		t.Fatalf("load_tool 回执 = %q, want 含工具名与参数 schema", loadEcho)
	}

	// 2) 外部工具的结果回到了对话里。
	callEcho := ""
	for _, call := range engine.CallsFor("coding") {
		for _, m := range call.messages {
			if m.Role == llm.RoleTool && m.ToolName == "mcp__fake__alpha" {
				callEcho = m.Content
			}
		}
	}
	if !strings.Contains(callEcho, "fake:hi") {
		t.Fatalf("外部工具回执 = %q, want 服务端回显", callEcho)
	}

	// 3) 假服务器**真的收到了**这次调用（side channel，不看客户端自说自话）。
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("读取 side channel: %v", err)
	}
	if !strings.Contains(string(data), "alpha|hi") {
		t.Errorf("side channel = %q, want alpha|hi（模型调用根本没到服务端）", data)
	}
}

// 反向臂：server 离线时同一个工具不在工具面上，且 Skipped 记下可读原因。
// 这条守的是"宁缺毋滥"——离线时注册一个必然失败的工具，模型会反复重试。
func TestExternalToolsDisappearWhenServerOffline(t *testing.T) {
	specs, _, _ := startFakeMCPManager(t)

	deps := toolspec.ProfileCoding.ProbeDeps()
	readOnly := true
	rebuilt, _ := tools.BuildExternalSpecs(tools.ExternalInput{
		Tools: []mcp.ServerTool{{Server: "fake", Tool: mcp.DiscoveredTool{
			Name:        "alpha",
			Description: "Echo.",
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: &readOnly},
		}}},
		Config: map[string]mcp.ServerConfig{"fake": {Name: "fake"}},
		Health: func(string) (bool, string) {
			return false, "server fake is offline: exit status 1 — its tools are unavailable this run"
		},
	})
	if len(rebuilt) != 1 {
		t.Fatalf("离线时规格仍应构造出来（可用性由 Available 判定），得到 %d 条", len(rebuilt))
	}
	deps.ExternalSpecs = rebuilt

	surface := tools.NewSurface(toolspec.ProfileCoding, deps, tools.SurfaceOptions{Deferred: true})
	for _, name := range surface.Names() {
		if name == "mcp__fake__alpha" {
			t.Fatal("离线 server 的工具仍然出现在工具面上")
		}
	}
	skipped := surface.Skipped()
	if len(skipped) != 1 || !strings.Contains(skipped[0].Reason, "offline") {
		t.Fatalf("Skipped = %+v, want 一条含 offline 的记录", skipped)
	}

	// 在线时同一份规格必须真的进工具面（对照臂：否则上一条断言可能恒真）。
	deps.ExternalSpecs = specs
	online := tools.NewSurface(toolspec.ProfileCoding, deps, tools.SurfaceOptions{Deferred: true})
	found := false
	for _, name := range online.Names() {
		if name == "mcp__fake__alpha" {
			found = true
		}
	}
	if !found {
		t.Fatalf("在线时外部工具没进工具面：%v", online.Names())
	}
}

// SpawnForDelegation 是浅拷整个结构体（cp := *a），所以 params.External 自动跟着走。
// 这条钉的是"端到端结果"而不是"某个字段有没有被拷"：谁把 Spawn 改成重置 params，
// 或者谁让 Surface 丢掉动态规格，都会让它红。
func TestSpawnedDelegationKeepsExternalTools(t *testing.T) {
	specs, _, _ := startFakeMCPManager(t)
	agent := newExternalCodingAgent(t, newScripted(nil), specs)

	spawned := agent.SpawnForDelegation(context.Background())
	if spawned == agent {
		t.Fatal("SpawnForDelegation 返回了同一个实例（C4 要求每次委派一份）")
	}
	got := spawned.params.External.specsOrNil()
	if len(got) != 1 || got[0].Name != "mcp__fake__alpha" {
		t.Fatalf("克隆后的外部规格 = %v, want 原样带过去", got)
	}
	// 反向：原实例的规格不能被克隆过程改写（浅拷的另一面）。
	if len(agent.params.External.specsOrNil()) != 1 {
		t.Error("克隆后原实例的规格变了")
	}
}

// Custom Agent 走 MatchNames（只认静态注册表），所以外部工具天然拿不到。
func TestCustomAgentsNeverGetExternalTools(t *testing.T) {
	selected, unknown, _ := toolspec.MatchNames(
		[]string{"mcp__fake__alpha", "read_file"},
		toolspec.ProfileDirector.Palette,
		toolspec.ProfileDirector.ProbeDeps(),
	)
	for _, s := range selected {
		if strings.HasPrefix(s.Name, "mcp__") {
			t.Errorf("Custom Agent 拿到了外部工具 %s", s.Name)
		}
	}
	if len(unknown) != 1 || unknown[0] != "mcp__fake__alpha" {
		t.Errorf("unknown = %v, want [mcp__fake__alpha]（静态注册表里没有它）", unknown)
	}
}
