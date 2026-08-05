package memory

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestNewJSONLWriter 测试创建 writer 时文件被正确创建，文件名格式符合 {timestamp}_{task_id}_{agent_name}_{task_hash}.jsonl
func TestNewJSONLWriter(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := MemoryJSONLConfig{
		Enable:    true,
		OutputDir: tmpDir,
	}

	writer, err := NewJSONLWriter(cfg, "test-project", "coding", "test task description", "test-task-id")
	if err != nil {
		t.Fatalf("NewJSONLWriter failed: %v", err)
	}
	defer writer.Close()

	// 验证 writer 已启用
	if !writer.Enabled() {
		t.Fatal("expected writer to be enabled")
	}

	// 验证文件路径不为空
	filePath := writer.FilePath()
	if filePath == "" {
		t.Fatal("expected file path to be non-empty")
	}

	// 验证文件存在
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		t.Fatalf("expected file to exist at %s", filePath)
	}

	// 验证 trace_id 非空
	if writer.TraceID() == "" {
		t.Error("expected non-empty trace_id")
	}

	// 验证文件名格式: {timestamp}_{task_id}_{agent_name}_{task_hash}.jsonl
	filename := filepath.Base(filePath)
	if !strings.HasSuffix(filename, ".jsonl") {
		t.Errorf("expected filename to end with .jsonl, got %s", filename)
	}

	// 验证文件名包含 task_id
	if !strings.Contains(filename, "test-task-id") {
		t.Errorf("expected filename to contain task_id 'test-task-id', got %s", filename)
	}

	// 验证文件名包含 agent_name
	if !strings.Contains(filename, "coding") {
		t.Errorf("expected filename to contain agent name 'coding', got %s", filename)
	}

	// 验证文件名包含 task_hash (8位hex)
	// 文件名格式: YYYYMMDD_HHMMSS_taskID_agentName_taskHash.jsonl
	filenamePattern := regexp.MustCompile(`^(\d{8}_\d{6})_(.+?)_(.+?)_(\w{8})\.jsonl$`)
	matches := filenamePattern.FindStringSubmatch(filename)
	if matches == nil {
		t.Errorf("filename does not match expected format: %s", filename)
	} else {
		// matches[3] is the task_hash
		taskHash := matches[4]
		if len(taskHash) != 8 {
			t.Errorf("expected task hash to be 8 characters, got %d: %s", len(taskHash), taskHash)
		}
		hexPattern := regexp.MustCompile(`^[0-9a-f]{8}$`)
		if !hexPattern.MatchString(taskHash) {
			t.Errorf("expected task hash to be hex, got %s", taskHash)
		}

		// 验证 taskHash 与 hashTask 结果一致
		expectedHash := hashTask("test task description")
		if taskHash != expectedHash {
			t.Errorf("expected taskHash to be %s, got %s", expectedHash, taskHash)
		}
	}

	// 验证文件名包含不同 task 的不同 hash
	cfg2 := MemoryJSONLConfig{
		Enable:    true,
		OutputDir: tmpDir,
	}
	writer2, err := NewJSONLWriter(cfg2, "test-project", "coding", "test task description 2", "test-task-id")
	if err != nil {
		t.Fatalf("NewJSONLWriter (second) failed: %v", err)
	}
	defer writer2.Close()

	filePath2 := writer2.FilePath()
	if filePath == filePath2 {
		t.Error("expected different file paths for different tasks")
	}
}

