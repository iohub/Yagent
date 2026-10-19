package blueprint

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件钉 §5.3 的第 2、3 条：老化只碰"已经没人能看见的段落"，
// 而且**删错了必须有 Undo**（整档先落进 revisions/）。

var gcNow = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

func seenAt(age time.Duration) string {
	return gcNow.Add(-age).Format(time.RFC3339)
}

func gcFixture(t *testing.T) (string, *Snapshot) {
	t.Helper()
	root := t.TempDir()
	snap := testSnapshot(testLeaf("internal/a", 2))
	body := strings.Join([]string{
		"## Architecture",
		"- (a0) one repo claim <!--id:a0 where:internal/a/a.go seen:" + seenAt(200*24*time.Hour) + " state:ok-->",
		"## Modules/internal/a",
		// 在派生集合里的片：再老也不许动 —— 老化清理的对象是孤儿，不是"旧"。
		"- (a1) purpose: still here <!--id:a1 field:purpose where:internal/a/a.go seen:" + seenAt(200*24*time.Hour) + " state:ok-->",
		"## Modules/internal/gone",
		"- (g1) purpose: deleted directory <!--id:g1 field:purpose where:internal/gone/g.go seen:" + seenAt(200*24*time.Hour) + " state:ok-->",
		"## Modules/internal/fresh",
		"- (f1) purpose: orphan but recent <!--id:f1 field:purpose where:internal/fresh/f.go seen:" + seenAt(1*24*time.Hour) + " state:ok-->",
	}, "\n")
	doc := &Doc{
		Front: Front{
			Version:     docSchemaVersion,
			ProjectID:   "proj",
			ProjectPath: "/repo",
			ModuleFingerprints: map[string]string{
				"internal/a": "fa", "internal/gone": "fg",
			},
		},
		Body: body,
	}
	if err := Save(root, doc); err != nil {
		t.Fatalf("夹具落盘失败: %v", err)
	}
	return root, snap
}

func TestAgeOutOrphansRemovesOnlyExpiredOrphans(t *testing.T) {
	root, snap := gcFixture(t)

	removed, err := AgeOutOrphans(root, snap, DefaultOrphanTTL, gcNow)
	if err != nil {
		t.Fatalf("老化清理失败: %v", err)
	}
	if strings.Join(removed, ",") != "internal/gone" {
		t.Errorf("摘除集合不对: %v, 要只剩 internal/gone", removed)
	}

	doc, err := Load(root)
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	body := doc.Body
	if strings.Contains(body, "internal/gone") {
		t.Errorf("到期的孤儿段落没被摘除:\n%s", body)
	}
	if !strings.Contains(body, "still here") {
		t.Errorf("派生集合内的片被连带删了（I13 的老化只碰孤儿）:\n%s", body)
	}
	if !strings.Contains(body, "orphan but recent") {
		t.Errorf("未到期的孤儿被提前删了:\n%s", body)
	}
	// 仓库级段落与它的老原子都不该被碰 —— 它们不属于 Modules/ 前缀段。
	if !strings.Contains(body, "one repo claim") {
		t.Errorf("Architecture 段被连带删了:\n%s", body)
	}
	// 指纹表里孤儿那条一起清掉：留着等于把"这片曾经存在"写进唯一还有它痕迹的地方。
	if _, ok := doc.Front.ModuleFingerprints["internal/gone"]; ok {
		t.Errorf("被删段落的路径指纹留在了 front-matter 里")
	}
	if _, ok := doc.Front.ModuleFingerprints["internal/a"]; !ok {
		t.Errorf("留下片段的指纹被误删: %+v", doc.Front.ModuleFingerprints)
	}

	// §5.3 第 3 条：摘除前整档已经在 revisions/ —— 这就是"删错了"的 Undo。
	revs := ListRevisions(root)
	if len(revs) != 1 {
		t.Fatalf("老化清理前没落整档快照: %v", revs)
	}
	old, err := os.ReadFile(revs[0])
	if err != nil {
		t.Fatalf("读归档失败: %v", err)
	}
	if !strings.Contains(string(old), "deleted directory") {
		t.Errorf("归档里没有被删的那一段，Undo 无从谈起")
	}
	if doc.Front.UpdatedAt == "" {
		t.Errorf("改动没更新时间戳")
	}
}

