package tui

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

// TestDashboardBorder_AllLinesExactWidth verifies that renderDashboard produces
// exactly `height` lines, each with lipgloss.Width == `width`, with complete
// top/bottom/left/right borders.
func TestDashboardBorder_AllLinesExactWidth(t *testing.T) {
	initLangManager()

	m := &model{
		termWidth:            140,
		termHeight:           40,
		taskRunning:          true,
		inputTokens:          1234000,
		outputTokens:         567000,
		cacheReadInputTokens: 100000,
		cacheCreationInputTokens: 50000,
		tokenUsagePerAgent: map[string]*AgentTokenUsage{
			"Director": {
				AgentName:                "Director",
				InputTokens:              500000,
				OutputTokens:             200000,
				CacheReadInputTokens:     50000,
				CacheCreationInputTokens: 20000,
			},
			"Coder": {
				AgentName:                "Coder",
				InputTokens:              300000,
				OutputTokens:             150000,
				CacheReadInputTokens:     30000,
				CacheCreationInputTokens: 10000,
			},
		},
		timelineEntries:        []*TimelineEntry{},
		dashboardCollapsed:     false,
	}

	const width = dashboardPanelWidth // 46
	const height = 30

	result := m.renderDashboard(width, height)
	lines := strings.Split(result, "\n")

	if len(lines) != height {
		t.Fatalf("expected %d lines, got %d", height, len(lines))
	}

	for i, line := range lines {
		w := lipgloss.Width(line)
		if w != width {
			t.Errorf("line %d: expected width %d, got %d (content=%q)", i, width, w, line)
		}
	}

	// First line must be top border
	if !strings.HasPrefix(stripANSI(lines[0]), "┌") {
		t.Errorf("first line should start with '┌', got: %q", lines[0])
	}
	if !strings.HasSuffix(stripANSI(lines[0]), "┐") {
		t.Errorf("first line should end with '┐', got: %q", lines[0])
	}

	// Last line must be bottom border
	if !strings.HasPrefix(stripANSI(lines[height-1]), "└") {
		t.Errorf("last line should start with '└', got: %q", lines[height-1])
	}
	if !strings.HasSuffix(stripANSI(lines[height-1]), "┘") {
		t.Errorf("last line should end with '┘', got: %q", lines[height-1])
	}

	// Every line must have left border ('│' or corner) and right border
	for i, line := range lines {
		s := stripANSI(line)
		runes := []rune(s)
		first := string(runes[0])
		last := string(runes[len(runes)-1])
		if first != "│" && first != "┌" && first != "└" {
			t.Errorf("line %d: expected left border char, got %q", i, first)
		}
		if last != "│" && last != "┐" && last != "┘" {
			t.Errorf("line %d: expected right border char, got %q", i, last)
		}
	}
}

// TestDashboardBorder_ShortContent verifies padding lines have borders when
// content is shorter than height.
func TestDashboardBorder_ShortContent(t *testing.T) {
	initLangManager()

	m := &model{
		termWidth:            140,
		termHeight:           40,
		taskRunning:          false,
		inputTokens:          100,
		outputTokens:         50,
		cacheReadInputTokens: 0,
		cacheCreationInputTokens: 0,
		tokenUsagePerAgent:   map[string]*AgentTokenUsage{},
		timelineEntries:      []*TimelineEntry{},
		dashboardCollapsed:   false,
	}

	const width = dashboardPanelWidth
	const height = 30

	result := m.renderDashboard(width, height)
	lines := strings.Split(result, "\n")

	if len(lines) != height {
		t.Fatalf("expected %d lines, got %d", height, len(lines))
	}

	// All lines should have borders (no plain-space padding lines)
	for i, line := range lines {
		w := lipgloss.Width(line)
		if w != width {
			t.Errorf("line %d: expected width %d, got %d", i, width, w)
		}
		// No line should be all spaces
		if strings.TrimSpace(line) == "" {
			t.Errorf("line %d: expected bordered line, got all-spaces", i)
		}
		// Every line should start and end with border chars
		s := stripANSI(line)
		if !strings.HasPrefix(s, "│") && !strings.HasPrefix(s, "┌") && !strings.HasPrefix(s, "└") {
			t.Errorf("line %d: expected border prefix, got: %q", i, stripANSI(line[:min(10, len(line))]))
		}
		if !strings.HasSuffix(s, "│") && !strings.HasSuffix(s, "┐") && !strings.HasSuffix(s, "┘") {
			t.Errorf("line %d: expected border suffix, got: %q", i, stripANSI(line[max(0, len(line)-10):]))
		}
	}

	if !strings.HasPrefix(stripANSI(lines[0]), "┌") {
		t.Errorf("first line should start with '┌', got: %q", lines[0])
	}
	if !strings.HasPrefix(stripANSI(lines[height-1]), "└") {
		t.Errorf("last line should start with '└', got: %q", lines[height-1])
	}
}

