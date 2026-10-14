package agents

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"yagent/internal/globalctx"
	"yagent/internal/llm"
	"yagent/internal/messaging"
	"yagent/internal/tools"
)

// 授权回路是"危险操作要问人"的唯一实现，横跨四层：
//
//	Adapter.Call → WorkspaceGuard.Check/RequestAuth → UserConfirmManager
//	→ 事件总线 user_help_needed →（UI）→ user_help_response → 按 request_id 配对
//	→ 允许则执行工具；拒绝则把原因当作工具回执回灌给模型
//
// 现有 fullchain 用例一律 FullYoloMode=true 且 Guard=nil，于是这条链**一次都没被走过**：
// 链上任一环坏了（配对键改了、consumer 没注册、拒绝没回灌）都不会有测试变红，
// 而表现是"要么静默放行越界写盘，要么用户答了却没反应"。
//
// 这里的 harness 按 app.go:115-287 的**同一条装配顺序**接线，
// 用真实 dispatcher + 真实 guard + 真实文件/Shell 工具，只把 LLM 与"用户的答复"换成脚本。

type authHarness struct {
	dispatcher *messaging.MessageDispatcher
	publisher  *messaging.MessagePublisher
	confirm    *tools.UserConfirmManager
	guard      *tools.WorkspaceGuard
	requests   *capturedRequests
	gctx       *globalctx.GlobalCtx
	mock       *fullChainMockLLM
}

// capturedRequests 记录总线上真正发出去的授权请求，供用例断言"问了什么"。
//
// hook 返回答复文本（"" = 不答，交给用例自己控制，用于测取消）；
// idFor 决定**用哪个 request_id 作答** —— 两者必须分开，否则"错配 id"会被写成
// "答复了一个不被允许的词"，那测的是拒绝分支而不是配对分支。
type capturedRequests struct {
	mu        sync.Mutex
	seen      []map[string]interface{}
	hook      func(content map[string]interface{}) string
	respondAs func(content map[string]interface{}) string
}

func (c *capturedRequests) record(content map[string]interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seen = append(c.seen, content)
}

func (c *capturedRequests) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.seen)
}

func (c *capturedRequests) all() []map[string]interface{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]map[string]interface{}(nil), c.seen...)
}

func (c *capturedRequests) replyFor(content map[string]interface{}) string {
	c.mu.Lock()
	hook := c.hook
	c.mu.Unlock()
	if hook == nil {
		return ""
	}
	return hook(content)
}

func (c *capturedRequests) idFor(content map[string]interface{}) string {
	c.mu.Lock()
	fn := c.respondAs
	c.mu.Unlock()
	if fn == nil {
		return ""
	}
	return fn(content)
}

// responder 是测试里的"用户"：把授权请求换成答复事件发回总线。
type responder struct {
	pub  *messaging.MessagePublisher
	reqs *capturedRequests
}

func (r *responder) ID() string { return "test-responder" }

func (r *responder) Types() []messaging.EventType {
	return []messaging.EventType{messaging.EventType("user_help_needed")}
}

func (r *responder) Consume(_ context.Context, ev *messaging.Event) error {
	content, ok := ev.Content.(map[string]interface{})
	if !ok {
		return nil
	}
	r.reqs.record(content)
	reply := r.reqs.replyFor(content)
	if reply == "" {
		return nil // 故意不答：用例稍后自己决定（取消）
	}
	id, _ := content["request_id"].(string)
	if override := r.reqs.idFor(content); override != "" {
		id = override
	}
	return r.pub.Publish("user_help_response", map[string]interface{}{
		"request_id": id,
		"response":   reply,
	}, "test-user")
}

// newAuthHarness 复刻 app.go 的接线顺序：
// publisher → mgr.SetPublisher → FlowOps.UserConfirmMgr → guard → RegisterConsumer(mgr)。
// 顺序不是风格问题：SetPublisher 没跑的话 RequestConfirmation 会直接报
// "publisher not set"，用例看到的只是"从没问过用户"。
func newAuthHarness(t *testing.T, workDir string) *authHarness {
	t.Helper()

	dispatcher := messaging.NewMessageDispatcher(256)
	publisher := messaging.NewMessagePublisher(dispatcher)
	confirm := tools.NewUserConfirmManager()

	gctx := newTestGlobalCtx(workDir)
	gctx.Publisher = publisher
	// FullYoloMode 保持 false：YOLO 把这条链短路了，测了等于没测。

	confirm.SetPublisher(publisher)
	gctx.FlowOps.UserConfirmMgr = confirm

	guard := tools.NewWorkspaceGuard(workDir, confirm)
	gctx.Guard = guard

	dispatcher.RegisterConsumer(confirm)

	reqs := &capturedRequests{}
	dispatcher.Subscribe(&responder{pub: publisher, reqs: reqs})

	t.Cleanup(dispatcher.Shutdown)

	return &authHarness{
		dispatcher: dispatcher,
		publisher:  publisher,
		confirm:    confirm,
		guard:      guard,
		requests:   reqs,
		gctx:       gctx,
	}
}

