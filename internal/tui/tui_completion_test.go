package tui

import (
	"testing"
)

// ============================================================================
// TestExtractWordAtCursor - 测试 extractWordAtCursorRunes 函数
// ============================================================================

func TestExtractWordAtCursor(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		cursorPos int
		wantWord  string
	}{
		{
			name:      "simple word",
			content:   "hello world",
			cursorPos: 5,
			wantWord:  "hello",
		},
		{
			name:      "chinese word",
			content:   "你好世界",
			cursorPos: 2, // rune index 2 (after "你好")
			wantWord:  "你好",
		},
		{
			name:      "mixed content",
			content:   "hello 世界 world",
			cursorPos: 8, // rune index 8 (after "世界": hello=5 + space=1 + 世界=2)
			wantWord:  "世界",
		},
		{
			name:      "at start",
			content:   "hello",
			cursorPos: 0,
			wantWord:  "",
		},
		{
			name:      "empty content",
			content:   "",
			cursorPos: 0,
			wantWord:  "",
		},
		{
			name:      "word with underscore",
			content:   "my_variable",
			cursorPos: 11,
			wantWord:  "my_variable",
		},
		{
			name:      "alphanumeric",
			content:   "test123",
			cursorPos: 7,
			wantWord:  "test123",
		},
		{
			name:      "multiple spaces",
			content:   "hello   world",
			cursorPos: 13, // rune index 13 (at end of string, after "world")
			wantWord:  "world",
		},
		{
			name:      "cursor at end of content",
			content:   "hello",
			cursorPos: 5,
			wantWord:  "hello",
		},
		{
			name:      "cursor out of bounds",
			content:   "hello",
			cursorPos: 10,
			wantWord:  "",
		},
		{
			name:      "negative cursor",
			content:   "hello",
			cursorPos: -1,
			wantWord:  "",
		},
		{
			name:      "word with special char stops",
			content:   "hello-world",
			cursorPos: 11,
			wantWord:  "world",
		},
		{
			name:      "tab character stops",
			content:   "hello\tworld",
			cursorPos: 11,
			wantWord:  "world",
		},
		{
			name:      "newline stops",
			content:   "hello\nworld",
			cursorPos: 11,
			wantWord:  "world",
		},
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

// ============================================================================
// TestExtractWordAtCursorRunes - 测试 extractWordAtCursorRunes 函数
// ============================================================================

func TestExtractWordAtCursorRunes(t *testing.T) {
	tests := []struct {
		name      string
		runes     []rune
		cursorPos int
		wantWord  string
	}{
		{
			name:      "simple word",
			runes:     []rune("hello world"),
			cursorPos: 5,
			wantWord:  "hello",
		},
		{
			name:      "chinese word",
			runes:     []rune("你好世界"),
			cursorPos: 2,
			wantWord:  "你好",
		},
		{
			name:      "empty runes",
			runes:     []rune(""),
			cursorPos: 0,
			wantWord:  "",
		},
		{
			name:      "cursor at start",
			runes:     []rune("hello"),
			cursorPos: 0,
			wantWord:  "",
		},
		{
			name:      "cursor at end",
			runes:     []rune("hello"),
			cursorPos: 5,
			wantWord:  "hello",
		},
		{
			name:      "cursor out of bounds",
			runes:     []rune("hello"),
			cursorPos: 10,
			wantWord:  "",
		},
		{
			name:      "negative cursor",
			runes:     []rune("hello"),
			cursorPos: -1,
			wantWord:  "",
		},
		{
			name:      "mixed alphanumeric and underscore",
			runes:     []rune("my_variable_name"),
			cursorPos: 16,
			wantWord:  "my_variable_name",
		},
		{
			name:      "mixed content with spaces",
			runes:     []rune("hello world"),
			cursorPos: 11,
			wantWord:  "world",
		},
		{
			name:      "digits only",
			runes:     []rune("12345"),
			cursorPos: 5,
			wantWord:  "12345",
		},
		{
			name:      "special characters stop extraction",
			runes:     []rune("hello-world"),
			cursorPos: 11,
			wantWord:  "world",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractWordAtCursorRunes(tt.runes, tt.cursorPos)
			if got != tt.wantWord {
				t.Errorf("extractWordAtCursorRunes(%v, %d) = %q, want %q", tt.runes, tt.cursorPos, got, tt.wantWord)
			}
		})
	}
}

