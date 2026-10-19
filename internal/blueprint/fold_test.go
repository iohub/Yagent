package blueprint

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件钉 R6 的四条承重不变量：
//   I17 提交只能覆盖本次 run 的证据集模块
//   I18 "没更新"与"更新失败"在账目上可区分
//   I20 fold 幂等 + 顺序可复现 + 多进程 CAS
//   I14 散文里的路径样 token 必须属于该片
// 外加 §7.4 的取代/配额与 §7.5 第 5 条"锚点判过期不拒收"。

var foldNow = time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)

// foldFixture 造一个真能扫的小仓库 + 一个独立存储根。
// 用真 Scan 而不是手搓 Snapshot：dirIndex、边、指纹都得是真的，
// 否则 ModuleOf/邻居判定这些被测对象在夹具里根本不成立（坑 33 的同一条教训）。
//
// internal/a 放 42 个文件是**夹具的前置条件**：§4.2 在 total <= MaxFilesPerModule(40) 时
// 把整棵树折成一片根 leaf —— 只给 2 个文件的夹具扫出来是一片 "."，
// 于是"internal/a 与 internal/b 是两个模块"这个前提本身就不成立，
// 邻居判据、证据集、同字段覆盖全都在测一条生产里不存在的形状。
func foldFixture(t *testing.T) (storeRoot string, snap *Snapshot, evidence map[string]bool) {
	t.Helper()
	project := t.TempDir()
	write(t, project, "go.mod", "module example.com/x\n\ngo 1.25\n")
	write(t, project, "internal/a/a.go", "package a\n\nfunc Foo() {}\n")
	for i := 0; i < 41; i++ {
		write(t, project, fmt.Sprintf("internal/a/fill%02d.go", i), "package a\n")
	}
	write(t, project, "internal/b/b.go", "package b\n\nimport \"example.com/x/internal/a\"\n\nvar _ = a.Foo\n")
	write(t, project, "internal/other/o.go", "package other\n")

	snap, err := Scan(project, DefaultOptions())
	if err != nil {
		t.Fatalf("夹具扫描失败: %v", err)
	}
	for _, want := range []string{"internal/a", "internal/b", "internal/other"} {
		if _, ok := snap.moduleByDir(want); !ok {
			t.Fatalf("夹具没把 %q 扫成独立一片（§4.2 把整棵树折成了一片）: leaves=%d", want, len(snap.Leaves))
		}
	}
	// b 依赖 a —— 邻居判据（I14 的放行侧）必须有真边，不然测试只是在证明"没边"。
	if !neighborSet(snap, "internal/b")["internal/a"] {
		t.Fatalf("夹具没造出 internal/b -> internal/a 这条边")
	}
	evidence = map[string]bool{}
	for _, m := range snap.EvidenceModules([]string{"internal/a/a.go"}) {
		evidence[m] = true
	}
	if !evidence["internal/a"] {
		t.Fatalf("证据集没把 internal/a 收进来: %v", evidence)
	}
	if evidence["internal/b"] {
		t.Fatalf("证据集把没读过的片也算进去了: %v", evidence)
	}
	return filepath.Join(t.TempDir(), "blueprint"), snap, evidence
}

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatalf("夹具目录失败: %v", err)
	}
	if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
		t.Fatalf("夹具写入失败: %v", err)
	}
}

func req(items ...FoldItem) FoldRequest {
	return FoldRequest{
		Items: items, Src: "repo_agent@deadbeef", Now: foldNow,
		ProjectID: "proj_1234", ProjectPath: "/somewhere",
		GitSHA: "abc123", GitBranch: "main",
	}
}

func moduleItem(module, field, claim, where string) FoldItem {
	return FoldItem{Section: "modules", Module: module, Field: field, Claim: claim, Where: where}
}

