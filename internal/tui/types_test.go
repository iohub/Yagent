package tui

import (
	"testing"
)

func TestIsMergeableTool(t *testing.T) {
	tools := []struct {
		name   string
		merge  bool
		msg    string
	}{
		{"read_file", true, ""},
		{"list_dir", true, ""},
		{"search_by_regex", true, ""},
		{"semantic_search", true, ""},
		{"edit_file", false, ""},
		{"llm_call", false, ""},
		{"run_bash", false, ""},
		{"thinking", false, ""},
	}

	for _, tt := range tools {
		if got := IsMergeableTool(tt.name); got != tt.merge {
			t.Errorf("IsMergeableTool(%q) = %v, want %v", tt.name, got, tt.merge)
		}
	}
}

func TestEffectiveStatus(t *testing.T) {
	tests := []struct {
		entry    []*TimelineEntry
		want    ToolStatus
		reason   string
	}{
		{
			entry: []*TimelineEntry{{Status: ToolStatusSuccess, IsError: false}},
			want:  ToolStatusSuccess,
			reason: "single finished -> Success",
		},
		{
			entry: []*TimelineEntry{{Status: ToolStatusSuccess, IsError: false}, {Status: ToolStatusRunning, IsError: false}},
			want:  ToolStatusRunning,
			reason: "one Running -> Running",
		},
		{
			entry: []*TimelineEntry{{Status: ToolStatusSuccess, IsError: false}, {Status: ToolStatusError, IsError: true}},
			want:  ToolStatusError,
			reason: "one IsError -> Error",
		},
		{
			entry: []*TimelineEntry{
				{Status: ToolStatusSuccess, IsError: false},
				{Status: ToolStatusSuccess, IsError: false},
				{Status: ToolStatusSuccess, IsError: false},
			},
			want:  ToolStatusSuccess,
			reason: "all finished -> Success",
		},
		{
			entry: []*TimelineEntry{
				{Status: ToolStatusSuccess, IsError: false},
				{Status: ToolStatusRunning, IsError: false},
				{Status: ToolStatusSuccess, IsError: false},
			},
			want:  ToolStatusRunning,
			reason: "one Running among Success -> Running",
		},
		{
			entry: []*TimelineEntry{
				{Status: ToolStatusSuccess, IsError: false},
				{Status: ToolStatusRunning, IsError: false},
				{Status: ToolStatusError, IsError: true},
			},
			want:  ToolStatusError,
			reason: "Error beats Running",
		},
	}

	for _, tt := range tests {
		e := &TimelineEntry{}
		for i, entry := range tt.entry {
			if i == 0 {
				e = entry // first is the container
			} else {
				e.SubEntries = append(e.SubEntries, entry)
			}
		}
		if got := e.EffectiveStatus(); got != tt.want {
			t.Errorf("%s: EffectiveStatus() = %v, want %v", tt.reason, got, tt.want)
		}
	}
}
