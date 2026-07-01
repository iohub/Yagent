package compact

import (
	"testing"
	"time"
)

// TestConfigFromFull_SafeDefaults 验证 ConfigFromFull() 调用 applySafeDefaults()
func TestConfigFromFull_SafeDefaults(t *testing.T) {
	// 传入全零值
	cfg := ConfigFromFull(
		0,   // MaxContextTokens
		false, // EnableAutoCompact (零值)
		"",  // SummarizationModel
		"",  // SummarizationProvider
		0,   // SummarizationTimeout
		0,   // SummarizationMaxInputTokens
		"",  // SummarizationPrompt
		0,   // KeepRecentRounds
	)

	// 验证所有字段都有安全默认值
	if cfg.MaxContextTokens != 198000 {
		t.Errorf("MaxContextTokens = %d, want 198000", cfg.MaxContextTokens)
	}
	if !cfg.EnableAutoCompact {
		t.Errorf("EnableAutoCompact = false, want true")
	}
	if cfg.SummarizationTimeout != 120*time.Second {
		t.Errorf("SummarizationTimeout = %v, want 120s", cfg.SummarizationTimeout)
	}
	if cfg.SummarizationMaxInputTokens != 120000 {
		t.Errorf("SummarizationMaxInputTokens = %d, want 120000", cfg.SummarizationMaxInputTokens)
	}
	if cfg.KeepRecentRounds != 2 {
		t.Errorf("KeepRecentRounds = %d, want 2", cfg.KeepRecentRounds)
	}

	// 验证异步/增量压缩字段
	if !cfg.AsyncCompactEnabled {
		t.Errorf("AsyncCompactEnabled = false, want true")
	}
	if cfg.CompactTriggerThreshold != 0.8 {
		t.Errorf("CompactTriggerThreshold = %f, want 0.8", cfg.CompactTriggerThreshold)
	}
	if cfg.MaxConcurrentSummaries != 3 {
		t.Errorf("MaxConcurrentSummaries = %d, want 3", cfg.MaxConcurrentSummaries)
	}
	if cfg.CompactWorkerInterval != 30*time.Second {
		t.Errorf("CompactWorkerInterval = %v, want 30s", cfg.CompactWorkerInterval)
	}
	if cfg.CompactionRetryAttempts != 2 {
		t.Errorf("CompactionRetryAttempts = %d, want 2", cfg.CompactionRetryAttempts)
	}
	if cfg.SummaryStackMaxDepth != 5 {
		t.Errorf("SummaryStackMaxDepth = %d, want 5", cfg.SummaryStackMaxDepth)
	}
}

// TestConfigFrom_DeprecatedButWorks 验证 ConfigFrom() (Deprecated) 也能正常工作
func TestConfigFrom_DeprecatedButWorks(t *testing.T) {
	cfg := ConfigFrom(
		0,  // 零值
		false, // 零值
		"", "", 0, 0, "", 0,
	)

	// 验证所有字段都有安全默认值（与 ConfigFromFull 一致）
	if !cfg.EnableAutoCompact {
		t.Error("ConfigFrom: EnableAutoCompact = false, want true (from applySafeDefaults)")
	}
	if !cfg.AsyncCompactEnabled {
		t.Error("ConfigFrom: AsyncCompactEnabled = false, want true (from applySafeDefaults)")
	}
	if cfg.CompactTriggerThreshold != 0.8 {
		t.Errorf("ConfigFrom: CompactTriggerThreshold = %f, want 0.8", cfg.CompactTriggerThreshold)
	}
}

// TestConfigFromV2_SafeDefaults 验证 ConfigFromV2() 调用 applySafeDefaults()
func TestConfigFromV2_SafeDefaults(t *testing.T) {
	// 传入全零值
	cfg := ConfigFromV2(
		0,   false, "", "",
		0, 0, "", 0,
		false, 0, 0, 0, 0, 0,
	)

	// 所有字段都应该有安全默认值
	if !cfg.EnableAutoCompact {
		t.Error("ConfigFromV2: EnableAutoCompact = false, want true")
	}
	if !cfg.AsyncCompactEnabled {
		t.Error("ConfigFromV2: AsyncCompactEnabled = false, want true")
	}
	if cfg.CompactTriggerThreshold != 0.8 {
		t.Errorf("ConfigFromV2: CompactTriggerThreshold = %f, want 0.8", cfg.CompactTriggerThreshold)
	}
}