// 这条测试是整件事的目的本身：**文件真的落盘**。
func TestCommitWritesBlueprintFile(t *testing.T) {
	root, snap, ev := foldFixture(t)
	reqCopy := req(moduleItem("internal/a", "purpose", "A owns the thing", "internal/a/a.go:3"))
	reqCopy.Evidence = ev

	res, err := Commit(root, snap, reqCopy)
	if err != nil {
		t.Fatalf("fold 失败: %v", err)
	}
	if res.Added != 1 || len(res.Rejected) != 0 {
		t.Fatalf("账目不对: %+v rejects=%v", res, res.Rejected)
	}
	data, err := os.ReadFile(DocPath(root))
	if err != nil {
		t.Fatalf("blueprint.md 没落盘: %v", err)
	}
	body := string(data)
	for _, want := range []string{"A owns the thing", "## Modules/internal/a", `"project_id": "proj_1234"`, `"git_sha": "abc123"`, "field:purpose"} {
		if !strings.Contains(body, want) {
			t.Errorf("文档里少了 %q:\n%s", want, body)
		}
	}
	// 指纹跟着写：那是"这段散文当时是关于哪个形状的"的唯一基线（§5.2(a)）。
	doc, err := Load(root)
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	leaf, ok := snap.moduleByDir("internal/a")
	if !ok || doc.Front.ModuleFingerprints["internal/a"] != leaf.Fingerprint {
		t.Errorf("指纹没记或记错: %+v", doc.Front.ModuleFingerprints)
	}
	// 第一次 fold 之前没有整档，所以 revisions/ 不该被凭空建出来。
	if _, err := os.Stat(RevisionsDir(root)); err == nil {
		t.Errorf("首次 fold 就写了 revisions/（没有旧内容可归档）")
	}
}

