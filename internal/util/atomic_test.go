package util

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestAtomicWriteRoundTripAndNoTmpResidue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "note.md")

	if err := AtomicWrite(path, []byte("hello world"), AtomicWriteOptions{
		DirPerm:  0o700,
		FilePerm: 0o600,
	}); err != nil {
		t.Fatalf("AtomicWrite: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "hello world" {
		t.Errorf("content = %q, want %q", got, "hello world")
	}
	assertNoTmpResidue(t, filepath.Dir(path))
}

// DirPerm 为 0 时必须保持"目录不存在就失败"的原有语义：
// 悄悄建目录会把"配置目录还没准备好"这类真实错误压成成功。
func TestAtomicWriteSkipsMkdirWhenDirPermZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "index.json")

	err := AtomicWrite(path, []byte("{}"), AtomicWriteOptions{FilePerm: 0o644})
	if err == nil {
		t.Fatalf("expected failure for missing parent dir with DirPerm=0, got none; dir now exists: %v", dirExists(filepath.Dir(path)))
	}
	if dirExists(filepath.Dir(path)) {
		t.Errorf("DirPerm=0 must not create the parent dir, but it exists")
	}
}

func TestAtomicWritePermRules(t *testing.T) {
	t.Run("默认 0600", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "a.txt")
		if err := AtomicWrite(path, []byte("x"), AtomicWriteOptions{}); err != nil {
			t.Fatalf("AtomicWrite: %v", err)
		}
		if got := permOf(t, path); got != 0o600 {
			t.Errorf("perm = %o, want 600 (zero FilePerm 按 0600)", got)
		}
	})

	t.Run("显式 0644 生效", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "b.txt")
		if err := AtomicWrite(path, []byte("x"), AtomicWriteOptions{FilePerm: 0o644}); err != nil {
			t.Fatalf("AtomicWrite: %v", err)
		}
		if got := permOf(t, path); got != 0o644 {
			t.Errorf("perm = %o, want 644", got)
		}
	})

	// 用户手工 chmod 过的配置文件不该被一次写回改掉权限。
	t.Run("InheritPerm 沿用原权限", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte("old\n"), 0o640); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if err := AtomicWrite(path, []byte("new\n"), AtomicWriteOptions{FilePerm: 0o600, InheritPerm: true}); err != nil {
			t.Fatalf("AtomicWrite: %v", err)
		}
		if got := permOf(t, path); got != 0o640 {
			t.Errorf("perm = %o, want 640 (inherited)", got)
		}
		if b, _ := os.ReadFile(path); string(b) != "new\n" {
			t.Errorf("content = %q, want %q", b, "new\n")
		}
	})

	t.Run("InheritPerm 对不存在的目标用 FilePerm", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "fresh.toml")
		if err := AtomicWrite(path, []byte("x"), AtomicWriteOptions{FilePerm: 0o644, InheritPerm: true}); err != nil {
			t.Fatalf("AtomicWrite: %v", err)
		}
		if got := permOf(t, path); got != 0o644 {
			t.Errorf("perm = %o, want 644", got)
		}
	})
}

// 中间态不能长成数据文件的扩展名 —— blackboard 只认 *.md、datamanager 只认 *.jsonl，
// 两处的列举过滤全靠这条。
func TestAtomicWriteTmpSuffixDoesNotMimicDataFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "20261017T000000Z_aaaa.md")

	err := AtomicWrite(path, []byte("x"), AtomicWriteOptions{DirPerm: 0o700, TmpSuffix: ".md.tmp"})
	if err != nil {
		t.Fatalf("AtomicWrite: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".md") && e.Name() != filepath.Base(path) {
			t.Errorf("中间态被 *.md 列举当成数据文件: %s", e.Name())
		}
	}
}

// rename 失败必须清掉临时文件：残留会在下一次启动时被当成垃圾重新发现一遍。
// 用一个目录占住目标路径来制造 rename 失败（unix 上文件不能 rename 到目录上）。
func TestAtomicWriteCleansTmpOnRenameFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.json")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}

	err := AtomicWrite(target, []byte("{}"), AtomicWriteOptions{Label: "artifact"})
	if err == nil {
		t.Fatalf("expected rename to fail onto an existing directory")
	}
	if !strings.Contains(err.Error(), "artifact: rename") {
		t.Errorf("error = %q, want it prefixed with the caller label", err)
	}
	assertNoTmpResidue(t, dir)
}

// 同一路径被多个写者同时命中时（两个 TUI tab 的索引写回、向导与主进程写 config.toml），
// 固定 tmp 名会让后来者截断前者的中间态。随机名必须保证每个写者写完整份。
func TestAtomicWriteConcurrentWritersNeverMix(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")

	payloads := make([]string, 8)
	for i := range payloads {
		payloads[i] = strings.Repeat(string(rune('a'+i)), 4096)
	}

	var wg sync.WaitGroup
	for i := 0; i < len(payloads); i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := AtomicWrite(path, []byte(payloads[i]), AtomicWriteOptions{FilePerm: 0o644}); err != nil {
				t.Errorf("writer %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	matched := false
	for _, p := range payloads {
		if string(got) == p {
			matched = true
			break
		}
	}
	if !matched {
		t.Errorf("最终文件是交错内容：长度 %d，前 8 字节 %q", len(got), got[:8])
	}
	assertNoTmpResidue(t, dir)
}

func assertNoTmpResidue(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", dir, err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("临时文件残留: %s", e.Name())
		}
	}
}

func permOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%s): %v", path, err)
	}
	return st.Mode().Perm()
}

func dirExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}
