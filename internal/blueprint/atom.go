package blueprint

import (
	"fmt"
	"sort"
	"strings"
)

// 原子的落盘形态：**一行一条**，可见正文 + 行尾 HTML 注释里的元数据。
//
//   - (a7c3) Director is the only user-facing agent <!--id:a7c3 where:internal/agents/executor.go:136 src:repo_agent@1f2e3c4d seen:2026-10-17T07:10:00Z state:ok-->
//
// 为什么必须是一行：折叠是行级手术，git diff 只动那一行，段内容许长到几十条而合并仍然可靠。
// 为什么短 ID 要**可见**：读到这份文档的 Agent 得能在下一次提交里用 `supersedes` 指认它；
// 人类读时它是噪声，这笔交换买的是"矛盾不并存"。
const (
	atomBulletPrefix = "- ("
	atomMetaOpen     = "<!--"
	atomMetaClose    = "-->"

	// 元数据里保留的键。除 id 外全部可选。
	metaID    = "id"
	metaField = "field"
	metaWhere = "where"
	metaSrc   = "src"
	metaSeen  = "seen"
	metaState = "state"
)

// 原子的三种状态。stale/unanchored **不删除**条目：
// 一条过期论断仍然是"该去看一眼这里"的信号，而删掉会让下一次折叠无法知道它曾经存在
// （`supersedes` 的 ID 也会断链）。
const (
	StateOK         = "ok"
	StateStale      = "stale"
	StateUnanchored = "unanchored"
)

// Atom 是一行原子。Extra 收下所有不认识的 `k:v`，渲染时原样带回 ——
// 文档格式将来加键时，旧版本读它不该把数据丢掉。
type Atom struct {
	ID    string
	Text  string
	Field string
	Where string
	Src   string
	Seen  string
	State string
	Extra map[string]string
}

// ParseAtomLine 解析一条原子行。
// 返回 (atom, true, nil) 是合法原子；(Atom{}, false, nil) 是正文里的其它行（标题、空行、
// 普通列表项）；(_, _, error) 只留给"看起来像原子但坏了"的情况 —— 那是文档被改坏的信号，
// 必须响，不能被当成普通文本继续留着。
func ParseAtomLine(line string) (Atom, bool, error) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, atomBulletPrefix) {
		return Atom{}, false, nil
	}
	// 短 ID 在 "- (" 之后、第一个 ")" 之前 —— 偏移量按前缀长度算，别按字面量 2 硬写。
	rest := trimmed[len(atomBulletPrefix):]
	closeParen := strings.Index(rest, ")")
	if closeParen < 0 {
		return Atom{}, false, nil // 不是我们写的列表项
	}
	body := strings.TrimSpace(rest[closeParen+1:])

	metaStart := strings.Index(body, atomMetaOpen)
	if metaStart < 0 {
		// 没有元数据注释 = 不是折叠写出来的行。留着当普通文本（用户手写的段落就该这样）。
		return Atom{}, false, nil
	}
	text := strings.TrimSpace(body[:metaStart])
	metaEnd := strings.Index(body[metaStart:], atomMetaClose)
	if metaEnd < 0 {
		return Atom{}, false, fmt.Errorf("blueprint: 元数据注释没有闭合: %q", line)
	}
	meta := parseMeta(strings.TrimSpace(body[metaStart+len(atomMetaOpen) : metaStart+metaEnd]))

	a := Atom{
		ID:    strings.TrimSpace(rest[:closeParen]),
		Text:  text,
		Field: meta[metaField],
		Where: meta[metaWhere],
		Src:   meta[metaSrc],
		Seen:  meta[metaSeen],
		State: meta[metaState],
	}
	wantID := meta[metaID]
	for _, k := range []string{metaID, metaField, metaWhere, metaSrc, metaSeen, metaState} {
		delete(meta, k)
	}
	if len(meta) > 0 {
		a.Extra = meta
	} else {
		a.Extra = map[string]string{}
	}

	if wantID == "" {
		return Atom{}, false, fmt.Errorf("blueprint: 原子缺少 id: %q", line)
	}
	if wantID != a.ID {
		return Atom{}, false, fmt.Errorf("blueprint: 短 ID 与元数据不符: (%s) vs id:%s", a.ID, wantID)
	}
	if a.ID == "" {
		return Atom{}, false, fmt.Errorf("blueprint: 原子没有可见短 ID: %q", line)
	}
	if a.Text == "" {
		return Atom{}, false, fmt.Errorf("blueprint: 原子正文为空: %q", line)
	}
	if a.State == "" {
		a.State = StateOK
	}
	return a, true, nil
}

// Line 渲染回落盘形态。键顺序固定（id/field/where/src/seen/state，未知键按字典序跟在后面）：
// 同一份内容必须逐字节还原成同一行，否则每次读写都会产出一整片 diff。
func (a Atom) Line() string {
	var b strings.Builder
	b.WriteString(atomBulletPrefix)
	b.WriteString(a.ID)
	b.WriteString(") ")
	b.WriteString(a.Text)

	pairs := make([]string, 0, len(a.Extra)+6)
	add := func(k, v string) {
		if v != "" {
			pairs = append(pairs, k+":"+v)
		}
	}
	add(metaID, a.ID)
	add(metaField, a.Field)
	add(metaWhere, a.Where)
	add(metaSrc, a.Src)
	add(metaSeen, a.Seen)
	add(metaState, a.State)
	if len(a.Extra) > 0 {
		keys := make([]string, 0, len(a.Extra))
		for k := range a.Extra {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			add(k, a.Extra[k])
		}
	}
	// 一条原子都没有元数据可写是不存在的（id 恒在），所以这里不必处理空 pairs。
	b.WriteString(atomMetaOpen)
	b.WriteString(strings.Join(pairs, " "))
	b.WriteString(atomMetaClose)
	return b.String()
}

// IsAnchored 报告这条原子有没有可复核的出处。
func (a Atom) IsAnchored() bool { return strings.TrimSpace(a.Where) != "" }

func parseMeta(s string) map[string]string {
	out := map[string]string{}
	if s == "" {
		return out
	}
	// 元数据的值一律不含空格（路径、短 ID、UTC 时间戳、状态词），所以按空格切分是安全的。
	// 真出现带空格的值时它会被当成一个无法识别的键丢掉 —— R6 的折叠会把这种行进拒收账目。
	for _, pair := range strings.Fields(s) {
		k, v, ok := strings.Cut(pair, ":")
		if !ok || k == "" {
			continue
		}
		out[k] = v
	}
	return out
}
