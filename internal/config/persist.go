package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"yagent/internal/util"
)

// 这套写回机制的唯一目的：改一个键，其它字节原样不动。
// 禁止整文件 TOML 重编码 —— toml.NewEncoder 会丢掉用户注释、打乱字段顺序，
// 而 config.toml 恰恰是靠注释自我说明的（模板里每个键都带一段解释）。
// 三个入口（use_provider / routing mode / llm timeout）与引导页的 provider 写回
// 共用同一段定位与替换逻辑，所以这里只有一份实现。

// SetKeyInSection 在 [section] 段内把 key 设为 renderedValue（调用方负责渲染成合法 TOML 右值）。
// 段缺失 → 文件末尾追加"空行 + 段头 + 键"；键缺失 → 段内最后一个非空行之后插入。
// 其它行（注释、空行、顺序、缩进）逐字节保留。
func SetKeyInSection(path, section, key, renderedValue string) error {
	if path == "" {
		return fmt.Errorf("config: config file path is empty")
	}
	if section == "" {
		return fmt.Errorf("config: section is empty")
	}
	if key == "" {
		return fmt.Errorf("config: key is empty")
	}
	if strings.TrimSpace(renderedValue) == "" {
		return fmt.Errorf("config: value for %s.%s is empty (render a valid TOML right-hand side)", section, key)
	}

	lines, masked, err := readLines(path)
	if err != nil {
		return err
	}

	keyLine := func(indent string) string { return indent + key + " = " + renderedValue }
	secStart, secEnd := sectionSpan(lines, masked, section)
	if secStart < 0 {
		return atomicWriteFile(path, []byte(strings.Join(appendSection(lines, "["+section+"]", []string{keyLine("")}), "\n")))
	}

	keyRe := regexp.MustCompile(`^` + regexp.QuoteMeta(key) + `\s*=`)
	keyRow, indent, lastNonEmpty := -1, "", secStart
	for i := secStart + 1; i < secEnd; i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" {
			continue
		}
		lastNonEmpty = i
		if keyRow < 0 && !masked[i] && keyRe.MatchString(trimmed) {
			keyRow = i
		}
		if indent == "" && !strings.HasPrefix(trimmed, "#") {
			indent = leadingWhitespace(lines[i])
		}
	}

	if keyRow >= 0 {
		lines[keyRow] = replaceValueOnLine(lines[keyRow], renderedValue)
	} else {
		updated := make([]string, 0, len(lines)+1)
		updated = append(updated, lines[:lastNonEmpty+1]...)
		updated = append(updated, keyLine(indent))
		updated = append(updated, lines[lastNonEmpty+1:]...)
		lines = updated
	}
	return atomicWriteFile(path, []byte(strings.Join(lines, "\n")))
}

// RemoveSection 删除 [section] 整段（段头到下一个段头之前）。段不存在时返回 nil，
// 所以调用方可以把它当"确保不存在"用。
func RemoveSection(path, section string) error {
	if path == "" {
		return fmt.Errorf("config: config file path is empty")
	}
	if section == "" {
		return fmt.Errorf("config: section is empty")
	}
	lines, masked, err := readLines(path)
	if err != nil {
		return err
	}
	start, end := sectionSpan(lines, masked, section)
	if start < 0 {
		return nil
	}
	// 段前的分隔空行保留，段后多余的空行收掉一条，避免删一段就长出一个空洞。
	out := append([]string{}, lines[:start]...)
	out = append(out, lines[end:]...)
	if start > 0 && start < len(out) && strings.TrimSpace(out[start-1]) == "" && strings.TrimSpace(out[start]) == "" {
		out = append(out[:start], out[start+1:]...)
	}
	return atomicWriteFile(path, []byte(strings.Join(out, "\n")))
}

// RemoveKeyInSection 删掉 [section] 段内的一个键（不存在则 no-op）。
// 引导页把 api_key 换成 api_key_env（或把 temperature 收回"不发这个字段"）时用它，
// 否则明文密钥会作为"已经不生效的那一行"留在文件里。
func RemoveKeyInSection(path, section, key string) error {
	if path == "" || section == "" || key == "" {
		return fmt.Errorf("config: RemoveKeyInSection needs path, section and key")
	}
	lines, masked, err := readLines(path)
	if err != nil {
		return err
	}
	start, end := sectionSpan(lines, masked, section)
	if start < 0 {
		return nil
	}
	keyRe := regexp.MustCompile(`^` + regexp.QuoteMeta(key) + `\s*=`)
	for i := start + 1; i < end; i++ {
		if masked[i] || strings.HasPrefix(strings.TrimSpace(lines[i]), "#") {
			continue
		}
		if keyRe.MatchString(strings.TrimSpace(lines[i])) {
			out := append([]string{}, lines[:i]...)
			out = append(out, lines[i+1:]...)
			return atomicWriteFile(path, []byte(strings.Join(out, "\n")))
		}
	}
	return nil
}

