package tui

import (
	"os"
	"path/filepath"
	"testing"

	"yagent/internal/dict"
)

func TestNewCompletionDict(t *testing.T) {
	d := dict.NewCompletionDict("test", nil)
	if d == nil {
		t.Fatal("NewCompletionDict should not return nil")
	}
	if d.Name() != "test" {
		t.Errorf("expected name 'test', got %q", d.Name())
	}
}

// TestCompleteEmptyPrefixSorted 是"空前缀 Complete 返回排序全量"的同一契约：
// 单批/多批/显式空前缀三种构造路径合并成一张表。
func TestCompleteEmptyPrefixSorted(t *testing.T) {
	tests := []struct {
		name  string
		batch [][]string
		want  []string
	}{
		{
			name:  "single batch sorted",
			batch: [][]string{{"banana", "apple", "cherry"}},
			want:  []string{"apple", "banana", "cherry"},
		},
		{
			name:  "multiple batches re-sorted",
			batch: [][]string{{"cherry"}, {"banana", "apple", "date"}},
			want:  []string{"apple", "banana", "cherry", "date"},
		},
		{
			name:  "empty prefix returns all within default limit",
			batch: [][]string{{"banana", "apple", "cherry"}},
			want:  []string{"apple", "banana", "cherry"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := dict.NewCompletionDict("test", nil)
			for _, words := range tt.batch {
				d.AddWords(words)
			}
			got := d.Complete("")
			if len(got) != len(tt.want) {
				t.Fatalf("Complete(\"\") = %v, want %v", got, tt.want)
			}
			for i, w := range got {
				if w != tt.want[i] {
					t.Errorf("word[%d] = %q, want %q", i, w, tt.want[i])
				}
			}
		})
	}
}

func TestAddWordDuplicate(t *testing.T) {
	d := dict.NewCompletionDict("test", nil)

	d.AddWords([]string{"apple", "apple"})

	// 去重验证（WordCount 删除后改用等价的 Complete 断言）
	got := d.Complete("")
	if len(got) != 1 || got[0] != "apple" {
		t.Errorf("expected [apple] (no duplicates), got %v", got)
	}
}

func TestComplete(t *testing.T) {
	d := dict.NewCompletionDict("test", nil)
	d.AddWords([]string{"apple", "application", "apply", "banana", "band", "cherry"})

	// 前缀匹配
	result := d.Complete("app")
	if len(result) != 3 {
		t.Errorf("expected 3 results for 'app', got %d: %v", len(result), result)
	}
	expected := []string{"apple", "application", "apply"}
	for i, w := range result {
		if w != expected[i] {
			t.Errorf("result[%d] = %q, expected %q", i, w, expected[i])
		}
	}

	// 大小写不敏感
	result = d.Complete("APP")
	if len(result) != 3 {
		t.Errorf("expected 3 results for 'APP', got %d: %v", len(result), result)
	}

	// 限制数量（dict.Complete 默认返回 20 个）
	result = d.Complete("a")
	if len(result) != 3 {
		t.Errorf("expected 3 results for 'a', got %d: %v", len(result), result)
	}

	// 无匹配
	result = d.Complete("xyz")
	if len(result) != 0 {
		t.Errorf("expected 0 results for 'xyz', got %d: %v", len(result), result)
	}
}

func TestLoadFromFile(t *testing.T) {
	// 创建临时词典文件
	tmpDir := t.TempDir()
	dictPath := filepath.Join(tmpDir, "dict.txt")
	content := `# 这是一个注释
apple
banana

  cherry  
# 另一个注释
date
`
	if err := os.WriteFile(dictPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	d := dict.NewCompletionDict("test", []string{dictPath})

	got := d.Complete("")
	expected := []string{"apple", "banana", "cherry", "date"}
	for i, w := range got {
		if w != expected[i] {
			t.Errorf("word[%d] = %q, expected %q", i, w, expected[i])
		}
	}
}

func TestLoadFromFileNotFound(t *testing.T) {
	// 文件不存在时，NewCompletionDict 会忽略该源
	// 所以不会报错，但词典为空
	d := dict.NewCompletionDict("test", []string{"/nonexistent/path/dict.txt"})
	// 不存在的源被忽略，词典保持为空（WordCount 删除后改用等价的 Complete 断言）
	if got := d.Complete(""); len(got) != 0 {
		t.Errorf("expected 0 words for nonexistent file, got %v", got)
	}
}
