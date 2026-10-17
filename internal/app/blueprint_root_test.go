package app

// blueprint_root_test.go — [blueprint] enable 的唯一读取点（resolveBlueprintRoot）。
//
// 这条接缝在 R1 只报一条启动日志，但它决定 R2–R8 有没有地方落盘，
// 而且三种结果（关掉 / 被护栏拒绝 / 就绪）必须能被调用方区分：
// 把"这个目录不该建蓝图"压成"关掉"，用户就永远不知道为什么没有蓝图。

import (
	"os"
	"path/filepath"
	"testing"

	"yagent/internal/config"
	"yagent/internal/globalctx"
	"yagent/internal/projectid"
)

// withBlueprintConfig 显式设定全局配置并在用例结束后恢复默认。
func withBlueprintConfig(t *testing.T, c config.BlueprintConfig, p config.BlueprintPresence) {
	t.Helper()
	t.Cleanup(func() {
		config.SetGlobalBlueprint(config.DefaultBlueprintConfig(), config.BlueprintPresence{})
	})
	config.SetGlobalBlueprint(c, p)
}

func appAt(projectPath string) *Yagent {
	return &Yagent{globalCtx: &globalctx.GlobalCtx{ProjectPath: projectPath}}
}

func TestResolveBlueprintRootDisabled(t *testing.T) {
	t.Setenv(configKillSwitchEnv, "")
	withBlueprintConfig(t,
		config.BlueprintConfig{Enable: false, InjectInto: []string{"director", "repo", "coding"}},
		config.BlueprintPresence{Section: true, Enable: true})

	root, err := appAt(t.TempDir()).resolveBlueprintRoot()
	if err != nil {
		t.Fatalf("关掉时不该报错: %v", err)
	}
	if root != "" {
		t.Errorf(`root = %q, want 空串（表示"整套机制退出"）`, root)
	}
}

// 环境变量压过配置里的 true —— 五处共用同一个判据（I8）才不会出现"关了一半"。
func TestResolveBlueprintRootKillSwitch(t *testing.T) {
	project := projectDir(t)
	t.Setenv(configKillSwitchEnv, "0")
	withBlueprintConfig(t, config.DefaultBlueprintConfig(), config.BlueprintPresence{})

	root, err := appAt(project).resolveBlueprintRoot()
	if err != nil {
		t.Fatalf("kill switch 下不该报错: %v", err)
	}
	if root != "" {
		t.Errorf("YAGENT_BLUEPRINT=0 时 root = %q, want 空", root)
	}
}

func TestResolveBlueprintRootIsPerProject(t *testing.T) {
	t.Setenv(configKillSwitchEnv, "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	withBlueprintConfig(t, config.DefaultBlueprintConfig(), config.BlueprintPresence{})

	first := projectDir(t)
	second := projectDir(t)

	a, err := appAt(first).resolveBlueprintRoot()
	if err != nil {
		t.Fatalf("第一个项目: %v", err)
	}
	if want := filepath.Join(home, ".yagent", "data", "blueprint", projectid.Compute(first)); a != want {
		t.Errorf("root = %q, want %q（必须与 rollout/artifact 同一把主键）", a, want)
	}
	b, err := appAt(second).resolveBlueprintRoot()
	if err != nil {
		t.Fatalf("第二个项目: %v", err)
	}
	if a == b {
		t.Errorf("两个项目算出了同一个存储根: %q", a)
	}
}

// 开着但目录不可信：必须返回 error，而不是"空串 + nil"（那与"关掉"长得一样）。
func TestResolveBlueprintRootRefusesNonProject(t *testing.T) {
	t.Setenv(configKillSwitchEnv, "")
	t.Setenv("HOME", t.TempDir())
	withBlueprintConfig(t, config.DefaultBlueprintConfig(), config.BlueprintPresence{})

	loose := filepath.Join(t.TempDir(), "loose")
	if err := os.MkdirAll(loose, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	root, err := appAt(loose).resolveBlueprintRoot()
	if err == nil {
		t.Fatalf("既非 git 又无 manifest 的目录本该被拒，实得 root=%q", root)
	}
}

const configKillSwitchEnv = "YAGENT_BLUEPRINT"

// projectDir 造一个带 go.mod 的临时项目根。
func projectDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/proj\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	return dir
}
