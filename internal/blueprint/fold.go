package blueprint

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// 本文件是设计稿 §7.5 的五道校验 + §8 的落盘纪律（归档、CAS、记账）。
// 纯合并算法在 merge.go —— 分两半不是为了好看，是因为 I20（幂等 / 顺序无关 /
// 重放安全）只能对**没有文件系统的**那个函数成立。

// 拒收原因词表。I18 的判据是"没更新"与"更新失败"必须在账目上可区分，
// 所以每个原因都是一条能被人读出来的账，而不是一个 error 字符串。
type RejectReason string

const (
	RejectSection      RejectReason = "unknown-section"       // section 不在枚举里
	RejectEmptyClaim   RejectReason = "empty-claim"           // claim 为空
	RejectModuleField  RejectReason = "missing-module-field"  // Modules 段缺 module 或 field
	RejectUnknownMod   RejectReason = "unknown-module"        // module 不在本次派生集合里（不许模型创建模块）
	RejectNoEvidence   RejectReason = "outside-evidence-set"  // module 属于本次 run 真打开过的文件之外
	RejectPathForeign  RejectReason = "path-not-in-module"    // 散文里的路径不属于这片（I14）
	RejectTooLong      RejectReason = "over-length"           // claim/where 超限（丢弃整字段，不截半句）
	RejectIDConflict   RejectReason = "id-conflict"           // 短 ID 撞上不同内容
	RejectNoReadAtAll  RejectReason = "no-evidence-in-run"    // 整轮一条证据都没有
	RejectUnknownField RejectReason = "unknown-field"         // field 不在三个模块字段里
	RejectRevision     RejectReason = "revision-write-failed" // 归档写不下去（不拦本次提交）
	RejectOverflow     RejectReason = "over-submission-limit" // 一次提交超过 maxItems，或 supersedes 超过上限
)

// Rejection 是一条拒收账目（带出处，方便 slog 与 rollout event_msg 共用一份）。
type Rejection struct {
	Reason RejectReason
	Detail string
	Module string
}

func (r Rejection) String() string {
	if r.Module == "" {
		return fmt.Sprintf("%s: %s", r.Reason, r.Detail)
	}
	return fmt.Sprintf("%s: %s (module %s)", r.Reason, r.Detail, r.Module)
}

// FoldItem 是一条提交上来的散文（schema 见 §7.2 的 agent_exit.blueprint[]）。
type FoldItem struct {
	Section    string
	Module     string
	Field      string
	Claim      string
	Where      string
	Supersedes []string
}

// FoldRequest 是一次折叠的输入。
//
// Git 三项由调用方传进来（gitinfo 会 fork 子进程，leaf 包不碰它）；
// Evidence 是"本次 run 真打开过的文件所属的模块集"（§7.5 第 2 条），
// 由 agents 侧用 Snapshot.EvidenceModules 算好 —— 校验用的还是派生集合本身。
type FoldRequest struct {
	Items    []FoldItem
	Src      string // {agent}@{session 前 8 位}
	Now      time.Time
	Evidence map[string]bool // 本次 run 真打开过的文件所属的模块集（§7.5 第 2 条）

	// ProjectID/ProjectPath 由装配层传：第一份文档落地时 front-matter 得有自己的身份，
	// 而 projectID 的唯一来源是 projectid.Compute(projectPath)（I12：与 rollout 同源）。
	ProjectID   string
	ProjectPath string

	GitSHA    string
	GitBranch string
	GitOrigin string
}

// FoldResult 是这次折叠的账目。调用方必须把它打出去或写进 rollout（I18）。
type FoldResult struct {
	Added      int
	Updated    int
	Deduped    int
	Supersede  int
	Stale      int // 写进去时锚点就判过期的条数（§7.5 第 5 条：不拒收，标 stale）
	Unanchored int // where 为空的条数
	Evicted    int // 因单段配额被挤出去的条数
	Rejected   []Rejection
	// DroppedSupersedes 是"指认了不存在的旧 ID"的次数：不报错也不假装成功（§7.4 第 3 条）。
	DroppedSupersedes int
	Modules           []string // 本次真的写到了的片（指纹要跟着更新）
	// NoOp 是"正文逐字没变"的判定：Commit 据此不写文件、不推进 Folds。
	// "没更新"与"更新失败"必须可区分（I18），而重放安全（I20）也靠它。
	NoOp bool
}

