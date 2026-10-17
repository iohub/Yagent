package blueprint

import (
	"fmt"
	"os"
	"path/filepath"
)

// 两条根护栏。它们的共同点：**宁可拒绝建蓝图，也不要一份假装可信的蓝图**。
// 没有这两条，"在 $HOME 下随手跑一次 yagent"就会扫出几千个模块，
// 而一份落在项目目录里的蓝图会同时撞上 git 脏工作区与 stash（设计稿 §2 R7）。

// 拒绝建蓝图的原因。每种都必须是能被测试单独命中的分支 ——
// 把三种情况压成一句"root 不可用"，用户就分不清是自己选错了目录还是程序坏了。
const (
	ReasonEmptyPath      = "empty_path"
	ReasonIsHomeDir      = "is_home_dir"
	ReasonIsFileSystemR  = "is_filesystem_root"
	ReasonNoRepoMarker   = "no_repo_marker"
	ReasonRootInsideRepo = "store_root_inside_project"
)

// GuardError 带着可判读的原因。
type GuardError struct {
	Reason string
	Path   string
	Hint   string
}

func (e *GuardError) Error() string {
	return fmt.Sprintf("blueprint: 拒绝建蓝图（%s）: %s —— %s", e.Reason, e.Path, e.Hint)
}

// manifests 是"这是一个项目根"的最低证据。与派生层的文档/配置类白名单同源（设计稿 §4.1）。
var manifests = []string{
	"go.mod", "package.json", "Cargo.toml", "pyproject.toml", "setup.py",
	"Makefile", "CMakeLists.txt", "pom.xml", "build.gradle", "Gemfile",
}

// CheckProjectRoot 拒绝三种危险的项目根：空、$HOME、文件系统根；
// 以及"既不是 git 仓库、也没有任何 manifest"的目录。
//
// 最后一条为什么必要：`max_scan_entries` 只能救慢，救不了"在家目录里扫出 3000 个模块"——
// 那份蓝图之后会被当成这个项目的地图用。
func CheckProjectRoot(projectPath string) error {
	if projectPath == "" {
		return &GuardError{Reason: ReasonEmptyPath, Path: projectPath, Hint: "工作目录为空"}
	}
	clean := filepath.Clean(projectPath)

	if clean == string(filepath.Separator) {
		return &GuardError{Reason: ReasonIsFileSystemR, Path: clean, Hint: "文件系统根不是一个项目"}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" && clean == filepath.Clean(home) {
		return &GuardError{Reason: ReasonIsHomeDir, Path: clean,
			Hint: "主目录里有太多无关目录，请在项目根里运行"}
	}

	if isGit := hasEntry(clean, ".git"); !isGit && !hasAnyManifest(clean) {
		return &GuardError{Reason: ReasonNoRepoMarker, Path: clean,
			Hint: "既不是 git 仓库也没有任何 manifest（go.mod / package.json / Cargo.toml / Makefile …），" +
				"没有可信的项目边界，因此不建蓝图"}
	}
	return nil
}

// CheckStoreRoot 保证存储根不在项目路径之内（I7）。
// 蓝图的**真源**必须在 ~/.yagent：进了工作区就会被 `git status --porcelain` 算成 dirty，
// 而 `[git_checkpoint] stash_dirty_worktree` 用的是 `--include-untracked`，会把真源收进 stash。
func CheckStoreRoot(storeRoot, projectPath string) error {
	if storeRoot == "" || projectPath == "" {
		return &GuardError{Reason: ReasonEmptyPath, Path: storeRoot,
			Hint: "存储根与项目路径都必须非空"}
	}
	rel, err := filepath.Rel(filepath.Clean(projectPath), filepath.Clean(storeRoot))
	if err != nil {
		// 一个绝对一个相对，或跨盘（windows）：无法比较就按不危险处理，但要说出来。
		return nil
	}
	if rel == "." {
		return &GuardError{Reason: ReasonRootInsideRepo, Path: storeRoot,
			Hint: "蓝图存储根与项目根同一个目录"}
	}
	if rel != ".." && !startsWithDotDot(rel) {
		return &GuardError{Reason: ReasonRootInsideRepo, Path: storeRoot,
			Hint: fmt.Sprintf("存储根落在项目里（相对 %s 是 %s），会脏工作区", filepath.Clean(projectPath), rel)}
	}
	return nil
}

func startsWithDotDot(rel string) bool {
	return rel == ".." || (len(rel) > 3 && rel[:3] == "../")
}

func hasEntry(dir, name string) bool {
	_, err := os.Lstat(filepath.Join(dir, name))
	return err == nil
}

func hasAnyManifest(dir string) bool {
	for _, m := range manifests {
		if hasEntry(dir, m) {
			return true
		}
	}
	return false
}
