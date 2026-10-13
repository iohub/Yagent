package agents

import (
	"strings"
	"testing"

	"yagent/internal/artifact"
	"yagent/internal/blackboard"
	"yagent/internal/llm"
)

func assistantMsg(content string) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, Content: content}
}

// R1：以 agent_exit 收尾的 Agent，Text 只是退出 JSON，交付物在最后一条 assistant 消息里。
func TestDeliverableTextRecoversAssistantContent(t *testing.T) {
	analysis := "调查结论：config 包没有写回 API，需要新增 persist 层。\n证据见 internal/config/config.go:120"
	exec := ExecutorResult{
		Text: `{"finished":true,"reason":"任务完成"}`,
		History: []llm.Message{
			{Role: llm.RoleUser, Content: "查一下"},
			assistantMsg(analysis),
			{Role: llm.RoleTool, Content: `{"finished":true,"reason":"任务完成"}`},
		},
	}

	if got := deliverableText(exec); got != analysis {
		t.Errorf("deliverableText = %q, want the assistant analysis", got)
	}
}

func TestDeliverableTextPassthrough(t *testing.T) {
	cases := map[string]ExecutorResult{
		"plain prose":            {Text: "一段普通交付文本"},
		"json without finished":  {Text: `{"result":"ok"}`},
		"finished false":         {Text: `{"finished":false,"reason":"卡住了"}`, History: []llm.Message{assistantMsg("不该被采用")}},
		"exit json, no history":  {Text: `{"finished":true,"reason":"只有 reason"}`},
		"exit json, blank msgs":  {Text: `{"finished":true,"reason":"只有 reason"}`, History: []llm.Message{assistantMsg("   \n  ")}},
		"empty":                  {Text: ""},
	}
	for name, exec := range cases {
		t.Run(name, func(t *testing.T) {
			want := exec.Text
			if got := deliverableText(exec); got != want {
				t.Errorf("deliverableText = %q, want passthrough %q", got, want)
			}
		})
	}
}

// 采集点只看退出 JSON：三档回落在 agents 侧的体现。
func TestCollectHandoffTiers(t *testing.T) {
	full := strings.Repeat("交付正文", 200)

	t.Run("tier1 compliant", func(t *testing.T) {
		exec := ExecutorResult{Text: `{"finished":true,"reason":"done","handoff":{"conclusion":"改 persist.go","facts":[{"claim":"SaveConfig 是唯一入口","where":"internal/config/persist.go:41"}]}}`}
		h := collectHandoff(exec, full)
		if h == nil {
			t.Fatal("nil handoff for an agent_exit result")
		}
		if h.Degraded != "" || h.Conclusion != "改 persist.go" || len(h.Facts) != 1 {
			t.Errorf("handoff = %+v, want the compliant parse", h)
		}
	})

	t.Run("tier2 no conclusion", func(t *testing.T) {
		exec := ExecutorResult{Text: `{"finished":true,"reason":"任务完成：三档回落","handoff":{"status":"ok"}}`}
		h := collectHandoff(exec, full)
		if h == nil || h.Degraded != blackboard.DegradedMissingFields {
			t.Fatalf("handoff = %+v, want missing_fields", h)
		}
		if h.Conclusion != "任务完成：三档回落" {
			t.Errorf("Conclusion = %q, want it filled from reason", h.Conclusion)
		}
	})

	t.Run("tier3 no handoff key", func(t *testing.T) {
		exec := ExecutorResult{Text: `{"finished":true,"reason":"任务完成"}`}
		h := collectHandoff(exec, full)
		if h == nil || h.Degraded != blackboard.DegradedAbsent {
			t.Fatalf("handoff = %+v, want absent", h)
		}
		if h.Conclusion != "任务完成" {
			t.Errorf("Conclusion = %q, want the reason", h.Conclusion)
		}
	})

	t.Run("not an agent_exit result", func(t *testing.T) {
		exec := ExecutorResult{Text: "散文交付，没有走 agent_exit"}
		if h := collectHandoff(exec, full); h != nil {
			t.Errorf("handoff = %+v, want nil (绝不给非退出路径编造结构化结论)", h)
		}
	})
}

