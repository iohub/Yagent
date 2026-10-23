package tools

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"yagent/internal/toolspec"
)

// use_skill 的执行器。它只认 toolspec.SkillSource 这个窄接口，不认识
// *skills.Registry —— 于是 internal/tools 不必依赖 internal/skills，
// 装配方向仍然是 app → tools + skills（依赖图里没有回边）。
func newSkillExecutor(src toolspec.SkillSource) toolspec.ToolFunc {
	return func(_ context.Context, params map[string]interface{}) (interface{}, error) {
		if src == nil {
			// 与 read_blueprint 同一条决定：没有技能时工具仍在场，回执自带修复指引，
			// 而不是从工具面静默消失（模型会以为这个 Agent 不支持技能）。
			return "no skills found — put them in .yagent/skills/<name>/SKILL.md or ~/.yagent/skills/<name>/SKILL.md", nil
		}
		name, err := requireString(params, "name")
		if err != nil {
			return nil, err
		}
		offset := paramInt(params, "offset", 0, 0, 1<<20)
		limit := paramInt(params, "limit", 0, 0, 1<<20)

		body, err := src.Body(name, offset, limit)
		if err != nil {
			if errors.Is(err, toolspec.ErrSkillNotFound) {
				return skillNotFoundReply(src, name), nil
			}
			return nil, fmt.Errorf("读取技能 %q 失败: %w", name, err)
		}
		return renderSkillBody(body, paramString(params, "arguments")), nil
	}
}

// skillNotFoundReply 回吐可用名单而不是报错：模型能自我纠正，
// 而不是拿着一条 "not found" 卡在那里重试同一个名字。
func skillNotFoundReply(src toolspec.SkillSource, name string) string {
	catalog := strings.TrimSpace(src.Catalog(0))
	if catalog == "" {
		return fmt.Sprintf("skill %q not found, and no skills are configured — put them in .yagent/skills/<name>/SKILL.md or ~/.yagent/skills/<name>/SKILL.md", name)
	}
	return fmt.Sprintf("skill %q not found. Available skills:\n%s", name, catalog)
}

// renderSkillBody 把 front-matter 事实与正文拼成一段可读回执。
// allowed-tools 是**咨询性**的：只回吐，不改工具面、不做强制（设计稿 §6.5）。
func renderSkillBody(b toolspec.SkillBody, args string) string {
	text := substituteSkillArgs(b.Text, args)

	var s strings.Builder
	fmt.Fprintf(&s, "skill: %s\n", b.Name)
	if b.Description != "" {
		fmt.Fprintf(&s, "description: %s\n", b.Description)
	}
	fmt.Fprintf(&s, "source: %s (%s)\n", b.Source, b.Path)
	if len(b.AllowedTools) > 0 {
		fmt.Fprintf(&s, "allowed-tools (advisory, not enforced): %s\n", strings.Join(b.AllowedTools, ", "))
	}
	if len(b.Arguments) > 0 {
		fmt.Fprintf(&s, "expected arguments: %s\n", strings.Join(b.Arguments, ", "))
	}
	if args != "" {
		fmt.Fprintf(&s, "arguments: %s\n", args)
	}
	s.WriteString("\n")
	s.WriteString(text)
	if !strings.HasSuffix(text, "\n") {
		s.WriteString("\n")
	}
	if b.Truncated {
		fmt.Fprintf(&s, "\ntruncated: continue with offset=%d\n", b.NextOffset)
	}
	return s.String()
}

// substituteSkillArgs 做 $ARGUMENTS / $1 / $2 的文本替换。
// 只有整词才算：`$10` 不会被当成 `$1` 加一个 `0`（数字位一直读到非数字为止）。
// 没有对应实参的占位符会被删掉 —— 留下字面 `$3` 会让模型以为那是要原样保留的语法。
func substituteSkillArgs(text, args string) string {
	if args == "" {
		return text
	}
	text = strings.ReplaceAll(text, "$ARGUMENTS", args)
	if !strings.Contains(text, "$") {
		return text
	}
	fields := strings.Fields(args)
	var b strings.Builder
	for i := 0; i < len(text); {
		if text[i] == '$' && i+1 < len(text) && text[i+1] >= '1' && text[i+1] <= '9' {
			j := i + 1
			for j < len(text) && text[j] >= '0' && text[j] <= '9' {
				j++
			}
			if n, err := strconv.Atoi(text[i+1 : j]); err == nil && n <= len(fields) {
				b.WriteString(fields[n-1])
			}
			i = j
			continue
		}
		b.WriteByte(text[i])
		i++
	}
	return b.String()
}
