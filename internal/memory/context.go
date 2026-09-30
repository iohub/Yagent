package memory

import (
	"context"
)

// conversationMemoryCtxKey 私有 context key 类型（对齐 rollout_writer.go 的模式）
type conversationMemoryCtxKey struct{}

// WithConversationMemory 将 ConversationMemory 注入到 context 中。
// mem 为 nil 时原样返回 ctx（不注入），调用方无需判空。
func WithConversationMemory(ctx context.Context, mem *ConversationMemory) context.Context {
	if mem == nil {
		return ctx
	}
	return context.WithValue(ctx, conversationMemoryCtxKey{}, mem)
}

// GetConversationMemory 从 context 中取出 ConversationMemory，
// 不存在时返回 (nil, false)。
func GetConversationMemory(ctx context.Context) (*ConversationMemory, bool) {
	if mem, ok := ctx.Value(conversationMemoryCtxKey{}).(*ConversationMemory); ok {
		return mem, true
	}
	return nil, false
}
