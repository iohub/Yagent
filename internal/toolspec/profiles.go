package toolspec

// 七个内建 Agent 的能力画像。这里是"谁能干什么"的唯一表达：
// 过去它散在 5 个 lookupToolFunc/switch 里，漏一个 case 就是一个工具静默消失。
//
// 阅读方式：Grant 是能力白名单，Deny 在其后生效，Audience 由 Spec 自己声明受众。
// 每个画像的解析结果由 golden_test.go 钉死在 testdata/tool_surfaces.golden，
// 改这里必然要让 golden 一起变 —— 那个 diff 就是评审物。

// CogAll 是四个认知工具的合集，用于"该 Agent 允许自我反思"。
const CogAll = EffectCogReflect | EffectCogSubLLM | EffectCogDeep | EffectCogDesign

// RepoAll 是 codeseek 桥接的两族：语义检索 + 调用图。
const RepoAll = EffectCodeSemantic | EffectCodeGraph

// BrowserAll 是 14 个浏览器工具的四族。
const BrowserAll = EffectBrowserObserve | EffectBrowserAct | EffectBrowserEval | EffectBrowserArtifact

// ProfileDirector 是唯一的用户接口：只读分析 + 委派，绝不直接写文件。
//
// Palette 是 Meta 设计的 Custom Agent 能拿到的能力上限。今天它等价于
// director.go 里那个独立的 getToolFunc switch；额外补进 CodeSemantic，
// 因为 meta.prompt.md 一直在向模型广告这三个工具却没有实现（背景文档 §8 缺口）。
var ProfileDirector = Profile{
	Agent: "director",
	Grant: EffectFSRead | EffectSearchRegex | EffectCogDeep | EffectFlow |
		EffectContext | EffectDelegate | EffectToolLoad |
		// 外部只读工具与技能目录：Director 是唯一对话入口，用户装的外部能力
		// 必须先能被它看见，否则"加一个 MCP server"就毫无意义。
		// 外部位只给读：与 Deny FSWrite 同一取向 —— Director 绝不直接产生副作用。
		EffectExternalRead | EffectSkill,
	Deny: EffectFSWrite | EffectShell,
	Palette: EffectFSRead | EffectSearchRegex | EffectFSWrite | EffectShell |
		EffectCogReflect | EffectCogSubLLM | EffectCogDeep | EffectCodeSemantic | EffectFlow,
	// Repo 的调用图三件套对分析型 Custom Agent 也合理，但不进 Palette：
	// 今天 Meta 没被广告过，扩它属于行为变更，需要单独签字。
	//
	// print_dir_tree 与 deepthinking 对 Director 是"每轮都可能用"的主分析工具，
	// 留在 Cold 会让它每次都多付一次 load_tool 往返 —— 这里显式提回 Hot。
	// 子 Agent 侧不动：deepthinking 自己的描述就写着"常规手段失败后才用"。
	Promote:      []string{"print_dir_tree", "deepthinking"},
	RequiresExit: true,
}

// ProfileCoding 是高级开发工程师：完整读写 + Shell + 快照 + 委派 browser。
var ProfileCoding = Profile{
	Agent: "coding",
	Grant: EffectFSRead | EffectFSWrite | EffectSearchRegex | EffectShell | RepoAll |
		CogAll | EffectFlow | EffectCheckpoint |
		EffectDelegate | EffectContext | EffectToolLoad |
		// 唯一拿到 external_write 的画像：写类外部工具默认被 allow_write 挡在
		// 构建之外，能走到这里的都是用户显式开了口的 server。
		EffectExternalRead | EffectExternalWrite | EffectSkill,
	// 改函数前必须先查 callers，所以把调用图从 Cold 提回 Hot；
	// print_dir_tree 对编码任务是次要动作，留在 Cold。
	Promote:      []string{"find_function_caller"},
	RequiresExit: true,
}

// ProfileRepo 是架构师/审计员：只读 + 调用图 + 可委派 DevOps。
var ProfileRepo = Profile{
	Agent: "repo",
	Grant: EffectFSRead | EffectSearchRegex | RepoAll | EffectCogDeep | EffectFlow |
		EffectContext | EffectDelegate | EffectToolLoad |
		// 外部只读：审计一个集成了第三方 server 的仓库时，那些 server 的查询能力
		// 就是新的证据来源。写类不给：它是只读审计员。
		EffectExternalRead | EffectSkill,
	Deny: EffectCogReflect | EffectCogSubLLM | EffectCogDesign,
	// 审计的主用工具就是调用图与目录树，它们必须每步可见。
	Promote:      []string{"query_call_graph", "find_function_caller", "print_dir_tree"},
	RequiresExit: true,
}

// ProfileChat 是通用问答：没有任何文件系统能力。
var ProfileChat = Profile{
	Agent: "chat",
	Grant: EffectCogReflect | EffectCogSubLLM | EffectCogDeep | EffectFlow |
		EffectContext | EffectToolLoad |
		// 技能只读本地文件，是纯问答也能用上的"用户写的流程"；
		// 外部 IO 不给：没有文件系统能力的画像也不该有第三方网络/进程入口。
		EffectSkill,
	RequiresExit: true,
}

// ProfileDevOps 是系统管理员：只读文件 + Shell。
var ProfileDevOps = Profile{
	Agent: "devops",
	Grant: EffectShell | EffectFSRead | EffectSearchRegex |
		EffectCogReflect | EffectCogSubLLM | EffectFlow |
		EffectContext | EffectToolLoad |
		// 外部只读：诊断类 server（k8s/云厂商）正是它的用武之地。
		// 技能不给：它已经有 run_bash，运维流程走命令比走散文更可审计；
		// 外部写也不给 —— 那会变成一个比 run_bash 更难审计的写入口。
		EffectExternalRead,
	RequiresExit: true,
}

// ProfileBrowser 只拿到浏览器动作与流程控制：无 Shell、无文件写工具。
// 外部位与技能都不给：它的工具面刻意只有 14 个浏览器动作 + 流程控制，
// 加一个第三方入口就破坏了"这个 Agent 只能碰浏览器"这条可审计的边界。
var ProfileBrowser = Profile{
	Agent: "browser",
	Grant: BrowserAll | EffectFlow | EffectContext | EffectToolLoad,
	// 截图是"给出证据"这条主路径的一部分，不能被降级成需要额外一次 load_tool。
	Promote:      []string{"browser_screenshot"},
	RequiresExit: true,
}

// BuiltinProfiles 供启动自检与 golden 测试遍历。
func BuiltinProfiles() []Profile {
	return []Profile{
		ProfileDirector, ProfileCoding, ProfileRepo,
		ProfileChat, ProfileDevOps, ProfileBrowser,
	}
}

// IsBuiltinProfile 报告名字是否是内建画像（Custom Agent 的名字是动态的）。
func IsBuiltinProfile(agent string) bool {
	for _, p := range BuiltinProfiles() {
		if p.Agent == agent {
			return true
		}
	}
	return false
}
