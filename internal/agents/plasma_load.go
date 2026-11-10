package agents

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"yagent/internal/config"
	"yagent/internal/plasma"
	"yagent/internal/tools"
	"yagent/internal/toolspec"
)

// 本文件是 P9b 的三件装配，全部围绕**已落盘的生成式 Agent**：
//
//  1. 加载期（§6.5）：Director 构造期把 store 里的 Agent 装进 `metaHandler` + 工具面。
//     加载期只跑阶段 1–3（`plasma.LoadLink`），不跑阶段 4（token 计数）——§6.1 明写。
//  2. 升级批准回路（§6.6 / ADR-4）：越权 bundle 的 grade 上限是 provisional，首次委派
//     必须经 `UserConfirmManager` 明示批准；批准**按 bundle hash** 写进 `meta.json`。
//  3. 遥测坐标（ADR-6）：`plasmaAgentInfo` 是"这个 delegate 对应哪一份 bundle、什么档位"
//     的唯一来源，`directorToolRunner.Call` 的汇聚点在写账本时读它。
//
// 三条设计取向，缺一条都会变成静默缺陷：
//   - **账目必须看得见**：拒载/省略/超预算各一行日志并点名（坑 43），不是 Debug；
//   - **批准必须按 hash**：按 name 记会让一次批准被后续所有变异继承（ADR-4 / T7）；
//   - **运行中不热替换**：`plasmaAgentInfo.Hash` 在加载时固定，一次委派用它到底；
//     版本变更只在下一轮 Surface 重建（新进程/新 Run 的加载）生效。

// UserConfirmer 是升级批准用的窄接口。`*tools.UserConfirmManager` 天然实现它；
// 窄接口的理由与 PlasmaGenerator 同：Director 只需要"问一句、拿回答"，
// 不认识消息总线、request_id 与超时细节。
type UserConfirmer interface {
	RequestConfirmation(ctx context.Context, question, options string, extra ...map[string]interface{}) (string, error)
}

// plasmaAgentInfo 是一个已加载 Agent 的坐标与档位（key = `CustomAgent.Name`）。
//
// Tools / Palette 是**当前生效**的工具面，不是 bundle 声明的原始清单：未批准时越权工具
// 已被阶段 2 拒掉（不在 Tools 里）；批准后由 applyEscalation 换成重新复核过的版本。
// 想"直接读 bundle.tools 装配"就会绕过升级门 —— 那正是 ADR-4 要修的那个洞。
type plasmaAgentInfo struct {
	Hash               string
	Grade              plasma.Grade
	EscalationRequired bool
	EscalationApproved bool
	// AutoDelegate 是按 `[plasma] auto_delegate_min_grade` 当场算出的准入结论。
	// 它是**派生值**（grade + 批准态），存在这里只为让账目与判据读同一份结论。
	AutoDelegate bool
	Tools        []string
	Palette      toolspec.Effect
}

// rememberPlasmaAgent 登记一个已加载 Agent 的坐标。plan 必须来自复核（Link/LoadLink）。
func (a *DirectorAgent) rememberPlasmaAgent(plan *plasma.LinkPlan, meta plasma.Meta) {
	if plan == nil || plan.Agent.Name == "" {
		return
	}
	minGrade := plasma.NormalizeMinGrade(config.GlobalPlasma().AutoDelegateMinGrade)
	approved := plan.EscalationApproved || meta.EscalationApproved
	info := plasmaAgentInfo{
		Hash:               plan.Hash,
		Grade:              plan.Grade,
		EscalationRequired: plan.EscalationRequired,
		EscalationApproved: approved,
		AutoDelegate:       plasma.CanAutoDelegate(plan.Grade, plan.EscalationRequired, approved, minGrade),
		Tools:              append([]string(nil), plan.Agent.Tools...),
		Palette:            plan.Profile.Palette,
	}
	a.plasmaMu.Lock()
	if a.plasmaAgents == nil {
		a.plasmaAgents = make(map[string]plasmaAgentInfo)
	}
	a.plasmaAgents[plan.Agent.Name] = info
	a.plasmaMu.Unlock()
}

