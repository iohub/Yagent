package blueprint

import (
	"os"
	"path/filepath"
	"sort"
)

// Scan 是 R2 扫描器的唯一入口（§4）：项目根护栏 → 单遍收集 → 折叠 → 边 → 指纹 → 快照。
// 零 LLM、零写盘、零执行；每次调用返回全新 Snapshot（I15 不可变语义），
// 两次调用同一仓库结果 DeepEqual（确定性）。opts 零值经 fillDefaults 填默认。
func Scan(projectPath string, opts Options) (*Snapshot, error) {
	// 根护栏（I16）：$HOME、/、无 manifest 且非 git 的目录拒绝建蓝图。
	if err := CheckProjectRoot(projectPath); err != nil {
		return nil, err
	}
	root, err := filepath.Abs(filepath.Clean(projectPath))
	if err != nil {
		return nil, err
	}
	o := fillDefaults(opts)

	cr, err := collect(root, o)
	if err != nil {
		return nil, err
	}
	pr := partition(cr, o)

	dirs := make(map[string]bool, len(cr.Dirs))
	for _, d := range cr.Dirs {
		dirs[d] = true
	}
	// 边抽取（§4.3）：零成本携带 package main 入口。
	er := extractEdges(root, cr.ModulePath, cr.Records, dirs, pr.cover)

	snap := &Snapshot{
		Root:             root,
		ModulePath:       cr.ModulePath,
		Leaves:           pr.Leaves,
		TotalEntries:     cr.TotalEntries,
		Truncated:        cr.Truncated,
		TruncReason:      cr.TruncReason,
		TruncAt:          cr.TruncAt,
		RejectedSymlinks: cr.RejectedSymlinks,
		OrphanDocs:       pr.OrphanDocs,
		OrphanDocsByKind: pr.OrphanDocsByKind,
		dirIndex:         pr.cover,
	}
	assembleEntries(snap, cr, er, pr, o)
	assembleCommands(snap, root, o)
	assembleEdgesAndFingerprints(snap, er, pr, o)
	return snap, nil
}

// assembleEntries 装配每片的入口事实（§4.3）：Go 可执行入口来自边抽取（ast），
// package.json / Cargo.toml 在这里读 —— 只列名字绝不执行，解析失败静默跳过（不猜）。
func assembleEntries(snap *Snapshot, cr *collectResult, er *edgeResult, pr *partitionResult, o Options) {
	leafEntries := map[string][]Entry{}
	// 1. Go 可执行入口（ast，同一次解析零额外成本）。
	seenMain := map[string]bool{}
	for _, leafDir := range er.MainLeaves {
		if !seenMain[leafDir] {
			seenMain[leafDir] = true
			leafEntries[leafDir] = append(leafEntries[leafDir],
				Entry{Kind: "go-main", Name: "main", Source: SourceAST})
		}
	}
	// 2. Node/Rust 入口与脚本（manifest；§4.3 表格第二行）。
	for i := range cr.Records {
		rec := &cr.Records[i]
		if rec.IsCode {
			continue
		}
		leafDir := pr.cover[rec.Dir]
		if leafDir == "" {
			continue // 无片认领的目录不冒充模块事实（§5.3 同源）
		}
		switch rec.Base {
		case "package.json":
			data, err := os.ReadFile(filepath.Join(snap.Root, rec.RelPath))
			if err != nil {
				continue // 读不到不猜
			}
			main, bins, scripts := parsePackageJSON(data)
			if main != "" {
				leafEntries[leafDir] = append(leafEntries[leafDir],
					Entry{Kind: "node-main", Name: main, Source: SourceManifest})
			}
			for _, b := range bins {
				leafEntries[leafDir] = append(leafEntries[leafDir],
					Entry{Kind: "node-bin", Name: b, Source: SourceManifest})
			}
			for _, s := range scripts {
				leafEntries[leafDir] = append(leafEntries[leafDir],
					Entry{Kind: "node-script", Name: s, Source: SourceManifest})
			}
		case "Cargo.toml":
			data, err := os.ReadFile(filepath.Join(snap.Root, rec.RelPath))
			if err != nil {
				continue
			}
			for _, b := range parseCargoBins(data) {
				leafEntries[leafDir] = append(leafEntries[leafDir],
					Entry{Kind: "cargo-bin", Name: b, Source: SourceManifest})
			}
		}
	}
	// 3. 排序（确定性）+ 截断（EntryCap；Total 把 +N 说出来）。
	for i := range snap.Leaves {
		leaf := &snap.Leaves[i]
		entries := leafEntries[leaf.Dir]
		sort.Slice(entries, func(a, b int) bool {
			if entries[a].Kind != entries[b].Kind {
				return entries[a].Kind < entries[b].Kind
			}
			return entries[a].Name < entries[b].Name
		})
		leaf.EntriesTotal = len(entries)
		if len(entries) > o.EntryCap {
			entries = entries[:o.EntryCap]
		}
		leaf.Entries = entries
	}
}

