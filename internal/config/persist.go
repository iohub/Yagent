package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// useProviderKeyRe 匹配 use_provider 键行（作用于 trim 后的行）。
var useProviderKeyRe = regexp.MustCompile(`^use_provider\s*=`)

// routingModeKeyRe 匹配 [routing] 段中的 mode 键行（作用于 trim 后的行）。
var routingModeKeyRe = regexp.MustCompile(`^mode\s*=`)

// llmTimeoutKeyRe 匹配 [llm] 段中的 timeout 键行（作用于 trim 后的行）。
var llmTimeoutKeyRe = regexp.MustCompile(`^timeout\s*=`)

// PersistLLMTimeout 将 LLM API 调用超时持久化写入 config.toml 的 [llm] 段
// timeout 键（如 timeout = "10m"），文本级精确更新，保留注释与格式。
// 段或键缺失时自动补齐。d 必须 > 0。值格式由 FormatLLMTimeout 生成，
// 可被 time.ParseDuration 与 TOML 解析正确读回。
func PersistLLMTimeout(path string, d time.Duration) error {
	if path == "" {
		return fmt.Errorf("config: config file path is empty")
	}
	if d <= 0 {
		return fmt.Errorf("config: invalid LLM timeout %s (must be positive)", d)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("config: read config file: %w", err)
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}

	masked := multilineMask(lines)
	const secHeader = "[llm]"
	value := tomlBasicString(FormatLLMTimeout(d))
	timeoutLine := func(indent string) string { return indent + "timeout = " + value }

	// 定位段头：仅接受 trim 后精确等于 "[llm]" 的行，
	// 防止误匹配任何名为 [xxx.llm] 的子表。
	secStart := -1
	for i, line := range lines {
		if !masked[i] && strings.TrimSpace(line) == secHeader {
			secStart = i
			break
		}
	}
	if secStart < 0 {
		// 段缺失 → 文件末尾追加：空行 + 段头 + 键
		out := lines
		if len(out) > 0 && out[len(out)-1] != "" {
			out = append(out, "")
		}
		out = append(out, secHeader, timeoutLine(""), "")
		return atomicWriteFile(path, []byte(strings.Join(out, "\n")))
	}

	// 段范围：段头下一行起，至下一个段头（含 [[...]] 数组表头）或 EOF
	secEnd := len(lines)
	for i := secStart + 1; i < len(lines); i++ {
		if !masked[i] && strings.HasPrefix(strings.TrimSpace(lines[i]), "[") {
			secEnd = i
			break
		}
	}

	keyRow, indent, lastNonEmpty := -1, "", secStart
	for i := secStart + 1; i < secEnd; i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" {
			continue
		}
		lastNonEmpty = i
		if keyRow < 0 && !masked[i] && llmTimeoutKeyRe.MatchString(trimmed) {
			keyRow = i
		}
		if indent == "" && !strings.HasPrefix(trimmed, "#") {
			indent = leadingWhitespace(lines[i])
		}
	}

	if keyRow >= 0 {
		eq := strings.Index(lines[keyRow], "=")
		prefix := lines[keyRow][:eq+1]
		lines[keyRow] = prefix + " " + value
	} else {
		// 键缺失 → 段内最后一个非空行之后插入
		updated := make([]string, 0, len(lines)+1)
		updated = append(updated, lines[:lastNonEmpty+1]...)
		updated = append(updated, timeoutLine(indent))
		updated = append(updated, lines[lastNonEmpty+1:]...)
		lines = updated
	}
	return atomicWriteFile(path, []byte(strings.Join(lines, "\n")))
}

