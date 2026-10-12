package thinklink

import (
	"reflect"
	"strings"
	"testing"
)

func TestExtractThoughtBlocks(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    int
	}{
		{"no block", "just prose, nothing structured", 0},
		{"empty content", "", 0},
		{"single block", "preamble\n<thought>\ngoal: g\n</thought>\ntrailing", 1},
		{"two blocks", "<thought>\ngoal: a\n</thought>\nmid\n<thought>\ngoal: b\n</thought>", 2},
		{"case insensitive tags", "<THOUGHT>\ngoal: a\n</Thought>", 1},
		{"empty inner dropped", "<thought>\n   \n</thought>", 0},
		{"unterminated ignored", "<thought>\ngoal: a", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractThoughtBlocks(tt.content)
			if len(got) != tt.want {
				t.Fatalf("ExtractThoughtBlocks() = %d blocks (%q), want %d", len(got), got, tt.want)
			}
		})
	}
}

func TestExtractThoughtBlocks_StripsTags(t *testing.T) {
	blocks := ExtractThoughtBlocks("<thought>\ngoal: g\nreason: r\n</thought>")
	if len(blocks) != 1 {
		t.Fatalf("want 1 block, got %d", len(blocks))
	}
	if blocks[0] != "goal: g\nreason: r" {
		t.Fatalf("inner content = %q, want tags stripped and trimmed", blocks[0])
	}
}

// SplitThoughtBlocks 是展示层的拆分入口：块单独渲染成面板，正文去掉块后走 markdown。
func TestSplitThoughtBlocks(t *testing.T) {
	t.Run("no block keeps content untouched", func(t *testing.T) {
		blocks, rest := SplitThoughtBlocks("just prose\nwith two lines")
		if blocks != nil {
			t.Errorf("blocks = %q, want nil", blocks)
		}
		if rest != "just prose\nwith two lines" {
			t.Errorf("rest = %q, want original content", rest)
		}
	})

	t.Run("block split out and blank gap collapsed", func(t *testing.T) {
		content := "<thought>\ngoal: g\nreason: r\n</thought>\n\n\n\n回答正文"
		blocks, rest := SplitThoughtBlocks(content)
		if len(blocks) != 1 || blocks[0] != "goal: g\nreason: r" {
			t.Fatalf("blocks = %q", blocks)
		}
		if rest != "回答正文" {
			t.Fatalf("rest = %q, want prose only", rest)
		}
	})

	t.Run("keeps prose around two blocks", func(t *testing.T) {
		content := "前\n<thought>\ngoal: a\n</thought>\n中\n<thought>\ngoal: b\n</thought>\n后"
		blocks, rest := SplitThoughtBlocks(content)
		if len(blocks) != 2 || blocks[0] != "goal: a" || blocks[1] != "goal: b" {
			t.Fatalf("blocks = %q, want both in order", blocks)
		}
		if want := "前\n\n中\n\n后"; rest != want {
			t.Fatalf("rest = %q, want %q", rest, want)
		}
	})

	t.Run("empty inner falls back to original", func(t *testing.T) {
		content := "prose <thought>\n   \n</thought> more"
		blocks, rest := SplitThoughtBlocks(content)
		if blocks != nil {
			t.Errorf("blocks = %q, want nil", blocks)
		}
		if rest != content {
			t.Errorf("rest = %q, want original content", rest)
		}
	})

	t.Run("unterminated tag stays in prose", func(t *testing.T) {
		blocks, rest := SplitThoughtBlocks("<thought>\ngoal: a")
		if blocks != nil {
			t.Errorf("blocks = %q, want nil", blocks)
		}
		if !strings.Contains(rest, "goal: a") {
			t.Errorf("rest = %q, want the partial block preserved", rest)
		}
	})
}

