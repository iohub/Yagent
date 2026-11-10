package config

import (
	"testing"
	"time"
)

// [tools.llm.<slot>.timeout] 是"工具级 LLM 预算"的唯一配置入口（解析点 CogToolTimeout）。
// 这些用例钉住四件事：Duration 字符串真的能被解码进来、优先级、兜底下限、负值归一化。
// 去掉 CogToolTimeout 的任意一半（override 分支或 max()），对应用例立刻红。

// fallbackCogToolTimeout 是"没有任何 override 时"的兜底：max(DefaultCogToolTimeout, 全局值)。
func fallbackCogToolTimeout(t *testing.T) time.Duration {
	t.Helper()
	want := DefaultCogToolTimeout
	if g := GlobalLLMTimeout(); g > want {
		want = g
	}
	return want
}

// TOML 里的 "45m" 必须直接进 ToolLLMOverride.Timeout（BurntSushi 自动解析 time.Duration）。
// 这条同时证明"新增的 key 不是被静默忽略的未识别键"。
func TestCogToolTimeoutFromTOML(t *testing.T) {
	cfg, err := LoadFromFile(writeCfg(t, twoProviders+`
[tools.llm.deepthinking]
use_provider = "main"
timeout = "45m"
`))
	if err != nil {
		t.Fatalf("LoadFromFile: %v", err)
	}

	if got := cfg.Tools.LLM.DeepThinking.Timeout; got != 45*time.Minute {
		t.Fatalf("解码后的 Timeout = %v, want 45m（Duration 字符串没被解析进来）", got)
	}
	if got := cfg.CogToolTimeout("deepthinking"); got != 45*time.Minute {
		t.Errorf("CogToolTimeout(deepthinking) = %v, want 45m", got)
	}
	// design 槽位没配 → 兜底下限（而不是被 deepthinking 的值串台）。
	if got, want := cfg.CogToolTimeout("design"), fallbackCogToolTimeout(t); got != want {
		t.Errorf("CogToolTimeout(design) = %v, want %v", got, want)
	}
}

// 未配置时取 max(DefaultCogToolTimeout, GlobalLLMTimeout())。
func TestCogToolTimeoutFallsBackToMax(t *testing.T) {
	cfg, err := LoadFromFile(writeCfg(t, twoProviders))
	if err != nil {
		t.Fatalf("LoadFromFile: %v", err)
	}

	prev := GlobalLLMTimeout()
	// 还原成 prev；prev 为 0（未初始化）时无法写回 0 —— SetGlobalLLMTimeout 拒绝非正值，
	// 于是还原成与兜底等值的 DefaultCogToolTimeout：对本包后续用例的判据逐字相同。
	t.Cleanup(func() {
		if prev > 0 {
			SetGlobalLLMTimeout(prev)
			return
		}
		SetGlobalLLMTimeout(DefaultCogToolTimeout)
	})

	// 场景一：全局未初始化（0）→ 只剩兜底常量。
	setGlobalTimeoutForTest(t, 0)
	if got := cfg.CogToolTimeout("deepthinking"); got != DefaultCogToolTimeout {
		t.Errorf("全局=0 时 = %v, want 兜底 %v", got, DefaultCogToolTimeout)
	}

	// 场景二：全局 20m > 兜底 10m → 取全局。
	SetGlobalLLMTimeout(20 * time.Minute)
	if got := cfg.CogToolTimeout("design"); got != 20*time.Minute {
		t.Errorf("全局=20m 时 = %v, want 20m（兜底只保下限，不许压过更大的全局预算）", got)
	}
}

// 没写这个键 / 显式写 0 / 认不出的槽位名，三者都回落兜底。
func TestCogToolTimeoutUnsetVariants(t *testing.T) {
	cfg, err := LoadFromFile(writeCfg(t, twoProviders+`
[tools.llm.design]
timeout = "0s"
`))
	if err != nil {
		t.Fatalf("LoadFromFile: %v", err)
	}
	want := fallbackCogToolTimeout(t)

	for _, slot := range []string{"design", "deepthinking", "thinking", "micro_agent", "no-such-tool"} {
		if got := cfg.CogToolTimeout(slot); got != want {
			t.Errorf("CogToolTimeout(%q) = %v, want %v（未配置/显式 0/未列出的槽位一律兜底）", slot, got, want)
		}
	}

	// nil receiver 必须安全：装配层在配置加载失败时仍会把解析器注入工具。
	var nilCfg *Config
	if got := nilCfg.CogToolTimeout("deepthinking"); got != want {
		t.Errorf("nil cfg 时 = %v, want %v", got, want)
	}
}

// 负时长没有语义（会让 ctx 当场过期），validate() 必须把它归一化成 0 = 没配。
// 同时钉住"归一化只发生在消费该字段的槽位上"：thinking 不读它，这里不做断言，
// 但 deepthinking 必须被清掉。
func TestCogToolTimeoutNegativeNormalizedToUnset(t *testing.T) {
	cfg, err := LoadFromFile(writeCfg(t, twoProviders+`
[tools.llm.deepthinking]
timeout = "-1m"
`))
	if err != nil {
		t.Fatalf("LoadFromFile: %v", err)
	}
	if got := cfg.Tools.LLM.DeepThinking.Timeout; got != 0 {
		t.Errorf("负超时归一化后 = %v, want 0", got)
	}
	if got, want := cfg.CogToolTimeout("deepthinking"), fallbackCogToolTimeout(t); got != want {
		t.Errorf("CogToolTimeout(deepthinking) = %v, want 兜底 %v", got, want)
	}
}

// setGlobalTimeoutForTest 把全局 LLM 超时置成 d（0 表示"当作未初始化"）。
// 归零只能靠写一个非正值被拒绝这条路上的反向操作，所以这里用 1ns 的"极小值"模拟"没有预算"：
// 对 CogToolTimeout 而言 < DefaultCogToolTimeout 与 0 等价（都取兜底常量）。
func setGlobalTimeoutForTest(t *testing.T, d time.Duration) {
	t.Helper()
	if d > 0 {
		SetGlobalLLMTimeout(d)
		return
	}
	SetGlobalLLMTimeout(time.Nanosecond)
}
