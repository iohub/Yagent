package tools

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
)

// TestWorkspaceGuard_Check_SafeSystemPaths tests that system paths in the whitelist
// are allowed without requiring authorization.
func TestWorkspaceGuard_Check_SafeSystemPaths(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", "test_guard_safe")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	confirmMgr := NewUserConfirmManager()
	guard := NewWorkspaceGuard(tmpDir, confirmMgr)

	testCases := []struct {
		name    string
		command string
	}{
		{"系统bin路径", "ls /usr/bin"},
		{"系统配置路径", "cat /etc/hostname"},
		{"临时目录", "python3 /tmp/test.py"},
		{"系统bin路径2", "/bin/ls -la"},
		{"系统sbin路径", "/usr/sbin/sshd -t"},
		{"usr_local_bin", "go install /usr/local/bin/tool"},
		{"usr_lib路径", "ldconfig -p | grep /usr/lib"},
		{"usr_share路径", "man -w /usr/share/man"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]interface{}{
				"command":       tc.command,
				"is_background": false,
				"is_dangerous":  false,
			}
			needsAuth, reason := guard.Check("run_bash", params)
			if needsAuth {
				t.Errorf("Expected safe system path to be allowed, but got auth required. command=%q, reason=%q", tc.command, reason)
			}
		})
	}
}

// TestWorkspaceGuard_Check_WorkspacePaths tests that paths inside the workspace
// are allowed without requiring authorization.
func TestWorkspaceGuard_Check_WorkspacePaths(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", "test_guard_workspace")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// 创建工作空间内的子目录和文件
	subDir := filepath.Join(tmpDir, "subdir")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	testFile := filepath.Join(subDir, "test.txt")
	if err := ioutil.WriteFile(testFile, []byte("test content"), 0644); err != nil {
		t.Fatal(err)
	}

	confirmMgr := NewUserConfirmManager()
	guard := NewWorkspaceGuard(tmpDir, confirmMgr)

	testCases := []struct {
		name    string
		command string
	}{
		{"工作空间绝对路径", "cat " + testFile},
		{"工作空间子目录相对路径", "ls subdir"},
		{"工作空间当前目录", "cat test.txt"},
		{"工作空间内读取", "cat " + tmpDir + "/subdir/test.txt"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]interface{}{
				"command":       tc.command,
				"is_background": false,
				"is_dangerous":  false,
			}
			needsAuth, reason := guard.Check("run_bash", params)
			if needsAuth {
				t.Errorf("Expected workspace path to be allowed, but got auth required. command=%q, reason=%q", tc.command, reason)
			}
		})
	}
}

// TestWorkspaceGuard_Check_DangerousExternalPaths tests that paths outside the
// workspace and not in the whitelist require authorization.
func TestWorkspaceGuard_Check_DangerousExternalPaths(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", "test_guard_dangerous")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	confirmMgr := NewUserConfirmManager()
	guard := NewWorkspaceGuard(tmpDir, confirmMgr)

	testCases := []struct {
		name    string
		command string
	}{
		{"root家目录", "cat /root/secret.txt"},
		{"opt私有路径", "ls /opt/proprietary"},
		{"home用户目录", "cat /home/user/data.csv"},
		{"var私有路径", "cat /var/log/secure"},
		{" srv路径", "ls /srv/secret"},
		{"media路径", "cat /media/usb/key.txt"},
		{"mnt路径", "ls /mnt/external"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]interface{}{
				"command":       tc.command,
				"is_background": false,
				"is_dangerous":  false,
			}
			needsAuth, reason := guard.Check("run_bash", params)
			if !needsAuth {
				t.Errorf("Expected dangerous external path to require auth, but got allowed. command=%q", tc.command)
			}
			if reason == "" {
				t.Errorf("Expected non-empty reason for dangerous path, got empty. command=%q", tc.command)
			}
		})
	}
}

// TestWorkspaceGuard_Check_RootPath tests that the root directory "/" is always
// treated as dangerous regardless of is_dangerous flag.
func TestWorkspaceGuard_Check_RootPath(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", "test_guard_root")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	confirmMgr := NewUserConfirmManager()
	guard := NewWorkspaceGuard(tmpDir, confirmMgr)

	testCases := []struct {
		name    string
		command string
	}{
		{"列出根目录", "ls /"},
		{"查找根目录", "find / -name test"},
		{"根目录通配", "find / -type f"},
		{"根目录递归", "du -sh /"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]interface{}{
				"command":       tc.command,
				"is_background": false,
				"is_dangerous":  false,
			}
			needsAuth, reason := guard.Check("run_bash", params)
			if !needsAuth {
				t.Errorf("Expected root path to require auth, but got allowed. command=%q", tc.command)
			}
			if reason == "" {
				t.Errorf("Expected non-empty reason for root path, got empty. command=%q", tc.command)
			}
		})
	}
}

