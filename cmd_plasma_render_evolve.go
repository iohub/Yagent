package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"yagent/internal/plasma"
)

// 本文件是 `plasma evolve` 的跨语言账目类型与渲染。
//
// 账目形状（Python 侧 `evolve.py` 的 stdout）与 `policy.json` 是**契约**：
// 键名漂一次的表现是"候选一条都没有"或"按默认阈值跑"，两者都不报错。
// 所以字段名在这里集中一份，且 `TestRunPlasmaEvolveDryRun` 用真 Python 跑一遍
// 端到端确认两侧同形。

// evolveLedger 是 `plasma.pyz evolve` 的 stdout。
type evolveLedger struct {
	SchemaVersion       int               `json:"schema_version"`
	SamplesTotal        int               `json:"samples_total"`
	SamplesUsed         int               `json:"samples_used"`
	ExcludedFixtureRuns int               `json:"excluded_fixture_runs"`
	BadLines            int               `json:"bad_lines"`
	PromoteMinRuns      int               `json:"promote_min_runs"`
	Groups              []evolveGroup     `json:"groups"`
	Candidates          []evolveCandidate `json:"candidates"`
	Skipped             []evolveSkip      `json:"skipped"`
}

type evolveGroup struct {
	Agent           string             `json:"agent"`
	BundleHash      string             `json:"bundle_hash"`
	Samples         int                `json:"samples"`
	Fitness         float64            `json:"fitness"`
	PromoteEligible bool               `json:"promote_eligible"`
	Metrics         map[string]float64 `json:"metrics"`
}

type evolveCandidate struct {
	ID          string `json:"id"`
	Mutation    string `json:"mutation"`
	ParentAgent string `json:"parent_agent"`
	ParentHash  string `json:"parent_hash"`
	Path        string `json:"path"`
}

type evolveSkip struct {
	Agent      string `json:"agent"`
	BundleHash string `json:"bundle_hash"`
	Reason     string `json:"reason"`
}

// evolveOutcomes 是交给 Python judge 的输入（`judge --in`）。
type evolveOutcomes struct {
	SchemaVersion int                 `json:"schema_version"`
	Agent         string              `json:"agent"`
	BundleHash    string              `json:"bundle_hash,omitempty"`
	Fixtures      []plasmaOutcomeJSON `json:"fixtures"`
}

// plasmaJudgeVerdict 是 `plasma.pyz judge` 的输出。字段名与 judge.py 一一对应。
type plasmaJudgeVerdict struct {
	SchemaVersion int                    `json:"schema_version"`
	Score         int                    `json:"score"`
	Verdict       string                 `json:"verdict"`
	Reasons       []string               `json:"reasons"`
	Metrics       map[string]interface{} `json:"metrics,omitempty"`
}

// plasmaEvolveResult / plasmaEvolveCandidate 是本命令的观测（渲染在下面）。
type plasmaEvolveResult struct {
	OutDir         string
	Telemetry      string
	DryRun         bool
	SamplesUsed    int
	ExcludedFixRun int
	Groups         []evolveGroup
	Candidates     []plasmaEvolveCandidate
	Skipped        []string
	NextRunHint    string
}

// plasmaEvolveCandidate 是一个候选的**全部**判据账目。
// Rejected 为空表示它走完了全程（dry-run 时"会落盘"，正式跑时已落盘）。
type plasmaEvolveCandidate struct {
	ID              string
	Mutation        string
	Path            string
	Hash            string
	Grade           plasma.Grade
	Rejected        []string
	DiagnosticCodes []string
	Structural      plasma.Outcomes
	Behavioral      plasma.Outcomes
	Judge           *plasmaJudgeVerdict
	Note            string
}

func (c plasmaEvolveCandidate) accepted() bool { return len(c.Rejected) == 0 }

// parseEvolveLedger 解析 Python 的 stdout。schema_version 不认就拒：
// 半旧账目会让"候选"看起来是 0 条，而真正的原因是协议漂了。
func parseEvolveLedger(raw []byte) (*evolveLedger, error) {
	var ledger evolveLedger
	if err := json.Unmarshal(raw, &ledger); err != nil {
		return nil, fmt.Errorf("plasma evolve 的 stdout 不是合法 JSON: %w（内容尾部：%s）",
			err, plasma.TruncateTail(string(raw), 400))
	}
	if ledger.SchemaVersion != 1 {
		return nil, fmt.Errorf("plasma evolve 的 schema_version=%d，本命令只认 1（两侧协议漂了）",
			ledger.SchemaVersion)
	}
	return &ledger, nil
}

