package blueprint

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// 本文件是设计稿 §5 的"三条机械判据"里的那两条可判定部分：
//
//	(a) 结构指纹不符 → 整片注解可疑（答"这个模块形状变了"）
//	(b) 出处锚点复核 → 这句话指的那个东西没了（答"这个坐标还在不在"）
//
// 两者各自只能答一件事，所以都要有：只有 (a) 时"结构没变但结论过期"会长期冒充事实，
// 只有 (b) 时一次整体重构里，每条锚点仍可单独成立而整片说法已经错了。
//
// 第三条（孤儿）在 gc.go 与 visibleSet 里，判据同样是派生集合，不依赖这里。

// sourceExts 是"路径样 token"的判据材料（§7.5 第 3 条）：含 `/` 或以这些扩展名结尾。
// 与扫描器的代码类白名单同源的一部分；这里刻意不含 .py/.rs 之外的全部 ——
// 判据宁严勿宽：误判成路径就多一次成员校验，漏判则让模型拿假路径支撑假断言。
var sourceExts = []string{
	".go", ".rs", ".c", ".h", ".cpp", ".hpp", ".py", ".java", ".kt",
	".ts", ".tsx", ".js", ".jsx", ".mjs", ".vue", ".proto", ".sql",
	".md", ".yaml", ".yml", ".json", ".toml", ".sh", ".mk",
}

