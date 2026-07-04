package compact

import (
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"unicode"

	"codeactor/internal/llm"
)

// ─────────────────────────────────────────────────────────
// 状态数据结构
// ─────────────────────────────────────────────────────────

// FileState 文件操作状态
type FileState struct {
	Path   string // 文件路径
	Action string // 操作类型：reading / read / modified / writing / wrote
}

// PlanState 计划状态
type PlanState struct {
	Title      string   // 计划标题
	Steps      []string // 计划步骤
	CurrentStep int    // 当前执行步骤索引（-1 = 未开始）
	Status     string   // 计划状态：pending / in_progress / completed
}

// SkillState 技能状态
type SkillState struct {
	Name    string // 技能名称
	Level   string // 技能等级
	Context string // 上下文信息
}

// ToolDecl 工具声明
type ToolDecl struct {
	Name        string // 工具名称
	Description string // 工具描述
	UsedInRound int    // 在哪个对话轮次中使用（-1 = 声明）
}

// ExtractedState 从对话中提取的关键状态
type ExtractedState struct {
	Files    []FileState    // 文件操作记录
	Plan     *PlanState     // 当前计划
	Skills   []SkillState   // 技能状态
	ToolDecls []ToolDecl    // 工具声明
}

// CompensateResult 状态补偿结果
type CompensateResult struct {
	FilesReinjected  int     // 重新注入的文件数
	PlanReinjected   bool    // 是否重新注入计划
	SkillsReinjected int     // 重新注入的技能数
	ToolsReinjected  int     // 重新注入的工具声明数
	TokensAdded      int     // 新增 token 数
	Injected         bool    // 是否执行了注入
}

// String 返回补偿结果的可读字符串
func (cr *CompensateResult) String() string {
	if !cr.Injected {
		return "StateCompensation{skipped}"
	}
	parts := make([]string, 0, 5)
	if cr.FilesReinjected > 0 {
		parts = append(parts, fmt.Sprintf("files=%d", cr.FilesReinjected))
	}
	if cr.PlanReinjected {
		parts = append(parts, "plan=yes")
	}
	if cr.SkillsReinjected > 0 {
		parts = append(parts, fmt.Sprintf("skills=%d", cr.SkillsReinjected))
	}
	if cr.ToolsReinjected > 0 {
		parts = append(parts, fmt.Sprintf("tools=%d", cr.ToolsReinjected))
	}
	return fmt.Sprintf("StateCompensation{%s, tokens_added=%d}", strings.Join(parts, ", "), cr.TokensAdded)
}

// ─────────────────────────────────────────────────────────
// 状态提取器
// ─────────────────────────────────────────────────────────

// StateExtractor 使用正则表达式从消息中提取状态信息
type StateExtractor struct {
	// 文件操作模式
	fileActionRe *regexp.Regexp

	// 计划模式
	planRe *regexp.Regexp
	stepRe *regexp.Regexp
	stepNumRe *regexp.Regexp

	// 工具声明模式
	toolDeclRe *regexp.Regexp

	// 技能模式
	skillRe *regexp.Regexp
}

// NewStateExtractor 编译正则表达式，创建状态提取器
func NewStateExtractor() *StateExtractor {
	se := &StateExtractor{}

	// 文件操作模式：匹配 "reading <path>", "read <path>", "modified <path>", "writing <path>", "wrote <path>"
	// 文件路径通常是带斜杠的路径，如 src/foo/bar.go 或 ./foo/bar
	se.fileActionRe = regexp.MustCompile(
		`(?:reading|read|modified|writing|wrote)\s+(?:the\s+)?(?:file\s+)?(?:` + "`" + `)([^` + "`" + `]+)` + "`" + `|` +
			`(?:reading|read|modified|writing|wrote)\s+(?:the\s+)?(?:file\s+)?(\.[^\s,;]{3,})`,
	)

	// 计划模式：匹配 "plan: <title>" 或 "Plan: <title>" 或 "# Plan" 标题
	se.planRe = regexp.MustCompile(
		`(?i)(?:^|\n)\s*(?:#?\s*)?(plan|计划)[\s:：]\s*(.+?)(?:\n|\r|$)`,
	)

	// 计划步骤模式：匹配 "- <step>" 或 "1. <step>" 或 "[ ] <step>" 等
	se.stepRe = regexp.MustCompile(
		`(?m)^\s*(?:[-*•]|\d+\.)\s+(?:\[.\]\s+)?(.+)$`,
	)

	// 步骤编号模式：匹配 "1. xxx" 中的编号
	se.stepNumRe = regexp.MustCompile(`^(\d+)\.`)

	// 工具声明模式：匹配 "tools:" 或 "Tool Declaration:" 或可用的工具列表
	se.toolDeclRe = regexp.MustCompile(
		`(?i)(?:^|\n)\s*(?:#?\s*)?(?:tools?|工具)[\s:：]\s*(.+?)(?:\n|\r|$)`,
	)

	// 技能模式：匹配 "skill: <name>" 或 "Skills:" 或 "<name> skill"
	se.skillRe = regexp.MustCompile(
		`(?i)(?:^|\n)\s*(?:#?\s*)?(?:skill|技能)[s]?[\s:：]\s*([a-zA-Z][a-zA-Z0-9_-]{1,30})(?:\s*\(([^)]+)\))?`,
	)

	return se
}

