package blueprint

import (
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// 本文件是 R2 扫描器的输出契约：Snapshot 及其子类型（设计稿 §3.1 的派生层形状）。
//
// 不可变语义（I15 的 v1 形态）：所有字段只在 Scan 装配时一次性赋值，无任何 setter；
// 每次 Scan 返回全新副本，消费方只读。R3 用 atomic.Pointer 做快照替换时依赖这条。
// 不设时间戳/随机字段 —— 两次扫描同一仓库必须 DeepEqual（确定性测试钉这条）。

// Edge 是一条模块级依赖边（§4.3 的目录→目录边聚合到模块级的产物）。
// From/To 是片路径（相对 POSIX，根片为 "."）；Imports 是聚合的 import 条数 ——
// 渲染层据此写 `+N`（I3/I4：截断必须被说出来）。
type Edge struct {
	From    string
	To      string
	Imports int
	// Source 标明这条边怎么来的（§4.3：`*_source` 必须原样渲染，"猜的"与"AST 验的"混写
	// 是蓝图最自我否定的失效模式）。v1 只实现 Go 抽取，恒为 SourceAST。
	Source string
}

// 事实来源三态（§4.3 的 `*_source`）。
const (
	SourceAST      = "ast"                // go/parser 验证过
	SourceManifest = "manifest"           // Cargo.toml / package.json 声明
	SourceFilename = "filename-heuristic" // 文件名存在性（Makefile target / build.sh）
)

// Entry 是一个入口/命令事实（§4.3）。只列名字，绝不执行。
type Entry struct {
	Kind   string // go-main / node-main / node-bin / node-script / cargo-bin
	Name   string
	Source string
}

// Leaf 是一片模块：§4.2 emit 的单位，`## Modules/<path>` 子节的数据源。
// 片路径（Dir）用相对 POSIX 路径；根片为 "."。
type Leaf struct {
	Dir string
	// Submodule 表示这片是 .gitmodules 声明的子模块：整片一张 leaf、不递归（§4.1 实测
	// bug：把这条写在文件分支会多出 14 片假模块），Files 恒空。
	Submodule bool

	// Files 是该片覆盖范围内保留的代码文件（含测试、不含生成物），相对 POSIX 路径，
	// 按显示截断。FilesTotal/OmittedFiles/Oversize 三者把截断说出来（§4.2 结论 2）。
	Files        []string
	FilesTotal   int
	OmittedFiles int
	Oversize     bool

	// TestsCount：*_test.go / tests/ 段 / test_*.py 的数量 —— 计入此处，不计阈值
	// （§4.1：1:1 配测试的仓库否则永远拆不完）。
	TestsCount int
	// GeneratedCount：生成物数量 —— 计此数、不进 Files（生成物不是"这片负责什么"的证据）。
	GeneratedCount int

	// DocsByKind 是文档/配置类计数，如 {md:7, yaml:1}（§4.1：渲染层单列一行 `docs: 7 md`）。
	DocsByKind map[string]int
	// LangHistogram 是保留代码文件的扩展语言直方图。
	LangHistogram map[string]int

	// EdgesOut/EdgesIn 是模块级出/入边，按 To/From 排序后按 EdgeCap 截断显示；
	// Total 字段是未截断总数。指纹用的是全量（见 scan_fingerprint.go）。
	EdgesOut      []Edge
	EdgesOutTotal int
	EdgesIn       []Edge
	EdgesInTotal  int

	// Entries 是入口/命令事实（cap 后）；EntriesTotal 是未截断数。
	Entries      []Entry
	EntriesTotal int

	// MissingExtractors 是直方图里没有对应边抽取器的语言（§4.3：其他语言渲染
	// `edges: (no extractor for rust)` 之类，不留空 —— 空读起来像"确实没有依赖"，那是假事实）。
	MissingExtractors []string

	// Fingerprint 是 §5.2(a) 的结构指纹：sha256(排序后的文件列表 + 语言直方图 + 出边 + 入边)。
	// R3 写进 front-matter 的 module_fingerprints；两次扫描间逐位相同（确定性）。
	Fingerprint string
}

// Module 是 Leaf 的别名 —— 蓝图文档与 R6 校验用的是"模块"这个词，
// 代码里的单位是片；两者是同一个实体，别再分裂出第三种叫法。
type Module = Leaf

// CommandFacts 是仓库根的构建/测试命令事实（§4.3 最后一行）。
// 只列名字，绝不执行 —— 蓝图派生层永不在任务关键路径上跑构建。
type CommandFacts struct {
	MakeTargets          []string
	MakeTargetsTruncated bool
	HasBuildSh           bool
	Source               string // 恒 SourceFilename
}

// Snapshot 是一次扫描的完整派生结果：R3 渲染与 R6 证据映射的唯一数据源。
// 消费方只读；每次 Scan 返回全新副本。
type Snapshot struct {
	Root string // 清洗后的绝对项目根

	// ModulePath 是 go.mod 的 module 值（边抽取的包前缀）；非 Go 仓库为空。
	// 它的 *_source 恒为 SourceManifest（来自 go.mod 文本，非 AST）。
	ModulePath string

	// Leaves 按 Dir 排序。
	Leaves []Leaf

	// TotalEntries 是 walk 访问的条目总数（预算消耗的账目）。
	TotalEntries int

	// Truncated/TruncReason/TruncAt 把"没看完"说出来（§4.1：超限停在当前层，
	// 宁可说没看完，不可静默少一片）。TruncReason ∈ {"", "entries", "deadline"}。
	Truncated   bool
	TruncReason string
	TruncAt     string

	// RejectedSymlinks 是被拒入的符号链接条目数（I6：一次 ln -s / 就能把扫描器带出项目根）。
	RejectedSymlinks int

	// OrphanDocs 是没有任何片覆盖的文档数（根 own==0 且多 children 时的顶层文档）。
	OrphanDocs       int
	OrphanDocsByKind map[string]int

	// Commands 是根 Makefile/build.sh 事实。
	Commands CommandFacts

	// dirIndex 是"覆盖目录 → 片路径"的索引，Scan 装配时冻结 ——
	// ModuleOf/EvidenceModules 依赖它，消费方不可见也不可改。
	dirIndex map[string]string
}

// ModuleOf 把一个文件路径映射到覆盖它的片（R6 证据映射的原子操作）。
// filePath 接受相对 POSIX 路径或以 Root 为前缀的绝对路径；先 Clean 归一，
// 再按"目录精确命中 → 逐级上溯"查 dirIndex。未命中返回 ("", false)。
//
// 为什么上溯：证据路径常常是文件（scan.go），而 dirIndex 记的是目录（internal/blueprint）。
func (s *Snapshot) ModuleOf(filePath string) (string, bool) {
	if s == nil || len(s.dirIndex) == 0 || filePath == "" {
		return "", false
	}
	p := filepath.ToSlash(filePath)
	// 绝对路径剥 Root 前缀（R6 拿到的证据常是绝对路径）。
	if s.Root != "" {
		root := filepath.ToSlash(s.Root)
		if p == root {
			p = "."
		} else if strings.HasPrefix(p, root+"/") {
			p = p[len(root)+1:]
		}
	}
	p = path.Clean(p)
	if p == ".." || strings.HasPrefix(p, "../") {
		return "", false // 越出项目根的证据不映射（I6 同源）
	}
	for {
		if leaf, ok := s.dirIndex[p]; ok {
			return leaf, true
		}
		if p == "." {
			return "", false
		}
		p = path.Dir(p)
	}
}

// EvidenceModules 把"本次 run 真打开过的文件"映射到派生模块集（R6 校验
// "module ∈ 派生模块集合"的入参）。去重、排序（确定性）；未命中项静默丢弃 ——
// 证据路径越出项目根时它本来就不属于任何派生模块。
func (s *Snapshot) EvidenceModules(files []string) []string {
	if s == nil || len(files) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(files))
	var out []string
	for _, f := range files {
		m, ok := s.ModuleOf(f)
		if !ok || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}
