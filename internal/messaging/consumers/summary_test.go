package messaging

import "testing"

// HTTP 模式写 stdout 的那个旧 consumer 靠**参数键名**取活动行的路径。
// 少一个键不报错、只让那一行空着 —— 而真 TUI 用的是另一份实现
// （internal/tui/tui_render.go 的 extractToolSummary，它认 target_file），
// 所以只有这里会漂走。两条实现各自钉住自己认识的键。
func TestExtractToolSummaryShowsFilePath(t *testing.T) {
	cases := []struct {
		toolName string
		argsJSON string
		want     string
	}{
		{"read_file", `{"target_file":"internal/a.go"}`, "internal/a.go"},
		{"create_file", `{"target_file":"new/a.go"}`, "new/a.go"},
		{"search_replace_in_file", `{"file_path":"edit/a.go"}`, "edit/a.go"},
		{"run_bash", `{"command":"go test ./..."}`, "go test ./..."},
		{"list_dir", `{"path":"internal/tools"}`, "internal/tools"},
		// 谁都不认识的时候必须是空串，而不是把 JSON 原文吐出来。
		{"read_file", `{}`, ""},
		{"read_file", `not json`, ""},
	}
	for _, c := range cases {
		if got := extractToolSummary(c.toolName, c.argsJSON); got != c.want {
			t.Errorf("extractToolSummary(%s, %s) = %q, want %q", c.toolName, c.argsJSON, got, c.want)
		}
	}
}
