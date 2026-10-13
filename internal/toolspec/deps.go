package toolspec

import "context"

// ToolFunc 是工具执行器签名。与 internal/tools 的 Adapter 共用同一形状。
type ToolFunc func(ctx context.Context, params map[string]interface{}) (interface{}, error)

// 本文件声明工具执行器需要的窄依赖（ports）。方法名与 internal/agents/ports.go
// 里的同名接口一致，因此 agents 侧只需改成类型别名，具体实现无需适配器。
// 之所以在 toolspec 里重新声明而不是 import agents：本包是 leaf，
// 且执行器工厂必须能被 tools 与 agents 两侧共用。

// FileToolSet 文件操作工具集。由 *tools.FileOperationsTool 天然实现。
type FileToolSet interface {
	ExecuteReadFile(ctx context.Context, params map[string]interface{}) (interface{}, error)
	ExecuteListDir(ctx context.Context, params map[string]interface{}) (interface{}, error)
	ExecutePrintDirTree(ctx context.Context, params map[string]interface{}) (interface{}, error)
	ExecuteCreateFile(ctx context.Context, params map[string]interface{}) (interface{}, error)
	ExecuteDeleteFile(ctx context.Context, params map[string]interface{}) (interface{}, error)
	ExecuteRenameFile(ctx context.Context, params map[string]interface{}) (interface{}, error)
}

// SearchToolSet 全文检索。由 *tools.SearchOperationsTool 天然实现。
type SearchToolSet interface {
	ExecuteGrepSearch(ctx context.Context, params map[string]interface{}) (interface{}, error)
}

// SysToolSet Shell 执行。由 *tools.SystemOperationsTool 天然实现。
type SysToolSet interface {
	ExecuteRunBash(ctx context.Context, params map[string]interface{}) (interface{}, error)
}

// EditToolSet 按块替换。由 *tools.ReplaceBlockTool 天然实现。
type EditToolSet interface {
	ExecuteReplaceBlock(ctx context.Context, params map[string]interface{}) (interface{}, error)
}

// RepoOps codeseek 桥接的仓库理解与调用图工具。由 *tools.RepoOperationsTool 天然实现。
type RepoOps interface {
	ExecuteSemanticSearch(ctx context.Context, params map[string]interface{}) (interface{}, error)
	ExecuteQueryCodeSkeleton(ctx context.Context, params map[string]interface{}) (interface{}, error)
	ExecuteQueryCodeSnippet(ctx context.Context, params map[string]interface{}) (interface{}, error)
	ExecuteFindFunctionCallees(ctx context.Context, params map[string]interface{}) (interface{}, error)
	ExecuteFindFunctionCallers(ctx context.Context, params map[string]interface{}) (interface{}, error)
	ExecuteCallGraph(ctx context.Context, params map[string]interface{}) (interface{}, error)
}

// FlowToolSet 流程控制。由 *tools.FlowControlTool 天然实现。
type FlowToolSet interface {
	ExecuteAgentExit(ctx context.Context, params map[string]interface{}) (interface{}, error)
	ExecuteAskUserForHelp(ctx context.Context, params map[string]interface{}) (interface{}, error)
}

// Thinker 认知反思工具。由 *tools.ThinkingTool 天然实现（签名与其余工具不同，
// 它吃序列化后的整段输入，因此这里保留 Call 而不是 Execute）。
type Thinker interface {
	Call(ctx context.Context, input string) (string, error)
}

// DeepThinker 深度分析。由 *tools.DeepThinkingTool 天然实现。
type DeepThinker interface {
	Execute(ctx context.Context, params map[string]interface{}) (interface{}, error)
}

// Designer 编码前设计。由 *tools.DesignTool 天然实现。
type Designer interface {
	Execute(ctx context.Context, params map[string]interface{}) (interface{}, error)
}

// MicroAgentRunner 隔离子 LLM 推理。由 *tools.MicroAgentTool 天然实现。
type MicroAgentRunner interface {
	Execute(ctx context.Context, params map[string]interface{}) (interface{}, error)
}

