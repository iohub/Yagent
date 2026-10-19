package blueprint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件钉 I19（锚点三态）与 I10（结构指纹不符 → 标 stale 且**保留**旧散文）。
//
// 两条各答一件事，所以都要有（§5.2）：
//   指纹答"这个模块形状变了，注解整体可疑"；锚点答"这句话指的那个东西没了"。
//   只有前者时"结构没变但结论过期"会长期冒充事实；只有后者时
//   一次整体重构里每条锚点仍可单独成立而整片说法已经错了。

func TestAnchorVerificationIsThreeValued(t *testing.T) {
	root := t.TempDir()
	body := "package a\n\nfunc Foo() {}\n"
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte(body), 0o600); err != nil {
		t.Fatalf("夹具写入失败: %v", err)
	}
	c := NewAnchorChecker(root)

	cases := []struct {
		name           string
		where          string
		wantState      string
		wantUnverifive bool
	}{
		{"bare path exists", "a.go", StateOK, false},
		{"path not there yet", "sub/", StateStale, false}, // 目录还没建（建好之后的判定见下面那条）
		{"line in range", "a.go:2", StateOK, false},
		{"line out of range", "a.go:99", StateStale, false},
		{"line zero", "a.go:0", StateStale, false},
		{"symbol present", "a.go:Foo", StateOK, false},
		{"symbol gone", "a.go:Bar", StateStale, false},
		{"file missing", "gone.go", StateStale, false},
		{"missing anchor", "", StateUnanchored, false},
		{"non-path anchor", "event bus", StateOK, true},
		{"key:value non path", "config.map", StateOK, true},
	}
	for _, tc := range cases {
		got := c.Check(tc.where)
		if got.State != tc.wantState {
			t.Errorf("%s: Check(%q).State = %q, 要 %q (%s)", tc.name, tc.where, got.State, tc.wantState, got.Reason)
		}
		if got.Unverifiable != tc.wantUnverifive {
			t.Errorf("%s: Check(%q).Unverifiable = %v, 要 %v", tc.name, tc.where, got.Unverifiable, tc.wantUnverifive)
		}
	}

	// 目录形态（`internal/tui/`）必须判得动 —— 建出来再验一次。
	// 用**新的** checker：上一个 checker 已经把 "sub/" 记成不存在了（一次 fold 内只 stat 一遍）。
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o700); err != nil {
		t.Fatalf("夹具目录失败: %v", err)
	}
	if got := NewAnchorChecker(root).Check("sub/"); got.State != StateOK {
		t.Errorf("已存在的目录锚点被判成 %q: %s", got.State, got.Reason)
	}
}

// §5.2 的"时机"：fold 末尾只验本次涉及的原子，单文件一次 stat/read，**本次 fold 内缓存**。
// 这条钉住缓存，免得将来有人把 exists/lines 两张表当成 bug 删掉 —— 那会让一次 fold
// 对同一个文件 stat 十几次。
func TestAnchorCheckerCachesWithinFold(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "c.go"), []byte("package c\n"), 0o600); err != nil {
		t.Fatalf("夹具写入失败: %v", err)
	}
	c := NewAnchorChecker(root)
	if got := c.Check("c.go"); got.State != StateOK {
		t.Fatalf("夹具里的文件本该存在: %s", got.Reason)
	}
	if err := os.Remove(filepath.Join(root, "c.go")); err != nil {
		t.Fatalf("夹具删除失败: %v", err)
	}
	if got := c.Check("c.go"); got.State != StateOK {
		t.Errorf("同一 fold 内重复 stat 了（缓存没生效）: %s", got.Reason)
	}
	if got := NewAnchorChecker(root).Check("c.go"); got.State != StateStale {
		t.Errorf("新的 checker 应当报文件缺失，实得 %q", got.State)
	}
}

// 符号匹配必须是词边界，不是子串：`Foo` 不该因为文件里有 `FooBar` 就被判成还在。
func TestAnchorSymbolMatchIsWordBounded(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "b.go"), []byte("func FooBar() {}\n"), 0o600); err != nil {
		t.Fatalf("夹具写入失败: %v", err)
	}
	c := NewAnchorChecker(root)
	if got := c.Check("b.go:Foo"); got.State != StateStale {
		t.Errorf("Foo 只因 FooBar 的前缀而被判成立（子串匹配），实得 %q: %s", got.State, got.Reason)
	}
	if got := c.Check("b.go:FooBar"); got.State != StateOK {
		t.Errorf("真实符号被判 stale: %s", got.Reason)
	}
}

// I10：指纹不符 → 该片进 stale 集合，而**散文照旧渲染**。
// 清空会把可用信息变成不可用；旧说法在下一次 Repo 覆盖之前仍然是"曾经成立的说法"。
func TestFingerprintMismatchKeepsProseAndFlagsStale(t *testing.T) {
	snap := testSnapshot(
		Leaf{Dir: "internal/a", FilesTotal: 2, Fingerprint: "new-shape"},
		Leaf{Dir: "internal/b", FilesTotal: 2, Fingerprint: "same"},
		Leaf{Dir: "internal/c", FilesTotal: 2, Fingerprint: "whatever"},
	)
	doc := &Doc{
		Front: Front{Version: docSchemaVersion, ModuleFingerprints: map[string]string{
			"internal/a": "old-shape", // 形状变了
			"internal/b": "same",      // 没变
			// internal/c 从来没记过指纹：没有基线就没有证据说它变了，不许判过期
		}},
		Body: "## Modules/internal/a\n" +
			"- (aaaa) purpose: A does things <!--id:aaaa field:purpose where:internal/a/a.go state:ok-->\n" +
			"## Modules/internal/b\n" +
			"- (bbbb) purpose: B does things <!--id:bbbb field:purpose where:internal/b/b.go state:ok-->\n" +
			"## Modules/internal/c\n" +
			"- (cccc) purpose: C does things <!--id:cccc field:purpose where:internal/c/c.go state:ok-->",
	}

	stale := StaleModules(doc, snap)
	if !stale["internal/a"] {
		t.Errorf("指纹不符的片没被标 stale")
	}
	if stale["internal/b"] {
		t.Errorf("指纹相同的片被判过期")
	}
	if stale["internal/c"] {
		t.Errorf("没有基线（从没记过指纹）的片被判过期 —— 那会把第一次写注解的模块全标可疑")
	}

	prose := RenderProse(snap, doc, ProseSpec{})
	joined := joinAll(prose)
	if !strings.Contains(joined, "A does things") {
		t.Errorf("指纹不符导致散文被删（I10 要求保留）:\n%s", joined)
	}
	if !strings.Contains(joined, "annotation predates a structural change") {
		t.Errorf("保留下来的散文没附过期原因:\n%s", joined)
	}
	if strings.Contains(joined, "B does things (annotation") {
		t.Errorf("没变过的片被连带标注:\n%s", joined)
	}
}

func joinAll(p Prose) string {
	return strings.Join(p.Header, "\n") + "\n" + strings.Join(p.Lines, "\n")
}
