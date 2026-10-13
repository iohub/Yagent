package config

import (
	"testing"
	"time"
)

// loadBlackboardCfg 加载一份带指定 [blackboard] 段的最小配置，测试结束时恢复全局缺省。
//
// 返回 *Config 而不是 BlackboardConfig：既要看归一化后的 GlobalBlackboard()，
// 也要看 blackboardSectionDefined 这个内部判定——它正是"整表缺省"分支的依据。
// cfg.Blackboard 是 TOML 的原始解码结果（未归一），断言时别和生效值混用。
func loadBlackboardCfg(t *testing.T, section string) *Config {
	t.Helper()
	t.Cleanup(func() {
		SetGlobalBlackboard(DefaultBlackboardConfig(), BlackboardPresence{})
	})
	cfg, err := LoadFromFile(minimalConfig(t, section))
	if err != nil {
		t.Fatalf("LoadFromFile: %v", err)
	}
	return cfg
}

// 老配置没有这一段：零值会把索引和清理静默关掉，必须由整表缺省兜住。
func TestBlackboardDefaultsWhenSectionAbsent(t *testing.T) {
	cfg := loadBlackboardCfg(t, "")
	d := DefaultBlackboardConfig()

	if cfg.blackboardSectionDefined {
		t.Error("blackboardSectionDefined = true, want false for a config without the section")
	}
	if cfg.Blackboard != (BlackboardConfig{}) {
		t.Errorf("raw decode = %+v, want all zero (TOML filled nothing)", cfg.Blackboard)
	}

	active := GlobalBlackboard()
	if active != d {
		t.Errorf("effective config = %+v, want defaults %+v", active, d)
	}
	// 最危险的一条：没写这一段 ≠ index_max_notes = 0。
	if active.IndexMaxNotes != 12 {
		t.Errorf("IndexMaxNotes = %d, want 12 — 缺段落不能等于关掉索引", active.IndexMaxNotes)
	}
	if active.TTL != 168*time.Hour {
		t.Errorf("TTL = %v, want 168h", active.TTL)
	}
	if active.MaxNotesPerBoard != 200 {
		t.Errorf("MaxNotesPerBoard = %d, want 200", active.MaxNotesPerBoard)
	}
}

// 三个"0 有意义"的取值：显式写出来就必须尊重，不能被当成"没写"。
func TestBlackboardExplicitZeroValves(t *testing.T) {
	cfg := loadBlackboardCfg(t, `
[blackboard]
index_max_notes = 0
index_line_chars = 0
ttl = "0"
`)

	if !cfg.blackboardSectionDefined {
		t.Error("blackboardSectionDefined = false, want true (段里有可解析的键)")
	}
	active := GlobalBlackboard()
	if active.IndexMaxNotes != 0 {
		t.Errorf("IndexMaxNotes = %d, want the explicit 0（关掉索引注入）", active.IndexMaxNotes)
	}
	if active.IndexLineChars != 0 {
		t.Errorf("IndexLineChars = %d, want the explicit 0（不裁剪索引行）", active.IndexLineChars)
	}
	if active.TTL != 0 {
		t.Errorf("TTL = %v, want the explicit 0（不做清理）", active.TTL)
	}
	// 没有"关"语义的键不吃这个 0：显式写 0 仍归默认 200。
	if active.MaxNotesPerBoard != 200 {
		t.Errorf("MaxNotesPerBoard = %d, want 200（它没有 off 语义，0 是脏值）", active.MaxNotesPerBoard)
	}
	if active.SummaryMaxRunes != 1200 {
		t.Errorf("SummaryMaxRunes = %d, want 1200（没写就取默认）", active.SummaryMaxRunes)
	}
}

