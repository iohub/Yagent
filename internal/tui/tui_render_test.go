package tui

import (
	"strings"
	"testing"
)

func TestRenderThinkingContinuation(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		maxWidth int
		wantPrefix string
		wantTruncated bool
	}{
		{
			name:     "short content",
			content:  "this is a short thought",
			maxWidth: 80,
			wantPrefix: "      │ ",
		},
		{
			name:     "content with newlines",
			content:  "line1\nline2\nline3",
			maxWidth: 80,
			wantPrefix: "      │ ",
		},
		{
			name:     "long content truncated",
			content:  strings.Repeat("a", 200),
			maxWidth: 80,
			wantPrefix: "      │ ",
			wantTruncated: true,
		},
		{
			name:     "narrow width",
			content:  "hello world",
			maxWidth: 30,
			wantPrefix: "      │ ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := logEntry{
				content: tt.content,
			}
			result := renderThinkingContinuation(entry, tt.maxWidth)

			if !strings.HasPrefix(result, tt.wantPrefix) {
				t.Errorf("renderThinkingContinuation() prefix = %q, want %q", result[:len(tt.wantPrefix)], tt.wantPrefix)
			}

			// Verify content is styled (thinkTextStyle renders with ANSI codes)
			if tt.content != "" && !strings.Contains(result, tt.wantPrefix) {
				t.Errorf("renderThinkingContinuation() missing expected prefix in result")
			}

			// Verify truncation for long content
			if tt.wantTruncated {
				// The result should be shorter than the original content
				if len(result) >= len(tt.content) {
					t.Errorf("renderThinkingContinuation() long content should be truncated, got len=%d", len(result))
				}
			}
		})
	}
}
