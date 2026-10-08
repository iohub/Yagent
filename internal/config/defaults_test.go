package config

import (
	"testing"
	"time"
)

// DeriveDelegateIdleTimeout 派生规则：max(10 分钟, llmTimeout + 5 分钟)；
// llmTimeout ≤0 时按 5 分钟兜底（与 executor.go 的 LLM 超时兜底一致）。
func TestDeriveDelegateIdleTimeout(t *testing.T) {
	cases := []struct {
		name       string
		llmTimeout time.Duration
		want       time.Duration
	}{
		{"默认 llm 5min → 10min", 5 * time.Minute, 10 * time.Minute},
		{"llm 未配置 0 → 兜底 5min + buffer = 10min", 0, 10 * time.Minute},
		{"llm 负值 → 兜底 5min + buffer = 10min", -time.Minute, 10 * time.Minute},
		{"llm 30min → 35min", 30 * time.Minute, 35 * time.Minute},
		{"llm 1min → floor 10min", time.Minute, 10 * time.Minute},
		{"llm 4min59s → floor 10min", 5*time.Minute - time.Second, 10 * time.Minute},
		{"llm 5min01s → 10min01s", 5*time.Minute + time.Second, 10*time.Minute + time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DeriveDelegateIdleTimeout(tc.llmTimeout); got != tc.want {
				t.Errorf("DeriveDelegateIdleTimeout(%v) = %v, want %v", tc.llmTimeout, got, tc.want)
			}
		})
	}
}
