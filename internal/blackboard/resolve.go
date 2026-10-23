package blackboard

import (
	"fmt"
	"strings"
)

// ContextPack 的默认预算。可被 [blackboard] 配置覆盖（通过 ResolveOptions）。
const (
	BudgetTotalChars    = 6000
	BudgetNoteInline    = 2000
	BudgetAgentMaxNotes = 3
	BudgetMaxRefs       = 10
)

// PackEntryKind distinguishes entry types in a ContextPack.
type PackEntryKind string

const (
	EntryNote     PackEntryKind = "note"
	EntryArtifact PackEntryKind = "artifact"
)

// PackEntry is one resolved reference in a ContextPack.
type PackEntry struct {
	Kind      PackEntryKind
	Ref       string
	Summary   string
	Body      string // 内联内容：全文，或预算不足时的节选（不再为空）
	Size      int    // 原始正文/产物的字符数
	Truncated bool

	// ArtifactIDs 是便签关联的 artifact，让读到节选的子 Agent 能直接二跳取全文。
	ArtifactIDs []string
	// ArtifactSummary 只对 EntryArtifact 有意义：artifact 自带的摘要首行。
	ArtifactSummary string
}

// ContextPack holds resolved references ready for injection into a delegate task.
type ContextPack struct {
	Entries    []PackEntry
	Unresolved []string
}

// ResolveOptions 是 ResolveRefs 的预算参数。零值字段取上面的默认常量。
type ResolveOptions struct {
	TotalChars    int
	NoteInline    int
	AgentMaxNotes int
	MaxRefs       int
	// SummaryRunes 是节选（handoff 渲染或头尾摘录）的 rune 预算。
	SummaryRunes int
}

func (o ResolveOptions) withDefaults() ResolveOptions {
	if o.TotalChars <= 0 {
		o.TotalChars = BudgetTotalChars
	}
	if o.NoteInline <= 0 {
		o.NoteInline = BudgetNoteInline
	}
	if o.AgentMaxNotes <= 0 {
		o.AgentMaxNotes = BudgetAgentMaxNotes
	}
	if o.MaxRefs <= 0 {
		o.MaxRefs = BudgetMaxRefs
	}
	if o.SummaryRunes <= 0 {
		o.SummaryRunes = DefaultRenderRunes
	}
	return o
}

// Text renders the ContextPack as a text block for prepending to a delegate task.
// Returns "" if the pack is empty.
// Sub-agents have read_note and read_artifact tools auto-injected by the executor,
// so the text can reference them directly.
func (p ContextPack) Text() string {
	if len(p.Entries) == 0 && len(p.Unresolved) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Referenced Context (from prior agent work)\n")
	for _, e := range p.Entries {
		switch e.Kind {
		case EntryNote:
			fmt.Fprintf(&b, "[note] %s — %s\n", e.Ref, e.Summary)
			if e.Body != "" {
				b.WriteString(strings.TrimRight(e.Body, "\n"))
				b.WriteByte('\n')
			}
			if e.Truncated {
				fmt.Fprintf(&b, "(正文已节选 — call read_note(ref=\"%s\") for full text", e.Ref)
				if len(e.ArtifactIDs) > 0 {
					fmt.Fprintf(&b, "; artifacts: %s", strings.Join(e.ArtifactIDs, ", "))
				}
				b.WriteString(")\n")
			}
		case EntryArtifact:
			fmt.Fprintf(&b, "[artifact] %s (%d chars)", e.Ref, e.Size)
			if e.ArtifactSummary != "" {
				fmt.Fprintf(&b, " — %s", firstLine(e.ArtifactSummary))
			}
			fmt.Fprintf(&b, " — call read_artifact(id=\"%s\") for full text\n", e.Ref)
		}
	}
	for _, u := range p.Unresolved {
		fmt.Fprintf(&b, "[unresolved] %s — 没取到内容，请换 agent:<name> 或 note:<agent>/<stem> 重试\n", u)
	}
	return b.String()
}

// ArtifactInfoFn 让 blackboard 拿到 artifact 的摘要与体积，而不用 import artifact 包。
// ok=false 表示该 id 不存在。
type ArtifactInfoFn func(id string) (summary string, charCount int, ok bool)

// ResolveRefs parses context_refs, loads notes/artifacts, and assembles a ContextPack
// within budget constraints.
func ResolveRefs(boardID string, refs []string, artifactInfo ArtifactInfoFn, opts ResolveOptions) ContextPack {
	if len(refs) == 0 {
		return ContextPack{}
	}
	opts = opts.withDefaults()
	if len(refs) > opts.MaxRefs {
		refs = refs[:opts.MaxRefs]
	}

	seen := make(map[string]bool)
	var pack ContextPack
	totalChars := 0

	for _, raw := range refs {
		ref := strings.TrimSpace(raw)
		if ref == "" || seen[ref] {
			continue
		}
		seen[ref] = true

		kind, value := parseRef(ref)
		switch kind {
		case "agent":
			entries, missing := resolveAgentRef(boardID, value, &totalChars, opts)
			pack.Entries = append(pack.Entries, entries...)
			if missing {
				// 名字对不上时必须上报：静默返回空 pack 会让 Director 以为上下文已送达。
				pack.Unresolved = append(pack.Unresolved, ref)
			}
		case "note":
			note, err := ReadNote(boardID, value)
			if err != nil {
				pack.Unresolved = append(pack.Unresolved, ref)
				continue
			}
			pack.Entries = append(pack.Entries, noteEntry(note, &totalChars, opts))
		case "artifact":
			entry, ok := resolveArtifactRef(value, artifactInfo)
			if !ok {
				pack.Unresolved = append(pack.Unresolved, ref)
				continue
			}
			pack.Entries = append(pack.Entries, entry)
		default:
			pack.Unresolved = append(pack.Unresolved, ref)
		}
	}
	return pack
}

