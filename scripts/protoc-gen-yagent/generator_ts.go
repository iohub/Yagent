package main

import (
	"fmt"
	"os"
	"strings"
)

func generateTypeScript(doc ProtocolDocument, outputPath string) {
	var sb strings.Builder

	sb.WriteString("// =============================================================================\n")
	sb.WriteString(fmt.Sprintf("// %s - TypeScript 类型定义\n", doc.Protocol.Name))
	sb.WriteString(fmt.Sprintf("// 版本: %s\n", doc.Protocol.Version))
	sb.WriteString("// 此文件由 codegen 自动生成，请勿手动修改\n")
	sb.WriteString("// =============================================================================\n\n")

	// 生成事件类型枚举
	sb.WriteString("// ===== 事件类型枚举 =====\n")
	sb.WriteString("export const EventTypes = {\n")
	for _, et := range doc.EventTypes {
		constName := toCamelCaseUpper(et.Name)
		sb.WriteString(fmt.Sprintf("  %s: \"%s\",\n", constName, et.Name))
	}
	sb.WriteString("} as const;\n\n")
	sb.WriteString("export type EventType = typeof EventTypes[keyof typeof EventTypes];\n\n")

	// 生成每个事件类型的接口
	sb.WriteString("// ===== 事件类型接口 =====\n")
	for _, et := range doc.EventTypes {
		ifaceName := toPascalCase(et.Name) + "Event"
		sb.WriteString(fmt.Sprintf("export interface %s {\n", ifaceName))
		sb.WriteString(fmt.Sprintf("  /** %s */\n", et.Description))
		sb.WriteString(fmt.Sprintf("  event: \"%s\";\n", et.Name))
		for _, f := range et.Fields {
			tsType := toTypeScriptType(f.Type)
			optional := ""
			if !f.Required {
				optional = "?"
			}
			sb.WriteString(fmt.Sprintf("  %s%s: %s;\n", f.Name, optional, tsType))
		}
		sb.WriteString("}\n\n")
	}

	// 生成联合类型
	sb.WriteString("// ===== 事件联合类型 =====\n")
	sb.WriteString("export type AgentEvent =\n")
	for i, et := range doc.EventTypes {
		ifaceName := toPascalCase(et.Name) + "Event"
		if i < len(doc.EventTypes)-1 {
			sb.WriteString(fmt.Sprintf("  | %s\n", ifaceName))
		} else {
			sb.WriteString(fmt.Sprintf("  | %s;\n\n", ifaceName))
		}
	}

	// 生成 WebSocket 消息包装
	sb.WriteString("// ===== WebSocket 消息包装 =====\n")
	sb.WriteString("export interface WebSocketMessage {\n")
	for _, f := range doc.WebSocketMessage.Fields {
		tsType := toTypeScriptType(f.Type)
		optional := ""
		if !f.Required {
			optional = "?"
		}
		sb.WriteString(fmt.Sprintf("  /** %s */\n", f.Description))
		sb.WriteString(fmt.Sprintf("  %s%s: %s;\n", f.Name, optional, tsType))
	}
	sb.WriteString("}\n\n")

	// 类型守卫
	sb.WriteString("// ===== 类型守卫 =====\n")
	for _, et := range doc.EventTypes {
		funcName := "is" + toPascalCase(et.Name)
		ifaceName := toPascalCase(et.Name) + "Event"
		// 通过 event 字段判别
		sb.WriteString(fmt.Sprintf("export function %s(msg: AgentEvent | WebSocketMessage): msg is %s {\n", funcName, ifaceName))
		sb.WriteString(fmt.Sprintf("  return 'event' in msg && msg.event === \"%s\";\n", et.Name))
		sb.WriteString("}\n\n")
	}

	if err := os.WriteFile(outputPath, []byte(sb.String()), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "写入 TS 类型文件失败: %v\n", err)
		return
	}
	fmt.Printf("  ✓ TS 类型已生成: %s\n", outputPath)
}