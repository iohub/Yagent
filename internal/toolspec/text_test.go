package toolspec

import "testing"

func TestFirstSentence(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Read a file. Supports ranges.", "Read a file."},
		{"No period at all", "No period at all"},
		{". starts with a period", ". starts with a period"},
		{"   padded. rest", "padded."},
		{"", ""},
	}
	for _, tc := range cases {
		if got := FirstSentence(tc.in); got != tc.want {
			t.Errorf("FirstSentence(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
