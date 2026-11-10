package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yagent/internal/config"
	"yagent/internal/llm"
	"yagent/internal/memory"
	"yagent/internal/plasma"
)

// 本文件是 P9b 的三条 E2E（设计稿 §11 / §1.3 第 4、9 条），全部**只 mock 模型与子进程**：
//
//	链路① 生成 → 编译 → --fix 重生成 → 注册 → 执行 → 遥测
//	链路② 越权 → 升级门 → 用户批准（按 hash） → 新 hash 失效
//	链路③ store 被改一字节 → 加载期拒载，其余 Agent 照常加载
//
// 真到什么程度：真 Store（内容寻址 + hash 重算）、真 trampoline 驱动器（spawn/--fix/
// diagnostics 契约）、真 RunAgentLoop（scripted 只回答 LLM 响应）、真
// telemetry.jsonl（app 里装的 PlasmaTelemetrySink 是同一份函数）。
//
// 为什么子进程用 /bin/sh 假 python 而不起真 zipapp：生成质量与 Python 包属 P10 与 5b；
// 这里钉的是 Go 侧契约（argv 形状、diagnostics 落盘、账本字段），起真解释器只会让用例
// 依赖部署状态。trampoline_test.go 已用同一套假 python 打通了契约层，这里把它接到
// Director 的完整事务上。
//
// 每条用例都 t.Setenv("HOME", …)：telemetry/rollout/blackboard 都按 $HOME 定位，
// 不拥有自己的目录就会把测试数据写进开发机的 ~/.yagent（坑 23）。

// plasmaSpecEscalating 请求 run_bash（SysExec，在 PlasmaPalette 之外）：
// 阶段 2 会拒掉这个工具并报 PLS-201，阶段 3 记 PLS-210 + EscalationRequired。
const plasmaSpecEscalating = `{
  "thinking": "要验证命令注入必须有 shell",
  "agent_name": "Security Auditor",
  "agent_design": "You are a security auditor. Use read_file to inspect code and run_bash to reproduce the suspected command injection, then call agent_exit with reason.",
  "tools_used": ["read_file", "run_bash"],
  "task_for_agent": "审计 internal/security 的注入风险"
}`

// plasmaSpecHallucinated 含一个不存在的工具名（read_filez）⇒ PLS-101。
const plasmaSpecHallucinated = `{
  "thinking": "需要只读审计能力",
  "agent_name": "Security Auditor",
  "agent_design": "You are a security auditor. Use read_filez and search_by_regex to inspect code, then call agent_exit with reason.",
  "tools_used": ["read_filez", "search_by_regex"],
  "task_for_agent": "审计 internal/security 的注入风险"
}`

// plasmaSpecReadOnlyAuditor 是 --fix 之后的合法版本（能过 Compile + 结构夹具）。
const plasmaSpecReadOnlyAuditor = `{
  "thinking": "需要只读审计能力",
  "agent_name": "Security Auditor",
  "agent_design": "You are a security auditor. Use read_file and search_by_regex to inspect code, then call agent_exit with reason and a handoff conclusion.",
  "tools_used": ["read_file", "search_by_regex"],
  "task_for_agent": "审计 internal/security 的注入风险"
}`

// recordingPlasmaGen 包住真 trampoline，记下每次 Generate 的观测（Attempts 是
// "--fix 真的发生过"的直接证据：Attempts==2 只可能来自首轮编译失败后的重生成）。
type recordingPlasmaGen struct {
	inner   PlasmaGenerator
	results []*TrampolineResult
	errs    []error
	n       int
}

func (g *recordingPlasmaGen) Generate(ctx context.Context, task, note string) (*TrampolineResult, error) {
	g.n++
	res, err := g.inner.Generate(ctx, task, note)
	if res != nil {
		// 浅拷贝：驱动器返回后调用方仍可能改它，而断言要的是"当时"的值。
		cp := *res
		g.results = append(g.results, &cp)
	}
	g.errs = append(g.errs, err)
	return res, err
}

func (g *recordingPlasmaGen) calls() int { return g.n }

