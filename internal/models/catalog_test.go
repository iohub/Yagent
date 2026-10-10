package models

import (
	"math"
	"testing"
	"testing/fstest"
)

// fixtureCatalog 覆盖典型场景：完整 id、裸名查询、跨 org 同裸名、无上下文数据的
// embedding 模型、大小写混写。
const fixtureCatalog = `{
  "alibaba/qwen3-coder-plus": {"id": "alibaba/qwen3-coder-plus", "limit": {"context": 1048576, "output": 65536}},
  "deepseek/deepseek-v4-flash": {"limit": {"context": 1000000}},
  "baai/bge-m3": {"limit": {"context": 0}},
  "acme/custom-model": {"limit": {"context": 32768}}
}`

// resetCatalog 清空全部索引，模拟目录未加载的状态。
func resetCatalog() {
	mu.Lock()
	byID, byBare, fuzzyIdx = nil, nil, nil
	mu.Unlock()
	fuzzyLogged.Clear()
}

func loadFixture(t *testing.T, content string) {
	t.Helper()
	if err := Load(fstest.MapFS{CatalogPath: &fstest.MapFile{Data: []byte(content)}}); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	t.Cleanup(resetCatalog)
}

func TestContextWindow(t *testing.T) {
	loadFixture(t, fixtureCatalog)

	tests := []struct {
		name  string
		model string
		want  int
	}{
		{"完整 id", "alibaba/qwen3-coder-plus", 1048576},
		{"裸模型名", "qwen3-coder-plus", 1048576},
		{"大小写不敏感", "Alibaba/Qwen3-Coder-Plus", 1048576},
		{"org 与目录不一致时按裸名匹配", "deepseek-ai/DeepSeek-V4-Flash", 1000000},
		{"首尾空白", "  qwen3-coder-plus\n", 1048576},
		{"未收录模型", "some/unknown-model", 0},
		{"空串", "", 0},
		{"无上下文数据的 embedding 模型", "baai/bge-m3", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ContextWindow(tt.model); got != tt.want {
				t.Errorf("ContextWindow(%q) = %d, want %d", tt.model, got, tt.want)
			}
		})
	}
}

func TestContextWindow_CatalogNotLoaded(t *testing.T) {
	// 未调用 Load（或已被清理）时必须返回 0，由调用方回退到默认阈值。
	resetCatalog()

	if got := ContextWindow("alibaba/qwen3-coder-plus"); got != 0 {
		t.Errorf("ContextWindow() with unloaded catalog = %d, want 0", got)
	}
}

func TestLoad_Errors(t *testing.T) {
	t.Run("文件缺失", func(t *testing.T) {
		if err := Load(fstest.MapFS{}); err == nil {
			t.Error("Load() with missing catalog = nil error, want error")
		}
	})
	t.Run("JSON 非法", func(t *testing.T) {
		if err := Load(fstest.MapFS{CatalogPath: &fstest.MapFile{Data: []byte("{not json")}}); err == nil {
			t.Error("Load() with malformed JSON = nil error, want error")
		}
	})
}

func TestLoad_BareNameCollisionIsDeterministic(t *testing.T) {
	// 两个 org 下存在同裸名时，固定取 id 字典序更小者，
	// 避免因 map 遍历顺序不同导致阈值抖动。
	const catalog = `{
	  "zeta/shared-model": {"limit": {"context": 200000}},
	  "alpha/shared-model": {"limit": {"context": 100000}}
	}`

	for i := 0; i < 20; i++ {
		loadFixtureOnce(t, catalog)
		if got := ContextWindow("shared-model"); got != 100000 {
			t.Fatalf("iteration %d: ContextWindow(\"shared-model\") = %d, want 100000 (alpha/ 字典序更小)", i, got)
		}
	}
}

