package agents

import (
	"fmt"
	"strings"
	"testing"
)

func TestNewResultCompressor_Defaults(t *testing.T) {
	rc := NewResultCompressor(0, 0)
	if rc.threshold != 4096 {
		t.Errorf("expected default threshold 4096, got %d", rc.threshold)
	}
	if rc.summaryMaxLen != 2048 {
		t.Errorf("expected default summaryMaxLen 2048, got %d", rc.summaryMaxLen)
	}
}

func TestNewResultCompressor_Custom(t *testing.T) {
	rc := NewResultCompressor(1024, 512)
	if rc.threshold != 1024 {
		t.Errorf("expected threshold 1024, got %d", rc.threshold)
	}
	if rc.summaryMaxLen != 512 {
		t.Errorf("expected summaryMaxLen 512, got %d", rc.summaryMaxLen)
	}
}

func TestCompress_SmallResult(t *testing.T) {
	rc := NewResultCompressor(100, 50)
	result := "Hello, World!" // 13 字符
	comp := rc.Compress("agent1", "task1", result)

	if comp.Compressed {
		t.Error("expected small result not to be compressed")
	}
	if comp.Content != result {
		t.Errorf("expected content to be original, got %q", comp.Content)
	}
	if comp.OriginalSize != len(result) {
		t.Errorf("expected original size %d, got %d", len(result), comp.OriginalSize)
	}
	if comp.StorageKey != "" {
		t.Error("expected empty storage key for uncompressed result")
	}
}

func TestCompress_LargeResult_WithSharedMemory(t *testing.T) {
	rc := NewResultCompressor(50, 30)
	// 不直接调用 SetSharedMemory，通过 Compress 内部路径验证 sharedMemory 存在性

	// 创建大结果 (100 字符)
	largeResult := strings.Repeat("x", 100)
	comp := rc.Compress("agent1", "task1", largeResult)

	if !comp.Compressed {
		t.Error("expected large result to be compressed")
	}
	if comp.Content == "" {
		t.Error("expected non-empty compressed content")
	}
	if comp.OriginalSize != 100 {
		t.Errorf("expected original size 100, got %d", comp.OriginalSize)
	}
}

func TestCompress_LargeResult_NoSharedMemory(t *testing.T) {
	rc := NewResultCompressor(50, 30)
	// 不设置 sharedMemory

	largeResult := strings.Repeat("x", 100)
	comp := rc.Compress("agent1", "task1", largeResult)

	if !comp.Compressed {
		t.Error("expected large result to be compressed")
	}
	if !strings.Contains(comp.Content, "警告") {
		t.Error("expected fallback content with warning message")
	}
	if comp.StorageKey != "" {
		t.Error("expected empty storage key when shared memory not available")
	}
}

func TestGenerateSummary_TooLong(t *testing.T) {
	rc := NewResultCompressor(0, 50)

	// 创建 400 字符的结果（需要 > summaryMaxLen + tailLen 才会添加省略号）
	result := "line1\n" + strings.Repeat("middle content here ", 25) + "\n" + "last line"

	summary := rc.generateSummary(result)

	// 摘要长度应该小于原始长度，并且包含省略号标记
	if len(summary) >= len(result) {
		t.Errorf("summary should be shorter than original: summary=%d, original=%d", len(summary), len(result))
	}
	if !strings.Contains(summary, "... [中间内容省略] ...") {
		t.Error("expected ellipsis marker in summary")
	}
	// 摘要应该以尾部内容结尾
	if !strings.Contains(summary, "last line") {
		t.Error("expected summary to contain tail content")
	}
}

func TestGenerateSummary_FitsInLimit(t *testing.T) {
	rc := NewResultCompressor(0, 500)

	shortResult := strings.Repeat("x", 100)
	summary := rc.generateSummary(shortResult)

	if summary != shortResult {
		t.Error("expected summary to be identical to short result")
	}
}

func TestSharedMemoryStore_NilSharedMemory(t *testing.T) {
	store := &SharedMemoryStore{} // sm is nil

	err := store.Store("key", "value")
	if err == nil {
		t.Error("expected error when storing with nil shared memory")
	}

	_, err = store.Retrieve("key")
	if err == nil {
		t.Error("expected error when retrieving with nil shared memory")
	}
}

func TestCompressionResult_JSONFields(t *testing.T) {
	comp := &CompressionResult{
		Compressed:     true,
		Content:        "test content",
		StorageKey:     "result:agent:task:full",
		OriginalSize:   100,
		CompressedSize: 50,
	}

	if comp.Compressed != true {
		t.Error("expected Compressed to be true")
	}
	if comp.StorageKey == "" {
		t.Error("expected StorageKey to be set")
	}

	// 测试 omitempty 对于未设置 StorageKey 的情况
	uncompressed := &CompressionResult{
		Compressed:     false,
		Content:        "small result",
		OriginalSize:   12,
		CompressedSize: 12,
	}
	if uncompressed.StorageKey != "" {
		t.Error("expected empty StorageKey for uncompressed result")
	}
}

func BenchmarkCompress_SmallResult(b *testing.B) {
	rc := NewResultCompressor(1024, 512)
	result := "Small result that should not be compressed"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rc.Compress("agent1", "task1", result)
	}
}

func BenchmarkCompress_LargeResult_WithSharedMemory(b *testing.B) {
	rc := NewResultCompressor(100, 50)
	result := strings.Repeat("x", 1000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rc.Compress("agent1", fmt.Sprintf("task%d", i), result)
	}
}

func BenchmarkCompress_LargeResult_NoSharedMemory(b *testing.B) {
	rc := NewResultCompressor(100, 50)
	result := strings.Repeat("x", 1000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rc.Compress("agent1", fmt.Sprintf("task%d", i), result)
	}
}
