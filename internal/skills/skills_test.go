package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustLoad(t *testing.T, dirs ...string) (*Registry, LoadAccount) {
	t.Helper()
	return Load(dirs)
}

// ---------- 既有行为：扁平 .md ----------

func TestLoad_EmptyDirectory(t *testing.T) {
	r, account := mustLoad(t, t.TempDir())
	if r.Count() != 0 {
		t.Errorf("expected 0 skills, got %d", r.Count())
	}
	if account.Total != 0 || len(account.Rejected) != 0 {
		t.Errorf("account = %+v, want 全零", account)
	}
}

func TestLoad_NonExistentDirectory(t *testing.T) {
	// 目录不存在是正常情况（用户没建），不记账 —— 每个新项目都会这样。
	r, account := mustLoad(t, "/nonexistent/path")
	if r.Count() != 0 {
		t.Errorf("expected 0 skills, got %d", r.Count())
	}
	if len(account.Rejected) != 0 {
		t.Errorf("Rejected = %+v, want 空（目录不存在不是错误）", account.Rejected)
	}
}

// 扁平 .md 的断言逐字保留：它是"没有任何 front-matter"那一档，
// 也是既有用户目录里最可能存在的形状。
func TestLoad_WithMarkdownFiles(t *testing.T) {
	dir := t.TempDir()
	content := "# Test Skill\nThis is a test skill."
	writeFile(t, filepath.Join(dir, "test-skill.md"), content)

	r, _ := mustLoad(t, dir)
	if r.Count() != 1 {
		t.Fatalf("expected 1 skill, got %d", r.Count())
	}
	skill, ok := r.Get("test-skill")
	if !ok {
		t.Fatal("skill 'test-skill' not found")
	}
	if skill.Description != "Test Skill" {
		t.Errorf("description = %q, want 首行去 #", skill.Description)
	}
	if skill.Body != content {
		t.Errorf("Body = %q, want 全文（没有 front-matter 可剥）", skill.Body)
	}
	if skill.FrontMatter != "none" {
		t.Errorf("FrontMatter = %q, want none", skill.FrontMatter)
	}
}

func TestLoad_MultipleFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "skill-a.md"), "# Skill A\nContent A")
	writeFile(t, filepath.Join(dir, "skill-b.md"), "# Skill B\nContent B")
	writeFile(t, filepath.Join(dir, "readme.txt"), "This should be ignored")

	r, _ := mustLoad(t, dir)
	if r.Count() != 2 {
		t.Errorf("expected 2 skills, got %d", r.Count())
	}
	if _, ok := r.Get("readme"); ok {
		t.Error("readme.txt should not be loaded")
	}
}

func TestRegistry_List(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"beta", "alpha", "gamma"} {
		writeFile(t, filepath.Join(dir, name+".md"), "# "+name)
	}
	r, _ := mustLoad(t, dir)
	if got := strings.Join(r.List(), ","); got != "alpha,beta,gamma" {
		t.Errorf("List() = %q, want 字典序（目录顺序稳定才能吃到 provider 前缀缓存）", got)
	}
}

// ---------- 合并语义 ----------

func TestLoad_MergesProjectOverUser(t *testing.T) {
	user := t.TempDir()
	project := t.TempDir()
	writeFile(t, filepath.Join(user, "shared.md"), "# Shared from user\nuser body")
	writeFile(t, filepath.Join(user, "only-user.md"), "# Only user")
	writeFile(t, filepath.Join(project, "shared.md"), "# Shared from project\nproject body")
	writeFile(t, filepath.Join(project, "only-project.md"), "# Only project")

	r, account := mustLoad(t, user, project)

	if r.Count() != 3 {
		t.Fatalf("Count() = %d, want 3（合并，不是整体替换）", r.Count())
	}
	if s, _ := r.Get("shared"); s.Body != "# Shared from project\nproject body" {
		t.Errorf("shared.Body = %q, want 后者覆盖前者", s.Body)
	}
	if len(account.Overridden) != 1 || account.Overridden[0].Name != "shared" {
		t.Fatalf("Overridden = %+v, want 一条 shared", account.Overridden)
	}
	if len(account.BySource) != 2 {
		t.Errorf("BySource = %v, want 两级各一组", account.BySource)
	}
}

func TestLoad_DirectoryLayoutWinsOverFlat(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "code-review.md"), "# Flat\nflat body")
	writeFile(t, filepath.Join(dir, "code-review", "SKILL.md"), "---\n{\"description\":\"目录式\"}\n---\nrich body")

	r, account := mustLoad(t, dir)
	s, ok := r.Get("code-review")
	if !ok {
		t.Fatal("code-review 没加载")
	}
	if s.Body != "rich body" || s.Dir == "" {
		t.Errorf("目录式没有胜出：%+v", s)
	}
	if len(account.Overridden) != 1 || !strings.Contains(account.Overridden[0].Reason, "directory") {
		t.Errorf("Overridden = %+v, want 一条目录式胜出的记录", account.Overridden)
	}
	if r.Count() != 1 {
		t.Errorf("Count() = %d, want 1（同名只算一个技能）", r.Count())
	}
}

