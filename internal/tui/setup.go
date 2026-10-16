// 首次使用引导（docs/config-trim-onboarding-design.md §6）。
//
// 为什么要有它：模板自带 use_provider 和一个 api_key = "your-key" 的完整块，而校验只查非空，
// 于是全新机器"配置合法、TUI 正常启动"，直到第一次提交任务才在 LLM 调用上失败 —— 而那条错误
// 只进 ~/.yagent/logs，终端上什么都看不到。
//
// 向导本身不依赖 Yagent/Dispatcher/MCP：它只读配置文件文本、写配置文件，以及（可选）发一次
// 最小 LLM 请求。所以配置坏到加载不了时它照样能跑 —— 那正是最需要它的时刻。
package tui

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"yagent/internal/config"
	"yagent/internal/llm"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
)

// setupStep 是向导的一屏。顺序即枚举顺序，欢迎 → provider → 默认档 → 首启项 → 摘要。
type setupStep int

const (
	stepWelcome setupStep = iota
	stepProviders
	stepProviderForm
	stepProbe
	stepDefault
	stepFirstRun
	stepSummary
)

func (s setupStep) title() string {
	switch s {
	case stepWelcome:
		return "WELCOME"
	case stepProviders:
		return "PROVIDERS"
	case stepProviderForm:
		return "PROVIDER"
	case stepProbe:
		return "CONNECTIVITY"
	case stepDefault:
		return "DEFAULT PROVIDER"
	case stepFirstRun:
		return "FIRST-RUN CHOICES"
	case stepSummary:
		return "REVIEW & WRITE"
	}
	return "SETUP"
}

// pendingOp 是一次待落盘的修改。只有摘要屏按 enter 才真正写文件，
// 所以在此之前放弃 = 配置文件逐字节没动过。
type pendingOp struct {
	kind     string // write | delete | default | firstrun
	provider config.ProviderDraft
	firstRun config.FirstRunDraft
	name     string
	note     string
}

// providerRow 是配置文件里一个 provider 块的可编辑视图。
type providerRow struct {
	draft config.ProviderDraft
	refs  []string // 谁在引用它（use_provider / fallback_providers）
}

// setupSnapshot 是向导读到的现状。一律走原始 TOML 解码而不是 LoadFromFile：
// "想删掉一个坏 provider"恰恰常发生在配置已经加载不了的时候。
type setupSnapshot struct {
	useProvider string
	providers   []providerRow
	lang        string
	yolo        bool
	fullYolo    bool
	routingMode string
	// browserEnabled 为 nil 表示 [browser] 段里没写过这个键（缺省按 DefaultBrowserConfig 的 true）。
	browserEnabled *bool
	llmTimeout     time.Duration
}

// 密钥来源是一行两态（见 setup_keys.go 的 formRows 注释）。常量而不是 bool：
// 三个消费点（load / collect / toggle）都要说得清"现在是哪一种"。
const (
	keyFromLiteral = iota
	keyFromEnv
)

// setupWizard 是可被两个宿主驱动的状态机：独立程序（坏配置启动、`yagent setup`）
// 与主 TUI 里的全屏模式（/setup）。对外只有 Init/Update/View/SetSize/Done/Saved。
type setupWizard struct {
	path   string
	reason config.SetupReason
	detail string

	step   setupStep
	note   string // 上一次成功的结果
	errMsg string // 上一次失败的原文，逐字显示不美化

	snap setupSnapshot

	cursor   int
	fieldIdx int
	inputs   []textarea.Model
	editing  string // 正在编辑的 provider 名（"" = 新增）
	keyMode  int    // keyFromLiteral | keyFromEnv，见 formRows 的第 3 行
	// ctxAutoValue 记住"上一次由目录回填的那个数"。只有仍等于它时才允许再回填，
	// 于是手输过的 context_window 永远不会被目录覆盖（§3.2）。
	ctxAutoValue int
	optionalOpen bool // 可选区是否展开
	presetOpen   bool // 表单屏内的端点预设子模式（不是新的一屏）
	probing      bool
	probeText    string
	probeLatency time.Duration
	probeStart   time.Time
	probeDone    time.Time
	spinFrame    int
	// probeTarget / probeModel 是"这一次探测在打哪儿"的现场：探测屏要在等待时就把它显示出来，
	// 而不是等结果回来再猜 —— 那正是这一屏唯一能证明"没有假装在工作"的东西。
	probeTarget string
	probeModel  string
	// probeErr 与 probeText 分开存：混在一个字段里，失败原文会被当成"模型的回答"涂绿。
	probeErr string

	// firstRunIdx 是 5 个首启项各自选到的下标，见 setupRender 的 firstRunRows。
	firstRunIdx [5]int

	// summaryTop 是摘要屏的滚动偏移：摘要没有"选中某条 op"这回事（要么全写要么不写），
	// 所以 j/k 只动视图，不动状态。
	summaryTop int

	ops []pendingOp
	// writtenCount 由 applyOps 维护：它不是事务，失败时"已经写了几条"是用户唯一能自救的事实。
	writtenCount int

	width, height int
	done          bool
	saved         bool
	// standalone 由 RunSetup 置真：独立宿主必须把 done 翻成 tea.Quit，
	// 否则 q / ctrl+c 只改了状态，程序停在最后一屏。嵌在主 TUI 里时它是假的 ——
	// 向导退出 = 交还键盘，不是退出 yagent。
	standalone bool
}

