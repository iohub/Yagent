package agents

import (
	"strings"
	"testing"
)

// 本文件钉住内建 prompt 导出的两件事：
//  1. 六个 Agent 都能取到**非空全文**，且 Text 与对应 embed 变量逐字节相同
//     （browser 的变量叫 browserPrompt、文件名是 browser.prompt.md —— 映射错位
//     不会编译报错，只会让提案改错文件）；
//  2. PromptAgents 排序且覆盖全部，未知名字返回 ok=false。
func TestLookupPromptCoversBuiltinAgents(t *testing.T) {
	cases := []struct {
		agent string
		path  string
		text  string
	}{
		{"coding", "internal/agents/coding.prompt.md", codingPrompt},
		{"repo", "internal/agents/repo.prompt.md", repoPrompt},
		{"devops", "internal/agents/devops.prompt.md", devopsPrompt},
		{"browser", "internal/agents/browser.prompt.md", browserPrompt},
		{"chat", "internal/agents/chat.prompt.md", chatPrompt},
		{"director", "internal/agents/director.prompt.md", directorPrompt},
	}
	for _, tc := range cases {
		info, ok := LookupPrompt(tc.agent)
		if !ok {
			t.Errorf("LookupPrompt(%q) 失败：这个内建 Agent 取不到 prompt", tc.agent)
			continue
		}
		if info.Agent != tc.agent {
			t.Errorf("LookupPrompt(%q).Agent = %q", tc.agent, info.Agent)
		}
		if info.Path != tc.path {
			t.Errorf("LookupPrompt(%q).Path = %q，want %q", tc.agent, info.Path, tc.path)
		}
		if strings.TrimSpace(info.Text) == "" {
			t.Errorf("LookupPrompt(%q).Text 为空", tc.agent)
		}
		if info.Text != tc.text {
			t.Errorf("LookupPrompt(%q).Text 与对应 embed 变量不一致（映射错位？）", tc.agent)
		}
	}

	// 大小写与空白不该改变判据（命令行上很容易多打一个空格）。
	if info, ok := LookupPrompt("  Coding  "); !ok || info.Agent != "coding" {
		t.Errorf("LookupPrompt(\"  Coding  \") = %+v, %v", info, ok)
	}
	if _, ok := LookupPrompt("nope"); ok {
		t.Error("未知 agent 返回了 ok=true")
	}
}

func TestPromptAgentsIsSortedAndComplete(t *testing.T) {
	got := PromptAgents()
	if len(got) != 6 {
		t.Fatalf("PromptAgents() 长度 = %d，want 6：%v", len(got), got)
	}
	for i := 1; i < len(got); i++ {
		if got[i-1] >= got[i] {
			t.Errorf("PromptAgents() 没有排序：%v", got)
			break
		}
	}
	// 返回的是副本：调用方改它不该影响下一次查询。
	got[0] = "mutated"
	if again := PromptAgents(); again[0] == "mutated" {
		t.Error("PromptAgents() 返回了内部切片，调用方能改到清单")
	}
}
