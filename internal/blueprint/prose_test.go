package blueprint

import (
	"fmt"
	"strings"
	"testing"
)

// 本文件钉 I13（可见集合 == 派生集合）与 §7.1 的读通道语义：
// 孤儿段落既渲染不出来也读不到；未知模块**绝不返回空**（空会被读成"这里没东西"，
// 然后模型开始不信蓝图）。

func proseFixture() (*Snapshot, *Doc) {
	snap := testSnapshot(
		Leaf{Dir: "internal/a", FilesTotal: 2, Fingerprint: "fa"},
		Leaf{Dir: "internal/b", FilesTotal: 3, Fingerprint: "fb"},
	)
	doc := &Doc{Front: Front{Version: docSchemaVersion}, Body: strings.Join([]string{
		"## Architecture",
		"- (a1) Director is the only user-facing agent <!--id:a1 where:internal/a/a.go src:repo_agent@1111 seen:2026-10-18T00:00:00Z state:ok-->",
		"## Modules/internal/a",
		"- (a2) purpose: A owns things <!--id:a2 field:purpose where:internal/a/a.go src:repo_agent@1111 seen:2026-10-18T00:00:00Z state:ok-->",
		"- (a3) add_here: new handlers go here <!--id:a3 field:add_here where:internal/a/a.go:2 src:repo_agent@1111 seen:2026-10-18T00:00:00Z state:ok-->",
		"## Modules/internal/gone",
		"- (x9) purpose: a deleted directory <!--id:x9 field:purpose where:internal/gone/g.go src:repo_agent@1111 seen:2026-10-01T00:00:00Z state:ok-->",
	}, "\n")}
	return snap, doc
}

// I13：目录改名/删除后，旧注解永远不会再被扫到、也永远不会自己删；
// 更坏的是目录名被复用时模型会读到关于**另一个旧模块**的散文。
func TestOrphanSectionNeverRendered(t *testing.T) {
	snap, doc := proseFixture()

	for _, view := range []ProseSpec{
		{},                    // 全量
		{Section: SecModules}, // 只看模块段
		{Module: "internal/gone"},
	} {
		p := RenderProse(snap, doc, view)
		if strings.Contains(joinAll(p), "a deleted directory") {
			t.Errorf("孤儿段落被渲染出来了（spec %+v）:\n%s", view, joinAll(p))
		}
	}
	// 只指定模块时，孤儿等于"未知模块"：走候选回执而不是空。
	p := RenderProse(snap, doc, ProseSpec{Section: SecModules, Module: "internal/gone"})
	if !p.UnknownModule {
		t.Errorf("孤儿模块没有走未知模块回执")
	}
	// 账目里也不许有它（否则"41 条原子"里混着没人能读到的 1 条）。
	idx := RenderIndex(snap, doc, IndexSpec{MaxTokens: 1 << 20})
	if idx.Atoms != 3 {
		t.Errorf("原子账目把孤儿段落算进去了: atoms=%d, 要 3", idx.Atoms)
	}
}

// §7.1：找不到模块时返回最接近的 5 个候选 + 一句去查 semantic_search，**不能返回空**。
func TestUnknownModuleGivesCandidatesNotEmpty(t *testing.T) {
	snap, doc := proseFixture()
	p := RenderProse(snap, doc, ProseSpec{Section: SecModules, Module: "internal/aaa"})
	if !p.UnknownModule {
		t.Fatalf("未知模块没被识别")
	}
	if len(p.Candidates) == 0 {
		t.Errorf("候选为空 —— 空回执会被读成\"这里没东西\"")
	}
	if len(p.Lines) == 0 {
		t.Errorf("未知模块返回了空正文")
	}
	out := joinAll(p)
	if !strings.Contains(out, "semantic_search") {
		t.Errorf("回执没指路到符号检索:\n%s", out)
	}
	// 候选按共享路径段排序：internal/a 比 internal/b 更接近 internal/aaa。
	if p.Candidates[0] != "internal/a" {
		t.Errorf("最接近的候选排错: %v", p.Candidates)
	}
}