func (w *setupWizard) Done() bool  { return w.done }
func (w *setupWizard) Saved() bool { return w.saved }

// newSetupWizard 读取现状并落在欢迎屏。
func newSetupWizard(path string, reason config.SetupReason, detail string) *setupWizard {
	w := &setupWizard{
		path:    path,
		reason:  reason,
		detail:  detail,
		step:    stepWelcome,
		editing: "",
	}
	w.snap = readSetupSnapshot(path)
	w.initFirstRunIdx()
	w.ensureInputs()
	return w
}

// readSetupSnapshot 解出向导要展示的字段。解不动的一律留零值 —— 这里只是只读展示，
// 合法性判定仍然由 validate() 在下次启动时负责。
func readSetupSnapshot(path string) setupSnapshot {
	snap := setupSnapshot{routingMode: "auto", llmTimeout: config.DefaultLLMCallTimeout}
	raw, err := config.DecodeRaw(path)
	if err != nil {
		return snap
	}

	if gllm := subMap(raw, "global", "llm"); gllm != nil {
		snap.useProvider, _ = gllm["use_provider"].(string)
		if provs, ok := gllm["providers"].(map[string]any); ok {
			names := make([]string, 0, len(provs))
			for n := range provs {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				entry, ok := provs[n].(map[string]any)
				if !ok {
					continue
				}
				snap.providers = append(snap.providers, providerRow{
					draft: draftFromEntry(n, entry),
					refs:  config.ProviderRefs(raw, n),
				})
			}
		}
	}

	if a, ok := raw["agent"].(map[string]any); ok {
		snap.lang, _ = a["lang"].(string)
		snap.yolo, _ = a["yolo_mode"].(bool)
		snap.fullYolo, _ = a["full_yolo_mode"].(bool)
	}
	if r, ok := raw["routing"].(map[string]any); ok {
		if m, ok := r["mode"].(string); ok && m != "" {
			snap.routingMode = m
		}
	}
	if b, ok := raw["browser"].(map[string]any); ok {
		if e, ok := b["enabled"].(bool); ok {
			snap.browserEnabled = &e
		}
	}
	if l, ok := raw["llm"].(map[string]any); ok {
		if s, ok := l["timeout"].(string); ok {
			if d, err := time.ParseDuration(s); err == nil {
				snap.llmTimeout = d
			}
		}
	}
	return snap
}

// subMap 沿键路径逐层下钻；任一层不是 map 就返回 nil。
func subMap(m map[string]any, keys ...string) map[string]any {
	var cur any = m
	for _, k := range keys {
		next, ok := cur.(map[string]any)[k]
		if !ok {
			return nil
		}
		cur = next
	}
	out, _ := cur.(map[string]any)
	return out
}

func draftFromEntry(name string, entry map[string]any) config.ProviderDraft {
	d := config.ProviderDraft{Name: name}
	d.Model, _ = entry["model"].(string)
	d.APIBaseURL, _ = entry["api_base_url"].(string)
	d.APIKey, _ = entry["api_key"].(string)
	d.APIKeyEnv, _ = entry["api_key_env"].(string)
	d.APIFormat, _ = entry["api_format"].(string)
	d.ReasoningEffort, _ = entry["reasoning_effort"].(string)
	d.MaxTokens = intAt(entry["max_tokens"])
	d.ContextWindow = intAt(entry["context_window"])
	if f, ok := entry["temperature"].(int64); ok {
		d.Temperature = float64(f)
	}
	if f, ok := entry["temperature"].(float64); ok {
		d.Temperature = f
	}
	return d
}

func intAt(v any) int {
	n, _ := v.(int64) // BurntSushi 把 TOML 整数解成 int64
	return int(n)
}

