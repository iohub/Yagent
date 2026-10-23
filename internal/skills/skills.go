package skills

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"yagent/internal/toolspec"
)

// 技能注册表：磁盘约定 + 合并语义 + 逐条账目。
//
// 与改动前最大的区别是 Load 永不返回 nil 注册表：目录存在但读不出时记进
// Rejected 并继续 —— 今天那条"返回 (nil, err) + 调用点 .Count()"是启动即崩。

// Skill 是一个已加载的技能。
type Skill struct {
	Name        string
	Description string
	// Body 是去掉 front-matter 的正文。字段名从 Content 改名是刻意的：
	// 留着旧名会让人以为它还是"全文含 front-matter"。
	Body string
	// Path 是 SKILL.md（或扁平 .md）的绝对路径。
	Path string
	// Source 是 "user" / "project" / "extra:<dir>"。
	Source string
	// FrontMatter 是 "json" / "yaml-subset" / "none"，供 TUI 面板与排障用。
	FrontMatter string
	// AllowedTools 是咨询性的：回吐给模型看，不改工具面、不做强制。
	AllowedTools []string
	// Arguments 是技能声明的参数名（同样只用于回吐）。
	Arguments []string
	// Dir 是目录式技能所在的目录（扁平 .md 为 ""）。
	Dir string
}

// Override 记录一次"后者覆盖前者"。
type Override struct {
	Name   string
	Winner string
	Loser  string
	Reason string
}

// Rejected 记录一个读不出来的条目。逐条可读是硬要求：
// "我的技能怎么没生效"必须有一行答案（同 Surface.Skipped 的理由）。
type Rejected struct {
	Path   string
	Reason string
}

// LoadAccount 是一次加载的账。
type LoadAccount struct {
	Total      int
	BySource   map[string][]string
	Overridden []Override
	Rejected   []Rejected
}

// String 渲染一行账（启动日志与 /skills 面板共用这一份实现）。
func (a LoadAccount) String() string {
	sources := make([]string, 0, len(a.BySource))
	for source := range a.BySource {
		sources = append(sources, source)
	}
	sort.Strings(sources)
	parts := make([]string, 0, len(sources))
	for _, source := range sources {
		parts = append(parts, fmt.Sprintf("%s: %d", source, len(a.BySource[source])))
	}
	line := fmt.Sprintf("skills: %d loaded", a.Total)
	if len(parts) > 0 {
		line += " (" + strings.Join(parts, ", ") + ")"
	}
	if len(a.Overridden) > 0 {
		names := make([]string, 0, len(a.Overridden))
		for _, o := range a.Overridden {
			names = append(names, fmt.Sprintf("%s: %s ▸ %s", o.Name, o.Winner, o.Loser))
		}
		line += fmt.Sprintf(", %d overridden (%s)", len(a.Overridden), strings.Join(names, "; "))
	} else {
		line += ", 0 overridden"
	}
	line += fmt.Sprintf(", %d rejected", len(a.Rejected))
	for _, r := range a.Rejected {
		line += fmt.Sprintf("; rejected %s: %s", r.Path, r.Reason)
	}
	return line
}

// Options 是渲染预算。零值使用默认（120 字行宽 / 4000 body token）。
type Options struct {
	LineChars     int
	BodyMaxTokens int
}

const (
	defaultLineChars     = 120
	defaultBodyMaxTokens = 4000
)

// Registry 是合并后的技能集合（并发安全）。
type Registry struct {
	mu      sync.RWMutex
	skills  map[string]*Skill
	account LoadAccount
	opts    Options
}

