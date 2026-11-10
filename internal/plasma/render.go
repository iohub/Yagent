package plasma

import (
	"fmt"
	"io"
)

// 本文件是"给人看的 store 账目"的**唯一**渲染实现。
//
// 为什么在 plasma 而不是在某一个调用方里：同一批账目有两个读者 —— CLI 子命令
// （`yagent plasma list/promote/test`）与 TUI 的 `/plasma` 面板。两份实现必然漂移，
// 而漂移的症状是"同一条账目在终端里和在面板里说的不是一回事"（根 AGENTS.md §6.9）。
//
// 机器可读形态**不**在这里：`--json` 的字段是跨语言契约（Python 侧也读），
// 它随 CLI 自己的 struct 走，不随人读排版变动。
//
// 依赖只有标准库：`scripts/check_plasma_leaf.sh` 不许本包 import 任何调用方。

// ShortHash 把 64 位 hash 截成前 12 位：人读账目里够区分，又不至于占满一行。
// 全文一律进 telemetry / report，不靠这 12 位做任何判定。
func ShortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// FormatEntries 渲染 store 列表。空 store 打印一行"尚无 plasma agent"并正常返回：
// 一台还没生成过任何 Agent 的机器上，"还没有"是正确答案，不是错误。
func FormatEntries(w io.Writer, entries []Entry) error {
	if len(entries) == 0 {
		_, err := fmt.Fprintln(w, "尚无 plasma agent")
		return err
	}
	if _, err := fmt.Fprintf(w, "%-24s %-12s %-12s %6s %5s\n",
		"NAME", "HASH", "GRADE", "SCORE", "RUNS"); err != nil {
		return err
	}
	for _, e := range entries {
		if _, err := fmt.Fprintf(w, "%-24s %-12s %-12s %6.2f %5d\n",
			e.Name, ShortHash(e.Hash), e.Meta.Grade, e.Meta.Score, e.Meta.Runs); err != nil {
			return err
		}
	}
	return nil
}

// PromoteResult 是 promote 的结果。JSON 与人读共用同一份字段 —— 两个表面读的是
// 同一批数，不是另一套计算。
type PromoteResult struct {
	Name     string `json:"name"`
	Hash     string `json:"hash"`
	Grade    string `json:"grade"`
	Runs     int    `json:"runs"`
	MinRuns  int    `json:"min_runs"`
	Promoted bool   `json:"promoted"`
}

// PromoteBlockedReason 是"为什么升不了档"的一句话；可升档时返回空串。
//
// 判据只有 CanPromote 一份，这里只负责把被拒的那一半讲清楚：两半都不满足时**两半
// 都写出来** —— 只说一半会让人去改错的地方（改了 grade 还差 runs，或者反过来）。
// minRuns <= 0 单列一条：阈值没配与"跑了但不够"是两件事，混成一句会把配置漏填
// 读成"再跑几次就好"。
func PromoteBlockedReason(grade Grade, runs, minRuns int) string {
	if CanPromote(grade, runs, minRuns) {
		return ""
	}
	if minRuns <= 0 {
		return fmt.Sprintf("promote_min_runs=%d 未配置（判据 fail-closed：阈值缺失不等于 0 门槛）", minRuns)
	}
	runsShort := runs < minRuns
	gradeShort := grade != GradeVerified && grade != GradePromoted
	switch {
	case runsShort && gradeShort:
		return fmt.Sprintf("runs 不足（%d/%d）且 grade=%s 未达 verified", runs, minRuns, grade)
	case runsShort:
		return fmt.Sprintf("runs 不足（%d/%d）", runs, minRuns)
	default:
		return fmt.Sprintf("grade=%s 未达 verified（runs=%d/%d 已经够）", grade, runs, minRuns)
	}
}

// FormatPromote 渲染 promote 的结果。未升档时**必须**把原因写出来：一次静默的拒绝
// 与一次成功的升档在界面上长得一样，只是"下一次部署还是旧版本"。
func FormatPromote(w io.Writer, res *PromoteResult) error {
	if res == nil {
		return nil
	}
	if res.Promoted {
		_, err := fmt.Fprintf(w, "promoted %s@%s (grade=%s, runs=%d/%d)\n",
			res.Name, ShortHash(res.Hash), res.Grade, res.Runs, res.MinRuns)
		return err
	}
	_, err := fmt.Fprintf(w, "未升档 %s@%s：%s\n",
		res.Name, ShortHash(res.Hash), PromoteBlockedReason(Grade(res.Grade), res.Runs, res.MinRuns))
	return err
}

// FormatOutcomes 逐条渲染夹具观测，**不带分组标题**：structural / behavioral 的标题
// 由调用方写（两个读者的分组方式不同），而"一条夹具怎么念"只有这一份实现。
//
// 未通过的**必须**把 reason 写出来：超预算 / 超时 / 契约不符的修法完全不同，
// 一句 "FAIL" 会让调用方去改错的地方。
func FormatOutcomes(w io.Writer, in Outcomes) error {
	for _, o := range in {
		if _, err := fmt.Fprintf(w, "  %s  %s  steps=%d", o.FixtureID, passLabel(o.Passed), o.Steps); err != nil {
			return err
		}
		// tokens 只在有数时出现：结构夹具不烧 token，打一排 0/0 反而像"没跑"。
		if o.TokensIn+o.TokensOut > 0 {
			fmt.Fprintf(w, "  tokens=%d/%d", o.TokensIn, o.TokensOut)
		}
		if o.StopKind != "" {
			fmt.Fprintf(w, "  stop=%s", o.StopKind)
		}
		fmt.Fprintln(w)
		if !o.Passed {
			fmt.Fprintf(w, "    reason: %s\n", o.Reason)
		}
	}
	return nil
}

// passLabel 是 PASS/FAIL 的唯一拼法（两个读者的账目里逐字相同）。
func passLabel(ok bool) string {
	if ok {
		return "PASS"
	}
	return "FAIL"
}
