package agents

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeModuleDocTree 在工作区里铺一棵带局部文档的目录树；rootName 为 "" 表示不写根文档。
func writeModuleDocTree(t *testing.T, root string, sizes map[string]int) {
	t.Helper()
	for dir, size := range sizes {
		full := filepath.Join(root, filepath.FromSlash(dir))
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		body := fmt.Sprintf("# local doc for %s\n%s\n", dir, strings.Repeat("x", size))
		if err := os.WriteFile(filepath.Join(full, "AGENTS.md"), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", dir, err)
		}
	}
}

func TestLocalDocsForTask_MatchesClosedSet(t *testing.T) {
	root := t.TempDir()
	writeModuleDocTree(t, root, map[string]int{
		"internal/agents": 10,
		"internal/tools":  10,
	})

	got := LocalDocsForTask(root, "改 internal/agents/executor.go 里的并发谓词")
	if !strings.Contains(got, "### internal/agents/AGENTS.md") {
		t.Fatalf("nearest doc missing:\n%s", got)
	}
	// 任务没提到的目录不许进来。
	if strings.Contains(got, "internal/tools") {
		t.Errorf("unmentioned dir leaked into the prefix:\n%s", got)
	}
	// 整条祖先链上都没有文档时，不许凭路径形状编出一份来（§11 坑 42 的反面）。
	// 注意祖先链上有文档时**必须**注入，那是这个通道的全部价值：
	// "internal/agents/nope/thing.go" 会带出 internal/agents 那份，而这里整条链都是空的。
	if out := LocalDocsForTask(root, "改 internal/blueprint/nope/thing.go 里的东西"); out != "" {
		t.Errorf("path-shaped guess produced output:\n%s", out)
	}
	if out := LocalDocsForTask(root, ""); out != "" || LocalDocsForTask("", "任意任务") != "" {
		t.Error("empty root or task must yield nothing")
	}
}

func TestLocalDocsForTask_NearestWinsAndNeverCarriesRoot(t *testing.T) {
	root := t.TempDir()
	const rootMarker = "ROOT-DOC-MUST-NOT-APPEAR"
	writeModuleDocTree(t, root, map[string]int{
		".":                         10,
		"internal":                  10,
		"internal/agents":           10,
		"internal/agents/director":  10,
		"internal/agents/toolbatch": 10,
	})
	// 根那一份换成可辨认的哨兵内容（工作区根文档已经在系统上下文里，重复注入是纯浪费）。
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"),
		[]byte("# root\n"+rootMarker+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := LocalDocsForTask(root, "看 internal/agents/director/planner_run.go 的停点判定")
	if strings.Contains(got, rootMarker) {
		t.Errorf("workspace-root AGENTS.md must never be re-injected:\n%s", got)
	}
	// 祖先链：最深优先，且不超过 moduleDocsMaxFiles 份。
	want := []string{"internal/agents/director", "internal/agents", "internal"}
	for i, dir := range want {
		head := "### " + dir + "/AGENTS.md"
		if !strings.Contains(got, head) {
			t.Fatalf("missing %s in:\n%s", head, got)
		}
		if strings.Count(got, "### ") != len(want) {
			t.Fatalf("expected exactly %d docs (cap is %d), got %d:\n%s",
				len(want), moduleDocsMaxFiles, strings.Count(got, "### "), got)
		}
		if i > 0 && strings.Index(got, head) < strings.Index(got, "### "+want[i-1]+"/AGENTS.md") {
			t.Errorf("order must be deepest-first; %s came before %s", dir, want[i-1])
		}
	}
	// 同深按字典序：两个平级目录同时被点到时顺序稳定。
	a := LocalDocsForTask(root, "对比 internal/agents/toolbatch 与 internal/agents 的 ctx 传法")
	b := LocalDocsForTask(root, "对比 internal/agents 与 internal/agents/toolbatch 的 ctx 传法")
	if a == "" || a != b {
		t.Errorf("output must be order-of-appearance independent (prefix cache), a==b: %v", a == b)
	}
}

func TestLocalDocsForTask_BudgetTruncationIsVisible(t *testing.T) {
	root := t.TempDir()
	// 夹具必须真的超预算，否则断言恒真。
	big := moduleDocsMaxBytes
	writeModuleDocTree(t, root, map[string]int{
		"internal":                 big,
		"internal/agents":          big,
		"internal/agents/director": big,
		"internal/tools":           big,
	})
	task := "同时改 internal/agents/director/planner_run.go、internal/agents/executor.go、internal/tools/adapter.go"
	got := LocalDocsForTask(root, task)

	if !strings.Contains(got, "### internal/agents/director/AGENTS.md") {
		t.Fatalf("first doc missing:\n%.200s", got)
	}
	if !strings.Contains(got, "(truncated)") || !strings.Contains(got, "omitted") {
		t.Errorf("over-budget must be visible, got:\n%.400s", got)
	}
	// 预算内才是硬约束：头部与省略行之外，正文合计不超过 moduleDocsMaxBytes。
	body := got[strings.Index(got, "### "):]
	if len(body) > moduleDocsMaxBytes+512 {
		t.Errorf("injected body %d bytes exceeds budget %d", len(body), moduleDocsMaxBytes)
	}
	if n := strings.Count(got, "### "); n > moduleDocsMaxFiles {
		t.Errorf("doc count %d over cap %d", n, moduleDocsMaxFiles)
	}
}

func TestLocalDocsForTask_RejectsPathEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeModuleDocTree(t, outside, map[string]int{"leak": 10})

	if got := LocalDocsForTask(root, "读 ../leak/AGENTS.md 和 ../../etc/AGENTS.md"); got != "" {
		t.Errorf("relative escape must yield nothing:\n%s", got)
	}
	if got := LocalDocsForTask(root, outside+"/leak/thing.go"); got != "" {
		t.Errorf("absolute path outside the workspace must yield nothing:\n%s", got)
	}
	// 工作区内的绝对路径要能用（用户会直接粘全路径）。
	resolved := outside
	if r, err := filepath.EvalSymlinks(outside); err == nil {
		resolved = r
	}
	if got := LocalDocsForTask(resolved, resolved+"/leak/thing.go"); !strings.Contains(got, "### leak/AGENTS.md") {
		t.Errorf("absolute path inside the workspace should match:\n%s", got)
	}
}