// withUsage 给一条脚本响应挂上 token 计数。
//
// 必须显式挂：遥测的 `tokens_in` 来自 AgentResult.Usage，而 scripted.TextResponse 不带
// usage —— 真实 provider 必带。链路①要断言 `tokens_in > 0`（§1.3 第 9 条：三个字段
// 都非零，否则就是"加了字段没人填"），所以这里补上真实形态。
func withUsage(resp *llm.Response, in, out int64) *llm.Response {
	resp.Usage = &llm.TokenUsage{
		PromptTokens: in, CompletionTokens: out,
		TotalTokens: in + out, TotalInputTokens: in,
	}
	return resp
}

// putPlasmaSpec 把一份 spec 走 Elaborate → Compile → Store.Put，返回 hash 与编译报告。
//
// 为什么先 Compile 再 Put：阶段 1 会补齐自动结构夹具，而 hash 必须对**落盘的那份字节**
// 计算 —— 补齐前算出来的 hash 与 store 里的不是同一份。
func putPlasmaSpec(t *testing.T, store *plasma.Store, spec string) (string, *plasma.Report) {
	t.Helper()
	return putPlasmaSpecWithDefaults(t, store, spec, plasma.DefaultDefaults())
}

func putPlasmaSpecWithDefaults(t *testing.T, store *plasma.Store, spec string, d plasma.Defaults) (string, *plasma.Report) {
	t.Helper()
	bundle, err := plasma.Elaborate([]byte(spec), d)
	if err != nil {
		t.Fatalf("Elaborate: %v", err)
	}
	report := (&plasma.Compiler{Palette: plasma.PlasmaPalette}).Compile(context.Background(), bundle)
	hash, err := store.Put(bundle, report)
	if err != nil {
		t.Fatalf("Store.Put: %v", err)
	}
	return hash, report
}

// escalationDefaults 是 Elaborate 的 Defaults 变体：把 shell 从 policy.deny 里去掉。
//
// 默认 Deny 是 [fs_write, shell] —— 它与"请求 run_bash"组合会先命中 PLS-206
// （工具的能力与 deny 冲突），那是另一条判据（自相矛盾的 bundle），会盖住本用例要验的
// 升级门（ADR-4）。这里只留 fs_write：run_bash 于是变成"声明了、也在注册表里、只是
// 超出 PlasmaPalette"，正是升级门要处理的那一类。
func escalationDefaults() plasma.Defaults {
	d := plasma.DefaultDefaults()
	d.Deny = []string{"fs_write"}
	return d
}

func hasDiagCode(r *plasma.Report, code string) bool {
	if r == nil {
		return false
	}
	for _, d := range r.Diagnostics {
		if d.Code == code {
			return true
		}
	}
	return false
}

// readTelemetry 读账本的所有行（文件不存在 ⇒ 空，不是错误）。
func readTelemetry(t *testing.T, root string) []plasma.TelemetryRecord {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "telemetry.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("读 telemetry.jsonl: %v", err)
	}
	var out []plasma.TelemetryRecord
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec plasma.TelemetryRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("telemetry 行不是合法 JSON: %v（%s）", err, line)
		}
		out = append(out, rec)
	}
	return out
}

// readStoreMeta 读 {name}/{hash}/meta.json（批准态的唯一权威落点）。
func readStoreMeta(t *testing.T, root, name, hash string) plasma.Meta {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, name, hash, "meta.json"))
	if err != nil {
		t.Fatalf("读 meta.json: %v", err)
	}
	var meta plasma.Meta
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("解析 meta.json: %v", err)
	}
	return meta
}

// setPlasmaEnabled 把全局 [plasma] 归到"开着"，并在用例结束时还原。
//
// 默认值本来就是开，但 CI 上可能带着 YAGENT_PLASMA=0（急停开关）。加载期读的是
// config.PlasmaEnabled()，这里把它钉死，避免"用例结果取决于开发机的环境变量"。
func setPlasmaEnabled(t *testing.T) {
	t.Helper()
	t.Setenv("YAGENT_PLASMA", "1")
	old := config.GlobalPlasma()
	if err := config.SetGlobalPlasma(config.DefaultPlasmaConfig(), config.PlasmaPresence{}); err != nil {
		t.Fatalf("SetGlobalPlasma: %v", err)
	}
	t.Cleanup(func() { _ = config.SetGlobalPlasma(old, config.PlasmaPresence{}) })
}

