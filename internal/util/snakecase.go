package util

import "strings"

// SnakeCase 把展示名归一化成 snake_case 标识符（Agent 名 → delegate_<name>）。
// 全仓单一实现：plasma 的 Elaborate 与 agents 的自定义 Agent 注册共用它，
// 各写一份就是"两份同形实现各自漂走"那类坑。
// 空结果回落 "custom_agent"，保证调用方永远拿到一个合法标识符。
func SnakeCase(name string) string {
	var result strings.Builder
	for i, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			result.WriteRune(r)
		} else if r == ' ' || r == '-' || r == '_' {
			if i > 0 {
				result.WriteRune('_')
			}
		} else {
			result.WriteRune('_')
		}
	}
	raw := result.String()
	for strings.Contains(raw, "__") {
		raw = strings.ReplaceAll(raw, "__", "_")
	}
	raw = strings.Trim(raw, "_")
	if raw == "" {
		raw = "custom_agent"
	}
	return raw
}
