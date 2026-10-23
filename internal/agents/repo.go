package agents

import (
	"context"
	_ "embed"
	"fmt"
	"yagent/internal/blackboard"
	"yagent/internal/toolspec"

	"yagent/internal/config"
	"yagent/internal/thinklink"
	"yagent/internal/tools"

	"yagent/internal/llm"
)

//go:embed repo.prompt.md
var repoPrompt string

// repoFinishReminder 是 Repo 停在纯文本上时的那一句提醒。
//
// 它刻意**不是**通用那条（"立刻调用工具去 explore/edit/run/verify"）：Repo 的报告本身就是
// 交付物，让它再去探索一轮是把已经做完的分析推翻重做。这里要补的只有收尾动作 ——
// `agent_exit`，以及挂在那上面的 handoff 与 blueprint[] 两条通道。
const repoFinishReminder = "SYSTEM REMINDER: your analysis above is the deliverable, but a text-only reply " +
	"ends this run *without handing anything back*: the caller sees no handoff, and the repository " +
	"blueprint stays unannotated because both are collected from agent_exit.\n\n" +
	"Call `agent_exit` now. Do not repeat or rewrite the report. In it put:\n" +
	"- `reason`: one line (what you analyzed).\n" +
	"- `handoff`: conclusion + verifiable facts with `where` + status.\n" +
	"- `blueprint`: one entry per directory you actually opened this run and reached a durable " +
	"conclusion about — section/module/field/claim/where; omit it only if you read no directory " +
	"or every finding is already annotated correctly (then say nothing rather than restate it)."

type RepoAgent struct {
	BaseAgent
	promptFmt   PromptFormatter
	env         Env
	ecCfg       config.EnhancedCommanderConfig
	params      RepoAgentParams
	maxSteps    int
	stepRetries int

	// DevOps-Agent（可选，nil 表示未注入，delegate_devops 将返回友好提示）
	devops *DevOpsAgent
}

// RepoAgentParams 聚合 NewRepoAgent 所需的全部依赖。
// 用具名字段替代原先 14 个位置参数，让调用点自文档化，且增删依赖不再破坏函数签名。
type RepoAgentParams struct {
	Formatter   PromptFormatter
	Publisher   EventBus
	Env         Env
	Files       FileToolSet
	Search      SearchToolSet
	RepoOps     RepoToolSet
	Flow        FlowToolSet
	DeepThinker DeepThinker
	Guard       *tools.WorkspaceGuard

	EnhancedCommanderCfg config.EnhancedCommanderConfig

	// Blueprint 是仓库蓝图的注入视图（nil = 这个项目没有蓝图）。
	// 放在 params 上而不是结构体上：每次委派走 SpawnForDelegation 克隆自身，
	// 克隆只搬 params —— 挂在结构体上的那份会在下一次委派里变成共享可变状态（C4/D2）。
	Blueprint *BlueprintView

	// External 是外部工具规格与技能通道（见 external.go）。同样只放 params：
	// 浅拷整个结构体时它自动跟着走，不需要逐字段搬运。
	External External

	LLM         llm.Engine
	MaxSteps    int
	StepRetries int
}

// NewRepoAgent 构造 RepoAgent：先填充结构体字段，再装配工具适配器
// （tools.json 常规工具 + delegate_devops + 便签回读）。
func NewRepoAgent(p RepoAgentParams) *RepoAgent {
	self := &RepoAgent{
		BaseAgent: BaseAgent{
			LLM:       p.LLM,
			Publisher: p.Publisher,
		},
		promptFmt:   p.Formatter,
		env:         p.Env,
		ecCfg:       p.EnhancedCommanderCfg,
		maxSteps:    p.MaxSteps,
		stepRetries: p.StepRetries,
	}
	self.params = p
	return self
}

// devopsDelegate 是 Repo 自己的委派实现：只认 devops 一个目标，并沿用历史的
// "友好降级"文案（DevOps 未接线/执行失败都不抛错，而是把说明回给模型）。
// 与 Director 的委派流水线不同：这里不写便签、不做档位路由、不落 artifact。
func (a *RepoAgent) devopsDelegate(ctx context.Context, target string, params map[string]interface{}) (interface{}, error) {
	if target != "devops" {
		return nil, fmt.Errorf("Repo-Agent 只能委派给 devops，收到 %q", target)
	}
	task, ok := params["task"].(string)
	if !ok {
		return nil, fmt.Errorf("task parameter required")
	}
	// 每次调用一个新实例：共享单例的 DevOps 一旦被两条并发 Repo 委派同时用，
	// 今天没有可变状态不代表以后没有（见 spawn.go 的不变量）。
	devops := a.devopsFor(ctx)
	if devops == nil {
		return "[DevOps-Agent unavailable] DevOps-Agent has not been wired to Repo-Agent. Complete the analysis with the information you have and note that the operational task could not be delegated.", nil
	}
	result, err := devops.Run(ctx, task)
	if err != nil {
		return fmt.Sprintf("[DevOps-Agent execution failed] task: %q, error: %v. You may retry with a clearer task description, or note the failure in your analysis result.", task, err), nil
	}
	return result.Text, nil
}