// loadFixtureOnce 加载 fixture 但不注册清理函数，供循环内重复加载使用。
func loadFixtureOnce(t *testing.T, content string) {
	t.Helper()
	if err := Load(fstest.MapFS{CatalogPath: &fstest.MapFile{Data: []byte(content)}}); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoad_OverwritesPreviousIndex(t *testing.T) {
	loadFixture(t, fixtureCatalog)
	if got := ContextWindow("acme/custom-model"); got != 32768 {
		t.Fatalf("ContextWindow(acme/custom-model) = %d, want 32768", got)
	}

	// 重新加载为不含该模型的目录，旧条目必须消失。
	if err := Load(fstest.MapFS{CatalogPath: &fstest.MapFile{Data: []byte(`{"other/model": {"limit": {"context": 8192}}}`)}}); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := ContextWindow("acme/custom-model"); got != 0 {
		t.Errorf("stale entry survived reload: ContextWindow(acme/custom-model) = %d, want 0", got)
	}
	if got := ContextWindow("other/model"); got != 8192 {
		t.Errorf("ContextWindow(other/model) = %d, want 8192", got)
	}
}

// fuzzyCatalog 用于相似度匹配测试，条目取自真实目录中的常见命名差异形态。
const fuzzyCatalog = `{
  "anthropic/claude-sonnet-4-5": {"limit": {"context": 200000}},
  "alibaba/qwen3.8-27b": {"limit": {"context": 262144}},
  "deepseek/deepseek-v4.1-flash": {"limit": {"context": 1000000}}
}`

func TestContextWindow_FuzzyMatch(t *testing.T) {
	loadFixture(t, fuzzyCatalog)

	tests := []struct {
		name  string
		model string
		want  int
	}{
		// 归一化后完全相同（分隔符 . 与 - 的差异），相似度 1.0
		{"分隔符差异", "claude-sonnet-4.5", 200000},
		{"分隔符差异 + 大小写混写", "Anthropic/Claude-Sonnet-4.5", 200000},
		// 私有部署常见形态：厂商在目录名后追加自有后缀，相似度恰好落在阈值 0.6 上
		{"带厂商后缀的私有部署名", "Qwen3.8-27B-GSQ-RCO", 262144},
		{"拼写多一个字符", "deepseek-v4.1-flashx", 1000000},
		// 相似度低于 fuzzyMinSimilarity → 视为未收录，返回 0
		{"完全无关的名称", "totally-unrelated-zzz", 0},
		{"纯符号", "!!!", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ContextWindow(tt.model); got != tt.want {
				t.Errorf("ContextWindow(%q) = %d, want %d", tt.model, got, tt.want)
			}
		})
	}
}

func TestContextWindow_FuzzyTieIsDeterministic(t *testing.T) {
	// 两个条目的归一化裸名相同，查询与二者相似度并列时，
	// 必须固定取 id 字典序更小者，避免 map/索引遍历顺序导致阈值抖动。
	const catalog = `{
	  "zeta/widget-a": {"limit": {"context": 200000}},
	  "alpha/widget-a": {"limit": {"context": 100000}}
	}`

	for i := 0; i < 20; i++ {
		loadFixtureOnce(t, catalog)
		if got := ContextWindow("widget-b"); got != 100000 {
			t.Fatalf("iteration %d: ContextWindow(\"widget-b\") = %d, want 100000 (alpha/ 字典序更小)", i, got)
		}
	}
}

func TestNormalize(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Claude-Sonnet-4.5", "claudesonnet45"},
		{"claude-sonnet-4-5", "claudesonnet45"},
		{"GLM_5.3 Flash", "glm53flash"},
		{"", ""},
		{"---", ""},
	}
	for _, tt := range tests {
		if got := normalize(tt.in); got != tt.want {
			t.Errorf("normalize(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSimilarity(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want float64
	}{
		{"完全相同", "abc", "abc", 1},
		{"单字符替换", "abc", "abd", 2.0 / 3},
		{"空串对空串", "", "", 1},
		{"一方为空", "abc", "", 0},
		{"前缀关系", "qwen3827b", "qwen3827bgsqrco", 9.0 / 15},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 常量表达式在编译期以任意精度求值，与运行时的浮点减法可能差 1 ULP，
			// 故用容差比较。
			if got := similarity(tt.a, tt.b); math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("similarity(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

// TestSimilarity_PrefixCaseSitsOnFuzzyCutoff 固定「前缀差 6 字符 / 长 15」这一
// 真实场景（Qwen3.8-27B-GSQ-RCO → qwen3.8-27b）的浮点结果恰好等于阈值，
// 若日后调整 fuzzyMinSimilarity 需同步复核该边界。
func TestSimilarity_PrefixCaseSitsOnFuzzyCutoff(t *testing.T) {
	got := similarity("qwen3827b", "qwen3827bgsqrco")
	if got < fuzzyMinSimilarity {
		t.Errorf("similarity = %v, 应 >= fuzzyMinSimilarity (%v)", got, fuzzyMinSimilarity)
	}
}
