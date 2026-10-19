package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yagent/internal/blueprint"
	"yagent/internal/config"
	"yagent/internal/globalctx"
	"yagent/internal/llm"
	"yagent/internal/memory"
	"yagent/internal/messaging"
	"yagent/internal/toolspec"
)

// 这条用例是整件事的目的本身（设计稿 §17 的 R6 验收）：
//
//	脚本化的 Repo 跑一轮（真 dispatcher / 真 WorkspaceGuard / 真 read_file / 真 rollout，
//	只把 LLM 换成按 Agent 身份分派的脚本）→ agent_exit{blueprint[]} → 钩子折叠 →
//	~/.yagent 之外的存储根里出现 blueprint.md
//
// 加上两条反向断言：没读过那片就不许出现（I17），以及蓝图块只进这次调用的 system prompt、
// 不进子 Agent 的记忆（I2）。

// testBlueprintSource 是装配层的替身：真实运行时由 internal/app 持有（那里才有
// supervise 域与配置），agents 侧只依赖 BlueprintSource 这三个方法。
type testBlueprintSource struct {
	root string
	snap *blueprint.Snapshot
	// requests 记被叫醒了几次 —— 注入侧"每轮请求一次重派生"因此可断言。
	requests int
}

func (s *testBlueprintSource) Request() { s.requests++ }
func (s *testBlueprintSource) Snapshot() *blueprint.Snapshot {
	return s.snap
}
func (s *testBlueprintSource) Root() string { return s.root }

