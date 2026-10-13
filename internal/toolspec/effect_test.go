package toolspec

import "testing"

// 能力位必须互不重叠：`1 << iota + 6` 会被解析成 `(1<<iota)+6`，
// 那样 CogReflect 就等于 7 = FSRead|SearchRegex，Match() 会把工具发给错误的 Agent。
func TestEffectBitsAreDistinctPowersOfTwo(t *testing.T) {
	all := append([]Effect(nil),
		EffectFSRead, EffectSearchRegex, EffectCodeSemantic, EffectCodeGraph, EffectContext, EffectToolLoad,
		EffectCogReflect, EffectCogSubLLM, EffectCogDeep, EffectCogDesign,
		EffectFSWrite, EffectShell, EffectBrowserObserve, EffectBrowserAct, EffectBrowserEval,
		EffectBrowserArtifact, EffectKnowledgeWrite, EffectKnowledgePrune, EffectCheckpoint,
		EffectFlow, EffectDelegate,
	)

	seen := map[Effect]string{}
	for _, e := range all {
		if e == 0 {
			t.Fatal("an Effect constant is zero")
		}
		if e&(e-1) != 0 {
			t.Fatalf("Effect %d is not a single bit — bit arithmetic is wrong", e)
		}
		if prev, dup := seen[e]; dup {
			t.Fatalf("duplicate bit value %d shared by two Effects (%s)", e, prev)
		}
		seen[e] = e.Names()[0]
	}
	if len(seen) != len(all) {
		t.Errorf("got %d distinct bits, want %d", len(seen), len(all))
	}
}

func TestEffectNamesRoundTrip(t *testing.T) {
	got := (EffectFSRead | EffectShell).Names()
	if len(got) != 2 || got[0] != "fs_read" || got[1] != "shell" {
		t.Errorf("Names() = %v, want [fs_read shell]", got)
	}
	if !(EffectFSRead | EffectShell).Has(EffectShell) {
		t.Error("Has(EffectShell) = false, want true")
	}
	if (EffectFSRead | EffectShell).Has(EffectFlow) {
		t.Error("Has(EffectFlow) = true, want false")
	}
}

// TruncTier 的零值必须映射到压缩层的历史默认值 1，
// 否则没显式声明档位的老工具会被当成"最先截断"。
func TestTruncTierZeroValueMatchesLegacyDefault(t *testing.T) {
	if got := TruncNormal.ToPriority(); got != 1 {
		t.Errorf("TruncNormal → %d, want 1", got)
	}
	if got := TruncFirst.ToPriority(); got != 0 {
		t.Errorf("TruncFirst → %d, want 0", got)
	}
	if got := TruncProtected.ToPriority(); got != -1 {
		t.Errorf("TruncProtected → %d, want -1", got)
	}
}

func TestFrequencyStringRoundTrip(t *testing.T) {
	for _, want := range []Frequency{Hot, Warm, Cold} {
		got, ok := FrequencyFromString(want.String())
		if !ok || got != want {
			t.Errorf("FrequencyFromString(%q) = (%v,%v), want (%v,true)", want, got, ok, want)
		}
	}
	if _, ok := FrequencyFromString("bogus"); ok {
		t.Error("unknown tier accepted")
	}
}
