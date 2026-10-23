package tui

import (
	"strings"
	"testing"
)

// mcp_server_status 必须渲染成一行人类可读文本。不处理的话它会落到
// formatEventAsEntry 的 default 分支，界面上就是一行 map[...] —— 见 tui_render.go 的 switch。
func TestMCPServerStatusEventRendersOneLine(t *testing.T) {
	out := formatMCPStatusEvent(map[string]interface{}{
		"server":       "github",
		"state":        "offline",
		"error":        "exit status 1",
		"attempt":      2,
		"max_attempts": 3,
		"tools":        12,
	})
	if strings.Contains(out, "map[") {
		t.Errorf("渲染成了 Go map：%q", out)
	}
	if strings.Contains(out, "\n") {
		t.Errorf("状态事件必须是单行：%q", out)
	}
	if !strings.Contains(out, "github: offline") || !strings.Contains(out, "exit status 1") || !strings.Contains(out, "attempt 2/3") {
		t.Errorf("渲染 = %q, want 与 Manager.StatusLines 同源的措辞", out)
	}

	// 数字过一遍 JSON 会变成 float64；两种形态都要认。
	ready := formatMCPStatusEvent(map[string]interface{}{
		"server": "alpha", "state": "ready", "tools": float64(3), "curated": float64(1),
	})
	if !strings.Contains(ready, "alpha: ready — 3 tools (1 curated, 2 external)") {
		t.Errorf("float64 形态 = %q", ready)
	}

	// payload 形状不对时不 panic，给一行可读的兜底。
	if got := formatMCPStatusEvent("nonsense"); got == "" || strings.Contains(got, "map[") {
		t.Errorf("非法 payload = %q", got)
	}
}