// Load 按优先级顺序加载目录：后面的覆盖前面的（与今天"项目级整体替换用户级"相反）。
//
// 每个目录里两种形态都认：`<name>/SKILL.md`（目录式，胜）与 `<name>.md`（扁平，兼容）。
// 只扫一层。目录不存在 → 跳过且不记账；存在但读不出 → 记进 Rejected 并继续。
func Load(dirs []string) (*Registry, LoadAccount) {
	r := &Registry{skills: make(map[string]*Skill), opts: Options{
		LineChars:     defaultLineChars,
		BodyMaxTokens: defaultBodyMaxTokens,
	}}
	r.account = LoadAccount{BySource: make(map[string][]string)}

	for _, dir := range dirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		source := sourceOf(dir)
		r.loadDir(dir, source)
	}

	// Total 必须等于合并后的条数：前者是"用户以为有几个"，后者是"实际有几个"。
	r.account.Total = len(r.skills)
	return r, r.account
}

// sourceOf 给目录一个可读来源名。标准两级用固定名，其余用 "extra:<dir>"。
func sourceOf(dir string) string {
	clean := filepath.Clean(dir)
	home, err := os.UserHomeDir()
	if err == nil && clean == filepath.Join(home, ".yagent", "skills") {
		return "user"
	}
	if clean == filepath.Join(".yagent", "skills") || strings.HasSuffix(clean, string(filepath.Separator)+filepath.Join(".yagent", "skills")) {
		return "project"
	}
	return "extra:" + dir
}

// loadDir 扫一层目录。任何失败都进账，绝不返回错误。
func (r *Registry) loadDir(dir, source string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return // 目录不存在是正常情况（用户没建），不记账
		}
		r.account.Rejected = append(r.account.Rejected, Rejected{Path: dir, Reason: "read: " + err.Error()})
		return
	}

	// 目录式优先：同名时 `<name>/SKILL.md` 胜，扁平 `<name>.md` 记进覆盖账。
	dirSkills := map[string]string{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		path := filepath.Join(dir, name, "SKILL.md")
		if _, statErr := os.Stat(path); statErr != nil {
			continue // 子目录里没有 SKILL.md：那是资源目录，不递归
		}
		dirSkills[name] = path
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".md")
		if name == entry.Name() {
			continue // 不是 .md
		}
		path := filepath.Join(dir, entry.Name())
		if _, shadowed := dirSkills[name]; shadowed {
			r.account.Overridden = append(r.account.Overridden, Override{
				Name: name, Winner: filepath.Join(name, "SKILL.md"), Loser: entry.Name(), Reason: "directory layout is more complete",
			})
			continue
		}
		r.add(name, path, source, "")
	}

	names := make([]string, 0, len(dirSkills))
	for name := range dirSkills {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		r.add(name, dirSkills[name], source, filepath.Dir(dirSkills[name]))
	}
}

var skillNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// add 读取并登记一个技能。所有拒收都进账。
func (r *Registry) add(name, path, source, skillDir string) {
	if !skillNamePattern.MatchString(name) {
		r.account.Rejected = append(r.account.Rejected, Rejected{
			Path: path, Reason: "invalid name (want ^[a-z0-9][a-z0-9_-]{0,63}$)",
		})
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		r.account.Rejected = append(r.account.Rejected, Rejected{Path: path, Reason: "read: " + err.Error()})
		return
	}

	fields, body, kind, fmErr := parseFrontMatter(string(raw))
	if fmErr != nil {
		// 解析失败仍加载正文：一条坏 front-matter 不该让整个技能消失，
		// 但必须留下 warn —— 静默当成"没有 front-matter"会让 user 以为字段生效了。
		slog.Warn("skills: front-matter 解析失败，已回落到首行描述",
			"component", "skills", "path", path, "error", fmErr)
		kind = "none"
	}

	skill := &Skill{
		Name:         name,
		Description:  descriptionOf(fields, body),
		Body:         body,
		Path:         path,
		Source:       source,
		FrontMatter:  kind,
		AllowedTools: stringList(fields["allowed-tools"]),
		Arguments:    stringList(fields["arguments"]),
		Dir:          skillDir,
	}

	if prev, exists := r.skills[name]; exists {
		// 后者覆盖前者：合并语义下这是正常路径，但必须记账（谁盖了谁）。
		r.account.Overridden = append(r.account.Overridden, Override{
			Name: name, Winner: source, Loser: prev.Source, Reason: "later directory wins",
		})
		for i, listed := range r.account.BySource[prev.Source] {
			if listed == name {
				r.account.BySource[prev.Source] = append(r.account.BySource[prev.Source][:i], r.account.BySource[prev.Source][i+1:]...)
				break
			}
		}
	}
	r.skills[name] = skill
	r.account.BySource[source] = append(r.account.BySource[source], name)
}

