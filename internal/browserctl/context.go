package browserctl

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// 一次浏览器委派独占一个工作区（.browser-control 目录），标识经 ctx 传给工具层。
// 放在 browserctl 而不是 agents：工具层要读它，但不能反向依赖 internal/agents。
type delegationIDKey struct{}

func WithDelegationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, delegationIDKey{}, id)
}

func DelegationIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(delegationIDKey{}).(string)
	return v
}

// NewDelegationID 生成委派标识：时间前缀便于排查，随机后缀避免并发撞车。
func NewDelegationID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%s-%s", time.Now().Format("20060102-150405"), hex.EncodeToString(b[:]))
}

// SanitizeDelegationID 把任意标识收敛成安全的目录名（防路径穿越），上限 32 字符。
func SanitizeDelegationID(id string) string {
	var sb strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			sb.WriteRune(r)
		default:
			sb.WriteRune('-')
		}
		if sb.Len() >= 32 {
			break
		}
	}
	out := strings.Trim(sb.String(), "-")
	if out == "" {
		return "task"
	}
	return out
}
