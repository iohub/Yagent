package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// [plasma] 的测试口径与 [blackboard]/[resume]/[blueprint] 一致：**逐键**断言
// "默认值"与"显式值不同"，而不是只断言结构体非零（坑 25/35）。
//
// 这里的 18 个键分两族，测试必须分开写：
//   - <=0 = 取默认：max_loaded_agents / max_llm_calls / behavioral_token_budget / promote_min_runs
//     （外加五个"0 无语义"的时长键）
//   - 0 本身有语义：catalog_line_chars / workdir_ttl / store_ttl
//
// 混族的后果是某一族的语义被另一族的归一化悄悄顶掉，而症状只是"我配的 0 没生效"。

func plasmaClearKillSwitch(t *testing.T) {
	t.Helper()
	t.Setenv("YAGENT_PLASMA", "")
}

// plasmaWholeSection 是一份"每个键都显式写了非默认值"的配置：显式值必须与默认值
// 逐键不同，否则下面那条"改了就得变"的断言会变成同义反复。
func plasmaWholeSection() PlasmaConfig {
	return PlasmaConfig{
		Enable:                   false,
		Python:                   "/usr/local/bin/python3.12",
		Store:                    "/tmp/plasma-store",
		MaxLoadedAgents:          3,
		CatalogLineChars:         40,
		MaxLLMCalls:              5,
		WorkdirTTL:               2 * time.Hour,
		CompileTimeout:           90 * time.Second,
		StructuralFixtureTimeout: 30 * time.Second,
		BehavioralFixtureTimeout: 3 * time.Minute,
		BehavioralTokenBudget:    1234,
		SampleTimeout:            4 * time.Minute,
		AutoDelegateMinGrade:     PlasmaGradePromoted,
		PromoteMinRuns:           9,
		StoreTTL:                 24 * time.Hour,
		EvolveEnable:             true,
		EvolveInterval:           45 * time.Minute,
		ProposalDir:              "/tmp/plasma-proposals",
	}
}

// plasmaAllPresent 让"整段都写了"的 presence 成立。三个 0-有语义的键在这里也是显式。
func plasmaAllPresent() PlasmaPresence {
	return PlasmaPresence{Section: true, Enable: true, CatalogLineChars: true, WorkdirTTL: true, StoreTTL: true}
}

func TestPlasmaDefaultsMatchDesignDoc(t *testing.T) {
	plasmaClearKillSwitch(t)
	got, err := NormalizePlasma(PlasmaConfig{}, PlasmaPresence{})
	if err != nil {
		t.Fatalf("整段缺省不该报错: %v", err)
	}
	// 与 docs/plasma-architecture.md §7.4 的表格逐格对齐。数字改了这里就必须改 ——
	// 这正是"默认值是程序保证值"的含义。
	cases := []struct {
		key  string
		got  interface{}
		want interface{}
	}{
		{"enable", got.Enable, true},
		{"python", got.Python, ""},
		{"store", got.Store, ""},
		{"max_loaded_agents", got.MaxLoadedAgents, 12},
		{"catalog_line_chars", got.CatalogLineChars, 120},
		{"max_llm_calls", got.MaxLLMCalls, 12},
		{"workdir_ttl", got.WorkdirTTL, time.Hour},
		{"compile_timeout", got.CompileTimeout, 60 * time.Second},
		{"structural_fixture_timeout", got.StructuralFixtureTimeout, 60 * time.Second},
		{"behavioral_fixture_timeout", got.BehavioralFixtureTimeout, 10 * time.Minute},
		{"behavioral_token_budget", got.BehavioralTokenBudget, 60000},
		{"sample_timeout", got.SampleTimeout, 10 * time.Minute},
		{"auto_delegate_min_grade", got.AutoDelegateMinGrade, PlasmaGradeVerified},
		{"promote_min_runs", got.PromoteMinRuns, 5},
		{"store_ttl", got.StoreTTL, 720 * time.Hour},
		{"evolve_enable", got.EvolveEnable, false},
		{"evolve_interval", got.EvolveInterval, 30 * time.Minute},
		{"proposal_dir", got.ProposalDir, ""},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("默认 %s = %v, want %v", c.key, c.got, c.want)
		}
	}
}