// autoAnswer 让 harness 对每个授权请求立刻回给定答复。
func (h *authHarness) autoAnswer(reply string) {
	h.requests.mu.Lock()
	defer h.requests.mu.Unlock()
	h.requests.hook = func(map[string]interface{}) string { return reply }
}

// codingWithResponses 用真实工具面 + 真实守卫 + 脚本化 LLM 构造 CodingAgent。
func (h *authHarness) codingWithResponses(responses ...*llm.Response) *CodingAgent {
	mock := &fullChainMockLLM{responses: responses}
	mock.SetDefaultOnExhaust(makeTextThenExitResponse("脚本耗尽，安全收尾"))
	h.mock = mock

	g := h.gctx
	return NewCodingAgent(CodingAgentParams{
		Formatter:   g,
		Publisher:   h.publisher,
		Env:         globalctx.NewEnvView(g),
		Files:       g.FileOps,
		Search:      g.SearchOps,
		Sys:         g.SysOps,
		Edit:        g.ReplaceTool,
		RepoOps:     g.RepoOps,
		Flow:        g.FlowOps,
		Thinker:     g.ThinkingTool,
		DeepThinker: g.DeepThinkingTool,
		Designer:    g.DesignTool,
		MicroAgent:  g.MicroAgentTool,
		Guard:       h.guard,
		// repoCtx 在 Run 里被无条件 Get()，nil 会直接 panic —— 与 fullchain 一致给一个空 store。
		RepoCtx:  globalctx.NewRepoContextStore(),
		LLM:      mock,
		MaxSteps: 6,
	})
}

// callMessages 返回第 n 次（从 1 起）LLM 调用实际收到的消息。
func (h *authHarness) callMessages(t *testing.T, n int) []llm.Message {
	t.Helper()
	if h.mock == nil {
		t.Fatal("harness 还没构造过 CodingAgent")
	}
	h.mock.mu.Lock()
	defer h.mock.mu.Unlock()
	if n > len(h.mock.callLog) {
		t.Fatalf("只发生了 %d 次 LLM 调用，取不到第 %d 次的消息", len(h.mock.callLog), n)
	}
	return h.mock.callLog[n-1].Messages
}

// toolEcho 取最近一次发给模型的消息里，指定工具的那条回执正文。
// 断言它，就是在断言"模型下一轮看得见这次执行的结果/失败原因"。
func (h *authHarness) toolEcho(t *testing.T, toolName string) string {
	t.Helper()
	msgs := h.callMessages(t, h.mock.CallCount())
	last := ""
	for _, m := range msgs {
		if m.Role == llm.RoleTool && m.ToolName == toolName {
			last = m.Content
		}
	}
	if last == "" {
		t.Fatalf("第 %d 次调用的消息里没有 %s 的回执（消息：%s）",
			h.mock.CallCount(), toolName, dumpContents(msgs))
	}
	return last
}

func dumpContents(msgs []llm.Message) string {
	parts := make([]string, 0, len(msgs))
	for _, m := range msgs {
		c := m.Content
		if len([]rune(c)) > 160 {
			c = string([]rune(c)[:160]) + "…"
		}
		parts = append(parts, string(m.Role)+":"+c)
	}
	return strings.Join(parts, " | ")
}

