package agents

import "yagent/internal/toolspec"

// ToolsJSON 是工具声明原文。已搬到 internal/toolspec/schemas.json（单一事实来源），
// 这里只是尚未迁走的老调用点的过渡别名 —— 迁移完成后本文件会删除。
var ToolsJSON = toolspec.SchemasJSON()
