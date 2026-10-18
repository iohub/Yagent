package blueprint

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// 本文件实现 §4.3 的边抽取：go/parser ImportsOnly + go.mod module path 前缀
// → 目录→目录边 → 聚合到模块级；package main 即入口（同一次解析零额外成本）。

// edgeResult 是边抽取的产物。
type edgeResult struct {
	// edgesOut[srcLeaf][tgtLeaf] = 聚合的 import 条数（全量 —— 指纹与 Total 计数的输入）。
	EdgesOut map[string]map[string]int
	// DirEdges 是目录级边的条数（§4.3 记账：今天实测 121 条目录级 → 103 条模块级）。
	DirEdges int
	// MainLeaves 是 package main 所在的片集合（Source=ast）。
	MainLeaves []string
	// ParseFailures 是解析失败的 .go 文件数（确定性跳过：宁可少一条边，不可中止扫描）。
	ParseFailures int
}

// extractEdges 对全部非测试非生成 Go 文件做 ImportsOnly 解析。
// 只取非测试文件是 §19 附录口径（"非测试 .go 文件"参与边）—— 测试的 import
// 不代表运行时依赖。modulePath 为空（非 Go 仓库）时返回空结果 —— 边为零不是错误（I9）。
func extractEdges(root, modulePath string, records []FileRecord, dirs map[string]bool, cover map[string]string) *edgeResult {
	er := &edgeResult{EdgesOut: map[string]map[string]int{}}
	if modulePath == "" {
		return er
	}
	mainDirs := map[string]bool{}
	for i := range records {
		rec := &records[i]
		if !rec.IsCode || rec.IsTest || rec.IsGenerated || rec.Lang != "go" {
			continue
		}
		// 每文件一个 fset：位置信息用完即弃，无共享状态（确定性）。
		fset := token.NewFileSet()
		ast, err := parser.ParseFile(fset, filepath.Join(root, rec.RelPath), nil, parser.ImportsOnly)
		if err != nil {
			er.ParseFailures++ // 解析失败的文件不参与边，也不中止扫描
			continue
		}
		if ast.Name != nil && ast.Name.Name == "main" {
			mainDirs[rec.Dir] = true // package main 即入口，零额外成本（§4.3）
		}
		for _, imp := range ast.Imports {
			ipath, uerr := strconv.Unquote(imp.Path.Value)
			if uerr != nil {
				continue
			}
			var target string
			switch {
			case ipath == modulePath:
				target = "."
			case strings.HasPrefix(ipath, modulePath+"/"):
				target = ipath[len(modulePath)+1:]
			default:
				continue // 仓库外依赖不进边
			}
			if !dirs[target] {
				// 目标目录本次没走到（被黑名单/预算挡住）—— 不猜，宁缺勿假。
				continue
			}
			if er.EdgesOut[rec.Dir] == nil {
				er.EdgesOut[rec.Dir] = map[string]int{}
			}
			er.EdgesOut[rec.Dir][target]++
			er.DirEdges++
		}
	}
	// 目录级 → 模块级聚合（§4.3）；同片自边丢弃 —— 模块内部依赖不是模块间依赖。
	moduleEdges := map[string]map[string]int{}
	for srcDir, ts := range er.EdgesOut {
		srcLeaf, ok := cover[srcDir]
		if !ok {
			continue // 不可能（有代码的目录必被覆盖），保守跳过
		}
		for tgtDir, n := range ts {
			tgtLeaf, ok := cover[tgtDir]
			if !ok {
				continue
			}
			if srcLeaf == tgtLeaf {
				continue
			}
			if moduleEdges[srcLeaf] == nil {
				moduleEdges[srcLeaf] = map[string]int{}
			}
			moduleEdges[srcLeaf][tgtLeaf] += n
		}
	}
	er.EdgesOut = moduleEdges
	// package main 的目录提到片级。
	for dir := range mainDirs {
		if leaf, ok := cover[dir]; ok {
			er.MainLeaves = append(er.MainLeaves, leaf)
		}
	}
	sort.Strings(er.MainLeaves)
	return er
}
