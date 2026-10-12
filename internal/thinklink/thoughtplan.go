package thinklink

import (
	"fmt"
	"regexp"
	"strings"
)

// 结构化 Thought & Plan 块。
//
// 旧格式是自由 markdown（"## Thought & Plan" + 加粗小标题 + 复选框），Go 侧只能靠
// 正则"猜"边界，无法做字段级校验，格式漂移即静默漏提/误提。新格式用显式定界标签
// `<thought>...</thought>` 包裹固定的行式 key 结构，可被确定性提取、解析与校验：
//
//	<thought>
//	goal: <高层目标>
//	step: <上一步及其结果>
//	reason: <为什么执行下一步>
//	plan:
//	- [x] <已完成>
//	- [>] <进行中，至多一条>
//	- [ ] <待处理>
//	</thought>
const (
	// ThoughtOpenTag 结构化块起始标签。
	ThoughtOpenTag = "<thought>"
	// ThoughtCloseTag 结构化块结束标签。
	ThoughtCloseTag = "</thought>"
)

// PlanStatus 计划条目状态。
type PlanStatus int

const (
	// PlanPending 待处理，标记 "[ ]"。
	PlanPending PlanStatus = iota
	// PlanCurrent 进行中，标记 "[>]"，一个块内至多一条。
	PlanCurrent
	// PlanDone 已完成，标记 "[x]"。
	PlanDone
)

// Marker 返回状态对应的文本标记（与 prompt 约定一致）。
func (s PlanStatus) Marker() string {
	switch s {
	case PlanDone:
		return "[x]"
	case PlanCurrent:
		return "[>]"
	default:
		return "[ ]"
	}
}

// PlanItem 单条计划。
type PlanItem struct {
	Status PlanStatus
	Text   string
}

// ThoughtPlan 一个结构化思考与计划块的解析结果。
// 字段全部为尽力解析：即使格式不完整，已识别的部分仍会填充，
// 保证 thinklink "永不丢计划" 的底线；是否合规由 Validate 单独判定。
type ThoughtPlan struct {
	Goal   string
	Step   string
	Reason string
	Plan   []PlanItem
}

// Empty 报告是否没有解析出任何结构化字段（用于区分旧 markdown 内容）。
func (tp ThoughtPlan) Empty() bool {
	return strings.TrimSpace(tp.Goal) == "" &&
		strings.TrimSpace(tp.Step) == "" &&
		strings.TrimSpace(tp.Reason) == "" &&
		len(tp.Plan) == 0
}

// thoughtBlockRe 匹配 <thought>...</thought>（忽略大小写、跨行、非贪婪）。
var thoughtBlockRe = regexp.MustCompile(`(?is)<thought>(.*?)</thought>`)

// blankRunRe 折叠删块后残留的连续空行。
var blankRunRe = regexp.MustCompile(`\n{3,}`)

// ExtractThoughtBlocks 从自由文本中提取所有结构化 <thought> 块的内部内容
// （不含定界标签，已 TrimSpace）。无匹配或全为空返回 nil。
func ExtractThoughtBlocks(content string) []string {
	matches := thoughtBlockRe.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return nil
	}
	blocks := make([]string, 0, len(matches))
	for _, mm := range matches {
		inner := strings.TrimSpace(mm[1])
		if inner == "" {
			continue
		}
		blocks = append(blocks, inner)
	}
	if len(blocks) == 0 {
		return nil
	}
	return blocks
}

// SplitThoughtBlocks 按出现顺序拆出结构化 <thought> 块（不含定界标签，已 TrimSpace），
// 并返回删掉这些块之后的剩余正文 —— 展示时块要单独渲染成结构化面板，正文才走 markdown。
// 无匹配时 blocks 为 nil、rest 即原内容。
func SplitThoughtBlocks(content string) (blocks []string, rest string) {
	matches := thoughtBlockRe.FindAllStringSubmatchIndex(content, -1)
	if len(matches) == 0 {
		return nil, content
	}

	var kept strings.Builder
	cursor := 0
	for _, mm := range matches {
		// mm[0]:mm[1] 是含标签的整块，mm[2]:mm[3] 是标签内的正文。
		if inner := strings.TrimSpace(content[mm[2]:mm[3]]); inner != "" {
			blocks = append(blocks, inner)
		}
		kept.WriteString(content[cursor:mm[0]])
		cursor = mm[1]
	}
	kept.WriteString(content[cursor:])
	if len(blocks) == 0 {
		return nil, content
	}
	return blocks, blankRunRe.ReplaceAllString(strings.TrimSpace(kept.String()), "\n\n")
}

// keyLineRe 匹配 "key: value" 行（key 仅由字母与空格组成，取第一个冒号分隔）。
var keyLineRe = regexp.MustCompile(`^([A-Za-z][A-Za-z ]*?)\s*:\s*(.*)$`)

