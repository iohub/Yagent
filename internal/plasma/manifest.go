package plasma

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"yagent/internal/toolspec"
)

// 本文件是 Python 侧唯一的**只读**能力/码表来源（设计稿 §6.3 的 manifest.json）。
//
// 为什么必须由 Go 发布而不是让 Python 自己写一份：
//   - 工具集合的唯一判据是 toolspec 注册表 + PlasmaPalette（`Effect.Has(palette)`，
//     与准入侧 `runCustomAgent` 的 `MatchNames` 同一个常量）；
//   - 诊断码的唯一来源是 diag.go 的码表；
//   - IR 默认值的唯一来源是 Elaborate 用的 Defaults。
//
// 三样东西只要在 Python 侧重写一遍，两侧就会漂 —— 而漂移的表现是"生成器选了一个
// Go 侧会拒的工具"，也就是一次必然失败的 trampoline 往返。
//
// 叶子约束：只 import stdlib + toolspec。

// ManifestSchemaVersion 与 IR 的 SchemaVersion 分开编号：manifest 是给 Python 的
// 只读契约，它可以独立于 IR 演进（加一个字段不必让旧 bundle 失效）。
const ManifestSchemaVersion = 1

// Manifest 是 workspace 里 manifest.json 的内容（§6.3：Go 写、Python 只读）。
type Manifest struct {
	SchemaVersion int `json:"schema_version"`
	// CapabilityLock 是其余字段的 sha256。它进 bundle.capability_lock，
	// 让"这份 bundle 是按哪一版能力清单生成的"可以溯源；注册表一变，锁就变。
	CapabilityLock string `json:"capability_lock"`
	// Palette 是本次生成允许的能力名（Effect.Names 的词表，顺序稳定）。
	Palette []string `json:"palette"`
	// Tools 是可选工具池，按名字字典序。
	Tools []ManifestTool `json:"tools"`
	// Codes 是诊断码表，按码字典序。
	Codes []ManifestCode `json:"codes"`
	// Defaults 是 Elaborate 会用的那套 IR 默认值，供 Python 生成时对齐。
	Defaults ManifestDefaults `json:"defaults"`
}

// ManifestTool 是工具池的一行：名字 / 能力名 / 一句话描述。
type ManifestTool struct {
	Name    string   `json:"name"`
	Effects []string `json:"effects"`
	Brief   string   `json:"brief"`
}

// ManifestCode 是码表的一行。
type ManifestCode struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Summary  string `json:"summary"`
}

// ManifestDefaults 是 DefaultDefaults() 的跨语言形态。
// LLMTimeout 用文本而不是纳秒：Python 侧拿到 120 秒比拿到 120000000000 更不容易错。
type ManifestDefaults struct {
	Lang                     string   `json:"lang"`
	MaxSteps                 int      `json:"max_steps"`
	StopOnFinish             bool     `json:"stop_on_finish"`
	LLMTimeout               string   `json:"llm_timeout"`
	Tier                     string   `json:"tier"`
	CompressionEnabled       bool     `json:"compression_enabled"`
	KeepToolResultTokens     int      `json:"keep_tool_result_tokens"`
	MaxParallelReadOnlyTools int      `json:"max_parallel_read_only_tools"`
	Deny                     []string `json:"deny"`
}

// Manifest 汇总当前注册表下这份 palette 的能力清单。
//
// 工具集合用 c.Palette（不是 c.effective()）：升级批准是**按 bundle hash** 记的，
// 对新生成的一份 bundle 没有意义 —— 把已批准的能力位发给生成器等于让它以为
// 那些工具默认可选。
func (c *Compiler) Manifest() *Manifest {
	palette := c.Palette
	m := &Manifest{
		SchemaVersion: ManifestSchemaVersion,
		Palette:       palette.Names(),
		Tools:         manifestTools(palette),
		Codes:         manifestCodes(),
		Defaults:      manifestDefaults(),
	}
	m.CapabilityLock = m.capabilityLock()
	return m
}

// manifestTools 是工具池判据的唯一实现（`Effect.Has(palette)`）：P9a 之前 agents 侧还有
// 一半（`renderCustomAgentToolPool` 渲染给 Meta-Agent prompt 的表格），Go 侧生成器删除后
// 那半份一起消失 —— 表格现在由 Python 从本 manifest 渲染。刻意不调 Spec.AvailableFor —— 那是运行期装配属性
// （某个 MCP server 没起来），而 manifest 描述的是**能力契约**，不是这台机器的状态。
func manifestTools(palette toolspec.Effect) []ManifestTool {
	out := make([]ManifestTool, 0, len(toolspec.Specs()))
	for _, s := range toolspec.Specs() {
		if !s.Effect.Has(palette) {
			continue
		}
		out = append(out, ManifestTool{
			Name:    s.Name,
			Effects: s.Effect.Names(),
			Brief:   s.Brief,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// manifestCodes 把 diag.go 的码表搬成跨语言形态。单一来源是 diagCatalog，
// 这里不做任何筛选或补充 —— 少一条的后果是 Python 侧认不出那个码。
func manifestCodes() []ManifestCode {
	catalog := DiagCatalog()
	out := make([]ManifestCode, 0, len(catalog))
	for _, c := range catalog {
		out = append(out, ManifestCode{Code: c.Code, Severity: string(c.Severity), Summary: c.Summary})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

// manifestDefaults 用 DefaultDefaults()（Elaborate 在调用方不覆盖时用的那套）。
func manifestDefaults() ManifestDefaults {
	d := DefaultDefaults()
	return ManifestDefaults{
		Lang:                     d.Lang,
		MaxSteps:                 d.MaxSteps,
		StopOnFinish:             d.StopOnFinish,
		LLMTimeout:               d.LLMTimeout.String(),
		Tier:                     d.Tier,
		CompressionEnabled:       d.CompressionEnabled,
		KeepToolResultTokens:     d.KeepToolResultTokens,
		MaxParallelReadOnlyTools: d.MaxParallelReadOnlyTools,
		Deny:                     append([]string(nil), d.Deny...),
	}
}

// capabilityLock 归一化后再哈希：把 CapabilityLock 自身清零，其余字段按结构体顺序
// 序列化（encoding/json 对结构体字段顺序稳定、对 slice 不去重）。于是
// "同一注册表 + 同一 palette → 同一把锁"，任何一项内容变化都会换锁。
func (m *Manifest) capabilityLock() string {
	if m == nil {
		return ""
	}
	clone := *m
	clone.CapabilityLock = ""
	raw, err := json.Marshal(clone)
	if err != nil {
		// 结构体里全是 string/int/slice，序列化不可能失败；真失败了给一把
		// 可区分的空锁而不是 panic（manifest 是只读契约，不能让它掀掉编译）。
		return fmt.Sprintf("unhashable:%v", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
