package blackboard

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseHandoffTier1Compliant(t *testing.T) {
	args := []byte(`{"reason":"ignored","handoff":{"conclusion":"配置写回走 config/persist.go 的原子替换","facts":[{"claim":"SaveConfig 在 persist.go:41","where":"internal/config/persist.go:41"},{"claim":"没有专门的 setter API","where":"internal/config/config.go"}],"open_questions":["热加载要不要同步"],"status":"ok"}}`)

	h := ParseHandoff(args, "fallback reason", "fallback text")
	if h.Degraded != "" {
		t.Errorf("Degraded = %q, want empty for compliant handoff", h.Degraded)
	}
	if !strings.HasPrefix(h.Conclusion, "配置写回") {
		t.Errorf("Conclusion = %q, want the LLM-provided one", h.Conclusion)
	}
	if len(h.Facts) != 2 || h.Facts[0].Where == "" {
		t.Errorf("Facts = %+v, want 2 facts with where preserved", h.Facts)
	}
	if len(h.OpenQuestions) != 1 || h.Status != "ok" {
		t.Errorf("OpenQuestions = %v status = %q", h.OpenQuestions, h.Status)
	}
}

func TestParseHandoffTier2MissingConclusion(t *testing.T) {
	args := []byte(`{"handoff":{"facts":[{"claim":"only a fact","where":"a.go:1"}],"status":"partial"}}`)

	h := ParseHandoff(args, "退出理由是 reason 字段", "irrelevant")
	if h.Degraded != DegradedMissingFields {
		t.Errorf("Degraded = %q, want %q", h.Degraded, DegradedMissingFields)
	}
	if h.Conclusion != "退出理由是 reason 字段" {
		t.Errorf("Conclusion = %q, want it filled from reason", h.Conclusion)
	}
	if len(h.Facts) != 1 || h.Status != "partial" {
		t.Errorf("other provided fields must survive: %+v", h)
	}
}

func TestParseHandoffTier3Absent(t *testing.T) {
	cases := map[string][]byte{
		"no handoff key":   []byte(`{"reason":"done"}`),
		"invalid json":     []byte(`{"handoff":`),
		"handoff a string": []byte(`{"handoff":"结论写在正文里了"}`),
		"handoff an array": []byte(`{"handoff":[1,2]}`),
		"empty args":       []byte(``),
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			h := ParseHandoff(args, "reason 兜底", "正文尾部才是结论")
			if h.Degraded != DegradedAbsent {
				t.Errorf("Degraded = %q, want %q", h.Degraded, DegradedAbsent)
			}
			if h.Conclusion != "reason 兜底" {
				t.Errorf("Conclusion = %q, want reason", h.Conclusion)
			}
			if len(h.Facts) != 0 {
				t.Errorf("tier 3 must not carry facts, got %+v", h.Facts)
			}
		})
	}
}

func TestParseHandoffTier3FallsBackToTextTail(t *testing.T) {
	// reason 也空：取交付文本**尾部**（前缀裁剪会把结论丢掉，这是本方案存在的原因）。
	long := strings.Repeat("客套话", 100) + "真正的结论在最后"
	h := ParseHandoff([]byte(`{"reason":""}`), "", long)
	if h.Degraded != DegradedAbsent {
		t.Errorf("Degraded = %q, want %q", h.Degraded, DegradedAbsent)
	}
	if !strings.HasSuffix(h.Conclusion, "真正的结论在最后") {
		t.Errorf("Conclusion = %q, want the tail of the deliverable", h.Conclusion)
	}
}