// planItemRe 匹配计划条目：可选前导列表符/序号 + 状态标记 + 文本。
var planItemRe = regexp.MustCompile(`^(?:[-*•]\s*)?(?:\d+[.)]\s*)?\[(x|X|>| |\-)\]\s*(.*)$`)

// planItemNumRe 去掉标记之后残留的序号前缀（如 "[x] 1. foo" 中的 "1. "）。
var planItemNumRe = regexp.MustCompile(`^\d+[.)]\s*`)

// ParseThoughtPlan 解析一个块（可含或不含 <thought> 定界标签）为结构化字段。
// 解析是宽容的：无法识别的行按"上一字段的续行"处理，尽量不丢信息。
func ParseThoughtPlan(block string) ThoughtPlan {
	var tp ThoughtPlan
	block = stripThoughtTags(block)
	currentKey := ""
	for _, raw := range strings.Split(block, "\n") {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		trimmed = strings.TrimLeft(trimmed, "#>*- \t")
		trimmed = strings.TrimSpace(trimmed)
		if trimmed == "" {
			continue
		}

		// 计划条目优先（其文本可能含冒号，须先于 key 判定）。
		if status, text, ok := parsePlanItem(trimmed); ok {
			tp.Plan = append(tp.Plan, PlanItem{Status: status, Text: text})
			currentKey = "plan"
			continue
		}

		if key, val, ok := splitKeyLine(trimmed); ok {
			currentKey = key
			switch key {
			case "goal":
				tp.Goal = joinValue(tp.Goal, val)
			case "step":
				tp.Step = joinValue(tp.Step, val)
			case "reason":
				tp.Reason = joinValue(tp.Reason, val)
			case "plan":
				// "plan:" 本身不带内容，仅切换 currentKey。
			}
			continue
		}

		// 续行：拼接到当前字段（plan 段的裸文本忽略）。
		switch currentKey {
		case "goal":
			tp.Goal = joinValue(tp.Goal, trimmed)
		case "step":
			tp.Step = joinValue(tp.Step, trimmed)
		case "reason":
			tp.Reason = joinValue(tp.Reason, trimmed)
		}
	}
	return tp
}

// Validate 返回块的格式问题列表；返回 nil 表示完全合规。
// 校验项：goal/reason 必填；进行中条目至多一条。
func (tp ThoughtPlan) Validate() []string {
	var issues []string
	if strings.TrimSpace(tp.Goal) == "" {
		issues = append(issues, "missing goal")
	}
	if strings.TrimSpace(tp.Reason) == "" {
		issues = append(issues, "missing reason")
	}
	current := 0
	for _, it := range tp.Plan {
		if it.Status == PlanCurrent {
			current++
		}
	}
	if current > 1 {
		issues = append(issues, fmt.Sprintf("%d in-progress plan items (want at most 1)", current))
	}
	return issues
}

// stripThoughtTags 去掉可能存在的 <thought>/</thought> 定界标签。
func stripThoughtTags(s string) string {
	lower := strings.ToLower(s)
	if i := strings.Index(lower, ThoughtOpenTag); i >= 0 {
		s = s[i+len(ThoughtOpenTag):]
	}
	if j := strings.LastIndex(strings.ToLower(s), ThoughtCloseTag); j >= 0 {
		s = s[:j]
	}
	return strings.TrimSpace(s)
}

// splitKeyLine 解析 "key: value"，返回归一化 key（goal/step/reason/plan）、value 与是否命中。
// 非上述四个 key 的行视为未命中（交由调用方按续行处理）。
func splitKeyLine(line string) (key, value string, ok bool) {
	m := keyLineRe.FindStringSubmatch(line)
	if m == nil {
		return "", "", false
	}
	norm := normalizeKey(m[1])
	if norm == "" {
		return "", "", false
	}
	return norm, strings.TrimSpace(m[2]), true
}

// normalizeKey 将各种写法归一到 goal/step/reason/plan；无法识别返回空串。
func normalizeKey(k string) string {
	k = strings.ToLower(strings.Join(strings.Fields(k), " "))
	switch k {
	case "goal", "current goal":
		return "goal"
	case "step", "current step":
		return "step"
	case "reason", "reasoning":
		return "reason"
	case "plan", "plan update":
		return "plan"
	default:
		return ""
	}
}

// parsePlanItem 解析计划条目行，返回状态、文本与是否命中。
func parsePlanItem(line string) (PlanStatus, string, bool) {
	m := planItemRe.FindStringSubmatch(line)
	if m == nil {
		return PlanPending, "", false
	}
	var status PlanStatus
	switch m[1] {
	case "x", "X":
		status = PlanDone
	case ">":
		status = PlanCurrent
	default:
		status = PlanPending
	}
	text := planItemNumRe.ReplaceAllString(m[2], "")
	return status, strings.TrimSpace(text), true
}

// joinValue 将续接内容拼到已有值上（单空格分隔）。
func joinValue(existing, add string) string {
	add = strings.TrimSpace(add)
	if add == "" {
		return existing
	}
	if existing == "" {
		return add
	}
	return existing + " " + add
}
