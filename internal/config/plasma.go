// 本文件维护 `[plasma]`（生成式 Agent 编译器，docs/plasma-architecture.md §7.4）的运行时配置。
//
// 与 blackboard.go / resume.go / blueprint.go 同构：plasma 的消费者散在 CLI（P7）、
// 加载期（P9）与进化器（P10），逐层穿参数会让每个入口各多一个入参，所以走
// "启动初始化 + 全局唯一事实源"。
//
// **两套取值约定不许混**（设计稿 §7.4 明写，根 AGENTS.md §11 坑 13/26）：
//
//	<=0 = 取默认   max_loaded_agents / max_llm_calls / behavioral_token_budget / promote_min_runs
//	0 本身有语义   catalog_line_chars（不裁剪） / store_ttl（不清理） / workdir_ttl（不清理孤儿目录）
//
// 后者必须靠 TOML 键的存在性区分"没写 → 默认"与"写了 0 → 关掉"，不能看零值。
package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// plasma 三档 grade 的名字。与 plasma.GradeProvisional/Verified/Promoted 逐字同值；
// config 不能 import plasma（那边是叶子包，反过来也会成环），所以这里钉一份：
// 装配侧的对账测试（internal/agents 的 registry_gate）比较两个集合，改一处就红。
const (
	PlasmaGradeProvisional = "provisional"
	PlasmaGradeVerified    = "verified"
	PlasmaGradePromoted    = "promoted"
)

// 默认路径写成含 `~` 的字面量（与设计稿 §7.4 一致）：空 = 用它们，展开由
// PlasmaStoreDir / PlasmaProposalDir 负责。写成绝对路径就没法在测试里挪 HOME。
const (
	DefaultPlasmaStorePath    = "~/.yagent/agents"
	DefaultPlasmaProposalPath = "~/.yagent/agents/proposals"
)

// DefaultPlasmaCatalogLineChars 是目录行的默认裁剪宽度；显式 0 = 不裁剪。
const DefaultPlasmaCatalogLineChars = 120

// PlasmaConfig 对应 [plasma]。键与设计稿 §7.4 一对一，多一个都没有：
// 判据是根 AGENTS.md §11 坑 25 ——"改这个值，有没有一条用户可观察的行为会变"。
type PlasmaConfig struct {
	// Enable 是整套生成式 Agent 机制的总开关。默认开；`YAGENT_PLASMA=0` 是急停
	// （判据在 NormalizePlasma 内部，见硬约束 8 的同一取向）。
	Enable bool `toml:"enable"`

	// Python 直通解释器路径。空 = PATH 上的 python3；显式路径用于指定版本/虚拟环境。
	Python string `toml:"python"`

	// Store 是内容寻址仓库根。空 = DefaultPlasmaStorePath（含 ~，用时展开）。
	Store string `toml:"store"`

	// MaxLoadedAgents 是加载期预算：超过按 grade→score→name 截断，并留一行可见省略账。
	// **<=0 = 默认**（12）。
	MaxLoadedAgents int `toml:"max_loaded_agents"`

	// CatalogLineChars 是 prompt 里目录行的裁剪宽度。**显式 0 = 不裁剪**，
	// 所以缺省判定看键的存在性；负数按"不裁剪"处理并告警。
	CatalogLineChars int `toml:"catalog_line_chars"`

	// MaxLLMCalls 是 trampoline 单次生成的采样上限（防 Python 侧死循环）。
	// **<=0 = 默认**（12）。
	MaxLLMCalls int `toml:"max_llm_calls"`

	// WorkdirTTL 是孤儿 workdir 的启动期 GC 门槛（进程被 kill -9 时 defer 不跑）。
	// **显式 0 = 不清理**；负值报错。
	WorkdirTTL time.Duration `toml:"workdir_ttl"`

	// CompileTimeout 是单次编译（含子进程）的超时。0 = 默认。
	CompileTimeout time.Duration `toml:"compile_timeout"`

	// StructuralFixtureTimeout 是 5a 结构夹具的单条超时。0 = 默认。
	StructuralFixtureTimeout time.Duration `toml:"structural_fixture_timeout"`

	// BehavioralFixtureTimeout 是 5b 行为夹具的单条超时。0 = 默认。
	BehavioralFixtureTimeout time.Duration `toml:"behavioral_fixture_timeout"`

	// BehavioralTokenBudget 是 5b 的 token 预算。**<=0 = 默认**（60000）。
	BehavioralTokenBudget int `toml:"behavioral_token_budget"`

	// SampleTimeout 是 `yagent plasma sample` 的单次调用上限。0 = 默认。
	SampleTimeout time.Duration `toml:"sample_timeout"`

	// AutoDelegateMinGrade 是"可以自动委派"的最低档位，只接受三档 grade 名字。
	// 认不出来的值**报错**而不是回落：它是自动委派的安全边界，静默回落成
	// verified 等于让没验证过的 Agent 被自动委派（fail-closed）。
	AutoDelegateMinGrade string `toml:"auto_delegate_min_grade"`

	// PromoteMinRuns 是 promote 需要的最小生产运行次数。**<=0 = 默认**（5）。
	// 刻意不与"0 = 不设门槛"混：配置漏填不能变成自动升级（见 plasma.CanPromote）。
	PromoteMinRuns int `toml:"promote_min_runs"`

	// StoreTTL 清理"未被 index 指向且超过 TTL"的 hash 目录。**显式 0 = 不清理**；
	// 负值报错。
	StoreTTL time.Duration `toml:"store_ttl"`

	// EvolveEnable 是离线进化器的开关（P10）。
	EvolveEnable bool `toml:"evolve_enable"`

	// EvolveInterval 是离线进化器的建议间隔。0 = 默认。
	EvolveInterval time.Duration `toml:"evolve_interval"`

	// ProposalDir 是"内建 Agent 提案"的落盘目录。空 = DefaultPlasmaProposalPath。
	ProposalDir string `toml:"proposal_dir"`
}

