package tui

import (
	"fmt"
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

func TestAddWord(t *testing.T) {
	d := dict.NewCompletionDict("test", nil)

	d.AddWords([]string{"banana", "apple", "cherry"})

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

	// 去重验证（WordCount 删除后改用等价的 Complete 断言）
	got := d.Complete("")
	if len(got) != 1 || got[0] != "apple" {
		t.Errorf("expected [apple] (no duplicates), got %v", got)
	}
}

func TestAddWords(t *testing.T) {
	d := dict.NewCompletionDict("test", nil)

	d.AddWords([]string{"cherry"})
	d.AddWords([]string{"banana", "apple", "date"})

	got := d.Complete("")
	if len(got) != 4 {
		t.Errorf("expected 4 words, got %d", len(got))
	}
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
}
