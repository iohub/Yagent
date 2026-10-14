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
		// 目录收录的模型：阈值 = 上下文窗口 * 0.75，忽略配置值（含线上常见的写死 120000）
		{"窗口 128000 → 阈值为其 75%", "deepseek/deepseek-v3.2", 0, int(128000 * 0.75)},
		{"目录优先于配置阈值", "deepseek/deepseek-v3.2", 120000, int(128000 * 0.75)},
		{"裸模型名同样命中", "qwen3-coder-plus", 120000, int(1048576 * 0.75)},
		// 未收录 / 无数据：回退配置值，再回退包默认值
		{"未收录模型回退配置值", "acme/private-model", 50000, 50000},
		{"未收录模型且无配置回退默认值", "acme/private-model", 0, DefaultContextCompressionThreshold},
		{"embedding 模型无上下文数据", "baai/bge-m3", 0, DefaultContextCompressionThreshold},
		{"空模型名回退配置值", "", 80000, 80000},
		{"负数配置视为未配置", "acme/private-model", -1, DefaultContextCompressionThreshold},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveThreshold(tt.model, tt.configured); got != tt.want {
				t.Errorf("ResolveThreshold(%q, %d) = %d, want %d", tt.model, tt.configured, got, tt.want)
			}
		})
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
		t.Fatalf("基线：目录阈值应优先于配置项，实际 %d, want %d", got, int(128000*0.75))
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
