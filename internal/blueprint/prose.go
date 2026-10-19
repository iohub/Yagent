package blueprint

import (
	"fmt"
	"sort"
	"strings"
)

// 本文件是"按需路径"的渲染器：read_blueprint 与委派前缀都走这里。
//
// 常驻注入只用 index.go 的形状②（骨架），散文的入口只有这里 ——
// 这是 §6.1 定案的另一半：purpose 是唯一不可验证的散文，所以永不进常驻。
//
// 三条判据在这里落地：
//   - 可见集合 == 派生集合（I13）：孤儿段落既渲染不出来，也读不到。
//     目录改名后旧注解永远不会再被扫到、也永远不会自己删；更坏的是目录名被复用时
//     模型会读到关于**另一个旧模块**的散文。
//   - 结构指纹不符 → 该片子节全部标 stale 并附一行原因，**正文照旧保留**（I10）。
//   - 找不到模块时绝不返回空（§7.1）：空会被读成"这里没东西"，然后模型开始不信蓝图。

// ProseSpec 是一次按需渲染的输入。
type ProseSpec struct {
	// Section 是段落名（SecArchitecture… 或 SecModules）；空 = 全部有内容的段落。
	Section Section
	// Module 只在 Section==SecModules 时有意义：具体某一片（派生片路径，"." 是根片）。
	Module string
	// Field 进一步收窄到三个模块字段之一。
	Field string
	// State 收窄到 ok/stale/unanchored 之一。
	State string

	// Offset/Limit 按**行**分页（与 read_note 同形）。
	Offset int
	Limit  int

	BudgetTokens int
	CountTokens  func(string) int
}

// Prose 是渲染结果。调用方按字段分派回执，不要再自己拼一遍。
type Prose struct {
	Ready     bool
	Header    []string
	Lines     []string
	Total     int  // 分页前的总行数
	Omitted   int  // 因预算被砍掉的行数
	Truncated bool // Offset/Limit 或预算动过
	// Budget/Tokens 把"预算是多少、实际用了多少"留在账上：
	// read_blueprint 的回执头部与 §16 的验收都要能回答"这次渲染花了多少 token"。
	Budget int
	Tokens int
	// UnknownModule 时 Candidates 给最接近的 5 个片路径（§7.1：不许返回空）。
	UnknownModule bool
	Candidates    []string
	// BadLines 是坏掉的原子行（文档被手改过 / 折叠写坏）。
	// 调用方必须把它们记进账目而不是静默跳过 —— "这一轮为什么没更新蓝图"要能被回答。
	BadLines []error
}

// RenderProse 渲染"派生骨架 + 落盘散文"的按需视图。
// snap 为 nil 时 Ready=false（调用方渲染 ScanningLine，不阻塞）。
func RenderProse(snap *Snapshot, doc *Doc, spec ProseSpec) Prose {
	if snap == nil {
		return Prose{Ready: false}
	}
	count := spec.CountTokens
	if count == nil {
		count = fallbackCount
	}
	budget := spec.BudgetTokens
	if budget <= 0 {
		budget = DefaultProseBudgetTokens
	}

	p := Prose{Ready: true, Budget: budget, Header: coverageLines(snap, doc)}

	blocks, bad := splitBlocks(doc)
	p.BadLines = bad
	stale := StaleModules(doc, snap)
	derived := snap.DerivedModuleSet()

	// 段落选择：先按 spec，再按可见集合筛 Modules 前缀段。
	var picked []SectionBlock
	for _, b := range blocks {
		if b.Module != "" && !derived[b.Module] {
			continue // I13：孤儿段落既看不见也读不到
		}
		if spec.Section != "" && !sectionMatches(b.Heading, spec.Section) {
			continue
		}
		if spec.Module != "" && b.Module != spec.Module {
			continue
		}
		picked = append(picked, b)
	}

	// 只指定了 Module 而一片没匹配上 → 未知模块回执（§7.1）。
	if spec.Module != "" && len(picked) == 0 {
		p.UnknownModule = true
		p.Candidates = nearestModules(snap, spec.Module)
		p.Lines = []string{fmt.Sprintf(
			"unknown module %q: no annotations yet. nearest: %s — for symbols inside a module use semantic_search",
			spec.Module, joinOrNone(p.Candidates))}
		return p
	}

	for _, b := range picked {
		flagStale := stale[b.Module]
		p.Lines = append(p.Lines, "## "+b.Heading)
		for _, a := range b.Atoms {
			if spec.Field != "" && a.Field != spec.Field {
				continue
			}
			state := a.State
			if state == "" {
				state = StateOK
			}
			if flagStale {
				state = StateStale
			}
			if spec.State != "" && state != spec.State {
				continue
			}
			p.Lines = append(p.Lines, atomDisplayLine(a, state, flagStale))
		}
	}
	p.Total = len(p.Lines)

	// 分页在预算之前：Offset/Limit 是调用方要的窗口，预算保护的是"没人限流时别炸"。
	if spec.Offset > 0 || spec.Limit > 0 {
		start := spec.Offset
		if start < 0 {
			start = 0
		}
		if start > len(p.Lines) {
			start = len(p.Lines)
		}
		end := len(p.Lines)
		if spec.Limit > 0 && start+spec.Limit < end {
			end = start + spec.Limit
		}
		if end != len(p.Lines) || start != 0 {
			p.Truncated = true
		}
		p.Lines = p.Lines[start:end]
	}

	used := 0
	for _, h := range p.Header {
		used += count(h)
	}
	kept := p.Lines[:0]
	for _, line := range p.Lines {
		if used+count(line) > budget {
			break
		}
		kept = append(kept, line)
		used += count(line)
	}
	p.Tokens = used
	if n := len(p.Lines) - len(kept); n > 0 {
		p.Omitted = n
		p.Truncated = true
	}
	p.Lines = kept
	return p
}

