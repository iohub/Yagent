package agents

// parallel_admission_test.go — 并行准入的**生产载体**契约（设计稿 R1 的最后一块）。
//
// R1 之所以能长期存在，不是因为判据写错，而是因为测试与生产喂给它的不是同一个东西：
// 测试往 cfg.Adapters 里塞夹具、生产只填 Surface。所以这里刻意不自己拼工具列表，
// 而是取真实的 Agent 画像（ProfileRepo / ProfileCoding）装配工具面来问判据 ——
// 画像或能力标签漂走时，这条测试会红，而那正是"生产到底有没有并行"的问题。

import (
	"testing"

	"yagent/internal/tools"
	"yagent/internal/toolspec"
)

func TestParallelAdmission_ReadOnlyToolsOnProductionRepoSurface(t *testing.T) {
	s := surfaceFor(toolspec.ProfileRepo, toolspec.ProfileRepo.ProbeDeps(), "Repo")

	for _, name := range []string{"read_file", "search_by_regex", "list_dir", "print_dir_tree"} {
		if _, state := s.Lookup(name); state == tools.StateUnknown {
			t.Fatalf("Repo 画像下 %s 不存在 —— 画像变了，这条测试的前提要先更新", name)
		}
		if !toolCallIsParallelizable(s, name) {
			t.Errorf("%s 在生产 Repo 画像下应可并行 —— 这正是 R1 里被静默关掉的那一类", name)
		}
	}

	// 流程控制永远独占：交互式要等用户，agent_exit 的收尾必须按原序提交。
	for _, name := range []string{"agent_exit", "ask_user_for_help"} {
		if toolCallIsParallelizable(s, name) {
			t.Errorf("%s 不该进并行批次", name)
		}
	}
	// Repo 的 delegate_devops 是委派，Coding-Agent 侧的委派由 Mutates/Serial 挡在门外；
	// 这条断言钉的是"委派不会被顺手并行化"这个边界，不是实现细节。
	if toolCallIsParallelizable(s, "delegate_devops") {
		t.Error("delegate_devops 被判可并行 —— 写类委派必须继续串行")
	}
}

func TestParallelAdmission_WriteToolsStaySerial(t *testing.T) {
	s := surfaceFor(toolspec.ProfileCoding, toolspec.ProfileCoding.ProbeDeps(), "Coding")

	for _, name := range []string{"create_file", "search_replace_in_file", "delete_file", "rename_file", "run_bash"} {
		if _, state := s.Lookup(name); state == tools.StateUnknown {
			t.Fatalf("Coding 画像下 %s 不存在 —— 画像变了，先更新这条测试的前提", name)
		}
		if toolCallIsParallelizable(s, name) {
			t.Errorf("%s 被判可并行 —— 写工具进并行批次就是竞态", name)
		}
	}
	// 读的一侧照常放行，否则这条测试等于"全都串行"也能过。
	if !toolCallIsParallelizable(s, "read_file") {
		t.Error("Coding 画像下 read_file 应可并行")
	}
}

func TestParallelAdmission_EdgeCases(t *testing.T) {
	s := surfaceFor(toolspec.ProfileChat, toolspec.ProfileChat.ProbeDeps(), "Chat")

	if toolCallIsParallelizable(s, "totally_made_up") {
		t.Error("查不到的名字被判可并行 —— 那会给一个不存在的工具开 goroutine")
	}
	if toolCallIsParallelizable(nil, "read_file") {
		t.Error("没有工具面时不该报可并行")
	}
}
