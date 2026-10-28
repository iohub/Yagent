package agents

import (
	"reflect"
	"testing"

	"yagent/internal/compression"
	"yagent/internal/config"
)

// TestCompressionOptionsFromConfig_MapsEveryConfigField 钉住"[enhanced_commander] 的每个键
// 都必须被映射进执行层 Options"。这条守卫必须存在：新增一个 TOML 键而忘了在转换点加一行，
// 编译器不会响、现有行为测试也不会响（表现只是那项配置在执行层静默失效）—— 那正是本次
// 单源化要消除的漂移（根 AGENTS.md §11 坑 33）。
//
// 五个字段各取互不相同的值：任何一条映射接错行也会被抓住。
func TestCompressionOptionsFromConfig_MapsEveryConfigField(t *testing.T) {
	ec := config.EnhancedCommanderConfig{
		EnableContextCompression:     true,
		ContextCompressionThreshold:  1234,
		ToolResultKeepTokens:         56,
		EnableUltimateCompression:    true,
		UltimateCompressionKeepPlans: 789,
	}

	got := compressionOptionsFromConfig(ec)
	want := compression.Options{
		Enabled:           true,
		Threshold:         1234,
		KeepTokens:        56,
		UltimateEnabled:   true,
		UltimateKeepPlans: 789,
	}
	if got != want {
		t.Errorf("compressionOptionsFromConfig() = %+v, want %+v", got, want)
	}

	// 两侧字段数必须相等：reflect 是编译期之外唯一能发现"config 加了一个键而 Options 没跟上"
	// 的手段。失败时的正确修法是把新键同时加进 compression.Options 与本函数的映射。
	cfgFields, optFields := reflect.TypeOf(ec).NumField(), reflect.TypeOf(got).NumField()
	if cfgFields != optFields {
		t.Errorf("config.EnhancedCommanderConfig 有 %d 个字段，compression.Options 只有 %d 个 —— 新增压缩配置项必须同步扩展两侧", cfgFields, optFields)
	}
}

// TestCompressionOptionsFromConfig_ZeroConfigMeansNoCompression 不启用压缩的 Agent（chat / meta）
// 走的就是这条零值路径：Options 全零 = 第一/二/三档全部不可达，与重构前"五个字段都不设"等价。
func TestCompressionOptionsFromConfig_ZeroConfigMeansNoCompression(t *testing.T) {
	if got := compressionOptionsFromConfig(config.EnhancedCommanderConfig{}); got != (compression.Options{}) {
		t.Errorf("零值配置的转换结果 = %+v, want 全零（不压缩）", got)
	}
	if DefaultExecutorConfig().Compression != (compression.Options{}) {
		t.Errorf("DefaultExecutorConfig().Compression = %+v, want 全零", DefaultExecutorConfig().Compression)
	}
}
