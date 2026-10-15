package compression

import (
	"context"
	"errors"
	"strings"
	"testing"

	"yagent/internal/llm"
	"yagent/internal/tokenutil"
)

// Prefix 的契约：provider 报过的段不再重算，没报过的段照常算，
// 计量不可用时逐字回落到本地全量估算。

// hugePrefixMessages 造一个"前缀不算小、增量很小"的列表：
// 前 12 条是已计量段（内容被换成什么都不该影响结果），后面才是新增的两条。
// 尺寸刻意保持小 —— 这条测试证的是"没重读前缀"这个性质，不是它的耗时。
func hugePrefixMessages() []llm.Message {
	msgs := make([]llm.Message, 0, 14)
	body := strings.Repeat("x", 120)
	for i := 0; i < 12; i++ {
		msgs = append(msgs, llm.Message{Role: llm.RoleTool, ToolName: "read_file", Content: body, ToolCallID: "id"})
	}
	msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, Content: "new"})
	msgs = append(msgs, llm.Message{Role: llm.RoleTool, ToolName: "run_bash", Content: "fresh output", ToolCallID: "id2"})
	return msgs
}

// TestPrefixTotalUsesMeasurement 有计量时：结果 = 计量 + 未计量部分的本地估算，
// 且**完全不读**已计量的那一段 —— 这是"每步不再全量扫一遍 tiktoken"的全部依据。
func TestPrefixTotalUsesMeasurement(t *testing.T) {
	msgs := hugePrefixMessages()
	sent := 2 // 前 2 条已被 provider 数过
	base := int64(500000)

	got := Prefix{Tokens: base, Len: sent}.Total(msgs)
	want := int(base) + EstimateMessagesTokens(msgs[sent:])
	if got != want {
		t.Errorf("Total = %d, want 计量 %d + 未计量部分 %d = %d",
			got, base, EstimateMessagesTokens(msgs[sent:]), want)
	}

	// 把已计量的前缀换成完全另一个规模，结果必须不变（说明那段没被再算）。
	shuffled := hugePrefixMessages()
	shuffled[0].Content = strings.Repeat("y", 200)
	shuffled[1].Content = strings.Repeat("z", 60000)
	if got2 := (Prefix{Tokens: base, Len: sent}).Total(shuffled); got2 != got {
		t.Errorf("改动已计量段的内容后 Total 从 %d 变 %d, want 不变（那段还在被本地重算）", got, got2)
	}
}

// TestPrefixTotalFallsBack 计量不可用的三种情形都必须回落到全量本地估算 ——
// 回落写反的表现是"有计量时反而算了全量"（白付 CPU）或"没计量时把 0 当基数"
// （阈值永远不超，压缩彻底失效）。
func TestPrefixTotalFallsBack(t *testing.T) {
	msgs := hugePrefixMessages()
	local := EstimateMessagesTokens(msgs)

	cases := []struct {
		name   string
		prefix Prefix
	}{
		{"没有计量（Tokens=0）", Prefix{}},
		{"网关报了 0（Tokens=0 但 Len 有值）", Prefix{Tokens: 0, Len: 2}},
		{"还没跑过任何一步（Len=0）", Prefix{Tokens: 5000, Len: 0}},
		{"列表被压缩重写变短了（Len 越界）", Prefix{Tokens: 5000, Len: len(msgs) + 1}},
		{"负数计量", Prefix{Tokens: -5, Len: 2}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.prefix.Total(msgs); got != local {
				t.Errorf("Total = %d, want 回落全量本地估算 %d", got, local)
			}
		})
	}

	// usable 与 Total 的回落条件必须同源（写漂的表现是判定与 stats 两个口径打架）。
	for _, tc := range cases {
		if tc.prefix.usable(msgs) {
			t.Errorf("%s：usable=true 但 Total 走的是回落分支", tc.name)
		}
	}
	if !(Prefix{Tokens: 10, Len: 1}).usable(msgs) {
		t.Error("可用计量被判成不可用")
	}
}

// TestPrefixTotalCountsReasoningAndArgs 未计量部分沿用 msgTokens 口径：
// 推理内容与工具调用参数都要算进去，否则触发时机系统性偏晚。
func TestPrefixTotalCountsReasoningAndArgs(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleSystem, Content: "counted already"},
		{Role: llm.RoleAssistant, Content: "fresh",
			Reasoning: strings.Repeat("thought ", 50),
			ToolCalls: []llm.ToolCall{{ID: "c1", Function: llm.FunctionCall{Name: "read_file", Arguments: strings.Repeat(`{"p":"a"}`, 40)}}}},
	}
	got := Prefix{Tokens: 1000, Len: 1}.Total(msgs)
	want := 1000 + tokenutil.EstimateTokens("fresh") +
		tokenutil.EstimateTokens(msgs[1].Reasoning) +
		tokenutil.EstimateTokens(msgs[1].ToolCalls[0].Function.Arguments)
	if got != want {
		t.Errorf("Total = %d, want %d（Reasoning 或 ToolCalls 参数没算进未计量部分）", got, want)
	}
}

