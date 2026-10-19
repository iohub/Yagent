package blueprint

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 本文件是 `revisions/` 的滚动快照（§3.1：fold 前的整档，留 5 份）。
//
// 它存在的唯一理由是 §5.3 第 3 条：**"删错了"必须有 Undo**。
// 孤儿老化会真删段落、fold 会真替换正文，两者都在唯一的可写文件上动刀；
// 没有这份归档，"上一次的正确说法"就是一次手滑就没回来的东西。
//
// 文件名用定宽 UTC 时间戳 —— 字典序等于时间序，所以"留最新 5 份"就是排序后砍尾巴。
// 随机后缀是为了两个 TUI tab 在同一秒各 fold 一次时不会互相覆盖（同一条理由见 util/atomic.go）。
const (
	RevisionsKeep     = 5
	revisionTimeFmt   = "20060102T150405Z"
	revisionFileExt   = ".md"
	revisionJitterLen = 4
)

// SaveRevision 把整档字节写进 root/revisions/，并滚动到最新 RevisionsKeep 份。
//
// 失败只返回错误、绝不 panic：调用方（fold / GC）对它只做 warn ——
// 归档写不下去时该继续的是"这次更新本身"，而不是把整次委派拖坏（§8 纪律 2）。
func SaveRevision(root string, data []byte, now time.Time) (string, error) {
	dir := RevisionsDir(root)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("blueprint: revisions dir: %w", err)
	}
	name := fmt.Sprintf("%s-%s%s",
		now.UTC().Format(revisionTimeFmt), randHex(revisionJitterLen), revisionFileExt)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("blueprint: write revision: %w", err)
	}
	if err := pruneRevisions(dir); err != nil {
		return path, err
	}
	return path, nil
}

// ListRevisions 返回按时间序（旧→新）排列的归档路径。
func ListRevisions(root string) []string {
	dir := RevisionsDir(root)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), revisionFileExt) {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, filepath.Join(dir, n))
	}
	return out
}

// pruneRevisions 只留最新 RevisionsKeep 份。
// <=0 的保留数按"关掉滚动"处理（与 [blackboard] 那套存在性判据同形）：
// 关掉了就一份都不删，因为"少留一份"和"删掉全部"是两个不同的动作。
func pruneRevisions(dir string) error {
	if RevisionsKeep <= 0 {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), revisionFileExt) {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	if len(names) <= RevisionsKeep {
		return nil
	}
	var firstErr error
	for _, n := range names[:len(names)-RevisionsKeep] {
		full := filepath.Join(dir, n)
		// Lstat 而不是 Stat：符号链接不是我们写的，不替用户删（形状照 blackboard/gc.go）。
		info, err := os.Lstat(full)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if err := os.Remove(full); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func randHex(n int) string {
	const hexDigits = "0123456789abcdef"
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteByte(hexDigits[rand.IntN(16)])
	}
	return b.String()
}
