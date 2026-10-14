package components

import (
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// testConfigEntries 取自真实形态：global + 7 个 agent + deepthinking 工具，
// provider/model 长短不一，并混入一个未解析出 provider 的目标。
func testConfigEntries(n int) []ConfigEntry {
	base := []ConfigEntry{
		{Target: "global", Group: TargetGroupGlobal, Provider: "autodl_glm53flash", Model: "GLM-5.3-flash", Context: 516000},
		{Target: "director", Group: TargetGroupAgents, Provider: "autodl_glm53flash", Model: "GLM-5.3-flash", Context: 516000},
		{Target: "coding", Group: TargetGroupAgents, Provider: "siliconflow_ds4_flash", Model: "deepseek-ai/DeepSeek-V4-Flash", Context: 1000000},
		{Target: "repo", Group: TargetGroupAgents, Provider: "siliconflow_dsv32", Model: "deepseek-ai/DeepSeek-V3.2", Context: 128000},
		{Target: "chat", Group: TargetGroupAgents, Provider: "qn_glm51", Model: "z-ai/glm-5.1", Context: 200000},
		{Target: "meta", Group: TargetGroupAgents, Provider: "", Model: "", Context: 0},
		{Target: "devops", Group: TargetGroupAgents, Provider: "localai", Model: "Qwen3.8-27B-GSQ-RCO", Context: 126000},
		{Target: "browser", Group: TargetGroupAgents, Provider: "private_endpoint", Model: "my-custom-llm-v2", Context: 0},
		{Target: "deepthinking", Group: TargetGroupTools, Provider: "xiaomi", Model: "mimo-v2-flash", Context: 262144},
		{Target: "design", Group: TargetGroupTools, Provider: "xiaomi", Model: "mimo-v2-flash", Context: 262144},
	}
	if n <= len(base) {
		return base[:n]
	}
	entries := append([]ConfigEntry{}, base...)
	for i := len(base); i < n; i++ {
		entries = append(entries, ConfigEntry{
			Target:   "custom_agent_" + strconv.Itoa(i),
			Group:    TargetGroupAgents,
			Provider: "extra_provider",
			Model:    "extra-model",
			Context:  32768,
		})
	}
	return entries
}

// TestAgentSelectDialog_ShowsGroupsAndContext 组标题、上下文列与占位符都应正确渲染。
func TestAgentSelectDialog_ShowsGroupsAndContext(t *testing.T) {
	d := NewAgentSelectDialog(testConfigEntries(9))
	d.SetBounds(120, 30)
	out := stripANSICodes(d.View())

	for _, want := range []string{
		"Select Target to Change Model",
		"TARGET", "PROVIDER", "MODEL", "CONTEXT",
		"GLOBAL", "AGENTS", "TOOLS",
		"516.0k", "1.0m", "128.0k", "200.0k", "126.0k", "262.1k",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered dialog missing %q\n%s", want, out)
		}
	}
	// 未解析出 provider/model 或上下文未知的目标显示占位符，保证列对齐
	metaRow := lineContaining(out, "meta")
	if strings.Count(metaRow, "-") < 3 {
		t.Errorf("unset provider/model/context should render as \"-\", got %q", metaRow)
	}
}

// lineContaining 返回渲染结果中第一个包含 s 的行。
func lineContaining(out, s string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, s) {
			return line
		}
	}
	return ""
}

// TestAgentSelectDialog_FocusedRowKeepsTargetName 焦点行与非焦点行的目标名必须一致，
// 旧实现焦点行会把目标名换成 provider，移动游标时首列内容会跳变。
func TestAgentSelectDialog_FocusedRowKeepsTargetName(t *testing.T) {
	d := NewAgentSelectDialog(testConfigEntries(9))
	d.SetBounds(120, 30)

	before := stripANSICodes(d.View())
	d.Update(tea.KeyPressMsg{Code: 'j'})
	after := stripANSICodes(d.View())

	for _, target := range []string{"global", "director", "coding", "repo", "chat", "meta", "devops", "browser", "deepthinking"} {
		if strings.Count(before, target) != strings.Count(after, target) {
			t.Errorf("target %q occurrence changed after moving the cursor: %d → %d\n%s",
				target, strings.Count(before, target), strings.Count(after, target), after)
		}
	}
}
