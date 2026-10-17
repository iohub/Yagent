package config

// blueprint_test.go — [blueprint] 的缺省归一、存在性判据与 kill-switch
// （docs/repo-blueprint-design.md v4 §10）。
//
// 每条用例都对应一个"错了不报错"的形状：
//   - enable 的缺省语义是"开"而 Go 零值是 false → 只看值会把"没写"读成"用户关掉了"（坑 26）；
//   - inject_into 显式写空数组是"三处都别注入"，与"没写这个键"（默认三处）必须能区分；
//   - 预算 <=0 归默认，但正数必须原样到达消费方，否则模板里那行注释就是谎话（坑 25）。

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func loadBlueprintCfg(t *testing.T, section string) *Config {
	t.Helper()
	t.Cleanup(func() {
		SetGlobalBlueprint(DefaultBlueprintConfig(), BlueprintPresence{})
	})
	cfg, err := LoadFromFile(minimalConfig(t, section))
	if err != nil {
		t.Fatalf("LoadFromFile: %v", err)
	}
	return cfg
}

func TestBlueprintDefaultsWhenSectionAbsent(t *testing.T) {
	cfg := loadBlueprintCfg(t, "")
	d := DefaultBlueprintConfig()

	if cfg.blueprintSectionDefined {
		t.Error("没有 [blueprint] 段时 blueprintSectionDefined 该是 false")
	}
	// InjectInto 是切片，结构体不能用 == 比较。
	if !reflect.DeepEqual(cfg.Blueprint, BlueprintConfig{}) {
		t.Errorf("raw decode = %+v, want 全零（TOML 什么都没填）", cfg.Blueprint)
	}
	active := GlobalBlueprint()
	if !active.Enable {
		t.Error("整段缺省被读成 Enable=false —— 蓝图会静默整体退出")
	}
	if got := strings.Join(active.InjectInto, ","); got != "director,repo,coding" {
		t.Errorf("InjectInto = %q, want director,repo,coding", got)
	}
	if active.IndexMaxTokens != d.IndexMaxTokens || active.ProseBudgetTokens != d.ProseBudgetTokens {
		t.Errorf("预算 = %d/%d, want 默认 %d/%d",
			active.IndexMaxTokens, active.ProseBudgetTokens, d.IndexMaxTokens, d.ProseBudgetTokens)
	}
	if active.ExportPath != "" {
		t.Errorf("ExportPath = %q, want 空（默认绝不往用户仓库写）", active.ExportPath)
	}
	if !BlueprintEnabled() {
		t.Error("BlueprintEnabled() = false，但配置里根本没提过这个特性")
	}
}

// 段写了、但没写 enable：仍然必须是"开"。
// 这条与整段缺省那条**必须分开**：归一化有两条不同路径（!Section 与 !Enable），
// 只测前者的话后者一个闸门都没有 —— 变异验证（把 `c.Enable = defaults.Enable` 改成 false）
// 在只测前者的情况下是绿的，那个洞就是这条测试补上的。
func TestBlueprintEnableDefaultsTrueWhenKeyAbsentInSection(t *testing.T) {
	loadBlueprintCfg(t, "\n[blueprint]\nindex_max_tokens = 800\n")

	active := GlobalBlueprint()
	if !active.Enable {
		t.Error("写了 [blueprint] 段但没写 enable，被当成关掉 —— 缺省语义是开（坑 26）")
	}
	// 同一条路径的反面：整段缺省的兜底不能把用户显式写的其它键顶掉。
	if active.IndexMaxTokens != 800 {
		t.Errorf("IndexMaxTokens = %d, want 800（显式值被缺省归一覆盖）", active.IndexMaxTokens)
	}
}

// 显式写 false 就是关：这条承诺与 [resume] 的 enable_ultimate_compression 那次修复同族 ——
// 以前"见 false 就写回 true"，那个键永远关不掉。
func TestBlueprintEnableFalseIsRespected(t *testing.T) {
	loadBlueprintCfg(t, "\n[blueprint]\nenable = false\n")

	if GlobalBlueprint().Enable {
		t.Error("显式 enable = false 被写回 true")
	}
	if BlueprintEnabled() {
		t.Error("BlueprintEnabled() 该跟随显式的 false")
	}
}