// plasmaAgentInfoOf 返回某个 Agent 的坐标副本。ok=false 表示它不是 plasma 产物
// （内建 Agent 或老路径注册的 CustomAgent）—— 调用方必须据此跳过升级门与 bundle 字段。
func (a *DirectorAgent) plasmaAgentInfoOf(name string) (plasmaAgentInfo, bool) {
	a.plasmaMu.RLock()
	defer a.plasmaMu.RUnlock()
	info, ok := a.plasmaAgents[name]
	return info, ok
}

func (a *DirectorAgent) storePlasmaAgentInfo(name string, info plasmaAgentInfo) {
	a.plasmaMu.Lock()
	defer a.plasmaMu.Unlock()
	if a.plasmaAgents == nil {
		a.plasmaAgents = make(map[string]plasmaAgentInfo)
	}
	a.plasmaAgents[name] = info
}

// loadPlasmaAgents 是加载期的唯一入口（§6.5）。在 Director 构造期调用一次。
//
// 顺序即契约：**先**注册进 `metaHandler`（注册表），**再**注册进工具面（Surface.Add，
// 由 registerCustomAgent 一次做完）。反过来会让"工具面有、注册表没有"的 Agent 在
// `delegateTo` 里解析不到 —— 那是一次必然失败的委派。
func (a *DirectorAgent) loadPlasmaAgents(max int) (loaded int) {
	if !config.PlasmaEnabled() {
		slog.Info("plasma: 加载期跳过（[plasma] enable=false 或 YAGENT_PLASMA=0）")
		return 0
	}
	if a.plasmaStore == nil {
		// 装配层漏传 store 时**不**回落到 config.PlasmaStoreDir()：那会让单测把用户 home
		// 里的 Agent 装进来（一次断言就依赖开发机的落盘状态）。这句话就是那条可执行提示。
		slog.Warn("plasma: 加载期未装配 PlasmaStore，已落盘的 Agent 不会被加载",
			"修复", "在 internal/app 给 DirectorAgentParams.PlasmaStore 传 plasma.NewStore(config.PlasmaStoreDir())")
		return 0
	}
	if max <= 0 {
		max = config.GlobalPlasma().MaxLoadedAgents
	}

	start := time.Now()
	compiler := &plasma.Compiler{Palette: plasma.PlasmaPalette}
	entries, skipped, omitted := plasma.LoadAll(a.plasmaStore, compiler, max)
	names := make([]string, 0, len(entries))
	for _, item := range entries {
		if !a.registerLoadedPlasmaAgent(item) {
			continue
		}
		names = append(names, item.Plan.Agent.Name)
	}
	loaded = len(names)
	elapsed := time.Since(start)

	// 加载期一行总账：条数、预算、耗时。耗时进日志的理由是 §9.2 的 25ms 预算 ——
	// 没有这个数字，"加载期是不是阻塞了首帧"只能靠感觉。
	slog.Info("plasma: 加载期完成", "loaded", loaded, "skipped", len(skipped),
		"omitted", omitted, "budget", max, "load_ms", elapsed.Milliseconds(), "agents", names)
	if elapsed > plasmaLoadBudget {
		slog.Warn("plasma: 加载期超出预算（下一阶段应改为首轮惰性加载）",
			"load_ms", elapsed.Milliseconds(), "budget_ms", plasmaLoadBudget.Milliseconds())
	}
	for _, s := range skipped {
		// 单项拒载一行，点名 name / hash / 原因（原因里带工具名与诊断码，见 plasma.LoadAll）。
		slog.Warn("plasma: Agent 拒载", "agent", s.Name, "hash", shortHash(s.Hash),
			"reason", s.Reason, "修复", "重新生成该 Agent，或删除/回滚 store 里那一份")
	}
	if omitted > 0 {
		// 省略账：少了几个 Agent 而没人说，与"它从来没存在过"无法区分。
		slog.Warn("plasma: 加载预算截断（其余 Agent 未装载）",
			"omitted", omitted, "budget", max, "修复", "调大 [plasma] max_loaded_agents，或清理低价值的旧 hash")
	}
	return loaded
}

