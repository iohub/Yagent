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
		// 目录收录的模型：阈值 = 上下文窗口 / 3，忽略配置值（含线上常见的写死 120000）
		{"窗口 128000 → 阈值为其 1/3", "deepseek/deepseek-v3.2", 0, 128000 / 3},
		{"目录优先于配置阈值", "deepseek/deepseek-v3.2", 120000, 128000 / 3},
		{"裸模型名同样命中", "qwen3-coder-plus", 120000, 1048576 / 3},
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
