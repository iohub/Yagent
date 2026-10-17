package blueprint

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleBody = `## Architecture
- (a7c3) Director is the only user-facing agent <!--id:a7c3 where:internal/agents/executor.go:136 src:repo_agent@1f2e3c4d seen:2026-10-17T07:10:00Z state:ok-->
- (dead1) This claim predates a structural change <!--id:dead1 where:internal/old/thing.go:9 src:repo_agent@1f2e3c4d seen:2026-09-01T00:00:00Z state:stale-->

## Gotchas
- (g0001) Unanchored notes come from the retired KV memory <!--id:g0001 state:unanchored-->

## Modules/internal/tui
- (5e12) purpose: Bubble Tea v2 frontend <!--id:5e12 field:purpose where:internal/tui/common/list_metrics.go src:repo_agent@aaaa1111 seen:2026-10-17T07:10:00Z state:ok-->
`

func docForTest() *Doc {
	return &Doc{
		Front: Front{
			Version:     docSchemaVersion,
			ProjectID:   "yagent_77506fcd",
			ProjectPath: "/Users/ydl/iohub/yagent",
			UpdatedAt:   "2026-10-17T07:10:00Z",
			GitSHA:      "2b16f3a",
			Folds:       17,
			Sources:     map[string]int{"repo_agent": 4},
		},
		Body: sampleBody,
	}
}

func TestDocRenderParseRoundTrip(t *testing.T) {
	d := docForTest()
	raw, err := d.Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	back, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v\n%s", err, raw)
	}
	if back.Front.ProjectID != d.Front.ProjectID || back.Front.Folds != d.Front.Folds ||
		back.Front.GitSHA != d.Front.GitSHA || back.Front.Sources["repo_agent"] != 4 {
		t.Errorf("front-matter 漂移: %+v", back.Front)
	}
	if strings.TrimSpace(back.Body) != strings.TrimSpace(sampleBody) {
		t.Errorf("正文漂移:\n%s", back.Body)
	}
	// 再渲染一次必须逐字节相同 —— 否则每次读写都会产出一整片 diff（§7.4 第 5 条的同一条纪律）。
	again, err := back.Render()
	if err != nil {
		t.Fatalf("re-Render: %v", err)
	}
	if string(again) != string(raw) {
		t.Errorf("二次渲染不幂等:\n--- first ---\n%s\n--- second ---\n%s", raw, again)
	}
}

// 计数是正文的派生态：手改（或旧版本写歪）的 atoms/stale/unanchored 必须在渲染时被重算覆盖。
// 留着它们就是让"两份事实"从第一天开始漂。
func TestRenderRecomputesDerivedCounters(t *testing.T) {
	d := docForTest()
	d.Front.Atoms = 999
	d.Front.Stale = 42
	d.Front.Unanchored = 7
	d.Front.DocSections = 1

	raw, err := d.Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	back, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if back.Front.Atoms != 4 {
		t.Errorf("atoms = %d, want 4（正文里就是 4 条）", back.Front.Atoms)
	}
	if back.Front.Stale != 1 {
		t.Errorf("stale = %d, want 1", back.Front.Stale)
	}
	if back.Front.Unanchored != 1 {
		t.Errorf("unanchored = %d, want 1", back.Front.Unanchored)
	}
	if back.Front.DocSections != 3 {
		t.Errorf("doc_sections = %d, want 3", back.Front.DocSections)
	}
}

func TestParseRejectsBadFrontMatter(t *testing.T) {
	cases := map[string]string{
		"没有分隔符":     "# Repository Blueprint\n",
		"分隔符没闭合":    "---\n{\"version\":1}\n",
		"不是合法 JSON": "---\n{version:1}\n---\n",
	}
	for name, raw := range cases {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Errorf("%s: 本该失败却通过了", name)
		}
	}

	// 版本认不出的必须**单独可判**：调用方据此决定"整份语义丢弃"，而不是猜。
	_, err := Parse([]byte("---\n{\"version\":9}\n---\n"))
	if !errors.Is(err, ErrUnknownVersion) {
		t.Errorf("未来的版本号应命中 ErrUnknownVersion，实得: %v", err)
	}
}

func TestSectionsSplitsAndReportsStrays(t *testing.T) {
	d := docForTest()
	blocks, errs := d.Sections()
	if len(errs) > 0 {
		t.Fatalf("样例文档不该有游离行: %v", errs)
	}
	if len(blocks) != 3 {
		t.Fatalf("段落数 = %d, want 3 (%v)", len(blocks), blocks)
	}
	if blocks[0].Heading != SecArchitecture || len(blocks[0].Atoms) != 2 {
		t.Errorf("第一段不符: %+v", blocks[0])
	}
	if blocks[1].Heading != SecGotchas || blocks[1].Module != "" {
		t.Errorf("第二段不符: %+v", blocks[1])
	}
	if blocks[2].Module != "internal/tui" {
		t.Errorf("Modules 前缀段没解析出片路径: %+v", blocks[2])
	}
	if blocks[2].Atoms[0].Field != "purpose" {
		t.Errorf("模块字段的 field 没解析出来: %+v", blocks[2].Atoms[0])
	}

	stray := docForTest()
	stray.Body = sampleBody + "\nsentence outside any section\n"
	if _, errs := stray.Sections(); len(errs) == 0 {
		t.Error("段落之外的裸句子必须被报出来 —— 它是文档被手改过或折叠写坏的信号")
	}

	broken := docForTest()
	broken.Body = sampleBody + "\n## Architecture\n- (zzzz) no meta comment at all\n"
	if _, errs := broken.Sections(); len(errs) == 0 {
		t.Error("落在段落里却没有元数据的行必须被报出来")
	}
}

func TestSaveLoadRoundTripViaAtomicWrite(t *testing.T) {
	root := filepath.Join(t.TempDir(), "yagent_77506fcd")
	d := docForTest()

	if err := Save(root, d); err != nil {
		t.Fatalf("Save: %v", err)
	}
	back, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if back == nil {
		t.Fatal("Load 返回 nil，但文件刚写下")
	}
	if strings.TrimSpace(back.Body) != strings.TrimSpace(sampleBody) {
		t.Errorf("落盘正文不符:\n%s", back.Body)
	}

	// 真源文件与修订快照都不该给同机其他用户读。
	st, err := os.Stat(DocPath(root))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := st.Mode().Perm(); got != 0o600 {
		t.Errorf("文档权限 = %o, want 600", got)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("临时文件残留: %s", e.Name())
		}
	}
}

// "还没有蓝图"不是错误：调用方据此决定是新建还是折叠。
func TestLoadMissingDocIsNotAnError(t *testing.T) {
	root := filepath.Join(t.TempDir(), "never_written")
	d, err := Load(root)
	if err != nil {
		t.Fatalf("Load 不该报错: %v", err)
	}
	if d != nil {
		t.Errorf("空目录该返回 nil，实得 %+v", d)
	}
}
