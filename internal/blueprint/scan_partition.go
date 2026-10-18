package blueprint

import (
	"sort"
	"strings"
)

// 本文件实现 §4.2 的规模阈值与链折叠（伪代码原样沿用，含两条确定性裁决，见 partitionNode）。

// dirNode 是 partition 的中间形态：目录树的一个节点。
type dirNode struct {
	rel      string
	parent   *dirNode
	children map[string]*dirNode
	records  []*FileRecord
	own      int // 自身非测试非生成代码数（§4.2 的 own）
	subTotal int // own + Σ 子树（伪代码里的"child 递归前计数"）
	// subHasCode 是"子树含 ≥1 代码类文件"（含测试/生成物）：§4.2 children 行写的是
	// "含 ≥1 代码文件"，"不含测试/生成物"只限定 own 那一行 —— tests/-only 目录若按
	// subTotal（非测试口径）过滤会被整目录静默丢掉，它的测试文件就从快照里消失了。
	subHasCode bool
}

// partitionResult 是折叠的产物：片集合 + 冻结的覆盖索引。
type partitionResult struct {
	Leaves  []Leaf
	leafIdx map[string]int // 片路径 → Leaves 下标（settleDocOnly 归并文档用）
	// cover 把"覆盖目录 → 片路径"冻结下来：ModuleOf 的索引、边聚合提模块级用。
	cover map[string]string
	// allFiles 是每片的全量代码文件（含测试、不含生成物，排序）—— 指纹的输入；
	// 与 Leaf.Files（显示截断后）不同，截断不能漏进指纹。
	allFiles map[string][]string
	// dirRecords 是 collect 的记录按目录分组后的缓存（emitLeaf 装配用）。
	dirRecords map[string][]*FileRecord

	// OrphanDocs 是没有任何片覆盖的文档数（§5.3 孤儿判据的派生侧）。
	OrphanDocs       int
	OrphanDocsByKind map[string]int
}

// partition 严格按 §4.2 折叠，产出片集合与覆盖索引。
func partition(cr *collectResult, o Options) *partitionResult {
	dirRecords := make(map[string][]*FileRecord)
	for i := range cr.Records {
		rec := &cr.Records[i]
		dirRecords[rec.Dir] = append(dirRecords[rec.Dir], rec)
	}
	pr := &partitionResult{
		leafIdx:          map[string]int{},
		cover:            map[string]string{},
		allFiles:         map[string][]string{},
		dirRecords:       dirRecords,
		OrphanDocsByKind: map[string]int{},
	}
	root := buildTree(dirRecords)
	partitionNode(root, 0, nil, o, pr)
	// .gitmodules 在目录分支命中的目录：整片一张 leaf、不递归（§4.1 实测 bug 的正确位置）。
	for _, rel := range cr.SubmoduleHits {
		pr.emitSubmoduleLeaf(rel)
	}
	pr.settleDocOnly(cr)
	sort.Slice(pr.Leaves, func(i, j int) bool { return pr.Leaves[i].Dir < pr.Leaves[j].Dir })
	return pr
}

// buildTree 从"目录 → 记录"建目录树。
// 只有含记录的目录才建节点 —— 空目录不影响阈值与折叠，其文档归属由 settleDocOnly 处理。
func buildTree(dirRecords map[string][]*FileRecord) *dirNode {
	root := &dirNode{rel: ".", children: map[string]*dirNode{}}
	nodes := map[string]*dirNode{".": root}
	for dir := range dirRecords {
		cur := root
		for _, part := range strings.Split(dir, "/") {
			if part == "." || part == "" {
				continue
			}
			child := cur.children[part]
			if child == nil {
				childRel := part
				if cur.rel != "." {
					childRel = cur.rel + "/" + part
				}
				child = &dirNode{rel: childRel, parent: cur, children: map[string]*dirNode{}}
				cur.children[part] = child
				nodes[childRel] = child
			}
			cur = child
		}
	}
	for dir, recs := range dirRecords {
		nodes[dir].records = recs
	}
	// 后序算 own 与 subTotal / subHasCode。
	var calc func(n *dirNode) int
	calc = func(n *dirNode) int {
		hasCode := false
		for _, rec := range n.records {
			if rec.IsCode {
				hasCode = true
				if !rec.IsTest && !rec.IsGenerated {
					n.own++
				}
			}
		}
		total := n.own
		subHasCode := hasCode
		for _, c := range n.childrenSorted() {
			total += calc(c)
			subHasCode = subHasCode || c.subHasCode
		}
		n.subTotal = total
		n.subHasCode = subHasCode
		return total
	}
	calc(root)
	return root
}

