package llm

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"yagent/internal/config"
	"yagent/internal/models"
)

// Tier 是模型自动路由的三个档位。
type Tier int

const (
	TierFast     Tier = iota // 快速模型：特别简单的执行类 / 运维类任务
	TierBalanced             // 性价比模型：常规编码 / 分析 / 问答
	TierHigh                 // 高智商模型：有难度或需要设计 / 给出方案的任务
)

func (t Tier) String() string {
	switch t {
	case TierFast:
		return "fast"
	case TierBalanced:
		return "balanced"
	case TierHigh:
		return "high"
	default:
		return "balanced"
	}
}

// ParseTier 把配置 / 分类器返回的字符串解析为 Tier，无法识别时返回 fallback。
func ParseTier(s string, fallback Tier) Tier {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "fast":
		return TierFast
	case "balanced", "mid", "cost":
		return TierBalanced
	case "high", "smart":
		return TierHigh
	default:
		return fallback
	}
}

// defaultBaselines 是各 agent 的角色基线档位，可被 config.Routing.Baselines 覆盖。
var defaultBaselines = map[string]Tier{
	"devops":   TierFast,
	"repo":     TierBalanced,
	"chat":     TierBalanced,
	"coding":   TierBalanced,
	"browser":  TierBalanced,
	"meta":     TierHigh,
	"director": TierHigh,
}

// tierKey 是 ctx 中携带 Tier 的键类型。
type tierKey struct{}

// WithTier 把档位写入 ctx，供下游 RunAgentLoop 解析实际引擎。
func WithTier(ctx context.Context, t Tier) context.Context {
	return context.WithValue(ctx, tierKey{}, t)
}

// TierFrom 读取 ctx 中的档位覆盖（通常由 delegate 分类器写入）。
func TierFrom(ctx context.Context) (Tier, bool) {
	t, ok := ctx.Value(tierKey{}).(Tier)
	return t, ok
}

// TierInfo 描述某一档选中的 provider / 模型 / 分数，用于 TUI 展示。
type TierInfo struct {
	Tier     Tier
	Provider string
	Model    string
	Score    float64
}

// RouteSnapshot 是 Router 的只读视图，供 TUI 渲染。
type RouteSnapshot struct {
	Enabled   bool
	Mode      string // "auto" | "fixed"
	Reason    string // Enabled=false 时的原因
	Tiers     [3]TierInfo
	Baselines map[string]string
}

// Router 持有三档引擎与角色基线，并按需做任务难度分类。
// 由 Client.BuildRouter 在启动时构建一次并缓存。
type Router struct {
	enabled  bool
	mode     string
	reason   string
	engines  [3]Engine
	provider [3]string
	model    [3]string
	score    [3]float64

	baselines map[string]Tier
}

// Enabled 报告自动路由是否生效（false 时一切回退到固定 provider 行为）。
func (r *Router) Enabled() bool { return r != nil && r.enabled }

// EngineFor 返回指定档位的引擎；未启用时返回 nil。
func (r *Router) EngineFor(t Tier) Engine {
	if !r.Enabled() {
		return nil
	}
	return r.engines[t]
}

// ModelFor 返回指定档位选中的模型名（未启用时为空串）。
func (r *Router) ModelFor(t Tier) string {
	if !r.Enabled() {
		return ""
	}
	return r.model[t]
}

// ScoreFor 返回指定档位的 ai_score。
func (r *Router) ScoreFor(t Tier) float64 {
	if !r.Enabled() {
		return 0
	}
	return r.score[t]
}

// BaselineOf 返回 agent 的角色基线档位（未知 agent 回退到 balanced）。
func (r *Router) BaselineOf(agent string) Tier {
	if r == nil {
		return TierBalanced
	}
	if t, ok := r.baselines[strings.ToLower(agent)]; ok {
		return t
	}
	return TierBalanced
}

// Snapshot 返回只读视图。
func (r *Router) Snapshot() RouteSnapshot {
	if r == nil {
		return RouteSnapshot{Mode: "fixed", Reason: "router not initialized"}
	}
	snap := RouteSnapshot{
		Enabled:   r.enabled,
		Mode:      r.mode,
		Reason:    r.reason,
		Baselines: make(map[string]string, len(r.baselines)),
	}
	for i := 0; i < 3; i++ {
		snap.Tiers[i] = TierInfo{
			Tier:     Tier(i),
			Provider: r.provider[i],
			Model:    r.model[i],
			Score:    r.score[i],
		}
	}
	for k, v := range r.baselines {
		snap.Baselines[k] = v.String()
	}
	return snap
}

