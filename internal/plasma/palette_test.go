package plasma_test

import (
	"context"
	"strings"
	"testing"

	"yagent/internal/plasma"
	"yagent/internal/toolspec"
)

// PlasmaPalette 必须真的窄于 ProfileDirector.Palette，且不含任何写/执行/第三方能力。
// 这条是 ADR-4 的安全底线：生成式 Agent 默认只读。
func TestPlasmaPaletteIsReadOnly(t *testing.T) {
	forbidden := []struct {
		name string
		bit  toolspec.Effect
	}{
		{"fs_write", toolspec.EffectFSWrite},
		{"shell", toolspec.EffectShell},
		{"browser_observe", toolspec.EffectBrowserObserve},
		{"browser_act", toolspec.EffectBrowserAct},
		{"browser_eval", toolspec.EffectBrowserEval},
		{"browser_artifact", toolspec.EffectBrowserArtifact},
		{"checkpoint", toolspec.EffectCheckpoint},
		{"delegate", toolspec.EffectDelegate},
		{"external_read", toolspec.EffectExternalRead},
		{"external_write", toolspec.EffectExternalWrite},
		{"skill", toolspec.EffectSkill},
	}
	for _, f := range forbidden {
		if plasma.PlasmaPalette.Has(f.bit) {
			t.Errorf("PlasmaPalette 含 %s —— 生成式 Agent 默认不该有这项能力（ADR-4）", f.name)
		}
	}
	// 必须含 flow（否则 agent_exit 追加不进来，Agent 无法收尾）与 tool_load
	//（否则 Cold 工具永远取不回，那正是 ProfileDirector.Palette 今天的 bug）。
	for _, must := range []struct {
		name string
		bit  toolspec.Effect
	}{
		{"flow", toolspec.EffectFlow},
		{"tool_load", toolspec.EffectToolLoad},
		{"fs_read", toolspec.EffectFSRead},
	} {
		if !plasma.PlasmaPalette.Has(must.bit) {
			t.Errorf("PlasmaPalette 缺 %s", must.name)
		}
	}
	// 第三方能力一位都不给：toolspec/AGENTS.md 硬约束 6.1。
	if plasma.PlasmaPalette.Has(toolspec.EffectExternalRead | toolspec.EffectExternalWrite | toolspec.EffectSkill) {
		t.Error("PlasmaPalette 给了第三方能力，违反 toolspec 硬约束 6.1")
	}
}

// 未批准的升级：工具被拒（PLS-201），同时留下"需要升级"的账目（PLS-210）。
func TestEscalation_UnapprovedIsRejectedButAccounted(t *testing.T) {
	b := cleanBundle()
	b.Tools = append(b.Tools, "create_file")
	c := &plasma.Compiler{Palette: plasma.PlasmaPalette}
	r := c.Compile(context.Background(), b)

	if !r.HasErrors() {
		t.Fatal("未批准的写能力必须被拒")
	}
	if !r.EscalationRequired {
		t.Error("EscalationRequired 应为真（即使被拒也要留账目）")
	}
	var saw201, saw210 bool
	for _, d := range r.Diagnostics {
		switch d.Code {
		case plasma.CodeOutsidePalette:
			saw201 = true
		case plasma.CodeEscalation:
			saw210 = true
			if d.Severity != plasma.SeverityWarning {
				t.Errorf("PLS-210 应是 warning（它是账目不是阻断），得到 %q", d.Severity)
			}
			if !strings.Contains(d.Note, "批准") {
				t.Errorf("note 应说明需要批准，得到 %q", d.Note)
			}
		}
	}
	if !saw201 || !saw210 {
		t.Errorf("需要 PLS-201 与 PLS-210 同时在场: 201=%v 210=%v\n%s", saw201, saw210, r.Text())
	}
}

