package scripted

import (
	"context"
	"strings"
	"testing"

	"yagent/internal/llm"
)

func sysMsg(s string) []llm.Message { return []llm.Message{{Role: llm.RoleSystem, Content: s}} }

func TestEngine_DispatchesByIdentityNotCallOrder(t *testing.T) {
	e := New(map[string][]Step{
		"director": {Fixed(TextResponse("from-director"))},
		"repo":     {Fixed(TextResponse("from-repo"))},
	})
	ctx := context.Background()
	// 先 repo 后 director：全局序号会错位，按身份不会。
	got1, _ := e.GenerateContent(ctx, sysMsg("You are the Repo-Agent. x"), nil, nil)
	got2, _ := e.GenerateContent(ctx, sysMsg("You are the **Director**. x"), nil, nil)
	if got1.Choices[0].Content != "from-repo" || got2.Choices[0].Content != "from-director" {
		t.Errorf("分派错位: repo=%q director=%q", got1.Choices[0].Content, got2.Choices[0].Content)
	}
}

func TestEngine_HonoursContext(t *testing.T) {
	e := New(nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.GenerateContent(ctx, sysMsg("x"), nil, nil); err == nil {
		t.Fatal("已取消的 ctx 必须返回错误（忽略 ctx 会掩盖取消没传播）")
	}
}

// 脚本必须能在锁外回看引擎记录，否则 sync.Mutex 不可重入会自锁死。
func TestEngine_StepRunsOutsideLock(t *testing.T) {
	var e *Engine
	e = NewWithResolver(func([]llm.Message) string { return "a" }, map[string][]Step{
		"a": {func(msgs []llm.Message, _ []llm.ToolDef) *llm.Response {
			// 在步骤里回看记录：锁内执行会在这里死锁。
			_ = e.CallsFor("a")
			return TextResponse("ok")
		}},
	})
	if _, err := e.GenerateContent(context.Background(), sysMsg("x"), nil, nil); err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}
	if len(e.CallsFor("a")) != 1 {
		t.Errorf("调用记录条数 = %d", len(e.CallsFor("a")))
	}
}

// 固定身份：生成式 Agent 的 prompt 首行不可预测，嗅探必然落 unknown。
func TestEngine_FixedIdentity(t *testing.T) {
	e := NewForIdentity("auditor", []Step{Fixed(TextResponse("fixed"))})
	got, _ := e.GenerateContent(context.Background(), sysMsg("完全认不出的 prompt"), nil, nil)
	if got.Choices[0].Content != "fixed" {
		t.Errorf("固定身份没生效: %q", got.Choices[0].Content)
	}
}

// AgentName 归一化后命中脚本时优先于嗅探（CallOptions.AgentName 的用途）。
func TestEngine_AgentNameBeatsSniffing(t *testing.T) {
	e := New(map[string][]Step{
		"repo": {Fixed(TextResponse("by-name"))},
	})
	// prompt 会嗅探成 director，但 AgentName 是 Repo-Agent → 归一化命中 "repo"
	got, _ := e.GenerateContent(context.Background(),
		sysMsg("You are the **Director**. x"), nil, &llm.CallOptions{AgentName: "Repo-Agent"})
	if got.Choices[0].Content != "by-name" {
		t.Errorf("AgentName 没压过嗅探: %q", got.Choices[0].Content)
	}
	// 没有对应脚本时回到嗅探，不该把调用扔进空脚本。
	e2 := New(map[string][]Step{"director": {Fixed(TextResponse("by-sniff"))}})
	got2, _ := e2.GenerateContent(context.Background(),
		sysMsg("You are the **Director**. x"), nil, &llm.CallOptions{AgentName: "DevOps-Agent"})
	if got2.Choices[0].Content != "by-sniff" {
		t.Errorf("AgentName 没有脚本时应回落嗅探: %q", got2.Choices[0].Content)
	}
}

func TestEngine_ExhaustFallsBackToPlainText(t *testing.T) {
	ctx := context.Background()
	e := NewForIdentity("chat", []Step{Fixed(TextResponse("first"))})
	_, _ = e.GenerateContent(ctx, sysMsg("x"), nil, nil)
	second, err := e.GenerateContent(ctx, sysMsg("x"), nil, nil)
	if err != nil {
		t.Fatalf("脚本耗尽不该返回 error（会触发重试并放大错位）: %v", err)
	}
	if !strings.Contains(second.Choices[0].Content, "脚本已用尽") {
		t.Errorf("耗尽回落文案 = %q", second.Choices[0].Content)
	}
}

func TestToolCallIDFormatIsStable(t *testing.T) {
	step := ToolCall("read_file", map[string]interface{}{"target_file": "a.go"})
	resp := step(nil, nil)
	if len(resp.Choices) != 1 || len(resp.Choices[0].ToolCalls) != 1 {
		t.Fatalf("ToolCall 形状错: %+v", resp)
	}
	tc := resp.Choices[0].ToolCalls[0]
	if tc.ID != "call_read_file" {
		t.Errorf("ID = %q, want call_read_file（既有配对修复依赖这个格式）", tc.ID)
	}
	if tc.Function.Name != "read_file" || !strings.Contains(tc.Function.Arguments, "target_file") {
		t.Errorf("调用内容错: %+v", tc)
	}
}

func TestNormalizeIdentity(t *testing.T) {
	cases := map[string]string{
		"Director": "director", "Repo-Agent": "repo", "Coding-Agent": "coding",
		"Chat-Agent": "chat", "  DevOps-Agent ": "devops", "Browser-Agent": "browser",
	}
	for in, want := range cases {
		if got := NormalizeIdentity(in); got != want {
			t.Errorf("NormalizeIdentity(%q) = %q, want %q", in, got, want)
		}
	}
}
