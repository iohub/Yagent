package blueprint

import (
	"fmt"
	"strings"
)

// 本文件是设计稿 §6.1 的形状②（spine + edges）渲染器：常驻注入的唯一形态。
//
// 定案沿用 v3：常驻只用 ②，purpose 永不进常驻 —— 边是唯一无可替代的确定性事实，
// purpose 是唯一不可验证的散文（§1.2 的对账表）。散文的入口只有 read_blueprint。
//
// 两条渲染纪律比格式本身更重要：
//   - **前缀稳定**（§6.2）：行序 = Dir 字典序，截断只砍尾部。注入文本进 system prompt，
//     前缀稳定才能命中 provider 缓存；每次 fold 后整片重排会让账单翻倍。
//   - **截断必须被说出来**（I3/I4）：`+N modules not shown` 进 header，
//     stale/unannotated/TRUNC/oversize 四态进索引行。静默少一片是蓝图最坏的失效。

// 形状②的显示常数（§6.3：v3 的两个键降为常数，索引只受 index_max_tokens 一个阀门管）。
const (
	IndexEdgeOutShow = 4
	IndexEdgeInShow  = 3

	// 四个标记是 I4 点名的四态，必须都能在索引行看到；submodule 是第五个事实但不属于四态。
	IndexFlagStale       = "stale"
	IndexFlagUnannotated = "unannotated"
	IndexFlagTrunc       = "TRUNC"
	IndexFlagOversize    = "oversize"
	IndexFlagSubmodule   = "submodule"
)

// ScanningLine 是 I15 的那句"没就绪就说不就绪"：注入侧绝不阻塞等派生。
const ScanningLine = "blueprint: scanning repository structure… (no index yet)"

// IndexSpec 是渲染常驻骨架需要的输入。
//
// CountTokens 由调用方注入（生产传 tokenutil.EstimateTokens）：本包是 leaf 包，
// 不 import tokenutil —— 把计数器做成参数同时让每条预算测试都无需真编码器。
// 传 nil 时退回 `len/4`，即 tokenutil 自己的降级口径（§6.1 注明它是**下限**）。
type IndexSpec struct {
	MaxTokens   int
	CountTokens func(string) int
}

// Index 是渲染结果。消费方（三处注入、read_blueprint 的目录视图、`/blueprint`）
// 只读 Header+Lines，不自己再拼一遍。
type Index struct {
	Ready        bool     // false = 派生还没跑完，调用方渲染 ScanningLine
	Header       []string // 账目行 + 行动理由行 + 截断行
	Lines        []string // 每片一行，Dir 字典序
	Budget       int      // 生效的 index_max_tokens，截断行要把它说出来
	TotalModules int
	ShownModules int
	Omitted      int // 因预算没渲染的片数（I3）

	StaleModules      int // 注解早于一次结构变化的片数（§5.2(a)）
	UnannotatedModule int // 一片散文都没有的片数
	Atoms             int
	StaleAtoms        int
	UnanchoredAtoms   int
	Unverifiable      int // where 非路径形态、程序判不了（不谎称验过）
	Tokens            int
}

// String 拼成注入用的整块文本。
func (i Index) String() string {
	if !i.Ready {
		return ScanningLine
	}
	all := make([]string, 0, len(i.Header)+len(i.Lines)+1)
	all = append(all, i.Header...)
	all = append(all, i.Lines...)
	return strings.Join(all, "\n")
}

// RenderIndex 把派生快照 + 落盘散文渲染成形状②。
// snap 为 nil（还没扫完）时返回 Ready=false；doc 为 nil 时按"还没有任何注解"处理。
func RenderIndex(snap *Snapshot, doc *Doc, spec IndexSpec) Index {
	if snap == nil {
		return Index{}
	}
	count := spec.CountTokens
	if count == nil {
		count = fallbackCount
	}
	budget := spec.MaxTokens
	if budget <= 0 {
		budget = DefaultIndexMaxTokens
	}

	stale := StaleModules(doc, snap)
	annotated, c := proseCounts(doc, snap)
	truncLeaf := ""
	if snap.Truncated && snap.TruncAt != "" {
		if m, ok := snap.ModuleOf(snap.TruncAt); ok {
			truncLeaf = m
		}
	}

	idx := Index{
		Ready:        true,
		Budget:       budget,
		TotalModules: len(snap.Leaves),
		StaleModules: len(stale),
		UnannotatedModule: func() int {
			n := 0
			for i := range snap.Leaves {
				if !annotated[snap.Leaves[i].Dir] && !snap.Leaves[i].Submodule {
					n++
				}
			}
			return n
		}(),
		Atoms:           c.atoms,
		StaleAtoms:      c.stale,
		UnanchoredAtoms: c.unanchored,
		Unverifiable:    c.unverifiable,
	}

	lines := make([]string, 0, len(snap.Leaves))
	for i := range snap.Leaves {
		lines = append(lines, indexLine(&snap.Leaves[i], stale, annotated, truncLeaf))
	}

	// header 先占预算：它比最后几片更需要被看到（截断计数与行动理由都在里面）。
	//
	// 这里必须**迭代**而不是两趟：`+N modules not shown` 那一行本身占 token，
	// 而 N 取决于砍掉几行 —— 先用不含它的 header 选行、再把这行加回去，
	// 结果会超出预算（I3 的硬顶就这样被自己的截断行顶穿）。
	// omitted 单调不减，所以循环必然收敛。
	omitted := 0
	for {
		header := indexHeader(snap, idx, omitted)
		used := 0
		for _, h := range header {
			used += count(h)
		}
		kept := make([]string, 0, len(lines))
		for _, line := range lines {
			if used+count(line) > budget {
				break
			}
			kept = append(kept, line)
			used += count(line)
		}
		next := len(lines) - len(kept)
		if next == omitted {
			idx.Header = header
			idx.Lines = kept
			idx.Tokens = used
			idx.ShownModules = len(kept)
			idx.Omitted = omitted
			break
		}
		omitted = next
	}
	return idx
}

