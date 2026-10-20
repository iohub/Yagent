package blueprint

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// 本文件是 §7.4 的合并与取代规则：**确定性纯函数，零 LLM、零文件系统**。
//
// 三条规则共同决定"要么生成要么更新"的确切语义，也决定 I20 能不能成立：
//   - 短 ID 由**内容**派生（段落 + field + claim）：同一批原子连 fold 两次字节不变，
//     乱序输入产出同一份正文 —— 多进程 CAS 敢重放就靠这两条。
//   - 段内按 (where, claim) 字典序：顺序若不可复现，每次 fold 会产出一整片 diff，
//     人类 review 与 provider 前缀缓存同时失效（§6.2 的账单依赖这条）。
//   - 同 module+field 的新值**非空才覆盖**：空字段不许抹掉上一次的内容。

// mergeState 是 fold 期间的工作副本（原 doc 不动）。
type mergeState struct {
	snap     *Snapshot
	sections map[Section][]Atom
	// owners 记 id → 生成它的内容 key。短 ID 撞上不同内容时必须响（§7.4 第 1 条最后一项）。
	owners map[string]string
	// claims 记归一化正文的存在性（dedupe 判据）。
	claims map[string]bool
	order  []Section
	// fs 是 I14 的存在性/归属判据来源（可为 nil = 退回纯派生判据）。
	fs *AnchorChecker
}

func newMergeState(blocks []SectionBlock, snap *Snapshot, fs *AnchorChecker) *mergeState {
	st := &mergeState{
		snap:     snap,
		sections: map[Section][]Atom{},
		owners:   map[string]string{},
		claims:   map[string]bool{},
		order:    []Section{SecArchitecture, SecFlows, SecConventions, SecDependencies, SecGotchas},
		fs:       fs,
	}
	for _, b := range blocks {
		if b.Heading == "" {
			continue
		}
		heading := Section(b.Heading)
		if _, seen := st.sections[heading]; !seen {
			st.order = append(st.order, heading)
		}
		for _, a := range b.Atoms {
			st.sections[heading] = append(st.sections[heading], a)
			st.track(heading, a)
		}
	}
	return st
}

func (st *mergeState) track(heading Section, a Atom) {
	if a.ID != "" {
		st.owners[a.ID] = contentKey(heading, a.Field, a.Text)
	}
	if n := normalizeClaim(a.Text); n != "" {
		st.claims[n] = true
	}
}

// apply 把一条已通过校验的原子落进工作副本，账目记全。
func (st *mergeState) apply(item mergeItem, src, seen string, res *FoldResult) {
	heading := item.section
	if item.module != "" {
		heading = ModuleHeading(item.module)
	}

	// supersedes 先处理。指认不存在的旧 ID：不报错也不假装成功（§7.4 第 3 条）——
	// 记一笔账然后照写新条目。模型指认错 ID 是常态，
	// 吞掉一条正确的观察比留一条噪声贵。
	for _, id := range item.supersedes {
		if st.removeByID(id) {
			res.Supersede++
			continue
		}
		res.DroppedSupersedes++
		res.Rejected = append(res.Rejected, Rejection{
			Reason: RejectIDConflict, Detail: "supersedes 指认了不存在的短 ID: " + id, Module: item.module,
		})
	}

	// dedupe（§7.4 第 2 条）：归一化正文相同 → 只更新既有条目的 seen/src/state，正文不动。
	if n := normalizeClaim(item.claim); n != "" && st.claims[n] {
		if heading, idx, ok := st.findByClaim(n); ok {
			a := st.sections[heading][idx]
			a.Seen, a.Src = seen, src
			a.State = pickState(item.state)
			st.sections[heading][idx] = a
			st.noteModule(res, item.module)
			res.Deduped++
			return
		}
	}

	newAtom := Atom{
		ID:    shortID(contentKey(heading, item.field, item.claim)),
		Text:  item.claim,
		Field: item.field,
		Where: item.where,
		Src:   src,
		Seen:  seen,
		State: pickState(item.state),
		Extra: map[string]string{},
	}
	if owner, taken := st.owners[newAtom.ID]; taken && owner != contentKey(heading, item.field, item.claim) {
		res.Rejected = append(res.Rejected, Rejection{
			Reason: RejectIDConflict,
			Detail: "短 ID " + newAtom.ID + " 已被另一条内容占用",
			Module: item.module,
		})
		return
	}

	// Modules 段：同一 module+field 只有一个槽位（§7.4 第 4 条，新值非空才覆盖）。
	if item.module != "" && item.field != "" {
		if idx := st.fieldSlot(heading, item.field); idx >= 0 {
			atoms := st.sections[heading]
			// 覆盖时**沿用槽位 ID**：读到这份文档的一方用同一个 ID 指认同一个槽位。
			delete(st.owners, atoms[idx].ID)
			if n := normalizeClaim(atoms[idx].Text); n != "" {
				delete(st.claims, n)
			}
			newAtom.ID = atoms[idx].ID
			st.sections[heading][idx] = newAtom
			st.track(heading, newAtom)
			st.noteModule(res, item.module)
			res.Updated++
			return
		}
	}

	if _, exists := st.sections[heading]; !exists {
		st.order = append(st.order, heading)
	}
	st.sections[heading] = append(st.sections[heading], newAtom)
	st.track(heading, newAtom)
	st.noteModule(res, item.module)
	res.Added++
}

