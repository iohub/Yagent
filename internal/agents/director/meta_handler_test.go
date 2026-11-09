// meta_handler_test.go — MetaAgentHandler 组件单测（P0-1 Phase 2d）。
//
// 独立于 DirectorAgent 可测：NewMetaAgentHandler 构造空注册表，CustomAgent 为纯
// 数据类型。断言锁定关键特征（与原 agents 门面层行为逐字一致的部分）：
//   - Register 正常注册（key = "delegate_" + Name）；重复注册返回包装
//     ErrAlreadyRegistered 的错误、错误文案精确、原条目不被覆盖；
//   - Get 命中返回同一指针；未命中（空/非空注册表）返回 (nil, false)；
//   - List/Names/Len 按注册（插入）序确定性返回，且为副本（外部修改不影响内部）；
//   - 空注册表 List/Names/Len 返回空结果不 panic；
//   - （P9a 删除）ParseMetaAgentOutput / ExtractJSONObject：生成职责移交 Python 之后，
//     "解析 LLM 裸输出"这几条用例随函数一起删除；注册表三组保留。
//   - 并发安全：多 goroutine 并发 Register/Get/List/Names 无 data race；
//     同 key 并发注册恰一个成功、其余 ErrAlreadyRegistered（防重复语义并发成立）。

package director

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
)

// makeCustomAgent 构造测试用 CustomAgent（纯数据，无闭包/执行器引用）。
func makeCustomAgent(name, prompt string) *CustomAgent {
	return &CustomAgent{
		Name:         name,
		DisplayName:  name + " display",
		SystemPrompt: prompt,
		ToolsUsed:    []string{"read_file"},
		Description:  "test custom agent " + name,
	}
}

// ─── Register Tests ──────────────────────────────────────────────────────

// TestMetaAgentHandlerRegister 固化注册与防重复语义：
// 首次注册成功；重复注册返回包装 ErrAlreadyRegistered 的错误（文案与实现一致），
// 且原条目不被覆盖（与原 registerCustomAgent 行为逐字一致）。
func TestMetaAgentHandlerRegister(t *testing.T) {
	h := NewMetaAgentHandler()
	ca := makeCustomAgent("security_auditor", "You are a security auditor.")

	if err := h.Register(ca); err != nil {
		t.Fatalf("unexpected error on first register: %v", err)
	}
	if got := h.Len(); got != 1 {
		t.Fatalf("Len() = %d, want 1", got)
	}

	// 重复注册同一 Name：拒绝并保持原条目不变。
	dup := makeCustomAgent("security_auditor", "DIFFERENT prompt")
	err := h.Register(dup)
	if err == nil {
		t.Fatal("expected error on duplicate register")
	}
	if !errors.Is(err, ErrAlreadyRegistered) {
		t.Errorf("error should wrap ErrAlreadyRegistered, got %v", err)
	}
	if want := "custom agent already registered: delegate_security_auditor"; err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}

	got, ok := h.Get("delegate_security_auditor")
	if !ok || got != ca {
		t.Errorf("duplicate register should keep the original entry, got %+v (ok=%v)", got, ok)
	}
	if got != nil && got.SystemPrompt != "You are a security auditor." {
		t.Errorf("original SystemPrompt was overwritten, got %q", got.SystemPrompt)
	}
	if h.Len() != 1 {
		t.Errorf("Len() after duplicate register = %d, want 1", h.Len())
	}
}

// ─── Get Tests ───────────────────────────────────────────────────────────

// TestMetaAgentHandlerGet 固化查找语义：
// 命中返回同一指针；未命中（空注册表与非空注册表）返回 (nil, false)，不 panic。
func TestMetaAgentHandlerGet(t *testing.T) {
	h := NewMetaAgentHandler()

	if got, ok := h.Get("delegate_missing"); ok || got != nil {
		t.Errorf("Get on empty registry = (%+v, %v), want (nil, false)", got, ok)
	}

	ca := makeCustomAgent("data_export", "You are a data exporter.")
	if err := h.Register(ca); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, ok := h.Get("delegate_data_export")
	if !ok {
		t.Fatal("Get should hit the registered agent")
	}
	if got != ca {
		t.Errorf("Get should return the same pointer, got %+v", got)
	}
	if got.SystemPrompt != "You are a data exporter." {
		t.Errorf("SystemPrompt = %q, want %q", got.SystemPrompt, "You are a data exporter.")
	}

	if got, ok := h.Get("delegate_nothing"); ok || got != nil {
		t.Errorf("Get miss on non-empty registry = (%+v, %v), want (nil, false)", got, ok)
	}
}

// ─── List / Names Tests ──────────────────────────────────────────────────

