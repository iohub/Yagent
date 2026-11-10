package blueprint

import (
	"reflect"
	"strings"
	"testing"
)

func TestScanSelfGolden(t *testing.T) {
	snap, err := Scan("../..", DefaultOptions())
	if err != nil {
		t.Fatalf("本仓自扫失败: %v", err)
	}
	if snap.ModulePath != "yagent" {
		t.Fatalf("ModulePath = %q, 要 %q", snap.ModulePath, "yagent")
	}
	if len(snap.Leaves) != 47 {
		// 47：P10 新增顶层 `plasma/` Python 包（contract/state/sampling/generate/explain/cli 等
		// 13 个文件）—— 它是仓库里第一个 .py 代码片，因此多出一片；`plasma/tests` 与
		// `plasma/testdata` 不单独成片（前者折叠进父片、后者被 testdata 规则排除）。
		// 边数不变（155/155，P10 当时）：Python 不产生 Go import 边。
		t.Fatalf("片数 = %d, golden 是 47 —— 形状变了，先确认变化来源再更新数字", len(snap.Leaves))
	}
	out, in := 0, 0
	for i := range snap.Leaves {
		out += snap.Leaves[i].EdgesOutTotal
		in += snap.Leaves[i].EdgesInTotal
	}
	// 158/158：认知类工具（deepthinking/design）的 LLM 预算必须能配（此前是硬编码 10m，
	// 且用 WithoutCancel 丢掉了取消信号），修复带来两条**新增依赖**，逐条用 `go list` 确认过来源：
	//   ① internal/tools 首次 import internal/config（tools/cog_budget.go 复用
	//      config.DefaultCogToolTimeout，兜底常量只留一处；config 只依赖 util，不成环）；
	//   ② internal/agents 首次 import internal/supervise（executor.go 在工具执行期起心跳
	//      goroutine —— 裸 go 全仓硬禁，supervise 仍是不回指的叶子）。
	// 156/156：P12b 让 internal/tui 首次 import plasma —— `/plasma` 与 `/proposals`
	// 两个斜杠命令要读的是**同一批账目**（store 列表、report.json 诊断、promote 结果、
	// 提案落盘与判决）。在 TUI 侧另写一份格式化必然与 CLI 漂，
	// 所以复用的是 plasma 的导出函数；`go list` 逐条确认过只多这一条边，
	// plasma 自身的 import 集合没动（它仍是 leaf 包）。
	// 155/155：P9a 删掉 Go 侧 Meta-Agent、把生成职责交给 plasma 之后，
	//   ① internal/app 首次 import plasma（装配 trampoline 与启动期 workdir GC，app/plasma.go）；
	//   ② internal/agents 首次 import embedbin（plasma 可用性判据要定位 plasma.pyz，
	//      plasma_gate.go 与 spawner 读同一个落盘路径）。
	// 两条都是**新增依赖**而不是搬家，逐条用 go list 确认过来源。
	// 153/153：P7 的 `yagent plasma` CLI 落在根包（cmd_plasma{,_run,_render}.go），
	// 于是根包首次 import plasma 与 toolspec（两条新边）。根包不是叶子，不影响叶子性。
	// 151/151：P2 让 plasma 再 import thinklink（复用 ThoughtPlan.Validate，不重写 thought 契约）
	// 与 tokenutil（阶段 4 的预算必须与压缩阈值同一个计数器），两条新边。两者都已实测是叶子包。
	// 上一版 149/149：P1 让 agents 首次 import plasma（工具池广告与准入同改用 PlasmaPalette）。
	// 148/148：新增 internal/plasma（P0）带来 plasma→toolspec、plasma→util、agents→util
	//（`toSnakeCase` 单源化成 `util.SnakeCase`）。更新前用 go list -f '{{.Imports}}' 逐条确认过。
	// 更早 145/145：求助与授权从弹窗改成 footer 面板，components/user_help_dialog.go 随之删除。
	if out != 158 || in != 158 {
		t.Fatalf("模块级边数 out=%d in=%d, golden 是 158/158 —— 形状变了，先确认变化来源", out, in)
	}
	for i := 1; i < len(snap.Leaves); i++ {
		if snap.Leaves[i-1].Dir >= snap.Leaves[i].Dir {
			t.Fatalf("Leaves 未按 Dir 排序: %q >= %q", snap.Leaves[i-1].Dir, snap.Leaves[i].Dir)
		}
	}
	var found bool
	for i := range snap.Leaves {
		l := &snap.Leaves[i]
		if l.Dir == "codeseek" {
			found = true
			if !l.Submodule || len(l.Files) != 0 {
				t.Fatalf("codeseek 应是整片 submodule leaf: sub=%v files=%d", l.Submodule, len(l.Files))
			}
		}
	}
	if !found {
		t.Fatal("codeseek submodule 未成为整片 leaf —— 目录分支跳过失效")
	}
	for i := range snap.Leaves {
		dir := snap.Leaves[i].Dir
		for _, seg := range strings.Split(dir, "/") {
			if DefaultBlacklistSet()[seg] {
				t.Fatalf("片路径 %q 含黑名单段 %q", dir, seg)
			}
		}
		if strings.HasPrefix(dir, "internal/blueprint/testdata") {
			t.Fatalf("fixture 目录 %q 不得进本仓实测（黑名单失效会污染 golden）", dir)
		}
	}
	for i := range snap.Leaves {
		if len(snap.Leaves[i].Fingerprint) != 64 {
			t.Fatalf("片 %q 指纹长度 %d, 要 64", snap.Leaves[i].Dir, len(snap.Leaves[i].Fingerprint))
		}
	}
	again, err := Scan("../..", DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snap, again) {
		t.Fatal("本仓两次自扫结果不一致 —— 违反确定性（I15 前置）")
	}
}

func DefaultBlacklistSet() map[string]bool {
	set := make(map[string]bool, len(DefaultBlacklist))
	for _, name := range DefaultBlacklist {
		set[name] = true
	}
	return set
}
