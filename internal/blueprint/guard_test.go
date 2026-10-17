package blueprint

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// projectWith 造一个"看起来像项目"的目录（有 manifest 或 .git 即通过边界判据）。
func projectWith(t *testing.T, name string, files ...string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	return dir
}

func reasonOf(t *testing.T, err error) string {
	t.Helper()
	var g *GuardError
	if !errors.As(err, &g) {
		t.Fatalf("期望 *GuardError，实得 %T: %v", err, err)
	}
	return g.Reason
}

func TestCheckProjectRootAcceptsRealProjects(t *testing.T) {
	gitDir := projectWith(t, "git-only")
	if err := os.Mkdir(filepath.Join(gitDir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	cases := []string{
		gitDir,                                 // git 仓库，没有任何 manifest
		projectWith(t, "go-project", "go.mod"), // 有 manifest，不是 git
		projectWith(t, "mixed", "Makefile", "pkg.a"), // Makefile 也算边界证据
	}
	for _, p := range cases {
		if err := CheckProjectRoot(p); err != nil {
			t.Errorf("合法项目根被拒绝 %s: %v", p, err)
		}
	}
}

func TestCheckProjectRootRefusesDangerousRoots(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cases := []struct {
		name string
		path string
		want string
	}{
		{"空路径", "", ReasonEmptyPath},
		{"主目录", home, ReasonIsHomeDir},
		{"文件系统根", "/", ReasonIsFileSystemR},
		{"既非 git 也无 manifest", projectWith(t, "loose", "readme.txt"), ReasonNoRepoMarker},
	}
	for _, c := range cases {
		err := CheckProjectRoot(c.path)
		if err == nil {
			t.Errorf("%s: 本该拒绝却通过了", c.name)
			continue
		}
		if got := reasonOf(t, err); got != c.want {
			t.Errorf("%s: reason = %q, want %q", c.name, got, c.want)
		}
	}

	// 主目录里有 go.mod 也照样拒绝：这条判据防的是"在家目录里扫出几千个模块"，
	// 有 manifest 不代表那个目录是项目边界。
	if err := os.WriteFile(filepath.Join(home, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatalf("seed go.mod: %v", err)
	}
	if got := reasonOf(t, CheckProjectRoot(home)); got != ReasonIsHomeDir {
		t.Errorf("带 manifest 的主目录 reason = %q, want %q", got, ReasonIsHomeDir)
	}

	// 主目录的子目录仍然合法 —— 拒绝不能连坐。
	sub := filepath.Join(home, "code", "proj")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("MkdirAll sub: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sub, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := CheckProjectRoot(sub); err != nil {
		t.Errorf("主目录下的项目被误伤: %v", err)
	}
}

func TestCheckStoreRootRefusesInsideProject(t *testing.T) {
	project := projectWith(t, "proj", "go.mod")

	cases := []struct {
		name      string
		storeRoot string
		wantErr   bool
	}{
		{"项目之下", filepath.Join(project, ".yagent-blueprint"), true},
		{"就是项目根", project, true},
		{"深一层也在项目之下", filepath.Join(project, "a", "b", "c"), true},
		{"家目录形态", filepath.Join(t.TempDir(), "blueprint", "proj_1"), false},
		{"项目同级", project + "_sibling", false},
	}
	for _, c := range cases {
		err := CheckStoreRoot(c.storeRoot, project)
		if c.wantErr && err == nil {
			t.Errorf("%s: 本该拒绝（存储根落在项目里 = 会脏工作区、会被 stash）", c.name)
		}
		if !c.wantErr && err != nil {
			t.Errorf("%s: 不该拒绝: %v", c.name, err)
		}
		if c.name == "就是项目根" && err != nil {
			if got := reasonOf(t, err); got != ReasonRootInsideRepo {
				t.Errorf("reason = %q, want %q", got, ReasonRootInsideRepo)
			}
		}
	}
}
