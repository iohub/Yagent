package blueprint

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// FileRecord 是 WalkDir 单遍收集的一条记录（代码与文档/配置共用）。
type FileRecord struct {
	RelPath string // 相对 POSIX 路径（WalkDir 原生产生，从不过 ..）
	Dir     string // 所在目录（相对 POSIX；根为 "."）
	Base    string
	Lang    string // 代码类：扩展语言；文档类：kind 名
	IsCode  bool
	// IsTest：*_test.go / tests/ 段 / test_*.py（§4.1：计入 tests 不计阈值）。
	IsTest bool
	// IsGenerated：生成物（头行命中）—— 计 generated、不进文件列表。
	IsGenerated bool
}

// collectResult 是单遍遍历的全部产物（partition 与 edges 的输入）。
type collectResult struct {
	Records    []FileRecord
	Dirs       []string // 走到的目录集合（相对 POSIX，含 "."），排序
	ModulePath string   // go.mod 的 module 值（非 Go 仓库为空）
	Submodules []string // .gitmodules 声明的相对目录，排序
	// SubmoduleHits 是 walk 中在【目录分支】实际命中的 submodule 目录（黑名单优先，
	// 被黑名单挡住的声明不会命中）。partition 据此整片 emit、不递归。
	SubmoduleHits    []string
	TotalEntries     int
	Truncated        bool
	TruncReason      string
	TruncAt          string
	RejectedSymlinks int
}

var (
	errStopEntries  = errors.New("blueprint: 扫描条目预算耗尽")
	errStopDeadline = errors.New("blueprint: 扫描墙钟超时")
)

// collect 单遍遍历项目根（§4.1）。目录分支的判定顺序即正确性：
// 黑名单/点前缀短路 → .gitmodules 命中（【目录分支】）→ 常规入集合。
func collect(root string, o Options) (*collectResult, error) {
	cr := &collectResult{}

	// go.mod 与 .gitmodules 先于 walk 读：分别供边抽取的包前缀与目录分支的 submodule 判定。
	// 读失败按"文件不存在"处理（非 Go 仓库 / 无 submodule 都不是错误，I9）。
	if data, err := os.ReadFile(filepath.Join(root, "go.mod")); err == nil {
		cr.ModulePath = parseGoModule(data)
	}
	if data, err := os.ReadFile(filepath.Join(root, ".gitmodules")); err == nil {
		cr.Submodules = parseGitmodules(data)
	}
	subSet := make(map[string]bool, len(cr.Submodules))
	for _, p := range cr.Submodules {
		subSet[p] = true
	}

	deadline := time.Now().Add(o.Deadline)

	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err // 读不到的条目如实报错：宁可失败，不可静默少一片
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		cr.TotalEntries++
		if cr.TotalEntries > o.MaxEntries {
			// 超限停在当前层（§4.1：宁可说"没看完"，不可静默少一片，I4）。
			cr.Truncated, cr.TruncReason, cr.TruncAt = true, "entries", rel
			return errStopEntries
		}
		if time.Now().After(deadline) {
			cr.Truncated, cr.TruncReason, cr.TruncAt = true, "deadline", rel
			return errStopDeadline
		}
		if d.IsDir() {
			return visitDir(rel, d, subSet, o, cr)
		}
		return visitFile(root, rel, d, cr)
	})
	if walkErr != nil && !errors.Is(walkErr, errStopEntries) && !errors.Is(walkErr, errStopDeadline) {
		return nil, walkErr
	}
	sort.Strings(cr.Dirs)
	sort.Strings(cr.SubmoduleHits)
	return cr, nil
}

// visitDir 是目录分支（§4.1 的排除规则，顺序即正确性）。
func visitDir(rel string, d fs.DirEntry, subSet map[string]bool, o Options, cr *collectResult) error {
	cr.Dirs = append(cr.Dirs, rel)
	if rel == "." {
		// 根目录不做黑名单检查（fixture 根 testdata/tree/ 依赖这条豁免）。
		return nil
	}
	base := d.Name()
	// 黑名单 + 任何 `.` 开头目录（§4.1）。
	if o.Blacklist[base] || strings.HasPrefix(base, ".") {
		return filepath.SkipDir
	}
	// §4.1：submodule 在【目录分支】跳过 —— 命中即整片一张 leaf、不递归。
	// v3 实测教训：把这条写在文件分支上，多出 14 片假模块（闸门 I6）。
	if subSet[rel] {
		cr.SubmoduleHits = append(cr.SubmoduleHits, rel)
		return filepath.SkipDir
	}
	return nil
}

// visitFile 是文件分支：白名单分级 → 测试标记 → 生成物探针。
func visitFile(root, rel string, d fs.DirEntry, cr *collectResult) error {
	base := d.Name()

	// 符号链接一律拒入（I6）：WalkDir 本就不跟随，这里把链接条目计数并丢弃 ——
	// 一次 ln -s / 就能把扫描器带出项目根；链接指向文件同样拒（宁缺勿越根）。
	if d.Type()&fs.ModeSymlink != 0 {
		cr.RejectedSymlinks++
		return nil
	}

	dir := parentDir(rel)
	ext := strings.ToLower(filepath.Ext(base))
	// 文档/配置类（§4.1 白名单后半段）：不计阈值，leaf 单列一行。
	if kind, ok := docFileKinds[base]; ok {
		cr.Records = append(cr.Records, FileRecord{RelPath: rel, Dir: dir, Base: base, Lang: kind})
		return nil
	}
	if kind, ok := docKinds[ext]; ok {
		cr.Records = append(cr.Records, FileRecord{RelPath: rel, Dir: dir, Base: base, Lang: kind})
		return nil
	}
	// 代码类：只有这一类计规模阈值（§4.1）。
	lang, ok := codeLangs[ext]
	if !ok {
		return nil
	}
	rec := FileRecord{RelPath: rel, Dir: dir, Base: base, Lang: lang, IsCode: true}
	// 测试分级（§4.1）：*_test.go / tests/ 段 / test_*.py 计入 tests 不计阈值。
	switch {
	case strings.HasSuffix(base, "_test.go"),
		path.Base(dir) == "tests",
		lang == "python" && strings.HasPrefix(base, "test_"):
		rec.IsTest = true
	}
	// 生成物按头行识别：只读前 4KB（§4.1）。生成物计 generated、不进文件列表。
	if lang == "go" && !rec.IsTest && isGeneratedFile(filepath.Join(root, rel)) {
		rec.IsGenerated = true
	}
	cr.Records = append(cr.Records, rec)
	return nil
}

func parentDir(rel string) string {
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		return rel[:i]
	}
	return "."
}

// isGeneratedFile 读前 4KB 匹配生成物头行（§4.1）。
// 读失败按"不是生成物"处理 —— 文件读不了不该让扫描中止，但也不能猜它是生成物。
func isGeneratedFile(abs string) bool {
	f, err := os.Open(abs)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 4096)
	n, _ := f.Read(buf)
	return n > 0 && generatedRe.Match(buf[:n])
}
