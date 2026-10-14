package memory

import (
	"context"
	"testing"
)

// TestWithConversationMemory_NilMem_ReturnsOriginalCtx 验证 nil mem 时原样返回 ctx
func TestWithConversationMemory_NilMem_ReturnsOriginalCtx(t *testing.T) {
	ctx := context.Background()

	got := WithConversationMemory(ctx, nil)
	if got != ctx {
		t.Fatal("expected the original ctx unchanged when mem is nil, got a new context")
	}

	// 这一步同时钉住"ctx 里压根没有 memory"的取值（返回 (nil, false)）：
	// 单独为 context.Background() 写一个用例只是在断言 context.Value 的自身保证。
	if _, ok := GetConversationMemory(got); ok {
		t.Fatal("expected ok=false when no memory was injected")
	}
	if mem, _ := GetConversationMemory(got); mem != nil {
		t.Fatalf("expected nil memory, got %+v", mem)
	}
}

// TestWithConversationMemory_NoInterferenceWithRolloutWriter 验证
// ConversationMemory 与 RolloutWriter 的 context key 互不干扰
func TestWithConversationMemory_NoInterferenceWithRolloutWriter(t *testing.T) {
	ctx := context.Background()
	mem := NewConversationMemory(100)
	rw := &RolloutWriter{enabled: true}

	// 同时注入两者
	combined := WithConversationMemory(WithRolloutWriter(ctx, rw), mem)

	gotMem, ok := GetConversationMemory(combined)
	if !ok || gotMem != mem {
		t.Fatalf("expected the injected ConversationMemory, got ok=%v mem=%+v", ok, gotMem)
	}
	gotRW := GetRolloutWriter(combined)
	if gotRW != rw {
		t.Fatalf("expected the injected RolloutWriter, got %+v", gotRW)
	}

	// 只注入 ConversationMemory 时，RolloutWriter 取出应为 nil
	memOnly := WithConversationMemory(ctx, mem)
	if _, ok := GetConversationMemory(memOnly); !ok {
		t.Fatal("expected ok=true for injected memory")
	}
	if GetRolloutWriter(memOnly) != nil {
		t.Fatal("expected nil RolloutWriter when only memory was injected")
	}

	// 只注入 RolloutWriter 时，ConversationMemory 取出应为 (nil, false)
	rwOnly := WithRolloutWriter(ctx, rw)
	if _, ok := GetConversationMemory(rwOnly); ok {
		t.Fatal("expected ok=false when only a RolloutWriter was injected")
	}
	if GetRolloutWriter(rwOnly) != rw {
		t.Fatal("expected the injected RolloutWriter")
	}
}