// TestWriteMessage 写入多条消息，验证文件内容行数正确，每行是有效 JSON
func TestWriteMessage(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := MemoryJSONLConfig{
		Enable:    true,
		OutputDir: tmpDir,
	}

	writer, err := NewJSONLWriter(cfg, "test-project", "coding", "test task", "test-task-id")
	if err != nil {
		t.Fatalf("NewJSONLWriter failed: %v", err)
	}
	defer writer.Close()

	// 写入多条消息
	messages := []interface{}{
		map[string]string{"role": "user", "content": "hello"},
		map[string]string{"role": "assistant", "content": "hi there"},
		map[string]interface{}{
			"role":    "assistant",
			"content": "detailed response",
			"metadata": map[string]int{
				"tokens": 100,
			},
		},
	}

	for i, msg := range messages {
		err := writer.WriteMessage(msg)
		if err != nil {
			t.Fatalf("WriteMessage(%d) failed: %v", i, err)
		}
	}

	// 验证文件行数
	fileContent, err := os.ReadFile(writer.FilePath())
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(fileContent)), "\n")
	if len(lines) != len(messages) {
		t.Errorf("expected %d lines, got %d", len(messages), len(lines))
	}

	// 验证每行是有效 JSON，且包含正确字段
	for i, line := range lines {
		var record MemoryRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Errorf("line %d is not valid JSON: %v", i, err)
			continue
		}

		// 验证基本字段
		if record.AgentName != "coding" {
			t.Errorf("line %d: expected agentName 'coding', got %s", i, record.AgentName)
		}

		// 验证 messageIdx
		if record.MessageIdx != i {
			t.Errorf("line %d: expected messageIdx %d, got %d", i, i, record.MessageIdx)
		}

		// 验证 timestamp 不为零
		if record.Timestamp.IsZero() {
			t.Errorf("line %d: expected non-zero timestamp", i)
		}

		// 验证 trace_id 非空
		if record.TraceID == "" {
			t.Errorf("line %d: expected non-empty trace_id", i)
		}

		// 验证 task_id 等于传入值
		if record.TaskID != "test-task-id" {
			t.Errorf("line %d: expected task_id 'test-task-id', got %s", i, record.TaskID)
		}
	}
}

// TestWriteMessage_Disabled 当 Enable=false 时，写入不创建文件
func TestWriteMessage_Disabled(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := MemoryJSONLConfig{
		Enable:    false,
		OutputDir: tmpDir,
	}

	writer, err := NewJSONLWriter(cfg, "test-project", "coding", "test task", "test-task-id")
	if err != nil {
		t.Fatalf("NewJSONLWriter failed: %v", err)
	}

	// 验证 writer 未启用
	if writer.Enabled() {
		t.Fatal("expected writer to be disabled")
	}

	// 验证文件路径为空
	if writer.FilePath() != "" {
		t.Errorf("expected empty file path for disabled writer, got %s", writer.FilePath())
	}

	// 验证写入不产生效果
	err = writer.WriteMessage(map[string]string{"test": "data"})
	if err != nil {
		t.Fatalf("WriteMessage should not fail for disabled writer, got: %v", err)
	}

	// 验证目录中没有创建文件
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("failed to read directory: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected no files in directory for disabled writer, got %d", len(entries))
	}
}

// TestWriteMessage_InvalidDir 输出目录无效时，降级返回 disabled writer 而非 panic
func TestWriteMessage_InvalidDir(t *testing.T) {
	// 使用一个绝对不可能的路径
	invalidDir := "/nonexistent/deeply/nested/path/that/does/not/exist/and/cannot/be/created"

	cfg := MemoryJSONLConfig{
		Enable:    true,
		OutputDir: invalidDir,
	}

	// 这应该返回一个 disabled writer 而不是 panic
	writer, err := NewJSONLWriter(cfg, "test-project", "coding", "test task", "test-task-id")
	if err == nil {
		t.Fatal("expected error for invalid directory")
	}

	// 验证返回的 writer 是 disabled 的
	if writer == nil {
		t.Fatal("expected non-nil writer even on error")
	}

	if writer.Enabled() {
		t.Fatal("expected writer to be disabled after invalid directory error")
	}

	// 验证验证文件路径为空
	if writer.FilePath() != "" {
		t.Errorf("expected empty file path for invalid directory, got %s", writer.FilePath())
	}

	// 验证写入不会产生 panic
	err = writer.WriteMessage(map[string]string{"test": "data"})
	if err != nil {
		t.Fatalf("WriteMessage should not fail for disabled writer, got: %v", err)
	}
}

