package main

import (
	"strings"
	"unicode"
)

// toPascalCase 将 snake_case 转换为 PascalCase
func toPascalCase(s string) string {
	parts := strings.Split(s, "_")
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "")
}

// toCamelCaseUpper 将 snake_case 转换为 UpperCamelCase（首字母大写）
func toCamelCaseUpper(s string) string {
	return toPascalCase(s)
}

// toCamelCaseLower 将 snake_case 转换为 lowerCamelCase
func toCamelCaseLower(s string) string {
	parts := strings.Split(s, "_")
	for i, p := range parts {
		if i == 0 {
			parts[i] = strings.ToLower(p)
		} else if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + strings.ToLower(p[1:])
		}
	}
	return strings.Join(parts, "")
}

// toTypeScriptType 将 YAML 类型映射为 TypeScript 类型
func toTypeScriptType(t string) string {
	switch t {
	case "string":
		return "string"
	case "number":
		return "number"
	case "boolean":
		return "boolean"
	case "object":
		return "Record<string, any>"
	case "array":
		return "any[]"
	default:
		return "any"
	}
}

// toGoType 将 YAML 类型映射为 Go 类型
func toGoType(t string) string {
	switch t {
	case "string":
		return "string"
	case "number":
		return "float64"
	case "boolean":
		return "bool"
	case "object":
		return "json.RawMessage"
	case "array":
		return "[]json.RawMessage"
	default:
		return "interface{}"
	}
}

// toSnakeCase 将 PascalCase 或 camelCase 转换为 snake_case
func toSnakeCase(s string) string {
	var result strings.Builder
	for i, r := range s {
		if unicode.IsUpper(r) {
			if i > 0 {
				result.WriteRune('_')
			}
			result.WriteRune(unicode.ToLower(r))
		} else {
			result.WriteRune(r)
		}
	}
	return result.String()
}