// PersistRoutingMode 将路由模式（"auto" | "fixed"）持久化写入 config.toml 的
// [routing] 段，文本级精确更新 mode 键，保留注释与格式。段或键缺失时自动补齐。
func PersistRoutingMode(path, mode string) error {
	if path == "" {
		return fmt.Errorf("config: config file path is empty")
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != "auto" && mode != "fixed" {
		return fmt.Errorf("config: invalid routing mode %q (want auto|fixed)", mode)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("config: read config file: %w", err)
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}

	masked := multilineMask(lines)
	const secHeader = "[routing]"
	modeLine := func(indent string) string { return indent + "mode = " + tomlBasicString(mode) }

	secStart := -1
	for i, line := range lines {
		if !masked[i] && strings.TrimSpace(line) == secHeader {
			secStart = i
			break
		}
	}
	if secStart < 0 {
		out := lines
		if len(out) > 0 && out[len(out)-1] != "" {
			out = append(out, "")
		}
		out = append(out, secHeader, modeLine(""), "")
		return atomicWriteFile(path, []byte(strings.Join(out, "\n")))
	}

	secEnd := len(lines)
	for i := secStart + 1; i < len(lines); i++ {
		if !masked[i] && strings.HasPrefix(strings.TrimSpace(lines[i]), "[") {
			secEnd = i
			break
		}
	}

	keyRow, indent, lastNonEmpty := -1, "", secStart
	for i := secStart + 1; i < secEnd; i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" {
			continue
		}
		lastNonEmpty = i
		if keyRow < 0 && !masked[i] && routingModeKeyRe.MatchString(trimmed) {
			keyRow = i
		}
		if indent == "" && !strings.HasPrefix(trimmed, "#") {
			indent = leadingWhitespace(lines[i])
		}
	}

	if keyRow >= 0 {
		eq := strings.Index(lines[keyRow], "=")
		prefix := lines[keyRow][:eq+1]
		lines[keyRow] = prefix + " " + tomlBasicString(mode)
	} else {
		updated := make([]string, 0, len(lines)+1)
		updated = append(updated, lines[:lastNonEmpty+1]...)
		updated = append(updated, modeLine(indent))
		updated = append(updated, lines[lastNonEmpty+1:]...)
		lines = updated
	}
	return atomicWriteFile(path, []byte(strings.Join(lines, "\n")))
}

// PersistUseProvider 将 provider 选择持久化写入 config.toml：文本级精确更新，
// 只改动目标段中的 use_provider 键，保留注释、空行与原有格式。
// 禁止整文件 TOML 重编码——那会丢失用户注释与字段顺序。
// section 为完整表头名（不含方括号），如 "global.llm"、"agents.llm.codewriter"、
// "tools.llm.deepthinking"。
func PersistUseProvider(path, section, provider string) error {
	if path == "" {
		return fmt.Errorf("config: config file path is empty")
	}
	if section == "" {
		return fmt.Errorf("config: section is empty")
	}
	if provider == "" {
		return fmt.Errorf("config: provider is empty")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("config: read config file: %w", err)
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil // 空文件从零开始
	}

	masked := multilineMask(lines)
	secHeader := "[" + section + "]"

	// 定位段头：仅接受 trim 后精确等于 "[section]" 的行，
	// 防止 [global.llm] 误匹配 [global.llm.providers.xxx] 等子表。
	secStart := -1
	for i, line := range lines {
		if !masked[i] && strings.TrimSpace(line) == secHeader {
			secStart = i
			break
		}
	}

	if secStart < 0 {
		// 段缺失 → 文件末尾追加：空行 + 段头 + 键
		return atomicWriteFile(path, []byte(strings.Join(appendSection(lines, secHeader, provider), "\n")))
	}

	// 段范围：段头下一行起，至下一个段头（含 [[...]] 数组表头）或 EOF
	secEnd := len(lines)
	for i := secStart + 1; i < len(lines); i++ {
		if !masked[i] && strings.HasPrefix(strings.TrimSpace(lines[i]), "[") {
			secEnd = i
			break
		}
	}

	keyRow, indent, lastNonEmpty := -1, "", secStart
	for i := secStart + 1; i < secEnd; i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" {
			continue
		}
		lastNonEmpty = i
		if keyRow < 0 && !masked[i] && useProviderKeyRe.MatchString(trimmed) {
			keyRow = i
		}
		if indent == "" && !strings.HasPrefix(trimmed, "#") {
			indent = leadingWhitespace(lines[i]) // 缩进跟随段内首个键/内容行
		}
	}

	if keyRow >= 0 {
		lines[keyRow] = replaceUseProviderValue(lines[keyRow], provider)
	} else {
		// 键缺失 → 段内最后一个非空行之后插入
		updated := make([]string, 0, len(lines)+1)
		updated = append(updated, lines[:lastNonEmpty+1]...)
		updated = append(updated, useProviderLine(indent, provider))
		updated = append(updated, lines[lastNonEmpty+1:]...)
		lines = updated
	}

	return atomicWriteFile(path, []byte(strings.Join(lines, "\n")))
}

