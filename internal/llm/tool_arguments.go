package llm

import "encoding/json"

// UsableToolArguments 报告 raw 是否能被当作工具参数对象使用。
//
// 判据：能 unmarshal 进 map[string]interface{} 且结果非 nil。
// 注意 "null" 会 unmarshal 成功但得到 nil map，必须判为不可用。
//
// 这是全仓唯一的"参数能不能用"判据（internal/llm/AGENTS.md 硬约束 13 要求判据只有一份）：
//   - internal/tools 的 Adapter.Call 用同一次 map unmarshal 决定走对象路径还是裸字符串兜底；
//   - Choice.ToolCallNote 用同一个形状判断"这轮的工具调用参数不完整"。
//
// 两侧的用途不同，不要混用：ingress（读 provider 的字节）只补空串，非法 JSON 原样留着
// 给诊断；egress（发请求给 provider）必须把所有不可用的形状规范化成对象，否则
// "arguments": "" 或 "input": null 会让整次请求 400，把一次幻觉放大成永久毒化的上下文。
func UsableToolArguments(raw string) bool {
	var params map[string]any
	if err := json.Unmarshal([]byte(raw), &params); err != nil {
		return false
	}
	return params != nil
}
