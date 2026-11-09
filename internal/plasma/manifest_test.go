package plasma_test

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"testing"

	"yagent/internal/plasma"
	"yagent/internal/toolspec"
)

// 本文件钉住 Manifest 的三件事：工具集合的唯一判据、CapabilityLock 的稳定性与
// 敏感性、以及码表与 corpus 的同集合关系。
//
// 为什么这些断言必须存在：manifest 是 Python 侧**唯一**的只读能力/码表来源。
// 它一旦与 Go 侧漂移，症状不是报错，而是"生成器选了一个注定被拒的工具"或
// "Python 认不出一个诊断码"，两者都要等一次 trampoline 往返才暴露。

// paletteDerivedTools 用当前注册表现算"本 palette 下的工具集合"，
// 与 Compiler.Manifest 同一句 `Effect.Has(palette)`。刻意不硬编码条数：
// 工具数会随注册表演进，硬编码只会让这条测试变成"改注册表就得改数字"。
func paletteDerivedTools(palette toolspec.Effect) []string {
	var names []string
	for _, s := range toolspec.Specs() {
		if s.Effect.Has(palette) {
			names = append(names, s.Name)
		}
	}
	sort.Strings(names)
	return names
}

func TestManifestToolsMatchPalette(t *testing.T) {
	m := (&plasma.Compiler{Palette: plasma.PlasmaPalette}).Manifest()
	want := paletteDerivedTools(plasma.PlasmaPalette)
	if len(want) == 0 {
		t.Fatal("palette 派生出 0 个工具 —— 注册表没有 populate（main_test.go 的空白 import 掉了？）")
	}
	got := make([]string, 0, len(m.Tools))
	for _, tool := range m.Tools {
		got = append(got, tool.Name)
		if len(tool.Effects) == 0 {
			t.Errorf("工具 %q 的 Effects 为空：Python 侧没法判断它的权限形状", tool.Name)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("manifest 工具数 = %d, palette 派生 = %d\n got=%v\nwant=%v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("工具[%d] = %q, want %q（顺序必须是字典序）", i, got[i], want[i])
		}
	}
	// palette 自己也必须逐位对上：Python 侧会拿它做二次校验。
	if len(m.Palette) != len(plasma.PlasmaPalette.Names()) {
		t.Errorf("manifest.Palette = %v，与 PlasmaPalette.Names() 不等长", m.Palette)
	}
}

// CapabilityLock 必须是内容哈希：同一注册表稳定，改动 palette 后变化。
// 只断言"非空"等于什么都没测（坑 25/35）。
func TestManifestCapabilityLockIsStableAndPaletteSensitive(t *testing.T) {
	base := (&plasma.Compiler{Palette: plasma.PlasmaPalette}).Manifest()
	again := (&plasma.Compiler{Palette: plasma.PlasmaPalette}).Manifest()
	if base.CapabilityLock == "" {
		t.Fatal("CapabilityLock 为空：bundle.capability_lock 将无法溯源")
	}
	if base.CapabilityLock != again.CapabilityLock {
		t.Fatalf("同一份注册表两次得到不同的锁：%s vs %s", base.CapabilityLock, again.CapabilityLock)
	}

	// 收窄一位能力 ⇒ 工具集合必须收窄，锁必须变。
	narrow := plasma.PlasmaPalette &^ toolspec.EffectToolLoad
	narrowManifest := (&plasma.Compiler{Palette: narrow}).Manifest()
	if narrowManifest.CapabilityLock == base.CapabilityLock {
		t.Error("palette 改了一位，CapabilityLock 却没变 —— 锁没有覆盖 palette")
	}
	if len(narrowManifest.Tools) >= len(base.Tools) {
		t.Errorf("去掉 tool_load 后工具数没有下降：%d → %d",
			len(base.Tools), len(narrowManifest.Tools))
	}

	// 多加一位能力（升级位）同样要换锁：Python 侧据此知道"这份清单变了"。
	// 判据用 BeyondBase 而不是手写位运算，避免第二份能力数学。
	extra := plasma.BeyondBase(toolspec.EffectFSWrite|toolspec.EffectShell, plasma.PlasmaPalette)
	wideManifest := (&plasma.Compiler{Palette: plasma.PlasmaPalette | extra}).Manifest()
	if wideManifest.CapabilityLock == base.CapabilityLock {
		t.Error("palette 扩了能力位，CapabilityLock 却没变")
	}
}

// manifest 的 JSON 必须能被读回（它是写给另一个进程的契约）。
func TestManifestRoundTripsThroughJSON(t *testing.T) {
	m := (&plasma.Compiler{Palette: plasma.PlasmaPalette}).Manifest()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back plasma.Manifest
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.CapabilityLock != m.CapabilityLock || len(back.Tools) != len(m.Tools) {
		t.Errorf("manifest 过一遍 JSON 后漂移: %+v", back)
	}
	if back.Defaults.LLMTimeout != m.Defaults.LLMTimeout {
		t.Errorf("defaults.llm_timeout = %q, want %q（必须是可读的 duration 文本）",
			back.Defaults.LLMTimeout, m.Defaults.LLMTimeout)
	}
}

// DiagCatalog 是码表本身，所以它必须与"corpus 真正发射过的码"是同一个集合。
// 两个方向都测：表里多一个码 = 发布了从没被触发过的规则；少一个 = Python 认不出。
func TestDiagCatalogMatchesCorpus(t *testing.T) {
	catalog := plasma.DiagCatalog()
	if len(catalog) == 0 {
		t.Fatal("DiagCatalog 为空")
	}
	inCatalog := make(map[string]bool, len(catalog))
	for _, c := range catalog {
		if c.Code == "" || c.Summary == "" {
			t.Errorf("码表条目不完整: %+v", c)
		}
		if c.Severity != plasma.SeverityError && c.Severity != plasma.SeverityWarning {
			t.Errorf("%s 的 severity = %q，只允许 error / warning", c.Code, c.Severity)
		}
		if inCatalog[c.Code] {
			t.Errorf("码表里 %s 重复", c.Code)
		}
		inCatalog[c.Code] = true
	}

	emitted := corpusEmittedCodes(t)
	for code := range inCatalog {
		if !emitted[code] {
			t.Errorf("码表里的 %s 没有任何 corpus 用例能发射它 —— 它的诊断文本没被钉住", code)
		}
	}
	for code := range emitted {
		if !inCatalog[code] {
			t.Errorf("corpus 发射了 %s，但 DiagCatalog 没登记 —— Python 侧会认不出它", code)
		}
	}
}

// corpusEmittedCodes 重放 corpus（与 TestCorpusCoversEveryCode 同一个数据源），
// 返回被真正发射过的码集合。5xx 只在验证阶段产生，所以也走一次 verifyOne。
func corpusEmittedCodes(t *testing.T) map[string]bool {
	t.Helper()
	seen := map[string]bool{}
	for _, tc := range corpus {
		pal := testPalette
		if tc.palette != 0 {
			pal = tc.palette
		}
		bundle, err := plasma.Elaborate([]byte(tc.raw), plasma.DefaultDefaults())
		if err != nil {
			var de *plasma.DiagError
			if errors.As(err, &de) {
				seen[de.Diag.Code] = true
			}
			continue
		}
		r := (&plasma.Compiler{Palette: pal, EscalationApproved: tc.escalation}).
			Compile(context.Background(), bundle)
		for _, d := range r.Diagnostics {
			seen[d.Code] = true
		}
		if _, vrep, ok := verifyOne(tc); ok {
			for _, d := range vrep.Diagnostics {
				seen[d.Code] = true
			}
		}
	}
	return seen
}
