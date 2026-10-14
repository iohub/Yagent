package llm

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"

	"yagent/internal/config"
)

// FallbackEngine 是"主模型挂了自动换一家"的唯一实现。它的失败模式很难看穿：
// 顺序错了会先打贵的/慢的那家；过滤漏了自引用会让同一个 provider 被连打两次；
// 全部失败时若丢掉原始错误，排障就只剩一句 "failed"。

// stubEngine 是可编排的假引擎，自带调用计数。
type stubEngine struct {
	name  string
	model string
	resp  *Response
	err   error

	mu    sync.Mutex
	calls int
}

func (e *stubEngine) Model() string { return e.model }

func (e *stubEngine) CloseIdleConnections() {}

func (e *stubEngine) GenerateContent(_ context.Context, _ []Message, _ []ToolDef, _ *CallOptions) (*Response, error) {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()
	return e.resp, e.err
}

func (e *stubEngine) callsMade() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

func okResp(model string) *Response {
	return &Response{Choices: []Choice{{Content: "from " + model}}}
}

func failing(model string, err error) *stubEngine {
	return &stubEngine{name: model, model: model, err: err}
}

// 顺序与过滤：权重降序、剔除不存在的 provider、剔除与主引擎同名的自引用。
// NewFallbackEngine 是这些规则唯一的成型点，配错不会有任何运行期报错。
func TestNewFallbackEngineFiltersAndOrders(t *testing.T) {
	primary := &stubEngine{name: "primary", model: "p", resp: okResp("p")}
	all := map[string]config.ProviderConfig{
		"cheap":  {Model: "cheap-model", APIBaseURL: "http://127.0.0.1:1", APIKey: "k"},
		"strong": {Model: "strong-model", APIBaseURL: "http://127.0.0.1:1", APIKey: "k"},
		"main":   {Model: "p", APIBaseURL: "http://127.0.0.1:1", APIKey: "k"}, // 与主引擎同名
	}
	cfgs := []config.FallbackProvider{
		{Provider: "cheap", Weight: 1},
		{Provider: "ghost", Weight: 9}, // 配置里没有这家
		{Provider: "main", Weight: 8},  // 自引用
		{Provider: "strong", Weight: 5},
	}

	fe := NewFallbackEngine(primary, "main", cfgs, all, config.LLMConfig{})

	var order []string
	for _, fb := range fe.fallbacks {
		order = append(order, fb.name)
	}
	if got := strings.Join(order, ","); got != "strong,cheap" {
		t.Errorf("fallback 顺序 = %v, want [strong cheap]（权重降序，未知与自引用被剔除）", got)
	}
	if fe.Model() != "p" {
		t.Errorf("Model() = %q, want 报主引擎的模型名（TUI 用它显示当前模型）", fe.Model())
	}

	// 每家 fallback 引擎都按 FallbackMaxRetries 单独配置，不能沿用主引擎的 5 次。
	// 主引擎挂掉后再让每家重试 5 次，一次调用能拖到几分钟。
	if len(fe.fallbacks) != 2 {
		t.Fatalf("fallbacks = %d 家, want 2", len(fe.fallbacks))
	}
}

// 全部候选都被过滤掉时，工具面里就没有 fallback：主引擎一挂必须给出
// "没配可用 fallback" 的错误，而不是回落到主引擎再打一遍。
func TestFallbackEngineNoUsableFallbacks(t *testing.T) {
	all := map[string]config.ProviderConfig{"main": {Model: "p"}}
	fe := NewFallbackEngine(failing("primary", errors.New("主引擎 503")), "main",
		[]config.FallbackProvider{{Provider: "ghost"}}, all, config.LLMConfig{})
	if len(fe.fallbacks) != 0 {
		t.Fatalf("fallbacks = %v, want 空", fe.fallbacks)
	}

	_, err := fe.GenerateContent(context.Background(), nil, nil, nil)
	if err == nil {
		t.Fatal("主引擎失败且无可用 fallback 却返回成功")
	}
	if !strings.Contains(err.Error(), "no fallback providers configured") {
		t.Errorf("err = %v, want 点明没有可用 fallback", err)
	}
	if !strings.Contains(err.Error(), "主引擎 503") {
		t.Errorf("err = %v, want 保留主引擎的原始错误", err)
	}
	if n := fe.primary.(*stubEngine).callsMade(); n != 1 {
		t.Errorf("主引擎被调用 %d 次, want 1（不重复打）", n)
	}
}