// ─── 链路① 生成 → --fix → 注册 → 执行 → 遥测 ──────────────────────────────

// TestE2E_Plasma_GenerateFixRegisterRunTelemetry 钉住整条在线事务与账本字段。
//
// 失败模式全是静默的：--fix 没发出去（首轮坏 bundle 被判死）、注册没进工具面（模型
// 看不见新 Agent）、循环没真跑（steps=0 但账本照样有一行）、遥测三个字段全零
// （§1.3 第 9 条明写要防的那个）。
func TestE2E_Plasma_GenerateFixRegisterRunTelemetry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	setPlasmaEnabled(t)

	workDir := t.TempDir()
	storeRoot := t.TempDir()

	// 假 python：首轮写幻觉工具名 bundle（exit 0 ⇒ 交给 Go 侧编译），
	// 第二轮必须看到 diagnostics.json（含 PLS-101）且 argv 带 --fix，才写合法 bundle。
	script := writeFakePython(t, `
if [ -f "$WD/diagnostics.json" ]; then
  case " $* " in
    *" --fix "*) ;;
    *) echo "第二轮 argv 缺 --fix: $*" >&2; exit 1 ;;
  esac
  if ! grep -q 'PLS-101' "$WD/diagnostics.json"; then
    echo "diagnostics.json 不含 PLS-101: $(cat "$WD/diagnostics.json")" >&2
    exit 1
  fi
  printf '%s' '`+plasmaSpecReadOnlyAuditor+`' > "$WD/bundle.json"
  exit 0
fi
printf '%s' '`+plasmaSpecHallucinated+`' > "$WD/bundle.json"
exit 0
`)

	engine := newScripted(map[string][]scriptStep{
		"director": {
			toolCallStep("delegate_meta", map[string]interface{}{
				"task": "生成一个安全审计 Agent 并让它审计 internal/security",
			}),
			// 生成之后必须**当场可委派**：这一步会失败在"注册没进工具面"或
			// "加载期坐标没登记"上（后者让账本没有 hash/grade）。
			toolCallStep("delegate_security_auditor", map[string]interface{}{
				"task": "复读一遍审计结论",
			}),
			fixed(textResponse("完成")),
		},
		// 身份键 = NormalizeIdentity(ca.DisplayName) = "security auditor"。
		"security auditor": {
			fixed(withUsage(exitWithHandoff("发现一处未过滤的 exec 拼接。DELIVERABLE-AUDIT-1", "存在命令注入风险"), 18400, 2100)),
		},
	})

	// 真 trampoline：Spawner 是 /bin/sh 假 python，LLM 用同一个 scripted 引擎
	// （假脚本不请求采样，所以它不会被调用 —— 这正是"编译失败走 --fix 而不是重采样"）。
	tr, gcBase := newTestTrampoline(t, script, engine, nil)
	gen := &recordingPlasmaGen{inner: tr}

	gctx := newTestGlobalCtx(workDir)
	director := newDirectorAgentForTest(gctx, engine, nil, nil, nil, gen, nil, nil, 10, nil, 3, config.Config{}, nil)
	director.plasmaStore = plasma.NewStore(storeRoot)
	director.plasmaRunner = fakeFixtureRunner{}
	// 与 internal/app 装的是**同一个** sink 函数：字段映射只有一份。
	director.SetDelegationMetricsHook(PlasmaTelemetrySink(storeRoot))

	if lines := readTelemetry(t, storeRoot); len(lines) != 0 {
		t.Fatalf("用例开始前账本应为空，实际 %d 行", len(lines))
	}

	ctx := memory.WithConversationMemory(context.Background(), memory.NewConversationMemory(300))
	if _, err := director.Run(ctx, "生成一个安全审计 Agent 并审计仓库"); err != nil {
		t.Fatalf("Director.Run: %v", err)
	}

	// 1) --fix 真的发生：Attempts==2 只可能来自"首轮编译失败 + 重生成成功"。
	if gen.calls() != 1 {
		t.Fatalf("Generate 调用 = %d，期望 1（delegate_meta 一次）", gen.calls())
	}
	if len(gen.errs) != 0 && gen.errs[0] != nil {
		t.Fatalf("Generate 失败：%v", gen.errs[0])
	}
	res := gen.results[0]
	if res.Attempts != 2 {
		t.Fatalf("Attempts = %d，期望 2（首轮 PLS-101 被拒 → --fix → 通过）", res.Attempts)
	}
	if res.LLMCalls != 0 {
		t.Errorf("LLMCalls = %d，期望 0（编译期反馈不该触发重新采样）", res.LLMCalls)
	}
	assertNoWorkdirs(t, gcBase)

	// 2) 注册：注册表 + 工具面都要有（只有注册表的话模型看不见）。
	if _, ok := director.metaHandler.Get("delegate_security_auditor"); !ok {
		t.Fatal("生成的 Agent 没有注册成 delegate_security_auditor")
	}
	if _, ok := director.plasmaAgentInfoOf("security_auditor"); !ok {
		t.Fatal("加载期坐标没登记：账本会缺 bundle_hash/grade")
	}

	// 3) RunAgentLoop 真的跑了（scripted 的调用记录非空），且工具面来自 bundle。
	calls := engine.CallsFor("security auditor")
	if len(calls) == 0 {
		t.Fatalf("生成式 Agent 一次 LLM 调用都没有（识别到的身份：%s）", seenAgents(engine))
	}
	if !hasToolNamed(calls[0].Tools, "read_file") {
		t.Errorf("第一次调用的工具面里没有 read_file: %v", toolNamesOf(calls[0].Tools))
	}
	if !hasToolNamed(calls[0].Tools, "agent_exit") {
		t.Errorf("工具面缺 agent_exit（MatchNames 必须自动追加收尾工具）: %v", toolNamesOf(calls[0].Tools))
	}

	// 4) 账本：恰好多出至少一行，其中的 plasma Agent 行三个指标都非零（§1.3 第 9 条）。
	lines := readTelemetry(t, storeRoot)
	if len(lines) == 0 {
		t.Fatal("telemetry.jsonl 没有新增任何行")
	}
	scored := 0
	for _, rec := range lines {
		if rec.Agent != "security_auditor" {
			continue
		}
		scored++
		if rec.Steps <= 0 {
			t.Errorf("账本 steps = %d，期望 > 0（循环没跑或字段没人填）", rec.Steps)
		}
		if rec.TokensIn <= 0 || rec.TokensOut <= 0 {
			t.Errorf("账本 tokens_in/out = %d/%d，期望都 > 0", rec.TokensIn, rec.TokensOut)
		}
		if strings.TrimSpace(rec.StopReason) == "" {
			t.Error("账本 stop_reason 为空：评分侧无法判断这次委派是怎么结束的")
		}
		if rec.BundleHash == "" || rec.Grade != plasma.GradeProvisional {
			t.Errorf("账本缺 bundle 坐标: hash=%q grade=%q", rec.BundleHash, rec.Grade)
		}
		if rec.FixtureRun {
			t.Error("生产委派被标成 fixture_run=true：夹具样本会污染评分")
		}
		if rec.Escalated {
			t.Error("只读 bundle 不该标 escalated")
		}
		if rec.Ts == "" {
			t.Error("账本缺 ts")
		}
	}
	if scored == 0 {
		t.Fatalf("账本里没有 agent=security_auditor 的行（agent 名可能被记成了 meta）：%+v", lines)
	}
	// 生成动作本身也不许被记成 "meta 跑了 N 步"（meta 没有 LLM 循环）。
	for _, rec := range lines {
		if rec.Agent == "meta" {
			t.Errorf("账本出现 agent=meta 的行（执行者身份应从 outcome 取）：%+v", rec)
		}
	}
}