func (a *RepoAgent) Name() string {
	return "Repo-Agent"
}

// SetDevOpsAgent 注入 DevOps-Agent 依赖，使 delegate_devops 工具可用。
// DevOps-Agent 可选，不设置时 delegate_devops 调用会返回友好提示。
func (a *RepoAgent) SetDevOpsAgent(devops *DevOpsAgent) {
	a.devops = devops
}

func (a *RepoAgent) Run(ctx context.Context, input string) (AgentResult, error) {
	systemPrompt := repoPrompt

	if a.env.ProjectPath() == "" {
		return AgentResult{}, fmt.Errorf("project_dir is empty")
	}

	systemPrompt = a.promptFmt.FormatPrompt(systemPrompt)

	// [仓库蓝图] 常驻骨架（形状②：模块 + 依赖边）。
	// 只进这一次 LLM 调用的 system prompt，绝不写回 ConversationMemory/rollout（I2）——
	// 第二/三档压缩会整段重写消息，进 messages 的东西会被摘要吞掉（§11 坑 11）。
	systemPrompt += a.params.Blueprint.Resident()

	cfg := DefaultExecutorConfig()
	cfg.SystemPrompt = systemPrompt
	cfg.UserInput = input
	// 工具面：只读 + 调用图 + deepthinking + 委派 devops + 便签/蓝图回读。
	deps := depsFor("repo", a.params.Env, a.params.Guard, blackboard.BoardIDFrom(ctx), a.params.Blueprint, a.params.External)
	deps.Files = a.params.Files
	deps.Search = a.params.Search
	deps.Repo = a.params.RepoOps
	deps.Flow = a.params.Flow
	deps.Deep = a.params.DeepThinker
	deps.Delegate = a.devopsDelegate
	cfg.Surface = surfaceFor(toolspec.ProfileRepo, deps, a.Name())
	cfg.SkillCatalog = skillCatalogFor("repo", a.params.External.Skills)
	cfg.LLM = a.LLM
	cfg.MaxSteps = a.maxSteps
	cfg.StepRetries = a.stepRetries
	cfg.Publisher = a.Publisher
	cfg.AgentName = a.Name()
	cfg.SystemAsHuman = true // RepoAgent uses Human role for its prompt
	// repo.prompt.md 的 Output Contract 要求模型以 agent_exit 收尾（handoff 与 blueprint 都挂在它上面）；
	// 不置这个标志时 agent_exit 只是"又执行了一次工具"，循环继续烧到 MaxSteps，
	// 而交付物变成后续那几轮的文本 —— 便签正文与 artifact 全文跟着全错。
	cfg.StopOnFinish = true

	// 停在文本上时补一次收尾提醒。存在的理由是实测：10-17~10-19 的 10 份 Repo rollout 里
	// 7 份 stop_reason=plain_text、一次 agent_exit 都没有 —— 于是 handoff 与蓝图写通道
	// 同时失效，而这两条通道的采集点都在 agent_exit 上。
	// 上限给 1 而不是更多：模型坚持用文本交付时，第二次催促只是多烧一轮。
	cfg.NoActionRetryLimit = 1
	cfg.NoActionReminder = repoFinishReminder

	// 上下文压缩配置（tool 结果截断）
	ec := a.ecCfg
	cfg.EnableContextCompression = ec.EnableContextCompression
	cfg.ContextCompressionThreshold = ec.ContextCompressionThreshold
	cfg.ToolResultKeepTokens = ec.ToolResultKeepTokens

	// 一次委派一个 store：RebuildPrompt 保留全部用户输入，本实例会被多次委派复用。
	store := thinklink.NewStore(0)
	store.AddUserInput(input, 0)
	cfg.UltimateThinkLink = store
	cfg.UltimateKeepPlans = ec.UltimateCompressionKeepPlans // 0=全部保留
	cfg.EnableUltimateCompression = ec.EnableUltimateCompression

	result, err := RunAgentLoop(ctx, cfg)
	if err != nil {
		return AgentResult{}, err
	}

	// Full 变体：Text 被程序化消费（Director 层 delegate_repo 结果 → RepoSummary
	// 注入 coding/browser 系统提示），需恒为完整输出；
	// LLM 上下文由 Director 层 FormatForDirector 走分级摘要。
	agentResult := FinalizeResultFull(a.Name(), input, result)

	// [仓库蓝图] 落盘钩子：仓库级持久知识只有这一个 sink。
	// 只做"解析 + 校验 + 一次文件写"（ms 级），跑在委派返回的这条 goroutine 上，
	// 不起线程、不调模型，也**绝不向 Repo 的返回值传播错误**（§8 纪律 2 / I11）。
	// 返回值是这次 fold 的账目（提交/写入/逐条拒收），随结果一起回给 Director ——
	// 只进日志的话，"四条散文全被拒收"在对话里等价于"这次委派顺利结束"。
	agentResult.BlueprintAccount = NewBlueprintCommit(a.params.Blueprint).Commit(ctx, a.Name(), agentResult)

	return agentResult, nil
}