// partitionNode 严格按 §4.2 伪代码递归。三条裁决（每条都会被变异测试盯住）：
//  1. children 用"子树含 ≥1 代码类文件"（subHasCode，含测试/生成物）—— §4.2 children 行
//     的字面口径是"含 ≥1 代码文件"，"不含测试/生成物"只限定 own 行。若按"自身含代码"
//     判定，own==0 的分叉父目录会丢整条链的叶子；若按 subTotal（非测试口径）判定，
//     tests/-only 目录会被整目录静默丢掉（fixture golden 里 tests/ 必须是一片）。
//  2. 阈值/深度 emit 分支携带停止边（return）—— 伪代码若缺这条，同一片会 emit 两次
//     （先整片、再部分片），片数翻倍。
//  3. total 只计非测试非生成代码（§4.2 total 行与 own 同口径）—— 测试不计入阈值（§4.1），
//     所以 tests/-only 的目录 total==0 ≤ 阈值，照常 emit（不是阈值逃逸，是设计意图）。
func partitionNode(node *dirNode, depth int, foldCover []string, o Options, pr *partitionResult) {
	var children []*dirNode
	for _, c := range node.childrenSorted() {
		if c.subHasCode {
			children = append(children, c)
		}
	}
	total := node.subTotal // own + Σ(child 递归前计数)
	if depth >= o.MaxDepth || total <= o.MaxFilesPerModule {
		// 阈值/深度内：整片 emit（覆盖全部子树）后停止 —— 不再下钻。
		pr.emitLeaf(node, subtreeDirs(node), foldCover, o)
		return
	}
	if node.own == 0 && len(children) == 1 {
		// 链折叠，不 emit（§4.2）：折叠的目录并入 foldCover，其文件归到折到的片上。
		next := make([]string, len(foldCover), len(foldCover)+1)
		copy(next, foldCover)
		partitionNode(children[0], depth+1, append(next, node.rel), o, pr)
		return
	}
	if node.own > 0 {
		// 部分覆盖：只覆盖自身，子目录各自成片。
		pr.emitLeaf(node, []string{node.rel}, foldCover, o)
	}
	for _, c := range children {
		partitionNode(c, depth+1, foldCover, o, pr)
	}
}

// emitLeaf 是 §4.2 emit 的唯一装配点。
// coverDirs 是这片声明覆盖的目录（整片=全部子树；部分片=自身）；
// foldCover 是链折叠穿越的祖先目录 —— 它们的文件（通常只有文档）归到折到的片上。
func (pr *partitionResult) emitLeaf(node *dirNode, coverDirs, foldCover []string, o Options) {
	leaf := Leaf{Dir: node.rel, LangHistogram: map[string]int{}, DocsByKind: map[string]int{}}
	var files []string
	for _, dir := range dedupDirs(coverDirs, foldCover) {
		for _, rec := range pr.dirRecords[dir] {
			switch {
			case rec.IsCode && rec.IsGenerated:
				leaf.GeneratedCount++ // 生成物：计此数、不进列表（§4.1）
			case rec.IsCode:
				files = append(files, rec.RelPath)
				leaf.LangHistogram[rec.Lang]++
				if rec.IsTest {
					leaf.TestsCount++
				}
			default:
				leaf.DocsByKind[rec.Lang]++
			}
		}
	}
	sort.Strings(files)
	leaf.FilesTotal = len(files)
	if len(files) > o.FileCap {
		// §4.2 结论 2：骨架的能力上限必须说出来 —— oversize 标记 + omitted 计数。
		leaf.Oversize = true
		leaf.OmittedFiles = len(files) - o.FileCap
		files = files[:o.FileCap]
	}
	leaf.Files = files
	for _, dir := range dedupDirs(coverDirs, foldCover) {
		pr.cover[dir] = leaf.Dir
	}
	pr.register(leaf, files)
}

