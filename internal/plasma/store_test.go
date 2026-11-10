package plasma_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yagent/internal/plasma"
)

func newStore(t *testing.T) (*plasma.Store, string) {
	t.Helper()
	root := t.TempDir()
	return plasma.NewStore(root), root
}

func TestStore_PutLoadRoundTrip(t *testing.T) {
	s, _ := newStore(t)
	b := cleanBundle()
	h1, err := s.Put(b, nil)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if len(h1) != 64 {
		t.Errorf("hash 长度 = %d, want 64（sha256 hex）", len(h1))
	}
	// hash 必须稳定：同一份 IR 两次得到同一个主键。
	h2, err := plasma.HashBundle(b)
	if err != nil || h2 != h1 {
		t.Errorf("HashBundle 不稳定: %s vs %s (%v)", h1, h2, err)
	}
	got, meta, err := s.Load(b.Name, h1)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Name != b.Name || got.Prompt.System != b.Prompt.System {
		t.Errorf("round-trip 漂移: %+v", got)
	}
	if meta.Grade != plasma.GradeProvisional {
		t.Errorf("grade = %q, want provisional", meta.Grade)
	}
	if cur, ok := s.Current(b.Name); !ok || cur != h1 {
		t.Errorf("index 未指向新 hash: %q ok=%v", cur, ok)
	}
}

// 不可变：同一个 {name,hash} 再 Put 不得覆盖（夹具与 bundle 一经落盘即不可变）。
func TestStore_PutIsIdempotent(t *testing.T) {
	s, root := newStore(t)
	b := cleanBundle()
	h, err := s.Put(b, nil)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, b.Name, h, "meta.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// 第二次带一份"看起来更高"的报告，也不能覆盖已有目录。
	h2, err := s.Put(b, &plasma.Report{Grade: plasma.GradePromoted})
	if err != nil {
		t.Fatal(err)
	}
	if h2 != h {
		t.Errorf("同一份 bundle 得到不同 hash: %s vs %s", h, h2)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("重复 Put 覆盖了已有 meta —— bundle 与夹具必须不可变")
	}
}

// 改夹具 ⇒ 新 hash ⇒ 新目录 ⇒ grade 从 provisional 重新开始（不继承）。
func TestStore_FixtureChangeStartsFreshGrade(t *testing.T) {
	s, _ := newStore(t)
	b := cleanBundle()
	b.Fixtures = &plasma.Fixtures{
		Provenance: plasma.Provenance{Author: "compiler", Lineage: "independent-1"},
		Structural: []plasma.Fixture{{ID: "s1", Input: "x"}},
	}
	h1, err := s.Put(b, &plasma.Report{Grade: plasma.GradeVerified})
	if err != nil {
		t.Fatal(err)
	}
	// 模拟"这个版本已经升到 promoted"
	if err := s.Promote(b.Name, h1); err != nil {
		t.Fatal(err)
	}
	b.Fixtures.Structural = append(b.Fixtures.Structural, plasma.Fixture{ID: "s2", Input: "y"})
	h2, err := s.Put(b, nil)
	if err != nil {
		t.Fatal(err)
	}
	if h2 == h1 {
		t.Fatal("改了夹具却得到同一个 hash")
	}
	_, meta, err := s.Load(b.Name, h2)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Grade != plasma.GradeProvisional {
		t.Errorf("新 hash 的 grade = %q, want provisional（不得继承旧 grade）", meta.Grade)
	}
	if meta.Runs != 0 {
		t.Errorf("新 hash 的运行计数 = %d, want 0", meta.Runs)
	}
}

// 出题谱系分离：同一 lineage 自己出题自己考（Goodhart）→ store 拒收。
func TestStore_RejectsSameLineageFixtures(t *testing.T) {
	s, _ := newStore(t)
	b := cleanBundle()
	b.Lineage = &plasma.Lineage{Mutation: "prompt:tweak"}
	b.Fixtures = &plasma.Fixtures{
		Provenance: plasma.Provenance{Author: "evolver", Lineage: "prompt:tweak"},
		Structural: []plasma.Fixture{{ID: "s1", Input: "x"}},
	}
	if _, err := s.Put(b, nil); !errors.Is(err, plasma.ErrFixtureProvenance) {
		t.Fatalf("同谱系出题应被拒，得到 %v", err)
	}

	// provenance 为空也不行：无法证明分离就拒收。
	b.Fixtures.Provenance.Lineage = ""
	if _, err := s.Put(b, nil); !errors.Is(err, plasma.ErrFixtureProvenance) {
		t.Fatalf("空 provenance 应被拒，得到 %v", err)
	}
}