// TestMetaAgentHandlerListOrder 固化插入序与副本语义：
// 乱序注册时 List/Names 按注册序确定性返回（对齐可控改进：原 map 随机序已消除）；
// 返回切片为副本，外部修改不影响内部状态。
func TestMetaAgentHandlerListOrder(t *testing.T) {
	h := NewMetaAgentHandler()
	agents := []*CustomAgent{
		makeCustomAgent("beta", "B"),
		makeCustomAgent("alpha", "A"),
		makeCustomAgent("gamma", "G"),
	}
	for _, ca := range agents {
		if err := h.Register(ca); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	wantNames := []string{"delegate_beta", "delegate_alpha", "delegate_gamma"}
	gotNames := h.Names()
	if len(gotNames) != len(wantNames) {
		t.Fatalf("Names() = %v, want %v", gotNames, wantNames)
	}
	for i := range wantNames {
		if gotNames[i] != wantNames[i] {
			t.Errorf("Names()[%d] = %q, want %q (insertion order)", i, gotNames[i], wantNames[i])
		}
	}

	gotList := h.List()
	if len(gotList) != 3 {
		t.Fatalf("List() length = %d, want 3", len(gotList))
	}
	for i := range agents {
		if gotList[i] != agents[i] {
			t.Errorf("List()[%d] should be the agent registered at position %d", i, i)
		}
	}

	// 副本语义：修改返回切片不得影响内部状态。
	gotNames[0] = "MUTATED"
	gotList[0] = nil
	if h.Names()[0] != "delegate_beta" {
		t.Error("Names() should return a copy; internal state was mutated")
	}
	if h.List()[0] != agents[0] {
		t.Error("List() should return a copy; internal state was mutated")
	}
}

// TestMetaAgentHandlerEmptyRegistry 固化空注册表行为：
// Len 为 0，List/Names 返回空结果（len 0）且不 panic。
func TestMetaAgentHandlerEmptyRegistry(t *testing.T) {
	h := NewMetaAgentHandler()
	if got := h.Len(); got != 0 {
		t.Errorf("Len() = %d, want 0", got)
	}
	if l := h.List(); len(l) != 0 {
		t.Errorf("List() on empty registry = %v, want empty", l)
	}
	if n := h.Names(); len(n) != 0 {
		t.Errorf("Names() on empty registry = %v, want empty", n)
	}
}

// ─── Concurrency Tests ───────────────────────────────────────────────────

// TestMetaAgentHandlerConcurrent 固化并发安全：
// 多 goroutine 并发注册唯一 name 全部成功，并发 Get/List/Names/Len 无 data race；
// 同 key 并发注册恰一个成功、其余返回 ErrAlreadyRegistered（防重复语义并发成立）。
func TestMetaAgentHandlerConcurrent(t *testing.T) {
	h := NewMetaAgentHandler()
	const workers = 50

	var wg sync.WaitGroup
	var success atomic.Int64
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("agent_%02d", i)
			if err := h.Register(makeCustomAgent(name, "prompt "+name)); err != nil {
				t.Errorf("unexpected error registering %q: %v", name, err)
				return
			}
			success.Add(1)
			// 并发读：Get/List/Names/Len 与写并发，验证无 data race。
			if got, ok := h.Get("delegate_" + name); !ok || got == nil {
				t.Errorf("Get(%q) after own register should hit", "delegate_"+name)
			}
			_ = h.Len()
			_ = h.Names()
			_ = h.List()
		}(i)
	}
	wg.Wait()

	if got := success.Load(); got != workers {
		t.Errorf("successful registers = %d, want %d", got, workers)
	}
	if got := h.Len(); got != workers {
		t.Errorf("Len() = %d, want %d", got, workers)
	}
	// 并发下插入序不确定，但集合必须确定：排序后与预期一致。
	names := append([]string(nil), h.Names()...)
	sort.Strings(names)
	if len(names) != workers {
		t.Fatalf("len(Names()) = %d, want %d", len(names), workers)
	}
	for i, n := range names {
		if want := fmt.Sprintf("delegate_agent_%02d", i); n != want {
			t.Errorf("sorted Names()[%d] = %q, want %q", i, n, want)
		}
	}

	// 同 key 并发注册：恰一个成功，其余 ErrAlreadyRegistered。
	h2 := NewMetaAgentHandler()
	var wg2 sync.WaitGroup
	var dupOK, dupErr atomic.Int64
	for i := 0; i < 20; i++ {
		wg2.Add(1)
		go func() {
			defer wg2.Done()
			if err := h2.Register(makeCustomAgent("same", "same prompt")); err != nil {
				if errors.Is(err, ErrAlreadyRegistered) {
					dupErr.Add(1)
				} else {
					t.Errorf("unexpected error type: %v", err)
				}
				return
			}
			dupOK.Add(1)
		}()
	}
	wg2.Wait()
	if got := dupOK.Load(); got != 1 {
		t.Errorf("same-key concurrent register: ok = %d, want 1", got)
	}
	if got := dupErr.Load(); got != 19 {
		t.Errorf("same-key concurrent register: err = %d, want 19", got)
	}
	if got, ok := h2.Get("delegate_same"); !ok || got == nil {
		t.Error("winner entry should remain registered")
	}
}