// 目录存在但读不出（权限）：必须记一条 Rejected、继续加载其余目录、且绝不 panic。
// 改动前这里是 `(nil, err)` + 调用点 .Count() 的 nil 解引用 —— 启动即崩。
func TestLoad_UnreadableDirDoesNotPanic(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 不受权限位限制，这条夹具在 root 下没有意义")
	}
	broken := filepath.Join(t.TempDir(), "broken")
	if err := os.Mkdir(broken, 0o000); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(broken, 0o755) })

	good := t.TempDir()
	writeFile(t, filepath.Join(good, "ok.md"), "# Ok")

	r, account := mustLoad(t, broken, good)
	if r == nil {
		t.Fatal("Load 返回了 nil 注册表")
	}
	if r.Count() != 1 {
		t.Errorf("Count() = %d, want 1（坏目录不该拖垮其余目录）", r.Count())
	}
	if len(account.Rejected) != 1 || !strings.Contains(account.Rejected[0].Reason, "read:") {
		t.Errorf("Rejected = %+v, want 一条读失败账", account.Rejected)
	}
}

func TestLoad_InvalidNameIsRejected(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "Bad Name.md"), "# nope")
	r, account := mustLoad(t, dir)
	if r.Count() != 0 {
		t.Errorf("非法名字被加载了：%v", r.List())
	}
	if len(account.Rejected) != 1 || !strings.Contains(account.Rejected[0].Reason, "invalid name") {
		t.Errorf("Rejected = %+v, want invalid name", account.Rejected)
	}
}

// ---------- front-matter ----------

func TestParseFrontMatter_JSON(t *testing.T) {
	raw := "---\n{\"description\":\"审查 diff\",\"allowed-tools\":[\"read_file\",\"search_by_regex\"],\"arguments\":[\"diff\"]}\n---\n正文第一行\n正文第二行"
	fields, body, kind, err := parseFrontMatter(raw)
	if err != nil {
		t.Fatalf("parseFrontMatter: %v", err)
	}
	if kind != "json" {
		t.Errorf("kind = %q, want json", kind)
	}
	if fields["description"] != "审查 diff" {
		t.Errorf("description = %v", fields["description"])
	}
	if body != "正文第一行\n正文第二行" {
		t.Errorf("body = %q, want 不含 front-matter", body)
	}
}

func TestParseFrontMatter_YAMLSubset(t *testing.T) {
	raw := "---\nname: code-review\ndescription: 审查 diff\nallowed-tools: read_file, search_by_regex\narguments: [diff, pr]\n---\nbody"
	fields, body, kind, err := parseFrontMatter(raw)
	if err != nil {
		t.Fatalf("parseFrontMatter: %v", err)
	}
	if kind != "yaml-subset" {
		t.Errorf("kind = %q, want yaml-subset", kind)
	}
	if fields["description"] != "审查 diff" {
		t.Errorf("description = %v", fields["description"])
	}
	if body != "body" {
		t.Errorf("body = %q", body)
	}
	if got := stringList(fields["allowed-tools"]); strings.Join(got, ",") != "read_file,search_by_regex" {
		t.Errorf("allowed-tools = %v", got)
	}
	if got := stringList(fields["arguments"]); strings.Join(got, ",") != "diff,pr" {
		t.Errorf("arguments = %v", got)
	}

	// 嵌套**不认**：悄悄支持一半会让用户以为嵌套生效了。
	nested := "---\ndescription: x\nmeta:\n  nested: y\n---\nbody"
	_, _, kind, err = parseFrontMatter(nested)
	if err == nil || kind != "none" {
		t.Errorf("嵌套 front-matter = kind %q err %v, want none + error", kind, err)
	}
}

func TestParseFrontMatter_BrokenFallsBackToFirstLine(t *testing.T) {
	// 有分隔线但内容不是 JSON、也没有 description：kind=none，正文仍剥掉那一段。
	raw := "---\njust some text\n---\n# Real Title\nbody"
	fields, body, kind, err := parseFrontMatter(raw)
	if err == nil {
		t.Error("坏 front-matter 没有报错")
	}
	if kind != "none" {
		t.Errorf("kind = %q, want none", kind)
	}
	if body != "# Real Title\nbody" {
		t.Errorf("body = %q, want 不含坏 front-matter", body)
	}
	if got := descriptionOf(fields, body); got != "Real Title" {
		t.Errorf("description = %q, want 正文首行去 #", got)
	}

	// 没有分隔线：正文逐字保留（既有行为）。
	plain := "# Test Skill\nThis is a test skill."
	fields, body, kind, err = parseFrontMatter(plain)
	if err != nil || kind != "none" || body != plain {
		t.Errorf("无 front-matter 时 = (%q,%q,%v), want 原文 + none + nil", body, kind, err)
	}
	if got := descriptionOf(fields, body); got != "Test Skill" {
		t.Errorf("description = %q, want 首行去 #", got)
	}
}

// ---------- 目录渲染 ----------