// TestPrefixRefresh 计量生命周期的三条规则（两个循环共用，所以只能在这里测一次）。
// 写错的表现都很安静：失败时清零 → 重试的那一步退回本地估算、少算工具声明；
// 没 usage 时留着旧基数 → 换模型/换档之后基数与新列表根本不对应；
// sentLen 取晚了 → 本步新增的工具结果被当成"已计量"，触发整整晚一步。
func TestPrefixRefresh(t *testing.T) {
	usage := &llm.TokenUsage{TotalInputTokens: 4000}
	withUsage := &llm.Response{Usage: usage}
	errors := []struct {
		name string
		err  error
	}{
		{"调用失败保留旧基数", errors.New("http 500")},
		{"ctx 超时也保留旧基数", context.DeadlineExceeded},
	}
	for _, tc := range errors {
		t.Run(tc.name, func(t *testing.T) {
			old := Prefix{Tokens: 4000, Len: 3}
			if got := old.Refresh(withUsage, tc.err, 9); got != old {
				t.Errorf("Refresh = %+v, want 原样保留 %+v", got, old)
			}
		})
	}

	for _, tc := range []struct {
		name string
		resp *llm.Response
	}{
		{"网关没报 usage", &llm.Response{}},
		{"报了 0", &llm.Response{Usage: &llm.TokenUsage{TotalInputTokens: 0}}},
		{"resp 为 nil", nil},
	} {
		t.Run("作废："+tc.name, func(t *testing.T) {
			if got := (Prefix{Tokens: 4000, Len: 3}).Refresh(tc.resp, nil, 9); got != (Prefix{}) {
				t.Errorf("Refresh = %+v, want 作废（回落到本地估算）", got)
			}
		})
	}

	if got := (Prefix{Tokens: 1, Len: 1}).Refresh(withUsage, nil, 9); got != (Prefix{Tokens: 4000, Len: 9}) {
		t.Errorf("Refresh = %+v, want {4000, 9}", got)
	}
}

// TestTruncateGateUsesMeasurement 第一档的入口判定必须走计量口径：
// 判定与 stats 用两个口径是设计（stats 保持本地），但**入口只能有一个口径** ——
// 否则第一档按本地口径判断"没超"、第二档按计量口径判断"超了"，
// 表现是"一条都不截、直接烧一次 LLM 摘要"。
func TestTruncateGateUsesMeasurement(t *testing.T) {
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "system"},
		{Role: llm.RoleUser, Content: "task"},
	}
	big := strings.Repeat("x", 4000) // 未计量部分，本地约 500 token
	messages = append(messages,
		llm.Message{Role: llm.RoleAssistant, Content: "call"},
		llm.Message{Role: llm.RoleTool, ToolName: "read_file", Content: big, ToolCallID: "c1"})

	// 夹具必须"本地口径不超阈值、计量口径超阈值"，
	// 否则这条测试区分不了两个口径。
	localOnly := EstimateMessagesTokens(messages)
	threshold := localOnly + 100
	if (Prefix{}).Total(messages) > threshold {
		t.Fatalf("夹具本地口径 = %d, want ≤ 阈值 %d（本地口径都不超的话，两个口径就没差别了）", localOnly, threshold)
	}
	if (Prefix{Tokens: 5000, Len: 2}).Total(messages) <= threshold {
		t.Fatalf("计量口径 = %d, want > 阈值 %d（5000 的基数没能把判定顶过去）",
			(Prefix{Tokens: 5000, Len: 2}).Total(messages), threshold)
	}

	// 没有计量：不触发，也不返回 stats。
	if _, stats := TruncateToolResultsToBudget(messages, Prefix{}, threshold, 8); stats != nil {
		t.Errorf("无计量时不该触发第一档，却拿到 stats %+v", stats)
	}

	// 有计量：触发，并且 stats 仍是本地口径（没把 5000 的基数算进 original_tokens）。
	_, stats := TruncateToolResultsToBudget(messages, Prefix{Tokens: 5000, Len: 2}, threshold, 8)
	if stats == nil {
		t.Fatal("有计量时第一档没被触发 —— 入口判定没走计量口径")
	}
	if stats.OriginalTokens != localOnly {
		t.Errorf("stats.OriginalTokens = %d, want 本地口径 %d（provider 基数混进了面板数字）",
			stats.OriginalTokens, localOnly)
	}
	if stats.TruncatedCount == 0 {
		t.Error("触发了却一条都没截")
	}
}