// ─── 链路② 越权 → 升级门 → 按 hash 批准 → 新 hash 失效 ────────────────────

// approveConfirmer / rejectConfirmer 是 UserConfirmer 的两个极简实现：
// 升级门只关心"问到了、回答是什么"，与消息总线无关（窄接口的全部意义）。
type approveConfirmer struct {
	questions int
	lastQ     string
}

func (c *approveConfirmer) RequestConfirmation(_ context.Context, question, _ string, _ ...map[string]interface{}) (string, error) {
	c.questions++
	c.lastQ = question
	return "yes", nil
}

type rejectConfirmer struct{ questions int }

func (c *rejectConfirmer) RequestConfirmation(_ context.Context, _, _ string, _ ...map[string]interface{}) (string, error) {
	c.questions++
	return "no", nil
}

// TestE2E_Plasma_EscalationGateApprovalIsPerHash 钉住 ADR-4 的三段：
// 未批准 ⇒ provisional + 不可自动委派 + 越权工具不在工具面；批准 ⇒ 按 hash 记；
// 新 hash ⇒ 批准自然失效（不是被删，而是不适用）。
func TestE2E_Plasma_EscalationGateApprovalIsPerHash(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	setPlasmaEnabled(t)

	storeRoot := t.TempDir()
	store := plasma.NewStore(storeRoot)
	hashA, reportA := putPlasmaSpecWithDefaults(t, store, plasmaSpecEscalating, escalationDefaults())

	// 落盘的账目：report.json 里必须有 PLS-210，且报告本身标了 EscalationRequired。
	if !reportA.EscalationRequired {
		t.Fatalf("报告没有标 EscalationRequired: %v", reportA.Text())
	}
	if !hasDiagCode(reportA, plasma.CodeEscalation) {
		t.Fatalf("报告里没有 %s: %v", plasma.CodeEscalation, reportA.Text())
	}
	reportRaw, err := os.ReadFile(filepath.Join(storeRoot, "security_auditor", hashA, "report.json"))
	if err != nil {
		t.Fatalf("读 report.json: %v", err)
	}
	if !strings.Contains(string(reportRaw), plasma.CodeEscalation) {
		t.Errorf("report.json 里没有 %s（诊断没随产物落盘）: %s", plasma.CodeEscalation, reportRaw)
	}

	director := newTestDirectorAgent(t, t.TempDir())
	director.plasmaStore = store
	director.loadPlasmaAgents(0)

	info, ok := director.plasmaAgentInfoOf("security_auditor")
	if !ok {
		t.Fatal("越权 bundle 没有被加载：升级门于是永远问不出口（ADR-4 的死锁）")
	}
	if info.Grade != plasma.GradeProvisional {
		t.Errorf("grade = %q，期望 provisional（升级上限）", info.Grade)
	}
	if !info.EscalationRequired || info.EscalationApproved {
		t.Errorf("批准态 = required:%v approved:%v，期望 true/false", info.EscalationRequired, info.EscalationApproved)
	}
	if info.AutoDelegate {
		t.Error("未批准的越权 Agent 不许自动委派")
	}
	if containsString(info.Tools, "run_bash") {
		t.Errorf("未批准时越权工具不许进工具面: %v", info.Tools)
	}
	if !containsString(info.Tools, "read_file") {
		t.Errorf("只读工具应当保留（能力受限不等于不可用）: %v", info.Tools)
	}

	ctx := context.Background()
	// 2a) 没有确认通道 ⇒ 明确收尾，不是崩溃。
	if err := director.ensureEscalationApproved(ctx, "security_auditor"); err == nil ||
		!strings.Contains(err.Error(), "需要用户批准写/shell 能力") {
		t.Fatalf("缺确认通道时应给出可读收尾，得到: %v", err)
	}
	// 2b) 用户拒绝 ⇒ 同样收尾（不是崩溃），且不许写入批准。
	reject := &rejectConfirmer{}
	director.userConfirm = reject
	if err := director.ensureEscalationApproved(ctx, "security_auditor"); err == nil ||
		!strings.Contains(err.Error(), "用户未批准") {
		t.Fatalf("拒绝批准时应说明用户未批准，得到: %v", err)
	}
	if reject.questions != 1 {
		t.Errorf("拒绝路径应问一次，实际 %d", reject.questions)
	}
	if meta := readStoreMeta(t, storeRoot, "security_auditor", hashA); meta.EscalationApproved {
		t.Fatal("用户拒绝后 meta.json 却被写成已批准")
	}
	// 2c) 用户同意 ⇒ 批准按 hash 落盘，工具面换成复核过的版本。
	approve := &approveConfirmer{}
	director.userConfirm = approve
	if err := director.ensureEscalationApproved(ctx, "security_auditor"); err != nil {
		t.Fatalf("批准后仍不放行: %v", err)
	}
	if approve.questions != 1 {
		t.Errorf("同意路径应问一次，实际 %d", approve.questions)
	}
	if !strings.Contains(approve.lastQ, shortHash(hashA)) {
		t.Errorf("提问里应带 bundle hash（用户批准的是这一份）: %q", approve.lastQ)
	}
	approved, _ := director.plasmaAgentInfoOf("security_auditor")
	if !approved.EscalationApproved {
		t.Error("批准后 EscalationApproved 仍为 false")
	}
	if !containsString(approved.Tools, "run_bash") {
		t.Errorf("批准后越权工具必须进工具面（否则放行是个空话）: %v", approved.Tools)
	}
	if approved.AutoDelegate {
		t.Error("批准不改档位：provisional 仍低于 auto_delegate_min_grade(verified)，不许自动委派")
	}
	if meta := readStoreMeta(t, storeRoot, "security_auditor", hashA); !meta.EscalationApproved {
		t.Fatal("批准没有写进 meta.json：重启后同一份 bundle 会再次不可用")
	}

	// 3) 新 hash ⇒ 批准自然失效。用**新实例**加载，模拟重启（同实例不会覆盖已注册的
	//    Agent：bundle 不可变、执行器不能被悄悄换掉）。
	hashB, _ := putPlasmaSpecWithDefaults(t, store,
		strings.Replace(plasmaSpecEscalating,
			"suspected command injection", "suspected filename injection", 1), escalationDefaults())
	if hashB == hashA {
		t.Fatal("两份内容不同的 bundle 得到了同一个 hash（主键失效）")
	}
	if meta := readStoreMeta(t, storeRoot, "security_auditor", hashA); !meta.EscalationApproved {
		t.Fatal("旧 hash 的批准记录不该被新版本删掉（批准是按 hash 记的，不是覆盖语义）")
	}

	reloaded := newTestDirectorAgent(t, t.TempDir())
	reloaded.plasmaStore = store
	reloaded.loadPlasmaAgents(0)
	fresh, ok := reloaded.plasmaAgentInfoOf("security_auditor")
	if !ok {
		t.Fatal("新 hash 没有被加载")
	}
	if fresh.Hash != hashB {
		t.Fatalf("刷新后 hash = %q，期望新版本 %q", fresh.Hash, hashB)
	}
	if fresh.EscalationApproved {
		t.Fatal("新 hash 继承了旧 hash 的批准：一次批准被后续所有变异继承（ADR-4/T7 要修的正是这个）")
	}
	if fresh.AutoDelegate {
		t.Error("新 hash 未批准 ⇒ 不许自动委派")
	}
}

