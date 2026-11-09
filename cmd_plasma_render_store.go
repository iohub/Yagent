package main

import (
	"fmt"
	"io"

	"yagent/internal/plasma"
)

// 本文件是 store 管理（list / promote）的渲染。与 cmd_plasma_render.go 分开是为了让
// 两个文件都在 250 行以内；判据仍只有一份（JSON 是同一批字段的机器可读形态）。

func renderPlasmaList(w io.Writer, entries []plasma.Entry, asJSON bool) error {
	if asJSON {
		type entryJSON struct {
			Name    string  `json:"name"`
			Hash    string  `json:"hash"`
			Grade   string  `json:"grade"`
			Score   float64 `json:"score"`
			Runs    int     `json:"runs"`
			Current bool    `json:"current"`
		}
		out := make([]entryJSON, 0, len(entries))
		for _, e := range entries {
			out = append(out, entryJSON{
				Name: e.Name, Hash: e.Hash, Grade: string(e.Meta.Grade),
				Score: e.Meta.Score, Runs: e.Meta.Runs, Current: e.Current,
			})
		}
		if len(out) == 0 {
			out = []entryJSON{}
		}
		return writeJSONValue(w, out)
	}
	if len(entries) == 0 {
		fmt.Fprintln(w, "尚无 plasma agent")
		return nil
	}
	fmt.Fprintf(w, "%-24s %-12s %-12s %6s %5s\n", "NAME", "HASH", "GRADE", "SCORE", "RUNS")
	for _, e := range entries {
		hash := e.Hash
		if len(hash) > 12 {
			hash = hash[:12]
		}
		fmt.Fprintf(w, "%-24s %-12s %-12s %6.2f %5d\n", e.Name, hash, e.Meta.Grade, e.Meta.Score, e.Meta.Runs)
	}
	return nil
}

// plasmaPromoteResult 是 promote 的结果（JSON 与人读共用同一份字段）。
type plasmaPromoteResult struct {
	Name     string `json:"name"`
	Hash     string `json:"hash"`
	Grade    string `json:"grade"`
	Runs     int    `json:"runs"`
	MinRuns  int    `json:"min_runs"`
	Promoted bool   `json:"promoted"`
}

func renderPlasmaPromote(w io.Writer, res *plasmaPromoteResult, asJSON bool) error {
	if res == nil {
		return nil
	}
	if asJSON {
		return writeJSONValue(w, res)
	}
	if res.Promoted {
		fmt.Fprintf(w, "promoted %s@%s (grade=%s, runs=%d/%d)\n",
			res.Name, shortHash(res.Hash), res.Grade, res.Runs, res.MinRuns)
		return nil
	}
	fmt.Fprintf(w, "未升档 %s@%s：grade=%s runs=%d（需要 verified 且 runs ≥ %d）\n",
		res.Name, shortHash(res.Hash), res.Grade, res.Runs, res.MinRuns)
	return nil
}

func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}