// 每个键都要有"改值即失败"的断言：显式值必须原样留在归一化结果里，
// 而且必须与默认值不同（否则这条断言不会因为归一化写错而红）。
func TestPlasmaEveryKeySurvivesNormalization(t *testing.T) {
	plasmaClearKillSwitch(t)
	in := plasmaWholeSection()
	got, err := NormalizePlasma(in, plasmaAllPresent())
	if err != nil {
		t.Fatalf("显式配置不该报错: %v", err)
	}
	if got != in {
		t.Errorf("归一化改动了显式值:\n got=%+v\nwant=%+v", got, in)
	}
	def, _ := NormalizePlasma(PlasmaConfig{}, PlasmaPresence{})
	cases := []struct {
		key string
		def interface{}
		got interface{}
	}{
		{"enable", def.Enable, got.Enable},
		{"python", def.Python, got.Python},
		{"store", def.Store, got.Store},
		{"max_loaded_agents", def.MaxLoadedAgents, got.MaxLoadedAgents},
		{"catalog_line_chars", def.CatalogLineChars, got.CatalogLineChars},
		{"max_llm_calls", def.MaxLLMCalls, got.MaxLLMCalls},
		{"workdir_ttl", def.WorkdirTTL, got.WorkdirTTL},
		{"compile_timeout", def.CompileTimeout, got.CompileTimeout},
		{"structural_fixture_timeout", def.StructuralFixtureTimeout, got.StructuralFixtureTimeout},
		{"behavioral_fixture_timeout", def.BehavioralFixtureTimeout, got.BehavioralFixtureTimeout},
		{"behavioral_token_budget", def.BehavioralTokenBudget, got.BehavioralTokenBudget},
		{"sample_timeout", def.SampleTimeout, got.SampleTimeout},
		{"auto_delegate_min_grade", def.AutoDelegateMinGrade, got.AutoDelegateMinGrade},
		{"promote_min_runs", def.PromoteMinRuns, got.PromoteMinRuns},
		{"store_ttl", def.StoreTTL, got.StoreTTL},
		{"evolve_enable", def.EvolveEnable, got.EvolveEnable},
		{"evolve_interval", def.EvolveInterval, got.EvolveInterval},
		{"proposal_dir", def.ProposalDir, got.ProposalDir},
	}
	for _, c := range cases {
		if c.def == c.got {
			t.Errorf("%s 的显式值与默认值相同（%v）—— 这条断言抓不到归一化写错", c.key, c.def)
		}
	}
}

// 第二族的三种 0：显式 0 必须保留，没写才取默认。两次归一化的入参**只有
// presence 不同**，所以任何一边错都直接指向"看值而不是看键"。
func TestPlasmaExplicitZeroKeepsItsMeaning(t *testing.T) {
	plasmaClearKillSwitch(t)
	zero := PlasmaConfig{}
	zeroPresent := PlasmaPresence{Section: true, CatalogLineChars: true, WorkdirTTL: true, StoreTTL: true}

	got, err := NormalizePlasma(zero, zeroPresent)
	if err != nil {
		t.Fatalf("显式 0 不该报错: %v", err)
	}
	if got.CatalogLineChars != 0 {
		t.Errorf("catalog_line_chars = %d, want 0（不裁剪）", got.CatalogLineChars)
	}
	if got.WorkdirTTL != 0 {
		t.Errorf("workdir_ttl = %v, want 0（不清理孤儿目录）", got.WorkdirTTL)
	}
	if got.StoreTTL != 0 {
		t.Errorf("store_ttl = %v, want 0（不清理）", got.StoreTTL)
	}

	absent, err := NormalizePlasma(zero, PlasmaPresence{Section: true})
	if err != nil {
		t.Fatalf("没写这三个键不该报错: %v", err)
	}
	def := DefaultPlasmaConfig()
	if absent.CatalogLineChars != def.CatalogLineChars {
		t.Errorf("没写 catalog_line_chars 时 = %d, want 默认 %d", absent.CatalogLineChars, def.CatalogLineChars)
	}
	if absent.WorkdirTTL != def.WorkdirTTL {
		t.Errorf("没写 workdir_ttl 时 = %v, want 默认 %v", absent.WorkdirTTL, def.WorkdirTTL)
	}
	if absent.StoreTTL != def.StoreTTL {
		t.Errorf("没写 store_ttl 时 = %v, want 默认 %v", absent.StoreTTL, def.StoreTTL)
	}
}

