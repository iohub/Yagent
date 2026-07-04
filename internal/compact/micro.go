package compact

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"codeactor/internal/llm"
)

// ─────────────────────────────────────────────────────────
// Micro-compression: 语义占位符替换（Layer 3）
// ─────────────────────────────────────────────────────────

// MicroMetadata 从 tool_call 参数中提取的元数据结构
type MicroMetadata struct {
	Command      string // run_bash: 执行的命令
	ExitCode     int    // run_bash: 退出码（默认 -1 表示未知）
	FilePath     string // read_file: 文件路径
	LineCount    int    // 所有工具: 行数（默认 -1 表示未知）
	SizeBytes    int    // read_file: 文件大小（字节）
	ListPath     string // list_files: 列表路径
	EntryCount   int    // list_files: 条目数
	Pattern      string // grep: 搜索模式
	MatchCount   int    // grep: 匹配数
	Query        string // search: 搜索查询
	ResultCount  int    // search: 结果数
	RawArgs      string // 原始参数 JSON（备用）
}

// MicroResult 微压缩结果
type MicroResult struct {
	// CompressedCount 被压缩的消息数量
	CompressedCount int `json:"compressed_count"`
	// TokensSaved 估算节省的 token 数
	TokensSaved int `json:"tokens_saved"`
}

// microPlaceholderTemplate 占位符模板（按工具类型）
var microPlaceholderTemplates = map[string]string{
	"run_bash":   "[bash: {command} → exit={exit_code}, {line_count} lines]",
	"read_file":  "[read_file: {file_path} ({line_count} lines, {size})]",
	"list_files": "[list_files: {path} → {entry_count} entries]",
	"grep":       "[grep: {pattern} → {match_count} matches]",
	"search":     "[search: {query} → {result_count} results]",
}

// microMinOutputLen 触发微压缩的最小输出长度（字节）
// 短输出无需占位符替换
const microMinOutputLen = 512

// microCompress 将旧轮次中 verbose 工具的大段输出替换为语义占位符，
// 保留元信息（工具名、退出码、文件路径等），不调用 LLM。
//
// 参数：
//   - msgs: 消息列表（不会被修改，内部深拷贝）
//   - keepBoundary: 保留边界索引，只处理 idx < keepBoundary 的旧消息
//
// 返回：
//   - *MicroResult: 压缩统计信息
//   - []llm.Message: 压缩后的消息列表
func (e *Engine) microCompress(msgs []llm.Message, keepBoundary int) (*MicroResult, []llm.Message) {
	if len(msgs) == 0 || keepBoundary <= 0 {
		return &MicroResult{CompressedCount: 0, TokensSaved: 0}, msgs
	}

	// 构建白名单集合
	microTools := e.buildMicroToolSet()
	if len(microTools) == 0 {
		return &MicroResult{CompressedCount: 0, TokensSaved: 0}, msgs
	}

	// 深拷贝消息列表，避免修改原始数据
	result := make([]llm.Message, len(msgs))
	for i := range msgs {
		result[i] = msgs[i]
	}

	var (
		compressedCount int
		totalSaved      int
	)

	// 只处理 keepBoundary 之前的旧消息
	for i := 0; i < keepBoundary && i < len(result); i++ {
		msg := &result[i]

		// 只处理 tool 角色
		if msg.Role != llm.RoleTool {
			continue
		}

		// 锚定消息不处理
		if msg.IsAnchored {
			continue
		}

		// 检查工具名是否在白名单中
		toolName := msg.ToolName
		if !microTools[toolName] {
			continue
		}

		// 检查输出是否足够长才进行微压缩
		if len(msg.Content) < microMinOutputLen {
			continue
		}

		// 查找对应的 tool_call 消息，提取元数据
		meta := e.extractMicroMetadata(result, i, toolName)

		// 对于 read_file，用原始内容估算行数和大小
		if toolName == "read_file" {
			meta.SizeBytes = len(msg.Content)
			meta.LineCount = CountToolResultLines(msg.Content)
		}

		// 构建语义占位符
		placeholder := e.buildMicroPlaceholder(toolName, meta)

		// 计算节省的 token（粗略估算：4 字符 = 1 token）
		originalTokens := len(msg.Content) / 4
		placeholderTokens := len(placeholder) / 4
		saved := originalTokens - placeholderTokens
		if saved <= 0 {
			continue
		}

		// 记录截断标记
		msg.TruncationMarker = &llm.TruncationMarker{
			ToolName:       toolName,
			OriginalLen:    len(msg.Content),
			OmittedLen:     len(msg.Content) - len(placeholder),
			TruncationPass: 0,
		}

		// 替换内容
		msg.Content = placeholder
		compressedCount++
		totalSaved += saved
	}

	return &MicroResult{
		CompressedCount: compressedCount,
		TokensSaved:     totalSaved,
	}, result
}