// TestConfigFromV2_ExplicitValues 验证 ConfigFromV2() 显式值被保留（如果非零）
func TestConfigFromV2_ExplicitValues(t *testing.T) {
	cfg := ConfigFromV2(
		100000, true, "gpt-3.5", "my-provider",
		30, 5000, "custom prompt", 5,
		true, 0.9, 5, 3, 10, 50*time.Millisecond,
	)

	// 验证显式值被保留
	if cfg.MaxContextTokens != 100000 {
		t.Errorf("MaxContextTokens = %d, want 100000", cfg.MaxContextTokens)
	}
	if cfg.CompactTriggerThreshold != 0.9 {
		t.Errorf("CompactTriggerThreshold = %f, want 0.9", cfg.CompactTriggerThreshold)
	}
	if cfg.MaxConcurrentSummaries != 5 {
		t.Errorf("MaxConcurrentSummaries = %d, want 5", cfg.MaxConcurrentSummaries)
	}
	if cfg.CompactionRetryAttempts != 3 {
		t.Errorf("CompactionRetryAttempts = %d, want 3", cfg.CompactionRetryAttempts)
	}
	if cfg.SummaryStackMaxDepth != 10 {
		t.Errorf("SummaryStackMaxDepth = %d, want 10", cfg.SummaryStackMaxDepth)
	}
	if cfg.CompactWorkerInterval != 50*time.Millisecond {
		t.Errorf("CompactWorkerInterval = %v, want 50ms", cfg.CompactWorkerInterval)
	}
}

// TestApplySafeDefaults_ZeroConfig 验证零值 Config 被正确填充
func TestApplySafeDefaults_ZeroConfig(t *testing.T) {
	cfg := &Config{} // 所有字段零值
	cfg.applySafeDefaults()

	if cfg.MaxContextTokens != 198000 {
		t.Errorf("MaxContextTokens = %d, want 198000", cfg.MaxContextTokens)
	}
	if !cfg.EnableAutoCompact {
		t.Error("EnableAutoCompact should be true")
	}
	if cfg.SummarizationTimeout != 120*time.Second {
		t.Errorf("SummarizationTimeout = %v, want 120s", cfg.SummarizationTimeout)
	}
	if !cfg.AsyncCompactEnabled {
		t.Error("AsyncCompactEnabled should be true")
	}
	if cfg.CompactTriggerThreshold != 0.8 {
		t.Errorf("CompactTriggerThreshold = %f, want 0.8", cfg.CompactTriggerThreshold)
	}
}

// TestApplySafeDefaults_PreservesNonZero 验证非零值被保留
func TestApplySafeDefaults_PreservesNonZero(t *testing.T) {
	cfg := &Config{
		MaxContextTokens:        50000,
		EnableAutoCompact:       true,
		AsyncCompactEnabled:     true,
		CompactTriggerThreshold: 0.9,
	}
	cfg.applySafeDefaults()

	// 非零值应该被保留
	if cfg.MaxContextTokens != 50000 {
		t.Errorf("MaxContextTokens = %d, want 50000", cfg.MaxContextTokens)
	}
	if cfg.CompactTriggerThreshold != 0.9 {
		t.Errorf("CompactTriggerThreshold = %f, want 0.9", cfg.CompactTriggerThreshold)
	}

	// 零值字段应该被填充
	if !cfg.EnableAutoCompact {
		t.Error("EnableAutoCompact should be true (was zero)")
	}
	if !cfg.AsyncCompactEnabled {
		t.Error("AsyncCompactEnabled should be true (was zero)")
	}
}

// TestConfigFrom_FullConfigValues 验证非零参数被正确传递
func TestConfigFrom_FullConfigValues(t *testing.T) {
	cfg := ConfigFrom(
		100000, true, "gpt-3.5", "my-provider",
		60, 10000, "custom prompt", 5,
	)

	if cfg.MaxContextTokens != 100000 {
		t.Errorf("MaxContextTokens = %d, want 100000", cfg.MaxContextTokens)
	}
	if cfg.SummarizationModel != "gpt-3.5" {
		t.Errorf("SummarizationModel = %s, want 'gpt-3.5'", cfg.SummarizationModel)
	}
	if cfg.SummarizationProvider != "my-provider" {
		t.Errorf("SummarizationProvider = %s, want 'my-provider'", cfg.SummarizationProvider)
	}
	if time.Duration(60)*time.Second != cfg.SummarizationTimeout {
		t.Errorf("SummarizationTimeout = %v, want 60s", cfg.SummarizationTimeout)
	}
	if cfg.SummarizationMaxInputTokens != 10000 {
		t.Errorf("SummarizationMaxInputTokens = %d, want 10000", cfg.SummarizationMaxInputTokens)
	}
	if cfg.KeepRecentRounds != 5 {
		t.Errorf("KeepRecentRounds = %d, want 5", cfg.KeepRecentRounds)
	}

	// 异步字段应该有默认值（因为 ConfigFrom 不传这些参数）
	if !cfg.AsyncCompactEnabled {
		t.Error("AsyncCompactEnabled should be true (default from applySafeDefaults)")
	}
	if cfg.CompactTriggerThreshold != 0.8 {
		t.Errorf("CompactTriggerThreshold = %f, want 0.8 (default from applySafeDefaults)", cfg.CompactTriggerThreshold)
	}
}
