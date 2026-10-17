package datamanager

// persistIndex 的原子写走 util.AtomicWrite（I21）。这里钉三件它对外承诺的事：
// 内容完整、权限 0644、不留中间态 —— 最后一条尤其重要，因为同目录下的
// *.jsonl 才是任务文件，残留的 tmp 会被下一次索引重建当成垃圾反复发现。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPersistIndexIsAtomicAndLeavesNoTmp(t *testing.T) {
	root := t.TempDir()
	// indexPath() = dataDir 的父目录 / indexFileName，所以 dataDir 必须是 root 的子目录。
	// 生产里这个目录由 NewDataManager 建（index.go 不负责建目录，这是它原有的失败语义）。
	dm := &DataManager{dataDir: filepath.Join(root, "data", "tasks")}
	if err := os.MkdirAll(dm.dataDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	idx := &taskIndex{Version: indexVersion, Tasks: map[string]IndexEntry{
		"task-1": {TaskID: "task-1", Title: "第一个任务", MsgCount: 3, FileSize: 42},
	}}
	if err := dm.persistIndex(idx); err != nil {
		t.Fatalf("persistIndex: %v", err)
	}

	raw, err := os.ReadFile(dm.indexPath())
	if err != nil {
		t.Fatalf("索引没落在预期位置 %s: %v", dm.indexPath(), err)
	}
	var back taskIndex
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("索引不是合法 JSON（半份？）: %v", err)
	}
	if back.Version != indexVersion || len(back.Tasks) != 1 || back.Tasks["task-1"].Title != "第一个任务" {
		t.Errorf("索引内容不符: %+v", back)
	}

	st, err := os.Stat(dm.indexPath())
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := st.Mode().Perm(); got != 0o644 {
		t.Errorf("索引权限 = %o, want 644", got)
	}

	leftovers := filesWithSuffix(t, filepath.Dir(dm.indexPath()), ".tmp")
	if len(leftovers) > 0 {
		t.Errorf("临时文件残留: %v", leftovers)
	}
}

// 索引文件被第二次写回时不能因为随机 tmp 名而互相截断 —— 逐字节覆盖即可。
func TestPersistIndexRewriteIsWholeDocument(t *testing.T) {
	root := t.TempDir()
	dm := &DataManager{dataDir: filepath.Join(root, "data", "tasks")}
	if err := os.MkdirAll(dm.dataDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	for i := 0; i < 20; i++ {
		idx := &taskIndex{Version: indexVersion, Tasks: map[string]IndexEntry{
			"task": {TaskID: "task", Title: strings.Repeat("标题", 64), MsgCount: i},
		}}
		if err := dm.persistIndex(idx); err != nil {
			t.Fatalf("persistIndex #%d: %v", i, err)
		}
	}
	raw, err := os.ReadFile(dm.indexPath())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var back taskIndex
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("第 20 次写回后索引不可解析: %v", err)
	}
	if back.Tasks["task"].MsgCount != 19 {
		t.Errorf("最后一次的计数没落进去: %+v", back.Tasks["task"])
	}
}

func filesWithSuffix(t *testing.T, dir, suffix string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), suffix) {
			out = append(out, e.Name())
		}
	}
	return out
}