// Merge 是纯函数：给定旧文档 + 派生快照 + 请求，返回新文档与账目。
//
// 幂等（同批原子连 fold 两次字节不变）与顺序无关（乱序输入结果字节相同）
// 是 I20 的两条断言，也是多进程 CAS 敢重放的前提。
// 实现纪律：**不碰文件系统**（锚点复核除外，那是 Checker 的职责，由调用方注入）。
func Merge(doc *Doc, snap *Snapshot, req FoldRequest, anchors *AnchorChecker) (*Doc, FoldResult) {
	base := req.Now.UTC().Format(time.RFC3339)
	if doc == nil {
		doc = EmptyDoc("", "")
	}
	res := FoldResult{}
	if snap == nil {
		return doc, res
	}

	derived := snap.DerivedModuleSet()
	blocks, _ := doc.Sections()
	state := newMergeState(blocks, snap, anchors)

	for i, raw := range req.Items {
		if i >= MaxSubmitItems {
			// schema 写了 maxItems=8，就必须有人执行：多出来的那些既不落地也不报错的话，
			// 模型下一次只会交得更多（§11 坑 25：写了≠生效）。
			res.Rejected = append(res.Rejected, Rejection{
				Reason: RejectOverflow, Module: raw.Module,
				Detail: fmt.Sprintf("一次提交最多 %d 条，第 %d 条起被丢弃", MaxSubmitItems, MaxSubmitItems+1),
			})
			break
		}
		if len(raw.Supersedes) > MaxSupersedes {
			res.Rejected = append(res.Rejected, Rejection{
				Reason: RejectOverflow, Module: raw.Module,
				Detail: fmt.Sprintf("supersedes 最多 %d 个，本次给了 %d 个（多余的不生效）", MaxSupersedes, len(raw.Supersedes)),
			})
			raw.Supersedes = raw.Supersedes[:MaxSupersedes]
		}
		item, reason, detail := validate(raw, derived, req.Evidence, state)
		if reason != "" {
			res.Rejected = append(res.Rejected, Rejection{Reason: reason, Detail: detail, Module: raw.Module})
			continue
		}
		if anchors != nil && item.where != "" {
			verdict := anchors.Check(item.where)
			switch verdict.State {
			case StateStale:
				res.Stale++
			case StateUnanchored:
				res.Unanchored++
			}
			// 锚点判过期**不拒收**（§7.5 第 5 条）：写进去并立刻标 stale，
			// 比拒收更有信息量 —— 拒收会让"最后一次正确的说法"直接消失。
			if verdict.State != StateOK {
				item.state = verdict.State
			}
		} else if item.where == "" {
			item.state = StateUnanchored
			res.Unanchored++
		}
		state.apply(item, req.Src, base, &res)
	}

	out := *doc
	body := state.render(&res)
	if body == strings.TrimSpace(doc.Body) {
		// 正文逐字没变（同一批原子重放、或 dedupe 连 seen 都没动）→ 这就不是一次 fold。
		// Folds/Sources 记的是**写了几次**：让空重放也推进它们，"这一轮到底更新没有"
		// 在账目上就变得不可回答（I18），而 I20 的"连 fold 两次字节不变"也当场红。
		res.NoOp = true
		return doc, res
	}
	out.Body = body
	out.Front.Folds = doc.Front.Folds + 1
	out.Front.UpdatedAt = base
	out.Front.Sources = mergeSources(doc.Front.Sources, req.Src, res.Added+res.Updated)
	if out.Front.ProjectID == "" {
		out.Front.ProjectID = req.ProjectID
	}
	if out.Front.ProjectPath == "" {
		out.Front.ProjectPath = req.ProjectPath
	}
	// 配额淘汰是累计账：段里的原子被挤出去之后，正文里就没有它了，
	// 唯一还能说出"这里曾经放不下"的地方是 front-matter（§7.4 第 6 条）。
	if res.Evicted > 0 {
		out.Front.Evicted = doc.Front.Evicted + res.Evicted
	}
	if req.GitSHA != "" {
		out.Front.GitSHA, out.Front.GitBranch, out.Front.GitOrigin = req.GitSHA, req.GitBranch, req.GitOrigin
	}
	// 本次写到的片的指纹跟着刷新：那是"这段散文当时是关于哪个形状的"的唯一基线（§5.2(a)）。
	if len(res.Modules) > 0 {
		fp := map[string]string{}
		for k, v := range doc.Front.ModuleFingerprints {
			fp[k] = v
		}
		for _, m := range res.Modules {
			if leaf, ok := snap.moduleByDir(m); ok {
				fp[m] = leaf.Fingerprint
			}
		}
		out.Front.ModuleFingerprints = fp
	}
	sort.Strings(res.Modules)
	return &out, res
}