// TestClose 测试 Close 后文件可被正常读取
func TestClose(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := MemoryJSONLConfig{
		Enable:    true,
		OutputDir: tmpDir,
	}

	writer, err := NewJSONLWriter(cfg, "test-project", "coding", "test task", "test-task-id")
	if err != nil {
		t.Fatalf("NewJSONLWriter failed: %v", err)
	}

	// 写入一些消息
	messages := []interface{}{
		map[string]string{"role": "user", "content": "message 1"},
		map[string]string{"role": "assistant", "content": "response 1"},
	}

	for _, msg := range messages {
		if err := writer.WriteMessage(msg); err != nil {
			t.Fatalf("WriteMessage failed: %v", err)
		}
	}

	filePath := writer.FilePath()

	// 关闭 writer
	if err := writer.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// 验证关闭后可以正常读取文件
	fileContent, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed to read file after close: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(fileContent)), "\n")
	if len(lines) != len(messages) {
		t.Errorf("expected %d lines, got %d", len(messages), len(lines))
	}

	// 验证再次 Close 不会产生错误
	if err := writer.Close(); err != nil {
		t.Fatalf("second Close should not fail, got: %v", err)
	}

	// 验证再次写入不会产生 panic
	err = writer.WriteMessage(map[string]string{"test": "after close"})
	if err != nil {
		t.Fatalf("WriteMessage after close should not fail, got: %v", err)
	}
}

// TestWithJSONLWriter_Context 测试 Context 注入和取出正确
func TestWithJSONLWriter_Context(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := MemoryJSONLConfig{
		Enable:    true,
		OutputDir: tmpDir,
	}

	writer, err := NewJSONLWriter(cfg, "test-project", "coding", "test task", "test-task-id")
	if err != nil {
		t.Fatalf("NewJSONLWriter failed: %v", err)
	}
	defer writer.Close()

	// 测试基本注入和取出
	ctx := context.Background()
	retrieved := GetJSONLWriter(ctx)
	if retrieved != nil {
		t.Fatal("expected nil writer from empty context")
	}

	ctx = WithJSONLWriter(ctx, writer)
	retrieved = GetJSONLWriter(ctx)
	if retrieved == nil {
		t.Fatal("expected non-nil writer after injection")
	}
	if retrieved != writer {
		t.Error("retrieved writer is not the same as the injected writer")
	}

	// 测试 nil writer 不注入
	ctx2 := WithJSONLWriter(context.Background(), nil)
	if GetJSONLWriter(ctx2) != nil {
		t.Fatal("expected nil writer when injecting nil")
	}

	// 测试 disabled writer 不注入
	disabledWriter := &JSONLWriter{enabled: false}
	ctx3 := WithJSONLWriter(context.Background(), disabledWriter)
	if GetJSONLWriter(ctx3) != nil {
		t.Fatal("expected nil writer when injecting disabled writer")
	}

	// 测试嵌套 context
	ctxNested := context.WithValue(ctx, "key", "value")
	retrieved = GetJSONLWriter(ctxNested)
	if retrieved == nil {
		t.Fatal("expected non-nil writer from nested context")
	}
}

// TestHashTask 测试 hashTask 输出长度固定为8位hex
func TestHashTask(t *testing.T) {
	testCases := []struct {
		name     string
		task     string
		expected int
	}{
		{"empty string", "", 8},
		{"short task", "hi", 8},
		{"long task", "this is a very long task description with many words to test hash consistency", 8},
		{"unicode", "任务描述 with unicode 测试", 8},
		{"special chars", "task with !@#$%^&*() chars", 8},
		{"whitespace", "  task with leading and trailing spaces  ", 8},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := hashTask(tc.task)

			// 验证长度固定为8
			if len(result) != tc.expected {
				t.Errorf("expected length %d, got %d: %s", tc.expected, len(result), result)
			}

			// 验证是有效的hex字符串
			hexPattern := regexp.MustCompile(`^[0-9a-f]+$`)
			if !hexPattern.MatchString(result) {
				t.Errorf("expected hex string, got %s", result)
			}

			// 验证相同输入产生相同输出
			result2 := hashTask(tc.task)
			if result != result2 {
				t.Errorf("hashTask is not deterministic: %s != %s", result, result2)
			}
		})
	}

	// 测试不同输入产生不同输出
	t.Run("different inputs produce different hashes", func(t *testing.T) {
		hash1 := hashTask("task one")
		hash2 := hashTask("task two")
		if hash1 == hash2 {
			t.Errorf("different tasks produced same hash: %s", hash1)
		}
	})

	// 测试 MD5 前8位的一致性
	t.Run("hash is first 8 chars of MD5", func(t *testing.T) {
		task := "test task"
		result := hashTask(task)

		// 手动计算 MD5
		hash := md5.Sum([]byte(task))
		fullHash := hex.EncodeToString(hash[:])
		expected := fullHash[:8]

		if result != expected {
			t.Errorf("expected %s (first 8 of MD5), got %s", expected, result)
		}
	})
}