// TestDashboardBorder_OverflowContent verifies that when content exceeds height,
// the result is truncated to exactly height lines and the bottom border is
// preserved.
func TestDashboardBorder_OverflowContent(t *testing.T) {
	initLangManager()

	// Create many agents to force overflow
	agents := make(map[string]*AgentTokenUsage)
	for i := 0; i < 20; i++ {
		name := fmt.Sprintf("Agent%d", i)
		agents[name] = &AgentTokenUsage{
			AgentName:                name,
			InputTokens:              int64(1000 + i*100),
			OutputTokens:             int64(500 + i*50),
			CacheReadInputTokens:     int64(100 + i*10),
			CacheCreationInputTokens: int64(50 + i*5),
		}
	}

	m := &model{
		termWidth:            140,
		termHeight:           40,
		taskRunning:          true,
		inputTokens:          1234000,
		outputTokens:         567000,
		cacheReadInputTokens: 100000,
		cacheCreationInputTokens: 50000,
		tokenUsagePerAgent:   agents,
		timelineEntries:      []*TimelineEntry{},
		dashboardCollapsed:   false,
	}

	const width = dashboardPanelWidth
	const height = 15

	result := m.renderDashboard(width, height)
	lines := strings.Split(result, "\n")

	if len(lines) != height {
		t.Fatalf("expected %d lines, got %d", height, len(lines))
	}

	// Last line must be bottom border even after truncation
	if !strings.HasPrefix(stripANSI(lines[height-1]), "└") {
		t.Errorf("last line should start with '└' after truncation, got: %q", lines[height-1])
	}

	// All lines must have exact width
	for i, line := range lines {
		w := lipgloss.Width(line)
		if w != width {
			t.Errorf("line %d: expected width %d, got %d", i, width, w)
		}
	}
}

// TestDashboardBorder_LargeTokens verifies correct rendering when token counts
// are large enough to produce wide formatToken output (e.g. "1.2mk").
func TestDashboardBorder_LargeTokens(t *testing.T) {
	initLangManager()

	m := &model{
		termWidth:            140,
		termHeight:           40,
		taskRunning:          true,
		inputTokens:          1234567,
		outputTokens:         987654,
		cacheReadInputTokens: 200000,
		cacheCreationInputTokens: 100000,
		tokenUsagePerAgent: map[string]*AgentTokenUsage{
			"Director": {
				AgentName:                "Director",
				InputTokens:              800000,
				OutputTokens:             400000,
				CacheReadInputTokens:     100000,
				CacheCreationInputTokens: 50000,
			},
			"Coder": {
				AgentName:                "Coder",
				InputTokens:              400000,
				OutputTokens:             200000,
				CacheReadInputTokens:     50000,
				CacheCreationInputTokens: 25000,
			},
			"Reviewer": {
				AgentName:                "Reviewer",
				InputTokens:              200000,
				OutputTokens:             100000,
				CacheReadInputTokens:     25000,
				CacheCreationInputTokens: 12500,
			},
		},
		timelineEntries:    []*TimelineEntry{},
		dashboardCollapsed: false,
	}

	const width = dashboardPanelWidth
	const height = 30

	result := m.renderDashboard(width, height)
	lines := strings.Split(result, "\n")

	if len(lines) != height {
		t.Fatalf("expected %d lines, got %d", height, len(lines))
	}

	for i, line := range lines {
		w := lipgloss.Width(line)
		if w != width {
			t.Errorf("line %d: expected width %d, got %d (content=%q)", i, width, w, line)
		}
	}

	if !strings.HasPrefix(stripANSI(lines[0]), "┌") {
		t.Errorf("first line should start with '┌', got: %q", lines[0])
	}
	if !strings.HasPrefix(stripANSI(lines[height-1]), "└") {
		t.Errorf("last line should start with '└', got: %q", lines[height-1])
	}
}

// initLangManager ensures the global langManager is initialized for tests.
func initLangManager() {
	if langManager == nil {
		langManager = NewLanguageManager()
	}
}
