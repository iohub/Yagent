package plasma

import "yagent/internal/toolspec"

// PlasmaPalette 是生成式 Agent 的默认能力上限：只读族 + 收尾 + 取回冷工具。
//
// 它刻意**不等于** toolspec.ProfileDirector.Palette —— 后者含 FSWrite|Shell
// （profiles.go:33），人在回路时可接受，但自动委派的自进化 Agent 继承它就是权限升级：
// 生成器一旦被任务描述里的注入内容污染，产出的 prompt 就带着写/shell 进内核。
//
// 第三方能力一位都不给（ExternalRead/ExternalWrite/Skill）：internal/toolspec/AGENTS.md
// 硬约束 6.1 明写"Palette 一个位都不加（Custom Agent 拿不到第三方能力）"，
// 而第三方 MCP 输出本身就是提示注入向量，与"prompt 是生成出来的"叠加等于把注入面交给进化器。
//
// 含 EffectToolLoad 是**修 bug**：ProfileDirector.Palette 不含它，于是今天 Meta 设计的
// Custom Agent 拿不到 load_tool，任何被降到 Cold 的工具永远取不回来。
//
// 本常量不进 toolspec.BuiltinProfiles()：plasma Agent 走 Profile{Names} 的按名挑选通道，
// 所以 tool_surfaces.golden 一个字节都不动。
//
// 刻意不含：FSWrite · Shell · BrowserObserve/Act/Eval/Artifact · Checkpoint · Delegate
// · ExternalRead · ExternalWrite · Skill · CogSubLLM · CogDesign。
// 要这些必须走升级门（逐个 Agent、用户批准、**按 hash 记**而不是按 name 记，
// 否则批准过一次写权限会被后续所有变异继承）。
const PlasmaPalette = toolspec.EffectFSRead | toolspec.EffectSearchRegex |
	toolspec.EffectCodeSemantic | toolspec.EffectCodeGraph |
	toolspec.EffectContext | toolspec.EffectFlow | toolspec.EffectToolLoad |
	toolspec.EffectCogReflect | toolspec.EffectCogDeep

// BeyondBase 报告 want 里超出 base 的那些位。base 由调用方注入（生产传 PlasmaPalette），
// 不在这里硬编码 —— 否则 Compiler.Palette 那条"忘配就全拒"的 fail-closed 性质会被绕开。
func BeyondBase(want, base toolspec.Effect) toolspec.Effect { return want &^ base }