// I20 之一：同批原子连 fold 两次字节不变。
func TestFoldIsIdempotent(t *testing.T) {
	root, snap, ev := foldFixture(t)
	first := req(moduleItem("internal/a", "purpose", "A owns the thing", "internal/a/a.go"))
	first.Evidence = ev
	if _, err := Commit(root, snap, first); err != nil {
		t.Fatalf("第一次 fold 失败: %v", err)
	}
	before, err := os.ReadFile(DocPath(root))
	if err != nil {
		t.Fatalf("读档失败: %v", err)
	}

	second := req(moduleItem("internal/a", "purpose", "A owns the thing", "internal/a/a.go"))
	second.Evidence = ev
	res, err := Commit(root, snap, second)
	if err != nil {
		t.Fatalf("第二次 fold 失败: %v", err)
	}
	if res.Added != 0 || res.Deduped != 1 {
		t.Errorf("重复提交没走 dedupe 分支: %+v", res)
	}
	after, err := os.ReadFile(DocPath(root))
	if err != nil {
		t.Fatalf("读档失败: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("同一批原子连 fold 两次改了字节（幂等破了，CAS 重放就不安全）:\n%s\n---\n%s", before, after)
	}
}

// I20 之二：乱序输入产出同一份正文（段内 (where, claim) 稳定排序）。
func TestFoldOrderIndependent(t *testing.T) {
	left, snap, ev := foldFixture(t)
	right, snap2, ev2 := foldFixture(t)

	a := moduleItem("internal/a", "purpose", "first claim", "internal/a/a.go")
	b := FoldItem{Section: "gotchas", Claim: "second claim", Where: "internal/a/a.go:3"}
	c := moduleItem("internal/a", "data_flow", "third claim", "internal/a/a.go")

	one := req(c, a, b)
	one.Evidence = ev
	two := req(b, a, c)
	two.Evidence = ev2
	if _, err := Commit(left, snap, one); err != nil {
		t.Fatalf("fold 失败: %v", err)
	}
	if _, err := Commit(right, snap2, two); err != nil {
		t.Fatalf("fold 失败: %v", err)
	}
	x, _ := os.ReadFile(DocPath(left))
	y, _ := os.ReadFile(DocPath(right))
	if string(x) != string(y) {
		t.Errorf("提交顺序影响了产出字节:\n%s\n---\n%s", x, y)
	}
}

// I20 之三：CAS 重放既不丢对方的条目，也不复制自己的条目。
func TestFoldCASReplay(t *testing.T) {
	root, snap, ev := foldFixture(t)
	ours := req(moduleItem("internal/a", "purpose", "our claim", "internal/a/a.go"))
	ours.Evidence = ev

	// 假装另一个 TUI tab 在我们读之后、写之前改了文档：塞进去一条完全不同的原子。
	committed := false
	old := commitReReadHook
	commitReReadHook = func(attempt int) {
		if committed || attempt != 0 {
			return
		}
		committed = true
		// 假装另一个 TUI tab 在我们读之后、写之前改了文档：塞进去一条完全不同的原子。
		doc, _, err := LoadForFold(root)
		if err != nil {
			t.Errorf("钩子里读档失败: %v", err)
			return
		}
		if doc == nil {
			doc = EmptyDoc("proj_1234", "/somewhere") // 首次 fold：对方也是从空文档开始写
		}
		doc.Body = strings.TrimSpace(doc.Body) +
			"\n## Gotchas\n- (ffff) somebody else wrote this <!--id:ffff where:internal/a/a.go src:other@1 seen:2026-10-19T08:00:00Z state:ok-->\n"
		if err := Save(root, doc); err != nil {
			t.Errorf("钩子里写档失败: %v", err)
		}
	}
	defer func() { commitReReadHook = old }()

	res, err := Commit(root, snap, ours)
	if err != nil {
		t.Fatalf("CAS 应当重放成功，实得: %v", err)
	}
	if res.Added != 1 {
		t.Errorf("重放后自己的条目没落地: %+v", res)
	}
	doc, err := Load(root)
	if err != nil {
		t.Fatalf("读档失败: %v", err)
	}
	body := doc.Body
	if !strings.Contains(body, "somebody else wrote this") {
		t.Errorf("重放把别人那条丢了:\n%s", body)
	}
	if !strings.Contains(body, "our claim") {
		t.Errorf("重放把自己那条弄没了:\n%s", body)
	}
	if n := strings.Count(body, "our claim"); n != 1 {
		t.Errorf("重放复制了同一条原子: %d 次", n)
	}
}

// 两次都冲突 → 放弃本次并回报，由下一次 fold 自然补上（不引入锁文件）。
func TestFoldGivesUpAfterTwoConflicts(t *testing.T) {
	root, snap, ev := foldFixture(t)
	r := req(moduleItem("internal/a", "purpose", "will not land", "internal/a/a.go"))
	r.Evidence = ev
	old := commitReReadHook
	n := 0
	commitReReadHook = func(int) {
		// 每一次重读都撞车：第 0 次触发重放，第 1 次仍然冲突 → 放弃本次。
		doc, _, err := LoadForFold(root)
		if err != nil {
			return
		}
		if doc == nil {
			doc = EmptyDoc("proj_1234", "/somewhere")
		}
		n++
		doc.Body = strings.TrimSpace(doc.Body) + fmt.Sprintf("\n## Gotchas\n- (g%03d) churn <!--id:g%03d where:internal/a/a.go state:ok-->", n, n)
		doc.Front.Folds = n
		_ = Save(root, doc)
	}
	defer func() { commitReReadHook = old }()

	if _, err := Commit(root, snap, r); err == nil {
		t.Errorf("每次重读都被改动，却始终没报冲突")
	}
	doc, _ := Load(root)
	if doc != nil && strings.Contains(doc.Body, "will not land") {
		t.Errorf("放弃本次却还是写了进去")
	}
}

// I17：module 不在证据集里就拒收；派生集合之外同样拒收（不许模型创建模块）。
func TestCommitRejectsModuleNeverReadInThisRun(t *testing.T) {
	root, snap, ev := foldFixture(t)
	r := req(
		moduleItem("internal/b", "purpose", "I never opened this module", "internal/b/b.go"),
		moduleItem("internal/nope", "purpose", "this module does not exist", "internal/nope/n.go"),
		moduleItem("internal/a", "purpose", "this one is fine", "internal/a/a.go"),
	)
	r.Evidence = ev
	res, err := Commit(root, snap, r)
	if err != nil {
		t.Fatalf("fold 失败: %v", err)
	}
	reasons := map[RejectReason]int{}
	for _, rej := range res.Rejected {
		reasons[rej.Reason]++
	}
	if reasons[RejectNoEvidence] != 1 {
		t.Errorf("证据集外模块没被拒收一条: %+v", res.Rejected)
	}
	if reasons[RejectUnknownMod] != 1 {
		t.Errorf("派生集合外模块没被拒收一条: %+v", res.Rejected)
	}
	if res.Added != 1 {
		t.Errorf("合法那条也没落地: %+v", res)
	}
	doc, _ := Load(root)
	if strings.Contains(doc.Body, "never opened") || strings.Contains(doc.Body, "does not exist") {
		t.Errorf("被拒收的散文写进了文档:\n%s", doc.Body)
	}
}

// 整轮一条证据都没有：仓库级段落也要求"真读过东西"，且**不写文件**。
func TestCommitWithoutAnyEvidenceWritesNothing(t *testing.T) {
	root, snap, _ := foldFixture(t)
	r := req(FoldItem{Section: "architecture", Claim: "guessed from the readme", Where: "internal/a/a.go"})
	r.Evidence = map[string]bool{} // 有这张表，但表是空的
	res, err := Commit(root, snap, r)
	if err != nil {
		t.Fatalf("fold 失败: %v", err)
	}
	if res.Added != 0 || len(res.Rejected) != 1 || res.Rejected[0].Reason != RejectNoReadAtAll {
		t.Errorf("没有一条拒收账目: %+v", res.Rejected)
	}
	if _, err := os.Stat(DocPath(root)); err == nil {
		t.Errorf("一条都没落地却写了文件 —— I18 要求\"没更新\"与\"更新失败\"可区分")
	}
}

// I18：五种以上的拒收情形各有一条能读出来的账。
func TestEveryRejectionReasonIsAccounted(t *testing.T) {
	root, snap, ev := foldFixture(t)
	long := strings.Repeat("x", MaxClaimRunes+1)
	r := req(
		FoldItem{Section: "nonsense", Claim: "unknown section"},
		FoldItem{Section: "gotchas", Claim: "   "},
		FoldItem{Section: "modules", Module: "internal/a", Claim: "missing field"},
		FoldItem{Section: "modules", Module: "internal/a", Field: "vibes", Claim: "unknown field"},
		FoldItem{Section: "gotchas", Claim: long},
		FoldItem{Section: "modules", Module: "internal/a", Field: "purpose", Claim: "way too long where", Where: long},
		FoldItem{Section: "modules", Module: "internal/a", Field: "purpose", Claim: "borrowed path internal/other/o.go lives elsewhere", Where: "internal/a/a.go"},
	)
	r.Evidence = ev
	res, err := Commit(root, snap, r)
	if err != nil {
		t.Fatalf("fold 失败: %v", err)
	}
	seen := map[RejectReason]bool{}
	for _, rej := range res.Rejected {
		if rej.Detail == "" {
			t.Errorf("一条拒收没有 detail，人读不出为什么: %+v", rej)
		}
		seen[rej.Reason] = true
	}
	for _, want := range []RejectReason{
		RejectSection, RejectEmptyClaim, RejectModuleField, RejectUnknownField,
		RejectTooLong, RejectPathForeign,
	} {
		if !seen[want] {
			t.Errorf("拒收原因 %q 没被这条夹具触发（I18 要求每种情形各有一条账）", want)
		}
	}
	if res.Added != 0 {
		t.Errorf("全坏的一条都没落地才对: added=%d", res.Added)
	}
}

// I14：邻居模块的路径可以出现在散文里，别的片不行。
func TestProsePathMustBelongToModule(t *testing.T) {
	snap, _ := Scan(foldProjectOf(t), DefaultOptions())
	// internal/b 依赖 internal/a：b 的散文里提 a 合法，提 other 不合法。
	if tok, bad := foreignPath("delegates into internal/a/a.go", "internal/b", snap); bad {
		t.Errorf("邻居模块的路径被判成外来（放行侧失效）: %q", tok)
	}
	if tok, bad := foreignPath("also reads internal/other/o.go", "internal/b", snap); !bad {
		t.Errorf("非邻居、非本片的文件路径被放行了（I14 的承重判据失效）")
	} else if tok != "internal/other/o.go" {
		t.Errorf("报出来的 token 不对: %q", tok)
	}
	// 本片自己的文件永远合法，即使显示列表被截断（判据走 dirIndex，不是 Files 前 24 条）。
	if _, bad := foreignPath("see internal/a/a.go", "internal/a", snap); bad {
		t.Errorf("本片自己的文件被判成外来")
	}
}

func foldProjectOf(t *testing.T) string {
	t.Helper()
	_, snap, _ := foldFixture(t)
	return snap.Root
}

// §7.5 第 5 条 + I19 的 fold 侧：锚点判过期**不拒收**，写进去并立刻标 stale。
func TestStaleAnchorIsWrittenNotRejected(t *testing.T) {
	root, snap, ev := foldFixture(t)
	r := req(
		moduleItem("internal/a", "purpose", "points at a line that is gone", "internal/a/a.go:999"),
		moduleItem("internal/a", "data_flow", "has no anchor at all", ""),
	)
	r.Evidence = ev
	res, err := Commit(root, snap, r)
	if err != nil {
		t.Fatalf("fold 失败: %v", err)
	}
	if len(res.Rejected) != 0 {
		t.Fatalf("锚点过期被当成拒收了: %+v", res.Rejected)
	}
	if res.Stale != 1 || res.Unanchored != 1 {
		t.Errorf("stale/unanchored 账目不对: %+v", res)
	}
	doc, _ := Load(root)
	if !strings.Contains(doc.Body, "state:stale") || !strings.Contains(doc.Body, "state:unanchored") {
		t.Errorf("状态没落进文档:\n%s", doc.Body)
	}
}

// §7.4 第 4 条：同 module+field 的非空新值覆盖，空值不抹掉上一次的内容。
func TestSameFieldOverwriteOnlyWhenNonEmpty(t *testing.T) {
	root, snap, ev := foldFixture(t)
	first := req(moduleItem("internal/a", "purpose", "the original statement", "internal/a/a.go"))
	first.Evidence = ev
	if _, err := Commit(root, snap, first); err != nil {
		t.Fatalf("fold 失败: %v", err)
	}
	doc, _ := Load(root)
	before := doc.Body

	second := req(moduleItem("internal/a", "purpose", "a corrected statement", "internal/a/a.go"))
	second.Evidence = ev
	res, err := Commit(root, snap, second)
	if err != nil {
		t.Fatalf("fold 失败: %v", err)
	}
	if res.Updated != 1 || res.Added != 0 {
		t.Errorf("同字段覆盖没走 updated 分支: %+v", res)
	}
	doc, _ = Load(root)
	if strings.Contains(doc.Body, "the original statement") {
		t.Errorf("新值没覆盖旧值:\n%s", doc.Body)
	}
	if !strings.Contains(doc.Body, "a corrected statement") {
		t.Errorf("新值丢了:\n%s", doc.Body)
	}
	if strings.Count(doc.Body, "## Modules/internal/a") != strings.Count(before, "## Modules/internal/a") {
		t.Errorf("同字段覆盖多出了一个段落（槽位规则破了）")
	}
	// 空 claim 在 validate 就被丢了，所以"空字段抹掉上一次内容"这条路径不可达 ——
	// 反过来验一次：带空 claim 的提交之后，旧内容还在。
	third := req(moduleItem("internal/a", "purpose", "  ", "internal/a/a.go"))
	third.Evidence = ev
	res, err = Commit(root, snap, third)
	if err != nil {
		t.Fatalf("fold 失败: %v", err)
	}
	if res.Added+res.Updated != 0 {
		t.Errorf("空 claim 竟然落地了: %+v", res)
	}
	doc, _ = Load(root)
	if !strings.Contains(doc.Body, "a corrected statement") {
		t.Errorf("空提交把上一次的内容抹掉了:\n%s", doc.Body)
	}
}

// §7.4 第 3 条：supersedes 指认错 ID 时不报错也不假装成功，新条目照写。
func TestSupersedesUnknownIdDropsAccountingNotClaim(t *testing.T) {
	root, snap, ev := foldFixture(t)
	r := req(moduleItem("internal/a", "purpose", "a fresh correct statement", "internal/a/a.go"))
	r.Items[0].Supersedes = []string{"ffff"}
	r.Evidence = ev
	res, err := Commit(root, snap, r)
	if err != nil {
		t.Fatalf("fold 失败: %v", err)
	}
	if res.DroppedSupersedes != 1 {
		t.Errorf("指认不存在的旧 ID 没记账: %+v", res)
	}
	if res.Added != 1 {
		t.Errorf("新条目被一起丢掉了: %+v", res)
	}
	doc, _ := Load(root)
	if !strings.Contains(doc.Body, "a fresh correct statement") {
		t.Errorf("新条目没写进去:\n%s", doc.Body)
	}
}

// 取代路径的正例：先提交、再用真 ID 指认，旧条目该被摘掉。
func TestSupersedesRemovesOldAtom(t *testing.T) {
	root, snap, ev := foldFixture(t)
	first := req(moduleItem("internal/a", "purpose", "the old statement", "internal/a/a.go"))
	first.Evidence = ev
	if _, err := Commit(root, snap, first); err != nil {
		t.Fatalf("fold 失败: %v", err)
	}
	doc, _ := Load(root)
	id := atomIDIn(doc.Body, "the old statement")
	if id == "" {
		t.Fatalf("没找到旧条目的短 ID:\n%s", doc.Body)
	}

	second := req(FoldItem{Section: "gotchas", Claim: "the new statement", Where: "internal/a/a.go", Supersedes: []string{id}})
	second.Evidence = ev
	res, err := Commit(root, snap, second)
	if err != nil {
		t.Fatalf("fold 失败: %v", err)
	}
	if res.Supersede != 1 {
		t.Errorf("取代没记账: %+v", res)
	}
	doc, _ = Load(root)
	if strings.Contains(doc.Body, "the old statement") {
		t.Errorf("旧条目还活着（矛盾并存）:\n%s", doc.Body)
	}
}

// commitBatches 按 schema 的 maxItems 分批 fold。
//
// 配额类断言必须**多批**驱动：一次提交最多 8 条（§7.2 的界，且由
// TestSubmissionLimitsAreEnforced 钉住执行），所以"段里长到超过 40 条"
// 这件事在真实世界里只能是一次次 fold 攒出来的。单批塞 45 条的夹具
// 测的是一条生产走不到的路。
func commitBatches(t *testing.T, root string, snap *Snapshot, ev map[string]bool, items []FoldItem) *FoldResult {
	t.Helper()
	var last FoldResult
	for start := 0; start < len(items); start += MaxSubmitItems {
		end := start + MaxSubmitItems
		if end > len(items) {
			end = len(items)
		}
		r := req(items[start:end]...)
		r.Evidence = ev
		res, err := Commit(root, snap, r)
		if err != nil {
			t.Fatalf("第 %d 批 fold 失败: %v", start/MaxSubmitItems+1, err)
		}
		last = *res
	}
	return &last
}

// §7.4 第 6 条：单段配额 + 淘汰必须写进渲染出的 Coverage 行。
func TestSectionQuotaEvictsAndAccounts(t *testing.T) {
	root, snap, ev := foldFixture(t)
	var items []FoldItem
	for i := 0; i < MaxAtomsPerSection+5; i++ {
		items = append(items, FoldItem{
			Section: "gotchas",
			Claim:   fmt.Sprintf("gotcha number %02d with distinct text", i),
			Where:   "internal/a/a.go",
		})
	}
	res := commitBatches(t, root, snap, ev, items)
	if res.Evicted != 5 {
		t.Errorf("配额淘汰数 = %d, 要 5", res.Evicted)
	}
	doc, _ := Load(root)
	if doc.Front.Evicted != 5 {
		t.Errorf("front-matter 没留淘汰账目: %d", doc.Front.Evicted)
	}
	blocks, _ := doc.Sections()
	for _, b := range blocks {
		if b.Heading == "Gotchas" && len(b.Atoms) > MaxAtomsPerSection {
			t.Errorf("段里还留着 %d 条，超过配额 %d", len(b.Atoms), MaxAtomsPerSection)
		}
	}
	// Coverage 行是"淘汰过"唯一的可见处。
	cover := strings.Join(coverageLines(snap, doc), "\n")
	if !strings.Contains(cover, "evicted 5 atoms") {
		t.Errorf("Coverage 没把淘汰说出来（静默少一片）:\n%s", cover)
	}
}

// 配额淘汰的**判据**：优先保 ok、最旧的先出。
// 上一条测试只数了条数 —— 而"排反了"的失效形态恰好是条数正确、丢的是好条目。
func TestSectionQuotaEvictsTheRightAtoms(t *testing.T) {
	root, snap, ev := foldFixture(t)
	var items []FoldItem
	// 38 条锚点成立的新原子（state ok）。
	for i := 0; i < 38; i++ {
		items = append(items, FoldItem{
			Section: "gotchas",
			Claim:   fmt.Sprintf("solid claim number %02d", i),
			Where:   "internal/a/a.go",
		})
	}
	// 4 条锚点指向不存在的文件（fold 时判成 stale），再加 3 条没锚点的（unanchored）。
	for i := 0; i < 4; i++ {
		items = append(items, FoldItem{
			Section: "gotchas",
			Claim:   fmt.Sprintf("anchor is gone number %02d", i),
			Where:   "internal/a/vanished.go",
		})
	}
	for i := 0; i < 3; i++ {
		items = append(items, FoldItem{
			Section: "gotchas",
			Claim:   fmt.Sprintf("no anchor at all number %02d", i),
		})
	}
	res := commitBatches(t, root, snap, ev, items)
	if res.Evicted != 5 {
		t.Fatalf("淘汰数 = %d, 要 5（总数 45、配额 40）", res.Evicted)
	}
	doc, _ := Load(root)
	blocks, _ := doc.Sections()
	var kept []Atom
	for _, b := range blocks {
		if b.Heading == "Gotchas" {
			kept = b.Atoms
		}
	}
	if len(kept) != MaxAtomsPerSection {
		t.Fatalf("段里剩 %d 条", len(kept))
	}
	// 淘汰顺序：38 条 ok 全留 → 剩 2 个格子给 unanchored（3 条里挤掉 1 条）→ 4 条 stale 全出。
	var okKept, unanchoredKept, staleKept int
	for _, a := range kept {
		switch a.State {
		case StateOK:
			okKept++
		case StateUnanchored:
			unanchoredKept++
		case StateStale:
			staleKept++
		}
	}
	if okKept != 38 {
		t.Errorf("ok 条目被挤掉了: 留下 %d, 要 38（配额判据是优先保 ok）", okKept)
	}
	if staleKept != 0 {
		t.Errorf("锚点已失效的条目留了 %d 条，而没锚点的反而被挤掉（state 优先级排反了）", staleKept)
	}
	if unanchoredKept != 2 {
		t.Errorf("unanchored 留下 %d 条, 要 2（40 格 - 38 条 ok）", unanchoredKept)
	}
	if !strings.Contains(doc.Body, "solid claim number 00") {
		t.Errorf("最早写入的 ok 条目被挤掉了（seen 相同时应按 ID 稳定序保留，而不是按写入序）")
	}
}

func atomIDIn(body, claim string) string {
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, claim) {
			rest := strings.TrimPrefix(strings.TrimSpace(line), "- (")
			if id, _, ok := strings.Cut(rest, ")"); ok {
				return id
			}
		}
	}
	return ""
}

