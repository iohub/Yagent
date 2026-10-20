package blueprint

import (
	"testing"
	"time"
)

// 本文件钉的是 2026-10-19 22:18 那次生产事故的另一半：
// 写通道跑到了、三条提交交了、`where` 都是真路径，而 I14 把三条全判成"散文里的外来路径"
// —— 因为 looksLikePath 对任何含 `/` 的 token 都返回 true，而中文技术散文里的斜杠
// 十有八九是"Name/Brief/Effect" 这类标识符列表，不是文件。
// 三条全拒 ⇒ 一次 fold 都没发生 ⇒ blueprint.md 从未出现，唯一痕迹是两行 WARN。

// foreignPathFixture 给 foldFixture 的仓库补一个与 internal/a 同名的文件，
// 于是"裸文件名"既有唯一归属的形态、也有多处同名的形态可测。
func foreignPathFixture(t *testing.T) (storeRoot string, snap *Snapshot, fs *AnchorChecker) {
	t.Helper()
	storeRoot, snap, _ = foldFixture(t)
	project := snap.Root
	write(t, project, "internal/b/a.go", "package b\n\nfunc Same() {}\n")
	return storeRoot, snap, NewAnchorChecker(project)
}

// 形一：散文里斜杠分隔的标识符列表不是路径断言。
func TestProseSlashListIsNotAPathAssertion(t *testing.T) {
	_, snap, fs := foreignPathFixture(t)
	claims := []string{
		"Spec 结构体包含 Name/Brief/Effect/Frequency/Mutates/Serial/Available/Describe/Build 等字段",
		"Surface 支持 Cold/Hot 分级 deferred schema",
	}
	for _, claim := range claims {
		if tok, bad := foreignPath(claim, "internal/a", snap, fs); bad {
			t.Errorf("标识符列表被判成外来路径: token=%q claim=%q", tok, claim)
		}
	}
}

// 形二：写短了的前缀与裸文件名 —— 真身在本片/邻居时必须认得（假阳性），
// 真身明确在别处时仍然要拒（真阳性不许被这次改动带走）。
func TestProseShortenedPathsResolveToRealOwner(t *testing.T) {
	_, snap, fs := foreignPathFixture(t)
	accept := []struct{ claim, module string }{
		{"other/o.go 是唯一没被依赖的片", "internal/other"},
		{"b.go 里声明了入口", "internal/b"},
		{"a.go 与 b.go 之间只有一条边", "internal/b"}, // 邻居（b→a）的裸文件名
	}
	for _, c := range accept {
		if tok, bad := foreignPath(c.claim, c.module, snap, fs); bad {
			t.Errorf("真属于本片/邻居的短路径被拒收: token=%q module=%s", tok, c.module)
		}
	}
	if tok, bad := foreignPath("顺路看一眼 other/o.go", "internal/b", snap, fs); !bad {
		t.Errorf("裸文件名指向别处却放行了（I14 真阳性失效）: %q", tok)
	} else if tok != "other/o.go" {
		t.Errorf("报名不对: %q", tok)
	}
}

// 形三：同名散在多片 ⇒ 判不了，不谎称违规（也绝不谎称验过）。
func TestProseAmbiguousBareNameIsNotJudged(t *testing.T) {
	_, snap, fs := foreignPathFixture(t)
	// a.go 同时在 internal/a 与 internal/b（foreignPathFixture 造的那个），
	// 从没有边关系的 internal/other 提它，两处都对不上 ⇒ 弃权而不是拒收。
	if tok, bad := foreignPath("a.go 里写着阈值", "internal/other", snap, fs); bad {
		t.Errorf("同名多处的裸文件名被判成外来: token=%q", tok)
	}
}

// 形四：仓库里查无此物的路径不由这道管 —— 它是 `where` 锚点的活。
// （`../x` 到了这里与凭空路径同形：pathTokens 把首尾的 `/` 和 `.` 都剪掉了。）
func TestProseInventedPathLeavesExistenceToAnchorCheck(t *testing.T) {
	_, snap, fs := foreignPathFixture(t)
	if tok, bad := foreignPath("见 internal/ghost/ghost.go", "internal/a", snap, fs); bad {
		t.Errorf("无法归属也不存在的路径被判成外来（I14 管不着存在性）: token=%q", tok)
	}
	// 兜底必须真的在：同一条凭空路径写进 where 时锚点判 stale，不是放行一切。
	if got := fs.Check("internal/ghost/ghost.go").State; got != StateStale {
		t.Errorf("凭空路径作为 where 时应判 stale，实际 %q", got)
	}
}

// 承重的一条：生产那三条提交原样进 Commit，必须出现文件。
// 断言用的是"拒收为零 + 文档落地"，不是某一行的措辞 —— 那才是当时失效的东西。
func TestCommitAcceptsProductionShapedProse(t *testing.T) {
	storeRoot, snap, _ := foreignPathFixture(t)
	items := []FoldItem{
		{Section: "modules", Module: "internal/a", Field: "purpose",
			Claim: "Spec 结构体包含 Name/Brief/Effect/Frequency/Mutates/Serial/Available/Describe/Build 等字段，registry 为进程级单一注册表",
			Where: "internal/a/a.go"},
		{Section: "modules", Module: "internal/b", Field: "purpose",
			Claim: "支持 Cold/Hot 分级 deferred schema，声明在 b.go",
			Where: "internal/b/b.go"},
	}
	res, err := Commit(storeRoot, snap, FoldRequest{
		Items: items, Src: "repo_agent@prose", Now: time.Now().UTC(),
		Evidence:  map[string]bool{"internal/a": true, "internal/b": true},
		ProjectID: "probe", ProjectPath: snap.Root,
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	for _, rej := range res.Rejected {
		t.Errorf("生产形态的散文不该被拒收: reason=%s module=%s detail=%s", rej.Reason, rej.Module, rej.Detail)
	}
	if res.Added != 2 {
		t.Errorf("两条都该落地: added=%d noop=%v", res.Added, res.NoOp)
	}
	if res.NoOp {
		t.Errorf("fold 过了就不该判成 NoOp")
	}
}
