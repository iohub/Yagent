package config

import (
	"os"
	"testing"
)

// YAGENT_CONCURRENCY=0 是全仓唯一的并发急停判据（G1 只读工具批与 G2 委派共用）。
// 与 YAGENT_BLACKBOARD=0 / YAGENT_RESUME=0 同构：只认精确的 "0"。认得宽一点
// （把 "false"/"00" 也当关）的后果是用户写了个自认为关掉的值，实际一切照旧 ——
// 表现不是报错，是"我明明关了怎么还在并发"。
func TestConcurrencyEnabledKillSwitch(t *testing.T) {
	for _, arm := range []struct {
		value string
		set   bool
		want  bool
	}{
		{set: false, want: true},
		{value: "0", set: true, want: false},
		{value: "1", set: true, want: true},
		{value: "", set: true, want: true},
		{value: "false", set: true, want: true},
		{value: "00", set: true, want: true},
	} {
		if arm.set {
			t.Setenv("YAGENT_CONCURRENCY", arm.value)
		} else {
			// 先 Setenv 一次，让 t 记得测完把变量还原成进来的样子。
			t.Setenv("YAGENT_CONCURRENCY", "placeholder")
			if err := os.Unsetenv("YAGENT_CONCURRENCY"); err != nil {
				t.Fatalf("unsetenv: %v", err)
			}
		}
		if got := ConcurrencyEnabled(); got != arm.want {
			t.Errorf("YAGENT_CONCURRENCY=%q（set=%v）→ ConcurrencyEnabled() = %v, want %v",
				arm.value, arm.set, got, arm.want)
		}
	}
}

// 键必须真的落到结构体上（模板那侧由 TestTemplatesDecodeCompletelyAndValidate
// 兜"写了没人认"，这里兜"认了但取错名"）。
func TestLoadFromFile_MaxParallelDelegations(t *testing.T) {
	cfg, err := LoadFromFile(writeCfg(t, twoProviders+"\n[agent]\nmax_parallel_delegations = 3\nmax_parallel_readonly_tools = 5\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Agent.MaxParallelDelegations != 3 {
		t.Errorf("MaxParallelDelegations = %d, want 3", cfg.Agent.MaxParallelDelegations)
	}
	if cfg.Agent.MaxParallelReadOnlyTools != 5 {
		t.Errorf("相邻的只读批键被写坏了：MaxParallelReadOnlyTools = %d, want 5", cfg.Agent.MaxParallelReadOnlyTools)
	}

	bare, err := LoadFromFile(writeCfg(t, twoProviders))
	if err != nil {
		t.Fatalf("load bare: %v", err)
	}
	// 没写 → 零值：这一位的默认折叠在 agents.NormalizeMaxParallelDelegations 里
	// （与 max_parallel_readonly_tools 同一套：1 = 串行、≤0 = 取默认）。
	if bare.Agent.MaxParallelDelegations != 0 {
		t.Errorf("没写这个键时 = %d, want 0（由 agents 折成默认）", bare.Agent.MaxParallelDelegations)
	}
}