// ExtractState 从消息列表中提取关键状态
// 在压缩前调用，扫描所有消息以捕获完整上下文
func (se *StateExtractor) ExtractState(msgs []llm.Message) *ExtractedState {
	state := &ExtractedState{
		Files:   make([]FileState, 0),
		Plan:    nil,
		Skills:  make([]SkillState, 0),
		ToolDecls: make([]ToolDecl, 0),
	}

	// 追踪已访问的文件（去重，保留最后一次操作）
	seenFiles := make(map[string]int) // path -> index in state.Files

	// 追踪是否找到计划
	planFound := false

	for _, msg := range msgs {
		content := msg.Content
		if content == "" {
			continue
		}

		// ── 提取文件状态 ──
		matches := se.fileActionRe.FindAllStringSubmatch(content, -1)
		for _, match := range matches {
			var path string
			if match[1] != "" {
				path = match[1]
			} else if match[2] != "" {
				path = match[2]
			} else {
				continue
			}

			// 提取操作类型
			action := extractActionType(content, match)

			if idx, exists := seenFiles[path]; exists {
				// 更新已有记录
				state.Files[idx].Action = action
			} else {
				state.Files = append(state.Files, FileState{
					Path:   path,
					Action: action,
				})
				seenFiles[path] = len(state.Files) - 1
			}
		}

		// ── 提取计划 ──
		if !planFound {
			planMatches := se.planRe.FindAllStringSubmatch(content, -1)
			if len(planMatches) > 0 {
				planTitle := strings.TrimSpace(planMatches[0][2])
				plan := &PlanState{
					Title:      planTitle,
					Steps:      make([]string, 0),
					CurrentStep: -1,
					Status:     "pending",
				}

				// 提取步骤（从同一消息或后续消息中）
				steps := se.extractStepsFromContent(content)
				if len(steps) > 0 {
					plan.Steps = steps
					plan.Status = "in_progress"
					plan.CurrentStep = 0
				}

				state.Plan = plan
				planFound = true
			}
		}

		// ── 提取工具声明 ──
		toolMatches := se.toolDeclRe.FindAllStringSubmatch(content, -1)
		for _, match := range toolMatches {
			toolsStr := strings.TrimSpace(match[1])
			// 分割多个工具名（用逗号、换行、空格分隔）
			toolNames := splitToolList(toolsStr)
			for _, tn := range toolNames {
				state.ToolDecls = append(state.ToolDecls, ToolDecl{
					Name:        tn,
					Description: "",
					UsedInRound: -1,
				})
			}
		}

		// ── 提取技能 ──
		skillMatches := se.skillRe.FindAllStringSubmatch(content, -1)
		for _, match := range skillMatches {
			skillName := strings.TrimSpace(match[1])
			skillContext := ""
			if len(match) > 2 && match[2] != "" {
				skillContext = strings.TrimSpace(match[2])
			}
			state.Skills = append(state.Skills, SkillState{
				Name:    skillName,
				Level:   "default",
				Context: skillContext,
			})
		}
	}

	// 去重工具声明
	state.ToolDecls = deduplicateToolDecls(state.ToolDecls)

	return state
}

