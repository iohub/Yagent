package main

import (
	"encoding/json"
	"fmt"
	"os"
)

func generateJSONSchema(doc ProtocolDocument, outputPath string) {
	schema := map[string]interface{}{
		"$schema":     "http://json-schema.org/draft-07/schema#",
		"$id":         "https://codeactor.dev/schemas/agent-events.json",
		"title":       doc.Protocol.Name,
		"version":     doc.Protocol.Version,
		"description": doc.Protocol.Description,
		"type":        "object",
		"definitions": map[string]interface{}{},
		"oneOf":       []interface{}{},
	}

	defs := schema["definitions"].(map[string]interface{})
	oneOf := schema["oneOf"].([]interface{})

	for _, et := range doc.EventTypes {
		// 为每个事件类型创建一个 JSON Schema 定义
		defName := toPascalCase(et.Name) + "Event"
		props := map[string]interface{}{}
		required := []string{"event"}

		// 添加 event 字段作为判别器
		props["event"] = map[string]interface{}{
			"type":        "string",
			"description": et.Description,
			"enum":        []string{et.Name},
		}

		for _, f := range et.Fields {
			propSchema := fieldToJSONSchema(f)
			props[f.Name] = propSchema
			if f.Required {
				required = append(required, f.Name)
			}
		}

		def := map[string]interface{}{
			"type":       "object",
			"properties": props,
			"required":   required,
			"additionalProperties": false,
		}

		// 添加渲染提示作为 x- 扩展字段
		if et.RenderHint != nil {
			hintMap := map[string]interface{}{}
			if et.RenderHint.Component != "" {
				hintMap["component"] = et.RenderHint.Component
			}
			if et.RenderHint.Heading != "" {
				hintMap["heading"] = et.RenderHint.Heading
			}
			if len(hintMap) > 0 {
				def["x-render-hint"] = hintMap
			}
		}

		defs[defName] = def

		// oneOf 引用
		oneOf = append(oneOf, map[string]interface{}{
			"$ref": fmt.Sprintf("#/definitions/%s", defName),
		})
	}

	// 重要：将 oneOf 重新赋值回 map，因为 append 可能已分配新底层数组
	schema["oneOf"] = oneOf

	// 添加 WebSocket 消息包装
	wsDef := map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
		"required":   []string{},
	}
	wsProps := wsDef["properties"].(map[string]interface{})
	for _, f := range doc.WebSocketMessage.Fields {
		wsProps[f.Name] = fieldToJSONSchema(f)
	}
	defs["WebSocketMessage"] = wsDef

	// VSCode 扩展消息
	extDef := map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
		"required":   []string{},
	}
	extProps := extDef["properties"].(map[string]interface{})
	for _, f := range doc.ExtensionMessage.Fields {
		extProps[f.Name] = fieldToJSONSchema(f)
	}
	defs["ExtensionMessage"] = extDef

	data, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "序列化 JSON Schema 失败: %v\n", err)
		return
	}

	if err := os.WriteFile(outputPath, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "写入 JSON Schema 文件失败: %v\n", err)
		return
	}
	fmt.Printf("  ✓ JSON Schema 已生成: %s\n", outputPath)
}

func fieldToJSONSchema(f FieldDef) map[string]interface{} {
	s := map[string]interface{}{
		"description": f.Description,
	}

	switch f.Type {
	case "string":
		s["type"] = "string"
		if len(f.Enum) > 0 {
			s["enum"] = f.Enum
		}
	case "number":
		s["type"] = "number"
	case "boolean":
		s["type"] = "boolean"
	case "object":
		s["type"] = "object"
		if len(f.Fields) > 0 {
			props := map[string]interface{}{}
			for _, sub := range f.Fields {
				props[sub.Name] = fieldToJSONSchema(sub)
			}
			s["properties"] = props
		}
	case "array":
		s["type"] = "array"
		if f.Items != nil {
			s["items"] = fieldToJSONSchema(*f.Items)
		}
	default:
		s["type"] = "string" // fallback
	}

	return s
}