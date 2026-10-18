package blueprint

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// R2 扫描器测试。fixture 树在 testdata/tree/（§19 附录：R2 的测量要变成仓库里的 golden）。
//
// fixture 命名冲突的解法（§10 要求"自行择优并说明"）：两条都用——
//  1. fixture 被保留的目录不叫 testdata（fixture 在 testdata/tree/ 之下，扫描根是 tree/）；
//  2. 黑名单做成可注入参数（Options.Blacklist），且扫描根自身不做黑名单检查
//     （collect 的 visitDir 对 rel=="." 豁免）——所以扫描根叫什么都行。
// 自扫 golden（scan_golden_test.go）里 internal/blueprint/testdata 被黑名单挡住，
// fixture 不会污染本仓实测数 —— 这本身就是黑名单正样本。

// fixtureRoot 是合成 fixture 树的扫描根。
func fixtureRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("testdata", "tree"))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// mustScan 是带失败前置条件的 Scan 包装。
func mustScan(t *testing.T, root string, o Options) *Snapshot {
	t.Helper()
	snap, err := Scan(root, o)
	if err != nil {
		t.Fatalf("Scan(%q) 意外失败: %v", root, err)
	}
	return snap
}

func leafOf(t *testing.T, snap *Snapshot, dir string) *Leaf {
	t.Helper()
	for i := range snap.Leaves {
		if snap.Leaves[i].Dir == dir {
			return &snap.Leaves[i]
		}
	}
	t.Fatalf("片 %q 不在快照里（有 %d 片）", dir, len(snap.Leaves))
	return nil
}

// TestScanFixtureStructure 断言片集合、链折叠、阈值截断与三级分级。
// 变异对照（§20.2 口径，什么错会红）：
//   - children 过滤退回"自身含代码"（own 而非 subHasCode）→ own==0 的 chain/ 分叉丢链，
//     "chain" 片消失，红；
//   - children 过滤退回"非测试代码"（subTotal）→ tests/ 目录整目录静默丢失，红；
//   - emit 分支缺停止边（§4.2 伪代码字面形状）→ 同一片 emit 两次，片数变 10+，红；
//   - 测试分级漏 *_test.go / tests/ 段 / test_*.py 任一 → 对应 TestsCount==0，红；
//   - 生成物探针失效 → gen 片 GeneratedCount==0，红；
//   - 显示截断漏 oversize 标记或 omitted 计数 → big 片断言红。
func TestScanFixtureStructure(t *testing.T) {
	snap := mustScan(t, fixtureRoot(t), DefaultOptions())

	if snap.ModulePath != "example.com/tree" {
		t.Fatalf("ModulePath = %q, 要 %q", snap.ModulePath, "example.com/tree")
	}
	var dirs []string
	for _, l := range snap.Leaves {
		dirs = append(dirs, l.Dir)
	}
	want := []string{".", "alpha", "beta", "big", "chain", "gen", "pyside", "rustside", "tests"}
	if !reflect.DeepEqual(dirs, want) {
		t.Fatalf("片集合 = %v, 要 %v", dirs, want)
	}

	root := leafOf(t, snap, ".")
	if !reflect.DeepEqual(root.Files, []string{"main.go"}) {
		t.Fatalf("根片 Files = %v, 要 [main.go]", root.Files)
	}
	// 测试分级：*_test.go（alpha）、tests/ 段（tests 片）、test_*.py（pyside）各计 1。
	if got := leafOf(t, snap, "alpha").TestsCount; got != 1 {
		t.Fatalf("alpha.TestsCount = %d, 要 1（*_test.go）", got)
	}
	testsLeaf := leafOf(t, snap, "tests")
	if testsLeaf.TestsCount != 1 || !reflect.DeepEqual(testsLeaf.Files, []string{"tests/helpers.go"}) {
		t.Fatalf("tests 片 = %+v, 要 Files=[tests/helpers.go] TestsCount=1（tests/ 段）", testsLeaf)
	}
	if got := leafOf(t, snap, "pyside").TestsCount; got != 1 {
		t.Fatalf("pyside.TestsCount = %d, 要 1（test_*.py）", got)
	}
	// 生成物：计 generated、不进 Files。
	gen := leafOf(t, snap, "gen")
	if gen.GeneratedCount != 1 {
		t.Fatalf("gen.GeneratedCount = %d, 要 1", gen.GeneratedCount)
	}
	for _, f := range gen.Files {
		if strings.HasSuffix(f, "gen.go") {
			t.Fatalf("生成物 %q 不得出现在 Files 里", f)
		}
	}
	// 链折叠：chain/ 只有唯一子 chain/leaf/，折叠为一片覆盖整条链。
	chain := leafOf(t, snap, "chain")
	if !reflect.DeepEqual(chain.Files, []string{"chain/leaf/leaf.go"}) {
		t.Fatalf("chain 片 Files = %v, 要 [chain/leaf/leaf.go]（折叠）", chain.Files)
	}
	if _, ok := snap.ModuleOf("chain/leaf/leaf.go"); !ok {
		t.Fatalf("ModuleOf(chain/leaf/leaf.go) 未命中 chain 片的覆盖")
	}
	// 规模阈值：45 个文件 > 40 → oversize，Files 按 24 截断，+21 more 会计数。
	big := leafOf(t, snap, "big")
	if !big.Oversize || big.FilesTotal != 45 || big.OmittedFiles != 21 || len(big.Files) != 24 {
		t.Fatalf("big 片截断形状错: over=%v total=%d omit=%d len=%d",
			big.Oversize, big.FilesTotal, big.OmittedFiles, len(big.Files))
	}
	if got := big.LangHistogram["go"]; got != 45 {
		t.Fatalf("big.LangHistogram[go] = %d, 要 45", got)
	}
	// 非 Go 语言直方图与 MissingExtractors（§4.3：不留空）。
	rs := leafOf(t, snap, "rustside")
	if !reflect.DeepEqual(rs.MissingExtractors, []string{"rust"}) {
		t.Fatalf("rustside.MissingExtractors = %v, 要 [rust]", rs.MissingExtractors)
	}
	if got := leafOf(t, snap, "pyside").LangHistogram["python"]; got != 2 {
		t.Fatalf("pyside.LangHistogram[python] = %d, 要 2", got)
	}
	// 黑名单正样本：node_modules / vendor / .hidden 的内容不得出现在任何片里。
	for i := range snap.Leaves {
		for _, f := range snap.Leaves[i].Files {
			for _, banned := range []string{"node_modules/", "vendor/", ".hidden/", "ignored.js", "extra.go", "x.go"} {
				if strings.Contains(f, banned) {
					t.Fatalf("黑名单内容 %q 出现在片 %q", f, snap.Leaves[i].Dir)
				}
			}
		}
	}
	if snap.OrphanDocs != 0 {
		t.Fatalf("OrphanDocs = %d, 要 0（docs/ 归入根片）", snap.OrphanDocs)
	}
	if snap.Truncated || snap.RejectedSymlinks != 0 {
		t.Fatalf("fixture 不应截断/拒链: truncated=%v symlinks=%d", snap.Truncated, snap.RejectedSymlinks)
	}
}