func TestCatalogEmptyRegistryReturnsEmpty(t *testing.T) {
	r, _ := mustLoad(t, t.TempDir())
	if got := r.Catalog(600); got != "" {
		t.Errorf("Catalog() = %q, want 空串（不注入空标题）", got)
	}
}

func TestCatalogRespectsBudget(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 20; i++ {
		writeFile(t, filepath.Join(dir, fmt.Sprintf("skill-%02d.md", i)), fmt.Sprintf("# Skill %02d\n%s", i, strings.Repeat("描述", 30)))
	}
	r, _ := mustLoad(t, dir)

	full := r.Catalog(0)
	if !strings.Contains(full, "skill-00") || !strings.Contains(full, "skill-19") {
		t.Fatalf("不限预算时应列出全部：\n%s", full)
	}

	tight := r.Catalog(80)
	if strings.Contains(tight, "skill-19") {
		t.Errorf("紧预算下仍列出了尾部技能：\n%s", tight)
	}
	if !strings.Contains(tight, "more not shown") {
		t.Errorf("截断标记没写进正文（模型会以为自己看到了全部）：\n%s", tight)
	}
	if !strings.Contains(tight, "skill-00") {
		t.Errorf("至少保留第一条：\n%s", tight)
	}
}

// 目录行宽是 rune 预算：按字节切会把中文切成半个字。
func TestCatalogShortensByRunes(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "cn.md"), "---\n{\"description\":\""+strings.Repeat("很长的描述", 40)+"\"}\n---\nbody")
	r, _ := mustLoad(t, dir)
	r.SetOptions(Options{LineChars: 10})

	catalog := r.Catalog(0)
	line := ""
	for _, l := range strings.Split(catalog, "\n") {
		if strings.HasPrefix(l, "- cn: ") {
			line = strings.TrimPrefix(l, "- cn: ")
		}
	}
	if line == "" {
		t.Fatalf("目录里没有 cn 那一行：\n%s", catalog)
	}
	if got := len([]rune(line)); got > 10 {
		t.Errorf("行宽 = %d rune, want ≤10", got)
	}
	if !strings.HasSuffix(line, "…") {
		t.Errorf("行 = %q, want 尾部省略号", line)
	}
}

// ---------- 正文分页 ----------

func TestBodyPaginationIsSelfConsistent(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&b, "line-%02d\n", i)
	}
	writeFile(t, filepath.Join(dir, "long.md"), "# Long\n"+b.String())
	r, _ := mustLoad(t, dir)

	first, err := r.Body("long", 0, 10)
	if err != nil {
		t.Fatalf("Body: %v", err)
	}
	if !first.Truncated || first.NextOffset != 10 {
		t.Fatalf("第一页 = {trunc:%v next:%d}, want trunc + next=10", first.Truncated, first.NextOffset)
	}
	second, err := r.Body("long", first.NextOffset, 10)
	if err != nil {
		t.Fatalf("Body 2: %v", err)
	}
	third, err := r.Body("long", second.NextOffset, 1000)
	if err != nil {
		t.Fatalf("Body 3: %v", err)
	}
	if third.Truncated {
		t.Errorf("最后一页仍报 truncated（next=%d）", third.NextOffset)
	}

	joined := first.Text + second.Text + third.Text
	full, _ := r.Body("long", 0, 0)
	if joined != full.Text {
		t.Errorf("分页拼接与全文不一致：\n%q\nvs\n%q", joined, full.Text)
	}

	if _, err := r.Body("nope", 0, 0); err == nil {
		t.Error("不存在的技能没有返回错误")
	}
}

func TestBodyBudgetCapsByDefault(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "big.md"), "# Big\n"+strings.Repeat("x", 40000))
	r, _ := mustLoad(t, dir)
	r.SetOptions(Options{BodyMaxTokens: 200})

	page, err := r.Body("big", 0, 0)
	if err != nil {
		t.Fatalf("Body: %v", err)
	}
	if !page.Truncated {
		t.Error("超预算正文没有标 truncated")
	}
	if len(page.Text)/4 > 400 {
		t.Errorf("正文 %d 字节远超预算 200 token 的量级", len(page.Text))
	}
}

// ---------- 账目 ----------

func TestLoadAccountStringIsStable(t *testing.T) {
	user := t.TempDir()
	project := t.TempDir()
	writeFile(t, filepath.Join(user, "b.md"), "# B")
	writeFile(t, filepath.Join(user, "a.md"), "# A")
	writeFile(t, filepath.Join(project, "a.md"), "# A2")

	_, account := mustLoad(t, user, project)
	first := account.String()
	for i := 0; i < 3; i++ {
		if again := account.String(); again != first {
			t.Fatalf("账目渲染不稳定：\n%q\nvs\n%q", first, again)
		}
	}
	if !strings.Contains(first, "2 loaded") || !strings.Contains(first, "1 overridden") {
		t.Errorf("账目 = %q, want 条数与覆盖数", first)
	}
	if !strings.Contains(first, "a: ") || !strings.Contains(first, " ▸ ") {
		t.Errorf("账目 = %q, want 点名谁盖了谁", first)
	}
}
