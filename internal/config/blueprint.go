// 本文件维护 `[blueprint]`（仓库蓝图，docs/repo-blueprint-design.md v4）的运行时配置。
//
// 与 resume.go / blackboard.go 同构：蓝图的派生、注入、落盘钩子、导出、GC 五处都要问同一件事，
// 而逐层穿参数会让六个子 Agent 的构造函数各多一个入参，所以走"启动初始化 + 全局唯一事实源"。
//
// 键数被刻意压在 5 个（设计稿 §10）：判据是 AGENTS.md §11 坑 25 ——
// "改这个值，有没有一条用户可观察的行为会变"。验锚、去重、修订快照都**没有**"关掉"的
// 用户语义，因此是常数而不是键。
package config

import (
	"log/slog"
	"os"
	"strings"
	"sync"
)

// BlueprintConfig 对应 [blueprint]。
type BlueprintConfig struct {
	// Enable 是总开关。关掉就是"这套机制整体退出"：不派生、不注入、不提供 read_blueprint、
	// 不折叠落盘、不导出 —— 留半个开关的后果与 [resume] 那条一样，
	// 会让"为什么这个项目没有蓝图"变成一个新问题。
	Enable bool `toml:"enable"`

	// InjectInto 是常驻骨架注入的三个位置。它是**成本自救阀**：
	// 觉得注入太贵时先把某个 Agent 摘掉，而不是去改预算数字。
	// 显式写空数组 = 三处都不注入（与"没写这一段"不同，那是默认三处）。
	InjectInto []string `toml:"inject_into"`

	// IndexMaxTokens 是常驻骨架（形状②：模块 + 依赖边）的 token 硬顶。
	// 本仓实测 874（42 片 / 103 条边），默认 1000 留余量；<=0 或没写 = 默认。
	// 超预算时截断必须写进 header（`+N modules not shown`），不许静默少一片。
	IndexMaxTokens int `toml:"index_max_tokens"`

	// ProseBudgetTokens 是按需路径（read_blueprint 与 Repo/Coding 的散文注入）的渲染预算。
	// 沿用今天 KV 记忆那套的上限数字，语义从"整份记忆"变成"一份文档的一次渲染"。
	ProseBudgetTokens int `toml:"prose_budget_tokens"`

	// ExportPath 非空时，每次折叠后把渲染结果同步重写到该路径（相对路径按项目根解析）。
	// 默认空 = 不往用户仓库里写任何东西。真源恒在 ~/.yagent/data/blueprint/{projectID}/，
	// 导出物是**可幂等重生成**的产物：被 stash / 误删都不丢信息（设计稿 §2 R7、§9）。
	ExportPath string `toml:"export_path"`
}

// DefaultBlueprintConfig 是整段缺省时的取值。
func DefaultBlueprintConfig() BlueprintConfig {
	return BlueprintConfig{
		Enable:            true,
		InjectInto:        []string{"director", "repo", "coding"},
		IndexMaxTokens:    1000,
		ProseBudgetTokens: 1500,
		ExportPath:        "",
	}
}

// BlueprintPresence 记录 [blueprint] 里哪些值被**显式**写出（由 toml.MetaData 得出）。
//
// enable 必须看键的存在性而不是零值：它的缺省语义是"开"，而 Go 零值是 false ——
// 直接看值会把"没写"当成"用户关掉了"（AGENTS.md §11 坑 26，与 enable_ultimate_compression
// 和 [git_checkpoint] 那次修复同一个病）。
type BlueprintPresence struct {
	Section    bool
	Enable     bool
	InjectInto bool
}

const blueprintKeyHint = "enable / inject_into / index_max_tokens / prose_budget_tokens / export_path"

// blueprintInjectSites 是 inject_into 认的名字。三个，不多不少：
// 只有这三处有 system prompt 级的常驻注入点（设计稿 §6）。写别的会被 warn 后丢掉 ——
// 把它静默当成"没写"等于用户的配置在悄悄失效。
var blueprintInjectSites = map[string]bool{"director": true, "repo": true, "coding": true}

