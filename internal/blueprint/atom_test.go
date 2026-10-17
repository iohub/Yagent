package blueprint

import (
	"strings"
	"testing"
)

func TestAtomLineRoundTrip(t *testing.T) {
	in := Atom{
		ID:    "a7c3",
		Text:  "Director is the only user-facing agent; every sub-agent shares RunAgentLoop",
		Where: "internal/agents/executor.go:136",
		Src:   "repo_agent@1f2e3c4d",
		Seen:  "2026-10-17T07:10:00Z",
		State: StateOK,
	}
	line := in.Line()

	out, ok, err := ParseAtomLine(line)
	if err != nil || !ok {
		t.Fatalf("刚渲染出来的行读不回来: ok=%v err=%v line=%q", ok, err, line)
	}
	if got := out.Line(); got != line {
		t.Errorf("二次渲染漂移:\n got %q\nwant %q", got, line)
	}
	if out.ID != in.ID || out.Text != in.Text || out.Where != in.Where ||
		out.Src != in.Src || out.Seen != in.Seen || out.State != in.State {
		t.Errorf("字段不符: %+v", out)
	}
}

// 未知元数据键必须原样活着：文档格式将来加键时，旧版本读新文档不该把信息读丢。
func TestAtomLineKeepsUnknownMetaKeys(t *testing.T) {
	line := `- (b2f1) some claim <!--id:b2f1 where:a.go seen:2026-01-01T00:00:00Z state:ok confidence:high-->`
	a, ok, err := ParseAtomLine(line)
	if err != nil || !ok {
		t.Fatalf("parse: ok=%v err=%v", ok, err)
	}
	if got := a.Extra["confidence"]; got != "high" {
		t.Fatalf("未知键没被留下: Extra=%v", a.Extra)
	}
	if !strings.Contains(a.Line(), "confidence:high") {
		t.Errorf("未知键没被带回渲染: %q", a.Line())
	}
}

// 一条都没有元数据注释的行不是原子（用户手写的正文要能活着）。
func TestParseAtomLineToleratesForeignLines(t *testing.T) {
	for _, line := range []string{
		"",
		"# Repository Blueprint",
		"## Architecture",
		"- 一个人类手写的条目，没有元数据",
		"普通句子",
	} {
		a, ok, err := ParseAtomLine(line)
		if err != nil {
			t.Errorf("不该报错: %q -> %v", line, err)
		}
		if ok {
			t.Errorf("不该被当成原子: %q (%+v)", line, a)
		}
	}
}

// 坏掉的原子必须响："看起来是我们写的、但元数据残缺"正是文档被改坏的信号。
func TestParseAtomLineRejectsBrokenAtoms(t *testing.T) {
	cases := map[string]string{
		"缺 id":        `- (a7c3) claim <!--where:a.go-->`,
		"短 ID 与元数据不符": `- (a7c3) claim <!--id:ffff where:a.go-->`,
		"注释没闭合":       `- (a7c3) claim <!--id:a7c3 where:a.go`,
		"正文为空":        `- (a7c3)  <!--id:a7c3 where:a.go-->`,
	}
	for name, line := range cases {
		if _, ok, err := ParseAtomLine(line); err == nil && ok {
			t.Errorf("%s: 本该失败却当成合法原子通过了: %q", name, line)
		}
		if _, _, err := ParseAtomLine(line); err == nil && name == "短 ID 与元数据不符" {
			t.Errorf("%s: 必须报错，不能静默", name)
		}
	}
}

func TestAtomIsAnchored(t *testing.T) {
	if (Atom{ID: "x", Text: "t"}).IsAnchored() {
		t.Error("没有 where 不该算有锚点")
	}
	if !(Atom{ID: "x", Text: "t", Where: "a.go:1"}).IsAnchored() {
		t.Error("有 where 该算有锚点")
	}
}