// mergeItem 是校验通过后的内部形态。
type mergeItem struct {
	section    Section
	module     string
	field      string
	claim      string
	where      string
	state      string
	supersedes []string
}

// validate 跑 §7.5 的前四道（第五道锚点在 Merge 里，因为那道不拒收）。
func validate(raw FoldItem, derived, evidence map[string]bool, st *mergeState) (mergeItem, RejectReason, string) {
	section, ok := NormaliseSection(raw.Section)
	if !ok {
		return mergeItem{}, RejectSection, fmt.Sprintf("section %q 不在枚举里", raw.Section)
	}
	claim := strings.TrimSpace(raw.Claim)
	if claim == "" {
		return mergeItem{}, RejectEmptyClaim, "claim 为空"
	}
	if len([]rune(claim)) > MaxClaimRunes {
		return mergeItem{}, RejectTooLong, fmt.Sprintf("claim %d rune 超过 %d（丢弃整字段，不截半句）", len([]rune(claim)), MaxClaimRunes)
	}
	where := strings.TrimSpace(raw.Where)
	if len([]rune(where)) > MaxWhereRunes {
		return mergeItem{}, RejectTooLong, fmt.Sprintf("where %q 超过 %d rune", where, MaxWhereRunes)
	}

	item := mergeItem{section: section, module: raw.Module, field: raw.Field, claim: claim, where: where, supersedes: raw.Supersedes}

	if section == SecModules {
		if item.module == "" || item.field == "" {
			return mergeItem{}, RejectModuleField, "Modules 段必须同时给 module 与 field"
		}
		if !IsModuleField(item.field) {
			return mergeItem{}, RejectUnknownField, fmt.Sprintf("field %q 不在 purpose/data_flow/add_here 里", item.field)
		}
	} else {
		// 仓库级段落没有 module/field：带着就丢掉，别让它长成第二个 key 空间。
		item.module, item.field = "", ""
	}

	if item.module != "" {
		if !derived[item.module] {
			// 边界是扫描器的决定 —— 不许模型创建模块（§7.5 第 1 条）。
			return mergeItem{}, RejectUnknownMod, fmt.Sprintf("module %q 不在本次派生的模块集合里", item.module)
		}
		if evidence != nil && !evidence[item.module] {
			return mergeItem{}, RejectNoEvidence, fmt.Sprintf("本次 run 没有打开过 %q 里的文件", item.module)
		}
		// I14：散文里的路径样 token 必须属于这片。os.Stat 只证明"存在"，
		// 成员判定才证明"属于这个模块" —— 后者才是真会被搞错的东西。
		// 判不了（同名多处、仓库里查无此物）不是违规：拒收会把正确的论断一起丢掉。
		if bad, yes := foreignPath(claim, item.module, st.snap, st.fs); yes {
			return mergeItem{}, RejectPathForeign, fmt.Sprintf("路径 %q 不属于 %s", bad, item.module)
		}
	} else if evidence != nil && len(evidence) == 0 {
		return mergeItem{}, RejectNoReadAtAll, "本次 run 一条文件证据都没有"
	}
	return item, "", ""
}

// MaxClaimRunes / MaxWhereRunes 是 §7.5 第 4 条的常数（超限丢弃整字段）。
const (
	MaxClaimRunes = 400
	MaxWhereRunes = 120
	// MaxAtomsPerSection 是 §7.4 第 6 条的单段配额。
	MaxAtomsPerSection = 40
	// MaxSubmitItems / MaxSupersedes 是 schema 的两个上界（§7.2）。
	// **schema 写了就必须有人执行**：否则模型以为一次能交 40 条，
	// 而多出来的那些既不报错也不落地（§11 坑 25 的同一条：写了≠生效）。
	MaxSubmitItems = 8
	MaxSupersedes  = 4
)

// LoadForFold 读文档原始字节 + 解析结果（fold 的 CAS 需要同一份字节做比较）。
func LoadForFold(root string) (*Doc, []byte, error) {
	raw, err := os.ReadFile(DocPath(root))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	doc, err := Parse(raw)
	if err != nil {
		return nil, raw, err
	}
	return doc, raw, nil
}