// DefaultPlasmaConfig 是整段缺省时的取值，与设计稿 §7.4 的表格逐格对齐。
//
// 不变量：这里的每个数字/时长都必须在 NormalizePlasma 的归一化里被**真的用到**，
// 否则它只是文档装饰（plasma_test.go 逐键比较"默认 vs 显式"来钉住这一点）。
func DefaultPlasmaConfig() PlasmaConfig {
	return PlasmaConfig{
		Enable:                   true,
		Python:                   "",
		Store:                    "",
		MaxLoadedAgents:          12,
		CatalogLineChars:         DefaultPlasmaCatalogLineChars,
		MaxLLMCalls:              12,
		WorkdirTTL:               time.Hour,
		CompileTimeout:           60 * time.Second,
		StructuralFixtureTimeout: 60 * time.Second,
		BehavioralFixtureTimeout: 10 * time.Minute,
		BehavioralTokenBudget:    60000,
		SampleTimeout:            10 * time.Minute,
		AutoDelegateMinGrade:     PlasmaGradeVerified,
		PromoteMinRuns:           5,
		StoreTTL:                 720 * time.Hour,
		EvolveEnable:             false,
		EvolveInterval:           30 * time.Minute,
		ProposalDir:              "",
	}
}

// PlasmaPresence 记录 [plasma] 里哪些值被**显式**写出（由 toml.MetaData 得出）。
// 只登记"0 本身有语义"的三个键：其余键的零值没有独立含义，靠 <=0 归一化就够，
// 多一个标记就多一处会漂的第二份事实（[blackboard] 的 summary_max_runes 同理）。
type PlasmaPresence struct {
	Section bool
	// Enable 的缺省是"开"而 Go 零值是 false —— 与 [blueprint]/[mcp]/[skills] 同一个坑（坑 26）：
	// 只看值会把"没写这个键"当成"用户关掉了整套机制"。
	Enable           bool
	CatalogLineChars bool
	WorkdirTTL       bool
	StoreTTL         bool
}

const plasmaKeyHint = "enable / python / store / max_loaded_agents / catalog_line_chars / " +
	"max_llm_calls / workdir_ttl / compile_timeout / structural_fixture_timeout / " +
	"behavioral_fixture_timeout / behavioral_token_budget / sample_timeout / " +
	"auto_delegate_min_grade / promote_min_runs / store_ttl / evolve_enable / " +
	"evolve_interval / proposal_dir"

var (
	plasmaMu     sync.RWMutex
	plasmaConfig = DefaultPlasmaConfig()
)

