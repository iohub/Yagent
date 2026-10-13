package browserctl

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// ResolveBinaryPath 依次尝试：[browser] binary_path → ~/.yagent/bin/browser-control → $PATH。
// 不内嵌二进制：这个能力的前提是机器上有 Chrome，内嵌 CLI 换不来开箱可用。
func ResolveBinaryPath(configured string) (string, error) {
	if configured != "" {
		p, err := executablePath(configured)
		if err != nil {
			return "", fmt.Errorf("[browser] binary_path 不可用: %w", err)
		}
		return p, nil
	}

	if home, err := os.UserHomeDir(); err == nil {
		candidate := filepath.Join(home, ".yagent", "bin", BinaryName)
		if p, err := executablePath(candidate); err == nil {
			return p, nil
		}
	}

	if p, err := exec.LookPath(BinaryName); err == nil {
		if abs, aerr := filepath.Abs(p); aerr == nil {
			return abs, nil
		}
		return p, nil
	}

	return "", fmt.Errorf(
		"未找到 %s：请执行 `cargo install browser-control-cli`（或从 GitHub release 下载二进制）并放入 PATH，"+
			"或用 [browser] binary_path 显式指定路径",
		BinaryName)
}

func executablePath(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s 是目录", path)
	}
	if info.Mode()&0o111 == 0 {
		return "", fmt.Errorf("%s 没有执行权限", path)
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs, nil
	}
	return path, nil
}