// plasmaLoadBudget 是 §9.2 的加载延迟预算。超了**不**回滚行为，只留一行可见账目 ——
// 静默变慢与静默变少一样难查。
const plasmaLoadBudget = 25 * time.Millisecond

// registerLoadedPlasmaAgent 把一份 Loaded 装成一个可委派工具，并登记执行参数与坐标。
func (a *DirectorAgent) registerLoadedPlasmaAgent(item plasma.Loaded) bool {
	if item.Plan == nil {
		return false
	}
	ca := customAgentFromPlan(item.Plan)
	if !a.registerCustomAgent(ca) {
		// 同名已注册：不覆盖（bundle 不可变，工具执行器也不能被悄悄换掉）。
		slog.Warn("plasma: 加载期同名冲突，保留现有注册", "agent", ca.Name, "hash", shortHash(item.Plan.Hash))
		return false
	}
	a.setExecutorConfig(ca.Name, item.Plan.Config)
	a.rememberPlasmaAgent(item.Plan, item.Meta)
	if item.Plan.EscalationRequired && !item.Plan.EscalationApproved {
		// 升级门的第一处可见账目：它被装进来了，但能力是残缺的，首次委派要问用户。
		slog.Info("plasma: 该 Agent 需要升级批准（首次委派会询问用户）",
			"agent", ca.Name, "hash", shortHash(item.Plan.Hash), "grade", string(item.Plan.Grade))
	}
	return true
}

// ensureEscalationApproved 是升级门的**唯一**入口：需要批准而尚未批准时，现场问一次用户。
//
// 返回 nil 表示"可以继续装配"。返回 error 表示这次委派必须收尾（不是崩溃）：调用方把
// 错误文案原样交给模型，模型看到的是"需要用户批准写/shell 能力"，而不是一次匿名失败。
func (a *DirectorAgent) ensureEscalationApproved(ctx context.Context, name string) error {
	info, ok := a.plasmaAgentInfoOf(name)
	if !ok || !info.EscalationRequired || info.EscalationApproved {
		return nil
	}

	if a.yoloMode() {
		// YOLO 照旧绕过询问，但**不写** meta.json：用户批准与策略放行不是一件事，
		// 把 YOLO 写成"用户批准过"会让下次非 YOLO 运行时静默放行。账本记 escalated=true。
		slog.Warn("plasma: 升级能力在 YOLO 下放行（未写 meta.json 批准）",
			"agent", name, "hash", shortHash(info.Hash))
		return a.applyEscalation(name, info, false)
	}
	if a.userConfirm == nil {
		return fmt.Errorf("Agent %s 需要用户批准写/shell 能力，但本次运行没有装配用户确认通道；"+
			"修复：在 internal/app 给 DirectorAgentParams.UserConfirm 传 globalCtx.UserConfirmMgr", name)
	}

	answer, err := a.userConfirm.RequestConfirmation(ctx,
		fmt.Sprintf("plasma Agent %q 请求写/shell 能力（bundle %s）。批准按 bundle hash 记，新版本需重新批准。",
			name, shortHash(info.Hash)),
		"yes/no",
		map[string]interface{}{
			"agent":       name,
			"bundle_hash": info.Hash,
			"tool_name":   "delegate_" + name,
			"reason":      "plasma escalation gate（ADR-4）",
		})
	if err != nil {
		return fmt.Errorf("Agent %s 需要用户批准写/shell 能力，确认通道失败：%w", name, err)
	}
	if !tools.IsAffirmativeAnswer(answer) {
		return fmt.Errorf("Agent %s 需要用户批准写/shell 能力（用户未批准：%q）；"+
			"批准按 bundle hash 记，批准后重新委派即可", name, strings.TrimSpace(answer))
	}
	if err := a.plasmaStoreFor().ApproveEscalation(name, info.Hash); err != nil {
		// 写不进 meta.json 就不放行：批准必须可复核、可跨进程复现，
		// "这次先跑通"会让同一份 bundle 在重启后又变成不可用。
		return fmt.Errorf("Agent %s 的升级批准写入失败（本次不放行）：%w", name, err)
	}
	return a.applyEscalation(name, info, true)
}