// ============================================================================
// TestSplitLogicalLines - 测试 splitLogicalLines 函数
// ============================================================================

func TestSplitLogicalLines(t *testing.T) {
	tests := []struct {
		name        string
		content     []rune
		count       int
		wantLines   int
		wantContent []string // Each element is one line's content as string
	}{
		{
			name:        "single line no newline",
			content:     []rune("hello"),
			count:       1,
			wantLines:   1,
			wantContent: []string{"hello"},
		},
		{
			name:        "two lines get first",
			content:     []rune("line1\nline2"),
			count:       1,
			wantLines:   1,
			wantContent: []string{"line1"},
		},
		{
			name:        "three lines get two",
			content:     []rune("line1\nline2\nline3"),
			count:       2,
			wantLines:   2,
			wantContent: []string{"line1", "line2"},
		},
		{
			name:        "count zero returns nil",
			content:     []rune("hello\nworld"),
			count:       0,
			wantLines:   0,
			wantContent: nil,
		},
		{
			name:        "negative count returns nil",
			content:     []rune("hello\nworld"),
			count:       -1,
			wantLines:   0,
			wantContent: nil,
		},
		{
			name:        "empty content",
			content:     []rune(""),
			count:       1,
			wantLines:   0,
			wantContent: nil,
		},
		{
			name:        "trailing newline",
			content:     []rune("line1\n"),
			count:       1,
			wantLines:   1,
			wantContent: []string{"line1"},
		},
		{
			name:        "multiple newlines",
			content:     []rune("a\nb\nc\nd"),
			count:       3,
			wantLines:   3,
			wantContent: []string{"a", "b", "c"},
		},
		{
			name:        "count larger than available",
			content:     []rune("line1\nline2"),
			count:       10,
			wantLines:   2,
			wantContent: []string{"line1", "line2"},
		},
		{
			name:        "two empty lines",
			content:     []rune("\n\n"),
			count:       3,
			wantLines:   2, // "\n\n" produces only 2 empty lines
			wantContent: []string{"", ""},
		},
		{
			name:        "content without trailing newline",
			content:     []rune("line1\nline2\nline3"),
			count:       3,
			wantLines:   3,
			wantContent: []string{"line1", "line2", "line3"},
		},
		{
			name:        "unicode content",
			content:     []rune("你好\n世界\n测试"),
			count:       2,
			wantLines:   2,
			wantContent: []string{"你好", "世界"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitLogicalLines(tt.content, tt.count)

			// Check length
			if len(got) != tt.wantLines {
				t.Errorf("splitLogicalLines(%v, %d) returned %d lines, want %d", tt.content, tt.count, len(got), tt.wantLines)
			}

			// Check content if we have expected values
			if tt.wantContent != nil && len(got) > 0 {
				for i, wantLine := range tt.wantContent {
					if i >= len(got) {
						break
					}
					gotStr := string(got[i])
					if gotStr != wantLine {
						t.Errorf("splitLogicalLines() line %d = %q, want %q", i, gotStr, wantLine)
					}
				}
			}

			// Verify nil check
			if tt.wantLines == 0 && got != nil {
				// nil or empty slice both acceptable for 0 lines
				if len(got) != 0 {
					t.Errorf("splitLogicalLines() returned non-empty slice for count 0")
				}
			}
		})
	}
}

// ============================================================================
// TestHasPrefixIgnoreCase - 测试 hasPrefixIgnoreCase 函数
// ============================================================================