// assembleCommands 装配根 Makefile/build.sh 事实（§4.3 最后一行：只列名字，绝不执行）。
func assembleCommands(snap *Snapshot, root string, o Options) {
	cmd := CommandFacts{Source: SourceFilename}
	if data, err := os.ReadFile(filepath.Join(root, "Makefile")); err == nil {
		targets, truncated := parseMakefileTargets(data, o.TargetCap)
		cmd.MakeTargets = targets
		cmd.MakeTargetsTruncated = truncated
	}
	if _, err := os.Lstat(filepath.Join(root, "build.sh")); err == nil {
		cmd.HasBuildSh = true
	}
	snap.Commands = cmd
}

// assembleEdgesAndFingerprints 装配每片的模块级双向边与 §5.2(a) 结构指纹。
// 指纹用全量（未截断）输入；显示列表按 EdgeCap 截断（I3/I4：Total 字段把 +N 说出来）。
func assembleEdgesAndFingerprints(snap *Snapshot, er *edgeResult, pr *partitionResult, o Options) {
	// 反向边：入边 = 出边映射的转置。
	edgesIn := map[string]map[string]int{}
	for src, ts := range er.EdgesOut {
		for tgt, n := range ts {
			if edgesIn[tgt] == nil {
				edgesIn[tgt] = map[string]int{}
			}
			edgesIn[tgt][src] += n
		}
	}
	for i := range snap.Leaves {
		leaf := &snap.Leaves[i]
		out := er.EdgesOut[leaf.Dir]
		in := edgesIn[leaf.Dir]
		leaf.EdgesOut, leaf.EdgesOutTotal = edgeList(out, leaf.Dir, o.EdgeCap, true)
		leaf.EdgesIn, leaf.EdgesInTotal = edgeList(in, leaf.Dir, o.EdgeCap, false)
		leaf.MissingExtractors = missingExtractors(leaf.LangHistogram)
		// §5.2(a)：sha256(排序后的文件列表 + 语言直方图 + 出边 + 入边)；全量输入。
		leaf.Fingerprint = fingerprint(pr.allFiles[leaf.Dir], leaf.LangHistogram, out, in)
	}
}

// edgeList 把 map 变成排序的 Edge 列表（outbound=true 时 self 是 From，否则是 To）。
func edgeList(m map[string]int, self string, capN int, outbound bool) (display []Edge, total int) {
	if len(m) == 0 {
		return nil, 0
	}
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	total = len(ks)
	if total > capN {
		ks = ks[:capN]
	}
	for _, k := range ks {
		e := Edge{Imports: m[k], Source: SourceAST}
		if outbound {
			e.From, e.To = self, k
		} else {
			e.From, e.To = k, self
		}
		display = append(display, e)
	}
	return display, total
}

// knownExtractors 是 v1 有边抽取器的语言（§4.3：v1 只实现 Go）。
var knownExtractors = map[string]bool{"go": true}

// missingExtractors 是直方图语言减去已知抽取器，排序 —— §4.3"不留空"：
// 空读起来像"确实没有依赖"，那是假事实。
func missingExtractors(hist map[string]int) []string {
	var out []string
	for lang := range hist {
		if !knownExtractors[lang] {
			out = append(out, lang)
		}
	}
	sort.Strings(out)
	return out
}