// atomDisplayLine 是按需视图里的一行：正文 + 元数据，stale 时附行动理由。
func atomDisplayLine(a Atom, state string, predatesChange bool) string {
	var b strings.Builder
	b.WriteString("- (")
	b.WriteString(a.ID)
	b.WriteString(") ")
	if a.Field != "" {
		b.WriteString(a.Field)
		b.WriteString(": ")
	}
	b.WriteString(a.Text)
	if a.Where != "" {
		b.WriteString(" [where:")
		b.WriteString(a.Where)
		b.WriteString("]")
	}
	if state != StateOK {
		b.WriteString(" [")
		b.WriteString(state)
		b.WriteString("]")
	}
	if predatesChange {
		b.WriteString(" (annotation predates a structural change)")
	}
	return b.String()
}

// splitBlocks 把文档切成段落并回报坏行；doc 为 nil 时返回空集合。
func splitBlocks(doc *Doc) ([]SectionBlock, []error) {
	if doc == nil {
		return nil, nil
	}
	return doc.Sections()
}

func sectionMatches(heading string, want Section) bool {
	if want == SecModules {
		return IsModuleHeading(heading)
	}
	return heading == want
}

// coverageLines 是 derived 段（§3.3：程序每次渲染现算、不落盘）的账目。
func coverageLines(snap *Snapshot, doc *Doc) []string {
	_, c := proseCounts(doc, snap)
	derived := snap.DerivedModuleSet()
	annotated := 0
	if doc != nil {
		blocks, _ := doc.Sections()
		seen := map[string]bool{}
		for _, b := range blocks {
			if b.Module != "" && derived[b.Module] && !seen[b.Module] {
				seen[b.Module] = true
				if len(b.Atoms) > 0 {
					annotated++
				}
			}
		}
	}
	out := []string{fmt.Sprintf(
		"blueprint coverage: %d/%d modules annotated · %d atoms (%d stale, %d unanchored, %d unverifiable)",
		annotated, len(snap.Leaves), c.atoms, c.stale, c.unanchored, c.unverifiable)}
	if doc != nil && doc.Front.Evicted > 0 {
		// 段配额淘汰过的条数：正文里已经没有被淘汰者的身影，不留这一行就是静默少一片（I4）。
		out = append(out, fmt.Sprintf("section quota evicted %d atoms historically (max %d per section)", doc.Front.Evicted, MaxAtomsPerSection))
	}
	if snap.Truncated {
		out = append(out, fmt.Sprintf("scan incomplete: stopped at %s (%s)", snap.TruncAt, snap.TruncReason))
	}
	return out
}

// nearestModules 给最接近的 5 个片路径。判据是"共享路径段最多"，其次是
// 子串包含；两者都没有时退回字典序前几个 —— 空回执永远比"一个都不给"更糟。
func nearestModules(snap *Snapshot, want string) []string {
	want = strings.Trim(want, "/")
	type scored struct {
		dir   string
		score int
	}
	var list []scored
	for _, m := range sortedModulePaths(snap) {
		s := 0
		ws, ms := strings.Split(want, "/"), strings.Split(m, "/")
		for i := 0; i < len(ws) && i < len(ms); i++ {
			if ws[i] == ms[i] {
				s += 2
			}
		}
		if strings.Contains(m, want) || strings.Contains(want, m) {
			s += 3
		}
		list = append(list, scored{m, s})
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].score != list[j].score {
			return list[i].score > list[j].score
		}
		return list[i].dir < list[j].dir
	})
	if len(list) > 5 {
		list = list[:5]
	}
	out := make([]string, 0, len(list))
	for _, e := range list {
		out = append(out, e.dir)
	}
	return out
}

// sortedModulePaths 是派生集合的稳定序（一份实现，见 Snapshot.DerivedModuleList）。
func sortedModulePaths(snap *Snapshot) []string { return snap.DerivedModuleList() }

func joinOrNone(in []string) string {
	if len(in) == 0 {
		return "(none)"
	}
	return strings.Join(in, ", ")
}