func TestFallbackEngine_GenerateContent(t *testing.T) {
	build := func() (*FallbackEngine, *stubEngine, *stubEngine, *stubEngine) {
		primary := failing("primary", errors.New("主引擎 503"))
		first := failing("first", errors.New("第一家也挂了"))
		second := &stubEngine{name: "second", model: "s", resp: okResp("s")}
		fe := &FallbackEngine{
			primary: primary,
			name:    "main",
			fallbacks: []fallbackEntry{
				{name: "first", engine: first, weight: 2},
				{name: "second", engine: second, weight: 1},
			},
		}
		return fe, primary, first, second
	}

	t.Run("主引擎成功就不碰 fallback", func(t *testing.T) {
		fe, primary, first, _ := build()
		primary.resp, primary.err = okResp("p"), nil

		resp, err := fe.GenerateContent(context.Background(), nil, nil, nil)
		if err != nil {
			t.Fatalf("GenerateContent: %v", err)
		}
		if resp.Choices[0].Content != "from p" {
			t.Errorf("content = %q, want 来自 p", resp.Choices[0].Content)
		}
		if n := first.callsMade(); n != 0 {
			t.Errorf("fallback 被调用 %d 次, want 0", n)
		}
	})

	t.Run("按声明顺序回落到成功的那家", func(t *testing.T) {
		fe, _, first, second := build()

		resp, err := fe.GenerateContent(context.Background(), nil, nil, nil)
		if err != nil {
			t.Fatalf("GenerateContent: %v", err)
		}
		if resp.Choices[0].Content != "from s" {
			t.Errorf("content = %q, want 落到 second", resp.Choices[0].Content)
		}
		if n := first.callsMade(); n != 1 {
			t.Errorf("第一家被调用 %d 次, want 1（每家只试一次）", n)
		}
		if n := second.callsMade(); n != 1 {
			t.Errorf("second 被调用 %d 次, want 1", n)
		}
	})

	t.Run("全部失败时两边的错误都要留下", func(t *testing.T) {
		fe, _, _, second := build()
		second.resp, second.err = nil, errors.New("第二家 429")

		_, err := fe.GenerateContent(context.Background(), nil, nil, nil)
		if err == nil {
			t.Fatal("全部失败却返回成功")
		}
		msg := err.Error()
		// 排障要同时看到主引擎为什么挂、以及最后一家为什么挂。
		if !strings.Contains(msg, "主引擎 503") || !strings.Contains(msg, "第二家 429") {
			t.Errorf("err = %q, want 同时包含主引擎与最后一个 fallback 的错误", msg)
		}
		if !strings.Contains(msg, "all providers failed") {
			t.Errorf("err = %q, want 以 all providers failed 描述整体结果", msg)
		}
	})

	t.Run("ctx 已取消时不再逐个打点", func(t *testing.T) {
		fe, _, first, second := build()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := fe.GenerateContent(ctx, nil, nil, nil)
		if err == nil {
			t.Fatal("已取消的 ctx 却返回成功")
		}
		if !strings.Contains(err.Error(), "fallback aborted") {
			t.Errorf("err = %v, want 说明被取消打断", err)
		}
		for _, fb := range []*stubEngine{first, second} {
			if n := fb.callsMade(); n != 0 {
				t.Errorf("fallback %s 被调用 %d 次, want 0（取消后不该继续打点）", fb.name, n)
			}
		}
	})

	t.Run("消息与工具要原样透传给被选中的那家", func(t *testing.T) {
		var gotMessages []Message
		var gotTools []ToolDef
		var gotOpts *CallOptions
		probe := &captureEngine{
			resp: okResp("cap"),
			sink: func(ms []Message, ts []ToolDef, o *CallOptions) {
				gotMessages, gotTools, gotOpts = ms, ts, o
			},
		}
		fe := &FallbackEngine{primary: failing("primary", errors.New("挂")), name: "main",
			fallbacks: []fallbackEntry{{name: "cap", engine: probe, weight: 1}}}

		tools := []ToolDef{{Type: "function", Function: FunctionDef{Name: "read_file"}}}
		msgs := []Message{{Role: RoleUser, Content: "读文件"}}
		if _, err := fe.GenerateContent(context.Background(), msgs, tools, &CallOptions{MaxTokens: 99}); err != nil {
			t.Fatalf("GenerateContent: %v", err)
		}
		if len(gotMessages) != 1 || gotMessages[0].Content != "读文件" {
			t.Errorf("透传的消息 = %+v, want 原样一条", gotMessages)
		}
		if len(gotTools) != 1 || gotTools[0].Function.Name != "read_file" {
			t.Errorf("透传的工具 = %+v, want 原样一条", gotTools)
		}
		if gotOpts == nil || gotOpts.MaxTokens != 99 {
			t.Errorf("透传的 CallOptions = %+v, want MaxTokens=99", gotOpts)
		}
	})

	t.Run("CloseIdleConnections 覆盖主与全部 fallback", func(t *testing.T) {
		rec := &closeRecorder{}
		fe, _, _, _ := build()
		fe.primary = &trackingEngine{name: "primary", rec: rec}
		for i := range fe.fallbacks {
			fe.fallbacks[i].engine = &trackingEngine{name: fe.fallbacks[i].name, rec: rec}
		}
		fe.CloseIdleConnections()

		got := rec.snapshot()
		sort.Strings(got)
		if strings.Join(got, ",") != "first,primary,second" {
			t.Errorf("关闭了 %v, want first,primary,second（漏一家就等于漏一次连接释放）", got)
		}
	})
}

