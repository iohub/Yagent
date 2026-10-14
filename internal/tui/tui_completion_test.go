package tui

import (
	"testing"
)

// TestExtractWordAtCursorRunes 是 extractWordAtCursorRunes 的唯一表驱动测试：
// 合并了旧的 TestExtractWordAtCursor / TestExtractWordAtCursorRunes /
// TestExtractWordAtCursor_Integration 三份同函数用例。
func TestExtractWordAtCursorRunes(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		cursorPos int
		wantWord  string
	}{
		{name: "simple word", content: "hello world", cursorPos: 5, wantWord: "hello"},
		{name: "chinese word", content: "你好世界", cursorPos: 2, wantWord: "你好"},
		{name: "mixed content", content: "hello 世界 world", cursorPos: 8, wantWord: "世界"},
		{name: "at start", content: "hello", cursorPos: 0, wantWord: ""},
		{name: "empty content", content: "", cursorPos: 0, wantWord: ""},
		{name: "word with underscore", content: "my_variable", cursorPos: 11, wantWord: "my_variable"},
		{name: "longer underscore word", content: "my_variable_name", cursorPos: 16, wantWord: "my_variable_name"},
		{name: "alphanumeric", content: "test123", cursorPos: 7, wantWord: "test123"},
		{name: "digits only", content: "12345", cursorPos: 5, wantWord: "12345"},
		{name: "multiple spaces", content: "hello   world", cursorPos: 13, wantWord: "world"},
		{name: "cursor at end of content", content: "hello", cursorPos: 5, wantWord: "hello"},
		{name: "second word at end", content: "hello world", cursorPos: 11, wantWord: "world"},
		{name: "cursor out of bounds", content: "hello", cursorPos: 10, wantWord: ""},
		{name: "negative cursor", content: "hello", cursorPos: -1, wantWord: ""},
		{name: "word with special char stops", content: "hello-world", cursorPos: 11, wantWord: "world"},
		{name: "tab character stops", content: "hello\tworld", cursorPos: 11, wantWord: "world"},
		{name: "newline stops", content: "hello\nworld", cursorPos: 11, wantWord: "world"},
		// 集成路径：中英混排串的三个游标位（旧 TestExtractWordAtCursor_Integration）
		{name: "mixed tail: end of hello", content: "hello world 测试", cursorPos: 5, wantWord: "hello"},
		{name: "mixed tail: end of world", content: "hello world 测试", cursorPos: 11, wantWord: "world"},
		{name: "mixed tail: end of 测试", content: "hello world 测试", cursorPos: 14, wantWord: "测试"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractWordAtCursorRunes([]rune(tt.content), tt.cursorPos)
			if got != tt.wantWord {
				t.Errorf("extractWordAtCursorRunes(%q, %d) = %q, want %q", tt.content, tt.cursorPos, got, tt.wantWord)
			}
		})
	}
}

