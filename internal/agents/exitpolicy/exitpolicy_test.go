package exitpolicy

import (
	"testing"

	"yagent/internal/toolspec"
)

// TestExitToolNameIsTheSingleLiteralSource 钉住「唯一的字面量来源」这件事本身。
// 常量值一旦改掉，两个循环都会安静地不再因 agent_exit 收尾（判据永远不成立），
// 而表现只是「循环烧到 MaxSteps」——所以这一条必须单独红，而不是留给下游测试去猜。
func TestExitToolNameIsTheSingleLiteralSource(t *testing.T) {
	if toolspec.ExitToolName != "agent_exit" {
		t.Errorf("toolspec.ExitToolName = %q, want %q（模型与 prompt 认的是这个名字）", toolspec.ExitToolName, "agent_exit")
	}
}

// nonExitNames 是判据必须**不**认的名字：空串、别的工具、以及前缀/大小写变体。
// 后两类专门钉「全等比较」：注册表里多一个 agent_exit_v2 时，前缀匹配会静默改变循环行为。
var nonExitNames = []string{
	"", "read_file", "run_bash", "delegate_repo",
	"Agent_Exit", "agent_exit ", " agent_exit", "agent_exit_v2", "agentEx",
}

// TestTerminateOnAgentExitFullTable 覆盖 (stopOnFinish × 名字对上 × 执行失败) 的全部 8 格。
// 每一格单独断言：只测「终止」那一格的话，把判据写成 `return stopOnFinish`（忽略名字与成败）
// 也能全绿 —— 那正是本批要消除的漂移形态，所以三个缺格必须各有自己的失败信号。
func TestTerminateOnAgentExitFullTable(t *testing.T) {
	cases := []struct {
		name         string
		stopOnFinish bool
		toolName     string
		execFailed   bool
		want         bool
		why          string
	}{
		{
			name:         "唯一终止格：开关开 + 名字对上 + 执行成功",
			stopOnFinish: true, toolName: toolspec.ExitToolName, execFailed: false,
			want: true, why: "正常收尾",
		},
		{
			name:         "开关关：名字对上也不许终止",
			stopOnFinish: false, toolName: toolspec.ExitToolName, execFailed: false,
			want: false, why: "StopOnFinish=false 的 Agent 不因 agent_exit 停（装配点见 internal/agents/AGENTS.md 硬约束 2）",
		},
		{
			name:         "执行失败：开关开也不许终止",
			stopOnFinish: true, toolName: toolspec.ExitToolName, execFailed: true,
			want: false, why: "失败那次的回执交回模型继续跑，反复失败由既有 MaxSteps 兜底",
		},
		{
			name:         "开关关 + 执行失败：更不许终止",
			stopOnFinish: false, toolName: toolspec.ExitToolName, execFailed: true,
			want: false, why: "两个否定条件同时成立",
		},
		{
			name:         "别的工具：开关开 + 执行成功也不许终止",
			stopOnFinish: true, toolName: "read_file", execFailed: false,
			want: false, why: "只有收尾工具能终止循环",
		},
		{
			name:         "别的工具执行失败：不许终止",
			stopOnFinish: true, toolName: "run_bash", execFailed: true,
			want: false, why: "普通工具失败照常把 Error 回执交回模型",
		},
		{
			name:         "委派工具成功：不许终止",
			stopOnFinish: false, toolName: "delegate_repo", execFailed: false,
			want: false, why: "一次委派完成不是整个 Agent 的交付完成",
		},
		{
			name:         "空名字：不许终止",
			stopOnFinish: true, toolName: "", execFailed: false,
			want: false, why: "畸形回执里的空工具名不能冒领收尾",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := TerminateOnAgentExit(tc.stopOnFinish, tc.toolName, tc.execFailed)
			if got != tc.want {
				t.Errorf("TerminateOnAgentExit(%v, %q, %v) = %v, want %v（%s）",
					tc.stopOnFinish, tc.toolName, tc.execFailed, got, tc.want, tc.why)
			}
		})
	}
}

// TestTerminateEqualsTerminateOnAgentExitWithSwitchOn 钉住两个函数的关系：
// Terminate 只是 stopOnFinish 恒为真的**等价形式**，不是第二套判据。
// 有人给其中一方单独加条件时，Director 与子 Agent 就会在这里重新分叉 —— 本批要消除的正是这种分叉。
func TestTerminateEqualsTerminateOnAgentExitWithSwitchOn(t *testing.T) {
	names := append([]string{toolspec.ExitToolName}, nonExitNames...)
	for _, name := range names {
		for _, failed := range []bool{false, true} {
			got := Terminate(name, failed)
			want := TerminateOnAgentExit(true, name, failed)
			if got != want {
				t.Errorf("Terminate(%q, %v) = %v, want %v：与 TerminateOnAgentExit(true,…) 必须逐格相同",
					name, failed, got, want)
			}
			// 反向不变量：开关关掉之后 TerminateOnAgentExit 必须比 Terminate 更保守（永不多停）。
			if TerminateOnAgentExit(false, name, failed) {
				t.Errorf("TerminateOnAgentExit(false, %q, %v) = true, want false：开关关一律不终止", name, failed)
			}
		}
	}
}

// TestTerminationIsExactNameMatch 钉住「全等比较」：判据不许写成前缀/包含匹配。
// 那种写法今天的行为差异看不出来，等注册表里出现 agent_exit_* 之类的兄弟工具时，
// 循环会在错误的时机停下 —— 与根 AGENTS.md §11 坑 33 同一类「测试全绿、生产静默失效」。
func TestTerminationIsExactNameMatch(t *testing.T) {
	for _, name := range nonExitNames {
		if name == "" || name == toolspec.ExitToolName {
			continue
		}
		if Terminate(name, false) {
			t.Errorf("Terminate(%q, false) = true, want false：只有全等于 toolspec.ExitToolName 才算收尾", name)
		}
		if TerminateOnAgentExit(true, name, false) {
			t.Errorf("TerminateOnAgentExit(true, %q, false) = true, want false：同上", name)
		}
	}
}