func TestNormalizeTrimsAndCaps(t *testing.T) {
	h := Handoff{
		Conclusion: "  " + strings.Repeat("结", MaxConclusionRunes+50) + "  ",
		Facts: []Fact{
			{Claim: strings.Repeat("事", MaxFactClaimRunes+20), Where: strings.Repeat("w", MaxFactWhereRunes+20)},
			{Claim: ""},
			{Claim: "   "},
		},
		OpenQuestions: []string{"", "  ", "q1", "q1", "q2", "q3", "q4", "q5", "q6", "q7"},
		Artifacts:     []string{"", " a1 ", "a1", "a2"},
	}
	n := h.Normalize()

	if runeLen(n.Conclusion) != MaxConclusionRunes {
		t.Errorf("Conclusion len = %d, want %d", runeLen(n.Conclusion), MaxConclusionRunes)
	}
	if !strings.HasSuffix(n.Conclusion, "…") {
		t.Errorf("Conclusion must be ellipsis-marked: %q", n.Conclusion)
	}
	if len(n.Facts) != 1 || runeLen(n.Facts[0].Claim) != MaxFactClaimRunes {
		t.Errorf("Facts = %+v, want 1 truncated fact (blanks dropped)", n.Facts)
	}
	if runeLen(n.Facts[0].Where) != MaxFactWhereRunes {
		t.Errorf("Where len = %d, want %d", runeLen(n.Facts[0].Where), MaxFactWhereRunes)
	}
	if len(n.OpenQuestions) != MaxOpenQuestions {
		t.Errorf("OpenQuestions = %v, want cap %d", n.OpenQuestions, MaxOpenQuestions)
	}
	if len(n.Artifacts) != 2 || n.Artifacts[0] != "a1" {
		t.Errorf("Artifacts = %v, want [a1 a2] trimmed and deduped", n.Artifacts)
	}
}

func TestNormalizeCapsFactCount(t *testing.T) {
	var facts []Fact
	for i := 0; i < MaxFacts+5; i++ {
		facts = append(facts, Fact{Claim: "fact " + string(rune('a'+i))})
	}
	n := Handoff{Conclusion: "c", Facts: facts}.Normalize()
	if len(n.Facts) != MaxFacts {
		t.Errorf("Facts = %d, want cap %d", len(n.Facts), MaxFacts)
	}
}

func TestRenderStructure(t *testing.T) {
	h := Handoff{
		Conclusion:    "TOML 库是 BurntSushi v1.5.0",
		Facts:         []Fact{{Claim: "解码走 LoadFile", Where: "internal/config/config.go:120"}, {Claim: "写回是原子替换"}},
		OpenQuestions: []string{"热加载是否同步"},
		Status:        "ok",
	}
	out := h.Render(0)
	for _, want := range []string{
		"结论: TOML 库是 BurntSushi v1.5.0\n",
		"事实:\n",
		"- 解码走 LoadFile  @ internal/config/config.go:120\n",
		"- 写回是原子替换\n",
		"待确认: 热加载是否同步\n",
		"状态: ok\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Render missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, DegradedAbsent) {
		t.Errorf("Degraded marker must not leak to LLM: %s", out)
	}
}

func TestRenderDropsFactsNotConclusion(t *testing.T) {
	// 预算只够结论 + 一条事实：丢的是 fact，且绝不截 conclusion。
	h := Handoff{
		Conclusion: "结论必须完整存活",
		Facts: []Fact{
			{Claim: "第一条事实"},
			{Claim: "第二条事实"},
			{Claim: "第三条事实"},
		},
	}
	out := h.Render(40)
	if !strings.Contains(out, "结论必须完整存活") {
		t.Errorf("Conclusion got truncated by budget:\n%s", out)
	}
	if strings.Contains(out, "第三条事实") {
		t.Errorf("over-budget fact leaked:\n%s", out)
	}
	if !strings.Contains(out, "因预算省略") {
		t.Errorf("omitted footer missing:\n%s", out)
	}
	if runeLen(out) > 60 {
		t.Errorf("Render badly over budget (%d runes):\n%s", runeLen(out), out)
	}
}

func TestRenderEmptyHandoff(t *testing.T) {
	for _, h := range []Handoff{{}, {Degraded: DegradedAbsent}} {
		if got := h.Render(0); got != "" {
			t.Errorf("Render(%+v) = %q, want empty", h, got)
		}
	}
}

func TestConclusionForIndex(t *testing.T) {
	h := Handoff{Conclusion: "多行\n结论要压成一行"}
	got := h.ConclusionForIndex("")
	if strings.Contains(got, "\n") {
		t.Errorf("index conclusion contains newline: %q", got)
	}
	if !strings.Contains(got, "结论要压成一行") {
		t.Errorf("conclusion lost: %q", got)
	}
	// v1 存量便签没有 handoff：回落到 Summary。
	if fb := (Handoff{}).ConclusionForIndex("v1 的 summary"); fb != "v1 的 summary" {
		t.Errorf("fallback = %q, want the v1 summary", fb)
	}
	// 索引行必须是单行且不超过 IndexConclusionRunes。
	long := Handoff{Conclusion: strings.Repeat("长", IndexConclusionRunes+40)}
	if n := runeLen(long.ConclusionForIndex("")); n > IndexConclusionRunes {
		t.Errorf("index conclusion len = %d, want <= %d", n, IndexConclusionRunes)
	}
}