// parseRef splits "kind:value" into components.
func parseRef(ref string) (kind, value string) {
	idx := strings.Index(ref, ":")
	if idx < 0 {
		return "", ref
	}
	return ref[:idx], ref[idx+1:]
}

const agentNameSuffix = "_agent"

// agentNameCandidates 归一 Agent 名的两种写法。
// 便签目录名是 repo_agent/coding_agent（由 Director 的 noteAgent 决定），而 prompt 教模型写
// agent:repo —— 不认这个别名就等于每次委派都静默丢掉上下文。
// 名单本身不在这里硬编码：规则只是"加/去后缀"，唯一真源仍是 Director 的 delegateTargets()。
func agentNameCandidates(name string) []string {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	out := []string{name}
	other := name + agentNameSuffix
	if strings.HasSuffix(name, agentNameSuffix) {
		other = strings.TrimSuffix(name, agentNameSuffix)
	}
	if other != "" {
		out = append(out, other)
	}
	return out
}

// ListNotesByAgent 按别名归一列出某个 Agent 的便签（newest-first）。
// agent 为空时列出整块板。这是别名规则的唯一出口：resolveAgentRef 与 list_notes 都走它。
func ListNotesByAgent(boardID, agent string, maxEntries int) ([]NoteMeta, error) {
	if strings.TrimSpace(agent) == "" {
		return ListNotes(boardID, "", maxEntries)
	}
	var lastErr error
	for _, candidate := range agentNameCandidates(agent) {
		metas, err := ListNotes(boardID, candidate, maxEntries)
		if err != nil {
			lastErr = err
			continue
		}
		if len(metas) > 0 {
			return metas, nil
		}
	}
	return nil, lastErr
}

// resolveAgentRef 返回该 Agent 的最新若干便签条目。
// 第二个返回值为 true 表示"一个都没取到"，调用方据此上报 unresolved。
func resolveAgentRef(boardID, agent string, totalChars *int, opts ResolveOptions) ([]PackEntry, bool) {
	metas, err := ListNotesByAgent(boardID, agent, opts.AgentMaxNotes)
	if err != nil || len(metas) == 0 {
		return nil, true
	}
	var entries []PackEntry
	for _, m := range metas {
		note, err := ReadNote(boardID, m.NoteRef)
		if err != nil {
			continue
		}
		entries = append(entries, noteEntry(note, totalChars, opts))
	}
	if len(entries) == 0 {
		return nil, true
	}
	return entries, false
}

// noteEntry 组装一条便签条目：预算够就内联全文，不够就内联节选。
//
// 以前的做法是"要么全文要么只剩 200 字符摘要"，而摘要永远是被裁过的前缀（客套话），
// 于是子 Agent 实际拿到 0 信息。现在截断分支一定给出一段有预算保证的内容。
func noteEntry(note Note, totalChars *int, opts ResolveOptions) PackEntry {
	meta := note.toMeta()
	summary := meta.Conclusion
	if summary == "" {
		summary = meta.Summary
	}
	entry := PackEntry{
		Kind:        EntryNote,
		Ref:         meta.NoteRef,
		Summary:     summary,
		Size:        meta.BodySize,
		ArtifactIDs: meta.ArtifactIDs,
	}

	body := note.Body
	bodyLen := runeLen(body)
	remaining := opts.TotalChars - *totalChars

	if remaining > 0 && bodyLen <= opts.NoteInline && bodyLen <= remaining {
		entry.Body = body
	} else {
		entry.Truncated = true
		entry.Body = noteExcerpt(note, min(remaining, opts.NoteInline), opts.SummaryRunes)
	}

	*totalChars += runeLen(entry.Body) + runeLen(entry.Summary) + runeLen(entry.Ref) + 20
	return entry
}

// noteExcerpt 是截断分支的节选策略。
//
// 有合规 handoff 时用它——结论是字段，不受"前缀里全是客套话"影响。
// v1 存量与降级 handoff 没有可信结论，只能给头尾摘录：一头一尾之间，结论更可能在尾。
func noteExcerpt(note Note, budget, summaryRunes int) string {
	if budget <= 0 {
		return ""
	}
	if note.Handoff.Degraded == "" {
		return note.Handoff.Render(min(budget, summaryRunes))
	}
	return headTailExcerpt(note.Body, min(budget, summaryRunes))
}

// headTailExcerpt 保留首尾、省略中段，总长不超过 budget 个 rune。
func headTailExcerpt(s string, budget int) string {
	if budget <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= budget {
		return s
	}
	const marker = "\n…[中间省略]…\n"
	avail := budget - runeLen(marker)
	if avail <= 1 {
		return truncateRunes(s, budget)
	}
	head := avail * 6 / 10
	tail := avail - head
	return string(r[:head]) + marker + string(r[len(r)-tail:])
}

func resolveArtifactRef(id string, info ArtifactInfoFn) (PackEntry, bool) {
	entry := PackEntry{Kind: EntryArtifact, Ref: id}
	if info == nil {
		// 没有探测能力时不做存在性判断，保留旧行为：只给 id 与取回方式。
		return entry, true
	}
	summary, charCount, ok := info(id)
	if !ok {
		return PackEntry{}, false
	}
	entry.Size = charCount
	entry.ArtifactSummary = summary
	return entry, true
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return strings.TrimSpace(s[:idx])
	}
	return s
}
