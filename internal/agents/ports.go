package agents

import (
	"context"

	"yagent/internal/knowledge"
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

// FileToolSet 文件操作工具集。
// 由 *tools.FileOperationsTool 天然实现。
// 含 Delete/Rename：Coding-Agent 的工具注册表需要完整文件操作。
type FileToolSet interface {
	ExecuteReadFile(ctx context.Context, params map[string]interface{}) (interface{}, error)
	ExecuteListDir(ctx context.Context, params map[string]interface{}) (interface{}, error)
	ExecutePrintDirTree(ctx context.Context, params map[string]interface{}) (interface{}, error)
	ExecuteCreateFile(ctx context.Context, params map[string]interface{}) (interface{}, error)
	ExecuteDeleteFile(ctx context.Context, params map[string]interface{}) (interface{}, error)
	ExecuteRenameFile(ctx context.Context, params map[string]interface{}) (interface{}, error)
}

// SearchToolSet 搜索操作工具集。
// 由 *tools.SearchOperationsTool 天然实现。
type SearchToolSet interface {
	ExecuteGrepSearch(ctx context.Context, params map[string]interface{}) (interface{}, error)
}

// SysToolSet 系统操作工具集（执行 shell 命令）。
// 由 *tools.SystemOperationsTool 天然实现。
type SysToolSet interface {
	ExecuteRunBash(ctx context.Context, params map[string]interface{}) (interface{}, error)
}

// EditToolSet 文件编辑工具集（按块替换）。
// 由 *tools.ReplaceBlockTool 天然实现。
type EditToolSet interface {
	ExecuteReplaceBlock(ctx context.Context, params map[string]interface{}) (interface{}, error)
}

// RepoToolSet 仓库理解工具集（语义搜索/代码骨架/代码片段/调用图）。
// 由 *tools.RepoOperationsTool 天然实现。
type RepoToolSet interface {
	ExecuteSemanticSearch(ctx context.Context, params map[string]interface{}) (interface{}, error)
	ExecuteQueryCodeSkeleton(ctx context.Context, params map[string]interface{}) (interface{}, error)
	ExecuteQueryCodeSnippet(ctx context.Context, params map[string]interface{}) (interface{}, error)
	ExecuteFindFunctionCallees(ctx context.Context, params map[string]interface{}) (interface{}, error)
	ExecuteFindFunctionCallers(ctx context.Context, params map[string]interface{}) (interface{}, error)
	ExecuteCallGraph(ctx context.Context, params map[string]interface{}) (interface{}, error)
}

// FlowToolSet 流程控制工具集（退出 agent / 请求用户帮助）。
// 由 *tools.FlowControlTool 天然实现。
type FlowToolSet interface {
	ExecuteAgentExit(ctx context.Context, params map[string]interface{}) (interface{}, error)
	ExecuteAskUserForHelp(ctx context.Context, params map[string]interface{}) (interface{}, error)
}

// Thinker 认知思考工具（轻量推理，无工具调用）。
// 由 *tools.ThinkingTool 天然实现。
type Thinker interface {
	Call(ctx context.Context, input string) (string, error)
}

// DeepThinker 深度思考工具（复杂问题的系统性深度分析）。
// 由 *tools.DeepThinkingTool 天然实现。
type DeepThinker interface {
	Execute(ctx context.Context, params map[string]interface{}) (interface{}, error)
}

// MicroAgentRunner 微代理工具（隔离上下文的聚焦子任务推理）。
// 由 *tools.MicroAgentTool 天然实现。
type MicroAgentRunner interface {
	Execute(ctx context.Context, params map[string]interface{}) (interface{}, error)
}
