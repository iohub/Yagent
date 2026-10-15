package llm

import (
	"context"
	"testing"

	"yagent/internal/config"
)

// fakeEngine 是测试用的最小 Engine，记录被调用的 model 名。
type fakeEngine struct{ model string }

func (f *fakeEngine) GenerateContent(ctx context.Context, m []Message, t []ToolDef, o *CallOptions) (*Response, error) {
	return &Response{Choices: []Choice{{Content: "ok"}}}, nil
}
func (f *fakeEngine) Model() string         { return f.model }
func (f *fakeEngine) CloseIdleConnections() {}

func TestParseTier(t *testing.T) {
	cases := map[string]Tier{
		"fast": TierFast, "FAST": TierFast,
		"balanced": TierBalanced, "mid": TierBalanced,
		"high": TierHigh, "smart": TierHigh,
		"": TierBalanced, "garbage": TierBalanced,
	}
	for in, want := range cases {
		if got := ParseTier(in, TierBalanced); got != want {
			t.Errorf("ParseTier(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParseTierJSON(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    Tier
	}{
		{"plain", `{"tier":"high"}`, TierHigh},
		{"markdown-wrapped", "```json\n{\"tier\":\"fast\"}\n```", TierFast},
		{"prose-around", `I think {"tier":"balanced"} is right`, TierBalanced},
		{"invalid-json", `{not json}`, TierBalanced},
		{"no-object", `high`, TierBalanced},
		{"unknown-tier", `{"tier":"turbo"}`, TierHigh}, // fallback honored
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fb := TierBalanced
			if tt.name == "unknown-tier" {
				fb = TierHigh
			}
			if got := parseTierJSON(tt.content, fb); got != tt.want {
				t.Errorf("parseTierJSON(%q) = %v, want %v", tt.content, got, tt.want)
			}
		})
	}
}

func TestTierContext(t *testing.T) {
	ctx := context.Background()
	if _, ok := TierFrom(ctx); ok {
		t.Fatal("empty ctx should not carry a tier")
	}
	ctx = WithTier(ctx, TierHigh)
	got, ok := TierFrom(ctx)
	if !ok || got != TierHigh {
		t.Fatalf("TierFrom = %v,%v; want High,true", got, ok)
	}
}

func TestResolveTierEngine(t *testing.T) {
	fast := &fakeEngine{model: "fast-model"}
	bal := &fakeEngine{model: "bal-model"}
	high := &fakeEngine{model: "high-model"}
	r := &Router{
		enabled:   true,
		engines:   [3]Engine{fast, bal, high},
		baselines: map[string]Tier{"coding": TierBalanced},
	}
	c := &Client{engines: map[string]Engine{}}
	c.setRouter(r)
	re := NewRoutingEngine(c, "coding")

	// 非路由引擎原样返回。
	if got := ResolveTierEngine(context.Background(), fast); got != Engine(fast) {
		t.Error("ResolveTierEngine should pass through non-routing engines")
	}
	// 无 ctx 覆盖 → baseline (balanced)。
	if got := ResolveTierEngine(context.Background(), re); got.Model() != "bal-model" {
		t.Errorf("baseline resolve = %s, want bal-model", got.Model())
	}
	// ctx 覆盖 → high。
	ctx := WithTier(context.Background(), TierHigh)
	if got := ResolveTierEngine(ctx, re); got.Model() != "high-model" {
		t.Errorf("ctx override resolve = %s, want high-model", got.Model())
	}
}