func TestLocalDocsForTask_SkipsSymlinkOutsideWorkspace(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeModuleDocTree(t, outside, map[string]int{"secret": 10})
	secret, err := os.ReadFile(filepath.Join(outside, "secret/AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	// 工作区内有一个看起来人畜无害的目录，里面的 AGENTS.md 是指向外面的软链。
	if err := os.MkdirAll(filepath.Join(root, "internal/agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret/AGENTS.md"),
		filepath.Join(root, "internal/agents/AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	got := LocalDocsForTask(root, "改 internal/agents/executor.go")
	if strings.Contains(got, string(secret[:min(len(secret), 16)])) {
		t.Errorf("a doc symlinked out of the workspace must not be injected:\n%s", got)
	}
	if strings.Contains(got, "### ") {
		t.Errorf("no doc should have been injected at all:\n%s", got)
	}
}

func TestLocalDocsForTask_NeverCachesBody(t *testing.T) {
	root := t.TempDir()
	writeModuleDocTree(t, root, map[string]int{"internal/agents": 10})
	task := "改 internal/agents/executor.go"

	first := LocalDocsForTask(root, task)
	if !strings.Contains(first, "local doc for internal/agents") {
		t.Fatalf("first read missing body:\n%s", first)
	}
	// 同一次会话里改掉盘上的文档，下一次委派必须拿到新的那一份。
	if err := os.WriteFile(filepath.Join(root, "internal/agents/AGENTS.md"),
		[]byte("EDITED-ON-DISK"), 0o644); err != nil {
		t.Fatal(err)
	}
	if second := LocalDocsForTask(root, task); !strings.Contains(second, "EDITED-ON-DISK") {
		t.Errorf("body must be read fresh each delegation:\n%s", second)
	}
}

func TestPackDelegateTask_CarriesModuleDocs(t *testing.T) {
	workDir := t.TempDir()
	writeModuleDocTree(t, workDir, map[string]int{"internal/compression": 10})
	d := newTestDirectorAgent(t, workDir)

	packed, _ := d.PackDelegateTask("把 internal/compression/truncate.go 的判定改成严格大于", map[string]interface{}{})
	if !strings.Contains(packed, moduleDocsTitle) ||
		!strings.Contains(packed, "### internal/compression/AGENTS.md") {
		t.Fatalf("delegate task must carry the module doc, got:\n%s", packed)
	}
	// 注入的是参考料，不是新指令：原任务永远排在最后。
	task := "把 internal/compression/truncate.go 的判定改成严格大于"
	if !strings.HasSuffix(strings.TrimRight(packed, "\n"), task) {
		t.Errorf("original task must stay last:\n%s", packed)
	}
	// 变异闸门：删掉 packDelegateTask 里那一行调用，这条立刻红。
	if bare, _ := d.PackDelegateTask("随便问一句没有路径的话", map[string]interface{}{}); bare != "随便问一句没有路径的话" {
		t.Errorf("task without any path-shaped token must stay unchanged, got:\n%s", bare)
	}
}