// TestScanFixtureDocsAndEntries 断言文档归并与入口/命令事实（§4.3 分级）。
// 变异对照：settleDocOnly 归并中途就写 cover（同目录第二条记录被"已覆盖"短路）→
// 根片 docs 缺 docs/ 的后两篇，md==2、yaml 缺失，红；
// package.json / Cargo.toml 解析失效 → 对应 Entry 缺失，红；
// 入口事实漏 *_source → Source 断言红。
func TestScanFixtureDocsAndEntries(t *testing.T) {
	snap := mustScan(t, fixtureRoot(t), DefaultOptions())
	root := leafOf(t, snap, ".")

	// docs/（a.md b.md cfg.yaml）整目录归并进根片：md:3 = README + a + b。
	wantDocs := map[string]int{"go.mod": 1, "json": 1, "makefile": 1, "md": 3, "sh": 1, "toml": 1, "yaml": 1}
	if !reflect.DeepEqual(root.DocsByKind, wantDocs) {
		t.Fatalf("根片 DocsByKind = %v, 要 %v", root.DocsByKind, wantDocs)
	}
	// 入口事实：ast 的 go-main 与 manifest 的 Node/Rust 入口分层。
	wantEntries := []Entry{
		{Kind: "cargo-bin", Name: "treeapp", Source: SourceManifest},
		{Kind: "go-main", Name: "main", Source: SourceAST},
		{Kind: "node-bin", Name: "treecli", Source: SourceManifest},
		{Kind: "node-main", Name: "index.js", Source: SourceManifest},
		{Kind: "node-script", Name: "build", Source: SourceManifest},
		{Kind: "node-script", Name: "test", Source: SourceManifest},
	}
	if !reflect.DeepEqual(root.Entries, wantEntries) {
		t.Fatalf("根片 Entries = %+v, 要 %+v", root.Entries, wantEntries)
	}
	if root.EntriesTotal != len(wantEntries) {
		t.Fatalf("EntriesTotal = %d, 要 %d", root.EntriesTotal, len(wantEntries))
	}
	// 根 Makefile/build.sh（只列名字，绝不执行）。
	wantCmd := CommandFacts{MakeTargets: []string{"build", "test"}, HasBuildSh: true, Source: SourceFilename}
	if !reflect.DeepEqual(snap.Commands, wantCmd) {
		t.Fatalf("Commands = %+v, 要 %+v", snap.Commands, wantCmd)
	}
}