// 超预算 + 合规 handoff：artifact 存交付全文，Director 摘要来自 handoff 而不是前缀。
func TestFinalizeResultOverBudgetWithHandoff(t *testing.T) {
	root := setArtifactRoot(t)

	analysis := strings.Repeat("分", 4000) + "\n最终结论：走 config/persist.go 的原子写"
	exec := ExecutorResult{
		Text:    `{"finished":true,"reason":"任务完成","handoff":{"conclusion":"走 config/persist.go 的原子写","facts":[{"claim":"persist.Save 是原子替换","where":"internal/config/persist.go:41"}],"status":"ok"}}`,
		History: []llm.Message{assistantMsg(analysis)},
	}

	got := FinalizeResult("coding_agent", "加配置写回", exec)

	if got.ArtifactRef == nil {
		t.Fatal("over-budget deliverable must be offloaded")
	}
	if !strings.Contains(got.Summary, "结论: 走 config/persist.go 的原子写") {
		t.Errorf("Summary should come from the handoff:\n%s", got.Summary)
	}
	if strings.Contains(got.Summary, "最终结论：走") {
		t.Errorf("Summary should not be the Reduce prefix:\n%s", got.Summary)
	}
	// R1 的核心修复：artifact 里必须是交付全文，而不是那一小段退出 JSON。
	stored, err := artifact.NewStore(root).Load("default", got.ArtifactRef.ID)
	if err != nil {
		t.Fatalf("load artifact: %v", err)
	}
	if stored.FullText != analysis {
		t.Errorf("artifact FullText length = %d, want the %d-rune deliverable", len([]rune(stored.FullText)), len([]rune(analysis)))
	}
	if stored.Summary != got.Summary {
		t.Error("artifact summary should match what the Director saw")
	}
	// Text 的既有语义不动（Full 变体才覆盖成 exec.Text）。
	if got.Text != got.Summary {
		t.Errorf("FinalizeResult Text should stay the summary, got %d vs %d runes", len([]rune(got.Text)), len([]rune(got.Summary)))
	}
	if got.Deliverable != analysis {
		t.Error("Deliverable must carry the full text for the note body")
	}
	if got.Handoff == nil || got.Handoff.Degraded != "" {
		t.Errorf("Handoff = %+v, want the parsed compliant one", got.Handoff)
	}
}

// 超预算但 handoff 降级：摘要逐字回落到 Reduce 前缀（最坏情况等于重构前）。
func TestFinalizeResultOverBudgetDegradedHandoffFallsBackToReduce(t *testing.T) {
	setArtifactRoot(t)

	analysis := strings.Repeat("析", 4000) + "\n结论在结尾"
	exec := ExecutorResult{
		Text:    `{"finished":true,"reason":"任务完成"}`,
		History: []llm.Message{assistantMsg(analysis)},
	}

	got := FinalizeResult("browser_agent", "取证据", exec)

	if got.ArtifactRef == nil {
		t.Fatal("should still offload")
	}
	if !strings.Contains(got.Summary, "[Truncated: showing first") {
		t.Errorf("Summary should be the legacy Reduce prefix, got:\n%.200s", got.Summary)
	}
	if got.Handoff == nil || got.Handoff.Degraded != blackboard.DegradedAbsent {
		t.Errorf("Handoff = %+v, want the absent-degraded parse for the Director's fallback", got.Handoff)
	}
	// 但 artifact 里已经是全文：Director 用 read_artifact 能翻到结尾那句结论。
	if got.Deliverable != analysis {
		t.Error("Deliverable must still be the full analysis")
	}
}

// 预算内：不落盘，摘要即全文（与重构前一致）。
func TestFinalizeResultWithinBudgetUnchanged(t *testing.T) {
	setArtifactRoot(t)

	text := "简短交付"
	got := FinalizeResult("chat_agent", "task", ExecutorResult{Text: text})
	if got.ArtifactRef != nil {
		t.Errorf("ArtifactRef = %+v, want nil", got.ArtifactRef)
	}
	if got.Summary != text || got.Deliverable != text {
		t.Errorf("Summary/Deliverable = %q/%q, want %q", got.Summary, got.Deliverable, text)
	}
	if got.Handoff != nil {
		t.Errorf("Handoff = %+v, want nil for a non-exit run", got.Handoff)
	}
}

// FinalizeResultFull 的 Text 语义不动（程序化消费链路依赖它）。
func TestFinalizeResultFullKeepsRawText(t *testing.T) {
	setArtifactRoot(t)

	long := strings.Repeat("满", 3000)
	exec := ExecutorResult{Text: long}
	got := FinalizeResultFull("repo_agent", "task", exec)
	if got.Text != long {
		t.Errorf("Text = %d runes, want the raw exec.Text", len([]rune(got.Text)))
	}
	if got.Deliverable != long {
		t.Error("Deliverable should equal the prose text for non-exit runs")
	}
}