// readLines 读文件并按行切分；空文件切出零行而不是 [""]。
// 同时算出多行字符串的遮罩（那些行里的假段头/假键不参与结构识别）。
func readLines(path string) ([]string, []bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("config: read config file: %w", err)
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	return lines, multilineMask(lines), nil
}

// sectionSpan 定位 [section] 的行区间 [start, end)。
// 段头必须 trim 后逐字符相等，否则 [global.llm] 会匹配到 [global.llm.providers.x] 这类子表。
// 段尾是下一个段头（含 [[数组表]]）或文件末尾。找不到返回 (-1, 0)。
func sectionSpan(lines []string, masked []bool, section string) (int, int) {
	header := "[" + section + "]"
	start := -1
	for i, line := range lines {
		if !masked[i] && strings.TrimSpace(line) == header {
			start = i
			break
		}
	}
	if start < 0 {
		return -1, 0
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if !masked[i] && strings.HasPrefix(strings.TrimSpace(lines[i]), "[") {
			end = i
			break
		}
	}
	return start, end
}

// appendSection 在文件末尾追加缺失的段：与上文隔一个空行，且以换行收尾。
func appendSection(lines []string, secHeader string, keyLines []string) []string {
	out := lines
	if len(out) > 0 && out[len(out)-1] != "" {
		out = append(out, "") // 原文件结尾无换行时先补齐
	}
	out = append(out, secHeader)
	out = append(out, keyLines...)
	return append(out, "")
}

// multilineMask 标记处于 TOML 多行字符串（""" / ”'）内部的行——这些行不参与
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

// replaceValueOnLine 就地替换一行键值对的右值：保留缩进、键名、等号两侧空白与行内注释。
// 兼容旧值为单引号字面量字符串；旧值缺失、不是字符串或引号未闭合等损坏情形
// 直接重写为 "<key> = <newValue>" 的标准形式。
func replaceValueOnLine(line, newValue string) string {
	eq := strings.Index(line, "=")
	if eq < 0 {
		return line // 调用前已用正则确认存在等号，此处兜底不改内容
	}
	prefix := line[:eq+1] // 缩进 + 键名 + 等号，原样保留

	vs := eq + 1
	for vs < len(line) && (line[vs] == ' ' || line[vs] == '\t') {
		vs++
	}
	ws := line[eq+1 : vs]
	rest := line[vs:]
	if rest == "" {
		return prefix + " " + newValue
	}
	if rest[0] != '"' && rest[0] != '\'' {
		return prefix + " " + newValue
	}

	q := rest[0]
	for j := 1; j < len(rest); j++ {
		if q == '"' && rest[j] == '\\' {
			j++ // basic string 中的转义字符，跳过
			continue
		}
		if rest[j] == q {
			// 结束引号之后的尾部（行内注释等）原样保留
			return prefix + ws + newValue + rest[j+1:]
		}
	}
	return prefix + " " + newValue
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

// atomicWriteFile 原子写入：tmp+rename 的语义在 util.AtomicWrite（全仓唯一一份，I21）。
// 这里保留 config 特有的那条规则：**权限继承原文件** —— 用户手工 chmod 过的
// config.toml 不该因为一次写回被改回去；原文件不存在时用 0644。
func atomicWriteFile(path string, data []byte) error {
	return util.AtomicWrite(path, data, util.AtomicWriteOptions{
		FilePerm:    0o644,
		InheritPerm: true,
		TmpSuffix:   ".persist.tmp",
		Label:       "config",
	})
}

// tomlBasicString 把 s 编码为 TOML basic string（双引号），转义反斜杠与引号。
func tomlBasicString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// PersistUseProvider 把 provider 选择写回 config.toml 的指定段。
// section 为完整表头名（不含方括号），如 "global.llm"、"agents.llm.coding"、
// "tools.llm.deepthinking"。
func PersistUseProvider(path, section, provider string) error {
	if provider == "" {
		return fmt.Errorf("config: provider is empty")
	}
	return SetKeyInSection(path, section, "use_provider", tomlBasicString(provider))
}

// PersistRoutingMode 把路由模式（"auto" | "fixed"）写回 [routing] mode。
func PersistRoutingMode(path, mode string) error {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != "auto" && mode != "fixed" {
		return fmt.Errorf("config: invalid routing mode %q (want auto|fixed)", mode)
	}
	return SetKeyInSection(path, "routing", "mode", tomlBasicString(mode))
}

// PersistLLMTimeout 把 LLM 调用超时写回 [llm] timeout。d 必须 > 0。
func PersistLLMTimeout(path string, d time.Duration) error {
	if d <= 0 {
		return fmt.Errorf("config: invalid LLM timeout %s (must be positive)", d)
	}
	return SetKeyInSection(path, "llm", "timeout", tomlBasicString(FormatLLMTimeout(d)))
}
