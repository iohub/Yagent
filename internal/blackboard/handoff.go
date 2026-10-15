package blackboard

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Handoff 是一次委派的结构化交付物。它是便签 front-matter 的 canonical 内容，
// 也是 ContextPack、read_note 与 Director 摘要的共同渲染源。
//
// 存在的理由：散文交付物的结论埋在正文尾部，而压缩与摘要一律保留前缀
// （artifact.Reduce 与 truncateRunes 都是）。结论必须是字段，不是第 4700 个字符。
type Handoff struct {
	Conclusion    string   `json:"conclusion"`
	Facts         []Fact   `json:"facts,omitempty"`
	Artifacts     []string `json:"artifact_ids,omitempty"`
	OpenQuestions []string `json:"open_questions,omitempty"`
	Status        string   `json:"status,omitempty"`
	Degraded      string   `json:"degraded,omitempty"`
}

// Fact 是一条可验证的论断。Where 存在的意义是让编造变得显眼——空 facts 优于假 facts。
type Fact struct {
	Claim string `json:"claim"`
	Where string `json:"where,omitempty"`
}

// 长度与条数上限。所有裁剪都按 rune 计（中文一个字符就是一个 rune）。
const (
	MaxConclusionRunes   = 200
	MaxFacts             = 12
	MaxFactClaimRunes    = 160
	MaxFactWhereRunes    = 120
	MaxOpenQuestions     = 5
	MaxArtifacts         = 8
	DefaultRenderRunes   = 1200
	IndexConclusionRunes = 80

	// MaxRenderRunes 是"不裁剪"的哨兵值。配置的 index_line_chars = 0 表达的就是这个意图，
	// 而本包把 <=0 当作"取默认"，所以换算必须在调用方完成，不能把 0 直接传进来。
	MaxRenderRunes = 1 << 20

	renderFooterReserve = 48
)

// 降级标记。Degraded 非空的 handoff 不视为结构化交接成功。
const (
	DegradedMissingFields = "missing_fields"
	DegradedAbsent        = "absent"
	DegradedV1            = "v1"
)

// 交付状态。LLM 可自报 partial；error 由系统按委派错误判定，不接受 LLM 自报
// （子 Agent 说自己 ok 但实际抛错的情况今天就已经存在）。
//
// degraded 同样是**系统专用**，刻意不进 agent_exit 的 handoff.status 枚举：
// 让模型自报"我这份交付是降级的"等于把降级判交给最没资格判断的一方 —— 降级与否
// 是重放侧按 §6.4 那八条判据算出来的事实，模型既看不到也没法复现。
//
// 词表里**没有 paused**：被闸门 abandon 的委派没有任何交付物，写上去只会是一张
// 空正文的便签（它的 conclusion 为空、还会占住每轮重扫的索引）。"停在第几步"这件事
// 由 rollout 的 stop_reason 与 agent_checkpoint 记账，恢复成功后再落一张 ok/degraded。
const (
	StatusOK       = "ok"
	StatusError    = "error"
	StatusDegraded = "degraded"
)

// agent_exit 的 arguments 形态：只关心 handoff 键是否存在、形状是否对。
// reason 由调用方单独传入（它同时是退出 JSON 的字段与工具必填项）。
type handoffArgs struct {
	Handoff json.RawMessage `json:"handoff"`
}

// ParseHandoff 从 agent_exit 的原始参数里提取 Handoff，三档回落。
//
// 永不返回 error，也永不返回 nil：三档全部落空时给出 Degraded=="absent" 的最小 Handoff，
// 最坏情况等于本次重构之前的行为。绝不能因为 LLM 没按格式输出就阻断委派。
func ParseHandoff(argsJSON []byte, reason, fallbackText string) Handoff {
	reason = strings.TrimSpace(reason)

	// 档 1/2：arguments 里有可解析的 handoff 对象。
	if h, ok := parseHandoffObject(argsJSON); ok {
		if strings.TrimSpace(h.Conclusion) == "" {
			h.Conclusion = reason
			h.Degraded = DegradedMissingFields
		} else {
			h.Degraded = ""
		}
		// 状态未报告但 LLM 已明确退出 ⇒ 视作 ok。
		// 理由：read_note 的 header 与索引行都显示状态，留空会被读成"未知/失败"。
		if strings.TrimSpace(h.Status) == "" && reason != "" {
			h.Status = StatusOK
		}
		return h.Normalize()
	}

	// 档 3：完全没有（无 handoff 键 / JSON 非法 / 类型不符）。
	conclusion := reason
	if conclusion == "" {
		conclusion = tailRunes(strings.TrimSpace(fallbackText), MaxConclusionRunes)
	}
	return Handoff{
		Conclusion: conclusion,
		Degraded:   DegradedAbsent,
	}.Normalize()
}

