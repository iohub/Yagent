package toolspec

import (
	"testing"
)

// 能力位必须互不重叠：`1 << iota + 6` 会被解析成 `(1<<iota)+6`，
// 那样 CogReflect 就等于 7 = FSRead|SearchRegex，Match() 会把工具发给错误的 Agent。
func TestEffectBitsAreDistinctPowersOfTwo(t *testing.T) {
	all := append([]Effect(nil),
		EffectFSRead, EffectSearchRegex, EffectCodeSemantic, EffectCodeGraph, EffectContext, EffectToolLoad,
		EffectCogReflect, EffectCogSubLLM, EffectCogDeep, EffectCogDesign,
		EffectFSWrite, EffectShell, EffectBrowserObserve, EffectBrowserAct, EffectBrowserEval,
		EffectBrowserArtifact, EffectCheckpoint,
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
		names := e.Names()
		if len(names) == 0 {
			t.Errorf("Effect %d 在 effectNames 里没有名字：新增常量请同步Names()表与本测试清单", e)
			continue
		}
		seen[e] = names[0]
	}
	if len(seen) != len(all) {
		t.Errorf("got %d distinct bits, want %d", len(seen), len(all))
	}

	// 手写清单必须与 effectNames 双向相等：新增常量只补了 const 块、
	// 忘了进 Names() 的表（或忘了进本清单），都会在这里失败，而不是静默漏检。
	if len(effectNames) != len(all) {
		t.Errorf("effectNames 有 %d 项，测试清单有 %d 项 —— 新增/删除 Effect 常量请同步两处",
			len(effectNames), len(all))
	}
	listed := make(map[Effect]bool, len(all))
	for _, e := range all {
		listed[e] = true
	}
	for _, c := range effectNames {
		if !listed[c.bit] {
			t.Errorf("effectNames 里的 %s (bit %d) 不在测试清单中", c.name, c.bit)
		}
	}
	for _, e := range all {
		found := false
		for _, c := range effectNames {
			if c.bit == e {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("清单里的 Effect %d 在 effectNames 没有对应名字 —— Names() 会把它读成空", e)
		}
	}

	// 每个已注册工具用到的位都必须能在 Names() 里读出名字。
	for _, name := range Names() {
		spec, ok := Get(name)
		if !ok {
			continue
		}
		if spec.Effect == 0 {
			continue // Validate() 负责报零 Effect
		}
		if len(spec.Effect.Names()) != bitCount(spec.Effect) {
			t.Errorf("%s: Effect %d 有 %d 个置位但只有 %d 个名字",
				name, spec.Effect, bitCount(spec.Effect), len(spec.Effect.Names()))
		}
	}
}

func bitCount(e Effect) int {
	n := 0
	for e != 0 {
		e &= e - 1
		n++
	}
	return n
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