func TestParseThoughtPlan_Full(t *testing.T) {
	block := `<thought>
goal: Analyze request and delegate coding
step: Repo-Agent returned the auth module map
reason: Need Coding-Agent to patch the token validator
plan:
- [x] Gather context via Repo-Agent
- [>] Delegate patch to Coding-Agent
- [ ] Verify with go build
</thought>`

	tp := ParseThoughtPlan(block)
	if tp.Goal != "Analyze request and delegate coding" {
		t.Errorf("Goal = %q", tp.Goal)
	}
	if tp.Step != "Repo-Agent returned the auth module map" {
		t.Errorf("Step = %q", tp.Step)
	}
	if tp.Reason != "Need Coding-Agent to patch the token validator" {
		t.Errorf("Reason = %q", tp.Reason)
	}
	want := []PlanItem{
		{PlanDone, "Gather context via Repo-Agent"},
		{PlanCurrent, "Delegate patch to Coding-Agent"},
		{PlanPending, "Verify with go build"},
	}
	if !reflect.DeepEqual(tp.Plan, want) {
		t.Errorf("Plan = %+v, want %+v", tp.Plan, want)
	}
	if issues := tp.Validate(); issues != nil {
		t.Errorf("Validate() = %v, want nil", issues)
	}
	if tp.Empty() {
		t.Error("Empty() = true, want false")
	}
}

func TestParseThoughtPlan_AliasesAndNumbering(t *testing.T) {
	block := `Current Goal: do the thing
Current Step: did a thing
Reasoning: because
Plan Update:
* [x] 1. first
* [>] 2. second`
	tp := ParseThoughtPlan(block)
	if tp.Goal != "do the thing" || tp.Step != "did a thing" || tp.Reason != "because" {
		t.Errorf("aliases not normalized: %+v", tp)
	}
	if len(tp.Plan) != 2 || tp.Plan[0].Status != PlanDone || tp.Plan[0].Text != "first" {
		t.Errorf("numbered plan items not parsed: %+v", tp.Plan)
	}
	if tp.Plan[1].Status != PlanCurrent || tp.Plan[1].Text != "second" {
		t.Errorf("plan[1] = %+v", tp.Plan[1])
	}
}

func TestParseThoughtPlan_ContinuationLines(t *testing.T) {
	block := `goal: a long goal
  that spans two lines
reason: short`
	tp := ParseThoughtPlan(block)
	if tp.Goal != "a long goal that spans two lines" {
		t.Errorf("Goal continuation = %q", tp.Goal)
	}
	if tp.Reason != "short" {
		t.Errorf("Reason = %q", tp.Reason)
	}
}

func TestParseThoughtPlan_ValueWithColon(t *testing.T) {
	tp := ParseThoughtPlan("step: Repo-Agent said: module found")
	if tp.Step != "Repo-Agent said: module found" {
		t.Errorf("Step = %q, want value colons preserved", tp.Step)
	}
}

func TestThoughtPlan_Validate(t *testing.T) {
	tests := []struct {
		name      string
		tp        ThoughtPlan
		wantIssue bool
	}{
		{"valid", ThoughtPlan{Goal: "g", Reason: "r", Plan: []PlanItem{{PlanCurrent, "x"}}}, false},
		{"missing goal", ThoughtPlan{Reason: "r"}, true},
		{"missing reason", ThoughtPlan{Goal: "g"}, true},
		{"two current", ThoughtPlan{Goal: "g", Reason: "r", Plan: []PlanItem{{PlanCurrent, "a"}, {PlanCurrent, "b"}}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issues := tt.tp.Validate()
			if got := len(issues) > 0; got != tt.wantIssue {
				t.Fatalf("Validate() = %v, wantIssue=%v", issues, tt.wantIssue)
			}
		})
	}
}

func TestParseThoughtPlan_LegacyMarkdownIsEmpty(t *testing.T) {
	// 旧 markdown 块无结构化 key，解析后应为空，供 TUI 走原文渲染兜底。
	legacy := "## Thought & Plan\n### Thought Process\nI will read the file first."
	tp := ParseThoughtPlan(legacy)
	if !tp.Empty() {
		t.Fatalf("legacy markdown parsed as non-empty: %+v", tp)
	}
}

func TestPlanStatusMarker(t *testing.T) {
	if PlanDone.Marker() != "[x]" || PlanCurrent.Marker() != "[>]" || PlanPending.Marker() != "[ ]" {
		t.Error("unexpected markers")
	}
}