// TestWorkspaceGuard_Check_IsDangerousTrue tests that commands with
// is_dangerous=true always require authorization, even if the command
// itself would be safe.
func TestWorkspaceGuard_Check_IsDangerousTrue(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", "test_guard_dangerous_flag")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	confirmMgr := NewUserConfirmManager()
	guard := NewWorkspaceGuard(tmpDir, confirmMgr)

	testCases := []string{
		"echo hello",
		"ls /usr/bin",
		"cat /etc/hostname",
		"pwd",
	}

	for _, cmd := range testCases {
		t.Run(cmd, func(t *testing.T) {
			params := map[string]interface{}{
				"command":       cmd,
				"is_background": false,
				"is_dangerous":  true,
			}
			needsAuth, reason := guard.Check("run_bash", params)
			if !needsAuth {
				t.Errorf("Expected is_dangerous=true to require auth, but got allowed. command=%q", cmd)
			}
			if reason == "" {
				t.Errorf("Expected non-empty reason for is_dangerous=true, got empty. command=%q", cmd)
			}
		})
	}
}

// TestWorkspaceGuard_Check_SimpleCommands tests that simple commands without
// external paths do not require authorization.
func TestWorkspaceGuard_Check_SimpleCommands(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", "test_guard_simple")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	confirmMgr := NewUserConfirmManager()
	guard := NewWorkspaceGuard(tmpDir, confirmMgr)

	testCases := []struct {
		name    string
		command string
	}{
		{"echo命令", "echo hello"},
		{"git状态", "git status"},
		{"ls当前目录", "ls -la"},
		{"pwd", "pwd"},
		{"date", "date"},
		{"whoami", "whoami"},
		{"简单的管道", "echo hello | grep hello"},
		{"变量赋值", "export MY_VAR=test"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]interface{}{
				"command":       tc.command,
				"is_background": false,
				"is_dangerous":  false,
			}
			needsAuth, reason := guard.Check("run_bash", params)
			if needsAuth {
				t.Errorf("Expected simple command to be allowed, but got auth required. command=%q, reason=%q", tc.command, reason)
			}
		})
	}
}

// TestWorkspaceGuard_Check_NilConfirmMgr tests that when confirmMgr is nil,
// the Check method returns (false, "") for all inputs.
func TestWorkspaceGuard_Check_NilConfirmMgr(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", "test_guard_nil")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	guard := NewWorkspaceGuard(tmpDir, nil)

	testCases := []struct {
		name    string
		command string
	}{
		{"系统路径", "ls /usr/bin"},
		{"危险路径", "cat /root/secret.txt"},
		{"简单命令", "echo hello"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]interface{}{
				"command":       tc.command,
				"is_background": false,
				"is_dangerous":  false,
			}
			needsAuth, reason := guard.Check("run_bash", params)
			if needsAuth {
				t.Errorf("Expected nil confirmMgr to return (false, \"\"), got (true, %q). command=%q", reason, tc.command)
			}
			if reason != "" {
				t.Errorf("Expected nil confirmMgr to return empty reason, got %q. command=%q", reason, tc.command)
			}
		})
	}
}

// TestWorkspaceGuard_Check_NonDangerousTools tests that non-dangerous tools
// are always allowed.
func TestWorkspaceGuard_Check_NonDangerousTools(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", "test_guard_nondangerous")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	confirmMgr := NewUserConfirmManager()
	guard := NewWorkspaceGuard(tmpDir, confirmMgr)

	// 测试不在 dangerousTools 中的工具
	nonDangerousTools := []string{"read_file", "search_by_regex", "list_dir", "delegate_browser"}

	for _, tool := range nonDangerousTools {
		t.Run(tool, func(t *testing.T) {
			params := map[string]interface{}{
				"command":       "any",
				"is_background": false,
			}
			needsAuth, reason := guard.Check(tool, params)
			if needsAuth {
				t.Errorf("Expected non-dangerous tool %q to be allowed, but got auth required", tool)
			}
			if reason != "" {
				t.Errorf("Expected empty reason for non-dangerous tool %q, got %q", tool, reason)
			}
		})
	}
}

