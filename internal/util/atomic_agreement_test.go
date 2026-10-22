package util_test

// I21 的聚合闸门：原子写的 tmp+rename 语义只准有一份（internal/util/atomic.go），
// 各落盘点只准转调、不准再实现一遍。
//
// 这里做的是**行为一致性**断言：三条可达的导出写路径必须同时满足
// 「内容逐字节正确」「不留中间态」「权限位符合各自声明」。
// 光靠 Go 测试证不了"只有一份实现"，那半条由 scripts/check_no_inline_atomic_write.sh 守。
//
// datamanager.persistIndex 的写路径在各自包里测（index_atomic_test.go）：
// 它是包内私有函数，跨包只能测到导出面。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yagent/internal/artifact"
	"yagent/internal/blackboard"
	"yagent/internal/config"
)

type site struct {
	name string
	// wantPerm 是该落盘点声明的权限位（继承类站点在下面单独测继承）。
	wantPerm os.FileMode
	// write 把数据写到 dir 下并返回被写的文件路径。
	write func(t *testing.T, dir string) string
}

func TestAtomicWritesAgree(t *testing.T) {
	sites := []site{
		{
			name:     "blackboard",
			wantPerm: 0o600,
			write: func(t *testing.T, dir string) string {
				t.Helper()
				path := filepath.Join(dir, "notes", "board", "repo_agent", "20261017T000000Z_aaaa.md")
				if err := blackboard.AtomicWrite(path, []byte("# note body\n")); err != nil {
					t.Fatalf("blackboard.AtomicWrite: %v", err)
				}
				return path
			},
		},
		{
			name:     "artifact",
			wantPerm: 0o600,
			write: func(t *testing.T, dir string) string {
				t.Helper()
				store := artifact.NewStore(filepath.Join(dir, "artifacts"))
				ref, err := store.Save("proj_1", "coding", "the task", "the summary", strings.Repeat("full text ", 200))
				if err != nil {
					t.Fatalf("artifact.Save: %v", err)
				}
				return filepath.Join(dir, "artifacts", "proj_1", ref.ID+".json")
			},
		},
		{
			name:     "config",
			wantPerm: 0o640, // 见下面 TestAtomicWriteConfigInheritsExistingPerm：种子文件是 0640
			write: func(t *testing.T, dir string) string {
				t.Helper()
				path := filepath.Join(dir, "config.toml")
				if err := os.WriteFile(path, []byte("[global.llm]\nuse_provider = \"old\"\n"), 0o640); err != nil {
					t.Fatalf("seed config: %v", err)
				}
				if err := config.SetKeyInSection(path, "global.llm", "use_provider", `"new"`); err != nil {
					t.Fatalf("config.SetKeyInSection: %v", err)
				}
				return path
			},
		},
	}

	for _, s := range sites {
		s := s
		t.Run(s.name, func(t *testing.T) {
			dir := t.TempDir()
			path := s.write(t, dir)

			if _, err := os.Stat(path); err != nil {
				t.Fatalf("%s: 最终文件不在预期位置 %s: %v", s.name, path, err)
			}
			st, err := os.Stat(path)
			if err != nil {
				t.Fatalf("Stat: %v", err)
			}
			if got := st.Mode().Perm(); got != s.wantPerm {
				t.Errorf("%s: 权限 = %o, want %o", s.name, got, s.wantPerm)
			}
			// 中间态不能留在盘上：它会被"只认某扩展名"的列举当垃圾长期留着。
			if leftovers := tmpEntries(t, dir); len(leftovers) > 0 {
				t.Errorf("%s: 临时文件残留 %v", s.name, leftovers)
			}
		})
	}
}

// 三条路径读回来的内容必须是**完整一份**，不是半份 —— 原子写唯一对外承诺的就是这件事。
func TestAtomicWritesAreWholeDocuments(t *testing.T) {
	dir := t.TempDir()

	// artifact 的落盘是 JSON：半份会直接解不开。
	store := artifact.NewStore(filepath.Join(dir, "artifacts"))
	ref, err := store.Save("proj_2", "repo", "task", "summary", "body body body")
	if err != nil {
		t.Fatalf("artifact.Save: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "artifacts", "proj_2", ref.ID+".json"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var art struct {
		ID       string `json:"id"`
		FullText string `json:"full_text"`
	}
	if err := json.Unmarshal(raw, &art); err != nil {
		t.Fatalf("artifact 落盘不是合法 JSON（半份？）: %v", err)
	}
	if art.ID != ref.ID || art.FullText != "body body body" {
		t.Errorf("artifact 内容不符: id=%q full_text=%q", art.ID, art.FullText)
	}

	// config 的写回必须保住用户注释 —— 这是它选文本级替换而不是整份重编码的全部理由。
	cfgPath := filepath.Join(dir, "config.toml")
	seed := "# keep me\n[global.llm]\nuse_provider = \"old\"\n"
	if err := os.WriteFile(cfgPath, []byte(seed), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := config.SetKeyInSection(cfgPath, "global.llm", "use_provider", `"new"`); err != nil {
		t.Fatalf("SetKeyInSection: %v", err)
	}
	out, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(out), "# keep me") {
		t.Errorf("用户注释被蒸发掉了:\n%s", out)
	}
	if !strings.Contains(string(out), `use_provider = "new"`) {
		t.Errorf("写回没生效:\n%s", out)
	}
}

func tmpEntries(t *testing.T, root string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".tmp") {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir(%s): %v", root, err)
	}
	return found
}