// applyEscalation 用**批准位**重新复核同一份 hash，并把生效工具面换成复核结果。
//
// 为什么必须重新 Link：未批准时越权工具在阶段 2 就被拒了，`info.Tools` 里根本没有它们；
// 只把 bool 翻成 true 会让"已批准"变成一个不影响装配的空话。
//
// 重新读的是 `info.Hash` 而不是 index 的当前值：一次委派保持加载时的版本（"运行中不热替换"）。
// 版本变更只会在下一轮加载（新进程 / 新一轮 Run 的重建）生效。
func (a *DirectorAgent) applyEscalation(name string, info plasmaAgentInfo, metaRecorded bool) error {
	store := a.plasmaStoreFor()
	bundle, _, err := store.Load(name, info.Hash)
	if err != nil {
		return fmt.Errorf("Agent %s 的升级复核读取失败：%w", name, err)
	}
	approved := plasma.EscalationEffects(bundle, plasma.PlasmaPalette)
	compiler := &plasma.Compiler{Palette: plasma.PlasmaPalette, EscalationApproved: approved}
	plan, err := compiler.LoadLink(bundle, info.Hash)
	if err != nil {
		return fmt.Errorf("Agent %s 的升级复核失败（工具可能在注册表里被改名/删除）：%w", name, err)
	}

	info.EscalationApproved = true
	info.Tools = append([]string(nil), plan.Agent.Tools...)
	info.Palette = plan.Profile.Palette
	info.Grade = plan.Grade // 批准不改档位：verified 仍要 5b 的行为夹具证据
	info.AutoDelegate = plasma.CanAutoDelegate(plan.Grade, info.EscalationRequired, true,
		plasma.NormalizeMinGrade(config.GlobalPlasma().AutoDelegateMinGrade))
	a.storePlasmaAgentInfo(name, info)

	slog.Warn("plasma: 升级已放行", "agent", name, "hash", shortHash(info.Hash),
		"escalation_bits", approved.Names(), "tools", info.Tools,
		"meta_recorded", metaRecorded, "grade", string(info.Grade), "auto_delegate", info.AutoDelegate)
	return nil
}

// yoloMode 读 YOLO 判据。env 可能为 nil（CLI/测试的部分路径），nil 一律按"非 YOLO"处理：
// 把"没装配"当成"全放行"是最危险的一种默认值。
func (a *DirectorAgent) yoloMode() bool {
	return a.env != nil && a.env.FullYoloMode()
}

// shortHash 把 64 位 hash 截成前 12 位，给日志与用户提示用（全文进 telemetry）。
func shortHash(h string) string {
	h = strings.TrimSpace(h)
	if len(h) <= 12 {
		return h
	}
	return h[:12]
}

// clipCatalogLine 按 rune 截断一行目录描述。limit<=0 = 不裁剪（`[plasma]
// catalog_line_chars` 的显式 0 就是这个意思，与"没写"取默认 120 严格区分）。
// 返回 (文本, 是否真的截断)：省略账要的是"有几行被截断"，而不是"截了几次"。
func clipCatalogLine(s string, limit int) (string, bool) {
	s = strings.TrimSpace(s)
	if limit <= 0 || utf8.RuneCountInString(s) <= limit {
		return s, false
	}
	runes := []rune(s)
	return string(runes[:limit]) + "…", true
}
