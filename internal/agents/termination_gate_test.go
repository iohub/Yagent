package agents

import (
	"os"
	"strings"
	"testing"
)

// TestTerminationDecisionIsNotInlinedInLoops 是「终止判据单源化」的防漂移闸门。
//
// 它守的不是行为而是**形状**：两个循环里不许再出现任何按收尾工具名的内联比较。
// 为什么行为测试不够——executor_agent_exit_test.go 与 director/planner_agent_exit_test.go
// 各自钉住了自己那一侧的语义，于是「把判据复制回循环里、两份都对」的改动照样全绿；
// 而漂移恰恰发生在下一次只改一边的时候（根 AGENTS.md §11 坑 33、internal/agents/AGENTS.md 硬约束 1/2）。
// 这条闸门读源码，所以「改回内联」这一步本身就会红。
//
// 扫描的文件是硬编码的两条相对路径：读不到就 Fatal —— 文件改名/搬走同样会让这条闸门
// 静默失效，而一条常绿的守卫等于没有守卫。
func TestTerminationDecisionIsNotInlinedInLoops(t *testing.T) {
	// 字面量与常量都要禁：用 toolspec.ExitToolName 自己拼判据同样是绕开 exitpolicy 的
	// 第二份实现，只是这次漂走的是别的条件（执行成败 / StopOnFinish 开关）。
	forbidden := []string{
		`== "agent_exit"`,
		`!= "agent_exit"`,
		`== toolspec.ExitToolName`,
		`!= toolspec.ExitToolName`,
		`case "agent_exit"`,
	}
	for _, f := range []struct {
		path string
		want string // 该侧应当调用的 exitpolicy 入口
	}{
		{"executor.go", "exitpolicy.TerminateOnAgentExit"},
		{"director/planner_run.go", "exitpolicy.Terminate"},
	} {
		src, err := os.ReadFile(f.path)
		if err != nil {
			t.Fatalf("读取 %s 失败，终止判据闸门无法生效（文件改名/搬走请同步本用例）: %v", f.path, err)
		}
		text := string(src)
		for _, bad := range forbidden {
			if strings.Contains(text, bad) {
				t.Errorf("%s 里出现内联的收尾工具名比较 %q：终止判据必须走 exitpolicy，"+
					"不要在两个循环里各写一份（两份同形实现必有一边漂走，见 internal/agents/AGENTS.md 硬约束 2）",
					f.path, bad)
			}
		}
		if !strings.Contains(text, f.want) {
			t.Errorf("%s 没有调用 %s：终止判据的单源实现被绕开了", f.path, f.want)
		}
	}
}
