package blueprint

import (
	"fmt"
	"strings"
	"testing"
)

// 本文件钉 R3 的两条渲染不变量：I3（预算硬顶且截断被说出来）与
// I4（stale/unannotated/TRUNC/oversize 四态必然可见），外加 §6.2 的
// "前缀稳定"——它不是一条风格偏好，而是 provider 缓存账单的前提。

// testSnapshot 造一片派生结果。dirIndex 是 Scan 装配时冻结的私有字段，
// 测试同包所以能直接补上——但必须补，否则 ModuleOf/EvidenceModules 会静默返回未命中，
// 那正是"夹具小到根本没触发被测分支"的那类假绿。
func testSnapshot(leaves ...Leaf) *Snapshot {
	s := &Snapshot{Root: "/repo", Leaves: leaves, dirIndex: map[string]string{}}
	for _, l := range leaves {
		s.dirIndex[l.Dir] = l.Dir
		for _, f := range l.Files {
			s.dirIndex[strings.Split(f, "/")[0]] = l.Dir
		}
	}
	return s
}

func testLeaf(dir string, filesTotal int) Leaf {
	return Leaf{Dir: dir, FilesTotal: filesTotal, LangHistogram: map[string]int{"go": filesTotal}}
}

func TestRenderIndexSkeletonLines(t *testing.T) {
	snap := testSnapshot(
		testLeaf(".", 3),
		Leaf{
			Dir: "internal/tui", FilesTotal: 39, Oversize: true,
			LangHistogram: map[string]int{"go": 39},
			EdgesOut:      []Edge{{From: "internal/tui", To: "internal/agents", Source: SourceAST}, {From: "internal/tui", To: "internal/tools", Source: SourceAST}},
			EdgesOutTotal: 6,
			EdgesIn:       []Edge{{From: "internal/app", To: "internal/tui", Source: SourceAST}},
			EdgesInTotal:  1,
		},
	)
	idx := RenderIndex(snap, nil, IndexSpec{MaxTokens: 1000})
	if !idx.Ready {
		t.Fatalf("有快照却报没就绪")
	}
	joined := strings.Join(idx.Lines, "\n")
	if !strings.Contains(joined, "internal/tui 39f") {
		t.Errorf("索引行少了片名与文件数，实得:\n%s", joined)
	}
	// 出边按"对端"渲染（->agents,tools），入边同理（<-app）。
	// 把入边的 self/other 弄反的症状是 `<-tui`：一片引用它自己，零信息量。
	if !strings.Contains(joined, "->agents,tools +4") {
		t.Errorf("出边没按对端渲染或截断没写成 +N（总数 6、渲染 2，要 +4），实得:\n%s", joined)
	}
	if !strings.Contains(joined, "<-app") {
		t.Errorf("入边没按对端渲染（出/入两头混用的症状是 <-自己），实得:\n%s", joined)
	}
	if !strings.Contains(joined, "[oversize,unannotated]") && !strings.Contains(joined, "unannotated,oversize]") {
		t.Errorf("oversize 与 unannotated 两态没同时出现在行上，实得:\n%s", joined)
	}
	// §4.3：只有 Go 有边抽取器；有别的语言时必须说出来，不留空。
	rust := testSnapshot(Leaf{Dir: "src", FilesTotal: 2, LangHistogram: map[string]int{"rust": 2},
		MissingExtractors: []string{"rust"}})
	line := strings.Join(RenderIndex(rust, nil, IndexSpec{}).Lines, "\n")
	if !strings.Contains(line, "no edge extractor: rust") {
		t.Errorf("没有边抽取器的语言没被说出来（空读起来像\"确实没有依赖\"），实得:\n%s", line)
	}
}

// I3：预算必须硬顶住，而且**截断要写进 header**。
// 夹具自带"必须超预算"的前置条件——历史上这类测试因为夹具太小而永远不会失败。
func TestIndexRespectsTokenBudget(t *testing.T) {
	var leaves []Leaf
	for i := 0; i < 40; i++ {
		leaves = append(leaves, testLeaf(fmt.Sprintf("internal/mod%02d", i), 5))
	}
	snap := testSnapshot(leaves...)
	full := RenderIndex(snap, nil, IndexSpec{MaxTokens: 1 << 20})
	if len(full.Lines) != 40 {
		t.Fatalf("夹具必须先不被截断才能测截断: got %d lines", len(full.Lines))
	}
	// 用 len/4 的降级口径当预算基数，这样这条断言不依赖 tiktoken。
	budget := full.Tokens / 2
	if budget <= 0 {
		t.Fatalf("夹具的 token 账目为 0，测不出预算: tokens=%d", full.Tokens)
	}

	cut := RenderIndex(snap, nil, IndexSpec{MaxTokens: budget})
	if cut.Omitted == 0 {
		t.Errorf("预算砍到一半却没报截断: shown=%d omitted=%d", cut.ShownModules, cut.Omitted)
	}
	if cut.ShownModules+cut.Omitted != 40 {
		t.Errorf("shown+omitted 不等于总数: %d+%d != 40", cut.ShownModules, cut.Omitted)
	}
	header := strings.Join(cut.Header, "\n")
	if !strings.Contains(header, fmt.Sprintf("+%d modules not shown", cut.Omitted)) {
		t.Errorf("截断没写进 header（静默少一片是蓝图最坏的失效），实得:\n%s", header)
	}
	// 预算是真上限，不是建议。
	used := 0
	for _, l := range cut.Lines {
		used += fallbackCount(l)
	}
	for _, h := range cut.Header {
		used += fallbackCount(h)
	}
	if used > budget {
		t.Errorf("渲染结果 %d tokens 超了预算 %d", used, budget)
	}
}

