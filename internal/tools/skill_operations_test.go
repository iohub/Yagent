package tools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"yagent/internal/toolspec"
)

// fakeSkills 是 SkillSource 的夹具：只回放一份固定技能与一份固定目录。
type fakeSkills struct {
	body   toolspec.SkillBody
	err    error
	calls  int
	offset int
	limit  int
}

func (f *fakeSkills) Catalog(int) string { return "## Skills\n- code-review: 审查 diff" }

func (f *fakeSkills) Body(name string, offset, limit int) (toolspec.SkillBody, error) {
	f.calls++
	f.offset, f.limit = offset, limit
	if f.err != nil {
		return toolspec.SkillBody{}, f.err
	}
	return f.body, nil
}

func runSkill(t *testing.T, src toolspec.SkillSource, params map[string]interface{}) string {
	t.Helper()
	out, err := newSkillExecutor(src)(context.Background(), params)
	if err != nil {
		t.Fatalf("use_skill: %v", err)
	}
	text, ok := out.(string)
	if !ok {
		t.Fatalf("use_skill 返回 %T, want string（回执是给模型读的文本）", out)
	}
	return text
}

// 没有技能时工具仍在场并解释"放哪儿"：从工具面静默消失的话，
// 模型会以为这个 Agent 不支持技能，用户也拿不到修复指引。
func TestUseSkill_NoSkillsExplainsWhy(t *testing.T) {
	out := runSkill(t, nil, map[string]interface{}{"name": "code-review"})
	for _, want := range []string{".yagent/skills/<name>/SKILL.md", "~/.yagent/skills/<name>/SKILL.md"} {
		if !strings.Contains(out, want) {
			t.Errorf("回执 = %q, want 含修复路径 %q", out, want)
		}
	}
}

// 名字不存在时回吐可用名单，让模型自我纠正，而不是拿着一条 not found 重试同一个名字。
func TestUseSkill_UnknownNameListsAvailable(t *testing.T) {
	src := &fakeSkills{err: toolspec.ErrSkillNotFound}
	out := runSkill(t, src, map[string]interface{}{"name": "no-such-skill"})
	if !strings.Contains(out, "no-such-skill") || !strings.Contains(out, "code-review") {
		t.Errorf("回执 = %q, want 名字 + 可用名单", out)
	}
}

// 读取失败（不是"没找到"）必须冒泡成 error：那是磁盘问题，不该伪装成一次成功回执。
func TestUseSkill_ReadFailureSurfacesError(t *testing.T) {
	src := &fakeSkills{err: errors.New("permission denied")}
	_, err := newSkillExecutor(src)(context.Background(), map[string]interface{}{"name": "code-review"})
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("err = %v, want 包住底层错误", err)
	}
}

func TestUseSkill_MissingNameIsAnError(t *testing.T) {
	_, err := newSkillExecutor(&fakeSkills{})(context.Background(), map[string]interface{}{})
	if err == nil || !strings.Contains(err.Error(), `"name"`) {
		t.Fatalf("err = %v, want 点名缺失的 name 参数", err)
	}
}

// front-matter 事实必须出现在回执里（模型据此知道来源与"这个技能期望哪些工具"），
// 而 allowed-tools 只是咨询性的一行 —— 它不改工具面。
func TestUseSkill_RendersFactsAndTruncationMarker(t *testing.T) {
	src := &fakeSkills{body: toolspec.SkillBody{
		Name:         "code-review",
		Description:  "审查 diff",
		Source:       "project",
		Path:         "/repo/.yagent/skills/code-review/SKILL.md",
		AllowedTools: []string{"read_file", "search_by_regex"},
		Arguments:    []string{"diff"},
		Text:         "第一步：读懂 diff",
		Truncated:    true,
		NextOffset:   40,
	}}
	out := runSkill(t, src, map[string]interface{}{"name": "code-review", "offset": float64(20), "limit": float64(20)})

	for _, want := range []string{
		"skill: code-review",
		"source: project (/repo/.yagent/skills/code-review/SKILL.md)",
		"allowed-tools (advisory, not enforced): read_file, search_by_regex",
		"expected arguments: diff",
		"第一步：读懂 diff",
		"truncated: continue with offset=40",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("回执缺 %q：\n%s", want, out)
		}
	}
	if src.offset != 20 || src.limit != 20 {
		t.Errorf("分页参数 = offset %d limit %d, want 原样传给 Body", src.offset, src.limit)
	}
	if strings.Contains(out, "---") {
		t.Error("回执里不该再有 front-matter 分隔线：正文已经剥掉了 front-matter")
	}
}

// $10 不能被当成 $1 加一个 0；没有对应实参的占位符要整个删掉。
func TestSubstituteSkillArgs_WholeTokensOnly(t *testing.T) {
	cases := []struct {
		text, args, want string
	}{
		{"review $1 please", "a.go", "review a.go please"},
		{"$1 $2", "left right", "left right"},
		{"$10 and $1", "only", " and only"},
		{"diff: $ARGUMENTS", "a.go b.go", "diff: a.go b.go"},
		{"no placeholders", "x", "no placeholders"},
		{"$3 missing", "only one", " missing"},
	}
	for _, tc := range cases {
		if got := substituteSkillArgs(tc.text, tc.args); got != tc.want {
			t.Errorf("substituteSkillArgs(%q, %q) = %q, want %q", tc.text, tc.args, got, tc.want)
		}
	}
	// args 为空时逐字返回原文：把 $1 静默删掉会让技能正文缺一段话。
	if got := substituteSkillArgs("$1 stays", ""); got != "$1 stays" {
		t.Errorf("空 arguments 时 = %q, want 原文", got)
	}
}
