package main

import (
	"testing"

	"yagent/internal/models"
)

// TestEmbeddedModelCatalog 校验内嵌的 dist/data/models.json 可解析，且默认配置
// （internal/config/default_config.toml）里用到的模型都能查到上下文窗口——
// 压缩阈值与 TUI 上下文进度条都依赖这一查询，查不到就会退回写死的默认值。
func TestEmbeddedModelCatalog(t *testing.T) {
	if err := models.Load(distDataFS); err != nil {
		t.Fatalf("models.Load(distDataFS) error = %v", err)
	}

	// 前三个是默认配置里的裸模型名；最后一个不在目录中，靠相似度匹配命中
	// alibaba/qwen3.8-27b（私有部署追加了厂商后缀）。
	for _, model := range []string{
		"qwen3-coder-plus",
		"deepseek-v4-pro",
		"Qwen3-30B-A3B",
		"Qwen3.8-27B-GSQ-RCO",
	} {
		if got := models.ContextWindow(model); got <= 0 {
			t.Errorf("ContextWindow(%q) = %d, want > 0", model, got)
		}
	}

	// 分隔符写法差异（4.5 vs 4-5）经归一化后应命中同一条目。
	if dotted, dashed := models.ContextWindow("claude-sonnet-4.5"), models.ContextWindow("anthropic/claude-sonnet-4-5"); dotted != dashed || dotted <= 0 {
		t.Errorf("ContextWindow(\"claude-sonnet-4.5\") = %d, want == ContextWindow(\"anthropic/claude-sonnet-4-5\") = %d (both > 0)", dotted, dashed)
	}

	// 无关字符串不得被模糊匹配硬套到某个收录模型上。
	if got := models.ContextWindow("totally-unrelated-zzz"); got != 0 {
		t.Errorf("ContextWindow(\"totally-unrelated-zzz\") = %d, want 0", got)
	}
}
