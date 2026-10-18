package agents

// spawn_test.go — C4 的闸门（docs/structured-concurrency-design.md D2 / D5）。
//
// 这两条不变量各自对应一个"错了不报错"的失败方式：
//   D2 共享实例  → 两次委派共用一份 thinkLink，第三档压缩的重建原料里混着上一次的任务；
//   D5 全局单槽  → 并行委派时先完成的一方 take 到别人的结果，还以自己的 callID 写进记忆。
// 所以断言都盯着"归属"而不是"有没有崩"。

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"yagent/internal/memory"
	"yagent/internal/thinklink"
)

func TestSpawnForDelegation_CodingGetsItsOwnThinkLink(t *testing.T) {
	shared := &CodingAgent{thinkLink: thinklink.NewStore(0)}

	a := shared.SpawnForDelegation(context.Background())
	b := shared.SpawnForDelegation(context.Background())

	if a == shared || b == shared {
		t.Fatal("Spawn 返回了单例本身 —— 隔离是假的")
	}
	if a == b {
		t.Fatal("两次委派拿到了同一个实例")
	}

	a.thinkLink.AddUserInput("任务 A：读文件", 1)
	b.thinkLink.AddUserInput("任务 B：跑测试", 1)

	promptA := a.thinkLink.RebuildPrompt(0)
	if !strings.Contains(promptA, "任务 A") || strings.Contains(promptA, "任务 B") {
		t.Errorf("A 的重建原料 = %q, 只想看见 A —— 这就是原来挂在单例上的串味", promptA)
	}
	if got := shared.thinkLink.Len(); got != 0 {
		t.Errorf("单例的 store 被写了 %d 条 —— 委派状态漏回了共享实例", got)
	}
}

func TestSpawnForDelegation_AllFiveAreDistinctAndNilSafe(t *testing.T) {
	ctx := context.Background()

	repo := &RepoAgent{}
	if got := repo.SpawnForDelegation(ctx); got == repo || got == nil {
		t.Error("RepoAgent.Spawn 必须返回一个新实例")
	}
	chat := &ChatAgent{}
	if got := chat.SpawnForDelegation(ctx); got == chat || got == nil {
		t.Error("ChatAgent.Spawn 必须返回一个新实例")
	}
	devops := &DevOpsAgent{}
	if got := devops.SpawnForDelegation(ctx); got == devops || got == nil {
		t.Error("DevOpsAgent.Spawn 必须返回一个新实例")
	}
	browser := &BrowserAgent{}
	if got := browser.SpawnForDelegation(ctx); got == browser || got == nil {
		t.Error("BrowserAgent.Spawn 必须返回一个新实例")
	}
	coding := &CodingAgent{thinkLink: thinklink.NewStore(0)}
	if got := coding.SpawnForDelegation(ctx); got == coding || got == nil {
		t.Error("CodingAgent.Spawn 必须返回一个新实例")
	}

	// 未装配的路径必须退回 nil，让调用方去报"这个子 Agent 没接线"，而不是 panic。
	var nilRepo *RepoAgent
	if got := nilRepo.SpawnForDelegation(ctx); got != nil {
		t.Error("nil 接收者的 Spawn 应返回 nil")
	}
	var nilCoding *CodingAgent
	if got := nilCoding.SpawnForDelegation(ctx); got != nil {
		t.Error("nil 接收者的 Spawn 应返回 nil")
	}
}

func TestDelegateTargetsSpawnAFreshInstancePerCall(t *testing.T) {
	agent := &DirectorAgent{
		RepoAgent:    &RepoAgent{},
		CodingAgent:  &CodingAgent{thinkLink: thinklink.NewStore(0)},
		ChatAgent:    &ChatAgent{},
		DevOpsAgent:  &DevOpsAgent{},
		BrowserAgent: &BrowserAgent{},
	}

	for _, target := range []string{"repo", "coding", "chat", "devops", "browser"} {
		spec, ok := agent.delegateTargets()[target]
		if !ok {
			t.Fatalf("委派目标 %s 不在表里", target)
		}
		first, second := spec.spawn(context.Background()), spec.spawn(context.Background())
		if first == nil || second == nil {
			t.Errorf("%s: spawn 返回 nil（已装配的情况下）", target)
			continue
		}
		if first == second {
			t.Errorf("%s: 两次委派拿到同一个实例 —— 共享自身状态就是竞态", target)
		}
	}

	// 一个都没装配的 Director：spawn 必须给 nil，delegateTo 才有"not wired"那条路径。
	bare := &DirectorAgent{}
	for _, target := range []string{"repo", "coding", "chat", "devops", "browser"} {
		if got := bare.delegateTargets()[target].spawn(context.Background()); got != nil {
			t.Errorf("%s: 未装配时应返回 nil", target)
		}
	}
}