// buildMicroToolSet 将 MicroCompressTools 配置转换为快速查找集合
func (e *Engine) buildMicroToolSet() map[string]bool {
	tools := e.config.MicroCompressTools
	if len(tools) == 0 {
		return nil
	}
	set := make(map[string]bool, len(tools))
	for _, t := range tools {
		set[t] = true
	}
	return set
}

// extractMicroMetadata 从消息中提取元数据
// 通过查找 tool_result 前面的 tool_call 消息来解析参数
func (e *Engine) extractMicroMetadata(msgs []llm.Message, idx int, toolName string) *MicroMetadata {
	meta := &MicroMetadata{
		RawArgs: "",
	}

	// 查找前面的 tool_call 消息
	toolCallMsg := e.findPrecedingToolCall(msgs, idx)
	if toolCallMsg == nil {
		return meta
	}

	// 尝试从 ToolCall 中提取参数
	if len(toolCallMsg.ToolCalls) > 0 {
		args := toolCallMsg.ToolCalls[0].Function.Arguments
		meta.RawArgs = args
		e.parseToolCallArgs(toolCallMsg, meta)
	}

	// 尝试从 tool_result 内容中提取 exit_code（run_bash）
	if toolName == "run_bash" {
		meta.ExitCode = e.parseExitCode(msgs[idx].Content)
	}

	return meta
}

// buildMicroPlaceholder 根据工具类型和元数据构建语义占位符
func (e *Engine) buildMicroPlaceholder(toolName string, meta *MicroMetadata) string {
	template, ok := microPlaceholderTemplates[toolName]
	if !ok {
		return fmt.Sprintf("[%s: %d bytes]", toolName, len(meta.RawArgs))
	}

	result := template
	result = strings.ReplaceAll(result, "{command}", safeEscape(meta.Command))
	result = strings.ReplaceAll(result, "{exit_code}", fmt.Sprintf("%d", meta.ExitCode))
	result = strings.ReplaceAll(result, "{file_path}", safeEscape(meta.FilePath))
	result = strings.ReplaceAll(result, "{line_count}", fmt.Sprintf("%d", meta.LineCount))
	result = strings.ReplaceAll(result, "{size}", formatSize(meta.SizeBytes))
	result = strings.ReplaceAll(result, "{path}", safeEscape(meta.ListPath))
	result = strings.ReplaceAll(result, "{entry_count}", fmt.Sprintf("%d", meta.EntryCount))
	result = strings.ReplaceAll(result, "{pattern}", safeEscape(meta.Pattern))
	result = strings.ReplaceAll(result, "{match_count}", fmt.Sprintf("%d", meta.MatchCount))
	result = strings.ReplaceAll(result, "{query}", safeEscape(meta.Query))
	result = strings.ReplaceAll(result, "{result_count}", fmt.Sprintf("%d", meta.ResultCount))

	return result
}

// findToolNameForResult 查找 tool_result 对应的工具名
// 通过遍历前面的消息找到最近的 tool_call 消息，匹配 ToolCallID
func (e *Engine) findToolNameForResult(msgs []llm.Message, idx int) string {
	// tool_result 消息本身可能已携带 ToolName
	msg := msgs[idx]
	if msg.ToolName != "" {
		return msg.ToolName
	}

	// 遍历前面的消息，查找匹配的 tool_call
	toolCallID := msg.ToolCallID
	if toolCallID == "" {
		return ""
	}

	for i := idx - 1; i >= 0; i-- {
		prev := msgs[i]
		if prev.Role != llm.RoleAssistant {
			continue
		}
		for _, tc := range prev.ToolCalls {
			if tc.ID == toolCallID {
				return tc.Function.Name
			}
		}
	}
	return ""
}

// findPrecedingToolCall 查找前面最近的 tool_call 消息
// 从 idx 往前遍历，找到最近的包含 ToolCalls 的 assistant 消息
func (e *Engine) findPrecedingToolCall(msgs []llm.Message, idx int) *llm.Message {
	// 优先使用 ToolCallID 精确定位
	targetID := msgs[idx].ToolCallID
	if targetID != "" {
		for i := idx - 1; i >= 0; i-- {
			msg := &msgs[i]
			if msg.Role != llm.RoleAssistant {
				continue
			}
			for _, tc := range msg.ToolCalls {
				if tc.ID == targetID {
					return msg
				}
			}
		}
	}

	// 备用方案：找前面最近的 assistant 消息
	for i := idx - 1; i >= 0; i-- {
		msg := &msgs[i]
		if msg.Role == llm.RoleAssistant && len(msg.ToolCalls) > 0 {
			return msg
		}
	}

	return nil
}