func TestProseFiltersFieldAndState(t *testing.T) {
	snap, doc := proseFixture()

	only := RenderProse(snap, doc, ProseSpec{Section: SecModules, Module: "internal/a", Field: "purpose"})
	out := joinAll(only)
	if !strings.Contains(out, "A owns things") {
		t.Errorf("field 过滤把该给的滤掉了:\n%s", out)
	}
	if strings.Contains(out, "new handlers go here") {
		t.Errorf("field 过滤没生效:\n%s", out)
	}

	// state 过滤走的是渲染态：指纹不符的片整片算 stale，即使落盘行写着 state:ok。
	snap2, doc2 := proseFixture()
	doc2.Front.ModuleFingerprints = map[string]string{"internal/a": "changed"}
	stale := RenderProse(snap2, doc2, ProseSpec{State: StateStale})
	if !strings.Contains(joinAll(stale), "A owns things") {
		t.Errorf("按 state=stale 过滤时没拿到结构变化过的那片:\n%s", joinAll(stale))
	}
	if strings.Contains(joinAll(stale), "Director is the only user-facing agent") {
		t.Errorf("没变化的仓库级段落被判成 stale:\n%s", joinAll(stale))
	}
}

// I3 的按需视图版本：预算是硬顶，而且**夹具必须先超预算**（历史上这类测试因为
// 夹具太小而永远不会失败）。
func TestProseRespectsTokenBudget(t *testing.T) {
	snap := testSnapshot(testLeaf("internal/a", 2))
	var lines []string
	lines = append(lines, "## Architecture")
	for i := 0; i < 30; i++ {
		lines = append(lines, fmt.Sprintf(
			"- (%04x) a long claim about the repository that costs tokens <!--id:%04x where:internal/a/a.go state:ok-->", i, i))
	}
	doc := &Doc{Front: Front{Version: docSchemaVersion}, Body: strings.Join(lines, "\n")}

	full := RenderProse(snap, doc, ProseSpec{BudgetTokens: 1 << 20})
	if full.Total < 25 {
		t.Fatalf("夹具太短，测不出预算: total=%d", full.Total)
	}
	cut := RenderProse(snap, doc, ProseSpec{BudgetTokens: full.Tokens / 3})
	if cut.Omitted == 0 {
		t.Errorf("预算砍到三分之一却没报省略: kept=%d", len(cut.Lines))
	}
	if !cut.Truncated {
		t.Errorf("省略了行却没置 Truncated，调用方无从知道拿到的是片段")
	}
	used := 0
	for _, l := range cut.Lines {
		used += fallbackCount(l)
	}
	for _, h := range cut.Header {
		used += fallbackCount(h)
	}
	if used > cut.Budget {
		t.Errorf("按需渲染 %d tokens 超了预算 %d", used, cut.Budget)
	}
}

func TestProsePagination(t *testing.T) {
	snap, doc := proseFixture()
	all := RenderProse(snap, doc, ProseSpec{})
	if all.Total < 4 {
		t.Fatalf("夹具行数不足以测分页: %d", all.Total)
	}
	page := RenderProse(snap, doc, ProseSpec{Offset: 1, Limit: 1})
	if len(page.Lines) != 1 {
		t.Errorf("limit 没生效: %d 行", len(page.Lines))
	}
	if !page.Truncated {
		t.Errorf("分页窗口没标记 Truncated")
	}
	// 窗口必须等于全量视图的那一段（比"少了一行"强：整行内容逐字一致）。
	if len(page.Lines) == 1 && page.Lines[0] != all.Lines[1] {
		t.Errorf("offset 窗口与全量视图不对齐: got %q, want %q", page.Lines[0], all.Lines[1])
	}
	// 越界 offset 不该 panic，也不该吐内容。
	over := RenderProse(snap, doc, ProseSpec{Offset: 999, Limit: 5})
	if len(over.Lines) != 0 {
		t.Errorf("越界 offset 拿到了 %d 行", len(over.Lines))
	}
}

// 没有快照时不阻塞、不编造：Ready=false。
func TestRenderProseWithoutSnapshot(t *testing.T) {
	_, doc := proseFixture()
	p := RenderProse(nil, doc, ProseSpec{})
	if p.Ready {
		t.Errorf("没有快照却报就绪")
	}
	if len(p.Lines) != 0 {
		t.Errorf("没有快照时不该渲染散文，实得 %d 行", len(p.Lines))
	}
}

// 坏行必须被回报，不许静默跳过（I18 的前半：这条承诺是"为什么没更新"能被回答的基础）。
func TestProseReportsBrokenAtomLines(t *testing.T) {
	snap := testSnapshot(testLeaf("internal/a", 1))
	doc := &Doc{Front: Front{Version: docSchemaVersion}, Body: strings.Join([]string{
		"## Architecture",
		"- (a1) fine <!--id:a1 state:ok-->",
		"- (bad) broken one <!--id:mismatch state:ok-->",
	}, "\n")}
	p := RenderProse(snap, doc, ProseSpec{})
	if len(p.BadLines) == 0 {
		t.Errorf("坏掉的原子行被静默跳过了:\n%s", joinAll(p))
	}
}