// 幂等：跑第二次不该再删任何东西，也不该再多写一份归档。
func TestAgeOutOrphansIsIdempotent(t *testing.T) {
	root, snap := gcFixture(t)
	if _, err := AgeOutOrphans(root, snap, DefaultOrphanTTL, gcNow); err != nil {
		t.Fatalf("第一次老化失败: %v", err)
	}
	before, err := os.ReadFile(DocPath(root))
	if err != nil {
		t.Fatalf("读档失败: %v", err)
	}
	removed, err := AgeOutOrphans(root, snap, DefaultOrphanTTL, gcNow)
	if err != nil {
		t.Fatalf("第二次老化失败: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("第二次仍在删: %v", removed)
	}
	after, err := os.ReadFile(DocPath(root))
	if err != nil {
		t.Fatalf("读档失败: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("第二次跑改写了文件字节（幂等破了）:\n%s\n---\n%s", before, after)
	}
	if n := len(ListRevisions(root)); n != 1 {
		t.Errorf("空跑也写了一份归档: %d 份", n)
	}
}

// ttl<=0 = 关掉（与 [blackboard] 那套存在性判据同形）。
func TestAgeOutDisabledWithNonPositiveTTL(t *testing.T) {
	root, snap := gcFixture(t)
	removed, err := AgeOutOrphans(root, snap, 0, gcNow)
	if err != nil || removed != nil {
		t.Errorf("ttl=0 时不该做任何事: removed=%v err=%v", removed, err)
	}
	doc, _ := Load(root)
	if !strings.Contains(doc.Body, "internal/gone") {
		t.Errorf("关掉了却把孤儿删了")
	}
}

// 还没有蓝图的仓库：不是错误，也不该凭空长出一个 revisions/。
func TestAgeOutWithNoDocument(t *testing.T) {
	root := filepath.Join(t.TempDir(), "blueprint")
	removed, err := AgeOutOrphans(root, testSnapshot(testLeaf("a", 1)), DefaultOrphanTTL, gcNow)
	if err != nil {
		t.Fatalf("没有文档不该报错: %v", err)
	}
	if removed != nil {
		t.Errorf("没有文档却报摘除了: %v", removed)
	}
	if _, err := os.Stat(RevisionsDir(root)); err == nil {
		t.Errorf("空跑却建了 revisions/ 目录")
	}
}

// 归档滚动到最新 RevisionsKeep 份。
func TestSaveRevisionRollsToKeep(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < RevisionsKeep+3; i++ {
		data := []byte("generation " + string(rune('A'+i)))
		if _, err := SaveRevision(root, data, gcNow.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatalf("写归档失败: %v", err)
		}
	}
	revs := ListRevisions(root)
	if len(revs) != RevisionsKeep {
		t.Fatalf("滚动没生效: %d 份, 要 %d", len(revs), RevisionsKeep)
	}
	// 留下的必须是最新那批（文件名是定宽 UTC，字典序即时间序）。
	last, err := os.ReadFile(revs[len(revs)-1])
	if err != nil {
		t.Fatalf("读最新归档失败: %v", err)
	}
	if string(last) != "generation "+string(rune('A'+RevisionsKeep+2)) {
		t.Errorf("最新的归档被删掉了，实得 %q", last)
	}
}

// 归档只删自己写的普通文件，不碰符号链接（形状照 blackboard/gc.go：Lstat 判定）。
// 夹具让那个链接的文件名排在**最旧**的位置，于是"跳过链接"这条分支必然被走到。
func TestPruneRevisionsSkipsSymlinks(t *testing.T) {
	dir := RevisionsDir(t.TempDir())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("夹具目录失败: %v", err)
	}
	outside := t.TempDir()
	target := filepath.Join(outside, "keepme.md")
	if err := os.WriteFile(target, []byte("user data"), 0o600); err != nil {
		t.Fatalf("夹具写入失败: %v", err)
	}
	linkName := "20251231T000000Z-link.md" // 字典序最早 → 滚动时第一个被处理
	link := filepath.Join(dir, linkName)
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("夹具软链失败: %v", err)
	}
	// 再写 RevisionsKeep+2 个普通文件，让总量超过保留数。
	var regular []string
	for i := 0; i < RevisionsKeep+2; i++ {
		name := filepath.Join(dir, fmt.Sprintf("20260101T0000%02dZ-a.md", i))
		if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
			t.Fatalf("夹具写入失败: %v", err)
		}
		regular = append(regular, name)
	}

	if err := pruneRevisions(dir); err != nil {
		t.Fatalf("滚动失败: %v", err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Errorf("符号链接被删了（不是我们写的东西，不替用户删）: %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("顺着符号链接删掉了用户的文件: %v", err)
	}
	// 普通文件确实被删到只剩保留数：不删任何东西的"守卫"同样是假绿。
	alive := 0
	for _, p := range regular {
		if _, err := os.Stat(p); err == nil {
			alive++
		}
	}
	if alive != RevisionsKeep {
		t.Errorf("普通归档剩 %d 份, 要 %d（链接那一份被跳过，所以最新的一批应当留下）", alive, RevisionsKeep)
	}
}