// ─── 链路③ 篡改拒载 ──────────────────────────────────────────────────────

// TestE2E_Plasma_TamperedBundleIsRejectedOthersStillLoad 钉住 §1.3 第 4 条：
// 改一个字节 ⇒ 加载期 hash 重算不符（PLS-701）⇒ 该 Agent 不注册，其余照常加载。
func TestE2E_Plasma_TamperedBundleIsRejectedOthersStillLoad(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	setPlasmaEnabled(t)

	storeRoot := t.TempDir()
	store := plasma.NewStore(storeRoot)

	specAlpha := strings.Replace(plasmaSpecReadOnlyAuditor, "Security Auditor", "Alpha Auditor", 1)
	specBeta := strings.Replace(plasmaSpecReadOnlyAuditor, "Security Auditor", "Beta Auditor", 1)
	hashAlpha, _ := putPlasmaSpec(t, store, specAlpha)
	putPlasmaSpec(t, store, specBeta)

	// 改一字节但保持 JSON 合法：把 prompt 里的一个词换掉。改坏结构会让 Load 失败在
	// parse 而不是 hash 比对，那就没验到"篡改检测"这条链。
	bundlePath := filepath.Join(storeRoot, "alpha_auditor", hashAlpha, "bundle.json")
	raw, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatalf("读 bundle.json: %v", err)
	}
	tampered := strings.Replace(string(raw), "security auditor", "security audit0r", 1)
	if tampered == string(raw) {
		t.Fatal("没有改到任何字节，用例无效")
	}
	if err := os.WriteFile(bundlePath, []byte(tampered), 0o600); err != nil {
		t.Fatalf("写回 bundle.json: %v", err)
	}

	// 3a) 账目层：LoadAll 必须点名拒载原因（PLS-701），而不是静默少一个。
	loaded, skipped, _ := plasma.LoadAll(store, &plasma.Compiler{Palette: plasma.PlasmaPalette}, 0)
	if len(loaded) != 1 || len(skipped) != 1 {
		t.Fatalf("loaded/skipped = %d/%d，期望 1/1（只拒载被改的那一份）", len(loaded), len(skipped))
	}
	if skipped[0].Name != "alpha_auditor" {
		t.Errorf("被拒载的是 %q，期望 alpha_auditor", skipped[0].Name)
	}
	if !strings.Contains(skipped[0].Reason, plasma.CodeLoadTampered) {
		t.Errorf("拒载原因里没有 %s（无法 grep、也无法指向修法）: %s", plasma.CodeLoadTampered, skipped[0].Reason)
	}
	if loaded[0].Plan.Agent.Name != "beta_auditor" {
		t.Errorf("加载成功的是 %q，期望 beta_auditor", loaded[0].Plan.Agent.Name)
	}

	// 3b) 装配层：Director 侧的效果 —— beta 可委派，alpha 从工具面消失。
	director := newTestDirectorAgent(t, t.TempDir())
	director.plasmaStore = store
	if n := director.loadPlasmaAgents(0); n != 1 {
		t.Errorf("loadPlasmaAgents = %d，期望 1（拒载一份、加载一份）", n)
	}
	if _, ok := director.metaHandler.Get("delegate_beta_auditor"); !ok {
		t.Error("未被篡改的 Agent 没有加载成功（拒载把整批拖下水）")
	}
	if _, ok := director.metaHandler.Get("delegate_alpha_auditor"); ok {
		t.Error("被篡改的 Agent 进了注册表（hash 重算没生效）")
	}
	if !containsString(director.surface.Names(), "delegate_beta_auditor") {
		t.Error("beta 不在工具面上：模型看不见它")
	}
	if containsString(director.surface.Names(), "delegate_alpha_auditor") {
		t.Error("被篡改的 Agent 出现在工具面上")
	}
}

