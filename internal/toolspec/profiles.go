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
		EffectContext | EffectDelegate | EffectToolLoad,
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
		EffectDelegate | EffectContext | EffectToolLoad,
	// 改函数前必须先查 callers，所以把调用图从 Cold 提回 Hot；
	// print_dir_tree 对编码任务是次要动作，留在 Cold。
	Promote:      []string{"find_function_caller"},
	RequiresExit: true,
}

// ProfileRepo 是架构师/审计员：只读 + 调用图 + 可委派 DevOps。
var ProfileRepo = Profile{
	Agent: "repo",
	Grant: EffectFSRead | EffectSearchRegex | RepoAll | EffectCogDeep | EffectFlow |
		EffectContext | EffectDelegate | EffectToolLoad,
	Deny: EffectCogReflect | EffectCogSubLLM | EffectCogDesign,
	// 审计的主用工具就是调用图与目录树，它们必须每步可见。
	Promote:      []string{"query_call_graph", "find_function_caller", "print_dir_tree"},
	RequiresExit: true,
}

// ProfileChat 是通用问答：没有任何文件系统能力。
var ProfileChat = Profile{
	Agent: "chat",
	Grant: EffectCogReflect | EffectCogSubLLM | EffectCogDeep | EffectFlow |
		EffectContext | EffectToolLoad,
	RequiresExit: true,
}

// ProfileDevOps 是系统管理员：只读文件 + Shell。
var ProfileDevOps = Profile{
	Agent: "devops",
	Grant: EffectShell | EffectFSRead | EffectSearchRegex |
		EffectCogReflect | EffectCogSubLLM | EffectFlow |
		EffectContext | EffectToolLoad,
	RequiresExit: true,
}

// ProfileBrowser 只拿到浏览器动作与流程控制：无 Shell、无文件写工具。
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
