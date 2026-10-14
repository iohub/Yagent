package tools

import (
	"context"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestExecuteRunBash(t *testing.T) {
	tool := NewSystemOperationsTool(t.TempDir())

	cases := []struct {
		name       string
		command    string
		background bool
		check      func(*testing.T, map[string]interface{})
	}{
		{
			name:    "foreground success",
			command: "echo 'hello'",
			check: func(t *testing.T, res map[string]interface{}) {
				if res["success"] != true {
					t.Fatalf("expected success=true, got %v", res["success"])
				}
				if output, _ := res["output"].(string); output != "hello\n" {
					t.Errorf("Expected output 'hello\\n', got %q", output)
				}
			},
		},
		{
			name:    "foreground failure",
			command: "echo boom >&2; exit 3",
			check: func(t *testing.T, res map[string]interface{}) {
				// 失败不是 error 返回值，而是 success=false + 保留的 stderr 输出
				if res["success"] != false {
					t.Fatalf("expected success=false for a non-zero exit, got %v", res["success"])
				}
				if e, _ := res["error"].(string); e == "" {
					t.Error("expected a non-empty error describing the failed exit")
				}
				if output, _ := res["output"].(string); output != "boom\n" {
					t.Errorf("expected combined output to survive the failure, got %q", output)
				}
			},
		},
		{
			name:       "background start",
			command:    "sleep 30",
			background: true,
			check: func(t *testing.T, res map[string]interface{}) {
				if res["success"] != true {
					t.Fatalf("Expected success=true for background command, got %v", res["success"])
				}
				if res["status"] != "started_background" {
					t.Errorf("Expected status='started_background', got %v", res["status"])
				}
				pid, ok := res["pid"].(int)
				if !ok {
					t.Fatalf("Expected pid to be an int, got %T", res["pid"])
				}
				// 后台进程不归本测试管，那就由本测试收尾：绝不允许它活过测试二进制。
				t.Cleanup(func() { terminateProcess(t, pid) })
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tool.ExecuteRunBash(context.Background(), map[string]interface{}{
				"command":       tc.command,
				"is_background": tc.background,
				"explanation":   "test",
			})
			if err != nil {
				t.Fatalf("Expected no error, got %v", err)
			}
			resMap, ok := result.(map[string]interface{})
			if !ok {
				t.Fatalf("Expected map result, got %T", result)
			}
			tc.check(t, resMap)
		})
	}
}

// terminateProcess 杀掉后台进程并等到它从进程表里消失（生产代码的 Wait goroutine 负责回收）。
func terminateProcess(t *testing.T, pid int) {
	t.Helper()
	p, err := os.FindProcess(pid)
	if err != nil {
		t.Errorf("FindProcess(%d): %v", pid, err)
		return
	}
	if err := p.Kill(); err != nil && err != os.ErrProcessDone {
		t.Errorf("Kill(%d): %v", pid, err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if p.Signal(syscall.Signal(0)) != nil {
			return // ESRCH：已经不在进程表里
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("background process %d is still alive after being killed", pid)
}
