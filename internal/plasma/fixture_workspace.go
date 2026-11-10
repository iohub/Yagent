package plasma

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// 本文件是 5b 的**沙箱复制**：夹具工作区快照（store 里的原始字节）在行为夹具执行前
// 复制到一个临时目录，执行完删掉。
//
// 为什么必须复制（ADR-5 的第一条防线）：行为夹具会跑真实循环，真模型可能调
// list_dir / read_file / grep_search。让这些调用直接落在 store 的不可变快照上，
// 一次"模型顺手写了点东西"就会改掉 hash 对应的字节 —— 而 Store.Load 的 hash 复核
// （PLS-701）此时会把**整个 bundle** 判成被篡改，症状是"跑完夹具后 store 坏了"。
//
// 纪律：
//   - 只复制普通文件与目录。**符号链接直接跳过并计数**：快照里的软链可以指向 store
//     之外（甚至 /etc），跟随它等于把"沙箱"变成"整机可读"；
//   - 有预算上限（文件数 / 总字节）。夹具目录由作者提供，一份误入的 node_modules
//     会把一次 5b 变成几万文件的复制，而症状是"夹具跑得很慢"而不是"配错了"；
//   - 权限 0700 / 0600：快照里可能有凭据形态的样例数据。
//
// 叶子约束：只 import stdlib。

// CopyLimits 是复制预算。零值不是"无限制"，而是 DefaultCopyLimits ——
// "忘了设"必须落在保守的一侧，否则这条预算就成了装饰。
type CopyLimits struct {
	MaxFiles int
	MaxBytes int64
}

// DefaultCopyLimits 是一份夹具工作区的合理上限：2000 个文件 / 32MB。
// 真实夹具是几个待读的源文件，量级差两个数量级；撞上限说明工作区配错了。
func DefaultCopyLimits() CopyLimits {
	return CopyLimits{MaxFiles: 2000, MaxBytes: 32 << 20}
}

// CopyStats 是一次复制的账目。它必须能被说出来：跳过几个软链、复制了多少字节，
// 是"这个夹具的工作区到底装了什么"的唯一证据。
type CopyStats struct {
	Files   int
	Bytes   int64
	Skipped int // 符号链接 / 设备文件 / 超出预算的项
}

// CopyTree 把 src 目录树复制进 dst（dst 由调用方创建，通常是 MkdirTemp 的产物）。
//
// dst 已存在时只允许是空目录：往一个已经装着别的东西的目录里 merge，出问题时分不清
// "这份文件是谁写的"。超预算**报错**而不是截断：一份不完整的工作区会让夹具以
// "文件不存在"的形式失败，而真正的原因是复制被截断了。
func CopyTree(src, dst string, limits CopyLimits) (CopyStats, error) {
	var stats CopyStats
	info, err := os.Stat(src)
	if err != nil {
		return stats, fmt.Errorf("plasma: 夹具工作区 %s 不可用: %w", src, err)
	}
	if !info.IsDir() {
		return stats, fmt.Errorf("plasma: 夹具工作区 %s 不是目录", src)
	}
	if limits.MaxFiles <= 0 || limits.MaxBytes <= 0 {
		limits = DefaultCopyLimits()
	}
	if entries, err := os.ReadDir(dst); err == nil && len(entries) > 0 {
		return stats, fmt.Errorf("plasma: 目标目录 %s 非空，拒绝合并复制", dst)
	}
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return stats, fmt.Errorf("plasma: 建沙箱目录 %s: %w", dst, err)
	}
	walkErr := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(src, path)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.Type()&os.ModeSymlink != 0:
			stats.Skipped++
			return nil
		case d.IsDir():
			return os.MkdirAll(target, 0o700)
		case d.Type().IsRegular():
			return copyFile(path, target, &stats, limits)
		default:
			// 设备文件 / FIFO / socket：夹具工作区里不该有，也不该被打开。
			stats.Skipped++
			return nil
		}
	})
	if walkErr != nil {
		return stats, fmt.Errorf("plasma: 复制夹具工作区 %s → %s: %w", src, dst, walkErr)
	}
	return stats, nil
}

func copyFile(src, dst string, stats *CopyStats, limits CopyLimits) error {
	if stats.Files+1 > limits.MaxFiles {
		return fmt.Errorf("plasma: 夹具工作区文件数超出上限 %d（ADR-5 的沙箱预算）", limits.MaxFiles)
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if stats.Bytes+info.Size() > limits.MaxBytes {
		return fmt.Errorf("plasma: 夹具工作区字节数超出上限 %d（ADR-5 的沙箱预算）", limits.MaxBytes)
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	stats.Files++
	stats.Bytes += written
	return nil
}

// FixtureWorkspacePath 把夹具声明的 workspace 解析成一个真实目录。
//
//   - 空串 ⇒ 空（调用方用空沙箱：5b 的题面本身不依赖文件）；
//   - 绝对路径 ⇒ 原样；
//   - 相对路径 ⇒ 相对 base（生产上是 bundle.json 所在目录，测试上是 t.TempDir()）。
//
// 刻意**不**回落到 CWD：CWD 在生产上是用户启动 yagent 的目录，一条 `workspace: "src"`
// 会把整棵源码树复制进沙箱，而作者以为自己指的是"bundle 旁边那份"。
func FixtureWorkspacePath(base, workspace string) (string, error) {
	w := strings.TrimSpace(workspace)
	if w == "" {
		return "", nil
	}
	if filepath.IsAbs(w) {
		return filepath.Clean(w), nil
	}
	if strings.TrimSpace(base) == "" {
		return "", fmt.Errorf("plasma: 夹具 workspace %q 是相对路径，但调用方没给 base 目录；"+
			"修复：传 bundle.json 所在目录，或把 workspace 写成绝对路径", w)
	}
	return filepath.Join(base, w), nil
}
