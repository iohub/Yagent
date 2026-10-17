package blueprint

import (
	"strings"
	"testing"
)

func TestIsSectionRejectsPrefixSectionItself(t *testing.T) {
	for _, s := range []string{SecArchitecture, SecFlows, SecConventions, SecDependencies, SecGotchas} {
		if !IsSection(s) {
			t.Errorf("%q 应当是合法段落", s)
		}
	}
	// "Modules" 单独不是段落：它必须是 Modules/<path>，否则孤儿判据（§5.3）无从算起。
	if IsSection(SecModules) {
		t.Error("Modules 本身不能当段落用")
	}
	if IsSection("Structure") || IsSection("Coverage") {
		t.Error("derived 段不许被模型写")
	}
	if IsSection("performance") {
		t.Error("枚举外的段名必须被拒收，不能被当成某个默认段")
	}
}

func TestModulePathRoundTrip(t *testing.T) {
	for _, path := range []string{"internal/tui", "internal/agents/director", "_root"} {
		heading := ModuleHeading(path)
		got, ok := ModulePath(heading)
		if !ok || got != path {
			t.Errorf("ModulePath(%q) = %q,%v, want %q", heading, got, ok, path)
		}
	}

	for _, bad := range []string{"Modules/", "Modules", "Modules/a/../../etc", "Architecture"} {
		if _, ok := ModulePath(bad); ok {
			t.Errorf("ModulePath(%q) 不该成立", bad)
		}
	}
}

func TestNormaliseSection(t *testing.T) {
	cases := map[string]Section{
		"architecture": SecArchitecture,
		"Architecture": SecArchitecture,
		"  flows ":     SecFlows,
		"conventions":  SecConventions,
		"dependencies": SecDependencies,
		"gotchas":      SecGotchas,
		"modules":      SecModules,
		"MODULES/":     SecModules,
	}
	for in, want := range cases {
		got, ok := NormaliseSection(in)
		if !ok || got != want {
			t.Errorf("NormaliseSection(%q) = %q,%v want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "performance", "module", "structure", "coverage"} {
		if got, ok := NormaliseSection(in); ok {
			t.Errorf("NormaliseSection(%q) = %q，应当不认", in, got)
		}
	}
}

func TestIsModuleField(t *testing.T) {
	for _, f := range ModuleFields {
		if !IsModuleField(f) {
			t.Errorf("字段 %q 应当合法", f)
		}
	}
	for _, bad := range []string{"", "Purpose", "summary", "notes"} {
		if IsModuleField(bad) {
			t.Errorf("字段 %q 不该被认", bad)
		}
	}
	if len(ModuleFields) != 3 || strings.Join(ModuleFields, ",") != "purpose,data_flow,add_here" {
		t.Errorf("三个模块字段是 schema 的 enum 源，改动必须以显式评审进行: %v", ModuleFields)
	}
}