// indexLine 渲染一片模块：`dir Nf ->out +N <-in +M [flags]`。
func indexLine(l *Leaf, stale, annotated map[string]bool, truncLeaf string) string {
	var b strings.Builder
	b.WriteString(l.Dir)
	fmt.Fprintf(&b, " %df", l.FilesTotal)

	if shown := edgeNames(l.EdgesOut, IndexEdgeOutShow, true); shown != "" {
		fmt.Fprintf(&b, " ->%s", shown)
		if extra := l.EdgesOutTotal - shownCount(len(l.EdgesOut), IndexEdgeOutShow); extra > 0 {
			fmt.Fprintf(&b, " +%d", extra)
		}
	}
	if shown := edgeNames(l.EdgesIn, IndexEdgeInShow, false); shown != "" {
		fmt.Fprintf(&b, " <-%s", shown)
		if extra := l.EdgesInTotal - shownCount(len(l.EdgesIn), IndexEdgeInShow); extra > 0 {
			fmt.Fprintf(&b, " +%d", extra)
		}
	}

	flags := indexFlags(l, stale, annotated, truncLeaf)
	if len(flags) > 0 {
		b.WriteString(" [")
		b.WriteString(strings.Join(flags, ","))
		b.WriteString("]")
	}
	// §4.3：没有边抽取器的语言必须说出来，不留空 —— 空读起来像"确实没有依赖"，那是假事实。
	if len(l.MissingExtractors) > 0 {
		fmt.Fprintf(&b, " (no edge extractor: %s)", strings.Join(l.MissingExtractors, ","))
	}
	return b.String()
}

// indexFlags 把 I4 点名的四态说出来的唯一地方。漏掉任何一态，
// 骨架就是在对自己的能力上限撒谎（§4.2 结论 2 的同一条理由）。
func indexFlags(l *Leaf, stale, annotated map[string]bool, truncLeaf string) []string {
	var flags []string
	if stale[l.Dir] {
		flags = append(flags, IndexFlagStale)
	}
	if !annotated[l.Dir] && !l.Submodule {
		flags = append(flags, IndexFlagUnannotated)
	}
	if l.Dir == truncLeaf {
		flags = append(flags, IndexFlagTrunc)
	}
	if l.Oversize {
		flags = append(flags, IndexFlagOversize)
	}
	if l.Submodule {
		flags = append(flags, IndexFlagSubmodule)
	}
	return flags
}

// indexHeader 是账目行 + 行动理由行 + 截断行。
func indexHeader(snap *Snapshot, idx Index, omitted int) []string {
	head := fmt.Sprintf("blueprint: %d modules · %d atoms (%d stale, %d unanchored, %d unverifiable)",
		len(snap.Leaves), idx.Atoms, idx.StaleAtoms, idx.UnanchoredAtoms, idx.Unverifiable)
	if snap.Truncated {
		reason := snap.TruncReason
		if reason == "" {
			reason = "budget"
		}
		head += fmt.Sprintf(" · scan TRUNC at %s (%s)", snap.TruncAt, reason)
	}
	h := []string{head}

	if idx.StaleModules > 0 {
		// §5.2：stale 是粘滞的，没有后台进程会补写。索引行必须写成**行动理由**，
		// Director 读到的语义是"要不要派人复核"，而不是"蓝图坏了"。
		h = append(h, fmt.Sprintf("%d modules annotated but stale (delegate_repo to refresh)", idx.StaleModules))
	}
	if idx.UnannotatedModule > 0 {
		h = append(h, fmt.Sprintf("%d modules have no annotations yet", idx.UnannotatedModule))
	}
	if omitted > 0 {
		h = append(h, fmt.Sprintf("+%d modules not shown (index budget %d tokens)", omitted, idx.Budget))
	}
	return h
}

// proseCounts 统计落盘散文里**属于本次派生集合**的原子（孤儿段落不进账目，I13）。
// unverifiable 只按 where 的形态判定，不碰文件系统 —— 全档实盘重验是 CLI 的动作（§5.2）。
func proseCounts(doc *Doc, snap *Snapshot) (map[string]bool, counts4) {
	annotated := map[string]bool{}
	var c counts4
	if doc == nil {
		return annotated, c
	}
	derived := snap.DerivedModuleSet()
	blocks, _ := doc.Sections()
	for _, b := range blocks {
		if b.Module != "" && !derived[b.Module] {
			continue
		}
		for _, a := range b.Atoms {
			c.atoms++
			switch a.State {
			case StateStale:
				c.stale++
			case StateUnanchored:
				c.unanchored++
			}
			if a.Where != "" && !looksLikePath(strings.SplitN(a.Where, ":", 2)[0]) {
				c.unverifiable++
			}
			if b.Module != "" {
				annotated[b.Module] = true
			}
		}
	}
	return annotated, c
}

type counts4 struct{ atoms, stale, unanchored, unverifiable int }
