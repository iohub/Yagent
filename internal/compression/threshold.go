package compression

import (
	"yagent/internal/models"
)

// ContextWindowDivisor 压缩阈值与模型上下文窗口的比例分母：
// token 用量超过窗口的 1/3 即触发压缩，为工具结果和模型输出预留余量。
const ContextWindowDivisor = 3

// ResolveThreshold 返回触发上下文压缩的 token 阈值，优先级：
//
//  1. 内嵌模型目录（models.json）收录的上下文窗口 / ContextWindowDivisor；
//  2. configured（配置项 context_compression_threshold，>0 时）；
//  3. DefaultContextCompressionThreshold。
//
// model 为 LLM 引擎实际使用的模型名，空串或未收录时直接走 2、3 级回退。
func ResolveThreshold(model string, configured int) int {
	if window := models.ContextWindow(model); window > 0 {
		return window / ContextWindowDivisor
	}
	if configured > 0 {
		return configured
	}
	return DefaultContextCompressionThreshold
}
