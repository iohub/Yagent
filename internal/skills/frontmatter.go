package skills

import (
	"encoding/json"
	"fmt"
	"strings"
)

// parseFrontMatter 解析技能文件头部的 `---` 块。
//
// **这不是 YAML 解析器**：只认两种形状 ——
//  1. JSON 对象（仓库里便签与蓝图的 front-matter 早就是这个形状，无需引入 YAML 依赖）；
//  2. 扁平 `key: value` / `key: [a, b]` / `key: a, b` 子集（从其他生态导入的技能几乎都是它）。
//
// 不认嵌套、多行、锚点、引号转义。认不出的形状返回 err 且 kind=none，
// 调用方仍会用正文首行当 description 并 warn —— 一条坏 front-matter 不该让技能消失。
//
// 返回的 body 永远不含 front-matter 块（分隔线成对出现时）。
func parseFrontMatter(raw string) (fields map[string]interface{}, body, kind string, err error) {
	fields = map[string]interface{}{}
	lines := strings.Split(raw, "\n")

	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return fields, raw, "none", nil
	}

	closeAt := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			closeAt = i
			break
		}
	}
	if closeAt < 0 {
		// 只有开头没有结尾：当成"没有 front-matter"处理，正文原样保留（不做半截剥离）。
		return fields, raw, "none", fmt.Errorf("front-matter 缺少结束的 ---")
	}

	block := strings.Join(lines[1:closeAt], "\n")
	body = strings.TrimPrefix(strings.Join(lines[closeAt+1:], "\n"), "\n")

	if strings.TrimSpace(block) == "" {
		return fields, body, "none", nil
	}

	if parsed, ok := parseJSONBlock(block); ok {
		return parsed, body, "json", nil
	}

	parsed, yamlErr := parseFlatBlock(lines[1:closeAt])
	if yamlErr != nil {
		return fields, body, "none", yamlErr
	}
	if _, ok := parsed["description"]; !ok {
		// "至少解析出 description 才算成功"：否则这段更像正文的一部分，而不是元数据。
		return fields, body, "none", fmt.Errorf("扁平 front-matter 里没有 description 键")
	}
	return parsed, body, "yaml-subset", nil
}

func parseJSONBlock(block string) (map[string]interface{}, bool) {
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(block), &parsed); err != nil || parsed == nil {
		return nil, false
	}
	return parsed, true
}

// parseFlatBlock 解析扁平子集。缩进的行一律视为"想写嵌套" → 报错，
// 悄悄支持一半会让用户以为嵌套生效了。
func parseFlatBlock(blockLines []string) (map[string]interface{}, error) {
	out := map[string]interface{}{}
	for _, line := range blockLines {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			return nil, fmt.Errorf("不支持嵌套/多行 front-matter：%q", strings.TrimSpace(line))
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("无法解析的 front-matter 行：%q", line)
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		if key == "" {
			return nil, fmt.Errorf("front-matter 行缺少键：%q", line)
		}
		out[key] = parseFlatValue(value)
	}
	return out, nil
}

func parseFlatValue(value string) interface{} {
	value = strings.Trim(value, `"'`)
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		value = strings.TrimSpace(value[1 : len(value)-1])
		if value == "" {
			return []interface{}{}
		}
	}
	if strings.Contains(value, ",") {
		parts := strings.Split(value, ",")
		out := make([]interface{}, 0, len(parts))
		for _, p := range parts {
			if t := strings.TrimSpace(strings.Trim(p, `"'`)); t != "" {
				out = append(out, t)
			}
		}
		return out
	}
	return value
}
