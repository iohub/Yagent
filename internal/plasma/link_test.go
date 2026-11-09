package plasma_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yagent/internal/plasma"
	"yagent/internal/toolspec"
)

func TestLink_BuildsPlan(t *testing.T) {
	c := &plasma.Compiler{Palette: testPalette}
	plan, err := c.Link(cleanBundle(), "abc123")
	if err != nil {
		t.Fatalf("Link: %v", err)
	}
	if plan.Hash != "abc123" {
		t.Errorf("Hash = %q", plan.Hash)
	}
	if plan.Agent.Name != "security_auditor" || plan.Agent.SystemPrompt == "" {
		t.Errorf("Agent view 不完整: %+v", plan.Agent)
	}
	if plan.Config.MaxSteps != 15 || !plan.Config.StopOnFinish {
		t.Errorf("Config view 漂移: %+v", plan.Config)
	}
	if plan.Profile.Agent != "security_auditor" || !plan.Profile.RequiresExit {
		t.Errorf("Profile 漂移: %+v", plan.Profile)
	}
	// 工具面必须含 agent_exit（MatchNames 自动追加）且只含声明 + 收尾。
	hasExit := false
	for _, n := range plan.Agent.Tools {
		if n == "agent_exit" {
			hasExit = true
		}
	}
	if !hasExit {
		t.Errorf("工具面缺 agent_exit: %v", plan.Agent.Tools)
	}
}

// 加载期必须对活注册表复核：bundle 落盘后注册表变了，Link 要拒。
func TestLink_RejectsBundleInvalidAgainstLiveRegistry(t *testing.T) {
	b := cleanBundle()
	b.Tools = []string{"read_filez"} // 幻觉工具
	c := &plasma.Compiler{Palette: testPalette}
	if _, err := c.Link(b, "h"); err == nil {
		t.Fatal("含幻觉工具的 bundle 不该 link 成功")
	} else if !strings.Contains(err.Error(), "PLS-101") {
		t.Errorf("错误里应带诊断码，得到: %v", err)
	}
}

// 用到了升级就必须把 grade 钉在 provisional，哪怕报告声称更高。
func TestLink_EscalationCapsGrade(t *testing.T) {
	b := cleanBundle()
	b.Tools = append(b.Tools, "create_file")
	b.Policy.Deny = nil
	c := &plasma.Compiler{Palette: plasma.PlasmaPalette | toolspec.EffectFSWrite, EscalationApproved: toolspec.EffectFSWrite}
	plan, err := c.Link(b, "h")
	if err != nil {
		t.Fatalf("Link: %v", err)
	}
	if plan.Grade != plasma.GradeProvisional {
		t.Errorf("用了升级的 grade = %q, want provisional", plan.Grade)
	}
}

func TestLoadAll_SortedBudgetAndSkipped(t *testing.T) {
	s, root := newStore(t)
	// 三个 agent：alpha / mike / zebra
	for _, n := range []string{"zebra_agent", "alpha_agent", "mike_agent"} {
		b := cleanBundle()
		b.Name = n
		b.DisplayName = n
		if _, err := s.Put(b, nil); err != nil {
			t.Fatal(err)
		}
	}
	// 把 zebra 改坏，制造一条 skipped
	zHash, _ := s.Current("zebra_agent")
	zp := filepath.Join(root, "zebra_agent", zHash, "bundle.json")
	raw, _ := os.ReadFile(zp)
	raw[0] = ' ' // 破坏 JSON 首字符（空白仍合法？用删掉一个字符更稳）
	_ = os.WriteFile(zp, raw[1:], 0o600)

	c := &plasma.Compiler{Palette: plasma.PlasmaPalette}
	loaded, skipped, omitted := plasma.LoadAll(s, c, 10)
	if omitted != 0 {
		t.Errorf("omitted = %d, want 0", omitted)
	}
	if len(skipped) != 1 || skipped[0].Name != "zebra_agent" {
		t.Errorf("skipped = %+v, want [zebra_agent]", skipped)
	}
	if len(loaded) != 2 {
		t.Fatalf("loaded = %d, want 2", len(loaded))
	}
	// 呈现顺序必须字典序
	if loaded[0].Plan.Agent.Name != "alpha_agent" || loaded[1].Plan.Agent.Name != "mike_agent" {
		t.Errorf("加载顺序不是字典序: %s, %s", loaded[0].Plan.Agent.Name, loaded[1].Plan.Agent.Name)
	}

	// 预算 1：必须截断并报到 omitted，且保留的是排序靠前的（这里 grade 相同按 name）
	loaded2, _, omitted2 := plasma.LoadAll(s, c, 1)
	if omitted2 == 0 {
		t.Error("预算 1 时应有 omitted")
	}
	if len(loaded2) != 1 {
		t.Errorf("预算 1 时 loaded = %d", len(loaded2))
	}
	if loaded2[0].Plan.Agent.Name != "alpha_agent" {
		t.Errorf("预算 1 应保留 alpha_agent，得到 %s", loaded2[0].Plan.Agent.Name)
	}
}
