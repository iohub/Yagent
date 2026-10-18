package director

import (
	"context"
	"strings"
	"testing"
	"time"

	"yagent/internal/compression"
	"yagent/internal/llm"
	"yagent/internal/thinklink"
	"yagent/internal/tokenutil"
)

// capturePublisher 记录事件名与 payload —— planner_test.go 的 fakePlannerPublisher
// 只留事件名，而这里要看 context_compressed 里的 original_tokens 是哪个口径。
type capturePublisher struct {
	events   []string
	contents []map[string]interface{}
}

func (p *capturePublisher) Publish(event string, content interface{}, _ string) error {
	p.record(event, content)
	return nil
}

func (p *capturePublisher) PublishWithMetadata(event string, content interface{}, _ string, _ map[string]interface{}) error {
	p.record(event, content)
	return nil
}

func (p *capturePublisher) record(event string, content interface{}) {
	p.events = append(p.events, event)
	if m, ok := content.(map[string]interface{}); ok {
		p.contents = append(p.contents, m)
	}
}

func (p *capturePublisher) count(event string) int {
	n := 0
	for _, e := range p.events {
		if e == event {
			n++
		}
	}
	return n
}

// TestPlannerRun_TriggerUsesProviderMeasurement Director 侧的接线。
// 与子 Agent 那条是一对：判定原来抄在两个循环里四份，只改一处的表现是
// "两边触发时机不一致"，而两侧的各自单测都会绿。
func TestPlannerRun_TriggerUsesProviderMeasurement(t *testing.T) {
	const reported = 4000
	bigResult := strings.Repeat("observed output line that is quite long. ", 300)
	toolLocal := tokenutil.EstimateTokens(bigResult)
	threshold := reported + toolLocal/2

	eng := &fakePlannerLLM{steps: []fakeLLMStep{
		{resp: withUsage(toolCallResp("call-p1", "echo_tool", `{"input":"hello"}`), reported)},
		{resp: toolCallResp("call-p2", "delegate_sub_agent", `{"agent":"coder"}`)},
		{resp: textResp("DONE_PREFIX")},
	}}
	tools := &fakePlannerTools{state: newState(false), results: map[string]string{"echo_tool": bigResult}}
	pub := &capturePublisher{}

	p := NewPlanner(PlannerConfig{
		LLM:                eng,
		Tools:              tools,
		Publisher:          pub,
		Compressor:         compression.NewContextCompressor(nil, "director", nil),
		Prompts:            func(_ context.Context, _ PromptInput) (string, error) { return "SYSTEM_PROMPT", nil },
		MaxSteps:           4,
		LLMTimeout:         30 * time.Second,
		CompressEnable:     true,
		CompressThreshold:  threshold,
		CompressKeepTokens: 8,
	}, tools.state)

	res, err := p.Run(context.Background(), PlanInput{Input: "PREFIX_TASK"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.StopReason != "plain_text" {
		t.Errorf("StopReason = %q, want plain_text", res.StopReason)
	}

	if pub.count("context_compressed") == 0 {
		t.Fatalf("第一档没触发：本地口径 ≈ %d token、阈值 %d、计量口径 ≈ %d —— 只有后者超阈值",
			toolLocal, threshold, reported+toolLocal)
	}
	if n := pub.count("context_compressed"); n != 1 {
		t.Errorf("context_compressed 事件 = %d 次, want 1（每步都触发就是阈值压不下去）", n)
	}
	if pub.count("context_emergency_compressed") != 0 {
		t.Error("第二档被触发：第一档压完应当已经落回阈值之下")
	}

	orig := -1
	for _, c := range pub.contents {
		if v, ok := c["original_tokens"].(int); ok {
			orig = v
			break
		}
	}
	if orig < 0 {
		t.Fatal("context_compressed 的 original_tokens 不是 int —— 发布形状漂了")
	}
	if orig >= reported {
		t.Errorf("stats.OriginalTokens = %d, want 本地口径（< 计量基数 %d）", orig, reported)
	}
}

// TestPlannerRun_MeasurementInvalidatedAfterRewrite 与子 Agent 那条对称：
// 第二档重写列表之后，紧跟其后的第三档判定不能继续沿用刚作废的基数。
// 两个循环里各有一份清零，只测一边就是留一半没闸。
func TestPlannerRun_MeasurementInvalidatedAfterRewrite(t *testing.T) {
	const reported = 4000
	bigResult := strings.Repeat("observed output line that is quite long. ", 300)

	eng := &fakePlannerLLM{steps: []fakeLLMStep{
		{resp: withUsage(toolCallResp("call-r1", "echo_tool", `{"input":"hello"}`), reported)},
		{resp: toolCallResp("call-r2", "delegate_sub_agent", `{"agent":"coder"}`)},
		{resp: textResp("DONE_INVALIDATE")},
	}}
	tools := &fakePlannerTools{state: newState(false), results: map[string]string{"echo_tool": bigResult}}
	pub := &capturePublisher{}
	journal := thinklink.NewStore(0)

	p := NewPlanner(PlannerConfig{
		LLM:                    eng,
		Tools:                  tools,
		Publisher:              pub,
		Journal:                journal,
		Compressor:             compression.NewContextCompressor(nil, "director", journal),
		Prompts:                func(_ context.Context, _ PromptInput) (string, error) { return "SYSTEM_PROMPT", nil },
		MaxSteps:               4,
		LLMTimeout:             30 * time.Second,
		CompressEnable:         true,
		CompressThreshold:      2000, // 基数 4000 单独就超阈值：清零与否的差别落在第三档上
		CompressKeepTokens:     8,
		UltimateCompressEnable: true,
	}, tools.state)

	if _, err := p.Run(context.Background(), PlanInput{Input: "INVALIDATE_TASK"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if pub.count("context_emergency_compressed") == 0 {
		t.Fatalf("第二档没触发，夹具不成立（基数 %d、阈值 2000）", reported)
	}
	if n := pub.count("context_ultimate_compressed"); n != 0 {
		t.Errorf("第三档触发 %d 次, want 0 —— 刚被第二档重写的列表又贴上了作废的计量基数", n)
	}
}

// withUsage 给响应挂上 provider 报的输入规模。
func withUsage(resp *llm.Response, inputTokens int64) *llm.Response {
	resp.Usage = &llm.TokenUsage{PromptTokens: inputTokens, TotalInputTokens: inputTokens}
	return resp
}
