package blueprint

import (
	"reflect"
	"strings"
	"testing"
)

// 本仓自扫 golden（§19 附录：R2 的测量要变成仓库里的 golden 测试；
// v3 那个"14 片假模块"的 bug 就是靠这条形态的断言抓的）。
//
// 以下数字是 R2 的权威实测值（首次测定于 2026-10-18，HEAD 含 R1+R2 全部源文件）：
//
//	45 片、141 条模块级出边（入边转置后同为 141）、
//	模块路径 "yagent"、internal/blueprint/testdata 被黑名单挡住不进实测。
//
// 仓库形状一变这些数字就该变 —— 变化本身是信号，不是测试腐烂：
// 更新数字前必须先重跑一遍并确认变化来源（新包？submodule 跳过失效？），
// 不许静默改数字。设计稿 §19 的近似值（103 条模块级边）由此替代。
//
// 2026-10-19 R3 修订：45 片不变、边数 141 → 144。逐条对过来源，三条都是新接的边：
//
//	internal/agents → internal/blueprint（ports.go 的 BlueprintSource + blueprint_view.go）
//	internal/app    → internal/tokenutil （注入侧的 token 计数器）
//	internal/app    → internal/gitinfo   （缓存模式的 HEAD 失效判据）
//
// 没有新增目录、也没有哪条黑名单失效 —— 片数那一条同时证明这一点。
//
// 2026-10-22 修订：45 → 44 片、144 → 134 条边。来源是"知识整理机制整体删除"
// （KV 仓库记忆 + codeseek 知识注入/沉淀），仓库级持久知识只剩蓝图这一个 sink。
// 十条边逐条对过：
//
//	internal/knowledge 片消失，带走它自己的 3 条出边（→logging /logging、→mcp、→tokenutil）
//	internal/agents    → internal/knowledge / internal/mcp / internal/supervise（各 −1：
//	                   consolidation_worker.go 与 knowledge_hook.go 是 agents 根包唯一直接用这三家的）
//	internal/app       → internal/knowledge
//	internal/globalctx → internal/knowledge
//	internal/tools     → internal/logging（knowledge.go 是 tools 里唯一用它的）
//	internal/memory    → internal/util   （shared.go 的原子写；local.go/shared.go 一并删除）
//
// 2026-10-22 第二轮修订：片数不变，边数 134 → 135。来源是启动期注册表自检：
//
//	internal/app → internal/toolspec（app.Init 调 toolspec.Validate，让 spec.go
//	              "装配期调用"那句注释第一次成为真话）
//
// 2026-10-23 第三轮修订：片数不变，边数 135 → 136。来源是 MCP 状态事件：
//
//	internal/tui → internal/mcp（mcp_server_status 的事件行复用 mcp.FormatServerLine，
//	              避免同一件事在界面与状态账里各说一套）
//
// 2026-10-23 第四轮修订：片数不变，边数 136 → 137。来源是斜杠补全的类型化：
//
//	internal/tui → internal/skills（skillCompletionItems 读注册表拿技能名与描述）
//
// 2026-10-23 第五轮修订：44 → 43 片、边数不变（137）。来源是删掉 `internal/tui/anim` 整包：
// 全仓零 import、零测试文件，包内 15 个声明没有一条被调用过（`internal/tui/setup.go` 与
// `setup_screens.go` 的注释就是当时拒绝接线的记录）。它只 import 标准库与 bubbletea，
// 也没有人 import 它，所以边的账不动，只有片数 −1。
//
// 2026-10-26 第六轮修订：片数不变，边数 137 → 138。来源是 run_bash 卡死的修复
// （docs/run-bash-hang-fix.md）：browserctl 的 cappedBuffer 上移成 `internal/util.CappedBuffer`，
// 于是多出一条以前不存在的模块级边 —— 两条同形实现合成一条，账上就是 +1 条依赖、−0 片。
//
//	internal/browserctl → internal/util
//
// 2026-10-26 第七轮修订：43 → 44 片、138 → 143 边。来源是同一份方案里的 P1-a
// （信号 → ctx 取消）：新增 `internal/shutdown` 一片，五条边逐条对过。
//
//	internal/shutdown → internal/supervise（本包那条看门狗 goroutine，全仓 goroutine 只能经 supervise 起）
//	internal/http     → internal/shutdown（NewTaskContext 的根从 Background 换成 shutdown.Root()）
//	internal/tui      → internal/shutdown（取消后重建的任务 ctx 同一个根）
//	.                 → internal/shutdown（main 三个入口装处理器）
//	.                 → internal/tools（main 把 KillLiveProcessGroups 传给硬退出钩子）
//
// 2026-10-28 第八轮修订：44 → 45 片、143 → 146 边。来源是 B2「终止判据单源化」：
// 新增 `internal/agents/exitpolicy` 一片（两个循环共用的判定纯函数），三条边逐条对过。
//
//	internal/agents/exitpolicy → internal/toolspec（唯一的收尾工具名来源 ExitToolName）
//	internal/agents            → internal/agents/exitpolicy（executor.go 的子 Agent 侧调用点）
//	internal/agents/director   → internal/agents/exitpolicy（planner_run.go 的 Director 侧调用点；
//	                           整行匹配的依赖守卫允许 import 该子包，与 toolbatch/activity 同形）
func TestScanSelfGolden(t *testing.T) {
	snap, err := Scan("../..", DefaultOptions()) // 测试 CWD 是 internal/blueprint
	if err != nil {
		t.Fatalf("本仓自扫失败: %v", err)
	}
	if snap.ModulePath != "yagent" {
		t.Fatalf("ModulePath = %q, 要 %q", snap.ModulePath, "yagent")
	}
	// golden 1：片数。
	if len(snap.Leaves) != 45 {
		t.Fatalf("片数 = %d, golden 是 45 —— 形状变了，先确认变化来源再更新数字", len(snap.Leaves))
	}
	// golden 2：模块级边数（out 与 in 是转置，总数必然相等）。
	out, in := 0, 0
	for i := range snap.Leaves {
		out += snap.Leaves[i].EdgesOutTotal
		in += snap.Leaves[i].EdgesInTotal
	}
	if out != 146 || in != 146 {
		t.Fatalf("模块级边数 out=%d in=%d, golden 是 146/146 —— 形状变了，先确认变化来源", out, in)
	}
	// 排序不变量：Leaves 按 Dir 排序（确定性的一部分）。
	for i := 1; i < len(snap.Leaves); i++ {
		if snap.Leaves[i-1].Dir >= snap.Leaves[i].Dir {
			t.Fatalf("Leaves 未按 Dir 排序: %q >= %q", snap.Leaves[i-1].Dir, snap.Leaves[i].Dir)
		}
	}
	// submodule leaf：本仓含 codeseek submodule，目录分支命中即整片、不递归。
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
	// 黑名单负样本：任何片路径不得含黑名单段；testdata（本 fixture 所在）不得进实测。
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
	// 指纹不变量：每片 64 hex。
	for i := range snap.Leaves {
		if len(snap.Leaves[i].Fingerprint) != 64 {
			t.Fatalf("片 %q 指纹长度 %d, 要 64", snap.Leaves[i].Dir, len(snap.Leaves[i].Fingerprint))
		}
	}
	// 确定性：同一 HEAD 两次扫描 DeepEqual。
	again, err := Scan("../..", DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snap, again) {
		t.Fatal("本仓两次自扫结果不一致 —— 违反确定性（I15 前置）")
	}
}

// DefaultBlacklistSet 是黑名单的集合形态（测试用；与 DefaultBlacklist 同源）。
func DefaultBlacklistSet() map[string]bool {
	set := make(map[string]bool, len(DefaultBlacklist))
	for _, name := range DefaultBlacklist {
		set[name] = true
	}
	return set
}