// TestWorkspaceGuard_Check_CompletePathCoverage tests comprehensive path coverage
// including edge cases in the safe system paths whitelist.
func TestWorkspaceGuard_Check_CompletePathCoverage(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", "test_guard_paths")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	confirmMgr := NewUserConfirmManager()
	guard := NewWorkspaceGuard(tmpDir, confirmMgr)

	// 白名单中的路径应该放行
	safePaths := []struct {
		name    string
		command string
	}{
		{"usr/bin完整路径", "/usr/bin/python3 script.py"},
		{"usr/local/bin", "/usr/local/bin/npm start"},
		{"usr/lib库", "gcc -L/usr/lib -lc"},
		{"usr/lib64", "ldd /usr/lib64/libc.so"},
		{"usr/libexec", "/usr/libexec/git-core/git-status"},
		{"usr/share文档", "man man"},
		{"etc配置", "cat /etc/resolv.conf"},
		{"dev/null", "cat /dev/null"},
		{"dev/zero", "dd if=/dev/zero of=/tmp/test bs=1k"},
		{"dev/random", "cat /dev/random"},
		{"dev/urandom", "cat /dev/urandom"},
		{"tmp临时", "cp /tmp/file ."},
		{"var/tmp", "cat /var/tmp/cache"},
		{"proc只读", "cat /proc/cpuinfo"},
		{"sys只读", "cat /sys/class/net/eth0/statistics/rx_bytes"},
		{"路径前缀匹配", "/usr/bin/env go run main.go"},
		{"etc子路径", "cat /etc/ssl/certs/ca-certificates.crt"},
	}

	for _, tc := range safePaths {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]interface{}{
				"command":       tc.command,
				"is_background": false,
				"is_dangerous":  false,
			}
			needsAuth, reason := guard.Check("run_bash", params)
			if needsAuth {
				t.Errorf("Expected safe path to be allowed, but got auth required. command=%q, reason=%q", tc.command, reason)
			}
		})
	}

	// 不在白名单且不在工作空间的危险路径应该被拦截
	dangerousPaths := []struct {
		name    string
		command string
	}{
		{"根目录", "ls /"},
		{"opt私有", "rm -rf /opt/proprietary"},
		{"home用户", "cat /home/user/secrets"},
		{"srv服务", "rm -rf /srv/backup"},
		{"media可移动", "ls /media"},
		{"mnt挂载", "umount /mnt/external"},
	}

	for _, tc := range dangerousPaths {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]interface{}{
				"command":       tc.command,
				"is_background": false,
				"is_dangerous":  false,
			}
			needsAuth, reason := guard.Check("run_bash", params)
			if !needsAuth {
				t.Errorf("Expected dangerous path to require auth, but got allowed. command=%q", tc.command)
			}
			if reason == "" {
				t.Errorf("Expected non-empty reason for dangerous path, got empty. command=%q", tc.command)
			}
		})
	}
}

// TestWorkspaceGuard_Check_EdgeCases tests various edge cases in command parsing.
func TestWorkspaceGuard_Check_EdgeCases(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", "test_guard_edge")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	confirmMgr := NewUserConfirmManager()
	guard := NewWorkspaceGuard(tmpDir, confirmMgr)

	testCases := []struct {
		name     string
		command  string
		wantAuth bool
	}{
		// 空命令不触发授权
		{"空命令", "", false},
		// 只有空格的命令不触发授权
		{"纯空格", "   ", false},
		// 引号包裹的路径
		{"引号系统路径", "cat '/usr/bin/ls'", false},
		{"引号危险路径", "cat '/root/secret'", true},
		// 路径带有参数
		{"路径加参数", "find /usr/share -name '*.go'", false},
		// 多命令 &&
		{"多命令安全", "echo a && ls /usr/bin", false},
		{"多命令危险", "echo a && cat /root/secret", true},
		// 路径在引号中
		{"双引号危险", "cat \"/root/secret.txt\"", true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]interface{}{
				"command":       tc.command,
				"is_background": false,
				"is_dangerous":  false,
			}
			needsAuth, _ := guard.Check("run_bash", params)
			if needsAuth != tc.wantAuth {
				t.Errorf("Command=%q: expected auth=%v, got auth=%v", tc.command, tc.wantAuth, needsAuth)
			}
		})
	}
}