// effectiveProviders 是列表屏该显示的内容：文件现状 + 已排队的增删改。
func (w *setupWizard) effectiveProviders() []providerRow {
	out := append([]providerRow{}, w.snap.providers...)
	for _, op := range w.ops {
		switch op.kind {
		case "write":
			replaced := false
			for i := range out {
				if out[i].draft.Name == op.provider.Name {
					out[i].draft = op.provider
					replaced = true
					break
				}
			}
			if !replaced {
				out = append(out, providerRow{draft: op.provider})
			}
		case "delete":
			for i := range out {
				if out[i].draft.Name == op.name {
					out = append(out[:i], out[i+1:]...)
					break
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].draft.Name < out[j].draft.Name })
	return out
}

// rowAt 返回光标所在行；列表为空时给零值行。
func (w *setupWizard) rowAt(rows []providerRow) providerRow {
	if len(rows) == 0 {
		return providerRow{}
	}
	if w.cursor >= len(rows) {
		w.cursor = len(rows) - 1
	}
	return rows[w.cursor]
}

// pendingFor 找某个 provider 已排队的写操作（编辑到一半再编辑要接着改同一条）。
func (w *setupWizard) pendingFor(name string) (config.ProviderDraft, bool) {
	for _, op := range w.ops {
		if op.kind == "write" && op.provider.Name == name {
			return op.provider, true
		}
	}
	return config.ProviderDraft{}, false
}

func (w *setupWizard) dropOpsFor(name string) {
	kept := w.ops[:0]
	for _, op := range w.ops {
		if (op.kind == "write" && op.provider.Name == name) || (op.kind == "delete" && op.name == name) {
			continue
		}
		kept = append(kept, op)
	}
	w.ops = kept
}

func (w *setupWizard) dropOpsOfKind(kind string) {
	kept := w.ops[:0]
	for _, op := range w.ops {
		if op.kind == kind {
			continue
		}
		kept = append(kept, op)
	}
	w.ops = kept
}

// queueProviderOp 校验并排队表单里的草稿。
// 这里只做本地校验（名字合法 / 有 model / 有密钥来源 / 有 base_url）；"这个端点认不认这把 key"
// 是探测屏的事 —— 校验器不联网，联网的探测也不假装在校验。
//
// base_url 这一条以前没查：config.WriteProvider 只要求"base_url 与密钥来源不全空"，
// 而 validate() 要求每个 provider 都有 base_url（config.go:592）。于是填了 key 忘了 URL
// 能一路放行到落盘，下一启动直接加载失败 —— 正是这个向导存在的理由。
func (w *setupWizard) queueProviderOp() {
	d := w.collectDraft()
	if err := config.IsValidProviderName(d.Name); err != nil {
		w.errMsg = strings.TrimPrefix(err.Error(), "config: ")
		return
	}
	if d.APIBaseURL == "" {
		w.errMsg = "api_base_url is required (startup validates it for every provider)"
		return
	}
	if d.Model == "" {
		w.errMsg = "model is required"
		return
	}
	if d.APIKey == "" && d.APIKeyEnv == "" {
		w.errMsg = "either api_key or api_key_env is required"
		return
	}
	w.errMsg = ""
	w.note = ""
	if d.APIKeyEnv != "" {
		if v, ok := os.LookupEnv(d.APIKeyEnv); !ok || v == "" {
			// 不拦：写了 .env 再启动的人在路上。探测会诚实失败并点名这个变量。
			w.note = fmt.Sprintf("note: $%s is not set in this shell", d.APIKeyEnv)
		}
	}
	w.dropOpsFor(d.Name)
	w.ops = append(w.ops, pendingOp{
		kind:     "write",
		provider: d,
		note:     fmt.Sprintf("write [global.llm.providers.%s]", d.Name),
	})
	w.editing = d.Name
	w.step = stepProviders
}

// queueDeleteOp 排队删除。被引用时直接拒绝并把引用位置显示出来 —— 让用户先改引用，
// 而不是写出一份"下次启动加载不了"的文件。
func (w *setupWizard) queueDeleteOp(name string) {
	if refs := w.referencesTo(name); len(refs) > 0 {
		w.errMsg = fmt.Sprintf("%s is still referenced by: %s", name, strings.Join(refs, ", "))
		return
	}
	w.dropOpsFor(name)
	w.ops = append(w.ops, pendingOp{kind: "delete", name: name, note: "delete [global.llm.providers." + name + "]"})
	w.errMsg = ""
	if w.editing == name {
		w.editing = ""
	}
}

// queueDefaultOp 排队"把某家设为默认"。列表屏的 s 与默认档屏的 enter 共用这一份实现：
// 两处若各写一条 op，引用扫描（referencesTo）就要认两种形状。
func (w *setupWizard) queueDefaultOp(name string) {
	w.dropOpsOfKind("default")
	w.ops = append(w.ops, pendingOp{
		kind: "default", name: name, note: `use_provider = "` + name + `"`,
	})
}

// referencesTo 在"当前视图"上重算引用：已排队的默认档变更要算进来，
// 否则删掉一个刚被设为默认的 provider 会一路放行到写盘才炸。
func (w *setupWizard) referencesTo(name string) []string {
	def := w.snap.useProvider
	for _, op := range w.ops {
		if op.kind == "default" {
			def = op.name
		}
		if op.kind == "firstrun" && op.firstRun.UseProvider != "" {
			def = op.firstRun.UseProvider
		}
	}
	if def == name {
		return []string{"global.llm.use_provider"}
	}
	for _, row := range w.effectiveProviders() {
		for _, r := range row.refs {
			if strings.HasPrefix(r, "global.llm.providers."+name+".") {
				return []string{r}
			}
		}
	}
	return nil
}

// applyOps 按队列顺序逐条写文件，失败立即停止。
// 这不是事务：失败之前的几条已经落盘了，所以错误信息必须带上"是哪一条 note 失败了"
// 和"已经写了几条"，用户才知道剩下的自己改。把它包装成"什么都没发生"才是真的撒谎。
func (w *setupWizard) applyOps() error {
	w.writtenCount = 0
	for _, op := range w.ops {
		var err error
		switch op.kind {
		case "write":
			err = config.WriteProvider(w.path, op.provider)
		case "delete":
			err = config.DeleteProvider(w.path, op.name)
		case "default":
			err = config.PersistUseProvider(w.path, "global.llm", op.name)
		case "firstrun":
			err = config.WriteFirstRun(w.path, op.firstRun)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", op.note, err)
		}
		w.writtenCount++
	}
	return nil
}

// probeResultMsg 是一次连通性探测的结果。延迟与原文都必须来自真实往返，
// 否则这一屏就是在给用户假保证。
type probeResultMsg struct {
	text    string
	latency time.Duration
	err     error
}

// probeTimeout 比 [llm] timeout 短得多是有意的：探测只要求"这个端点认这把 key"，
// 不要求模型把问题思考完。
const probeTimeout = 30 * time.Second

// probeTickMsg 驱动那一格的转圈。刻意不接 internal/tui/anim —— 那个包目前全仓零引用，
// 把一个正在交付的界面接到没有测试的包上是新风险（决策 DD）。
type probeTickMsg struct{}

// probeTickInterval 是计时 seam：测试把它调短，而不是 sleep 等一帧。
var probeTickInterval = 120 * time.Millisecond

func tickProbe() tea.Cmd {
	return tea.Tick(probeTickInterval, func(time.Time) tea.Msg { return probeTickMsg{} })
}

// startProbeCmd 发一次最小请求（无工具、极小 max_tokens），并挂上转圈的 tick。
func (w *setupWizard) startProbeCmd(d config.ProviderDraft) tea.Cmd {
	w.probing = false
	w.probeText = ""
	w.probeErr = ""
	w.probeLatency = 0
	w.note = ""
	w.errMsg = ""
	if d.Model == "" || (d.APIKey == "" && d.APIKeyEnv == "") {
		w.errMsg = "cannot probe: model and a key are both required"
		return nil
	}
	w.probing = true
	w.spinFrame = 0
	w.probeStart = time.Now()
	w.probeTarget = d.APIBaseURL
	w.probeModel = d.Model
	probe := func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
		defer cancel()
		start := time.Now()
		provider := &config.ProviderConfig{
			Model:           d.Model,
			APIBaseURL:      d.APIBaseURL,
			APIKey:          d.APIKey,
			ApiFormat:       d.APIFormat,
			ReasoningEffort: d.ReasoningEffort,
			MaxTokens:       16,
		}
		engine := llm.NewEngine(provider, config.LLMConfig{Timeout: probeTimeout, MaxRetries: 0})
		resp, err := engine.GenerateContent(ctx,
			[]llm.Message{{Role: llm.RoleUser, Content: "Reply with exactly: ok"}},
			nil,
			&llm.CallOptions{MaxTokens: 16},
		)
		if err != nil {
			return probeResultMsg{err: err, latency: time.Since(start)}
		}
		text := ""
		if len(resp.Choices) > 0 {
			text = resp.Choices[0].Content
		}
		return probeResultMsg{text: strings.TrimSpace(text), latency: time.Since(start)}
	}
	return tea.Batch(probe, tickProbe())
}

// probeHost 只取 scheme+host 上屏：带 token 的 URL 不该整条摊在终端里，
// 而 path 对"这个端点认不认这把 key"也没有信息量。
func probeHost(baseURL string) string {
	s := strings.TrimSpace(baseURL)
	if s == "" {
		return "(no base_url)"
	}
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	return s
}

func (w *setupWizard) Init() tea.Cmd { return nil }
