package tools

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWorkspaceGuard_Check_RunBash 是 run_bash 授权判据的唯一一张表：
// (命令, LLM 的 is_dangerous) → 是否要求授权。
// 覆盖三类：安全系统路径白名单、工作区内路径、工作区外/根目录，外加 is_dangerous=true 的强制授权。
func TestWorkspaceGuard_Check_RunBash(t *testing.T) {
	tmpDir := t.TempDir()

	subDir := filepath.Join(tmpDir, "subdir")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	testFile := filepath.Join(subDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
		t.Fatal(err)
	}

	guard := NewWorkspaceGuard(tmpDir, NewUserConfirmManager())

	cases := []struct {
		name      string
		command   string
		dangerous bool
		wantAuth  bool
	}{
		// ---- 白名单内的系统路径：放行 ----
		{"系统bin路径", "ls /usr/bin", false, false},
		{"系统配置路径", "cat /etc/hostname", false, false},
		{"临时目录", "python3 /tmp/test.py", false, false},
		{"系统bin路径2", "/bin/ls -la", false, false},
		{"系统sbin路径", "/usr/sbin/sshd -t", false, false},
		{"usr_local_bin", "go install /usr/local/bin/tool", false, false},
		{"usr_lib路径", "ldconfig -p | grep /usr/lib", false, false},
		{"usr_share路径", "man -w /usr/share/man", false, false},
		{"usr/bin完整路径", "/usr/bin/python3 script.py", false, false},
		{"usr/local/bin", "/usr/local/bin/npm start", false, false},
		{"usr/lib库", "gcc -L/usr/lib -lc", false, false},
		{"usr/lib64", "ldd /usr/lib64/libc.so", false, false},
		{"usr/libexec", "/usr/libexec/git-core/git-status", false, false},
		{"usr_share文档", "man man", false, false},
		{"etc配置", "cat /etc/resolv.conf", false, false},
		{"dev/null", "cat /dev/null", false, false},
		{"dev/zero", "dd if=/dev/zero of=/tmp/test bs=1k", false, false},
		{"dev/random", "cat /dev/random", false, false},
		{"dev/urandom", "cat /dev/urandom", false, false},
		{"tmp临时", "cp /tmp/file .", false, false},
		{"var/tmp", "cat /var/tmp/cache", false, false},
		{"proc只读", "cat /proc/cpuinfo", false, false},
		{"sys只读", "cat /sys/class/net/eth0/statistics/rx_bytes", false, false},
		{"路径前缀匹配", "/usr/bin/env go run main.go", false, false},
		{"etc子路径", "cat /etc/ssl/certs/ca-certificates.crt", false, false},

		// ---- 工作区内路径：放行 ----
		{"工作空间绝对路径", "cat " + testFile, false, false},
		{"工作空间子目录相对路径", "ls subdir", false, false},
		{"工作空间当前目录", "cat test.txt", false, false},
		{"工作空间内读取", "cat " + tmpDir + "/subdir/test.txt", false, false},

		// ---- 无路径的简单命令：放行 ----
		{"echo命令", "echo hello", false, false},
		{"git状态", "git status", false, false},
		{"ls当前目录", "ls -la", false, false},
		{"pwd", "pwd", false, false},
		{"date", "date", false, false},
		{"whoami", "whoami", false, false},
		{"简单的管道", "echo hello | grep hello", false, false},
		{"变量赋值", "export MY_VAR=test", false, false},

		// ---- 白名单外且在工作区外：要求授权 ----
		{"root家目录", "cat /root/secret.txt", false, true},
		{"opt私有路径", "ls /opt/proprietary", false, true},
		{"home用户目录", "cat /home/user/data.csv", false, true},
		{"var私有路径", "cat /var/log/secure", false, true},
		{"srv路径", "ls /srv/secret", false, true},
		{"media路径", "cat /media/usb/key.txt", false, true},
		{"mnt路径", "ls /mnt/external", false, true},
		{"opt私有删除", "rm -rf /opt/proprietary", false, true},
		{"home用户秘密", "cat /home/user/secrets", false, true},
		{"srv服务删除", "rm -rf /srv/backup", false, true},
		{"media可移动", "ls /media", false, true},
		{"mnt挂载卸载", "umount /mnt/external", false, true},

		// ---- 根目录：无论怎么写都危险 ----
		{"列出根目录", "ls /", false, true},
		{"查找根目录", "find / -name test", false, true},
		{"根目录通配", "find / -type f", false, true},
		{"根目录递归", "du -sh /", false, true},

		// ---- is_dangerous=true：即使命令本身安全也要授权 ----
		{"危险标记echo", "echo hello", true, true},
		{"危险标记系统路径", "ls /usr/bin", true, true},
		{"危险标记etc", "cat /etc/hostname", true, true},
		{"危险标记pwd", "pwd", true, true},

		// ---- 命令解析的边界情况 ----
		{"空命令", "", false, false},
		{"纯空格", "   ", false, false},
		{"引号系统路径", "cat '/usr/bin/ls'", false, false},
		{"引号危险路径", "cat '/root/secret'", false, true},
		{"路径加参数", "find /usr/share -name '*.go'", false, false},
		{"多命令安全", "echo a && ls /usr/bin", false, false},
		{"多命令危险", "echo a && cat /root/secret", false, true},
		{"双引号危险", "cat \"/root/secret.txt\"", false, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]interface{}{
				"command":       tc.command,
				"is_background": false,
				"is_dangerous":  tc.dangerous,
			}
			needsAuth, reason := guard.Check("run_bash", params)
			if needsAuth != tc.wantAuth {
				t.Errorf("Check(run_bash, %q, dangerous=%v) = (%v, %q), want auth=%v",
					tc.command, tc.dangerous, needsAuth, reason, tc.wantAuth)
			}
			if tc.wantAuth && reason == "" {
				t.Errorf("requiring auth must come with a reason the user can read. command=%q", tc.command)
			}
		})
	}

	// confirmMgr 为 nil 时守卫整体哑火：任何命令都返回 (false, "")，而不是 panic。
	nilGuard := NewWorkspaceGuard(tmpDir, nil)
	for _, cmd := range []string{"ls /usr/bin", "cat /root/secret.txt", "echo hello"} {
		t.Run("nilConfirmMgr/"+cmd, func(t *testing.T) {
			needsAuth, reason := nilGuard.Check("run_bash", map[string]interface{}{
				"command":       cmd,
				"is_background": false,
				"is_dangerous":  false,
			})
			if needsAuth || reason != "" {
				t.Errorf("Check with nil confirmMgr = (%v, %q), want (false, \"\")", needsAuth, reason)
			}
		})
	}
}