// extractActionType 从匹配位置提取操作类型
func extractActionType(content string, match []string) string {
	// 找到匹配的文件路径在原文中的位置
	var pathStr string
	if match[1] != "" {
		pathStr = match[1]
	} else {
		pathStr = match[2]
	}

	// 在路径前后查找操作关键词
	idx := strings.Index(content, pathStr)
	if idx < 0 {
		return "unknown"
	}

	// 向前查找最多 30 个字符，寻找操作类型
	start := idx - 30
	if start < 0 {
		start = 0
	}
	prefix := strings.ToLower(content[start:idx])

	switch {
	case strings.Contains(prefix, "writing") || strings.Contains(prefix, "wrote"):
		return "writing"
	case strings.Contains(prefix, "reading") || strings.Contains(prefix, " read "):
		return "reading"
	case strings.Contains(prefix, "modified") || strings.Contains(prefix, "edit"):
		return "modified"
	default:
		return "read"
	}
}

// extractStepsFromContent 从内容中提取计划步骤
func (se *StateExtractor) extractStepsFromContent(content string) []string {
	lines := strings.Split(content, "\n")
	var steps []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// 跳过空行和标题
		if trimmed == "" || strings.HasPrefix(trimmed, "#") ||
			strings.HasPrefix(strings.ToLower(trimmed), "plan") {
			continue
		}

		// 匹配步骤格式
		if se.stepRe.MatchString(line) {
			matches := se.stepRe.FindStringSubmatch(line)
			if len(matches) > 1 {
				step := strings.TrimSpace(matches[1])
				// 跳过非步骤内容（如工具调用、代码块）
				if len(step) > 0 && !strings.HasPrefix(step, "```") {
					steps = append(steps, step)
				}
			}
		}
	}

	return steps
}

// ─────────────────────────────────────────────────────────
// 状态补偿注入
// ─────────────────────────────────────────────────────────

// compensateState 将提取的状态重新注入到压缩后的消息列表中
// 在压缩后调用，将关键状态作为一条 System 消息注入到摘要之后、recent 消息之前
//
// 流程：
//  1. 计算可用 token 预算
//  2. 按优先级（Plan > Tools > Files > Skills）构建状态内容
//  3. 在摘要消息之后插入状态注入消息
func (se *StateExtractor) compensateState(
	cc *CompressionContext,
	compressed []llm.Message,
	extracted *ExtractedState,
	engine *Engine,
) *CompensateResult {
	result := &CompensateResult{
		Injected: false,
	}

	// 没有可注入的状态 → 跳过
	if !hasMeaningfulState(extracted) {
		return result
	}

	// 计算可用 token 预算
	availableTokens := se.calculateAvailableTokens(compressed, engine, cc)
	if availableTokens <= 0 {
		slog.Debug("State compensation skipped: no token budget available")
		return result
	}

	// 按优先级构建状态内容：Plan > Tools > Files > Skills
	injectionContent := se.buildStateInjectionContent(extracted, availableTokens)
	if injectionContent == "" {
		return result
	}

	// 计算注入内容的 token 数
	injectedTokens := engine.countMessagesTokens([]llm.Message{{
		Role:    llm.RoleSystem,
		Content: injectionContent,
	}})

	result.TokensAdded = injectedTokens
	result.Injected = true

	// 构建状态注入消息
	stateMsg := llm.Message{
		Role:    llm.RoleSystem,
		Content: "[CONTEXT STATE]\n" + injectionContent,
		IsAnchored: true, // 状态消息锚定，永不压缩
	}

	// 将状态消息注入到摘要之后
	result.FilesReinjected = countReinjectedFiles(extracted)
	result.PlanReinjected = extracted.Plan != nil && extracted.Plan.Title != ""
	result.SkillsReinjected = len(extracted.Skills)
	result.ToolsReinjected = len(extracted.ToolDecls)

	// 在 compressed 消息列表中找到插入位置（摘要消息之后）
	insertIdx := se.findInsertionPoint(compressed)
	if insertIdx >= 0 && insertIdx <= len(compressed) {
		// 在摘要之后插入
		newMsgs := make([]llm.Message, 0, len(compressed)+1)
		newMsgs = append(newMsgs, compressed[:insertIdx]...)
		newMsgs = append(newMsgs, stateMsg)
		newMsgs = append(newMsgs, compressed[insertIdx:]...)
		// 注意：调用方需要更新 compressed 切片
		*(*[]llm.Message)(&compressed) = newMsgs
	}

	slog.Debug("State compensation applied",
		"files", result.FilesReinjected,
		"plan", result.PlanReinjected,
		"skills", result.SkillsReinjected,
		"tools", result.ToolsReinjected,
		"tokens_added", result.TokensAdded)

	return result
}