func TestBlueprintKillSwitchOverridesConfig(t *testing.T) {
	loadBlueprintCfg(t, "\n[blueprint]\nenable = true\n")
	if !BlueprintEnabled() {
		t.Fatal("前置条件：enable = true 时该是开的")
	}

	t.Setenv("YAGENT_BLUEPRINT", "0")
	if BlueprintEnabled() {
		t.Error("YAGENT_BLUEPRINT=0 必须压过配置里的 true（五处共用同一个判据，I8）")
	}

	t.Setenv("YAGENT_BLUEPRINT", "1")
	if !BlueprintEnabled() {
		t.Error("只有 =0 才关；其它取值该回落到配置")
	}
}

// 显式空数组 = "哪儿都别注入"，不能被当成"没写"再兜回默认三处。
func TestBlueprintInjectIntoExplicitEmpty(t *testing.T) {
	loadBlueprintCfg(t, "\n[blueprint]\ninject_into = []\n")

	active := GlobalBlueprint()
	if len(active.InjectInto) != 0 {
		t.Errorf("InjectInto = %v, want 空（用户显式关掉全部注入）", active.InjectInto)
	}
	// 另一侧：没写这个键时必须还是三处，否则"缺省"与"关掉"长得一样。
	loadBlueprintCfg(t, "\n[blueprint]\nenable = true\n")
	if got := strings.Join(GlobalBlueprint().InjectInto, ","); got != "director,repo,coding" {
		t.Errorf("只写 enable 时 InjectInto = %q, want 默认三处", got)
	}
}

// 不认识的名字必须被丢掉并播报：静默接受会让用户以为 browser 也吃注入了。
func TestBlueprintInjectIntoDropsUnknownNames(t *testing.T) {
	loadBlueprintCfg(t, "\n[blueprint]\ninject_into = [\"Director\", \"browser\", \"repo\", \"repo\"]\n")

	got := strings.Join(GlobalBlueprint().InjectInto, ",")
	if got != "director,repo" {
		t.Errorf("InjectInto = %q, want \"director,repo\"（小写归一 + 去重 + 丢掉画像外的名字）", got)
	}
}

// 预算数字必须真的到达消费方读的那个字段（坑 25 的判据是"改值有可观察的行为差"）。
func TestBlueprintBudgetsReachConsumers(t *testing.T) {
	cases := []struct {
		section              string
		wantIndex, wantProse int
	}{
		{"\n[blueprint]\nindex_max_tokens = 500\n", 500, 1500},
		{"\n[blueprint]\nprose_budget_tokens = 250\n", 1000, 250},
		{"\n[blueprint]\nindex_max_tokens = 0\nprose_budget_tokens = -1\n", 1000, 1500},
	}
	for _, c := range cases {
		loadBlueprintCfg(t, c.section)
		active := GlobalBlueprint()
		if active.IndexMaxTokens != c.wantIndex || active.ProseBudgetTokens != c.wantProse {
			t.Errorf("节 %q → 预算 = %d/%d, want %d/%d",
				strings.TrimSpace(c.section), active.IndexMaxTokens, active.ProseBudgetTokens,
				c.wantIndex, c.wantProse)
		}
	}
}

func TestBlueprintExportPathIsTrimmed(t *testing.T) {
	loadBlueprintCfg(t, "\n[blueprint]\nexport_path = \"  docs/BLUEPRINT.md  \"\n")
	if got := GlobalBlueprint().ExportPath; got != "docs/BLUEPRINT.md" {
		t.Errorf("ExportPath = %q, want 去空白后的原值", got)
	}
}

// 两份模板承诺了这五个键，就必须真的带着它们、并且真的被 schema 认识 ——
// 与 removed_keys_test.go 的"登记表与夹具互为镜像"同一条纪律。
func TestBlueprintTemplatesCarryAllFiveKeys(t *testing.T) {
	repoTemplate := filepath.Join("..", "..", "config", "config.toml")
	embedded := filepath.Join(t.TempDir(), "config.toml")
	if _, err := EnsureConfigExists(embedded); err != nil {
		t.Fatalf("EnsureConfigExists: %v", err)
	}

	keys := []string{"enable", "inject_into", "index_max_tokens", "prose_budget_tokens", "export_path"}
	for _, path := range []string{embedded, repoTemplate} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(raw)
		if !strings.Contains(text, "[blueprint]") {
			t.Errorf("%s 没有 [blueprint] 段", path)
			continue
		}
		for _, k := range keys {
			if !strings.Contains(text, k+" =") {
				t.Errorf("%s 的 [blueprint] 段缺键 %q", path, k)
			}
		}
	}
}