// parseToolCallArgs 从 tool_call 参数中提取命令/路径等元数据
func (e *Engine) parseToolCallArgs(msg *llm.Message, meta *MicroMetadata) {
	if len(msg.ToolCalls) == 0 {
		return
	}
	args := msg.ToolCalls[0].Function.Arguments
	if args == "" {
		return
	}

	// 尝试解析 JSON 参数
	var params map[string]interface{}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return
	}

	toolName := msg.ToolCalls[0].Function.Name

	switch toolName {
	case "run_bash":
		if v, ok := params["command"].(string); ok {
			meta.Command = v
		}
	case "read_file":
		if v, ok := params["file_path"].(string); ok {
			meta.FilePath = v
		} else if v, ok := params["path"].(string); ok {
			meta.FilePath = v
		}
	case "list_files":
		if v, ok := params["path"].(string); ok {
			meta.ListPath = v
		} else if v, ok := params["directory"].(string); ok {
			meta.ListPath = v
		}
	case "grep":
		if v, ok := params["pattern"].(string); ok {
			meta.Pattern = v
		}
	case "search":
		if v, ok := params["query"].(string); ok {
			meta.Query = v
		} else if v, ok := params["search_query"].(string); ok {
			meta.Query = v
		}
	}
}

// parseExitCode 从 bash 输出中解析退出码
// 通常 bash 输出末尾会有类似 "Exit code: 0" 或 "exit status 1" 的信息
func (e *Engine) parseExitCode(content string) int {
	// 模式 1: "Exit code: 0" 或 "Exit code 0"
	re1 := regexp.MustCompile(`(?i)(?:Exit\s+code)[\s:]+(\d+)`)
	if matches := re1.FindStringSubmatch(content); len(matches) > 1 {
		var code int
		fmt.Sscanf(matches[1], "%d", &code)
		return code
	}

	// 模式 2: "exit status 1" 或 "exit 1"
	re2 := regexp.MustCompile(`exit\s+(?:status\s+)?(\d+)`)
	if matches := re2.FindStringSubmatch(content); len(matches) > 1 {
		var code int
		fmt.Sscanf(matches[1], "%d", &code)
		return code
	}

	// 模式 3: 末尾的数字行（最后一行只有数字）
	lines := strings.Split(strings.TrimSpace(content), "\n")
	if len(lines) > 0 {
		lastLine := strings.TrimSpace(lines[len(lines)-1])
		var code int
		if _, err := fmt.Sscanf(lastLine, "%d", &code); err == nil && lastLine == fmt.Sprintf("%d", code) {
			return code
		}
	}

	return -1 // 未知退出码
}

// formatSize 将字节数格式化为人类可读的大小
func formatSize(bytes int) string {
	if bytes < 1024 {
		return fmt.Sprintf("%dB", bytes)
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%.1fKB", float64(bytes)/1024)
	}
	if bytes < 1024*1024*1024 {
		return fmt.Sprintf("%.1fMB", float64(bytes)/(1024*1024))
	}
	return fmt.Sprintf("%.1fGB", float64(bytes)/(1024*1024*1024))
}

// safeEscape 对占位符中的字符串进行安全转义
// 防止特殊字符破坏占位符格式
func safeEscape(s string) string {
	if s == "" {
		return "<unknown>"
	}
	// 截断过长字符串
	if len(s) > 100 {
		s = s[:100] + "…"
	}
	// 替换可能破坏占位符格式的字符
	s = strings.ReplaceAll(s, "]", "\\]")
	s = strings.ReplaceAll(s, "[", "\\[")
	// 清理换行符
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, "\r", "\\r")
	// 对文件路径使用 basename
	if strings.Contains(s, "/") {
		s = filepath.Base(s)
	}
	return s
}

// ─────────────────────────────────────────────────────────
// 工具结果行数统计（用于 read_file 等工具）
// ─────────────────────────────────────────────────────────

// CountToolResultLines 统计工具输出内容的行数
func CountToolResultLines(content string) int {
	if content == "" {
		return 0
	}
	lines := strings.Split(content, "\n")
	// 如果最后一行是空字符串（内容以换行结尾），不计入
	if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		return len(lines) - 1
	}
	return len(lines)
}