// <=0 = 取默认的那一族：显式 0 与负数都必须回落，与上面那一族正好相反。
func TestPlasmaNonPositiveMeansDefault(t *testing.T) {
	plasmaClearKillSwitch(t)
	def := DefaultPlasmaConfig()
	for _, v := range []int{0, -1} {
		got, err := NormalizePlasma(PlasmaConfig{
			MaxLoadedAgents:       v,
			MaxLLMCalls:           v,
			BehavioralTokenBudget: v,
			PromoteMinRuns:        v,
		}, PlasmaPresence{Section: true})
		if err != nil {
			t.Fatalf("%d 不该报错: %v", v, err)
		}
		if got.MaxLoadedAgents != def.MaxLoadedAgents || got.MaxLLMCalls != def.MaxLLMCalls ||
			got.BehavioralTokenBudget != def.BehavioralTokenBudget || got.PromoteMinRuns != def.PromoteMinRuns {
			t.Errorf("%d 没有回落到默认: %+v", v, got)
		}
	}
	// 五个"0 无语义"的时长键同理。
	got, err := NormalizePlasma(PlasmaConfig{}, PlasmaPresence{Section: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.CompileTimeout != def.CompileTimeout || got.StructuralFixtureTimeout != def.StructuralFixtureTimeout ||
		got.BehavioralFixtureTimeout != def.BehavioralFixtureTimeout || got.SampleTimeout != def.SampleTimeout ||
		got.EvolveInterval != def.EvolveInterval {
		t.Errorf("0 时长键没有回落到默认: %+v", got)
	}
}

// 急停判据必须在 NormalizePlasma 内部：调用点不写任何环境变量判断。
func TestPlasmaKillSwitchForcesEnableFalse(t *testing.T) {
	t.Setenv("YAGENT_PLASMA", "0")
	c := plasmaWholeSection()
	c.Enable = true
	got, err := NormalizePlasma(c, plasmaAllPresent())
	if err != nil {
		t.Fatal(err)
	}
	if got.Enable {
		t.Error("YAGENT_PLASMA=0 时 Enable 仍为 true")
	}
	// 只认精确 "0"：别的值不能误触发（否则 YAGENT_PLASMA=false 会被当成急停）。
	t.Setenv("YAGENT_PLASMA", "false")
	got, err = NormalizePlasma(c, plasmaAllPresent())
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enable {
		t.Error("YAGENT_PLASMA=false 被误判成急停")
	}
}

// 非法值必须报错：时长写错时静默归零会让超时/清理变成"没有"，
// grade 写错会改变自动委派的安全边界（见 NormalizePlasma 的注释）。
func TestPlasmaRejectsInvalidValues(t *testing.T) {
	plasmaClearKillSwitch(t)
	negative := []struct {
		name string
		c    PlasmaConfig
	}{
		{"workdir_ttl", PlasmaConfig{WorkdirTTL: -time.Second}},
		{"store_ttl", PlasmaConfig{StoreTTL: -time.Second}},
		{"compile_timeout", PlasmaConfig{CompileTimeout: -time.Second}},
		{"structural_fixture_timeout", PlasmaConfig{StructuralFixtureTimeout: -time.Second}},
		{"behavioral_fixture_timeout", PlasmaConfig{BehavioralFixtureTimeout: -time.Second}},
		{"sample_timeout", PlasmaConfig{SampleTimeout: -time.Second}},
		{"evolve_interval", PlasmaConfig{EvolveInterval: -time.Second}},
	}
	for _, c := range negative {
		_, err := NormalizePlasma(c.c, PlasmaPresence{Section: true})
		if err == nil {
			t.Errorf("%s 为负却通过了归一化", c.name)
		} else if !strings.Contains(err.Error(), c.name) {
			t.Errorf("%s 的报错没有点名那个键: %v", c.name, err)
		}
	}

	for _, bad := range []string{"strong", "PROVISIONAL ", "verified!", "unknown"} {
		_, err := NormalizePlasma(PlasmaConfig{AutoDelegateMinGrade: bad}, PlasmaPresence{Section: true})
		if err == nil {
			t.Errorf("auto_delegate_min_grade = %q 却通过了归一化", bad)
		}
	}
	// 三档都必须被接受，否则"合法值报错"会让配置永不可用。
	for _, good := range []string{PlasmaGradeProvisional, PlasmaGradeVerified, PlasmaGradePromoted} {
		if _, err := NormalizePlasma(PlasmaConfig{AutoDelegateMinGrade: good}, PlasmaPresence{Section: true}); err != nil {
			t.Errorf("合法档位 %q 被拒: %v", good, err)
		}
	}
}

// 路径键：空 = 默认路径，且 `~` 要展开。用 t.Setenv("HOME") 拥有自己的目录（坑 23）。
func TestPlasmaPathsExpandTilde(t *testing.T) {
	plasmaClearKillSwitch(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	if got := DefaultPlasmaConfig().Store; got != "" {
		t.Errorf("默认 store = %q, want 空（空才代表默认路径）", got)
	}
	if got := PlasmaStoreDir(); got != filepath.Join(home, ".yagent", "agents") {
		t.Errorf("PlasmaStoreDir() = %q, want %q", got, filepath.Join(home, ".yagent", "agents"))
	}
	if got := PlasmaProposalDir(); got != filepath.Join(home, ".yagent", "agents", "proposals") {
		t.Errorf("PlasmaProposalDir() = %q", got)
	}

	if err := SetGlobalPlasma(PlasmaConfig{Store: "~/custom/agents", ProposalDir: "~/custom/props"},
		PlasmaPresence{Section: true}); err != nil {
		t.Fatal(err)
	}
	if got := PlasmaStoreDir(); got != filepath.Join(home, "custom", "agents") {
		t.Errorf("显式 store 没有展开 ~: %q", got)
	}
	if got := PlasmaProposalDir(); got != filepath.Join(home, "custom", "props") {
		t.Errorf("显式 proposal_dir 没有展开 ~: %q", got)
	}
	// 绝对路径原样使用。
	if err := SetGlobalPlasma(PlasmaConfig{Store: "/abs/agents"}, PlasmaPresence{Section: true}); err != nil {
		t.Fatal(err)
	}
	if got := PlasmaStoreDir(); got != "/abs/agents" {
		t.Errorf("绝对路径被改写: %q", got)
	}
	// 复位，避免污染同包其它用例（全局单例是包级状态）。
	t.Cleanup(func() {
		if err := SetGlobalPlasma(DefaultPlasmaConfig(), PlasmaPresence{}); err != nil {
			t.Fatalf("复位全局 plasma 配置: %v", err)
		}
	})
}

// SetGlobalPlasma 必须把错误传出来：忽略它等于把"配置坏了"变成"按默认值跑"。
func TestSetGlobalPlasmaPropagatesErrors(t *testing.T) {
	plasmaClearKillSwitch(t)
	if err := SetGlobalPlasma(PlasmaConfig{AutoDelegateMinGrade: "nope"}, PlasmaPresence{Section: true}); err == nil {
		t.Error("SetGlobalPlasma 吞掉了非法 grade 的错误")
	}
	t.Cleanup(func() {
		if err := SetGlobalPlasma(DefaultPlasmaConfig(), PlasmaPresence{}); err != nil {
			t.Fatalf("复位全局 plasma 配置: %v", err)
		}
	})
}

// [plasma] 段缺失时整段走默认，且不会因为残留的零值字段报错。
func TestPlasmaMissingSectionUsesDefaultsEvenWithZeroes(t *testing.T) {
	plasmaClearKillSwitch(t)
	got, err := NormalizePlasma(PlasmaConfig{AutoDelegateMinGrade: "garbage"}, PlasmaPresence{})
	if err != nil {
		t.Fatalf("整段缺失时不该因为字段零值报错: %v", err)
	}
	if got != DefaultPlasmaConfig() {
		t.Errorf("整段缺失没有回落到默认:\n got=%+v\nwant=%+v", got, DefaultPlasmaConfig())
	}
}

// 模板里必须写上 [plasma] 段：它是用户发现这组键存在的唯一入口，
// 而 TestTemplatesDecodeCompletelyAndValidate 只保证"写了能被解析"。
func TestPlasmaSectionIsInTemplates(t *testing.T) {
	for _, path := range []string{"default_config.toml", filepath.Join("..", "..", "config", "config.toml")} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Logf("跳过 %s: %v", path, err)
			continue
		}
		body := string(raw)
		if !strings.Contains(body, "[plasma]") {
			t.Errorf("%s 里没有 [plasma] 段", path)
		}
		for _, key := range []string{
			"max_loaded_agents", "catalog_line_chars", "max_llm_calls", "workdir_ttl",
			"compile_timeout", "structural_fixture_timeout", "behavioral_fixture_timeout",
			"behavioral_token_budget", "sample_timeout", "auto_delegate_min_grade",
			"promote_min_runs", "store_ttl", "evolve_enable", "evolve_interval", "proposal_dir",
		} {
			if !strings.Contains(body, key) {
				t.Errorf("%s 的 [plasma] 段缺键 %s", path, key)
			}
		}
	}
}