func toolCallJSON(v map[string]interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func waitUntil(t *testing.T, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("超时等待：%s", desc)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// ---------- 用例 ----------

// 批准路径：越界写盘必须真的发生，且模型下一轮拿得到工具回执。
func TestE2E_AuthorizationApproveExecutesTool(t *testing.T) {
	workDir := t.TempDir()
	outside := filepath.Join(filepath.Dir(workDir), "outside-approved.txt")
	defer func() { _ = os.Remove(outside) }()

	h := newAuthHarness(t, workDir)
	h.autoAnswer("allow")

	coding := h.codingWithResponses(
		makeToolCallResponse("create_file", toolCallJSON(map[string]interface{}{
			"target_file": outside,
			"content":     "created after approval",
		})),
		makeTextThenExitResponse("done"),
	)

	if _, err := coding.Run(context.Background(), "写一个工作区外的文件"); err != nil {
		t.Fatalf("Coding.Run: %v", err)
	}

	// 1) 总线上确实问过，而且问的内容足够让人决策。
	if got := h.requests.count(); got != 1 {
		t.Fatalf("授权请求 = %d 条, want 1（越界写盘必须问）", got)
	}
	req := h.requests.all()[0]
	if req["tool_name"] != "create_file" {
		t.Errorf("tool_name = %v, want create_file", req["tool_name"])
	}
	if id, _ := req["request_id"].(string); id == "" {
		t.Error("request_id 为空, want 非空（配对的键）")
	}
	if reason, _ := req["reason"].(string); !strings.Contains(reason, "工作空间外部") {
		t.Errorf("reason = %q, want 说明越界", reason)
	}

	// 2) 批准后副作用真的发生 —— 这是链路终点，不能只满足"没报错"。
	data, err := os.ReadFile(outside)
	if err != nil {
		t.Fatalf("批准后文件应当存在: %v", err)
	}
	if string(data) != "created after approval" {
		t.Errorf("写入内容 = %q, want created after approval", data)
	}

	// 3) 回执进了下一次 LLM 调用的消息。
	echo := h.toolEcho(t, "create_file")
	if strings.Contains(echo, "Error:") {
		t.Errorf("批准后工具却报错: %s", echo)
	}
	if !strings.Contains(echo, filepath.Base(outside)) {
		t.Errorf("工具回执没点名写到的文件: %s", echo)
	}
}

// 拒绝路径：不执行，而且拒绝原因必须让模型看见 ——
// 否则模型只看到一个失败，会拿同一条越界路径反复重试。
func TestE2E_AuthorizationDenyIsFedBackToModel(t *testing.T) {
	workDir := t.TempDir()
	outside := filepath.Join(filepath.Dir(workDir), "outside-denied.txt")
	defer func() { _ = os.Remove(outside) }()

	h := newAuthHarness(t, workDir)
	h.autoAnswer("deny")

	coding := h.codingWithResponses(
		makeToolCallResponse("create_file", toolCallJSON(map[string]interface{}{
			"target_file": outside,
			"content":     "should never exist",
		})),
		makeTextThenExitResponse("用户拒绝，停止"),
	)

	if _, err := coding.Run(context.Background(), "写工作区外的文件"); err != nil {
		t.Fatalf("Coding.Run: %v", err)
	}

	if got := h.requests.count(); got != 1 {
		t.Fatalf("授权请求 = %d 条, want 1", got)
	}
	if fileExists(outside) {
		t.Error("用户已拒绝，文件仍然被写出去了")
	}
	echo := h.toolEcho(t, "create_file")
	if !strings.Contains(echo, "用户拒绝了操作") {
		t.Errorf("模型没看到拒绝原因: %s", echo)
	}
}

// allow_session 之后，同一工具的越界操作不再逐个询问。
// 这条语义靠 guard.sessionAllowed；一旦 Check 的判断顺序被改动（比如把它挪到
// 路径检查之后），弹窗就会变成骚扰。
func TestE2E_AuthorizationAllowSessionStopsAsking(t *testing.T) {
	workDir := t.TempDir()
	a := filepath.Join(filepath.Dir(workDir), "sess-1.txt")
	b := filepath.Join(filepath.Dir(workDir), "sess-2.txt")
	defer func() { _ = os.Remove(a); _ = os.Remove(b) }()

	h := newAuthHarness(t, workDir)
	h.autoAnswer("allow_session")

	coding := h.codingWithResponses(
		makeToolCallResponse("create_file", toolCallJSON(map[string]interface{}{"target_file": a, "content": "1"})),
		makeToolCallResponse("create_file", toolCallJSON(map[string]interface{}{"target_file": b, "content": "2"})),
		makeTextThenExitResponse("done"),
	)

	if _, err := coding.Run(context.Background(), "写两个越界文件"); err != nil {
		t.Fatalf("Coding.Run: %v", err)
	}
	if got := h.requests.count(); got != 1 {
		t.Errorf("授权请求 = %d 条, want 1（allow_session 之后同工具不该再问）", got)
	}
	for _, p := range []string{a, b} {
		if !fileExists(p) {
			t.Errorf("%s 应当被写出", p)
		}
	}
}

// 工作区内的路径不该打扰用户：判据是"越界"而不是"写操作"。
// 退化成"所有写都问"的话，正常编码任务会被弹窗淹没。
func TestE2E_AuthorizationInWorkspaceNeedsNoPrompt(t *testing.T) {
	workDir := t.TempDir()
	h := newAuthHarness(t, workDir)
	// 一旦被问就拒绝，用例因此能区分"没问"与"问了且允许"。
	h.autoAnswer("deny")

	inside := filepath.Join(workDir, "inside.txt")
	coding := h.codingWithResponses(
		makeToolCallResponse("create_file", toolCallJSON(map[string]interface{}{"target_file": inside, "content": "ok"})),
		makeTextThenExitResponse("done"),
	)

	if _, err := coding.Run(context.Background(), "在工作区里写文件"); err != nil {
		t.Fatalf("Coding.Run: %v", err)
	}
	if got := h.requests.count(); got != 0 {
		t.Errorf("工作区内写盘触发了 %d 次授权请求, want 0", got)
	}
	if !fileExists(inside) {
		t.Error("工作区内的文件没被写出")
	}
}

// 响应带错 request_id 时必须继续等：解错栈等于"用户答了 A，B 却放行"。
func TestE2E_AuthorizationIgnoresMismatchedRequestID(t *testing.T) {
	workDir := t.TempDir()
	outside := filepath.Join(filepath.Dir(workDir), "mismatched.txt")
	defer func() { _ = os.Remove(outside) }()

	h := newAuthHarness(t, workDir)
	var mu sync.Mutex
	realID := ""
	h.requests.mu.Lock()
	// 答复文本是合法的 allow，但故意用**别人的** request_id 作答。
	h.requests.hook = func(map[string]interface{}) string { return "allow" }
	h.requests.respondAs = func(content map[string]interface{}) string {
		id, _ := content["request_id"].(string)
		mu.Lock()
		realID = id
		mu.Unlock()
		return "not-the-right-id"
	}
	h.requests.mu.Unlock()

	coding := h.codingWithResponses(
		makeToolCallResponse("create_file", toolCallJSON(map[string]interface{}{"target_file": outside, "content": "x"})),
		makeTextThenExitResponse("done"),
	)

	done := make(chan error, 1)
	go func() {
		_, err := coding.Run(context.Background(), "越界写盘")
		done <- err
	}()

	waitUntil(t, "授权请求发出", func() bool { return h.requests.count() >= 1 })

	// 假 id 的答复不该放行 —— 给它一点时间，工具必须还悬在那里。
	time.Sleep(150 * time.Millisecond)
	if fileExists(outside) {
		t.Fatal("错配 request_id 的答复放行了工具执行")
	}

	mu.Lock()
	id := realID
	mu.Unlock()
	if id == "" {
		t.Fatal("没抓到真实 request_id")
	}
	if err := h.publisher.Publish("user_help_response", map[string]interface{}{
		"request_id": id, "response": "allow",
	}, "test-user"); err != nil {
		t.Fatalf("publish response: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("补上正确 id 的答复后任务应当跑完: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("正确 request_id 的答复到达后仍在等待（配对失败）")
	}
	if !fileExists(outside) {
		t.Errorf("放行后文件未写出；工具回执 = %s", h.toolEcho(t, "create_file"))
	}
}

// 用户一直不答时，取消必须能解开在飞的工具调用，而不是挂到步数耗尽。
func TestE2E_AuthorizationCancelReleasesBlockedTool(t *testing.T) {
	workDir := t.TempDir()
	outside := filepath.Join(filepath.Dir(workDir), "never-approved.txt")
	defer func() { _ = os.Remove(outside) }()

	h := newAuthHarness(t, workDir)
	h.requests.mu.Lock()
	h.requests.hook = func(map[string]interface{}) string { return "" } // 永远不答
	h.requests.mu.Unlock()

	coding := h.codingWithResponses(
		makeToolCallResponse("create_file", toolCallJSON(map[string]interface{}{"target_file": outside, "content": "x"})),
		makeTextThenExitResponse("不该走到这里"),
	)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := coding.Run(ctx, "越界写盘，用户不答")
		errCh <- err
	}()

	waitUntil(t, "授权请求发出", func() bool { return h.requests.count() >= 1 })
	cancel()

	select {
	case <-errCh:
	case <-time.After(5 * time.Second):
		t.Fatal("取消后 RunAgentLoop 仍卡在授权等待中")
	}
	if fileExists(outside) {
		t.Error("取消的授权请求最终却放行了写盘")
	}
}

// Shell 走的是另一条判据（is_dangerous），但必须接在同一条回路上。
func TestE2E_AuthorizationRunBashDangerous(t *testing.T) {
	workDir := t.TempDir()
	h := newAuthHarness(t, workDir)
	h.autoAnswer("allow")

	coding := h.codingWithResponses(
		makeToolCallResponse("run_bash", toolCallJSON(map[string]interface{}{
			"command":      "echo hi",
			"is_dangerous": true,
		})),
		makeTextThenExitResponse("done"),
	)

	if _, err := coding.Run(context.Background(), "跑一条被标记危险的命令"); err != nil {
		t.Fatalf("Coding.Run: %v", err)
	}
	if got := h.requests.count(); got != 1 {
		t.Fatalf("危险命令的授权请求 = %d 条, want 1", got)
	}
	if req := h.requests.all()[0]; req["tool_name"] != "run_bash" {
		t.Errorf("tool_name = %v, want run_bash", req["tool_name"])
	}
	if echo := h.toolEcho(t, "run_bash"); !strings.Contains(echo, "hi") {
		t.Errorf("run_bash 的输出没有回灌给模型: %s", echo)
	}
}