// appendSection 在文件末尾追加缺失的段：确保与上文隔一个空行，且以换行收尾。
func appendSection(lines []string, secHeader, provider string) []string {
	out := lines
	if len(out) > 0 && out[len(out)-1] != "" {
		out = append(out, "") // 原文件结尾无换行时先补齐
	}
	return append(out, secHeader, useProviderLine("", provider), "")
}

// multilineMask 标记处于 TOML 多行字符串（""" / '''）内部的行——这些行不参与
// 段头与键行的识别，避免把字符串内容误当作配置结构。
// 简化：只记录"进入该行时"是否在字符串内；单行内先闭合再开启的混合情形
// 极为罕见，不足以引入完整 TOML 解析器。
func multilineMask(lines []string) []bool {
	masked := make([]bool, len(lines))
	var triple string
	for i, line := range lines {
		masked[i] = triple != ""
		for j := 0; j < len(line); {
			switch {
			case triple == "" && strings.HasPrefix(line[j:], `"""`):
				triple = `"""`
				j += 3
			case triple == "" && strings.HasPrefix(line[j:], `'''`):
				triple = `'''`
				j += 3
			case triple != "" && strings.HasPrefix(line[j:], triple):
				triple = ""
				j += 3
			default:
				j++
			}
		}
	}
	return masked
}

// replaceUseProviderValue 就地替换 use_provider 行的字符串值：保留行首缩进、
// 键名、等号两侧空白与行内注释，值统一写成双引号 basic string。
// 兼容旧值为单引号字面量字符串（TOML literal string）；旧值缺失或引号未闭合
// 等损坏情形直接重写为标准形式。
func replaceUseProviderValue(line, provider string) string {
	eq := strings.Index(line, "=")
	if eq < 0 {
		// 调用前已用正则确认存在等号，此处仅兜底
		return useProviderLine(leadingWhitespace(line), provider)
	}
	prefix := line[:eq+1] // 缩进 + 键名 + 等号，原样保留

	// 等号右侧的空白原样保留（含紧贴等号的无空格写法）
	vs := eq + 1
	for vs < len(line) && (line[vs] == ' ' || line[vs] == '\t') {
		vs++
	}
	ws := line[eq+1 : vs]
	rest := line[vs:]
	if rest == "" {
		return prefix + " " + tomlBasicString(provider) // 旧行无值，补一个空格保可读性
	}

	value := tomlBasicString(provider)
	if rest[0] != '"' && rest[0] != '\'' {
		return prefix + " " + value // 旧值不是字符串（异常手改），重写为标准形式
	}

	q := rest[0]
	for j := 1; j < len(rest); j++ {
		if q == '"' && rest[j] == '\\' {
			j++ // basic string 中的转义字符，跳过
			continue
		}
		if rest[j] == q {
			// 结束引号之后的尾部（行内注释等）原样保留
			return prefix + ws + value + rest[j+1:]
		}
	}
	return prefix + " " + value // 引号未闭合（文件损坏），重写为标准形式
}

// useProviderLine 生成一行 use_provider 键值。
func useProviderLine(indent, provider string) string {
	return indent + "use_provider = " + tomlBasicString(provider)
}

// tomlBasicString 把 s 编码为 TOML basic string（双引号），转义反斜杠与引号。
func tomlBasicString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// leadingWhitespace 返回行首空白（空格/tab）。
func leadingWhitespace(line string) string {
	for i := 0; i < len(line); i++ {
		if line[i] != ' ' && line[i] != '\t' {
			return line[:i]
		}
	}
	return line
}

// atomicWriteFile 原子写入：先写同目录临时文件再 rename，避免写一半被读到；
// 失败时清理临时文件不残留，文件权限继承原文件（无则 0644）。
func atomicWriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	mode := os.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".persist-*.tmp")
	if err != nil {
		return fmt.Errorf("config: create temp file: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("config: write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("config: close temp file: %w", err)
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("config: chmod temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("config: rename temp file: %w", err)
	}
	return nil
}
