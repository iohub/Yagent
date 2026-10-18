package blueprint

import (
	"regexp"
	"time"
)

// R2 扫描器的常数集中定义（设计稿 §10：配置只有 5 个键，其余是常数）。
// 这些常数将来进 [blueprint] 配置注释时逐字引用 —— 别处不许出现魔法数。
const (
	// MaxFilesPerModule 是 §4.2 的规模阈值：取 40（"零超阈值片"的最小值）。
	// v3/v4 复测：12→40 片数几乎不动，64 才明显合并 —— 调它不是"索引太大"的自救手段。
	MaxFilesPerModule = 40
	// MaxDepth 是 §4.2 的递归深度上限：depth >= 4 即 emit leaf。
	MaxDepth = 4
	// MaxScanEntries 是 §4.1 的硬预算。它只能救慢，救不了"在家目录里扫出 3000 个模块"
	// —— 那条由 CheckProjectRoot 拒（I16）。
	MaxScanEntries = 20000
	// ScanDeadline 是单次扫描的墙钟上限（§4.1）。超限停在当前层、标 TRUNC ——
	// 宁可说"没看完"，不可静默少一片（I4）。
	ScanDeadline = 2 * time.Second
	// DisplayFileCap 是 leaf.files 的显示截断（§4.2 结论 2：骨架的能力上限必须说出来，
	// 否则模型以为那 24 个文件就是全部）。
	DisplayFileCap = 24
	// DisplayEdgeCap 是模块级出/入边的显示截断（I3/I4：截断必须写 +N）。
	DisplayEdgeCap = 24
	// EntryCap 与 TargetCap 是入口与 Makefile target 名单的显示截断。
	EntryCap  = 16
	TargetCap = 16
)

// generatedRe 是 stdlib 生成物头行（§4.1）：只看每个文件的前 4KB，
// 命中即计入 generated、不进文件列表 —— 生成物是机器写的，不是"这片负责什么"的证据。
var generatedRe = regexp.MustCompile(`(?m)^// Code generated .* DO NOT EDIT\.$`)

// codeLangs 是代码类扩展名 → 语言名（§4.1 白名单）。只有代码类计入规模阈值；
// 语言名同时是直方图与 MissingExtractors 的 key（§4.3：没有抽取器的语言必须说出来，不留空）。
var codeLangs = map[string]string{
	".go": "go", ".rs": "rust",
	".c": "c", ".h": "c", ".cpp": "cpp", ".hpp": "cpp",
	".py": "python", ".java": "java", ".kt": "kotlin",
	".ts": "ts", ".tsx": "ts", ".js": "js", ".jsx": "js", ".mjs": "js",
	".vue": "vue", ".proto": "proto", ".sql": "sql",
}

// docKinds 是文档/配置类扩展名 → kind 名。文档类不计阈值，在 leaf 单列一行
// （如 `docs: 7 md`）—— 200 篇文档的仓库不该被切成 9 个"文档模块"。
var docKinds = map[string]string{
	".md": "md", ".yaml": "yaml", ".yml": "yaml", ".json": "json",
	".toml": "toml", ".sh": "sh", ".ps1": "ps1", ".mk": "makefile",
}

// docFileKinds 是白名单后半段：无扩展名或特殊名的文档/配置类文件。
var docFileKinds = map[string]string{
	"Makefile": "makefile", "go.mod": "go.mod", "package.json": "json", "Cargo.toml": "toml",
}

// DefaultBlacklist 是目录黑名单（§4.1）。任何 `.` 开头目录在 collect 里另行短路，
// 不进这个集合 —— 集合只收显式点名的目录名。
var DefaultBlacklist = []string{
	".git", ".yagent", "node_modules", "vendor", "dist", "build", "out",
	"target", "__pycache__", "testdata", ".next", "coverage", "bin",
}

// Options 是一次扫描的参数。零值经 fillDefaults 填默认。
//
// 黑名单做成可注入字段，不只是为了测试方便：它让"黑名单不是硬编码"成为可变异验证的
// 断言（fixture 命名冲突的另一半解法 —— 扫描根的 base 名不做黑名单检查，
// 所以 internal/blueprint/testdata/tree/ 这个 fixture 根可以进扫描）。
type Options struct {
	// Blacklist 为 nil 时用 DefaultBlacklist 拷贝。
	Blacklist map[string]bool
	// 以下为零值时的默认来源（常量，见上）。
	MaxFilesPerModule int
	MaxDepth          int
	MaxEntries        int
	Deadline          time.Duration
	FileCap           int
	EdgeCap           int
	EntryCap          int
	TargetCap         int
}

// DefaultOptions 返回填好默认值的参数集。
func DefaultOptions() Options {
	return fillDefaults(Options{})
}

func fillDefaults(o Options) Options {
	if o.Blacklist == nil {
		o.Blacklist = make(map[string]bool, len(DefaultBlacklist))
		for _, name := range DefaultBlacklist {
			o.Blacklist[name] = true
		}
	}
	if o.MaxFilesPerModule <= 0 {
		o.MaxFilesPerModule = MaxFilesPerModule
	}
	if o.MaxDepth <= 0 {
		o.MaxDepth = MaxDepth
	}
	if o.MaxEntries <= 0 {
		o.MaxEntries = MaxScanEntries
	}
	if o.Deadline <= 0 {
		o.Deadline = ScanDeadline
	}
	if o.FileCap <= 0 {
		o.FileCap = DisplayFileCap
	}
	if o.EdgeCap <= 0 {
		o.EdgeCap = DisplayEdgeCap
	}
	if o.EntryCap <= 0 {
		o.EntryCap = EntryCap
	}
	if o.TargetCap <= 0 {
		o.TargetCap = TargetCap
	}
	return o
}