// TestScanFixtureEdges 断言模块级双向边（§4.3）。
// 变异对照：module path 前缀匹配失效 → 全部边消失，红；
// 反向边（转置）缺失 → EdgesIn 全 0，红；
// 同片自边未丢弃 → root.EdgesOut 里出现 (.,.)，红；
// 目标目录不在走到的集合里也建边（不猜判据失效）→ 不影响本 fixture，由 I6 场景覆盖。
func TestScanFixtureEdges(t *testing.T) {
	snap := mustScan(t, fixtureRoot(t), DefaultOptions())
	root := leafOf(t, snap, ".")
	wantOut := []Edge{
		{From: ".", To: "alpha", Imports: 1, Source: SourceAST},
		{From: ".", To: "beta", Imports: 1, Source: SourceAST},
	}
	if !reflect.DeepEqual(root.EdgesOut, wantOut) || root.EdgesOutTotal != 2 {
		t.Fatalf("根片 EdgesOut = %+v (total %d), 要 %+v (2)", root.EdgesOut, root.EdgesOutTotal, wantOut)
	}
	if root.EdgesInTotal != 0 {
		t.Fatalf("根片 EdgesInTotal = %d, 要 0", root.EdgesInTotal)
	}
	beta := leafOf(t, snap, "beta")
	if beta.EdgesInTotal != 2 {
		t.Fatalf("beta.EdgesInTotal = %d, 要 2（← . 与 ← alpha）", beta.EdgesInTotal)
	}
	var froms []string
	for _, e := range beta.EdgesIn {
		froms = append(froms, e.From)
	}
	if !reflect.DeepEqual(froms, []string{".", "alpha"}) {
		t.Fatalf("beta.EdgesIn 的 From = %v, 要 [. alpha]", froms)
	}
	for i := range snap.Leaves {
		l := &snap.Leaves[i]
		if len(l.Fingerprint) != 64 {
			t.Fatalf("片 %q 指纹长度 %d, 要 64 hex", l.Dir, len(l.Fingerprint))
		}
	}
}

// TestScanMapping 断言 R6 要用的证据映射（ModuleOf / EvidenceModules）。
// 变异对照：dirIndex 没在 Scan 装配时冻结（cover 未传给 Snapshot）→ 全部未命中，红；
// 上溯缺失（只查目录精确命中）→ 文件路径查不到，红；
// 越出项目根的 .. 路径未拒 → red（I6 同源）。
func TestScanMapping(t *testing.T) {
	snap := mustScan(t, fixtureRoot(t), DefaultOptions())
	cases := []struct {
		path string
		want string
		ok   bool
	}{
		{"main.go", ".", true},
		{"alpha/alpha.go", "alpha", true},
		{"chain/anything.go", "chain", true},
		{filepath.Join(fixtureRoot(t), "alpha", "alpha.go"), "alpha", true}, // 绝对路径剥 Root 前缀
		// 仓库里不存在的路径：按"最近覆盖祖先"语义落在根片 —— 覆盖域的判定是路径拓扑，
		// 文件是否真存在由 R6 的锚点验旧（§5.2(b)）负责，ModuleOf 不重复这一层。
		{"nope/x.go", ".", true},
		{"../outside.go", "", false}, // 越出项目根不映射
	}
	for _, tc := range cases {
		got, ok := snap.ModuleOf(tc.path)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("ModuleOf(%q) = (%q,%v), 要 (%q,%v)", tc.path, got, ok, tc.want, tc.ok)
		}
	}
	got := snap.EvidenceModules([]string{"main.go", "alpha/alpha.go", "chain/deep.go", "nope/x.go"})
	if want := []string{".", "alpha", "chain"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("EvidenceModules = %v, 要 %v", got, want)
	}
}

// TestScanDeterministic 断言确定性（I15 的前置：两次扫描同一仓库 DeepEqual）。
// 变异对照：任何 map 遍历未排序、时间戳/随机字段进 Snapshot → 红。
func TestScanDeterministic(t *testing.T) {
	root := fixtureRoot(t)
	a := mustScan(t, root, DefaultOptions())
	b := mustScan(t, root, DefaultOptions())
	if !reflect.DeepEqual(a, b) {
		t.Fatal("两次扫描同一 fixture 结果不一致 —— 违反确定性")
	}
}