// Guard 工作区边界与危险操作授权。由 *tools.WorkspaceGuard 天然实现。
// 只有 Check/RequestAuth 被 Adapter 用到，其余方法不暴露给工具层。
type Guard interface {
	Check(toolName string, params map[string]interface{}) (bool, string)
	RequestAuth(ctx context.Context, toolName, reason string) error
}

// BrowserResolver 按工具名取回浏览器工具的执行器。
// 由 *tools.BrowserOperationsTool 实现：argv 组装/渲染仍在 tools 侧，
// 而 name/schema/能力标签在注册表侧，两侧靠 Validate() 对账。
type BrowserResolver interface {
	ToolFunc(name string) (ToolFunc, bool)
	Available() (ok bool, reason string)
}

// DelegateFunc 是委派工具的统一步骤。target 是目标 Agent 名（"repo"/"coding"/
// "security_auditor" 等），由 Director 的实现负责解析 context_refs、写便签、
// 路由档位与超预算落盘。
type DelegateFunc func(ctx context.Context, target string, params map[string]interface{}) (interface{}, error)

// Deps 是一次工具装配需要的全部依赖。由 internal/tools.BuildDeps 构造，
// 每个 Agent 在 Run 期填上自己的身份与可用范围。字段为 nil 表示该能力缺失，
// 对应的 Spec 会经 Available() 判定为不可用并跳过（跳过原因记进 Surface.Skipped）。
type Deps struct {
	// 身份与环境
	AgentName   string
	ProjectPath string
	FullYolo    bool
	BoardID     string // "" → EffectContext 类工具不可用

	// 工具实现
	Files    FileToolSet
	Search   SearchToolSet
	Sys      SysToolSet
	Edit     EditToolSet
	Repo     RepoOps
	Flow     FlowToolSet
	Thinker  Thinker
	Micro    MicroAgentRunner
	Deep     DeepThinker
	Design   Designer
	Browser  BrowserResolver
	Guard    Guard
	Delegate DelegateFunc

	// 按 Agent 绑定的执行器（source/kind 因 Agent 而异，故在装配侧构造）
	Consolidate ToolFunc
	Prune       ToolFunc

	// Checkpoint 三个执行器；nil 表示当前仓库/配置不支持 git 快照
	CheckpointList     ToolFunc
	CheckpointCreate   ToolFunc
	CheckpointRollback ToolFunc

	// LoadTool 是 deferred schema 的取回入口，由 Surface 自己提供。
	LoadTool ToolFunc
	// DeferSchemas 由 NewSurface 在 deferred 开启时置真。
	// load_tool 的可用性判据用它而不是 LoadTool != nil：探针 Deps 会预填 LoadTool，
	// 用后者的话"关掉 deferred"就退不干净（load_tool 仍会注册）。
	DeferSchemas bool

	// Disabled 是被 -d/--disable-agents 关掉的子 Agent 集合，键为 target 名。
	Disabled map[string]bool

	// ResolveTarget 判定某个委派 target 当前是否存在（内建 Agent 或已注册的
	// Custom Agent）。nil 表示只有内建 Agent。
	ResolveTarget func(target string) bool
}

// IsDisabled 报告 target 是否被 -d/--disable-agents 显式关闭。
func (d *Deps) IsDisabled(target string) bool {
	if d == nil || d.Disabled == nil {
		return false
	}
	return d.Disabled[target]
}

// HasBoard 报告当前委派是否有 blackboard 便签板。
func (d *Deps) HasBoard() bool { return d != nil && d.BoardID != "" }

// ProbeDeps 返回"能力齐备"的探针 Deps：所有依赖都已装配、没有 Agent 被禁用。
// 供 golden 测试与静态校验使用 —— 它把 Available 的运行期变量固定下来，
// 于是画像的能力数学可以被钉成一个可评审的文件。
func (p Profile) ProbeDeps() *Deps { return p.testDeps() }