func (st *mergeState) noteModule(res *FoldResult, module string) {
	if module == "" || containsModule(res.Modules, module) {
		return
	}
	res.Modules = append(res.Modules, module)
}

// findByClaim 找回正文归一化相同的既有条目。
func (st *mergeState) findByClaim(norm string) (Section, int, bool) {
	for _, heading := range st.order {
		for i, a := range st.sections[heading] {
			if normalizeClaim(a.Text) == norm {
				return heading, i, true
			}
		}
	}
	return "", 0, false
}

func (st *mergeState) fieldSlot(heading Section, field string) int {
	for i, a := range st.sections[heading] {
		if a.Field == field {
			return i
		}
	}
	return -1
}

func (st *mergeState) removeByID(id string) bool {
	if id == "" {
		return false
	}
	for _, heading := range st.order {
		atoms := st.sections[heading]
		for i, a := range atoms {
			if a.ID != id {
				continue
			}
			st.sections[heading] = append(atoms[:i:i], atoms[i+1:]...)
			delete(st.owners, id)
			if n := normalizeClaim(a.Text); n != "" {
				delete(st.claims, n)
			}
			return true
		}
	}
	return false
}

// render 产出正文：固定段顺序 + 片路径字典序，段内 (where, claim) 稳定排序，
// 超配额按"保 ok、先出最旧 seen"淘汰（§7.4 第 5、6 条）。
func (st *mergeState) render(res *FoldResult) string {
	// 段落顺序：五个仓库级段固定在前（它们是索引、schema、验旧三方共用的 key），
	// 然后所有 Modules/<path> 段按片路径排序。
	var moduleHeadings []Section
	for _, h := range st.order {
		if !strings.HasPrefix(string(h), SecModules) {
			continue
		}
		moduleHeadings = append(moduleHeadings, h)
	}
	sort.Slice(moduleHeadings, func(i, j int) bool { return moduleHeadings[i] < moduleHeadings[j] })

	ordered := make([]Section, 0, len(st.order))
	for _, name := range AssertedSections {
		if name == SecModules {
			continue
		}
		ordered = append(ordered, name)
	}
	ordered = append(ordered, moduleHeadings...)

	var b strings.Builder
	for _, heading := range ordered {
		atoms := st.sections[heading]
		kept := applySectionQuota(atoms, &res.Evicted)
		if len(kept) == 0 && !st.mustRender(heading) {
			continue
		}
		sortAtomsStable(kept)
		b.WriteString("## " + string(heading) + "\n")
		for _, a := range kept {
			b.WriteString(a.Line())
			b.WriteByte('\n')
		}
	}
	return strings.TrimSpace(b.String())
}