// parseJudgeVerdict 解析 judge 的输出。**不**用 json.Unmarshal 的 error 判断"这次
// judge 失败了"：judge 判 fail 时退出码是 1，但 stdout 上仍然是一份完整判决。
func parseJudgeVerdict(raw []byte) (*plasmaJudgeVerdict, bool) {
	var verdict plasmaJudgeVerdict
	if err := json.Unmarshal(raw, &verdict); err != nil {
		return nil, false
	}
	if strings.TrimSpace(verdict.Verdict) == "" {
		return nil, false
	}
	return &verdict, true
}

// renderPlasmaEvolve 输出一次进化的账目。人读模式不许省略"被拒的原因"与
// "被跳过的原因"——进化最容易出的问题是"什么都没发生而且不说为什么"。
func renderPlasmaEvolve(w io.Writer, res *plasmaEvolveResult) error {
	if res == nil {
		return nil
	}
	fmt.Fprintf(w, "telemetry: %s\n", res.Telemetry)
	fmt.Fprintf(w, "out:       %s\n", res.OutDir)
	fmt.Fprintf(w, "samples:   %d 条生产样本（已排除夹具样本 %d 条）\n",
		res.SamplesUsed, res.ExcludedFixRun)
	if res.DryRun {
		fmt.Fprintln(w, "dry-run:   true（不落盘；正式跑会把判过的候选 Put 成 verified）")
	}
	if len(res.Groups) > 0 {
		fmt.Fprintln(w, "父本（按适应度降序）：")
		for _, g := range res.Groups {
			fmt.Fprintf(w, "  %s@%s  samples=%d  fitness=%.4f  promote_eligible=%v\n",
				g.Agent, plasma.ShortHash(g.BundleHash), g.Samples, g.Fitness, g.PromoteEligible)
		}
	}
	if len(res.Candidates) == 0 {
		fmt.Fprintln(w, "candidates: 0（没有可变的父本；见下方 skipped 的原因）")
	} else {
		fmt.Fprintln(w, "candidates:")
		for _, c := range res.Candidates {
			fmt.Fprintf(w, "  %s  %s  grade=%s\n", c.ID, c.Mutation, gradeLabel(c.Grade))
			fmt.Fprintf(w, "    structural: %s  behavioral: %s\n",
				outcomeSummary(c.Structural), outcomeSummary(c.Behavioral))
			if c.Judge != nil {
				fmt.Fprintf(w, "    judge: %s score=%d\n", c.Judge.Verdict, c.Judge.Score)
			}
			for _, r := range c.Rejected {
				fmt.Fprintf(w, "    rejected: %s\n", r)
			}
			if c.Hash != "" {
				fmt.Fprintf(w, "    hash: %s\n", c.Hash)
			}
			if c.Note != "" {
				fmt.Fprintf(w, "    note: %s\n", c.Note)
			}
		}
	}
	if len(res.Skipped) > 0 {
		fmt.Fprintln(w, "skipped:")
		for _, s := range res.Skipped {
			fmt.Fprintf(w, "  %s\n", s)
		}
	}
	if res.NextRunHint != "" {
		fmt.Fprintf(w, "next:      %s\n", res.NextRunHint)
	}
	return nil
}

// outcomeSummary 一句话概括一组夹具：几条过、几条没过、没过的点名。
func outcomeSummary(o plasma.Outcomes) string {
	if len(o) == 0 {
		return "未执行"
	}
	failed := o.Failed()
	if len(failed) == 0 {
		return fmt.Sprintf("%d/%d 通过", len(o), len(o))
	}
	names := make([]string, 0, len(failed))
	for _, item := range failed {
		names = append(names, item.FixtureID)
	}
	return fmt.Sprintf("%d/%d 通过（红：%s）", len(o)-len(failed), len(o), strings.Join(names, ","))
}