// disabledRouter 构造一个未启用的 Router（固定模式或分类失败时使用）。
func disabledRouter(mode, reason string, baselines map[string]Tier) *Router {
	return &Router{enabled: false, mode: mode, reason: reason, baselines: baselines}
}

// resolveBaselines 合并默认基线与配置覆盖。
func resolveBaselines(cfg config.RoutingConfig) map[string]Tier {
	out := make(map[string]Tier, len(defaultBaselines))
	for k, v := range defaultBaselines {
		out[k] = v
	}
	for k, v := range cfg.Baselines {
		out[strings.ToLower(k)] = ParseTier(v, TierBalanced)
	}
	return out
}

// setRouter 在锁保护下缓存 router 并返回它。
func (c *Client) setRouter(r *Router) *Router {
	c.mu.Lock()
	c.router = r
	c.mu.Unlock()
	return r
}

// BuildRouter 依据配置构建（或禁用）自动路由，并在 Client 上缓存。
// 候选池 = 配置中所有 provider 的模型；按严格基准交集打分选出三档。
// 无法产生三档不同模型时返回一个 disabled Router，系统回退到固定 provider 行为。
func (c *Client) BuildRouter() *Router {
	baselines := resolveBaselines(c.Config.Routing)

	if !c.Config.Routing.IsAuto() {
		slog.Info("Model auto-routing disabled (fixed mode)")
		return c.setRouter(disabledRouter("fixed", "routing mode set to fixed", baselines))
	}

	if c.Config.Global.LLM == nil || len(c.Config.Global.LLM.Providers) == 0 {
		return c.setRouter(disabledRouter("auto", "no providers configured", baselines))
	}

	// 候选模型名 → provider 名（同名模型取任一 provider）。
	candidates := make([]string, 0, len(c.Config.Global.LLM.Providers))
	modelToProvider := make(map[string]string, len(c.Config.Global.LLM.Providers))
	for name, p := range c.Config.Global.LLM.Providers {
		m := strings.TrimSpace(p.Model)
		if m == "" {
			continue
		}
		candidates = append(candidates, m)
		if _, dup := modelToProvider[m]; !dup {
			modelToProvider[m] = name
		}
	}

	picks, ok := models.ClassifyTiers(candidates)
	if !ok {
		slog.Info("Model auto-routing disabled", "reason", picks.Reason)
		return c.setRouter(disabledRouter("auto", picks.Reason, baselines))
	}

	r := &Router{
		enabled:   true,
		mode:      "auto",
		baselines: baselines,
	}
	assign := func(t Tier, model string) bool {
		provName, found := modelToProvider[model]
		if !found {
			return false
		}
		prov, err := c.Config.GetProvider(provName)
		if err != nil {
			return false
		}
		eng := c.getOrCreateEngine(prov, provName)
		r.engines[t] = eng
		r.provider[t] = provName
		r.model[t] = model
		r.score[t] = picks.Scores[model]
		return true
	}
	if !assign(TierHigh, picks.High) || !assign(TierBalanced, picks.Mid) || !assign(TierFast, picks.Low) {
		slog.Warn("Model auto-routing disabled: tier engine build failed")
		return c.setRouter(disabledRouter("auto", "failed to build engine for a selected tier provider", baselines))
	}

	slog.Info("Model auto-routing enabled",
		"high", r.model[TierHigh], "high_score", r.score[TierHigh],
		"balanced", r.model[TierBalanced], "balanced_score", r.score[TierBalanced],
		"fast", r.model[TierFast], "fast_score", r.score[TierFast],
		"intersection", picks.Intersection)
	return c.setRouter(r)
}