// NormalizePlasma 归一化缺省值并做合法性判定。
//
// 返回 error 而不是"warn 后回落"，是这一段的**有意选择**，与硬约束 12
// （认不出的字符串枚举 warn 后回落）刻意不同：
//   - 时长写错（"10 minutes"、负数）说明配置本身坏了，静默归零会让超时/清理
//     在无人察觉的情况下变成"没有"；
//   - grade 写错会改变自动委派的安全边界，回落成哪一档都可能在放行未验证的 Agent。
//
// 两者都是"宁可启动失败并点名，也不要静默按另一种策略跑"。
func NormalizePlasma(c PlasmaConfig, p PlasmaPresence) (PlasmaConfig, error) {
	d := DefaultPlasmaConfig()

	if !p.Section {
		// 整段没写过：字段内容没有意义，一律换成默认（负值也不报错 —— 它不可能来自
		// 用户，只可能是调用方残留的零值）。
		c = d
	} else {
		// 合法性检查先于缺省回落：负时长必须报出来，不能被"这个键没写"的默认值顶掉。
		// 顺序写反的症状是"我配了 -1s，它悄悄变成 1h 而没人说话"。
		if err := checkPlasmaDurations(&c); err != nil {
			return c, err
		}
		if err := checkPlasmaGrade(c.AutoDelegateMinGrade); err != nil {
			return c, err
		}

		c.Python = strings.TrimSpace(c.Python)
		c.Store = strings.TrimSpace(c.Store)
		c.ProposalDir = strings.TrimSpace(c.ProposalDir)

		if !p.Enable {
			c.Enable = d.Enable
		}
		// <=0 = 默认 的一组。
		if c.MaxLoadedAgents <= 0 {
			c.MaxLoadedAgents = d.MaxLoadedAgents
		}
		if c.MaxLLMCalls <= 0 {
			c.MaxLLMCalls = d.MaxLLMCalls
		}
		if c.BehavioralTokenBudget <= 0 {
			c.BehavioralTokenBudget = d.BehavioralTokenBudget
		}
		if c.PromoteMinRuns <= 0 {
			c.PromoteMinRuns = d.PromoteMinRuns
		}

		// 0 本身有语义 的一组：只有"没写这个键"才取默认。
		if !p.CatalogLineChars {
			c.CatalogLineChars = d.CatalogLineChars
		} else if c.CatalogLineChars < 0 {
			// 负数没有"更不裁剪"的含义，按不裁剪（0）处理并说出来。
			slog.Warn("plasma: catalog_line_chars 是负数，按 0（不裁剪）处理",
				"configured", c.CatalogLineChars)
			c.CatalogLineChars = 0
		}
		if !p.WorkdirTTL {
			c.WorkdirTTL = d.WorkdirTTL
		}
		if !p.StoreTTL {
			c.StoreTTL = d.StoreTTL
		}
		if c.AutoDelegateMinGrade == "" {
			c.AutoDelegateMinGrade = d.AutoDelegateMinGrade
		}
	}

	// 0 无语义的时长键取默认。三个"0 本身有语义"的键（workdir_ttl / store_ttl）
	// 不在这里，它们的 0 已由上面的 presence 分支保留。
	for _, k := range []struct {
		val *time.Duration
		def time.Duration
	}{
		{&c.CompileTimeout, d.CompileTimeout},
		{&c.StructuralFixtureTimeout, d.StructuralFixtureTimeout},
		{&c.BehavioralFixtureTimeout, d.BehavioralFixtureTimeout},
		{&c.SampleTimeout, d.SampleTimeout},
		{&c.EvolveInterval, d.EvolveInterval},
	} {
		if *k.val == 0 {
			*k.val = k.def
		}
	}

	// 急停开关写在**函数内部**（硬约束 8）：写两处的后果是一处忘，而漏掉那处的
	// 表现只是"还是开着"。与 YAGENT_BLUEPRINT / YAGENT_RESUME / YAGENT_CONCURRENCY 同构。
	if os.Getenv("YAGENT_PLASMA") == "0" {
		c.Enable = false
	}
	return c, nil
}

// checkPlasmaDurations 拒收负时长。负值没有"关掉"或"更短"的含义：
// 把它静默归零会让超时/清理变成"没有"，而那是最难在事后发现的一类改变。
func checkPlasmaDurations(c *PlasmaConfig) error {
	for _, k := range []struct {
		name         string
		val          *time.Duration
		zeroMeansOff bool
	}{
		{"workdir_ttl", &c.WorkdirTTL, true},
		{"store_ttl", &c.StoreTTL, true},
		{"compile_timeout", &c.CompileTimeout, false},
		{"structural_fixture_timeout", &c.StructuralFixtureTimeout, false},
		{"behavioral_fixture_timeout", &c.BehavioralFixtureTimeout, false},
		{"sample_timeout", &c.SampleTimeout, false},
		{"evolve_interval", &c.EvolveInterval, false},
	} {
		if *k.val >= 0 {
			continue
		}
		zero := "0 = 取默认"
		if k.zeroMeansOff {
			zero = "0 = 关掉该行为"
		}
		return fmt.Errorf("[plasma] %s = %v 不能为负（%s）", k.name, *k.val, zero)
	}
	return nil
}

