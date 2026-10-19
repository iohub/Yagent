package app

// blueprint_runtime_test.go — 蓝图运行时的装配接缝（R3 起才是真消费者）。
//
// R1 时这里只有"算一个存储根、报一条日志"；现在这块要回答三个问题：
//   - 开着时：真的派生出东西、真的建了存储根、退出时 goroutine 真的被等掉了；
//   - 关掉时（配置 false 或 YAGENT_BLUEPRINT=0）：**什么都不动** —— 不起线程、不建目录
//     （I8 的"每加一处消费点就加一条 env=0 时它必须不动"，这条就是那一处）；
//   - 被根护栏拒绝时：原因要说得出来，而不是退化成"关掉"。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yagent/internal/blueprint"
	"yagent/internal/config"
	"yagent/internal/supervise"
)

// isolateBlueprintRoot 把存储根指到临时目录：用例绝不该往真的 ~/.yagent 里写。
func isolateBlueprintRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "blueprint-store")
	t.Setenv(blueprint.RootEnv, root)
	return root
}

func TestBuildBlueprintDerivesAndStops(t *testing.T) {
	t.Setenv(configKillSwitchEnv, "")
	isolateBlueprintRoot(t)
	withBlueprintConfig(t, config.DefaultBlueprintConfig(), config.BlueprintPresence{})

	project := projectDir(t)
	ca := appAt(project)
	r := ca.buildBlueprint()
	if r == nil {
		t.Fatalf("配置开着、目录合规，却没建出运行时")
	}
	if r.Root() == "" {
		t.Errorf("存储根为空")
	}
	snap := r.Snapshot()
	if snap == nil {
		t.Fatalf("启动时同步派生没落地（后续一切都会是 scanning…）")
	}
	if len(snap.Leaves) == 0 {
		t.Errorf("派生出 0 片模块: %+v", r.Status())
	}
	// 派生循环必须挂在**具名** supervise 域上（否则退出时没人等它，--prompt 会挂住）。
	if !hasBlueprintLease("blueprint") {
		t.Errorf("派生循环没进 supervise 域，退出时无人可等它: leases=%v", leaseNames())
	}

	ca.blueprint = r
	ca.blueprint.stop()
	if hasBlueprintLease("blueprint/derive") {
		t.Errorf("Close 之后派生循环仍在跑: %v", leaseNames())
	}
	// 幂等：Yagent.Close 可能被多家调用。
	ca.blueprint.stop()
}

func TestBuildBlueprintKillSwitchDoesNothing(t *testing.T) {
	root := isolateBlueprintRoot(t)
	t.Setenv(configKillSwitchEnv, "0")
	// 配置里写的是 true —— 环境变量必须压过它（I8 唯一的合并点在 config.BlueprintEnabled）。
	withBlueprintConfig(t, config.DefaultBlueprintConfig(), config.BlueprintPresence{Section: true, Enable: true})

	r := appAt(projectDir(t)).buildBlueprint()
	if r != nil {
		t.Fatalf("YAGENT_BLUEPRINT=0 却建出了运行时")
	}
	if dirExists(root) {
		t.Errorf("关掉整套机制却还是建了存储根: %s", root)
	}
	if hasBlueprintLease("blueprint") {
		t.Errorf("关掉之后 supervise 域里仍有蓝图 goroutine: %v", leaseNames())
	}
}

func TestBuildBlueprintRefusedProjectRoot(t *testing.T) {
	t.Setenv(configKillSwitchEnv, "")
	isolateBlueprintRoot(t)
	withBlueprintConfig(t, config.DefaultBlueprintConfig(), config.BlueprintPresence{})

	// 既不是 git 仓库也没有 manifest 的空目录（I16）：必须是"被拒绝"，不是"没有开关"。
	home := t.TempDir()
	if r := appAt(home).buildBlueprint(); r != nil {
		t.Errorf("护栏该拒绝的目录却建出了运行时")
	}
}

// inject_into 是成本自救阀：摘掉某个站点只是**不注入**，
// 读取通道（read_blueprint）与落盘钩子都不该跟着一起关（§6.3 与 §10 的分工）。
func TestViewForHonorsInjectInto(t *testing.T) {
	t.Setenv(configKillSwitchEnv, "")
	isolateBlueprintRoot(t)
	withBlueprintConfig(t,
		config.BlueprintConfig{
			Enable: true, InjectInto: []string{"repo"},
			IndexMaxTokens: 1000, ProseBudgetTokens: 1500,
		},
		config.BlueprintPresence{Section: true, Enable: true, InjectInto: true})

	r := appAt(projectDir(t)).buildBlueprint()
	if r == nil {
		t.Fatalf("没建出运行时")
	}
	defer r.stop()

	if got := r.viewFor("repo").Resident(); !strings.Contains(got, "Repository Blueprint") {
		t.Errorf("在 inject_into 里的站点没拿到常驻块: %q", got)
	}
	if got := r.viewFor("coding").Resident(); got != "" {
		t.Errorf("被摘掉的站点还是注入了: %q", got)
	}
	// 被摘掉的站点仍然读得到骨架 —— 关的是每轮成本，不是能力。
	if r.viewFor("coding").Source().Snapshot() == nil {
		t.Errorf("摘掉注入把快照也弄没了（inject_into 不是能力开关）")
	}
}

func hasBlueprintLease(substr string) bool {
	for _, l := range supervise.Root().Pending() {
		if strings.Contains(l.String(), substr) {
			return true
		}
	}
	return false
}

func leaseNames() []string {
	var out []string
	for _, l := range supervise.Root().Pending() {
		out = append(out, l.String())
	}
	return out
}

func dirExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
