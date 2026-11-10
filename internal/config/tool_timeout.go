// 本文件解析"认知类工具一次 LLM 调用的预算"。
//
// 背景：deepthinking/design 曾把 10 分钟硬编码在工具实现里（deepthinking.go:84、design.go:88），
// 用户既改不了它，也无法通过任何配置让慢速模型跑完一次长分析。预算现在是配置项
// [tools.llm.<slot>.timeout]，解析规则只有这一份实现。
package config

import "time"

// cogToolSlots 是**真正消费** [tools.llm.<slot>.timeout] 的槽位。
//
// thinking / micro_agent 虽然有独立的 provider 覆盖段，但它们的调用方不读这个超时字段
// （没有 WithTimeout，预算由外层 ctx 给），所以既不进本表、也不参与 validate() 的看门狗
// 告警 —— 给一个没人读的槽位告警，只会让人以为自己设了但没生效。
var cogToolSlots = []string{"deepthinking", "design"}

// CogToolTimeout 解析认知类工具一次 LLM 调用的预算。
//
// 优先级：[tools.llm.<tool>.timeout] > max(DefaultCogToolTimeout, GlobalLLMTimeout())。
// 返回值恒 > 0：调用方直接拿它套 context.WithTimeout，返回 0 等于立刻过期。
// nil receiver 安全（装配层可能没有配置对象）。
func (c *Config) CogToolTimeout(toolName string) time.Duration {
	if c != nil {
		if override := c.getToolOverride(toolName); override != nil && override.Timeout > 0 {
			return override.Timeout
		}
	}
	base := GlobalLLMTimeout()
	if base < DefaultCogToolTimeout {
		base = DefaultCogToolTimeout
	}
	return base
}
