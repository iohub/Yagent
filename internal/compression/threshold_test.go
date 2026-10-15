package compression

import (
	"testing"
	"testing/fstest"

	"yagent/internal/models"
)

// loadCatalog 注入一个最小模型目录，供 ResolveThreshold 的优先级测试使用。
func loadCatalog(t *testing.T) {
	t.Helper()
	const catalog = `{
	  "deepseek/deepseek-v3.2": {"limit": {"context": 128000}},
	  "alibaba/qwen3-coder-plus": {"limit": {"context": 1048576}},
	  "baai/bge-m3": {"limit": {"context": 0}}
	}`
	if err := models.Load(fstest.MapFS{models.CatalogPath: &fstest.MapFile{Data: []byte(catalog)}}); err != nil {
		t.Fatalf("models.Load() error = %v", err)
	}
}

func TestResolveThreshold(t *testing.T) {
	loadCatalog(t)

	tests := []struct {
		name       string
		model      string
		configured int
		want       int
	}{
		// 目录与配置都是上限，两者都给值时取小（docs/compression-trigger-design.md §5.1）。
		{"128000 窗口：目录 96000 小于配置 120000 → 目录胜出", "deepseek/deepseek-v3.2", 120000, int(128000 * 0.75)},
		{"1M 窗口：配置 120000 小于目录 786432 → 配置胜出", "alibaba/qwen3-coder-plus", 120000, 120000},
		{"1M 窗口：配置比目录还大 → 仍取目录", "alibaba/qwen3-coder-plus", 900000, int(1048576 * 0.75)},
		// 只有一边给值
		{"目录命中且无配置 → 目录", "deepseek/deepseek-v3.2", 0, int(128000 * 0.75)},
		{"裸模型名同样命中目录", "qwen3-coder-plus", 0, int(1048576 * 0.75)},
		// 未收录 / 无数据：回退配置值，再回退包默认值
		{"未收录模型回退配置值", "acme/private-model", 50000, 50000},
		{"未收录模型且无配置回退默认值", "acme/private-model", 0, DefaultContextCompressionThreshold},
		{"embedding 模型无上下文数据", "baai/bge-m3", 0, DefaultContextCompressionThreshold},
		{"embedding 模型 + 配置值 → 配置", "baai/bge-m3", 60000, 60000},
		{"空模型名回退配置值", "", 80000, 80000},
		{"负数配置视为未配置", "acme/private-model", -1, DefaultContextCompressionThreshold},
		// 目录命中时，0 与负数都必须按"没写"处理，否则阈值会掉进兜底值
		{"负数配置不参与取小", "alibaba/qwen3-coder-plus", -1, int(1048576 * 0.75)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveThreshold(tt.model, tt.configured); got != tt.want {
				t.Errorf("ResolveThreshold(%q, %d) = %d, want %d", tt.model, tt.configured, got, tt.want)
			}
		})
	}
}

// TestResolveThreshold_ReplayBudgetUnaffected 钉住取小的**边界**：
// ResolveThreshold 有两个语义不同的调用方 —— 压缩触发传配置值，而重放预算
// （internal/agents/resume_delegation.go:187 的 ResolveThreshold(model, 0)）传 0，
// 要的是"这个模型装得下多少"。取小一旦让 0 也参与，重放预算就会从 786432 掉到
// 兜底的 120000，表现是"能重放的委派突然全部降级成交接报告"——那与压缩无关，
// 却会被误当成压缩改动的一部分。
func TestResolveThreshold_ReplayBudgetUnaffected(t *testing.T) {
	loadCatalog(t)

	const big = "alibaba/qwen3-coder-plus" // 1,048,576 窗口 → 目录推导 786,432
	if got := ResolveThreshold(big, 0); got != int(1048576*0.75) {
		t.Errorf("configured=0（重放预算的传参形态）= %d, want %d：0 必须按\"没写\"处理，不参与取小",
			got, int(1048576*0.75))
	}
	// 对照：同一条模型路径上压缩触发确实会被配置压小，两件事不能糊在一起。
	if got := ResolveThreshold(big, 120000); got != 120000 {
		t.Errorf("configured=120000（压缩触发的传参形态）= %d, want 120000（取小没生效）", got)
	}
}

// TestResolveThreshold_EnvOverrideBeatsCatalog 钉住调试用的第 0 级优先级：
// 设了 YAGENT_COMPRESS_THRESHOLD 就直接用它，目录与配置都不作数。
// 这条分支存在的理由很实际 —— 目录收录的模型动辄 1M 窗口（阈值 750k），
// 想在真机上肉眼验收压缩行为根本攒不到那么长。
func TestResolveThreshold_EnvOverrideBeatsCatalog(t *testing.T) {
	loadCatalog(t)

	const model = "deepseek/deepseek-v3.2"
	if got := ResolveThreshold(model, 120000); got != int(128000*0.75) {
		t.Fatalf("基线：取小后应是目录的 96000，实际 %d, want %d", got, int(128000*0.75))
	}

	t.Setenv(EnvThreshold, "2000")
	if got := ResolveThreshold(model, 120000); got != 2000 {
		t.Errorf("覆盖后阈值 = %d, want 2000（env 必须压过目录与配置）", got)
	}
}

// TestResolveThreshold_EnvOverrideInvalid 钉住"配错了就什么也别改"：
// 非法值必须原样回落到目录/配置，而不是把阈值变成 0 —— 0 会让每一步都触发压缩。
func TestResolveThreshold_EnvOverrideInvalid(t *testing.T) {
	loadCatalog(t)

	for _, bad := range []string{"0", "-5", "abc", "", "  ", "12k"} {
		t.Setenv(EnvThreshold, bad)
		if got := ResolveThreshold("", 50000); got != 50000 {
			t.Errorf("%s=%q 时阈值 = %d, want 回落到配置值 50000", EnvThreshold, bad, got)
		}
	}
	t.Setenv(EnvThreshold, "4000")
	if got := ResolveThreshold("deepseek/deepseek-v3.2", 50000); got != 4000 {
		t.Errorf("合法值未生效：%d", got)
	}
}

// TestResolveThreshold_EnvOverrideReadEveryCall 钉住"不缓存取值"：
// 运行期改了环境变量必须立刻生效，否则测试与调试都会读到旧值。
func TestResolveThreshold_EnvOverrideReadEveryCall(t *testing.T) {
	loadCatalog(t)

	t.Setenv(EnvThreshold, "3000")
	if got := ResolveThreshold("", 120000); got != 3000 {
		t.Fatalf("第一次 = %d, want 3000", got)
	}
	t.Setenv(EnvThreshold, "4000")
	if got := ResolveThreshold("", 120000); got != 4000 {
		t.Fatalf("改环境后 = %d, want 4000（取值被缓存住了？）", got)
	}
	t.Setenv(EnvThreshold, "")
	if got := ResolveThreshold("", 120000); got != 120000 {
		t.Fatalf("清空后 = %d, want 120000", got)
	}
}