func TestHasPrefixIgnoreCase(t *testing.T) {
	tests := []struct {
		name   string
		s      string
		prefix string
		want   bool
	}{
		{
			name:   "exact match",
			s:      "hello",
			prefix: "hello",
			want:   true,
		},
		{
			name:   "case insensitive uppercase s",
			s:      "Hello",
			prefix: "hello",
			want:   true,
		},
		{
			name:   "case insensitive all caps s",
			s:      "HELLO",
			prefix: "hello",
			want:   true,
		},
		{
			name:   "case insensitive uppercase prefix",
			s:      "hello",
			prefix: "HELLO",
			want:   true,
		},
		{
			name:   "case insensitive both mixed",
			s:      "HeLLo",
			prefix: "hELlo",
			want:   true,
		},
		{
			name:   "prefix match full string",
			s:      "helloworld",
			prefix: "hello",
			want:   true,
		},
		{
			name:   "prefix match case",
			s:      "HelloWorld",
			prefix: "hello",
			want:   true,
		},
		{
			name:   "no match different string",
			s:      "world",
			prefix: "hello",
			want:   false,
		},
		{
			name:   "no match different beginning",
			s:      "hello",
			prefix: "world",
			want:   false,
		},
		{
			name:   "empty prefix matches all",
			s:      "hello",
			prefix: "",
			want:   true,
		},
		{
			name:   "both empty strings",
			s:      "",
			prefix: "",
			want:   true,
		},
		{
			name:   "prefix longer than string",
			s:      "hi",
			prefix: "hello",
			want:   false,
		},
		{
			name:   "empty string with non-empty prefix",
			s:      "",
			prefix: "hello",
			want:   false,
		},
		{
			name:   "single character match",
			s:      "a",
			prefix: "A",
			want:   true,
		},
		{
			name:   "single character no match",
			s:      "a",
			prefix: "b",
			want:   false,
		},
		{
			name:   "numbers only",
			s:      "12345",
			prefix: "123",
			want:   true,
		},
		{
			name:   "mixed alphanumeric",
			s:      "Test123",
			prefix: "test",
			want:   true,
		},
		// Note: hasPrefixIgnoreCase only handles ASCII case conversion
		// Chinese characters are compared byte-for-byte without case conversion
		{
			name:   "chinese exact match",
			s:      "你好世界",
			prefix: "你好",
			want:   true,
		},
		{
			name:   "chinese no match",
			s:      "你好世界",
			prefix: "世界",
			want:   false,
		},
		{
			name:   "underscore in string",
			s:      "hello_world",
			prefix: "hello",
			want:   true,
		},
		{
			name:   "special characters",
			s:      "hello-world",
			prefix: "hello",
			want:   true,
		},
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

// ============================================================================
// Integration Tests - 集成测试
// ============================================================================

func TestExtractWordAtCursor_Integration(t *testing.T) {
	// Test that extractWordAtCursorRunes correctly extracts words from rune slices
	content := "hello world 测试"
	// Total rune length: hello(5) + space(1) + world(5) + space(1) + 测试(2) = 14

	runes := []rune(content)

	// Position at end of "hello"
	if got := extractWordAtCursorRunes(runes, 5); got != "hello" {
		t.Errorf("expected 'hello', got %q", got)
	}

	// Position at end of "world" (rune position 11)
	if got := extractWordAtCursorRunes(runes, 11); got != "world" {
		t.Errorf("expected 'world', got %q", got)
	}

	// Position at end of "测试" (rune position 14 = string length)
	if got := extractWordAtCursorRunes(runes, 14); got != "测试" {
		t.Errorf("expected '测试', got %q", got)
	}
}

func TestSplitLogicalLines_EdgeCases(t *testing.T) {
	// Test that splitLogicalLines properly handles early termination
	content := []rune("line1\nline2\nline3\nline4\nline5")

	// Request only 2 lines
	lines := splitLogicalLines(content, 2)
	if len(lines) != 2 {
		t.Errorf("expected 2 lines, got %d", len(lines))
	}
	if string(lines[0]) != "line1" {
		t.Errorf("expected 'line1', got %q", string(lines[0]))
	}
	if string(lines[1]) != "line2" {
		t.Errorf("expected 'line2', got %q", string(lines[1]))
	}

	// Request all lines
	lines = splitLogicalLines(content, 10)
	if len(lines) != 5 {
		t.Errorf("expected 5 lines, got %d", len(lines))
	}
}

func TestHasPrefixIgnoreCase_ASCIIOnly(t *testing.T) {
	// hasPrefixIgnoreCase only handles ASCII case conversion
	// Non-ASCII characters are compared byte-for-byte

	// ASCII mixed case
	if !hasPrefixIgnoreCase("Hello", "hello") {
		t.Error("expected ASCII case insensitivity to work")
	}

	// Chinese characters don't have case, so exact match required
	if !hasPrefixIgnoreCase("你好世界", "你好") {
		t.Error("expected exact Chinese prefix match")
	}

	// Mixed ASCII and non-ASCII
	if !hasPrefixIgnoreCase("Hello世界", "hello") {
		t.Error("expected ASCII prefix to match mixed content")
	}
}