// schema 写了 maxItems=8 / supersedes 最多 4，就必须有人执行（§11 坑 25：写了≠生效）。
// 只测"代码里有这个常数"不够 —— 要测**超限的那几条真的不落地，而且各留一条账**。
func TestSubmissionLimitsAreEnforced(t *testing.T) {
	root, snap, ev := foldFixture(t)

	var items []FoldItem
	for i := 0; i < MaxSubmitItems+2; i++ {
		items = append(items, FoldItem{
			Section: "gotchas", Claim: fmt.Sprintf("claim %02d beyond the cap", i),
			Where:      "internal/a/a.go",
			Supersedes: []string{"a", "b", "c", "d", "e"},
		})
	}
	r := req(items...)
	r.Evidence = ev
	res, err := Commit(root, snap, r)
	if err != nil {
		t.Fatalf("fold 失败: %v", err)
	}
	if res.Added != MaxSubmitItems {
		t.Errorf("落地的条数 = %d, 要恰好 %d（maxItems 没人执行）", res.Added, MaxSubmitItems)
	}
	var overflow int
	for _, rej := range res.Rejected {
		if rej.Reason == RejectOverflow {
			overflow++
		}
	}
	// 一条"整批溢出"的账 + 每条 supersedes 超限各一条（落地的那 8 条）。
	if overflow != 1+MaxSubmitItems {
		t.Errorf("超限账目 = %d, 要 %d（第 9 条一条 + 落地 8 条各一条）", overflow, 1+MaxSubmitItems)
	}
	doc, _ := Load(root)
	if strings.Contains(doc.Body, "claim 09") {
		t.Errorf("第 9 条越界提交还是写进去了:\n%s", doc.Body)
	}
}