// TestRoutingEngine_RuntimeOverrideBeatsTier 钉住"手动选过一次 provider 就压过档位"。
// 默认 [routing] mode 是 auto，若覆盖排在档位之后，TUI 里切模型等于没切。
func TestRoutingEngine_RuntimeOverrideBeatsTier(t *testing.T) {
	c := &Client{
		engines: map[string]Engine{},
		Config: &config.Config{Global: config.TopLevelConfig{LLM: &config.GlobalLLMConfig{
			Providers: map[string]config.ProviderConfig{
				"hand": {Model: "hand-picked", APIBaseURL: "http://127.0.0.1:1/v1", APIKey: "k"},
			},
		}}},
	}
	c.setRouter(&Router{
		enabled:   true,
		engines:   [3]Engine{&fakeEngine{model: "fast-model"}, &fakeEngine{model: "bal-model"}, &fakeEngine{model: "high-model"}},
		baselines: map[string]Tier{"coding": TierBalanced},
	})
	ctx := WithTier(context.Background(), TierHigh)

	if err := c.SetAgentProvider("coding", "hand"); err != nil {
		t.Fatalf("SetAgentProvider: %v", err)
	}
	if got := ResolveTierEngine(ctx, NewRoutingEngine(c, "coding")).Model(); got != "hand-picked" {
		t.Errorf("overridden agent resolve = %q, want the hand-picked model", got)
	}
	// HasAgentOverride 必须与 resolve 的判据同源，否则 routeCtx 要么白跑一次分类，
	// 要么发出一条描述未使用档位的 model_routed。
	if !c.HasAgentOverride("coding") {
		t.Error("HasAgentOverride(coding) = false，但 resolve 用的是它的覆盖")
	}
	// 没被手动选过的角色照旧走档位。
	if got := ResolveTierEngine(ctx, NewRoutingEngine(c, "repo")).Model(); got != "high-model" {
		t.Errorf("un-overridden agent resolve = %q, want the tier engine", got)
	}
	if c.HasAgentOverride("repo") {
		t.Error("HasAgentOverride(repo) = true，但从未为它设置过覆盖")
	}
	// 覆盖指向配置里已不存在的 provider 时回落档位，绝不能交出 nil 引擎。
	c.agentProviderOverrides["coding"] = "ghost"
	if got := ResolveTierEngine(ctx, NewRoutingEngine(c, "coding")).Model(); got != "high-model" {
		t.Errorf("ghost override resolve = %q, want fallback to the tier engine", got)
	}
}

// TestBaselineOf 钉住"查不到就回落 balanced"这条解析行为本身。
// 各角色的默认档位是可被 [routing.baselines] 覆盖的产品可调值，不属于行为契约，故不在此断言。
func TestBaselineOf(t *testing.T) {
	r := &Router{baselines: map[string]Tier{"coding": TierHigh, "repo": TierBalanced}}

	if got := r.BaselineOf("coding"); got != TierHigh {
		t.Errorf("BaselineOf(coding) = %v, want the configured High", got)
	}
	// 角色名大小写不敏感：配置里写 "Coding" 也必须命中。
	if got := r.BaselineOf("CODING"); got != TierHigh {
		t.Errorf("BaselineOf(CODING) = %v, want High (lookup is case-insensitive)", got)
	}
	// 没有基线条目的角色（自定义 Agent 的 delegate_<name>）回落 balanced，而不是零值 Tier。
	if got := r.BaselineOf("security_reviewer"); got != TierBalanced {
		t.Errorf("BaselineOf(unknown) = %v, want Balanced", got)
	}
	// nil router：调用方（RoutingEngine.resolve）不应拿到零值档位。
	var nilRouter *Router
	if got := nilRouter.BaselineOf("coding"); got != TierBalanced {
		t.Errorf("nil Router BaselineOf = %v, want Balanced", got)
	}
}

func TestClassify_DisabledFallsBack(t *testing.T) {
	r := disabledRouter("fixed", "test", defaultBaselines)
	if got := r.Classify(context.Background(), "anything", TierHigh); got != TierHigh {
		t.Errorf("disabled Classify = %v, want fallback High", got)
	}
}

func TestSnapshot_Disabled(t *testing.T) {
	r := disabledRouter("auto", "empty intersection", defaultBaselines)
	snap := r.Snapshot()
	if snap.Enabled || snap.Reason == "" {
		t.Errorf("snapshot = %+v, want disabled with reason", snap)
	}
}