// mustRender 决定"空段落要不要留在文档里"。
// 五个仓库级段落留标题（§3.2 的样例就是这样，读到的人据此知道还有哪几格是空的），
// Modules 段没有原子就整段不留。
func (st *mergeState) mustRender(heading Section) bool {
	for _, name := range AssertedSections {
		if name == heading {
			return true
		}
	}
	return false
}

// applySectionQuota 实现 §7.4 第 6 条：超配额时优先保 ok，其次按 seen 最旧先出。
func applySectionQuota(atoms []Atom, evicted *int) []Atom {
	if len(atoms) <= MaxAtomsPerSection {
		return atoms
	}
	ranked := make([]int, len(atoms))
	for i := range atoms {
		ranked[i] = i
	}
	// 排序口径是"**最该留的排前面**"，尾部 len-配额 条被淘汰（§7.4 第 6 条）。
	sort.SliceStable(ranked, func(i, j int) bool {
		a, b := atoms[ranked[i]], atoms[ranked[j]]
		ra, rb := stateRank(a.State), stateRank(b.State)
		if ra != rb {
			return ra > rb // 优先保 ok（unanchored 比 stale 弱一档：连出处都没给）
		}
		if a.Seen != b.Seen {
			return a.Seen > b.Seen // 新的留下，旧的先出
		}
		return a.ID < b.ID
	})
	drop := map[int]bool{}
	for _, i := range ranked[MaxAtomsPerSection:] {
		drop[i] = true
	}
	kept := make([]Atom, 0, MaxAtomsPerSection)
	for i, a := range atoms {
		if !drop[i] {
			kept = append(kept, a)
		}
	}
	*evicted += len(atoms) - len(kept)
	return kept
}

func stateRank(s string) int {
	switch s {
	case StateStale:
		return 0
	case StateUnanchored:
		return 1
	default:
		return 2
	}
}

// sortAtomsStable 把段内顺序钉成 (where, claim) 字典序 —— 可复现的顺序
// 是"每次 fold 只动那一行"的前提。
func sortAtomsStable(atoms []Atom) {
	sort.SliceStable(atoms, func(i, j int) bool {
		if atoms[i].Where != atoms[j].Where {
			return atoms[i].Where < atoms[j].Where
		}
		if atoms[i].Text != atoms[j].Text {
			return atoms[i].Text < atoms[j].Text
		}
		return atoms[i].ID < atoms[j].ID
	})
}

// normalizeClaim 折空白、转小写、去尾句号：dedupe 的判据必须比"逐字节相等"宽，
// 否则模型每次换一个标点就重播一条同样的论断。
func normalizeClaim(s string) string {
	n := strings.ToLower(strings.Join(strings.Fields(s), " "))
	return strings.TrimRight(n, ".")
}

func contentKey(heading Section, field, claim string) string {
	return strings.Join([]string{string(heading), field, normalizeClaim(claim)}, "\x00")
}

func shortID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:4]
}

func pickState(s string) string {
	if s == "" {
		return StateOK
	}
	return s
}

func containsModule(in []string, want string) bool {
	for _, s := range in {
		if s == want {
			return true
		}
	}
	return false
}

// mergeSources 把 {agent: 原子数} 的账累加上去（front-matter 由程序写，I5）。
func mergeSources(old map[string]int, src string, n int) map[string]int {
	out := map[string]int{}
	for k, v := range old {
		out[k] = v
	}
	if src != "" && n > 0 {
		agent := src
		if at := strings.Index(src, "@"); at > 0 {
			agent = src[:at]
		}
		out[agent] += n
	}
	return out
}

