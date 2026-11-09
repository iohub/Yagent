package app

import (
	"log/slog"
	"strings"
	"time"

	"yagent/internal/agents"
	"yagent/internal/config"
	"yagent/internal/llm"
	"yagent/internal/plasma"
)

// 本文件是生成式 Agent（plasma）在装配层的唯一落点：判可用性、装 trampoline、
// 启动期清孤儿 workdir。
//
// 三条纪律（ADR-15）：
//   - 不可用是**正常状态**：P10 之前 plasma.pyz 天然不存在，于是 delegate_meta 不进
//     工具面，原因经 Surface.Skipped 报出来；
//   - 流式必须来自**父进程** dispatcher：trampoline 的 Publisher 就是这里的 publisher，
//     不许新建一条事件流（否则 TUI 上会多出一份不归任何 Agent 的 chunk）；
//   - 判据只有一处：agents.PlasmaAvailable 与 spawner 的 Pkg 读同一个 plasmaPyzPath，
//     "判定通过但起不来"这种半装配状态从这里就不可能出现。

// buildPlasmaGenerator 装配生成器。返回 (nil, reason) 表示不可用，reason 会进
// delegate_meta 的 Skipped 原因与运行期错误文案。
//
// 注意这里**没有 Go 兜底实现**：ADR-15 明确"缺 Python 就降级"，因为一条"Go 也能生成"
// 的第二路径必然与 Python 侧的判定漂移，而且它正是 P9a 删掉的那份代码。
func buildPlasmaGenerator(cfg *config.Config, publisher agents.EventBus, engine llm.Engine) (agents.PlasmaGenerator, string) {
	if cfg == nil {
		return nil, "plasma 生成器未装配：配置未加载（cfg == nil）；修复：先完成 config 初始化"
	}

	// 与 spawner 共用同一个路径解析：判定通过而子进程起不来，只会变成"每次委派都 exit 1"。
	pyz, err := agents.PlasmaZipappPath()
	if err != nil {
		return nil, "plasma 生成器未装配：定位不到 plasma.pyz（" + err.Error() + "）；修复：检查 $HOME 可读"
	}
	if ok, reason := agents.PlasmaAvailableWith(cfg.Plasma, pyz); !ok {
		// 判据失败：连 trampoline 都不建。建了也会在第一次 Generate 时炸，
		// 而那时的错误与"生成失败了"无法区分。
		return nil, reason
	}

	compiler := &plasma.Compiler{Palette: plasma.PlasmaPalette}
	trampoline := &agents.PlasmaTrampoline{
		Spawner: &agents.ExecSpawner{
			Python: strings.TrimSpace(cfg.Plasma.Python),
			Pkg:    pyz,
		},
		LLM:       engine,
		Publisher: publisher,
		// 事件归属 "meta"：TUI 的流式面板与成本统计按这个名字归组，与路由档位同名。
		AgentName:    "meta",
		Manifest:     compiler.Manifest(),
		Compiler:     compiler,
		Elaborate:    agents.ElaborateWithDefaults(cfg),
		MaxLLMCalls:  cfg.Plasma.MaxLLMCalls,
		RetryCount:   plasmaRetryCount(cfg),
		SpawnTimeout: cfg.Plasma.CompileTimeout,
		// GCBase 留空 = os.TempDir()：与 plasma.NewWorkdir 的约定一致，
		// 启动期 GC 扫的也是同一个目录（见 gcPlasmaWorkdirs）。
		GCBase: "",
	}
	slog.Info("plasma: 生成器已装配", "python", cfg.Plasma.Python, "zipapp", pyz,
		"max_llm_calls", cfg.Plasma.MaxLLMCalls, "retry_count", plasmaRetryCount(cfg))
	return trampoline, ""
}

// plasmaRetryCount 是"编译失败后 --fix 的轮数"，语义已从"JSON 解析重试"改成编译重生成
// （ADR-11）。旋钮仍然只有 [agent] meta_retry_count 一个，不再加第二个。
func plasmaRetryCount(cfg *config.Config) int {
	if cfg == nil {
		return config.DefaultPlasmaConfig().MaxLLMCalls // 不会走到；调用方已判 nil
	}
	if cfg.Agent.MetaRetryCount > 0 {
		return cfg.Agent.MetaRetryCount
	}
	return 3
}

// gcPlasmaWorkdirs 在建 Director 之前清一次孤儿 workdir。
//
// 只有 trampoline 的 defer 是不够的：进程被 kill -9 时 defer 不跑，而每次
// delegate_meta 都会留下一个 0700 目录（里面可能有用户输入）。启动期清一次是补偿控制，
// 清理结果**必须**有一行可见账目 —— 静默清理与静默泄漏一样难以察觉。
func gcPlasmaWorkdirs(cfg *config.Config) {
	if cfg == nil || !cfg.Plasma.Enable {
		return
	}
	removed, err := plasma.GCWorkdirs("", cfg.Plasma.WorkdirTTL, time.Now())
	attrs := []any{"category", "plasma-gc", "removed", removed, "ttl", cfg.Plasma.WorkdirTTL}
	if err != nil {
		attrs = append(attrs, "error", err.Error())
	}
	slog.Info("plasma: 启动期 workdir GC", attrs...)
}
