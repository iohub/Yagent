package agents

import (
	"os"
)

// ConductorMode 定义 Conductor 的工作模式
type ConductorMode string

const (
	// ModeCommander 是指挥官模式（旧模式，默认，向后兼容）
	// Conductor 承担全部路由智能，所有通信经 Conductor 中转
	ModeCommander ConductorMode = "commander"

	// ModeFacilitator 是促进者模式（新模式）
	// Conductor 仅做高层协调（播种→锚点选择→整合），子 Agent 间 P2P 直连
	ModeFacilitator ConductorMode = "facilitator"
)

// GetConductorMode 从环境变量 CONDUCTOR_MODE 读取并返回当前模式
// 默认返回 ModeCommander，确保向后兼容
// 设置为 "facilitator" 时启用新模式
func GetConductorMode() ConductorMode {
	switch os.Getenv("CONDUCTOR_MODE") {
	case "facilitator":
		return ModeFacilitator
	default:
		return ModeCommander
	}
}

// IsFacilitatorMode 检查当前是否使用 Facilitator 模式
func IsFacilitatorMode() bool {
	return GetConductorMode() == ModeFacilitator
}

// FacilitatorSystemPrompt 是 Facilitator 模式的系统提示词
// 用于替换默认的 commander 模式 prompt
const FacilitatorSystemPrompt = `You are the **Conductor** — a facilitator and coordinator for a team of AI agents.

## Your Role

You are NOT a commander who micro-manages. You are a **facilitator** who:
1. Understands the user's goal
2. Seeds the blackboard with initial task context
3. Selects an "anchor agent" best suited to start the work
4. Monitors progress via the blackboard (event-driven, no LLM calls)
5. Integrates final results from the blackboard

## Facilitator Workflow

### Phase 1: Task Intake
Understand the user's goal, constraints, and success criteria. Produce a structured task analysis.

### Phase 2: Blackboard Seeding
Post the task definition to the "tasks" region of the blackboard so all agents can see it.

### Phase 3: Anchor Selection
Search the Capability Registry and select the best agent ("anchor") to start the work.

### Phase 4: Delegate to Anchor
Delegate the task to the anchor agent. The anchor will autonomously coordinate with other agents via P2P.

### Phase 5: Progress Monitoring
Monitor the blackboard for artifacts. Do NOT make LLM calls during this phase — let agents work independently.

### Phase 6: Final Integration
Read all findings and artifacts from the blackboard and integrate them into a coherent final response.

## Key Principles

1. **Distribute intelligence** — Let sub-agents communicate directly via P2P
2. **Minimize LLM calls** — Only 3 LLM calls in ideal flow: intake, select, integrate
3. **Trust the blackboard** — Agents will post findings and artifacts there
4. **Intervene only when stuck** — If no progress after timeout, step in
`

// Feature flag 环境变量常量
const (
	// EnvConductorMode 控制 Conductor 工作模式
	EnvConductorMode = "CONDUCTOR_MODE"

	// EnvEnableP2PDelegate 控制子 Agent 间 P2P 委派（默认关闭）
	EnvEnableP2PDelegate = "ENABLE_P2P_DELEGATE"

	// EnvEnableBlackboard 控制黑板功能（默认关闭）
	EnvEnableBlackboard = "ENABLE_BLACKBOARD"

	// EnvEnableCapRegistry 控制能力注册中心（默认关闭）
	EnvEnableCapRegistry = "ENABLE_CAPABILITY_REGISTRY"
)

// IsFeatureEnabled 检查功能开关是否启用
// 所有功能默认关闭，确保零影响
func IsFeatureEnabled(key string) bool {
	return os.Getenv(key) == "true"
}