// blueprintProject 造一个能扫出多片模块的仓库（40 文件阈值以下会被折成一片根 leaf）。
func blueprintProject(t *testing.T) (workDir string, snap *blueprint.Snapshot) {
	t.Helper()
	workDir = t.TempDir()
	mk := func(rel, body string) {
		full := filepath.Join(workDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatalf("夹具目录失败: %v", err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatalf("夹具写入失败: %v", err)
		}
	}
	mk("go.mod", "module example.com/x\n\ngo 1.25\n")
	mk("internal/a/a.go", "package a\n\nfunc Alpha() string { return \"alpha\" }\n")
	// 42 个文件是夹具成立的前提：低于扫描器的 40 文件阈值时整棵树会被折成一片根 leaf，
	// "internal/a 是一个模块"这条判据本身就没了（见 blueprint 包的 fold_test 同一条注释）。
	for i := 0; i < 41; i++ {
		mk("internal/a/fill"+pad2(i)+".go", "package a\n")
	}
	mk("internal/b/b.go", "package b\n\nimport \"example.com/x/internal/a\"\n\nvar _ = a.Alpha\n")

	snap, err := blueprint.Scan(workDir, blueprint.DefaultOptions())
	if err != nil {
		t.Fatalf("夹具扫描失败: %v", err)
	}
	for _, want := range []string{"internal/a", "internal/b"} {
		if _, ok := snap.DerivedModuleSet()[want]; !ok {
			t.Fatalf("夹具没把 %q 扫成独立一片: leaves=%d", want, len(snap.Leaves))
		}
	}
	return workDir, snap
}

func pad2(i int) string {
	return fmt.Sprintf("%02d", i)
}

func newBlueprintRepoAgent(t *testing.T, workDir string, engine llm.Engine, view *BlueprintView) *RepoAgent {
	t.Helper()
	gctx := newTestGlobalCtx(workDir)
	gctx.FullYoloMode = true
	publisher := messaging.NewMessagePublisher(nil)
	gctx.Publisher = publisher

	return NewRepoAgent(RepoAgentParams{
		Formatter:            gctx,
		Publisher:            publisher,
		Env:                  globalctx.NewEnvView(gctx),
		Files:                gctx.FileOps,
		Search:               gctx.SearchOps,
		RepoOps:              gctx.RepoOps,
		Flow:                 gctx.FlowOps,
		DeepThinker:          gctx.DeepThinkingTool,
		Guard:                gctx.Guard,
		LLM:                  engine,
		MaxSteps:             8,
		EnhancedCommanderCfg: config.EnhancedCommanderConfig{},
		Blueprint:            view,
	})
}

// exitWithBlueprint 生成"读过的结论 + agent_exit(blueprint[])"这一轮回复。
func exitWithBlueprint(text string, items []map[string]interface{}) *llm.Response {
	args, err := json.Marshal(map[string]interface{}{
		"reason":    "分析完成",
		"handoff":   map[string]interface{}{"conclusion": "done", "status": "ok"},
		"blueprint": items,
	})
	if err != nil {
		panic(err)
	}
	return &llm.Response{Choices: []llm.Choice{{
		Content: text,
		ToolCalls: []llm.ToolCall{{
			ID:       "call_exit",
			Type:     "function",
			Function: llm.FunctionCall{Name: "agent_exit", Arguments: string(args)},
		}},
	}}}
}

func TestE2ERepoRunWritesBlueprintFile(t *testing.T) {
	workDir, snap := blueprintProject(t)
	storeRoot := filepath.Join(t.TempDir(), "blueprint")
	src := &testBlueprintSource{root: storeRoot, snap: snap}
	view := NewBlueprintView(src, "repo", true, 4000, 1500, nil)

	engine := newScripted(map[string][]scriptStep{"repo": {
		// 第一步：真读 internal/a/a.go —— 这条读就是后面那张提交唯一合法的证据。
		toolCallStep("read_file", map[string]interface{}{"target_file": "internal/a/a.go", "should_read_entire_file": true}),
		fixed(exitWithBlueprint("internal/a 是这套夹具的算法片。", []map[string]interface{}{
			{
				"section": "modules",
				"module":  "internal/a",
				"field":   "purpose",
				"claim":   "holds the fixture algorithms for this repository",
				"where":   "internal/a/a.go:3",
			},
		})),
	}})

	agent := newBlueprintRepoAgent(t, workDir, engine, view)
	res, err := agent.Run(memory.WithDelegationID(context.Background(), "call_delegate_repo_1"), "看看 internal/a 是干什么的")
	if err != nil {
		t.Fatalf("委派失败: %v", err)
	}

	// 交付物一侧：提交与证据都在 AgentResult 上（采集点在 result.go，与 handoff 同一处）。
	// 少这一句，"文件出现了"可能只是上一次 fold 的残留 —— 而这条链路的目的恰恰是
	// "本轮的提交落到本轮的文件里"。
	if len(res.Blueprint) != 1 || res.Blueprint[0].Module != "internal/a" {
		t.Errorf("提交没被采集进 AgentResult: %+v", res.Blueprint)
	}
	if strings.Join(res.EvidenceFiles, ",") != "internal/a/a.go" {
		t.Errorf("证据集不对: %v, 要只有一条 internal/a/a.go", res.EvidenceFiles)
	}

	data, err := os.ReadFile(blueprint.DocPath(storeRoot))
	if err != nil {
		t.Fatalf("blueprint.md 没落盘（整条链路的终点）: %v", err)
	}
	body := string(data)
	for _, want := range []string{
		"holds the fixture algorithms", "## Modules/internal/a", "field:purpose",
		"src:repo_agent@call_del", // {agent}@{会话坐标前 8 位}
		`"module_fingerprints"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("文档里少了 %q:\n%s", want, body)
		}
	}
	// 委派前缀这次没匹配到片（任务文本里没有 internal/a 之外的路径？其实有），
	// 但注入侧必须至少请求过一次重派生 —— 一次都没有就说明 Resident() 没接上。
	if src.requests == 0 {
		t.Errorf("注入侧一次都没请求重派生（每轮重派生的判据没被执行）")
	}
}

// I17：提交覆盖没读过的片 → 什么都不写，而且委派本身照常成功（I11）。
func TestE2ERepoRunDoesNotWriteUnreadModule(t *testing.T) {
	workDir, snap := blueprintProject(t)
	storeRoot := filepath.Join(t.TempDir(), "blueprint")
	src := &testBlueprintSource{root: storeRoot, snap: snap}
	view := NewBlueprintView(src, "repo", true, 4000, 1500, nil)

	engine := newScripted(map[string][]scriptStep{"repo": {
		toolCallStep("read_file", map[string]interface{}{"target_file": "internal/b/b.go", "should_read_entire_file": true}),
		fixed(exitWithBlueprint("关于 internal/a 的臆测。", []map[string]interface{}{
			{
				"section": "modules", "module": "internal/a", "field": "purpose",
				"claim": "a claim about a module this run never opened", "where": "internal/a/a.go",
			},
		})),
	}})

	agent := newBlueprintRepoAgent(t, workDir, engine, view)
	if _, err := agent.Run(context.Background(), "看看 internal/b"); err != nil {
		t.Fatalf("钩子把错误传给了委派（I11）: %v", err)
	}
	if _, err := os.Stat(blueprint.DocPath(storeRoot)); err == nil {
		data, _ := os.ReadFile(blueprint.DocPath(storeRoot))
		t.Errorf("没读过的片被写进了文档:\n%s", data)
	}
}

// I2：蓝图块只进这次调用的 system prompt，绝不进子 Agent 的记忆。
// 一旦写回记忆，第二/三档压缩会把目录算进正文（§11 坑 11 记的就是这个事故）。
func TestBlueprintResidentNeverEntersMemory(t *testing.T) {
	workDir, snap := blueprintProject(t)
	storeRoot := filepath.Join(t.TempDir(), "blueprint")
	view := NewBlueprintView(&testBlueprintSource{root: storeRoot, snap: snap}, "repo", true, 4000, 1500, nil)

	block := view.Resident()
	if !strings.Contains(block, "Repository Blueprint") {
		t.Fatalf("常驻块没渲染出来: %q", block)
	}
	// 标记取第一个**非空行**：Resident() 以换行开头，直接 Split("\n")[0] 会得到空串，
	// 而空串匹配一切 —— 那等于写了一条永远不会失败的测试（§8.3 的"夹具小到没触发分支"）。
	marker := ""
	for _, line := range strings.Split(block, "\n") {
		if strings.TrimSpace(line) != "" {
			marker = line
			break
		}
	}
	if len(marker) < 10 {
		t.Fatalf("标记太短，测不出任何东西: %q", marker)
	}

	engine := newScripted(map[string][]scriptStep{"repo": {
		fixed(textResponse("调查完成。")),
	}})
	agent := newBlueprintRepoAgent(t, workDir, engine, view)
	res, err := agent.Run(context.Background(), "这个仓库有什么")
	if err != nil {
		t.Fatalf("委派失败: %v", err)
	}
	// 断言一：模型这一轮**看见**了它（否则下一条断言是在证明空气）。
	call := engine.lastCallFor(t, "repo")
	if !strings.Contains(messagesText(call.messages), marker) {
		t.Errorf("常驻骨架没进这次 LLM 调用的 system prompt:\n%s", truncateForFailure(messagesText(call.messages)))
	}
	// 断言二：它在对话里只出现一次。Repo 的 system prompt 以 human 角色下发
	// （SystemAsHuman），"就在头部那一条"正是它扛住上下文压缩的机制（压缩动的是
	// 会话历史，而 system prompt 每轮由闭包重建）；被再抄进任何后续轮次，
	// 等于每轮都给对话加一份目录 —— 那才是 §11 坑 11 的病灶。
	hits := 0
	for _, m := range res.Memory {
		if strings.Contains(m.Content, marker) {
			hits++
		}
	}
	if hits != 1 {
		t.Errorf("蓝图块在记忆里出现了 %d 次, 要 1 次（只该在 system prompt 那一条）", hits)
	}
}

// 关掉注入的站点：不渲染常驻块，但 read_blueprint 仍然读得到东西。
// 这条区分的是"成本自救阀"（inject_into）与"整套机制退出"（enable/env）两种关法。
func TestBlueprintInjectIntoGatesResidentOnly(t *testing.T) {
	_, snap := blueprintProject(t)
	src := &testBlueprintSource{root: filepath.Join(t.TempDir(), "blueprint"), snap: snap}

	off := NewBlueprintView(src, "repo", false, 4000, 1500, nil)
	if got := off.Resident(); got != "" {
		t.Errorf("inject_into 之外的站点还是渲染了常驻块: %q", got)
	}

	// 执行器恒绑：摘掉注入不等于摘掉工具（§8：静默摘工具会让模型连问都不问）。
	fn := blueprintExecutor(off)
	if fn == nil {
		t.Fatalf("blueprintExecutor 返回了 nil")
	}
	out, err := fn(context.Background(), map[string]interface{}{"section": "modules"})
	if err != nil {
		t.Fatalf("read_blueprint 报错: %v", err)
	}
	text, _ := out.(string)
	if !strings.Contains(text, "internal/a") {
		t.Errorf("关掉注入后读通道也读不到骨架了:\n%s", text)
	}
}

// read_blueprint 在"这个项目根本没有蓝图"时给原因与修复方式，而不是空回执、
// 也不是把工具从面上摘掉（设计稿 §7.1/§8）。
func TestReadBlueprintDegradesInsteadOfVanishing(t *testing.T) {
	deps := &toolspec.Deps{Blueprint: blueprintExecutor(nil)}
	spec, ok := toolspec.Get("read_blueprint")
	if !ok || spec == nil {
		t.Fatalf("read_blueprint 没注册进注册表")
	}
	avail, reason := spec.Available(deps)
	if !avail {
		t.Errorf("没有蓝图时工具被摘掉了: %s", reason)
	}
	out, err := deps.Blueprint(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("降级回执不该报错: %v", err)
	}
	text, _ := out.(string)
	for _, want := range []string{"not available", "YAGENT_BLUEPRINT", "semantic_search"} {
		if !strings.Contains(text, want) {
			t.Errorf("降级回执少了 %q:\n%s", want, text)
		}
	}
}

// 这条是"补偿控制真的补上了那个洞"的证据：脚本先**只用文本交付**（就是今晚生产里
// 5/5 次的形态），提醒之后才读文件、才以 agent_exit 提交。
// 没有那条提醒，这个夹具会停在第一步，blueprint.md 根本不会出现。
func TestE2EPlainTextRepoRunIsNudgedIntoSubmitting(t *testing.T) {
	workDir, snap := blueprintProject(t)
	storeRoot := filepath.Join(t.TempDir(), "blueprint")
	view := NewBlueprintView(&testBlueprintSource{root: storeRoot, snap: snap}, "repo", true, 4000, 1500, nil)

	engine := newScripted(map[string][]scriptStep{"repo": {
		fixed(textResponse("internal/a 是这套夹具的算法片，调查完成。")),
		toolCallStep("read_file", map[string]interface{}{
			"target_file": "internal/a/a.go", "should_read_entire_file": true,
		}),
		fixed(exitWithBlueprint("补交结论。", []map[string]interface{}{
			{"section": "modules", "module": "internal/a", "field": "purpose",
				"claim": "holds the fixture algorithms", "where": "internal/a/a.go:3"},
		})),
	}})
	agent := newBlueprintRepoAgent(t, workDir, engine, view)

	if _, err := agent.Run(context.Background(), "看看 internal/a 是干什么的"); err != nil {
		t.Fatalf("委派失败: %v", err)
	}
	data, err := os.ReadFile(blueprint.DocPath(storeRoot))
	if err != nil {
		t.Fatalf("提醒没有把这次 run 推向 agent_exit，蓝图没落盘: %v", err)
	}
	if !strings.Contains(string(data), "holds the fixture algorithms") {
		t.Errorf("落盘了但内容不对:\n%s", data)
	}
	// 提醒必须真的发出去了（第二条 repo 调用里带着它），否则上一条断言可能只是脚本恰好走下去。
	if len(engine.CallsFor("repo")) < 2 {
		t.Fatalf("Repo 只被调用 %d 次，提醒分支根本没进", len(engine.CallsFor("repo")))
	}
	second := messagesText(engine.CallsFor("repo")[1].messages)
	if !strings.Contains(second, "Call `agent_exit` now") {
		t.Errorf("第二次调用里没有收尾契约的提醒:\n%s", truncateForFailure(second))
	}
	if strings.Contains(second, "explore/edit/run/verify") {
		t.Errorf("Repo 收到的是通用那条\"再去执行一轮\"提醒 —— 它的报告本来就是交付物")
	}
}

// 模型坚持只给文本时，补偿控制不许把分析本身弄丢：交付物仍是那段报告，
// 蓝图不出现（没有提交），而且不无限追。
func TestE2ERepoStillRefusingToExitKeepsItsReport(t *testing.T) {
	workDir, snap := blueprintProject(t)
	storeRoot := filepath.Join(t.TempDir(), "blueprint")
	view := NewBlueprintView(&testBlueprintSource{root: storeRoot, snap: snap}, "repo", true, 4000, 1500, nil)

	engine := newScripted(map[string][]scriptStep{"repo": {
		fixed(textResponse("第一版报告：internal/a 负责夹具算法。")),
		fixed(textResponse("第二版报告，仍然不收尾。")),
	}})
	agent := newBlueprintRepoAgent(t, workDir, engine, view)
	res, err := agent.Run(context.Background(), "看看 internal/a")
	if err != nil {
		t.Fatalf("提醒之后模型仍不收尾，不该把委派变成错误: %v", err)
	}
	if !strings.Contains(res.Deliverable, "报告") {
		t.Errorf("交付物丢了: %q", truncateForFailure(res.Deliverable))
	}
	if _, err := os.Stat(blueprint.DocPath(storeRoot)); err == nil {
		t.Errorf("没有任何提交却写出了文档")
	}
	if n := len(engine.CallsFor("repo")); n != 2 {
		t.Errorf("Repo 被调了 %d 次, 要 2 次（提醒一次，然后接受它的选择）", n)
	}
}