// Classify 用快速档位引擎对单个任务做难度分类，返回应使用的档位。
// 任何错误（超时 / 解析失败 / 未启用）都回退到 fallback，绝不阻塞任务。
func (r *Router) Classify(ctx context.Context, task string, fallback Tier) Tier {
	if !r.Enabled() {
		return fallback
	}
	eng := r.engines[TierFast]
	if eng == nil {
		return fallback
	}

	const maxTaskChars = 2000
	if len(task) > maxTaskChars {
		task = task[:maxTaskChars]
	}

	const system = `You are a model-routing classifier. Decide the difficulty tier for an agent sub-task.
Tiers:
- "fast": trivial execution / ops work — run a command, check logs, simple lookup, formatting, mechanical edits.
- "balanced": ordinary coding, code analysis, retrieval, or Q&A.
- "high": hard reasoning, architecture/design, producing a plan or solution, subtle debugging.
Reply with ONLY a JSON object: {"tier":"fast|balanced|high"}`

	msgs := []Message{
		{Role: RoleSystem, Content: system},
		{Role: RoleUser, Content: "Task:\n" + task},
	}

	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	resp, err := eng.GenerateContent(cctx, msgs, nil, &CallOptions{MaxTokens: 32})
	if err != nil || resp == nil || len(resp.Choices) == 0 {
		slog.Warn("routing classify failed, using baseline", "error", err, "fallback", fallback.String())
		return fallback
	}
	tier := parseTierJSON(resp.Choices[0].Content, fallback)
	slog.Info("routing classify", "tier", tier.String(), "fallback", fallback.String())
	return tier
}

// parseTierJSON 从模型输出中稳健地抽取 {"tier": "..."}，失败回退 fallback。
func parseTierJSON(content string, fallback Tier) Tier {
	start := strings.IndexByte(content, '{')
	end := strings.LastIndexByte(content, '}')
	if start < 0 || end <= start {
		return fallback
	}
	var out struct {
		Tier string `json:"tier"`
	}
	if err := json.Unmarshal([]byte(content[start:end+1]), &out); err != nil {
		return fallback
	}
	return ParseTier(out.Tier, fallback)
}

// RoutingEngine 是绑定某个 agent 的 Engine 包装：GenerateContent 按 ctx 中的档位
// （无覆盖时用该 agent 的角色基线）分派到对应档位的真实引擎。它持有 Client 而非
// 固定的 Router 指针，因此运行时切换 auto/fixed 模式后立即生效；固定模式或路由
// 不可用时回退到该 agent 的 provider 级引擎（等价于关闭路由前的行为）。
//
// RunAgentLoop 会在循环开始时通过 ResolveTierEngine 把它换成具体档位引擎，从而让
// Model()/压缩阈值/model_info 事件都反映实际路由到的模型。
type RoutingEngine struct {
	client *Client
	agent  string
}

// NewRoutingEngine 为指定 agent 创建一个路由引擎。
func NewRoutingEngine(c *Client, agent string) *RoutingEngine {
	return &RoutingEngine{client: c, agent: agent}
}

func (e *RoutingEngine) GenerateContent(ctx context.Context, messages []Message, tools []ToolDef, opts *CallOptions) (*Response, error) {
	return e.resolve(ctx).GenerateContent(ctx, messages, tools, opts)
}

func (e *RoutingEngine) Model() string { return e.resolve(context.Background()).Model() }

func (e *RoutingEngine) CloseIdleConnections() {
	if e.client == nil {
		return
	}
	e.client.CloseIdleConnections()
}

// resolve 返回本轮应使用的具体引擎：TUI 手动选过的 provider 优先（显式意图），
// 其次路由启用时取 ctx 档位（或角色基线），否则回退到该 agent 的 provider 级引擎。
func (e *RoutingEngine) resolve(ctx context.Context) Engine {
	if e.client == nil {
		return nil
	}
	if eng := e.client.overrideEngine(e.agent); eng != nil {
		return eng
	}
	if r := e.client.GetRouter(); r.Enabled() {
		tier := r.BaselineOf(e.agent)
		if t, ok := TierFrom(ctx); ok {
			tier = t
		}
		if eng := r.EngineFor(tier); eng != nil {
			return eng
		}
	}
	return e.client.GetAgentEngine(e.agent)
}

// ResolveTierEngine 在 RunAgentLoop 起始处把 RoutingEngine 换成当前档位的具体引擎。
// 非路由引擎（固定模式）原样返回。
func ResolveTierEngine(ctx context.Context, e Engine) Engine {
	re, ok := e.(*RoutingEngine)
	if !ok {
		return e
	}
	return re.resolve(ctx)
}