// looksLikePath 报告一个 token 是否应当被当作路径对待。
func looksLikePath(token string) bool {
	if token == "" {
		return false
	}
	if strings.Contains(token, "/") {
		return true
	}
	lower := strings.ToLower(token)
	for _, ext := range sourceExts {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	// 裸 Makefile/go.mod 这类无扩展名但确实是文件的 manifest。
	switch lower {
	case "makefile", "go.mod", "go.sum", "package.json", "cargo.toml", "agents.md", "readme.md":
		return true
	}
	return false
}

// AnchorState 是一次锚点复核的结果三态（I19）。
type AnchorState struct {
	// State 是 StateOK / StateStale / StateUnanchored 之一。
	State string
	// Unverifiable 表示锚点不是路径形态（"event bus is one-shot"这类跨模块论断），
	// 程序判不了。**不谎称验过**：它记进 Coverage 的 unverifiable 计数。
	Unverifiable bool
	// Reason 在人话层面说明为什么判成 stale（文件缺失 / 行号越界 / 符号不见了）。
	Reason string
}

// AnchorChecker 复核 `where` 锚点（§5.2(b) 的表）。
//
// 一次 fold 内缓存每个文件的行数与存在性：全档重验只在 CLI 跑（§5.2"时机"），
// fold 只验本次涉及的原子，所以这份缓存的生命周期就是一次 fold。
type AnchorChecker struct {
	root   string
	lines  map[string]int
	exists map[string]bool
}

func NewAnchorChecker(root string) *AnchorChecker {
	return &AnchorChecker{
		root:   root,
		lines:  map[string]int{},
		exists: map[string]bool{},
	}
}

// Check 判定一条锚点。where 为空 → unanchored（不是 stale：没声明出处就不该被追究）。
func (c *AnchorChecker) Check(where string) AnchorState {
	where = strings.TrimSpace(where)
	if where == "" {
		return AnchorState{State: StateUnanchored}
	}
	pathPart, suffix, hasSuffix := strings.Cut(where, ":")
	if !looksLikePath(pathPart) && !hasSuffix {
		// 非路径锚点：不判定，state 保持 ok，但计入 unverifiable。
		return AnchorState{State: StateOK, Unverifiable: true}
	}
	if !looksLikePath(pathPart) {
		// 形如 `foo:bar` 且 foo 不像路径 → 同样归入"判不了"。
		return AnchorState{State: StateOK, Unverifiable: true}
	}
	if !c.fileExists(pathPart) {
		return AnchorState{State: StateStale, Reason: "锚点文件不存在: " + pathPart}
	}
	if !hasSuffix || suffix == "" {
		return AnchorState{State: StateOK}
	}
	// `path/` 形态（目录锚点）在 Cut(":") 之后 suffix 为空，已返回。
	if n, err := strconv.Atoi(suffix); err == nil {
		// path:line
		if total := c.lineCount(pathPart); n <= 0 || n > total {
			return AnchorState{
				State:  StateStale,
				Reason: "锚点行号越界: " + pathPart + ":" + suffix,
			}
		}
		return AnchorState{State: StateOK}
	}
	// path:symbol —— 朴素匹配 `\bName\b`（§5.2(b)：不依赖 codeseek，
	// 索引在建或不可用是常态）。
	if !c.containsSymbol(pathPart, suffix) {
		return AnchorState{
			State:  StateStale,
			Reason: "锚点符号已不在该文件: " + pathPart + ":" + suffix,
		}
	}
	return AnchorState{State: StateOK}
}

func (c *AnchorChecker) fileExists(rel string) bool {
	if v, ok := c.exists[rel]; ok {
		return v
	}
	full := filepath.Join(c.root, filepath.FromSlash(rel))
	// Lstat 而不是 Stat：符号链接指向哪里不由锚点负责，存在即算存在。
	_, err := os.Lstat(full)
	ok := err == nil
	c.exists[rel] = ok
	return ok
}

// lineCount 数一个文件的行数（带缓存）。读不动就当 0 行 —— 判 stale 而不是崩。
func (c *AnchorChecker) lineCount(rel string) int {
	if v, ok := c.lines[rel]; ok {
		return v
	}
	n := countLines(filepath.Join(c.root, filepath.FromSlash(rel)))
	c.lines[rel] = n
	return n
}

func (c *AnchorChecker) containsSymbol(rel, name string) bool {
	data, err := os.ReadFile(filepath.Join(c.root, filepath.FromSlash(rel)))
	if err != nil {
		return false
	}
	return matchWord(string(data), name)
}

// countLines 用 bufio 数换行，不把整个大文件拉进内存第二次。
func countLines(full string) int {
	f, err := os.Open(full)
	if err != nil {
		return 0
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	count := 0
	for scanner.Scan() {
		count++
	}
	return count
}

// wordReCache 是 `\bName\b` 判定的编译缓存。标识符里出现的 `.`（config.Map）
// 不是合法正则片段，所以 quote 掉再拼边界。
var wordReCache = map[string]*regexp.Regexp{}

func matchWord(text, name string) bool {
	if name == "" {
		return false
	}
	re, ok := wordReCache[name]
	if !ok {
		re = regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`)
		wordReCache[name] = re
	}
	return re.MatchString(text)
}

// StaleModules 报告哪些片模块的注解**早于**一次结构变化（§5.2(a)）。
//
// 判据是"文档里记过指纹、且与本次派生的指纹不符"。没记指纹不算过期 ——
// 没有基线就没有证据说它变了，把它标 stale 会把"第一次写注解的模块"全标可疑。
// 派生集合里根本没有这片（孤儿）也不在这里报：那是 gc.go 与可见集合的职责。
func StaleModules(doc *Doc, snap *Snapshot) map[string]bool {
	stale := map[string]bool{}
	if doc == nil || snap == nil || len(doc.Front.ModuleFingerprints) == 0 {
		return stale
	}
	blocks, _ := doc.Sections()
	for _, b := range blocks {
		if b.Module == "" {
			continue
		}
		want, recorded := doc.Front.ModuleFingerprints[b.Module]
		if !recorded || want == "" {
			continue
		}
		leaf, ok := snap.moduleByDir(b.Module)
		if !ok {
			continue // 孤儿：由可见集合挡掉，不在这里冒充结构变化
		}
		if leaf.Fingerprint != want {
			stale[b.Module] = true
		}
	}
	return stale
}

// moduleByDir 按片路径取 Leaf（Leaves 已按 Dir 排序，二分即可）。
func (s *Snapshot) moduleByDir(dir string) (Leaf, bool) {
	lo, hi := 0, len(s.Leaves)-1
	for lo <= hi {
		mid := (lo + hi) / 2
		switch {
		case s.Leaves[mid].Dir == dir:
			return s.Leaves[mid], true
		case s.Leaves[mid].Dir < dir:
			lo = mid + 1
		default:
			hi = mid - 1
		}
	}
	return Leaf{}, false
}

// DerivedModuleSet 是本次派生的模块集合（§5.3 的"可见集合 == 派生集合"的左半边）。
func (s *Snapshot) DerivedModuleSet() map[string]bool {
	out := map[string]bool{}
	if s == nil {
		return out
	}
	for i := range s.Leaves {
		out[s.Leaves[i].Dir] = true
	}
	return out
}

// DerivedModuleList 是同一份集合的**稳定序**（Leaves 已按 Dir 排序）。
// 注入前缀的匹配、read_blueprint 的候选都要遍历它，两边必须共用一份实现。
func (s *Snapshot) DerivedModuleList() []string {
	if s == nil {
		return nil
	}
	out := make([]string, 0, len(s.Leaves))
	for i := range s.Leaves {
		out = append(out, s.Leaves[i].Dir)
	}
	return out
}
