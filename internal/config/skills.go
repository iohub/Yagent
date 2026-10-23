// 本文件维护 `[skills]`（用户技能目录与注入预算）的运行时配置。
//
// 与 blueprint.go 同构：技能注册表在 app.Init 里加载一次，但目录预算、
// 注入名单、正文预算由四个消费方共问（skills.Registry、executor、director、
// use_skill 执行器），所以走"启动初始化 + 全局唯一事实源"。
package config

import (
	"log/slog"
	"os"
	"strings"
	"sync"
)

// SkillsConfig 对应 `[skills]`。
type SkillsConfig struct {
	// Enable 是总开关：关掉就是不扫描目录、不注入目录、use_skill 解释"为什么没有"。
	Enable bool `toml:"enable"`

	// Dirs 是追加目录。默认的两级（用户级 + 项目级）已经在内，这里只加。
	Dirs []string `toml:"dirs"`

	// InjectInto 决定谁的 system prompt 里出现技能目录。
	// 与 [blueprint] inject_into 同语义：关的是**常驻注入**，不关 use_skill 这个工具本身。
	// 显式写空数组 = 一处都不注入（与"没写"不同，那是默认四处）。
	InjectInto []string `toml:"inject_into"`

	// CatalogMaxTokens 是目录块的 token 预算；超出按名字排序截断并把标记写进正文。
	CatalogMaxTokens int `toml:"catalog_max_tokens"`
	// CatalogLineChars 是目录单行描述的字数上限。
	CatalogLineChars int `toml:"catalog_line_chars"`
	// BodyMaxTokens 是 use_skill 单次回吐正文的预算；超出的部分靠 offset 分页取。
	BodyMaxTokens int `toml:"body_max_tokens"`
}

// DefaultSkillsConfig 是整段缺省时的取值。
func DefaultSkillsConfig() SkillsConfig {
	return SkillsConfig{
		Enable:           true,
		Dirs:             nil,
		InjectInto:       []string{"director", "coding", "repo", "chat"},
		CatalogMaxTokens: 600,
		CatalogLineChars: 120,
		BodyMaxTokens:    4000,
	}
}

// SkillsPresence 记录 [skills] 里哪些值被显式写出。
// enable 的缺省是"开"，Go 零值是 false ⇒ 必须看键的存在性（坑 26）。
type SkillsPresence struct {
	Section    bool
	Enable     bool
	InjectInto bool
}

const skillsKeyHint = "enable / dirs / inject_into / catalog_max_tokens / catalog_line_chars / body_max_tokens"

// skillsInjectSites 是 inject_into 认的名字。
// 只有这四个画像拿到了 EffectSkill（画像矩阵，设计稿 §4.5）——
// 写别的名字会被 warn 后丢掉，而不是静默当成"没写"。
var skillsInjectSites = map[string]bool{"director": true, "coding": true, "repo": true, "chat": true}

var (
	skillsMu     sync.RWMutex
	skillsConfig = DefaultSkillsConfig()
)

// SetGlobalSkills 写入全局配置并归一化缺省值。
func SetGlobalSkills(c SkillsConfig, p SkillsPresence) {
	defaults := DefaultSkillsConfig()

	if !p.Section {
		c = defaults
	} else {
		if !p.Enable {
			c.Enable = defaults.Enable
		}
		// nil = 没写这个键 → 默认四处；显式 [] = 用户就是要关掉全部注入。
		if c.InjectInto == nil && !p.InjectInto {
			c.InjectInto = defaults.InjectInto
		}
		c.InjectInto = normaliseSkillsInjectInto(c.InjectInto)
		if c.CatalogMaxTokens <= 0 {
			c.CatalogMaxTokens = defaults.CatalogMaxTokens
		}
		if c.CatalogLineChars <= 0 {
			c.CatalogLineChars = defaults.CatalogLineChars
		}
		if c.BodyMaxTokens <= 0 {
			c.BodyMaxTokens = defaults.BodyMaxTokens
		}
	}

	skillsMu.Lock()
	skillsConfig = c
	skillsMu.Unlock()

	attrs := []any{
		"enable", c.Enable,
		"dirs", len(c.Dirs),
		"inject_into", strings.Join(c.InjectInto, ","),
		"catalog_max_tokens", c.CatalogMaxTokens,
		"catalog_line_chars", c.CatalogLineChars,
		"body_max_tokens", c.BodyMaxTokens,
	}
	if !SkillsEnabled() {
		attrs = append(attrs, "kill_switch", "YAGENT_SKILLS=0")
	}
	slog.Info("skills: 技能配置", attrs...)
}

// 每个键的消费者：
//
//	enable             → app.Init 是否加载注册表（唯一判据 SkillsEnabled）
//	dirs               → skills.Load 的追加目录
//	inject_into        → agents 决定哪几个 Agent 的 system prompt 带目录
//	catalog_max_tokens → Registry.Catalog 的预算
//	catalog_line_chars → Registry.Catalog 的单行截断
//	body_max_tokens    → use_skill 执行器的单次回显预算

// normaliseSkillsInjectInto 去空白、转小写、去重，并丢掉不认识的名字（每次丢都 warn）。
func normaliseSkillsInjectInto(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, raw := range in {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" {
			continue
		}
		if !skillsInjectSites[name] {
			slog.Warn("skills: inject_into 里有不认识的名字，已忽略",
				"value", raw, "expected", "director / coding / repo / chat")
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

// GlobalSkills 返回当前生效的 [skills] 配置。
func GlobalSkills() SkillsConfig {
	skillsMu.RLock()
	defer skillsMu.RUnlock()
	return skillsConfig
}

// SkillsEnabled 是这套机制的**唯一**启用判据。
// 环境变量 YAGENT_SKILLS=0 是 kill-switch（只认精确的 "0"）。
func SkillsEnabled() bool {
	if os.Getenv("YAGENT_SKILLS") == "0" {
		return false
	}
	return GlobalSkills().Enable
}

// SkillsInjectedInto 报告某个 Agent 的 system prompt 是否该带技能目录。
// 判据只此一处：五个子 Agent 各写一遍 if 正是压缩开关历史上写过 5 个 AND 点的病。
func SkillsInjectedInto(agent string) bool {
	if !SkillsEnabled() {
		return false
	}
	for _, name := range GlobalSkills().InjectInto {
		if name == agent {
			return true
		}
	}
	return false
}