// 载体必须是**每次调用各一个**：这是原来那个 Director 级单槽会串味的地方。
func TestDelegationOutcomeIsPerCall(t *testing.T) {
	ctxA, outA := withDelegationOutcome(context.Background())
	ctxB, outB := withDelegationOutcome(context.Background())

	var wg sync.WaitGroup
	for i, pair := range []struct {
		ctx  context.Context
		text string
	}{
		{ctxA, "A 的结论"},
		{ctxB, "B 的结论"},
	} {
		wg.Add(1)
		go func(i int, ctx context.Context, text string) {
			defer wg.Done()
			recordDelegationOutcome(ctx, AgentResult{Text: text})
		}(i, pair.ctx, pair.text)
	}
	wg.Wait()

	if !outA.has || outA.result.Text != "A 的结论" {
		t.Errorf("A 的载体 = %+v, want 'A 的结论'", outA.result)
	}
	if !outB.has || outB.result.Text != "B 的结论" {
		t.Errorf("B 的载体 = %+v, want 'B 的结论'", outB.result)
	}

	// 没有载体的 ctx 上记录必须是 no-op，而不是 panic 或吞掉一次委派。
	recordDelegationOutcome(context.Background(), AgentResult{Text: "没人认领"})

	// 载体不存在时读出来就是 has=false（runner 据此跳过注入）
	_, empty := withDelegationOutcome(context.Background())
	if empty.has {
		t.Error("新建的载体不该已经带上结果")
	}
}

// 注入必须挂在自己的 callID 上 —— ParentID 是"这条摘要来自哪一次委派"的唯一答案。
func TestInjectSubAgentMemoryUsesTheGivenCallID(t *testing.T) {
	mem := memory.NewConversationMemory(200)
	agent := &DirectorAgent{currentMemory: mem}

	agent.injectSubAgentMemory(AgentResult{Text: "repo 的结论"}, "call_42", "delegate_repo")

	msgs := mem.Snapshot()
	if len(msgs) != 1 {
		t.Fatalf("注入了 %d 条, want 1", len(msgs))
	}
	if msgs[0].ParentID != "call_42" {
		t.Errorf("ParentID = %q, want call_42", msgs[0].ParentID)
	}
	if !msgs[0].IsSubAgent {
		t.Error("摘要必须标 IsSubAgent，否则 ToMessages 会把它发给模型")
	}

	// 没接记忆（nil）时静默跳过，不该 panic。
	(&DirectorAgent{}).injectSubAgentMemory(AgentResult{Text: "x"}, "c1", "delegate_repo")
}

// 十次"并发注入"各挂各的 callID：单槽实现在这里必然错配（take 拿到最后写入的那份）。
func TestConcurrentInjectsKeepTheirOwnCallIDs(t *testing.T) {
	const n = 20
	mem := memory.NewConversationMemory(500)
	agent := &DirectorAgent{currentMemory: mem}

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			callID := fmt.Sprintf("call_%02d", i)
			agent.injectSubAgentMemory(AgentResult{Text: callID}, callID, "delegate_repo")
		}(i)
	}
	wg.Wait()

	msgs := mem.Snapshot()
	if len(msgs) != n {
		t.Fatalf("注入了 %d 条, want %d", len(msgs), n)
	}
	seen := map[string]bool{}
	for _, m := range msgs {
		if m.ParentID == "" {
			t.Errorf("一条摘要没有 ParentID：%q", m.Content)
			continue
		}
		if seen[m.ParentID] {
			t.Errorf("ParentID %q 出现了两次 —— 单槽串味的形状", m.ParentID)
		}
		seen[m.ParentID] = true
	}
	if len(seen) != n {
		t.Errorf("只有 %d 个不同的 callID 落进了记忆, want %d", len(seen), n)
	}
}

// 未装配的子 Agent 必须走"not wired"那条友好错误，而不是 nil 解引用。
// 原实现的 spawn 返回"装着 nil 的 interface"，所以 delegateTo 里的 `agent == nil`
// 永远不成立 —— 这条测试就是为了把这个洞钉在原地。
func TestDelegateTo_UnwiredSubAgentReturnsError(t *testing.T) {
	agent := &DirectorAgent{}

	for _, target := range []string{"repo", "coding", "chat", "devops", "browser"} {
		_, err := agent.delegateTo(context.Background(), target, map[string]interface{}{"task": "do it"})
		if err == nil {
			t.Errorf("%s: 未装配却返回了成功", target)
			continue
		}
		if !strings.Contains(err.Error(), "not wired") {
			t.Errorf("%s: 错误 = %q, want 说明这个子 Agent 没接线", target, err)
		}
	}
}