// foreignPath 实现 I14：散文里的路径样 token 必须属于该片的派生事实集
// （files ∪ 邻居模块路径）。os.Stat 只证明"这条路径存在"，而模型完全可以拿真路径
// 支撑假断言；成员判定证明"它属于这个模块"，那才是真会被搞错的东西。
//
// fs 为 nil 时（纯派生判据的单元测试、不接文件系统的调用方）退回"ModuleOf + 前缀关系"
// 的老判据 —— 宁可保守，也不在缺判据时放行。
//
// fs 在场时多两道**只收紧假阳性、不放松真阳性**的闸门：
//   - 归属先问"它在仓库里到底指哪几片"（含写短的路径与裸文件名，见 prosepath.go）；
//     同名散在多片、哪片都对得上时**不判**，判不了不等于违规（I19 同一条纪律）；
//   - 仓库里查无此物 ⇒ 那不是路径断言，是散文里的斜杠
//     （"Name/Brief/Effect/Frequency" 这种标识符列表在中文技术写作里天天出现；
//     2026-10-19 一次真实提交三条全被它打死，fold 因此 NoOp、蓝图文件从未出现）。
//     "凭空写一个路径"由 `where` 锚点复核兜住，不靠这道。
func foreignPath(claim, module string, snap *Snapshot, fs *AnchorChecker) (string, bool) {
	if snap == nil {
		return "", false
	}
	neighbors := neighborSet(snap, module)
	for _, tok := range pathTokens(claim) {
		if tok == "" {
			continue
		}
		if belongsToPatch(tok, module, neighbors) {
			continue
		}
		if fs == nil {
			if m, ok := snap.ModuleOf(tok); ok {
				if m == module || neighbors[m] {
					continue
				}
				return tok, true
			}
			return tok, true
		}
		owners := fs.ownersOf(snap, tok)
		if len(owners) > 0 {
			if len(owners) > 1 {
				continue // 多处同名：判不了，不谎称验过
			}
			if owners[0] == module || neighbors[owners[0]] {
				continue
			}
			return tok, true
		}
		if !fs.existsUnderRoot(tok) {
			continue // 不是路径断言
		}
		return tok, true
	}
	return "", false
}

// belongsToPatch 收"token 与这片有直接字面关系"的三种写法：本片本身、本片的目录、
// 本片的祖先目录。它必须在归属判定之前跑 —— "internal" 这类祖先会被 ModuleOf 上溯到根片，
// 而根片并不是这条论断的归属。
func belongsToPatch(tok, module string, neighbors map[string]bool) bool {
	if tok == module || strings.HasPrefix(tok, module+"/") || strings.HasPrefix(module, tok+"/") {
		return true
	}
	for n := range neighbors {
		if n == tok || strings.HasPrefix(n, tok+"/") || strings.HasPrefix(tok, n+"/") {
			return true
		}
	}
	return false
}

// neighborSet 是一片模块的直接出/入边对端（§4.3 的派生事实里"邻居"这一项）。
func neighborSet(snap *Snapshot, module string) map[string]bool {
	out := map[string]bool{}
	leaf, ok := snap.moduleByDir(module)
	if !ok {
		return out
	}
	for _, e := range leaf.EdgesOut {
		out[e.To] = true
	}
	for _, e := range leaf.EdgesIn {
		out[e.From] = true
	}
	return out
}

// pathTokens 从一句论断里挑出"路径样 token"。分隔按空白与常见包裹标点，
// 并剥掉 `:line` / `:symbol` 后缀 —— 判归属用的是文件本身。
func pathTokens(claim string) []string {
	fields := strings.FieldsFunc(claim, func(r rune) bool {
		switch r {
		case ' ', '\t', '(', ')', '[', ']', ',', ';', ':', '"', '\'', '`', '<', '>', '{', '}':
			return true
		}
		return false
	})
	var out []string
	for _, f := range fields {
		f = strings.Trim(f, "/.")
		if f == "" || !looksLikePath(f) {
			continue
		}
		if head, _, hasColon := strings.Cut(f, ":"); hasColon && looksLikePath(head) {
			f = head
		}
		out = append(out, f)
	}
	return out
}
