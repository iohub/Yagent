package config

import "time"

// ═══════════════════════════════════════════════════════════════
// 统一超时配置（P0 架构优化 Step 1：单一默认来源）
// ═══════════════════════════════════════════════════════════════
//
// 目标：把散落在执行内核中的硬编码超时常量收敛到本结构，后续步骤由
// 消费方从 config 读取。本步骤仅新增配置层（纯加法），不改动任何
// 消费方，零行为变化。
//
// 默认值与现状硬编码值一一对应（行为等价基准）：
//   - LLM          = 5m   ← internal/agents/executor.go:118（RunAgentLoop 默认 LLM 调用超时）
//   - SubAgentTool = 180s ← internal/agents/executor.go:497（defaultToolTimeout）
//   - PlannerTool  = 120s ← internal/agents/director.go:867（directorToolRunner 普通工具超时）
//   - Delegate     = 10m  ← internal/agents/director.go:869（delegate_* 专用超时）
//
// 交互式工具（ask_user_for_help）不受本配置约束：其耗时取决于用户响应
// 速度，调用侧必须无限等待（不加 deadline），见
// internal/agents/tool_timeout.go 的 isInteractiveUserTool。
type TimeoutsConfig struct {
	// LLM 单次 LLM 调用超时（RunAgentLoop / Planner 的 LLM 生成调用），默认 5m。
	// 与 LLMConfig.Timeout（[llm].timeout，LLM 引擎兜底配置）语义对齐，
	// 后续步骤统一时以本字段为准。
	LLM time.Duration `toml:"llm" json:"llm" yaml:"llm"`

	// SubAgentTool 子 Agent（Repo/Coding/Chat/DevOps/Browser）普通工具调用超时，默认 180s
	SubAgentTool time.Duration `toml:"sub_agent_tool" json:"sub_agent_tool" yaml:"sub_agent_tool"`

	// PlannerTool Director/Planner 普通工具调用超时，默认 120s
	PlannerTool time.Duration `toml:"planner_tool" json:"planner_tool" yaml:"planner_tool"`

	// Delegate delegate_* 工具（委派子 Agent 多轮交互）调用超时，默认 10m
	Delegate time.Duration `toml:"delegate" json:"delegate" yaml:"delegate"`
}

// DefaultTimeouts 返回统一超时默认值。
// 默认值与现状硬编码值一致（executor.go 5m/180s、director.go 120s/10m），
// 保证「配置缺失或解析失败时行为与现状一致」。
func DefaultTimeouts() TimeoutsConfig {
	return TimeoutsConfig{
		LLM:          5 * time.Minute,
		SubAgentTool: 180 * time.Second,
		PlannerTool:  120 * time.Second,
		Delegate:     10 * time.Minute,
	}
}

// Normalize 零值字段回退默认值（幂等：对已 Normalize 的结果再 Normalize 不变）。
// 非正值（零或负）视为未配置，回退 DefaultTimeouts() 对应默认值；
// 显式配置的正值原样保留。
func (c TimeoutsConfig) Normalize() TimeoutsConfig {
	d := DefaultTimeouts()
	n := c
	if n.LLM <= 0 {
		n.LLM = d.LLM
	}
	if n.SubAgentTool <= 0 {
		n.SubAgentTool = d.SubAgentTool
	}
	if n.PlannerTool <= 0 {
		n.PlannerTool = d.PlannerTool
	}
	if n.Delegate <= 0 {
		n.Delegate = d.Delegate
	}
	return n
}