// 调用方注入计数器时用它，而不是退回 len/4 —— 否则配置里的 1000 是两套口径之一。
func TestIndexUsesInjectedTokenCounter(t *testing.T) {
	var leaves []Leaf
	for i := 0; i < 10; i++ {
		leaves = append(leaves, testLeaf(fmt.Sprintf("m%02d", i), 5))
	}
	snap := testSnapshot(leaves...)
	// 每条 1 token 的极端计数器：预算 3 就只剩 3 行（header 先占 1）。
	idx := RenderIndex(snap, nil, IndexSpec{MaxTokens: 4, CountTokens: func(string) int { return 1 }})
	if idx.ShownModules > 3 {
		t.Errorf("自定义计数器没生效: shown=%d", idx.ShownModules)
	}
	if idx.Omitted == 0 {
		t.Errorf("没按注入口径截断: omitted=%d", idx.Omitted)
	}
}

// I4：四态同时出现在一份夹具里时，四个标记都必须在索引行上找到。
func TestAllFourFlagsAlwaysRendered(t *testing.T) {
	staleFP := map[string]string{"internal/a": "old"}
	snap := testSnapshot(
		Leaf{Dir: "internal/a", FilesTotal: 4, Fingerprint: "new"},
		testLeaf("internal/b", 4),
		testLeaf("internal/c", 4),
	)
	snap.Truncated = true
	snap.TruncReason = "entries"
	snap.TruncAt = "internal/b/x.go"
	doc := &Doc{Front: Front{Version: docSchemaVersion, ModuleFingerprints: staleFP}, Body: strings.Join([]string{
		"## Modules/internal/a",
		"- (aaaa) purpose: a thing <!--id:aaaa field:purpose where:internal/a/a.go seen:2026-10-18T00:00:00Z state:ok-->",
		"## Modules/internal/b",
		"- (bbbb) purpose: b thing <!--id:bbbb field:purpose where:internal/b/b.go seen:2026-10-18T00:00:00Z state:ok-->",
	}, "\n")}
	// internal/c 故意没被截断也没注解，但它同时是 oversize 的那片。
	snap.Leaves[2].Oversize = true

	idx := RenderIndex(snap, doc, IndexSpec{MaxTokens: 1 << 20})
	joined := strings.Join(idx.Lines, "\n")
	for _, want := range []string{IndexFlagStale, IndexFlagUnannotated, IndexFlagTrunc, IndexFlagOversize} {
		if !strings.Contains(joined, want) {
			t.Errorf("四态里的 %q 没出现在索引行（I4 要求必然可见），实得:\n%s", want, joined)
		}
	}
	// stale 的行动理由必须写成"要不要派人复核"，不是"蓝图坏了"（§5.2 粘滞语义）。
	header := strings.Join(idx.Header, "\n")
	if !strings.Contains(header, "annotated but stale (delegate_repo to refresh)") {
		t.Errorf("stale 没写成行动理由，实得:\n%s", header)
	}
}

// §6.2：前缀稳定。两次渲染同一份输入必须逐字节相同，
// 否则每次 fold 都会打散 provider 的 prompt 缓存（那笔账比 874 token 贵得多）。
func TestIndexRenderIsByteStable(t *testing.T) {
	snap := testSnapshot(testLeaf("a", 1), testLeaf("b", 2), testLeaf("c", 3))
	doc := &Doc{Body: "## Architecture\n- (aaaa) one thing <!--id:aaaa where:a/a.go state:ok-->"}
	first := RenderIndex(snap, doc, IndexSpec{}).String()
	second := RenderIndex(snap, doc, IndexSpec{}).String()
	if first != second {
		t.Errorf("同一份输入两次渲染不一致:\n%s\n---\n%s", first, second)
	}
	// 尾部截断：新增一片排在末尾时，前面的行必须原样不动。
	snap2 := testSnapshot(testLeaf("a", 1), testLeaf("b", 2), testLeaf("c", 3), testLeaf("d", 4))
	third := RenderIndex(snap2, doc, IndexSpec{}).Lines
	if strings.Join(third[:3], "\n") != strings.Join(RenderIndex(snap, doc, IndexSpec{}).Lines, "\n") {
		t.Errorf("末尾多一片让前面的索引行重排了（前缀稳定性是缓存账单的前提）")
	}
}

func TestRenderIndexWithoutSnapshotIsScanning(t *testing.T) {
	idx := RenderIndex(nil, nil, IndexSpec{})
	if idx.Ready {
		t.Errorf("没有快照却报就绪")
	}
	if idx.String() != ScanningLine {
		t.Errorf("没就绪时应该说不就绪，实得: %q", idx.String())
	}
	if strings.Contains(idx.String(), "modules not shown") {
		t.Errorf("没就绪时不该有任何账目行")
	}
}

func TestRelEdgeName(t *testing.T) {
	cases := []struct{ from, to, want string }{
		{"internal/blueprint", "internal/agents", "agents"},
		{"internal/tools", "internal/agents/director", "agents/director"},
		{"internal/tui", ".", "."},
		{"internal/tui", "internal/tui", "self"},
		{"a/b", "c/d", "c/d"}, // 没有公共前缀就整条留着
	}
	for _, c := range cases {
		if got := relEdgeName(c.from, c.to); got != c.want {
			t.Errorf("relEdgeName(%q,%q) = %q, 要 %q", c.from, c.to, got, c.want)
		}
	}
}