// TestSplitLogicalLines 是 splitLogicalLines 的唯一表驱动测试：
// 合并了旧的 TestSplitLogicalLines / TestSplitLogicalLines_EdgeCases。
func TestSplitLogicalLines(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		count       int
		wantLines   int
		wantContent []string
	}{
		{name: "single line no newline", content: "hello", count: 1, wantLines: 1, wantContent: []string{"hello"}},
		{name: "two lines get first", content: "line1\nline2", count: 1, wantLines: 1, wantContent: []string{"line1"}},
		{name: "three lines get two", content: "line1\nline2\nline3", count: 2, wantLines: 2, wantContent: []string{"line1", "line2"}},
		{name: "count zero returns nil", content: "hello\nworld", count: 0, wantLines: 0},
		{name: "negative count returns nil", content: "hello\nworld", count: -1, wantLines: 0},
		{name: "empty content", content: "", count: 1, wantLines: 0},
		{name: "trailing newline", content: "line1\n", count: 1, wantLines: 1, wantContent: []string{"line1"}},
		{name: "multiple newlines", content: "a\nb\nc\nd", count: 3, wantLines: 3, wantContent: []string{"a", "b", "c"}},
		{name: "count larger than available", content: "line1\nline2", count: 10, wantLines: 2, wantContent: []string{"line1", "line2"}},
		{name: "two empty lines", content: "\n\n", count: 3, wantLines: 2, wantContent: []string{"", ""}},
		{name: "content without trailing newline", content: "line1\nline2\nline3", count: 3, wantLines: 3, wantContent: []string{"line1", "line2", "line3"}},
		{name: "unicode content", content: "你好\n世界\n测试", count: 2, wantLines: 2, wantContent: []string{"你好", "世界"}},
		{name: "early termination", content: "line1\nline2\nline3\nline4\nline5", count: 2, wantLines: 2, wantContent: []string{"line1", "line2"}},
		{name: "all lines", content: "line1\nline2\nline3\nline4\nline5", count: 10, wantLines: 5, wantContent: []string{"line1", "line2", "line3", "line4", "line5"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitLogicalLines([]rune(tt.content), tt.count)

			if len(got) != tt.wantLines {
				t.Fatalf("splitLogicalLines(%q, %d) = %d lines, want %d", tt.content, tt.count, len(got), tt.wantLines)
			}
			for i, wantLine := range tt.wantContent {
				if gotStr := string(got[i]); gotStr != wantLine {
					t.Errorf("splitLogicalLines(%q, %d) line %d = %q, want %q", tt.content, tt.count, i, gotStr, wantLine)
				}
			}
		})
	}
}

// TestHasPrefixIgnoreCase 是 hasPrefixIgnoreCase 的唯一表驱动测试：
// 合并了旧的 TestHasPrefixIgnoreCase / TestHasPrefixIgnoreCase_ASCIIOnly。
// 注意：该函数只做 ASCII 大小写折叠，非 ASCII 按原样比较。
func TestHasPrefixIgnoreCase(t *testing.T) {
	tests := []struct {
		name   string
		s      string
		prefix string
		want   bool
	}{
		{name: "exact match", s: "hello", prefix: "hello", want: true},
		{name: "case insensitive uppercase s", s: "Hello", prefix: "hello", want: true},
		{name: "case insensitive all caps s", s: "HELLO", prefix: "hello", want: true},
		{name: "case insensitive uppercase prefix", s: "hello", prefix: "HELLO", want: true},
		{name: "case insensitive both mixed", s: "HeLLo", prefix: "hELlo", want: true},
		{name: "prefix match full string", s: "helloworld", prefix: "hello", want: true},
		{name: "prefix match case", s: "HelloWorld", prefix: "hello", want: true},
		{name: "no match different string", s: "world", prefix: "hello", want: false},
		{name: "no match different beginning", s: "hello", prefix: "world", want: false},
		{name: "empty prefix matches all", s: "hello", prefix: "", want: true},
		{name: "both empty strings", s: "", prefix: "", want: true},
		{name: "prefix longer than string", s: "hi", prefix: "hello", want: false},
		{name: "empty string with non-empty prefix", s: "", prefix: "hello", want: false},
		{name: "single character match", s: "a", prefix: "A", want: true},
		{name: "single character no match", s: "a", prefix: "b", want: false},
		{name: "numbers only", s: "12345", prefix: "123", want: true},
		{name: "mixed alphanumeric", s: "Test123", prefix: "test", want: true},
		{name: "chinese exact match", s: "你好世界", prefix: "你好", want: true},
		{name: "chinese no match", s: "你好世界", prefix: "世界", want: false},
		{name: "ascii prefix on mixed content", s: "Hello世界", prefix: "hello", want: true},
		{name: "underscore in string", s: "hello_world", prefix: "hello", want: true},
		{name: "special characters", s: "hello-world", prefix: "hello", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasPrefixIgnoreCase(tt.s, tt.prefix)
			if got != tt.want {
				t.Errorf("hasPrefixIgnoreCase(%q, %q) = %v, want %v", tt.s, tt.prefix, got, tt.want)
			}
		})
	}
}
