// Package toolspec 是工具注册表的单一事实来源：一个工具的能力标签、频率档、
// 授权要求、截断优先级与执行器工厂都在这里声明一次。
//
// 本包是 leaf 包，禁止 import 任何 yagent 内部包（由
// scripts/check_toolspec_leaf.sh 守卫）。需要外部能力时通过 Deps 的窄字段传入，
// 由 internal/tools / internal/agents 在装配时填充。
package toolspec

// Effect 是能力标签位掩码。它同时决定两件事：
//  1. 哪些 Agent 能拿到这个工具（Profile.Grant/Deny 匹配）；
//  2. 派生出原本散落在 8 张按名索引表里的元数据（并发/授权/截断/展示/超时）。
//
// 新增工具必须显式声明 Effect；零值在 Registry.Validate() 里是致命错误。
type Effect uint64

// 只读族：不改变任何共享状态。
const (
	EffectFSRead       Effect = 1 << iota // read_file / list_dir / print_dir_tree
	EffectSearchRegex                     // search_by_regex
	EffectCodeSemantic                    // semantic_search / query_code_skeleton / query_code_snippet
	EffectCodeGraph                       // find_function_callee / find_function_caller / query_call_graph
	EffectContext                         // read_note / read_artifact
	EffectToolLoad                        // load_tool：deferred schema 的取回入口
)

// 认知族：隔离子 LLM 推理，无副作用。
const (
	EffectCogReflect Effect = 1 << (iota + 6) // thinking
	EffectCogSubLLM                           // micro_agent
	EffectCogDeep                             // deepthinking
	EffectCogDesign                           // design
)

// 写族：改变文件、进程、浏览器或工作树状态。
const (
	EffectFSWrite         Effect = 1 << (iota + 10) // create_file / search_replace_in_file / delete_file / rename_file
	EffectShell                                     // run_bash
	EffectBrowserObserve                            // browser_status / inspect / snapshot / wait
	EffectBrowserAct                                // browser_click / fill / type / press / scroll / select / tabs / open
	EffectBrowserEval                               // browser_eval：在当前页面执行任意 JS
	EffectBrowserArtifact                           // browser_screenshot：向会话工作区写文件
	EffectCheckpoint                                // git_checkpoint_*
)

// 控制族。
const (
	EffectFlow     Effect = 1 << (iota + 24) // agent_exit / ask_user_for_help
	EffectDelegate                           // delegate_*
)

// 外部能力族：运行期发现的东西。位序必须追加在末尾 —— Names() 按位序输出，
// 插在中间会让既存工具的 EffectSummary 整体漂移（golden 会以"画像变了"的样子变红）。
const (
	// EffectExternalRead 是外部 MCP server 的只读工具。
	EffectExternalRead Effect = 1 << 26
	// EffectExternalWrite 是外部 MCP server 的写类工具。
	// 没有 annotations 的第三方工具一律落到这一族（fail-safe，见 BuildExternalSpecs）。
	EffectExternalWrite Effect = 1 << 27
	// EffectSkill 是 use_skill：读取用户写的技能正文。
	EffectSkill Effect = 1 << 28
)

const (
	// writeEffects 是**必然**有副作用的能力集合：命中即必须声明 Mutates=true。
	// EffectCheckpoint 不在其中 —— git_checkpoint_list 只读标签，create/rollback 才改工作树，
	// 所以快照类由各自显式声明。
	// EffectExternalWrite 在这里，是为了让第三方工具的"有副作用却不声明"被既存校验自动拦住。
	writeEffects = EffectFSWrite | EffectShell | EffectBrowserEval | EffectExternalWrite
	// authEffects 是需要 WorkspaceGuard 授权的能力集合。
	authEffects = EffectFSWrite | EffectShell | EffectBrowserEval | EffectExternalWrite
	// flowEffects 是流程控制能力：永远 Hot（不可 deferred），否则模型无法收尾。
	flowEffects = EffectFlow | EffectToolLoad
)

// Has 报告 e 是否包含 want 中的任意一个位。
func (e Effect) Has(want Effect) bool { return e&want != 0 }

// Names 返回 e 里每个置位对应的能力名（用于日志与 golden 文件，按位序稳定）。
func (e Effect) Names() []string {
	names := make([]string, 0, 8)
	for _, c := range effectNames {
		if e&c.bit != 0 {
			names = append(names, c.name)
		}
	}
	return names
}

var effectNames = []struct {
	bit  Effect
	name string
}{
	{EffectFSRead, "fs_read"},
	{EffectSearchRegex, "search_regex"},
	{EffectCodeSemantic, "code_semantic"},
	{EffectCodeGraph, "code_graph"},
	{EffectContext, "context"},
	{EffectToolLoad, "tool_load"},
	{EffectCogReflect, "cog_reflect"},
	{EffectCogSubLLM, "cog_subllm"},
	{EffectCogDeep, "cog_deep"},
	{EffectCogDesign, "cog_design"},
	{EffectFSWrite, "fs_write"},
	{EffectShell, "shell"},
	{EffectBrowserObserve, "browser_observe"},
	{EffectBrowserAct, "browser_act"},
	{EffectBrowserEval, "browser_eval"},
	{EffectBrowserArtifact, "browser_artifact"},
	{EffectCheckpoint, "checkpoint"},
	{EffectFlow, "flow"},
	{EffectDelegate, "delegate"},
	{EffectExternalRead, "external_read"},
	{EffectExternalWrite, "external_write"},
	{EffectSkill, "skill"},
}

// Frequency 是 deferred schema 的频率档。
type Frequency int

const (
	// Hot：每步都发全量声明。流程控制与元工具必须落在这一档。
	Hot Frequency = iota
	// Warm：默认也发全量，但超出每步 token 预算时会被降级为 Cold。
	Warm
	// Cold：只在 system prompt 的目录里出现 name + Brief，
	// 模型调 load_tool 后才拿到 parameters schema。
	Cold
)

func (f Frequency) String() string {
	switch f {
	case Warm:
		return "warm"
	case Cold:
		return "cold"
	default:
		return "hot"
	}
}

// FrequencyFromString 解析配置里的档位名；无法识别时返回 ok=false。
func FrequencyFromString(s string) (Frequency, bool) {
	switch s {
	case "hot", "Hot", "":
		return Hot, true
	case "warm", "Warm":
		return Warm, true
	case "cold", "Cold":
		return Cold, true
	default:
		return Hot, false
	}
}

// TruncTier 是压缩层截断工具结果的优先顺序。零值 TruncNormal 与历史默认一致。
type TruncTier int

const (
	// TruncNormal：常规（历史上的优先级 1）。
	TruncNormal TruncTier = iota
	// TruncFirst：先截断（历史上的 0）——大体积、易过时、可重取的观察类结果。
	TruncFirst
	// TruncProtected：永不截断（历史上的 -1）——只有不可重取的分析产物才配。
	TruncProtected
)

// ToPriority 映射到压缩层使用的整数语义（0=先截 1=常规 -1=受保护）。
func (t TruncTier) ToPriority() int {
	switch t {
	case TruncFirst:
		return 0
	case TruncProtected:
		return -1
	default:
		return 1
	}
}

// DisplayMode 是 TUI 的展示意图。零值与历史行为一致（默认展开正文）。
type DisplayMode int

const (
	DisplayFull DisplayMode = iota
	// DisplayCompact：TUI 只渲染参数摘要，不展开工具结果正文。
	DisplayCompact
)