// checkPlasmaGrade 只接受三档 grade 名字（与 plasma.Grade 逐字同值）。
// 空串按"没写"处理由调用方先填默认，所以这里只拒"写了但不认识"。
func checkPlasmaGrade(g string) error {
	switch g {
	case "", PlasmaGradeProvisional, PlasmaGradeVerified, PlasmaGradePromoted:
		return nil
	default:
		return fmt.Errorf("[plasma] auto_delegate_min_grade = %q 不是合法档位（provisional / verified / promoted）", g)
	}
}

// SetGlobalPlasma 归一化并写入全局单例。error 必须由调用方传播 ——
// 忽略它等于把"配置坏了"变成"按默认值跑"。
func SetGlobalPlasma(c PlasmaConfig, p PlasmaPresence) error {
	normalized, err := NormalizePlasma(c, p)
	if err != nil {
		return err
	}

	plasmaMu.Lock()
	plasmaConfig = normalized
	plasmaMu.Unlock()

	attrs := []any{
		"enable", normalized.Enable,
		"python", normalized.Python,
		"store", PlasmaStoreDir(),
		"max_loaded_agents", normalized.MaxLoadedAgents,
		"catalog_line_chars", normalized.CatalogLineChars,
		"max_llm_calls", normalized.MaxLLMCalls,
		"workdir_ttl", normalized.WorkdirTTL,
		"compile_timeout", normalized.CompileTimeout,
		"structural_fixture_timeout", normalized.StructuralFixtureTimeout,
		"behavioral_fixture_timeout", normalized.BehavioralFixtureTimeout,
		"behavioral_token_budget", normalized.BehavioralTokenBudget,
		"sample_timeout", normalized.SampleTimeout,
		"auto_delegate_min_grade", normalized.AutoDelegateMinGrade,
		"promote_min_runs", normalized.PromoteMinRuns,
		"store_ttl", normalized.StoreTTL,
		"evolve_enable", normalized.EvolveEnable,
		"evolve_interval", normalized.EvolveInterval,
		"proposal_dir", PlasmaProposalDir(),
	}
	if !PlasmaEnabled() {
		attrs = append(attrs, "kill_switch", "YAGENT_PLASMA=0")
	}
	slog.Info("plasma: 生成式 Agent 配置", attrs...)
	return nil
}

// GlobalPlasma 返回当前生效的 [plasma] 配置。
func GlobalPlasma() PlasmaConfig {
	plasmaMu.RLock()
	defer plasmaMu.RUnlock()
	return plasmaConfig
}

// PlasmaEnabled 是唯一的启用判据（生成、加载、自动委派、进化四处共用）。
// 急停判据在 NormalizePlasma 内部已经写进 Enable，这里只读它 ——
// 两处都读环境变量的后果是漂移，不是冗余。
func PlasmaEnabled() bool {
	return GlobalPlasma().Enable
}

// PlasmaStoreDir 返回展开后的仓库根。
func PlasmaStoreDir() string { return expandUserPath(GlobalPlasma().Store, DefaultPlasmaStorePath) }

// PlasmaProposalDir 返回展开后的提案目录。
func PlasmaProposalDir() string {
	return expandUserPath(GlobalPlasma().ProposalDir, DefaultPlasmaProposalPath)
}

// expandUserPath 展开 `~` 并处理"空 = 默认"。只认前缀 `~/`：`~user` 形态在容器里
// 不可靠，而本仓库的默认值全是 `~/.yagent/...`。
func expandUserPath(value, def string) string {
	p := strings.TrimSpace(value)
	if p == "" {
		p = def
	}
	if !strings.HasPrefix(p, "~/") {
		return filepath.Clean(p)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		// 拿不到 home 时原样返回：调用方会看到 `~` 开头的路径并在 IO 上失败，
		// 比造一个 CWD 相对的目录更安全（那会在仓库里凭空多出一个 agents/）。
		return p
	}
	return filepath.Join(home, p[2:])
}
