package agents

import (
	"context"
	"strings"
	"testing"

	"yagent/internal/config"
	"yagent/internal/llm"
	"yagent/internal/mcp"
	"yagent/internal/tools"
	"yagent/internal/toolspec"
)

// 注入面的测试：技能目录进 system prompt 的什么位置、哪些 Agent 拿得到，
// 以及 MCP 状态行只给 Director。

type captureEngine struct {
	systemPrompts []string
}

func (e *captureEngine) GenerateContent(_ context.Context, messages []llm.Message, _ []llm.ToolDef, _ *llm.CallOptions) (*llm.Response, error) {
	if len(messages) > 0 {
		e.systemPrompts = append(e.systemPrompts, messages[0].Content)
	}
	return &llm.Response{Choices: []llm.Choice{{Content: "ok"}}}, nil
}

func (e *captureEngine) Model() string         { return "capture" }
func (e *captureEngine) CloseIdleConnections() {}

func TestExecutorInjectsSkillCatalogAfterToolCatalog(t *testing.T) {
	deps := toolspec.ProfileCoding.ProbeDeps()
	surface := tools.NewSurface(toolspec.ProfileCoding, deps, tools.SurfaceOptions{Deferred: true})
	engine := &captureEngine{}

	catalog := "## Skills\nReusable procedures.\n- demo: 演示技能"
	_, _ = RunAgentLoop(context.Background(), ExecutorConfig{
		SystemPrompt: "sys",
		UserInput:    "hi",
		Surface:      surface,
		LLM:          engine,
		MaxSteps:     1,
		AgentName:    "coding-test",
		StopOnFinish: true,
		SkillCatalog: catalog,
	})

	if len(engine.systemPrompts) == 0 {
		t.Fatal("没有发生任何 LLM 调用")
	}
	sys := engine.systemPrompts[0]
	toolCatalog := surface.Catalog()
	if toolCatalog == "" {
		t.Fatal("夹具失效：Coding 的工具目录为空（没有冷工具）")
	}
	skillAt := strings.Index(sys, catalog)
	toolAt := strings.Index(sys, toolCatalog)
	if skillAt < 0 {
		t.Fatalf("技能目录没进 system prompt：\n%s", sys)
	}
	if toolAt < 0 || skillAt < toolAt {
		t.Errorf("技能目录位置 = %d、工具目录 = %d，want 技能目录在工具目录之后", skillAt, toolAt)
	}

	// 空目录不注入：一个空标题只会让模型以为"这里有东西但看不到"。
	empty := &captureEngine{}
	_, _ = RunAgentLoop(context.Background(), ExecutorConfig{
		SystemPrompt: "sys", UserInput: "hi", Surface: surface, LLM: empty,
		MaxSteps: 1, AgentName: "coding-test", StopOnFinish: true,
	})
	if strings.Contains(empty.systemPrompts[0], "## Skills") {
		t.Error("SkillCatalog 为空时仍然注入了标题")
	}
}

// inject_into 的判据只在一处：名单外的 Agent 拿不到目录。
func TestSkillCatalogRespectsInjectInto(t *testing.T) {
	t.Cleanup(func() { config.SetGlobalSkills(config.DefaultSkillsConfig(), config.SkillsPresence{}) })
	cfg := config.DefaultSkillsConfig()
	cfg.InjectInto = []string{"chat"}
	config.SetGlobalSkills(cfg, config.SkillsPresence{Section: true, Enable: true, InjectInto: true})

	skills := &fakeSkillSource{catalog: "## Skills\n- demo: x"}
	if got := skillCatalogFor("repo", skills); got != "" {
		t.Errorf("repo 不在 inject_into 里却拿到了目录：%q", got)
	}
	if got := skillCatalogFor("chat", skills); !strings.Contains(got, "## Skills") {
		t.Errorf("chat 在名单里却没拿到目录：%q", got)
	}
	// nil 技能通道永远不注入（而不是 panic）。
	if got := skillCatalogFor("chat", nil); got != "" {
		t.Errorf("nil 技能通道 = %q, want 空", got)
	}
}

type fakeSkillSource struct{ catalog string }

func (f *fakeSkillSource) Catalog(int) string { return f.catalog }
func (f *fakeSkillSource) Body(string, int, int) (toolspec.SkillBody, error) {
	return toolspec.SkillBody{}, toolspec.ErrSkillNotFound
}

// 状态行只给 Director，且 status_line=false 时整体不注入。
func TestDirectorPromptCarriesMCPStatusLine(t *testing.T) {
	t.Cleanup(func() { config.SetGlobalMCP(config.DefaultMCPConfig(), config.MCPPresence{}) })
	config.SetGlobalMCP(config.DefaultMCPConfig(), config.MCPPresence{Section: true, Enable: true, StatusLine: true})

	d := &DirectorAgent{external: External{Status: func() string {
		return "mcp: 1 servers, 3 tools (3 external + 0 curated), 0 offline\n  alpha: ready — 3 tools (0 curated, 3 external)"
	}}}
	got := d.mcpStatusLine()
	if !strings.Contains(got, "## MCP servers") || !strings.Contains(got, "alpha: ready") {
		t.Errorf("mcpStatusLine() = %q, want 标题 + 状态账", got)
	}

	// status_line = false ⇒ 一个字都不注入（那是明确的"我嫌它吵"）。
	cfg := config.DefaultMCPConfig()
	cfg.StatusLine = false
	config.SetGlobalMCP(cfg, config.MCPPresence{Section: true, Enable: true, StatusLine: true})
	if got := d.mcpStatusLine(); got != "" {
		t.Errorf("status_line=false 时仍注入：%q", got)
	}

	// 没有状态来源（未装配 Manager）时安静返回空串。
	config.SetGlobalMCP(config.DefaultMCPConfig(), config.MCPPresence{Section: true, Enable: true, StatusLine: true})
	bare := &DirectorAgent{}
	if got := bare.mcpStatusLine(); got != "" {
		t.Errorf("没有 Status 钩子时 = %q, want 空", got)
	}
}

// 状态行有行数上限：server 再多也不能把 system prompt 撑爆。
// 直接调 mcp 包的渲染函数 —— 上限逻辑只有一份实现（Manager.StatusLines 也走它）。
func TestMCPStatusLineIsBounded(t *testing.T) {
	var health []mcp.ServerHealth
	for i := 0; i < 12; i++ {
		health = append(health, mcp.ServerHealth{Name: "s" + string(rune('a'+i)), State: mcp.StateReady, Tools: 1})
	}
	lines := mcp.RenderStatusLines(health, 8)
	if got := strings.Count(lines, ": ready —"); got != 8 {
		t.Errorf("server 行数 = %d, want 上限 8", got)
	}
	if !strings.Contains(lines, "+4 more servers") {
		t.Errorf("截断标记没写进正文：\n%s", lines)
	}
	// 不限行数时全部列出（0 = 不限），否则这条上限就成了唯一形态。
	if got := strings.Count(mcp.RenderStatusLines(health, 0), ": ready —"); got != 12 {
		t.Errorf("不限行数时 = %d 行, want 12", got)
	}
}
