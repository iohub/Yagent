package util

import (
	"fmt"
	"os"
	"path/filepath"
)

// AtomicWriteOptions 描述一次原子写的落盘语义。零值可用：不建目录、0600、
// tmp 后缀 .tmp、不继承原文件权限。
type AtomicWriteOptions struct {
	// DirPerm 非 0 时先 MkdirAll(dir, DirPerm)。留 0 是给"目录必然已存在"的调用点
	// （datamanager 的索引、config 的写回），让它们保持原有的失败方式。
	DirPerm os.FileMode

	// FilePerm 是最终文件的权限位，0 按 0600 处理。
	FilePerm os.FileMode

	// TmpSuffix 追加在随机段之后。约束：结果不能命中调用方目录的列举扩展名 ——
	// blackboard 只认 *.md（`index.go:129`、`gc.go:131`），datamanager 只认 *.jsonl
	// （`index.go:124`），这两处过滤靠的就是"中间态不长成数据文件的后缀"。
	TmpSuffix string

	// InheritPerm 为真时沿用目标文件已有的权限位（这是 config/persist.go 原有的规则：
	// 用户手工 chmod 过的配置文件不该被一次写回改掉权限）。目标不存在时用 FilePerm。
	InheritPerm bool

	// Label 出现在错误前缀里，让失败能指回调用点（"blackboard: rename: …"）。
	Label string
}

// AtomicWrite 先写同目录的临时文件再 rename：读者要么看到旧整份、要么看到新整份，
// 不会看到半份。任何一步失败都清理临时文件 —— 残留会被"只认某扩展名"的列举当垃圾长期留着。
//
// 临时文件名带随机段（而不是 path+".tmp"）：同一路径可能被两个进程同时写
// （两个 TUI tab 的 history 索引、setup 向导与运行中的 TUI 同时写 config.toml），
// 固定名会让后到的写入截断前者的中间态，随后两次 rename 把交错的内容落成最终文件。
//
// 本函数是全仓唯一的"tmp + rename"实现：其余落盘点一律转调它（I21，
// 棘轮见 scripts/check_no_inline_atomic_write.sh）。
func AtomicWrite(path string, data []byte, opts AtomicWriteOptions) error {
	label := opts.Label
	if label == "" {
		label = "util"
	}
	suffix := opts.TmpSuffix
	if suffix == "" {
		suffix = ".tmp"
	}
	filePerm := opts.FilePerm
	if filePerm == 0 {
		filePerm = 0o600
	}

	dir := filepath.Dir(path)
	if opts.DirPerm != 0 {
		if err := os.MkdirAll(dir, opts.DirPerm); err != nil {
			return fmt.Errorf("%s: create dir: %w", label, err)
		}
	}

	mode := filePerm
	if opts.InheritPerm {
		if st, err := os.Stat(path); err == nil {
			mode = st.Mode().Perm()
		}
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*"+suffix)
	if err != nil {
		return fmt.Errorf("%s: create temp: %w", label, err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("%s: write temp: %w", label, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("%s: close temp: %w", label, err)
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("%s: chmod temp: %w", label, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("%s: rename: %w", label, err)
	}
	return nil
}
