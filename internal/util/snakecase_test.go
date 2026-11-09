package util

import "testing"

func TestSnakeCase(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Security Auditor", "security_auditor"},
		{"Data-Export Agent", "data_export_agent"},
		{"SQL Injection Scanner", "sql_injection_scanner"},
		{"Simple", "simple"},
		{"", "custom_agent"},
		{"!!!Special!!!", "special"},
		{"UPPER CASE", "upper_case"},
		{"Multi   Space   Agent", "multi_space_agent"},
		{"_Leading_Trailing_", "leading_trailing"},
		{"Test__Double__Underscore", "test_double_underscore"},
		{"agent123", "agent123"},
		{"   ", "custom_agent"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := SnakeCase(tc.input)
			if got != tc.expected {
				t.Errorf("SnakeCase(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}
