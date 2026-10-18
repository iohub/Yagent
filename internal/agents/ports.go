package agents

import (
	"context"

	"yagent/internal/browserctl"
	"yagent/internal/knowledge"
	"yagent/internal/toolspec"
)

// 本文件定义按消费方划分的窄接口（ports），用于解耦各 Agent 对
// globalctx.GlobalCtx 巨型上下文的依赖：每个 Agent 构造函数只声明
// 自己实际需要的接口。各接口均由具体类型天然实现（无需适配器）。

// EventBus 事件总线：agent 通过它发布跨 agent 消息。
// 由 *messaging.MessagePublisher 天然实现。
type EventBus interface {
	Publish(eventType string, content interface{}, from string) error
	PublishWithMetadata(eventType string, content interface{}, from string, metadata map[string]interface{}) error
}

// 委派坐标的绑定不放进 EventBus：那会逼着 5 个测试 mock 各写一个假方法，
// 而"没绑定"的正确后果只是事件少一个坐标，不是编译不过。
// 所以执行侧用能力断言（见 executor.go 的 ForDelegation 断言）。

// PromptFormatter 提示词格式化器：为系统提示追加环境/语言/自定义指令等公共段。
// 由 *globalctx.GlobalCtx 天然实现。
type PromptFormatter interface {
	FormatPrompt(prompt string) string
}

// Env 运行时环境视图：动态读取可变环境字段（每次调用重读当前值，
// 保持与原先直接访问 GlobalCtx 字段一致的动态语义）。
// 由 *globalctx.EnvView 天然实现。
type Env interface {
	ProjectPath() string
	FullYoloMode() bool
}

// RepoContextStore 仓库上下文摘要存储（并发安全）：Director 写入
// delegate_repo 工具结果，coding/browser agent 在构建 prompt 时读取。
// 由 *globalctx.RepoContextStore 天然实现。
type RepoContextStore interface {
	Get() string
	Set(summary string)
}

// KnowledgeProvider 知识注入器：对话前检索并注入领域知识。
// 由 *knowledge.KnowledgeInjector 天然实现。
type KnowledgeProvider interface {
	Inject(ctx context.Context, injCtx knowledge.InjectionContext) (string, error)
}

// 工具类窄接口已下移到 internal/toolspec（注册表与执行器工厂都要用它们，
// 而 toolspec 是 leaf）。这里保留别名：既有调用点与具体实现的对应关系不变，
// 但方法集只有一处声明，不会再出现"两边各写一份、改一边忘另一边"的漂移。
type (
	FileToolSet      = toolspec.FileToolSet
	SearchToolSet    = toolspec.SearchToolSet
	SysToolSet       = toolspec.SysToolSet
	EditToolSet      = toolspec.EditToolSet
	RepoToolSet      = toolspec.RepoOps
	FlowToolSet      = toolspec.FlowToolSet
	Thinker          = toolspec.Thinker
	DeepThinker      = toolspec.DeepThinker
	Designer         = toolspec.Designer
	MicroAgentRunner = toolspec.MicroAgentRunner
)

// BoardWriter 让子 Agent 的二级委派（今天只有 Coding→Browser）也能落便签、也能带上索引。
//
// 没有它时 Browser 的结论只活在 Coding 的上下文里：Coding 一旦被压缩，或者 Director
// 换一次委派，那份证据就没了，只能把同一个网页再抓一遍。由 *DirectorAgent 实现；
// 未注入（nil）时 browserDelegate 退回改动前的行为，绝不 panic。
type BoardWriter interface {
	// PackDelegateTask 前置便签索引与显式 refs 的 ContextPack。
	PackDelegateTask(task string, params map[string]interface{}) (packedTask string, unresolved int)
	// WriteDelegateNote 把子 Agent 的结果写成便签，返回 noteRef（写不出就返回空串）。
	WriteDelegateNote(agentName, toolName, task string, r AgentResult, runErr error) string
}

// BrowserControl 浏览器执行层入口（由 *browserctl.Manager 天然实现）。
// 用类型别名而不是新接口：Browser-Agent 与工具层共用同一份契约，避免两份声明漂移。
type BrowserControl = browserctl.Controller