// 夹具不可变（ADR-5 第一条防线）：同 hash 目录里已有 bundle.json 且字节不一致 ⇒ 拒写。
//
// hash 是"归一化 IR 的 sha256"，落盘的是 MarshalIndent 的字节；两者正常同源，
// 但一旦有人在中间做了归一化行为变更（或手改了那目录），"hash 相同"就不再等价于
// "内容相同"。此处的复现方式就是后者：磁盘被改过一个字节，而内存里的 b 没动。
func TestStore_RejectsBundleThatDiffersUnderSameHash(t *testing.T) {
	s, root := newStore(t)
	b := cleanBundle()
	h, err := s.Put(b, nil)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	path := filepath.Join(root, b.Name, h, "bundle.json")
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// 仍然合法 JSON，只是字节与本次要写的不一致（比"改坏文件"更接近真实事故：
	// 有人在同目录里手改了一个字段值）。
	mutated := strings.Replace(string(onDisk), "{", "{ ", 1)
	if mutated == string(onDisk) {
		t.Fatal("改写没有生效，用例前提不成立")
	}
	if err := os.WriteFile(path, []byte(mutated), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Put(b, nil); !errors.Is(err, plasma.ErrImmutable) {
		t.Fatalf("同 hash 下字节不一致必须拒写（否则夹具不可变性被绕过），得到 %v", err)
	} else if !strings.Contains(err.Error(), plasma.CodeStoreImmutable) {
		t.Errorf("拒写的错误里要带 %s（可 grep、可断言），得到: %v", plasma.CodeStoreImmutable, err)
	}
}

// 磁盘被改一个字节 ⇒ 加载期 hash 校验失败（防篡改）。
func TestStore_DetectsTampering(t *testing.T) {
	s, root := newStore(t)
	b := cleanBundle()
	h, err := s.Put(b, nil)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, b.Name, h, "bundle.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// 改一个字节（把 name 里的一个字母换掉，保持 JSON 合法）
	tampered := []byte(string(raw))
	for i := range tampered {
		if tampered[i] == 's' { // security_auditor 的首字母
			tampered[i] = 'x'
			break
		}
	}
	if err := os.WriteFile(path, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Load(b.Name, h); !errors.Is(err, plasma.ErrTampered) {
		t.Fatalf("篡改应被检出，得到 %v", err)
	}
}

func TestStore_ListIsSortedAndPromoteMovesPointer(t *testing.T) {
	s, _ := newStore(t)
	names := []string{"zebra_agent", "alpha_agent", "mike_agent"}
	hashes := map[string]string{}
	for _, n := range names {
		b := cleanBundle()
		b.Name = n
		b.DisplayName = n
		h, err := s.Put(b, nil)
		if err != nil {
			t.Fatal(err)
		}
		hashes[n] = h
	}
	entries := s.List()
	if len(entries) != 3 {
		t.Fatalf("List 条数 = %d, want 3", len(entries))
	}
	want := []string{"alpha_agent", "mike_agent", "zebra_agent"}
	for i, e := range entries {
		if e.Name != want[i] {
			t.Errorf("List[%d] = %q, want %q（必须字典序，前缀缓存靠它）", i, e.Name, want[i])
		}
	}
	// Promote 到一个新的 hash（这里用另一个 name 的目录不存在，应报错）
	if err := s.Promote("alpha_agent", "deadbeef"); err == nil {
		t.Error("promote 到不存在的 hash 应报错")
	}
	if cur, _ := s.Current("alpha_agent"); cur != hashes["alpha_agent"] {
		t.Errorf("失败的 promote 改动了指针: %s", cur)
	}
}

// 升级批准按 hash 记：新 hash 没有这条记录，批准自然失效。
func TestStore_EscalationApprovalIsPerHash(t *testing.T) {
	s, _ := newStore(t)
	b := cleanBundle()
	h1, err := s.Put(b, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ApproveEscalation(b.Name, h1); err != nil {
		t.Fatalf("ApproveEscalation: %v", err)
	}
	_, meta1, _ := s.Load(b.Name, h1)
	if !meta1.EscalationApproved {
		t.Error("批准没落盘")
	}
	// 改一个字节产生新 hash：批准不得继承。
	b.Prompt.System += "\nBe careful."
	h2, err := s.Put(b, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, meta2, _ := s.Load(b.Name, h2)
	if meta2.EscalationApproved {
		t.Error("新 hash 继承了旧的升级批准 —— 必须按 hash 记")
	}
}

func TestStore_GCRemovesUnreferencedOldDirs(t *testing.T) {
	s, root := newStore(t)
	b := cleanBundle()
	h1, err := s.Put(b, nil)
	if err != nil {
		t.Fatal(err)
	}
	b.Prompt.System += "\nv2"
	h2, err := s.Put(b, nil) // index 现在指向 h2，h1 变成孤儿
	if err != nil {
		t.Fatal(err)
	}
	// 未到期：不删
	if n, err := s.GC(time.Now(), time.Hour); err != nil || n != 0 {
		t.Fatalf("未到期不该删: n=%d err=%v", n, err)
	}
	// 把孤儿目录的 mtime 推老
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Join(root, b.Name, h1), old, old); err != nil {
		t.Fatal(err)
	}
	n, err := s.GC(time.Now(), time.Hour)
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if n != 1 {
		t.Errorf("删除数 = %d, want 1", n)
	}
	if _, err := os.Stat(filepath.Join(root, b.Name, h1, "bundle.json")); !os.IsNotExist(err) {
		t.Error("孤儿目录没被删")
	}
	if _, err := os.Stat(filepath.Join(root, b.Name, h2, "bundle.json")); err != nil {
		t.Error("被 index 指向的目录被误删了")
	}
	// ttl=0 是"不清理"
	if n, err := s.GC(time.Now(), 0); err != nil || n != 0 {
		t.Errorf("ttl=0 时不该清理: n=%d err=%v", n, err)
	}
}

// SetGrade 是 promote 的唯一写口。三条都必须钉住：写完能读回、hash 不存在时报错、
// 写完 index.json 仍可解析（原子写的意义）。
func TestStore_SetGrade(t *testing.T) {
	s, root := newStore(t)
	b := cleanBundle()
	h, err := s.Put(b, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.SetGrade(b.Name, h, plasma.GradePromoted); err != nil {
		t.Fatalf("SetGrade: %v", err)
	}
	if _, meta, err := s.Load(b.Name, h); err != nil {
		t.Fatalf("Load: %v", err)
	} else if meta.Grade != plasma.GradePromoted {
		t.Errorf("grade = %q, want promoted", meta.Grade)
	}

	// 不存在的 hash：必须报错，且不能顺手建出目录（否则 store 里会多一份
	// 没有 bundle.json 的幽灵条目，而 List 只读 index、看不见它）。
	missing := filepath.Join(root, b.Name, "deadbeef")
	if err := s.SetGrade(b.Name, "deadbeef", plasma.GradePromoted); err == nil {
		t.Error("对不存在的 hash 调 SetGrade 却成功了")
	}
	if _, statErr := os.Stat(missing); !os.IsNotExist(statErr) {
		t.Error("SetGrade 为不存在的 hash 建了目录")
	}

	// 非法档位必须被拒（空串尤其危险：它会静默把 grade 清空）。
	if err := s.SetGrade(b.Name, h, plasma.Grade("")); err == nil {
		t.Error("SetGrade 接受了空档位")
	}
	if _, meta, err := s.Load(b.Name, h); err != nil {
		t.Fatal(err)
	} else if meta.Grade != plasma.GradePromoted {
		t.Errorf("失败的 SetGrade 改了 grade: %q", meta.Grade)
	}

	// 原子写：index.json 仍是合法 JSON 且仍指向这个 hash。
	raw, err := os.ReadFile(filepath.Join(root, "index.json"))
	if err != nil {
		t.Fatalf("读 index.json: %v", err)
	}
	var idx map[string]string
	if err := json.Unmarshal(raw, &idx); err != nil {
		t.Fatalf("index.json 被写坏: %v\n%s", err, raw)
	}
	if idx[b.Name] != h {
		t.Errorf("index[%s] = %q, want %q", b.Name, idx[b.Name], h)
	}
}
