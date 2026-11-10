package main

import (
	"io"

	"yagent/internal/plasma"
)

// 本文件是 store 管理（list / promote）的**机器可读**渲染。
//
// 人读那一半已经整体搬到 `internal/plasma/render.go`：TUI 的 `/plasma` 面板是同一批
// 账目的第二个读者，两份实现必然漂（例如"空 store 打印什么"这种一眼可见的差别）。
// 这里只留 JSON —— `--json` 是给脚本与 Python 的跨语言契约，字段名不与人读排版耦合。

// plasmaPromoteResult 是 promote 的结果；字段定义在 plasma 包（人读与 JSON 共用一份）。
type plasmaPromoteResult = plasma.PromoteResult

func renderPlasmaList(w io.Writer, entries []plasma.Entry, asJSON bool) error {
	if !asJSON {
		return plasma.FormatEntries(w, entries)
	}
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

func renderPlasmaPromote(w io.Writer, res *plasmaPromoteResult, asJSON bool) error {
	if res == nil {
		return nil
	}
	if asJSON {
		return writeJSONValue(w, res)
	}
	return plasma.FormatPromote(w, res)
}