// TestScanRefusesSymlinkedDir 是 I6 闸门：一次 ln -s 就不能把扫描器带出项目根。
// 变异对照：删掉 ModeSymlink 检查（或把它放在 IsDir 之后）→ 链接条目不再计数
// （RejectedSymlinks==0，红）；链接到 .go 的 impostor 会被当普通代码文件
// 读进文件列表（impostor.go 进根片 Files，红）。
func TestScanRefusesSymlinkedDir(t *testing.T) {
	tmp := t.TempDir()
	writeFile := func(name, content string) {
		if err := os.WriteFile(filepath.Join(tmp, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFile("go.mod", "module tmp\n") // 根护栏：无 manifest 的临时目录会被拒
	if err := os.MkdirAll(filepath.Join(tmp, "realdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(filepath.Join("realdir", "real.go"), "package realdir\n")
	if err := os.Symlink("/", filepath.Join(tmp, "rootlink")); err != nil { // I6 的攻击向量
		t.Fatal(err)
	}
	if err := os.Symlink("realdir", filepath.Join(tmp, "dirlink")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("realdir", "real.go"), filepath.Join(tmp, "impostor.go")); err != nil {
		t.Fatal(err)
	}
	snap := mustScan(t, tmp, DefaultOptions())
	if snap.RejectedSymlinks != 3 {
		t.Fatalf("RejectedSymlinks = %d, 要 3（rootlink + dirlink + impostor.go）", snap.RejectedSymlinks)
	}
	// 临时项目 total=1 ≤ 阈值：整片 emit，全部文件都在根片里。
	root := leafOf(t, snap, ".")
	if !reflect.DeepEqual(root.Files, []string{filepath.Join("realdir", "real.go")}) {
		t.Fatalf("根片 Files = %v, 要 [realdir/real.go] —— 符号链接条目不得进入文件列表（I6）", root.Files)
	}
	for i := range snap.Leaves {
		for _, f := range snap.Leaves[i].Files {
			if f == "impostor.go" || strings.HasPrefix(f, "dirlink") || strings.HasPrefix(f, "rootlink") {
				t.Fatalf("符号链接 %q 不得进入片 %q（I6）", f, snap.Leaves[i].Dir)
			}
		}
	}
}

// TestScanSkipsSubmoduleAtDirLevel 是 I6 闸门（§4.1 记录的实测 bug：
// submodule 跳过写在文件分支会多出 14 片假模块）。
// 变异对照：把 submodule 判定从 visitDir 挪到 visitFile → vendored-lib 被当普通目录
// 走进去，Submodule leaf 消失、lib.go 进根片 Files，红；
// 声明了但磁盘上不存在的 path（ghost-lib）也 emit leaf → 红一不做假片。
func TestScanSkipsSubmoduleAtDirLevel(t *testing.T) {
	tmp := t.TempDir()
	writeFile := func(name, content string) {
		if err := os.WriteFile(filepath.Join(tmp, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFile("go.mod", "module tmp\n")
	writeFile(".gitmodules", "[submodule \"lib\"]\n\tpath = vendored-lib\n\turl = https://example.com/lib.git\n"+
		"[submodule \"ghost\"]\n\tpath = ghost-lib\n\turl = https://example.com/ghost.git\n")
	writeFile("main.go", "package main\n")
	if err := os.MkdirAll(filepath.Join(tmp, "vendored-lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(filepath.Join("vendored-lib", "lib.go"), "package lib\n")

	snap := mustScan(t, tmp, DefaultOptions())
	if len(snap.Leaves) != 2 {
		t.Fatalf("片数 = %d, 要 2（根片 + submodule 片；ghost-lib 磁盘上不存在，不做假片）", len(snap.Leaves))
	}
	sub := leafOf(t, snap, "vendored-lib")
	if !sub.Submodule || len(sub.Files) != 0 {
		t.Fatalf("vendored-lib 应是整片 submodule leaf（Files 恒空）: %+v", sub)
	}
	root := leafOf(t, snap, ".")
	for _, f := range root.Files {
		if strings.HasPrefix(f, "vendored-lib/") {
			t.Fatalf("submodule 内容 %q 不得进入根片（目录分支跳过失效）", f)
		}
	}
	for i := range snap.Leaves {
		if snap.Leaves[i].Dir == "ghost-lib" {
			t.Fatal("ghost-lib 未在磁盘上出现，不得 emit leaf")
		}
	}
}

// TestScanBudgetTruncation 断言硬预算（§4.1：宁可说没看完，不可静默少一片）。
// 变异对照：条目预算判定删掉 → Truncated 恒 false，红；
// errStopEntries 不被吞（walk 错误泄漏成 Scan 错误）→ Scan 返回 error，红。
func TestScanBudgetTruncation(t *testing.T) {
	snap := mustScan(t, fixtureRoot(t), Options{MaxEntries: 10})
	if !snap.Truncated || snap.TruncReason != "entries" || snap.TruncAt == "" {
		t.Fatalf("超预算必须标 TRUNC 并给出截断点: truncated=%v reason=%q at=%q",
			snap.Truncated, snap.TruncReason, snap.TruncAt)
	}
}