// findInsertionPoint 找到状态消息的插入位置（摘要消息之后）
// 返回摘要消息的索引，如果未找到则返回 1（system 之后）
func (se *StateExtractor) findInsertionPoint(msgs []llm.Message) int {
	for i, msg := range msgs {
		if msg.Role == llm.RoleSystem && strings.HasPrefix(msg.Content, "[CONTEXT SUMMARY]") {
			return i + 1 // 在摘要之后插入
		}
	}
	// 未找到摘要消息，在第一条消息之后插入
	if len(msgs) > 0 {
		return 1
	}
	return 0
}

// calculateAvailableTokens 计算可用于状态注入的 token 预算
// 预算 = MaxContextTokens - (system_tokens + summary_tokens + recent_tokens + state_msg_overhead)
func (se *StateExtractor) calculateAvailableTokens(
	compressed []llm.Message,
	engine *Engine,
	cc *CompressionContext,
) int {
	if engine == nil {
		return 2000 // 默认预算
	}

	// 基础预算：使用配置的最大 token 数
	budget := engine.config.MaxContextTokens
	if cc != nil && cc.Threshold != nil {
		budget = cc.Threshold.EffectiveWindow
	}

	// 计算已占用 token
	currentTokens := engine.countMessagesTokens(compressed)

	// 为状态消息预留 overhead（约 100 tokens 用于标记和格式）
	overhead := 100

	// 可用预算 = 目标 - 当前 - overhead
	available := budget - currentTokens - overhead
	if available < 0 {
		available = 0
	}

	return available
}

// buildStateInjectionContent 按优先级构建状态注入内容
// 优先级：Plan > Tools > Files > Skills
func (se *StateExtractor) buildStateInjectionContent(state *ExtractedState, maxTokens int) string {
	var parts []string

	// 1. 计划（最高优先级）
	if state.Plan != nil && state.Plan.Title != "" {
		planText := se.formatPlan(state.Plan)
		if planText != "" {
			parts = append(parts, planText)
		}
	}

	// 2. 工具声明
	if len(state.ToolDecls) > 0 {
		toolsText := se.formatToolDecls(state.ToolDecls)
		if toolsText != "" {
			parts = append(parts, toolsText)
		}
	}

	// 3. 文件状态
	if len(state.Files) > 0 {
		filesText := se.formatFileStates(state.Files)
		if filesText != "" {
			parts = append(parts, filesText)
		}
	}

	// 4. 技能状态
	if len(state.Skills) > 0 {
		skillsText := se.formatSkillStates(state.Skills)
		if skillsText != "" {
			parts = append(parts, skillsText)
		}
	}

	if len(parts) == 0 {
		return ""
	}

	content := strings.Join(parts, "\n\n")

	// Token 预算限制：截断尾部以保持预算
	estimatedTokens := len([]rune(content)) / 4 // 粗略估算
	if estimatedTokens > maxTokens && maxTokens > 0 {
		content = se.truncateToBudget(content, maxTokens)
	}

	return content
}

// formatPlan 格式化计划为文本
func (se *StateExtractor) formatPlan(plan *PlanState) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## Plan: %s\n", plan.Title))
	sb.WriteString(fmt.Sprintf("Status: %s\n", plan.Status))

	if plan.CurrentStep >= 0 && plan.CurrentStep < len(plan.Steps) {
		sb.WriteString(fmt.Sprintf("Current Step: %d/%d\n", plan.CurrentStep+1, len(plan.Steps)))
	}

	if len(plan.Steps) > 0 {
		sb.WriteString("Steps:\n")
		maxSteps := 20 // 最多显示 20 步
		if len(plan.Steps) > maxSteps {
			sb.WriteString(fmt.Sprintf("  (showing %d of %d steps)\n", maxSteps, len(plan.Steps)))
			plan.Steps = plan.Steps[:maxSteps]
		}
		for i, step := range plan.Steps {
			marker := "⬜"
			if i == plan.CurrentStep {
				marker = "🔵"
			} else if i < plan.CurrentStep {
				marker = "✅"
			}
			sb.WriteString(fmt.Sprintf("  %s %d. %s\n", marker, i+1, truncateLine(step, 120)))
		}
	}

	return sb.String()
}

// formatToolDecls 格式化工具声明为文本
func (se *StateExtractor) formatToolDecls(tools []ToolDecl) string {
	if len(tools) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## Tools\n")
	for _, t := range tools {
		if t.UsedInRound < 0 {
			// 声明式工具
			if t.Description != "" {
				sb.WriteString(fmt.Sprintf("- %s: %s\n", t.Name, truncateLine(t.Description, 80)))
			} else {
				sb.WriteString(fmt.Sprintf("- %s\n", t.Name))
			}
		} else {
			sb.WriteString(fmt.Sprintf("- %s (used in round %d)\n", t.Name, t.UsedInRound))
		}
	}

	return sb.String()
}