// emitSubmoduleLeaf 是 .gitmodules 命中目录的整片 emit：不递归、无文件（§4.1）。
func (pr *partitionResult) emitSubmoduleLeaf(rel string) {
	pr.cover[rel] = rel
	pr.register(Leaf{Dir: rel, Submodule: true, LangHistogram: map[string]int{}, DocsByKind: map[string]int{}}, nil)
}

func (pr *partitionResult) register(leaf Leaf, allFiles []string) {
	sort.Strings(allFiles)
	pr.Leaves = append(pr.Leaves, leaf)
	pr.leafIdx[leaf.Dir] = len(pr.Leaves) - 1
	pr.allFiles[leaf.Dir] = allFiles
}

// settleDocOnly 把不在任何片覆盖内的文档归入最近覆盖祖先片；全无覆盖 → OrphanDocs。
// （§5.3 的派生侧：可见集合 == 本次派生的模块集合 —— 没有片认领的文档不冒充模块事实。）
//
// settled 是本函数的暂存归并（"未覆盖目录 → 最近祖先"），循环结束后才写进 cover：
// 若归并中途就写 cover，同目录第二条记录会被下面的"已覆盖"短路跳过 ——
// docs/ 有 3 篇文档时只有第一篇被计数，其余静默丢失（闸门：fixture golden 的
// 根片 docs 计数必须含 docs/ 全部三篇）。
func (pr *partitionResult) settleDocOnly(cr *collectResult) {
	settled := map[string]string{}
	for i := range cr.Records {
		rec := &cr.Records[i]
		if rec.IsCode {
			continue
		}
		if _, ok := pr.cover[rec.Dir]; ok {
			continue // 已被整片 emit 覆盖（emit 时已计过）
		}
		dir := rec.Dir
		owner := ""
		found := false
		for {
			if owner, found = pr.cover[dir]; found {
				break
			}
			if o2, ok := settled[dir]; ok {
				owner, found = o2, true
				break
			}
			if dir == "." {
				break
			}
			dir = parentDir(dir)
		}
		if !found {
			pr.OrphanDocs++
			pr.OrphanDocsByKind[rec.Lang]++
			continue
		}
		pr.Leaves[pr.leafIdx[owner]].DocsByKind[rec.Lang]++
		settled[rec.Dir] = owner
	}
	// 冻结进 cover（ModuleOf 的索引）：归并完再写，不干扰循环内的"已 emit 覆盖"判定。
	for d, owner := range settled {
		pr.cover[d] = owner
	}
}

// subtreeDirs 收集 node 子树的全部目录（含自身）—— 整片 emit 的覆盖集合。
func subtreeDirs(node *dirNode) []string {
	var out []string
	var walk func(n *dirNode)
	walk = func(n *dirNode) {
		out = append(out, n.rel)
		for _, c := range n.childrenSorted() {
			walk(c)
		}
	}
	walk(node)
	return out
}

// dedupDirs 合并 coverDirs 与 foldCover 并去重（保序不重复）。
func dedupDirs(coverDirs, foldCover []string) []string {
	seen := make(map[string]bool, len(coverDirs)+len(foldCover))
	var out []string
	for _, d := range coverDirs {
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	for _, d := range foldCover {
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

func (n *dirNode) childrenSorted() []*dirNode {
	out := make([]*dirNode, 0, len(n.children))
	for _, c := range n.children {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out
}