var (
	blueprintMu     sync.RWMutex
	blueprintConfig = DefaultBlueprintConfig()
)

// SetGlobalBlueprint 写入全局配置并归一化缺省值。
func SetGlobalBlueprint(c BlueprintConfig, p BlueprintPresence) {
	defaults := DefaultBlueprintConfig()

	if !p.Section {
		c = defaults
	} else {
		if !p.Enable {
			c.Enable = defaults.Enable
		}
		// nil = 没写这个键 → 默认三处；显式 [] = 用户就是要关掉全部注入。
		if c.InjectInto == nil && !p.InjectInto {
			c.InjectInto = defaults.InjectInto
		}
		c.InjectInto = normaliseInjectInto(c.InjectInto)
		if c.IndexMaxTokens <= 0 {
			c.IndexMaxTokens = defaults.IndexMaxTokens
		}
		if c.ProseBudgetTokens <= 0 {
			c.ProseBudgetTokens = defaults.ProseBudgetTokens
		}
		c.ExportPath = strings.TrimSpace(c.ExportPath)
	}

	blueprintMu.Lock()
	blueprintConfig = c
	blueprintMu.Unlock()

	attrs := []any{
		"enable", c.Enable,
		"inject_into", strings.Join(c.InjectInto, ","),
		"index_max_tokens", c.IndexMaxTokens,
		"prose_budget_tokens", c.ProseBudgetTokens,
		"export_path", c.ExportPath,
	}
	if !BlueprintEnabled() {
		attrs = append(attrs, "kill_switch", "YAGENT_BLUEPRINT=0")
	}
	slog.Info("blueprint: 仓库蓝图配置", attrs...)
}

// 每个键的消费者（写在这里是为了让"写了没人读"无处藏身；AGENTS.md §11 坑 25）：
//
//	enable              → app 启动时解析存储根（R1，`Yagent.Initialize`），
//	                      之后由派生 / 注入 / read_blueprint / 落盘钩子 / GC 五处共问（R2–R8，I8）
//	index_max_tokens    → 常驻骨架渲染（R3）
//	inject_into         → 三处 system prompt 注入点（R4）
//	prose_budget_tokens → Repo/Coding 的散文渲染与 read_blueprint 分页（R4/R6）
//	export_path         → 折叠后的幂等重渲染导出（R8）
//
// R1 只落地 enable 的真实读取 + 其余四个的解析、归一化与逐键测试（blueprint_test.go 里
// 每个键都有一条"改值就必须变"的断言）。分阶段是刻意的：蓝图是一条分支 R1→R8 一次交付，
// 但任何一个键如果在消费者出现之前就被合进 main，这个注释就是它该被删掉的信号。

// normaliseInjectInto 去空白、转小写、去重，并丢掉不认识的名字（每次丢都 warn）。
func normaliseInjectInto(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, raw := range in {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" {
			continue
		}
		if !blueprintInjectSites[name] {
			slog.Warn("blueprint: inject_into 里有不认识的名字，已忽略",
				"value", raw, "expected", "director / repo / coding")
			continue
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// GlobalBlueprint 返回当前生效的 [blueprint] 配置。
func GlobalBlueprint() BlueprintConfig {
	blueprintMu.RLock()
	defer blueprintMu.RUnlock()
	return blueprintConfig
}

// BlueprintEnabled 是唯一的启用判据（派生、注入、工具、落盘钩子、GC 五处共用，I8）。
// 环境变量 YAGENT_BLUEPRINT=0 是 kill-switch，与 YAGENT_BLACKBOARD / YAGENT_RESUME 同构：
// 出问题时先关掉整套机制再排查，而不是改配置文件再重启。
//
// internal/blueprint 自己不读这个环境变量 —— 判据只能有一份，读它的必须是调用方。
func BlueprintEnabled() bool {
	if os.Getenv("YAGENT_BLUEPRINT") == "0" {
		return false
	}
	return GlobalBlueprint().Enable
}