// ─── 加载期预算（§9.2 的 25ms）──────────────────────────────────────────

// TestE2E_Plasma_LoadStaysWithinFrameBudget 钉住"加载期不阻塞首帧"。
//
// §6.5 要求先测量再决定要不要改成首轮惰性加载；没有这条用例，25ms 只是个愿望 ——
// 一次把 12 份 bundle 全跑阶段 1–3 的改动（或某天有人把它挪进 rebuildSurface 的循环里）
// 会让首帧变慢而没有任何断言变红。预算与 `plasmaLoadBudget` 同源，改那个常量即改这里。
func TestE2E_Plasma_LoadStaysWithinFrameBudget(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	setPlasmaEnabled(t)

	storeRoot := t.TempDir()
	store := plasma.NewStore(storeRoot)
	const n = 12 // DefaultMaxLoadedAgents：预算满编时才是最坏情况
	for i := 0; i < n; i++ {
		spec := strings.Replace(plasmaSpecReadOnlyAuditor, "Security Auditor",
			fmt.Sprintf("Auditor %02d", i), 1)
		putPlasmaSpec(t, store, spec)
	}

	director := newTestDirectorAgent(t, t.TempDir())
	director.plasmaStore = store
	start := time.Now()
	loaded := director.loadPlasmaAgents(0)
	elapsed := time.Since(start)

	if loaded != n {
		t.Fatalf("loaded = %d，期望 %d（否则这条用例没测到最坏情况）", loaded, n)
	}
	if elapsed > plasmaLoadBudget {
		t.Fatalf("加载期耗时 %v 超预算 %v（§9.2；应改为首轮惰性加载并保持字典序）",
			elapsed, plasmaLoadBudget)
	}
	t.Logf("加载期实测 %v（预算 %v，%d 个 Agent）", elapsed, plasmaLoadBudget, n)
}
