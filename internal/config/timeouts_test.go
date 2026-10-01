package config

import (
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

// TestDefaultTimeouts 断言 DefaultTimeouts() 的四个值与现状硬编码值一致：
//   - LLM          = 5m   ← internal/agents/executor.go:118（llmTimeout 默认值）
//   - SubAgentTool = 180s ← internal/agents/executor.go:497（defaultToolTimeout）
//   - PlannerTool  = 120s ← internal/agents/director.go:867（directorToolRunner）
//   - Delegate     = 10m  ← internal/agents/director.go:869（delegate_* 专用）
func TestDefaultTimeouts(t *testing.T) {
	d := DefaultTimeouts()
	if d.LLM != 5*time.Minute {
		t.Errorf("DefaultTimeouts().LLM = %v, want %v", d.LLM, 5*time.Minute)
	}
	if d.SubAgentTool != 180*time.Second {
		t.Errorf("DefaultTimeouts().SubAgentTool = %v, want %v", d.SubAgentTool, 180*time.Second)
	}
	if d.PlannerTool != 120*time.Second {
		t.Errorf("DefaultTimeouts().PlannerTool = %v, want %v", d.PlannerTool, 120*time.Second)
	}
	if d.Delegate != 10*time.Minute {
		t.Errorf("DefaultTimeouts().Delegate = %v, want %v", d.Delegate, 10*time.Minute)
	}
}

// TestNormalizeZeroValueMatchesDefaults 零值 TimeoutsConfig 调用 Normalize 后
// 与 DefaultTimeouts() 一致（保证配置缺失时行为与现状一致），且幂等。
func TestNormalizeZeroValueMatchesDefaults(t *testing.T) {
	var zero TimeoutsConfig
	n := zero.Normalize()

	want := DefaultTimeouts()
	if n != want {
		t.Errorf("zero.Normalize() = %+v, want %+v", n, want)
	}

	// 幂等：对已 Normalize 的结果再 Normalize 不变
	if again := n.Normalize(); again != n {
		t.Errorf("Normalize not idempotent: %+v -> %+v", n, again)
	}
}

// TestNormalizePartialDefaults 部分缺省（仅设置其中 1-2 个字段）时，
// Normalize 仅回退未设置（非正值）字段，显式配置的正值原样保留。
func TestNormalizePartialDefaults(t *testing.T) {
	t.Run("仅设置两个字段", func(t *testing.T) {
		in := TimeoutsConfig{LLM: 2 * time.Minute, Delegate: 30 * time.Minute}
		n := in.Normalize()
		if n.LLM != 2*time.Minute {
			t.Errorf("LLM = %v, want %v（显式设置的值不应被覆盖）", n.LLM, 2*time.Minute)
		}
		if n.Delegate != 30*time.Minute {
			t.Errorf("Delegate = %v, want %v（显式设置的值不应被覆盖）", n.Delegate, 30*time.Minute)
		}
		if n.SubAgentTool != 180*time.Second {
			t.Errorf("SubAgentTool = %v, want %v（未设置字段应回退默认值）", n.SubAgentTool, 180*time.Second)
		}
		if n.PlannerTool != 120*time.Second {
			t.Errorf("PlannerTool = %v, want %v（未设置字段应回退默认值）", n.PlannerTool, 120*time.Second)
		}
	})

	t.Run("仅设置一个字段", func(t *testing.T) {
		in := TimeoutsConfig{SubAgentTool: 60 * time.Second}
		n := in.Normalize()
		if n.SubAgentTool != 60*time.Second {
			t.Errorf("SubAgentTool = %v, want %v（显式设置的值不应被覆盖）", n.SubAgentTool, 60*time.Second)
		}
		want := DefaultTimeouts()
		if n.LLM != want.LLM || n.PlannerTool != want.PlannerTool || n.Delegate != want.Delegate {
			t.Errorf("未设置字段未正确回退默认值: got %+v, want defaults %+v", n, want)
		}
	})

	t.Run("负值视为未配置", func(t *testing.T) {
		in := TimeoutsConfig{LLM: -1}
		n := in.Normalize()
		if n.LLM != 5*time.Minute {
			t.Errorf("LLM = %v, want %v（负值应视为未配置并回退默认值）", n.LLM, 5*time.Minute)
		}
	})
}

// TestTimeoutsTOMLDecode 验证 [timeouts] 段的 TOML 解析：
// BurntSushi/toml v1.5.0 将字符串以 time.ParseDuration 解析为 time.Duration
//（decode.go:94），整数按纳秒解析。
func TestTimeoutsTOMLDecode(t *testing.T) {
	const content = `
[timeouts]
llm = "2m"
sub_agent_tool = "90s"
planner_tool = "120s"
delegate = "10m"
`
	var cfg Config
	if _, err := toml.Decode(content, &cfg); err != nil {
		t.Fatalf("decode [timeouts] failed: %v", err)
	}

	if cfg.Timeouts.LLM != 2*time.Minute {
		t.Errorf("Timeouts.LLM = %v, want %v", cfg.Timeouts.LLM, 2*time.Minute)
	}
	if cfg.Timeouts.SubAgentTool != 90*time.Second {
		t.Errorf("Timeouts.SubAgentTool = %v, want %v", cfg.Timeouts.SubAgentTool, 90*time.Second)
	}
	if cfg.Timeouts.PlannerTool != 120*time.Second {
		t.Errorf("Timeouts.PlannerTool = %v, want %v", cfg.Timeouts.PlannerTool, 120*time.Second)
	}
	if cfg.Timeouts.Delegate != 10*time.Minute {
		t.Errorf("Timeouts.Delegate = %v, want %v", cfg.Timeouts.Delegate, 10*time.Minute)
	}
}

// TestTimeoutsSectionMissingDefaultsToHardcodedValues 验证配置中完全没有
// [timeouts] 段时（现状配置文件的常态），Timeouts 为零值，Normalize 后
// 与 DefaultTimeouts() 一致 —— 即「缺省即默认值，行为与现状一致」。
func TestTimeoutsSectionMissingDefaultsToHardcodedValues(t *testing.T) {
	const content = `
[llm]
timeout = "5m"
`
	var cfg Config
	if _, err := toml.Decode(content, &cfg); err != nil {
		t.Fatalf("decode config failed: %v", err)
	}

	if cfg.Timeouts != (TimeoutsConfig{}) {
		t.Errorf("[timeouts] 段缺失时应解码为零值, got %+v", cfg.Timeouts)
	}
	if got := cfg.Timeouts.Normalize(); got != DefaultTimeouts() {
		t.Errorf("Normalize() = %+v, want %+v", got, DefaultTimeouts())
	}
}