// index_line_chars 的 0 不是"没写"：显式 0 = 不裁剪索引行（调用方换算成 MaxRenderRunes）。
func TestBlackboardExplicitUnboundedLineChars(t *testing.T) {
	cfg := loadBlackboardCfg(t, `
[blackboard]
index_line_chars = 0
`)

	if !cfg.blackboardSectionDefined {
		t.Error("blackboardSectionDefined = false, want true")
	}
	active := GlobalBlackboard()
	if active.IndexLineChars != 0 {
		t.Errorf("IndexLineChars = %d, want the explicit 0（不裁剪）", active.IndexLineChars)
	}
	if active.IndexMaxNotes != 12 || active.SummaryMaxRunes != 1200 ||
		active.TTL != 168*time.Hour || active.MaxNotesPerBoard != 200 {
		t.Errorf("unset keys must still default: %+v", active)
	}
}

// 只写了一半的段：写过的尊重，没写的取默认。
func TestBlackboardPartialSection(t *testing.T) {
	cfg := loadBlackboardCfg(t, `
[blackboard]
index_line_chars = 80
`)

	if !cfg.blackboardSectionDefined {
		t.Error("blackboardSectionDefined = false, want true")
	}
	active := GlobalBlackboard()
	if active.IndexLineChars != 80 {
		t.Errorf("IndexLineChars = %d, want 80", active.IndexLineChars)
	}
	if active.IndexMaxNotes != 12 || active.SummaryMaxRunes != 1200 ||
		active.TTL != 168*time.Hour || active.MaxNotesPerBoard != 200 {
		t.Errorf("unset keys in a partially written section must still default: %+v", active)
	}
}

// 负数一律视为脏值（ttl 的"不清理"要显式写 "0"，条数不限同理）。
func TestBlackboardNegativeValuesFallBackToDefaults(t *testing.T) {
	cfg := loadBlackboardCfg(t, `
[blackboard]
index_max_notes = -1
index_line_chars = -5
summary_max_runes = -5
max_notes_per_board = -1
ttl = "-1h"
`)

	if cfg.Blackboard.IndexMaxNotes != -1 || cfg.Blackboard.TTL != -time.Hour {
		t.Errorf("raw decode should keep what the file said: %+v", cfg.Blackboard)
	}
	active := GlobalBlackboard()
	if active.IndexMaxNotes != 12 {
		t.Errorf("IndexMaxNotes = %d, want 12 (负数不等于关掉)", active.IndexMaxNotes)
	}
	if active.IndexLineChars != 160 || active.SummaryMaxRunes != 1200 {
		t.Errorf("budget keys = (%d,%d), want (160,1200)", active.IndexLineChars, active.SummaryMaxRunes)
	}
	if active.MaxNotesPerBoard != 200 {
		t.Errorf("MaxNotesPerBoard = %d, want 200 (0 和负数都是脏值，放宽请写个大数字)", active.MaxNotesPerBoard)
	}
	if active.TTL != 0 {
		t.Errorf("TTL = %v, want 0 (负的存活时长归一成不清理)", active.TTL)
	}
}

// 只剩不认识键的段落等同于没写过：不能因为有 [blackboard] 表头就当成用户显式配置。
// 判定落点就是 blackboardSectionDefined=false → 整表缺省分支 → index_max_notes 仍是 12。
func TestBlackboardStaleOnlySectionFallsBackToDefaults(t *testing.T) {
	cfg := loadBlackboardCfg(t, `
[blackboard]
notes_enabled = true
`)

	if cfg.blackboardSectionDefined {
		t.Error("blackboardSectionDefined = true, want false: 只剩失效键的段落等于没写过")
	}
	active := GlobalBlackboard()
	if active.IndexMaxNotes != 12 {
		t.Errorf("IndexMaxNotes = %d, want 12 (不能因为表头存在就留 0)", active.IndexMaxNotes)
	}
	if active.TTL != 168*time.Hour {
		t.Errorf("TTL = %v, want 168h", active.TTL)
	}
	if active.MaxNotesPerBoard != 200 {
		t.Errorf("MaxNotesPerBoard = %d, want 200", active.MaxNotesPerBoard)
	}
	if active.IndexLineChars != 160 || active.SummaryMaxRunes != 1200 {
		t.Errorf("budget keys = (%d,%d), want (160,1200)", active.IndexLineChars, active.SummaryMaxRunes)
	}
}