// 档 1 与档 2 的区别只在 conclusion 来源；两者都不该被标成 absent。
func TestParseHandoffHandoffPresentIsNeverAbsent(t *testing.T) {
	args := []byte(`{"handoff":{"conclusion":"有结论"}}`)
	if h := ParseHandoff(args, "r", "t"); h.Degraded != "" {
		t.Errorf("tier 1 Degraded = %q, want empty", h.Degraded)
	}

	args2 := []byte(`{"handoff":{"conclusion":""}}`)
	if h := ParseHandoff(args2, "r", "t"); h.Degraded != DegradedMissingFields {
		t.Errorf("tier 2 Degraded = %q, want %q", h.Degraded, DegradedMissingFields)
	}
}

// handoff 存在但没自报状态时，按"已明确退出"推断 ok —— header 与索引行留空会被读成失败。
func TestParseHandoffStatusInferredFromReason(t *testing.T) {
	args := []byte(`{"handoff":{"conclusion":"做完了"}}`)
	if h := ParseHandoff(args, "任务完成", ""); h.Status != StatusOK {
		t.Errorf("Status = %q, want %q", h.Status, StatusOK)
	}
	// reason 也拿不到时不推断（未知就留未知）。
	if h := ParseHandoff(args, "", ""); h.Status != "" {
		t.Errorf("Status = %q, want empty when nothing reports it", h.Status)
	}
	// LLM 自报 partial 必须保留。
	partial := []byte(`{"handoff":{"conclusion":"部分完成","status":"partial"}}`)
	if h := ParseHandoff(partial, "任务完成", ""); h.Status != "partial" {
		t.Errorf("Status = %q, want the LLM-reported partial", h.Status)
	}
}

// 只有 conclusion、什么别的内容都没有时，Render 仍必须产出非空块（Director 摘要的唯一来源）。
func TestRenderConclusionOnlyStillRenders(t *testing.T) {
	h := ParseHandoff([]byte(`{"reason":"done"}`), "只有一句结论", "")
	if h.Empty() {
		t.Error("a handoff carrying a conclusion must not be Empty()")
	}
	out := h.Render(0)
	if !strings.Contains(out, "只有一句结论") {
		t.Errorf("Render dropped the sole conclusion:\n%s", out)
	}
}

// 省略标记自身必须便宜：它计入预算，否则 facts 会为了迁就一句提示语而被多丢一条。
func TestHandoffRenderOmittedFooterFitsBudget(t *testing.T) {
	footer := renderOmittedFooter(7)
	if n := runeLen(footer); n == 0 || n >= 60 {
		t.Errorf("runeLen(footer) = %d, want a short (0,60) marker: %q", n, footer)
	}

	// 预算刚好只够一条事实 + footer：拿到的必须是第一条，且必须被告知丢了几个。
	h := Handoff{
		Conclusion: "结论",
		Facts: []Fact{
			{Claim: "第一条事实"},
			{Claim: "第二条事实"},
			{Claim: "第三条事实"},
		},
	}
	out := h.Render(30)
	if strings.Contains(out, "第二条事实") || strings.Contains(out, "第三条事实") {
		t.Errorf("over-budget facts leaked into:\n%s", out)
	}
	if !strings.Contains(out, "因预算省略") {
		t.Errorf("footer missing:\n%s", out)
	}
}

// Handoff 必须能原样往返 front-matter —— 它是便签的 canonical 内容。
func TestHandoffJSONRoundTrip(t *testing.T) {
	original := Handoff{
		Conclusion:    "c",
		Facts:         []Fact{{Claim: "f1", Where: "w1"}, {Claim: "f2"}},
		Artifacts:     []string{"20261013-000000-aaaaaaaa"},
		OpenQuestions: []string{"q"},
		Status:        "partial",
		Degraded:      DegradedMissingFields,
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Handoff
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal %s: %v", data, err)
	}
	if back.Facts[1].Where != "" || len(back.Facts) != 2 || back.Status != "partial" ||
		back.Degraded != DegradedMissingFields || back.Artifacts[0] != "20261013-000000-aaaaaaaa" {
		t.Errorf("round trip lost data: %+v (json=%s)", back, data)
	}
}