// 已批准的升级：工具放行、无 error，但账目仍然要在（坑 43：防线生效过必须有人看得见）。
func TestEscalation_ApprovedPassesButStillAccounted(t *testing.T) {
	b := cleanBundle()
	b.Tools = append(b.Tools, "create_file")
	b.Policy.Deny = nil // 夹具默认 deny fs_write，这里要测放行
	c := &plasma.Compiler{
		Palette:            plasma.PlasmaPalette,
		EscalationApproved: toolspec.EffectFSWrite,
	}
	r := c.Compile(context.Background(), b)
	if r.HasErrors() {
		t.Fatalf("已批准的升级不该报 error:\n%s", r.Text())
	}
	if !r.EscalationRequired {
		t.Error("已批准也要置 EscalationRequired（grade 上限与审计都靠它）")
	}
	var note string
	for _, d := range r.Diagnostics {
		if d.Code == plasma.CodeEscalation {
			note = d.Note
		}
	}
	if note == "" {
		t.Fatal("已批准的升级必须仍留 PLS-210 账目，否则没人知道这个 Agent 有写权限")
	}
	if !strings.Contains(note, "已获批准") {
		t.Errorf("note 应区分已批准/未批准，得到 %q", note)
	}
}

// 批准只覆盖它声明的那些位，不是"批准过一次就全放行"。
func TestEscalation_ApprovalIsPerBit(t *testing.T) {
	b := cleanBundle()
	b.Tools = append(b.Tools, "create_file", "run_bash")
	b.Policy.Deny = nil
	c := &plasma.Compiler{
		Palette:            plasma.PlasmaPalette,
		EscalationApproved: toolspec.EffectFSWrite, // 只批了写文件，没批 shell
	}
	r := c.Compile(context.Background(), b)
	if !r.HasErrors() {
		t.Fatal("只批准 fs_write 时 run_bash 仍须被拒")
	}
	var rejectedShell bool
	for _, d := range r.Diagnostics {
		if d.Code == plasma.CodeOutsidePalette && strings.Contains(d.Message, "run_bash") {
			rejectedShell = true
		}
	}
	if !rejectedShell {
		t.Errorf("run_bash 应被拒:\n%s", r.Text())
	}
}

// 只用默认 palette 内的能力时不该有任何升级账目（否则每个 Agent 都带一条噪声 warning）。
func TestEscalation_ReadOnlyBundleHasNoEscalation(t *testing.T) {
	r := (&plasma.Compiler{Palette: plasma.PlasmaPalette}).Compile(context.Background(), cleanBundle())
	if r.HasErrors() {
		t.Fatalf("只读 bundle 应通过:\n%s", r.Text())
	}
	if r.EscalationRequired {
		t.Error("只读 bundle 不该标记 EscalationRequired")
	}
	for _, d := range r.Diagnostics {
		if d.Code == plasma.CodeEscalation {
			t.Errorf("只读 bundle 不该有 PLS-210:\n%s", r.Text())
		}
	}
}

// policy.effects_required 声明了超出 base 的能力，即使没有工具真的用到它，也要留账目：
// 那是生成侧的意图声明，批准门要按它问用户。
func TestEscalation_DeclaredIntentIsAccounted(t *testing.T) {
	b := cleanBundle()
	b.Policy.EffectsRequired = []string{"fs_read", "shell"}
	r := (&plasma.Compiler{Palette: plasma.PlasmaPalette}).Compile(context.Background(), b)
	if !r.EscalationRequired {
		t.Errorf("声明了 shell 意图就该标记需要升级:\n%s", r.Text())
	}
}

func TestBeyondBase(t *testing.T) {
	got := plasma.BeyondBase(toolspec.EffectFSRead|toolspec.EffectShell, toolspec.EffectFSRead)
	if got != toolspec.EffectShell {
		t.Errorf("BeyondBase = %v, want [shell]", got.Names())
	}
	if plasma.BeyondBase(toolspec.EffectFSRead, toolspec.EffectFSRead) != 0 {
		t.Error("完全在 base 内应返回 0")
	}
}