// descriptionOf 是"front-matter 的 description > 正文首行去 #"。
func descriptionOf(fields map[string]interface{}, body string) string {
	if d, ok := fields["description"].(string); ok && strings.TrimSpace(d) != "" {
		return strings.TrimSpace(d)
	}
	first, _, _ := strings.Cut(body, "\n")
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(first), "#"))
}

// SetOptions 设置渲染预算（装配层按 [skills] 配置调用一次）。
func (r *Registry) SetOptions(opts Options) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if opts.LineChars > 0 {
		r.opts.LineChars = opts.LineChars
	}
	if opts.BodyMaxTokens > 0 {
		r.opts.BodyMaxTokens = opts.BodyMaxTokens
	}
}

// Get 按名字取技能。
func (r *Registry) Get(name string) (*Skill, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.skills[name]
	return s, ok
}

// List 返回全部技能名（排序后）。
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.skills))
	for name := range r.skills {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Skills 返回全部技能（按名字排序），供 /skills 面板。
func (r *Registry) Skills() []Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Skill, 0, len(r.skills))
	for _, name := range sortedNames(r.skills) {
		out = append(out, *r.skills[name])
	}
	return out
}

// Count 返回技能数。
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.skills)
}

// Body 实现 toolspec.SkillSource：按行分页取回正文。
func (r *Registry) Body(name string, offset, limit int) (toolspec.SkillBody, error) {
	r.mu.RLock()
	skill, ok := r.skills[name]
	opts := r.opts
	r.mu.RUnlock()
	if !ok {
		return toolspec.SkillBody{}, toolspec.ErrSkillNotFound
	}

	text, truncated, next := paginate(skill.Body, offset, limit, opts.BodyMaxTokens)
	return toolspec.SkillBody{
		Name:         skill.Name,
		Source:       skill.Source,
		Path:         skill.Path,
		Description:  skill.Description,
		AllowedTools: append([]string(nil), skill.AllowedTools...),
		Arguments:    append([]string(nil), skill.Arguments...),
		Text:         text,
		Truncated:    truncated,
		NextOffset:   next,
	}, nil
}

// paginate 按行分页。limit<=0 表示"预算内尽量多"：逐行累计到 bodyMaxTokens 为止。
// 返回的片段可以直接首尾相接还原全文（非末页结尾保留换行）。
func paginate(body string, offset, limit, bodyMaxTokens int) (string, bool, int) {
	lines := strings.Split(body, "\n")
	if offset < 0 {
		offset = 0
	}
	if offset >= len(lines) {
		return "", false, len(lines)
	}

	end := len(lines)
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	if limit <= 0 && bodyMaxTokens > 0 {
		used := 0
		end = offset
		for end < len(lines) {
			cost := len(lines[end])/4 + 1
			if used+cost > bodyMaxTokens && end > offset {
				break
			}
			used += cost
			end++
		}
	}

	text := strings.Join(lines[offset:end], "\n")
	if end < len(lines) {
		text += "\n"
	}
	return text, end < len(lines), end
}

func sortedNames(m map[string]*Skill) []string {
	out := make([]string, 0, len(m))
	for name := range m {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// stringList 把 front-matter 里的字符串或字符串数组归一成切片。
func stringList(v interface{}) []string {
	switch x := v.(type) {
	case string:
		parts := strings.Split(x, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if t := strings.TrimSpace(p); t != "" {
				out = append(out, t)
			}
		}
		return out
	case []interface{}:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	case []string:
		return append([]string(nil), x...)
	default:
		return nil
	}
}
