package blueprint

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRootUsesHomeLayoutAndEnvOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(RootEnv, "")

	got, err := Root("yagent_77506fcd")
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	want := filepath.Join(home, ".yagent", "data", "blueprint", "yagent_77506fcd")
	if got != want {
		t.Errorf("Root = %q, want %q", got, want)
	}

	override := t.TempDir()
	t.Setenv(RootEnv, override)
	got, err = Root("proj_1")
	if err != nil {
		t.Fatalf("Root with override: %v", err)
	}
	if want := filepath.Join(override, "proj_1"); got != want {
		t.Errorf("覆盖后的 Root = %q, want %q", got, want)
	}
}

// 拿不到 home 时报错，**不退回当前目录**：当前目录就是用户的项目仓库，
// 退回那里等于把真源写进工作区（I7 + 会被 stash_dirty_worktree 收走）。
func TestRootFailsInsteadOfFallingBackToCwd(t *testing.T) {
	t.Setenv(RootEnv, "")
	t.Setenv("HOME", "")

	if _, err := Root("proj_1"); err == nil {
		t.Error("HOME 为空时本该报错（否则就退回当前目录）")
	}
}

func TestSanitizeProjectID(t *testing.T) {
	got, err := SanitizeProjectID("my proj/v2")
	if err != nil {
		t.Fatalf("SanitizeProjectID: %v", err)
	}
	if strings.ContainsAny(got, "/ ") {
		t.Errorf("清洗后仍有路径分隔符或空格: %q", got)
	}
	for _, bad := range []string{"", "..", ".", "   "} {
		if _, err := SanitizeProjectID(bad); err == nil {
			t.Errorf("SanitizeProjectID(%q) 本该拒绝 —— 蓝图目录名是项目身份，不能猜一个 adhoc 出来", bad)
		}
	}
}

func TestPrepareEndToEnd(t *testing.T) {
	project := projectWith(t, "proj", "go.mod")
	t.Setenv(RootEnv, t.TempDir())

	root, err := Prepare(project)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	// 目录名必须与 rollout/artifact 同一把主键（I12）——否则同一仓库的数据散在两个名字里。
	if base := filepath.Base(root); !strings.HasPrefix(base, "proj_") {
		t.Errorf("存储目录名 = %q, want 前缀 proj_", base)
	}

	// 覆盖根指到项目里面时，Prepare 必须拦住（I7 的可执行形态）。
	t.Setenv(RootEnv, filepath.Join(project, "sneaky"))
	if _, err := Prepare(project); err == nil {
		t.Fatal("存储根落在项目内时 Prepare 本该拒绝")
	}

	// 危险项目根在算 ID 之前就被拒绝：顺序反了会先把 "$HOME" 折算成目录名。
	t.Setenv(RootEnv, t.TempDir())
	if _, err := Prepare(""); err == nil {
		t.Error("空项目路径本该被拒")
	}
}