// 失效键与显式阀门混写：段仍算"被写过"（有一个能解析的键），显式的 0 必须被尊重。
func TestBlackboardStaleKeyAlongsideExplicitValve(t *testing.T) {
	cfg := loadBlackboardCfg(t, `
[blackboard]
notes_enabled = true
index_max_notes = 0
`)

	if !cfg.blackboardSectionDefined {
		t.Error("blackboardSectionDefined = false, want true (index_max_notes 是可解析的键)")
	}
	active := GlobalBlackboard()
	if active.IndexMaxNotes != 0 {
		t.Errorf("IndexMaxNotes = %d, want the explicit 0 next to a stale key", active.IndexMaxNotes)
	}
	if active.TTL != 168*time.Hour || active.MaxNotesPerBoard != 200 {
		t.Errorf("unset keys must still default: %+v", active)
	}
}

// ttl 有两种合法写法：`"0"`（时长字符串）与 `0`（裸整数，time.Duration 就是 int64 纳秒）。
// 两种都必须被认成"显式写了 ttl"，否则 presence 为假会被归一回 168h，用户的"不清理"意图消失。
func TestBlackboardTTLSpellings(t *testing.T) {
	for _, spelling := range []string{`ttl = "0"`, `ttl = 0`} {
		t.Run(spelling, func(t *testing.T) {
			loadBlackboardCfg(t, "\n[blackboard]\nindex_max_notes = 5\n"+spelling+"\n")

			active := GlobalBlackboard()
			if active.TTL != 0 {
				t.Errorf("ttl (%s) = %v, want 0 (不清理)", spelling, active.TTL)
			}
			// 同段里显式写的另一个键不能被串影响。
			if active.IndexMaxNotes != 5 {
				t.Errorf("IndexMaxNotes = %d, want 5", active.IndexMaxNotes)
			}
		})
	}
}

// 归一化规则本身的单元测试（不经过 TOML）。
func TestSetGlobalBlackboardNormalization(t *testing.T) {
	defer SetGlobalBlackboard(DefaultBlackboardConfig(), BlackboardPresence{})

	SetGlobalBlackboard(BlackboardConfig{
		IndexMaxNotes:    0,
		IndexLineChars:   0,
		SummaryMaxRunes:  0,
		TTL:              -time.Hour,
		MaxNotesPerBoard: 0,
	}, BlackboardPresence{
		Section:        true,
		IndexMaxNotes:  true,
		IndexLineChars: true,
		TTL:            true,
	})

	got := GlobalBlackboard()
	if got.IndexMaxNotes != 0 {
		t.Errorf("IndexMaxNotes = %d, want 0", got.IndexMaxNotes)
	}
	if got.IndexLineChars != 0 {
		t.Errorf("IndexLineChars = %d, want 0（不裁剪）", got.IndexLineChars)
	}
	if got.SummaryMaxRunes != 1200 {
		t.Errorf("SummaryMaxRunes = %d, want default 1200", got.SummaryMaxRunes)
	}
	if got.TTL != 0 {
		t.Errorf("TTL = %v, want 0", got.TTL)
	}
	if got.MaxNotesPerBoard != 200 {
		t.Errorf("MaxNotesPerBoard = %d, want 200（0 是脏值，不是不限）", got.MaxNotesPerBoard)
	}

	// 段不存在时一切照默认，忽略传入的脏值。
	SetGlobalBlackboard(BlackboardConfig{IndexMaxNotes: 0, TTL: -time.Second}, BlackboardPresence{})
	if got = GlobalBlackboard(); got != DefaultBlackboardConfig() {
		t.Errorf("absent section = %+v, want defaults", got)
	}

	// 段存在但没给 presence 标记：等于这些键没写，取默认而不是留 0。
	SetGlobalBlackboard(BlackboardConfig{IndexMaxNotes: 0, MaxNotesPerBoard: 0}, BlackboardPresence{Section: true})
	got = GlobalBlackboard()
	if got.IndexMaxNotes != 12 || got.MaxNotesPerBoard != 200 {
		t.Errorf("section without key presence = (%d,%d), want defaults (12,200)",
			got.IndexMaxNotes, got.MaxNotesPerBoard)
	}
}