// formatFileStates 格式化文件状态为文本
func (se *StateExtractor) formatFileStates(files []FileState) string {
	if len(files) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## Active Files\n")
	for _, f := range files {
		sb.WriteString(fmt.Sprintf("- %s: %s\n", f.Path, f.Action))
	}

	return sb.String()
}

// formatSkillStates 格式化技能状态为文本
func (se *StateExtractor) formatSkillStates(skills []SkillState) string {
	if len(skills) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## Skills\n")
	for _, s := range skills {
		if s.Context != "" {
			sb.WriteString(fmt.Sprintf("- %s (%s): %s\n", s.Name, s.Level, s.Context))
		} else {
			sb.WriteString(fmt.Sprintf("- %s: %s\n", s.Name, s.Level))
		}
	}

	return sb.String()
}

// truncateToBudget 将内容截断到指定 token 预算内
func (se *StateExtractor) truncateToBudget(content string, maxTokens int) string {
	// 使用字符估算：约 4 字符 = 1 token
	maxChars := maxTokens * 4
	if len([]rune(content)) <= maxChars {
		return content
	}

	// 从尾部截断，保留最后 256 字符作为尾部摘要
	tailLen := 256
	if len([]rune(content)) <= tailLen+maxChars {
		return content
	}

	runes := []rune(content)
	head := string(runes[:maxChars])
	tail := string(runes[len(runes)-tailLen:])

	// 在最近的行边界截断头部
	if idx := strings.LastIndex(head, "\n"); idx > maxChars/2 {
		head = head[:idx]
	}

	return head + "\n\n... [state truncated to fit token budget] ...\n\n" + tail
}

// ─────────────────────────────────────────────────────────
// 工具函数
// ─────────────────────────────────────────────────────────

// hasMeaningfulState 检查是否有值得注入的状态
func hasMeaningfulState(state *ExtractedState) bool {
	if state == nil {
		return false
	}
	if len(state.Files) > 0 {
		return true
	}
	if state.Plan != nil && state.Plan.Title != "" {
		return true
	}
	if len(state.Skills) > 0 {
		return true
	}
	if len(state.ToolDecls) > 0 {
		return true
	}
	return false
}

// countReinjectedFiles 计算重新注入的文件数
func countReinjectedFiles(state *ExtractedState) int {
	if state == nil {
		return 0
	}
	return len(state.Files)
}

// splitToolList 分割工具列表字符串为单个工具名
func splitToolList(s string) []string {
	var tools []string
	// 按换行、逗号、分号、竖线分割
	sepRe := regexp.MustCompile(`[\n,;|]`)
	parts := sepRe.Split(s, -1)
	for _, p := range parts {
		p = strings.TrimSpace(p)
		// 移除 markdown 列表标记
		p = strings.TrimLeft(p, "-*• ")
		p = strings.TrimSpace(p)
		if p != "" {
			tools = append(tools, p)
		}
	}
	return tools
}

// deduplicateToolDecls 去重工具声明
func deduplicateToolDecls(tools []ToolDecl) []ToolDecl {
	seen := make(map[string]bool)
	result := make([]ToolDecl, 0, len(tools))
	for _, t := range tools {
		if !seen[t.Name] {
			seen[t.Name] = true
			result = append(result, t)
		}
	}
	return result
}

// truncateLine 截断行到最大长度
func truncateLine(line string, maxLen int) string {
	runes := []rune(line)
	if len(runes) <= maxLen {
		return line
	}
	// 在 word boundary 处截断
	truncated := string(runes[:maxLen])
	if idx := findWordBoundary(truncated); idx > 0 {
		truncated = truncated[:idx]
	}
	return truncated + "..."
}

// findWordBoundary 在字符串末尾附近寻找单词边界
func findWordBoundary(s string) int {
	runes := []rune(s)
	// 从末尾倒数最多 20 个字符寻找边界
	searchStart := len(runes) - 20
	if searchStart < 0 {
		searchStart = 0
	}
	for i := len(runes) - 1; i >= searchStart; i-- {
		if unicode.IsSpace(runes[i]) || runes[i] == '(' || runes[i] == '[' {
			return i
		}
	}
	return 0
}
