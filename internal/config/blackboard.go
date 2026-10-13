// 本文件维护跨 Agent 便签板（docs/blackboard-handoff-design.md §9）的运行时配置。
//
// 与 deferred_tools.go 同构：便签的索引与预算被装配层与执行循环深处读取
// （agents/result.go、agents/director_surface.go、app.go 的 GC），
// 逐层穿参数会让 6 个 Agent 的构造函数各多一个入参，所以走"启动初始化 + 全局唯一事实源"。
package config

import (
	"log/slog"
	"sync"
	"time"
)

// BlackboardConfig 对应 [blackboard]。
type BlackboardConfig struct {
	// IndexMaxNotes 是索引每次最多列几条便签。
	// **0 表示关掉索引注入**（应急阀门），所以它不能按零值兜底成默认值——
	// 需要 BlackboardPresence.IndexMaxNotes 来区分"显式写 0"与"没写"。负数是脏值 → 默认。
	IndexMaxNotes int `toml:"index_max_notes"`

	// IndexLineChars 是索引单行的 rune 预算。
	// 显式写 0 = **不裁剪行**（调用方会换算成 blackboard.MaxRenderRunes）；
	// 负数或没写 = 默认 160。0 有含义，所以它也需要 presence 标记。
	IndexLineChars int `toml:"index_line_chars"`

	// SummaryMaxRunes 是给 Director 的 handoff 摘要预算（ContextPack 节选同源）。
	// <=0 → 默认值。
	SummaryMaxRunes int `toml:"summary_max_runes"`

	// TTL 是 board 目录的存活时长，超期整目录清理。"0" = 不清理（同样需要存在性区分）。
	TTL time.Duration `toml:"ttl"`

	// MaxNotesPerBoard 是每个 board 保留的最新便签数，超出的旧便签删除。
	// 0 或负数都归默认 200 —— 它没有"关掉"的语义，想放宽就写个大数字。
	MaxNotesPerBoard int `toml:"max_notes_per_board"`
}

// DefaultBlackboardConfig 是整段缺省时的取值。
// 索引预算取 12×160 rune：委派一次给子 Agent、Director 每轮各一次，
// 最坏约 2k token——够覆盖一次任务里绝大多数委派轮次，又不至于压过任务描述本身。
func DefaultBlackboardConfig() BlackboardConfig {
	return BlackboardConfig{
		IndexMaxNotes:    12,
		IndexLineChars:   160,
		SummaryMaxRunes:  1200,
		TTL:              168 * time.Hour,
		MaxNotesPerBoard: 200,
	}
}

// BlackboardPresence 记录 [blackboard] 里哪些值被**显式**写出（由 toml.MetaData 得出）。
//
// 为什么必须有它：本表里有三个"0 是有意义的"取值（关索引 / 不裁剪行 / 不清理），
// 而"用户没写这一段"解析出来同样是 0。只看零值会在两个方向上都出错——
// 要么把用户的关闭意图当成没写而强行开启，要么反过来静默关掉新能力
// （[browser] enabled 的零值坑就是后一种）。
// max_notes_per_board 与 summary_max_runes 没有"关掉"的语义，0/负数都归默认，不需要标记。
type BlackboardPresence struct {
	Section        bool
	IndexMaxNotes  bool
	IndexLineChars bool
	TTL            bool
}

// blackboardKeyHint 是 [blackboard] 失效键告警里给出的正确键名清单。
const blackboardKeyHint = "index_max_notes / index_line_chars / summary_max_runes / ttl / max_notes_per_board"

var (
	blackboardMu     sync.RWMutex
	blackboardConfig = DefaultBlackboardConfig()
)

// SetGlobalBlackboard 写入全局配置并归一化缺省值。
func SetGlobalBlackboard(c BlackboardConfig, p BlackboardPresence) {
	if !p.Section {
		c = DefaultBlackboardConfig()
	} else {
		// 没写、或写了脏值（负数）都取默认；显式 0 才是"关掉索引注入"的阀门。
		if !p.IndexMaxNotes || c.IndexMaxNotes < 0 {
			c.IndexMaxNotes = DefaultBlackboardConfig().IndexMaxNotes
		}
		if !p.TTL {
			c.TTL = DefaultBlackboardConfig().TTL
		}
		// 显式 0 = 不裁剪行（负数才是脏值），所以这里不能写成 <= 0。
		if !p.IndexLineChars || c.IndexLineChars < 0 {
			c.IndexLineChars = DefaultBlackboardConfig().IndexLineChars
		}
		if c.SummaryMaxRunes <= 0 {
			c.SummaryMaxRunes = DefaultBlackboardConfig().SummaryMaxRunes
		}
		// 与 index_max_notes / ttl 不同，这个键没有"关掉"的含义：0 和负数都是脏值 → 默认 200。
		// 想放宽就写个大数字，不必为它保留一个 presence 标记。
		if c.MaxNotesPerBoard <= 0 {
			c.MaxNotesPerBoard = DefaultBlackboardConfig().MaxNotesPerBoard
		}
		if c.TTL < 0 {
			c.TTL = 0
		}
	}

	blackboardMu.Lock()
	blackboardConfig = c
	blackboardMu.Unlock()

	// 三个阀门可能同时关着，所以不用互斥的 switch 逐个播报：一条日志带上全部取值，
	// 关掉的那几个各加一个显式标记。排障时在日志里看到 "index_injection=off" 就够了。
	// max_notes_per_board 不在其中：归一之后它恒为正数，没有"关"这个状态。
	attrs := []any{
		"index_max_notes", c.IndexMaxNotes,
		"index_line_chars", c.IndexLineChars,
		"summary_max_runes", c.SummaryMaxRunes,
		"ttl", c.TTL.String(),
		"max_notes_per_board", c.MaxNotesPerBoard,
	}
	if c.IndexMaxNotes == 0 {
		attrs = append(attrs, "index_injection", "off")
	}
	if c.IndexLineChars == 0 {
		attrs = append(attrs, "line_cap", "unbounded")
	}
	if c.TTL == 0 {
		attrs = append(attrs, "notes_ttl", "off")
	}
	slog.Info("blackboard: 索引与摘要预算", attrs...)
}

// GlobalBlackboard 返回当前生效的 [blackboard] 配置。
func GlobalBlackboard() BlackboardConfig {
	blackboardMu.RLock()
	defer blackboardMu.RUnlock()
	return blackboardConfig
}
