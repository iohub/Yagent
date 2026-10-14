package tools_test

import (
	"strings"
	"testing"

	"yagent/internal/tools"
)

// TestWorkspaceGuard_BrowserEvalAuthByRisk 验证 browser_eval 只对敏感 JS 要求授权：
// 良性逃生口（reload/back/dispatchEvent）不该弹窗，否则模型会把 eval 当禁区。
func TestWorkspaceGuard_BrowserEvalAuthByRisk(t *testing.T) {
	guard := tools.NewWorkspaceGuard(t.TempDir(), tools.NewUserConfirmManager())

	risky := []struct {
		js     string
		reason string
	}{
		{"document.cookie", "cookie"},
		{"await fetch('/api/me')", "网络请求"},
		{"localStorage.setItem('a','b')", "存储"},
		{"document.querySelector('form').submit()", "提交表单"},
	}
	for _, tc := range risky {
		needsAuth, reason := guard.Check("browser_eval", map[string]interface{}{"js": tc.js})
		if !needsAuth {
			t.Errorf("Check(browser_eval, %q) should require authorization", tc.js)
			continue
		}
		if !strings.Contains(reason, tc.reason) {
			t.Errorf("reason for %q should mention %q, got %q", tc.js, tc.reason, reason)
		}
		if !strings.Contains(reason, tc.js) {
			t.Errorf("reason should show the JS being executed, got %q", reason)
		}
	}

	benign := []string{
		"location.reload()",
		"history.back()",
		`document.querySelector('[data-browser-control-ref="e2"]').dispatchEvent(new MouseEvent("mouseover",{bubbles:true}))`,
		"",
	}
	for _, js := range benign {
		needsAuth, reason := guard.Check("browser_eval", map[string]interface{}{"js": js})
		if needsAuth {
			t.Errorf("Check(browser_eval, %q) must not require authorization (reason=%q)", js, reason)
		}
	}
}
