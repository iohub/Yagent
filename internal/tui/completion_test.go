package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"codeactor/internal/dict"
)

func TestNewCompletionDict(t *testing.T) {
	d := dict.NewCompletionDict("test", nil)
	if d == nil {
		t.Fatal("NewCompletionDict should not return nil")
	}
	if d.WordCount() != 0 {
		t.Errorf("expected 0 words, got %d", d.WordCount())
	}
	if d.Name() != "test" {
		t.Errorf("expected name 'test', got %q", d.Name())
	}
}

func TestAddWord(t *testing.T) {
	d := dict.NewCompletionDict("test", nil)

	d.AddWords([]string{"banana", "apple", "cherry"})

	if d.WordCount() != 3 {
		t.Errorf("expected 3 words, got %d", d.WordCount())
	}

	// 验证已排序
	got := d.Complete("")
	expected := []string{"apple", "banana", "cherry"}
	for i, w := range got {
		if w != expected[i] {
			t.Errorf("word[%d] = %q, expected %q", i, w, expected[i])
		}
	}
}

func TestAddWordDuplicate(t *testing.T) {
	d := dict.NewCompletionDict("test", nil)

	d.AddWords([]string{"apple", "apple"})

	if d.WordCount() != 1 {
		t.Errorf("expected 1 word (no duplicates), got %d", d.WordCount())
	}
}

func TestAddWords(t *testing.T) {
	d := dict.NewCompletionDict("test", nil)

	d.AddWords([]string{"cherry"})
	d.AddWords([]string{"banana", "apple", "date"})

	if d.WordCount() != 4 {
		t.Errorf("expected 4 words, got %d", d.WordCount())
	}

	got := d.Complete("")
	expected := []string{"apple", "banana", "cherry", "date"}
	for i, w := range got {
		if w != expected[i] {
			t.Errorf("word[%d] = %q, expected %q", i, w, expected[i])
		}
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

func TestCompleteEmpty(t *testing.T) {
	d := dict.NewCompletionDict("test", nil)
	d.AddWords([]string{"banana", "apple", "cherry"})

	// 空前缀返回前 20 个（默认限制）
	result := d.Complete("")
	if len(result) != 3 {
		t.Errorf("expected 3 results for empty prefix, got %d: %v", len(result), result)
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

	if d.WordCount() != 4 {
		t.Errorf("expected 4 words, got %d", d.WordCount())
	}

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
	if d.WordCount() != 0 {
		t.Errorf("expected 0 words for nonexistent file, got %d", d.WordCount())
	}
}

func TestClear(t *testing.T) {
	d := dict.NewCompletionDict("test", nil)
	d.AddWords([]string{"apple", "banana"})

	d.Clear()

	if d.WordCount() != 0 {
		t.Errorf("expected 0 words after clear, got %d", d.WordCount())
	}
}

func TestLoadFromData(t *testing.T) {
	d := dict.NewCompletionDict("test", nil)
	content := []byte(`# 注释
hello
world
`)
	if err := d.LoadFromData(content); err != nil {
		t.Fatalf("LoadFromData failed: %v", err)
	}

	if d.WordCount() != 2 {
		t.Errorf("expected 2 words, got %d", d.WordCount())
	}
}

func TestDefaultKeywords(t *testing.T) {
	keywords := dict.DefaultKeywords()
	if len(keywords) == 0 {
		t.Error("DefaultKeywords should return non-empty list")
	}

	// 验证常见关键词存在
	expectedKeywords := []string{"agent", "api", "async", "auth", "build", "cache"}
	for _, kw := range expectedKeywords {
		found := false
		for _, k := range keywords {
			if k == kw {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected default keyword %q not found", kw)
		}
	}
}

func TestConcurrency(t *testing.T) {
	d := dict.NewCompletionDict("test", nil)

	// 并发添加
	done := make(chan bool)
	for i := 0; i < 100; i++ {
		go func(n int) {
			word := fmt.Sprintf("word%d", n)
			d.AddWords([]string{word})
			done <- true
		}(i)
	}

	// 并发搜索
	for i := 0; i < 10; i++ {
		go func() {
			d.Complete("word")
		}()
	}

	// 等待所有添加完成
	for i := 0; i < 100; i++ {
		<-done
	}

	if d.WordCount() != 100 {
		t.Errorf("expected 100 words, got %d", d.WordCount())
	}
}