// parseHandoffObject 尝试把 arguments 里的 handoff 键解析成结构。
// 返回 false 表示"档 3"：没有该键、JSON 非法、或不是对象。
func parseHandoffObject(argsJSON []byte) (Handoff, bool) {
	raw := strings.TrimSpace(string(argsJSON))
	if raw == "" {
		return Handoff{}, false
	}
	var args handoffArgs
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return Handoff{}, false
	}
	if len(args.Handoff) == 0 {
		return Handoff{}, false
	}
	var h Handoff
	if err := json.Unmarshal(args.Handoff, &h); err != nil {
		// LLM 偶尔把 handoff 写成字符串或数组。宁可整体降级，也不要半解析出假 fact。
		return Handoff{}, false
	}
	return h, true
}

// Normalize 裁剪长度与条数、去空、按内容去重。
// ParseHandoff 内部已调用；程序化构造 Handoff 时（v1 兼容路径、便签写入）需显式调用。
func (h Handoff) Normalize() Handoff {
	out := Handoff{
		Conclusion: truncateRunes(strings.TrimSpace(h.Conclusion), MaxConclusionRunes),
		Status:     strings.TrimSpace(h.Status),
		Degraded:   strings.TrimSpace(h.Degraded),
	}

	seenClaim := make(map[string]bool)
	for _, f := range h.Facts {
		claim := truncateRunes(strings.TrimSpace(f.Claim), MaxFactClaimRunes)
		if claim == "" || seenClaim[claim] {
			continue
		}
		seenClaim[claim] = true
		out.Facts = append(out.Facts, Fact{
			Claim: claim,
			Where: truncateRunes(strings.TrimSpace(f.Where), MaxFactWhereRunes),
		})
		if len(out.Facts) >= MaxFacts {
			break
		}
	}

	seenText := make(map[string]bool)
	for _, q := range h.OpenQuestions {
		q = truncateRunes(strings.TrimSpace(q), MaxFactClaimRunes)
		if q == "" || seenText[q] {
			continue
		}
		seenText[q] = true
		out.OpenQuestions = append(out.OpenQuestions, q)
		if len(out.OpenQuestions) >= MaxOpenQuestions {
			break
		}
	}

	seenID := make(map[string]bool)
	for _, id := range h.Artifacts {
		id = strings.TrimSpace(id)
		if id == "" || seenID[id] {
			continue
		}
		seenID[id] = true
		out.Artifacts = append(out.Artifacts, id)
		if len(out.Artifacts) >= MaxArtifacts {
			break
		}
	}

	return out
}

// Empty 报告这个 Handoff 是否什么内容都没有（只有一个降级标记）。
// 调用方据此决定"要不要渲染 handoff 块"，避免输出空壳。
func (h Handoff) Empty() bool {
	return h.Conclusion == "" && len(h.Facts) == 0 && len(h.OpenQuestions) == 0
}

// Render 渲染成确定性文本块。maxRunes <= 0 时用 DefaultRenderRunes。
//
// 预算不足时**逐条丢 Fact**并在末尾标注丢弃条数——绝不截断 Conclusion。
// 理由：结论被截一半就等同于本次重构之前的失败模式（前缀里只有客套话）。
func (h Handoff) Render(maxRunes int) string {
	if maxRunes <= 0 {
		maxRunes = DefaultRenderRunes
	}
	if h.Empty() {
		return ""
	}

	var b strings.Builder
	if h.Conclusion != "" {
		b.WriteString("结论: ")
		b.WriteString(h.Conclusion)
		b.WriteByte('\n')
	}

	// 尾部固定段先算出来，好让 facts 知道自己还剩多少预算。
	var tail strings.Builder
	if len(h.OpenQuestions) > 0 {
		tail.WriteString("待确认: ")
		tail.WriteString(strings.Join(h.OpenQuestions, " / "))
		tail.WriteByte('\n')
	}
	if h.Status != "" {
		tail.WriteString("状态: ")
		tail.WriteString(h.Status)
		tail.WriteByte('\n')
	}
	tailLen := runeLen(tail.String())

	used := runeLen(b.String()) + tailLen + renderFooterReserve
	if len(h.Facts) > 0 {
		written := 0
		if used+runeLen("事实:\n") <= maxRunes {
			b.WriteString("事实:\n")
			used += runeLen("事实:\n")
			for _, f := range h.Facts {
				line := "- " + f.Claim
				if f.Where != "" {
					line += "  @ " + f.Where
				}
				line += "\n"
				if used+runeLen(line) > maxRunes {
					break // 丢尾部连续若干条，与"余下 N 条"的措辞一致
				}
				b.WriteString(line)
				used += runeLen(line)
				written++
			}
		}
		// 省略标记不计入预算也在所不惜：丢了事实却不说明，等于让 LLM 以为交接完整。
		if omitted := len(h.Facts) - written; omitted > 0 {
			b.WriteString(renderOmittedFooter(omitted))
		}
	}

	b.WriteString(tail.String())
	return b.String()
}

func renderOmittedFooter(n int) string {
	return "…（余下 " + strconv.Itoa(n) + " 条事实因预算省略，见同一便签的正文或 read_note）\n"
}

func runeLen(s string) int {
	return len([]rune(s))
}

// tailRunes 取尾部至多 n 个 rune（结论在结尾时，前缀裁剪会把它整段丢掉）。
// 省略号计入预算：它不该吃掉真正要保留的尾部内容。
func tailRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	return "…" + string(r[len(r)-(n-1):])
}