// captureEngine 记录收到的入参，用来验证透传不被吃掉。
type captureEngine struct {
	resp *Response
	sink func([]Message, []ToolDef, *CallOptions)
}

func (e *captureEngine) Model() string         { return "captured" }
func (e *captureEngine) CloseIdleConnections() {}
func (e *captureEngine) GenerateContent(_ context.Context, ms []Message, ts []ToolDef, o *CallOptions) (*Response, error) {
	e.sink(ms, ts, o)
	return e.resp, nil
}

type closeRecorder struct {
	mu     sync.Mutex
	closed []string
}

func (r *closeRecorder) record(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = append(r.closed, name)
}

func (r *closeRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.closed...)
}

type trackingEngine struct {
	name string
	rec  *closeRecorder
}

func (e *trackingEngine) Model() string         { return e.name }
func (e *trackingEngine) CloseIdleConnections() { e.rec.record(e.name) }
func (e *trackingEngine) GenerateContent(context.Context, []Message, []ToolDef, *CallOptions) (*Response, error) {
	return nil, errors.New("不该被调用")
}

// NewEngine 按 api_format 分派 —— 这条路径也被 fallback 复用，
// 认不出协议时"配了 anthropic 却发 OpenAI 形状"会表现为每次调用 400。
func TestNewEngineDispatch(t *testing.T) {
	cases := []struct {
		apiFormat string
		want      string
	}{
		{"anthropic", "*llm.AnthropicEngine"},
		{"Anthropic", "*llm.AnthropicEngine"},
		{"", "*llm.OpenAIEngine"},
		{"openai", "*llm.OpenAIEngine"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run("api_format="+tc.apiFormat, func(t *testing.T) {
			e := NewEngine(&config.ProviderConfig{Model: "m", ApiFormat: tc.apiFormat}, config.LLMConfig{})
			if e == nil {
				t.Fatal("NewEngine = nil")
			}
			if got := typeName(e); got != tc.want {
				t.Errorf("引擎类型 = %s, want %s", got, tc.want)
			}
		})
	}
}

func typeName(e Engine) string {
	switch e.(type) {
	case *AnthropicEngine:
		return "*llm.AnthropicEngine"
	case *OpenAIEngine:
		return "*llm.OpenAIEngine"
	default:
		return "*unknown"
	}
}
