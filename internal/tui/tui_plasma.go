package tui

import (
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"yagent/internal/config"
	"yagent/internal/plasma"
	"yagent/internal/tui/components"
)

// 本文件是 `/plasma [sub]` 命令：列 store、看某份 Agent 的诊断、升档。
//
// 三条纪律（与 internal/tui/AGENTS.md 的斜杠命令条目同源）：
//  1. 命令必须在 `processCommand` 的 `/` 分支里**显式**列出（那条分支默认是搜索）；
//  2. 输出只走既有通道 —— 多行走信息面板（`components.TextPanel`），单行走
//     `appendStatusLine`，参数错误走 `setInfoMsg`。不许直写 stdout（会撕渲染），
//     不许新开 goroutine（TUI 的监听器是一次性的，见硬约束 1）；
//  3. 所有排版与判据复用 `internal/plasma` 的导出函数（`FormatEntries` /
//     `FormatOutcomes` / `FormatPromote` / `PromoteBlockedReason` / `Report.Text`）——
//     CLI 是同一批账目的另一个读者，两份实现必漂。
//
// 读盘全部是同步的：store 目录只有几百 KB，且这里**不能**起 goroutine。

// plasmaPanelID 是 /plasma 面板的 id（对话框守卫与测试按它认领）。
const plasmaPanelID = "plasma_dialog"

// plasmaHelpLines 是 `/plasma` 与 `/plasma help` 的输出。它必须覆盖全部子命令，
// 且与下面的 switch 一一对应 —— 写了却不存在（或反过来）是同一类静默。
func plasmaHelpLines() []string {
	return []string{
		"/plasma                     这个帮助",
		"/plasma list                列出已落盘的 plasma agent（name/hash/grade/score/runs）",
		"/plasma explain <agent>     该 agent 的 report.json 诊断 + 失败夹具说明",
		"/plasma promote <agent> [hash]  升档并把 index 指针指过去（样本不足会拒绝）",
		"",
		"说明：promote 的判据是 plasma.CanPromote（grade=verified 且 runs ≥ [plasma] promote_min_runs）；",
		"被拒时不会动 index —— 一次失败的 promote 静默切换当前版本，比拒绝更危险。",
	}
}

// plasmaCommand 处理 `/plasma [sub] [args...]`。
func (m *model) plasmaCommand(args []string) tea.Cmd {
	if len(args) == 0 {
		return m.showPlasmaPanel(plasmaHelpLines())
	}
	switch args[0] {
	case "help", "-h", "--help":
		return m.showPlasmaPanel(plasmaHelpLines())

	case "list", "ls":
		lines, err := plasmaListLines(plasmaStore())
		if err != nil {
			m.setInfoMsg(err.Error())
			return nil
		}
		return m.showPlasmaPanel(lines)

	case "explain":
		if len(args) < 2 {
			m.setInfoMsg("用法：/plasma explain <agent>")
			return nil
		}
		lines, err := plasmaExplainLines(plasmaStore(), args[1])
		if err != nil {
			m.setInfoMsg(err.Error())
			return nil
		}
		return m.showPlasmaPanel(lines)

	case "promote":
		if len(args) < 2 {
			m.setInfoMsg("用法：/plasma promote <agent> [hash]")
			return nil
		}
		hash := ""
		if len(args) >= 3 {
			hash = args[2]
		}
		return m.promotePlasmaAgent(args[1], hash)

	default:
		m.setInfoMsg(fmt.Sprintf("未知子命令 %q（/plasma help）", args[0]))
		return nil
	}
}

// plasmaStore 取当前生效的 store 根。
// 路径来源只有 `[plasma] store`（`config.PlasmaStoreDir` 负责展开 `~`）——
// 在这里另拼一份 `~/.yagent/agents` 的后果是配了自定义目录的人看不到自己的 Agent。
func plasmaStore() *plasma.Store { return plasma.NewStore(config.PlasmaStoreDir()) }

// showPlasmaPanel 把若干行文本放进信息面板。
// 入栈前必须 SetBounds：弹窗守卫会吞 WindowSizeMsg，漏设则 View() 因尺寸过小而返回空串。
func (m *model) showPlasmaPanel(lines []string) tea.Cmd {
	return m.showTextPanel(plasmaPanelID, "◆ plasma", lines)
}

// showTextPanel 是两个命令共用的面板入栈（几何与按键全在 components.TextPanel）。
func (m *model) showTextPanel(id, title string, lines []string) tea.Cmd {
	d := components.NewTextPanel(id, title, lines, "")
	d.SetBounds(m.termWidth, m.termHeight)
	m.dialogStack.Push(d)
	return nil
}

// plasmaListLines 列 store。空 store 不是错误：`plasma.FormatEntries` 会打印
// "尚无 plasma agent" 那一行并正常返回（CLI 的 `plasma list` 读的是同一份实现）。
func plasmaListLines(store *plasma.Store) ([]string, error) {
	var b strings.Builder
	if err := plasma.FormatEntries(&b, store.List()); err != nil {
		return nil, err
	}
	return splitPanelLines(b.String()), nil
}

// plasmaExplainLines 组装 `/plasma explain <agent>`：档位/运行数 + report.json 的诊断
// 与失败夹具。三段都**如实**写：没落过 report.json 是"没有快照"，不是"没有诊断"。
func plasmaExplainLines(store *plasma.Store, name string) ([]string, error) {
	hash, ok := store.Current(name)
	if !ok {
		return nil, fmt.Errorf("store 里没有 %q（/plasma list 看已落盘的 agent）", name)
	}
	_, meta, err := store.Load(name, hash)
	if err != nil {
		return nil, fmt.Errorf("载入 %s@%s 失败：%w", name, plasma.ShortHash(hash), err)
	}
	minRuns := config.GlobalPlasma().PromoteMinRuns

	lines := []string{
		fmt.Sprintf("agent:   %s@%s", name, plasma.ShortHash(hash)),
		fmt.Sprintf("grade:   %s    runs: %d    score: %.2f", meta.Grade, meta.Runs, meta.Score),
	}
	if reason := plasma.PromoteBlockedReason(meta.Grade, meta.Runs, minRuns); reason != "" {
		lines = append(lines, "promote: 不可 —— "+reason)
	} else {
		lines = append(lines, "promote: 可升档（/plasma promote "+name+"）")
	}

	rec, err := store.ReadReport(name, hash)
	switch {
	case errors.Is(err, plasma.ErrNoReport):
		// 与"读了但没诊断"分开写：前者说明这份 hash 没经历过带报告的落盘（例如
		// 手工 Put 的样本），后者是编译真的没发射诊断。混成一句会让人去查编译。
		lines = append(lines, "", "report.json: 无（这份 hash 没落过诊断快照）")
	case err != nil:
		lines = append(lines, "", "report.json: 读不出来 —— "+err.Error())
	default:
		escalation := "否"
		if rec.EscalationRequired {
			escalation = "需要（越权能力，升级未批准时自动委派不放行）"
		}
		lines = append(lines, "", "报告 grade: "+string(rec.Grade)+"    escalation_required: "+escalation)
		if text := rec.Text(); text != "" {
			lines = append(lines, "诊断：")
			lines = append(lines, splitPanelLines(text)...)
		} else {
			lines = append(lines, "诊断：无（上次编译没有发射任何诊断）")
		}
		if len(rec.Outcomes) == 0 {
			lines = append(lines, "", "夹具：report.json 里没有逐条观测（5a 未跑）")
		} else {
			fails := plasma.Outcomes(rec.Outcomes.Failed())
			lines = append(lines, "", fmt.Sprintf("夹具：%d 条，通过 %d 条，失败 %d 条",
				len(rec.Outcomes), len(rec.Outcomes)-len(fails), len(fails)))
			if len(fails) > 0 {
				lines = append(lines, "失败夹具：")
				var b strings.Builder
				if err := plasma.FormatOutcomes(&b, fails); err != nil {
					lines = append(lines, "  （渲染失败："+err.Error()+"）")
				} else {
					lines = append(lines, splitPanelLines(b.String())...)
				}
			}
		}
	}
	return lines, nil
}

// promotePlasmaAgent 走与 CLI 完全相同的三段语义：已 promoted 只改指针（回滚路径）、
// 判据不过就**不碰盘**、通过才 SetGrade + Promote。
//
// 结果用一行状态行回显（不是面板）：promote 的产出就是一句话，而"被拒"与"成功"
// 都必须留下可检索的账目 —— 只 setInfoMsg 的话下一次按键就把它冲掉了。
func (m *model) promotePlasmaAgent(name, hash string) tea.Cmd {
	store := plasmaStore()
	if hash == "" {
		current, ok := store.Current(name)
		if !ok {
			m.setInfoMsg(fmt.Sprintf("store 里没有 %q（/plasma list 看已落盘的 agent）", name))
			return nil
		}
		hash = current
	}
	_, meta, err := store.Load(name, hash)
	if err != nil {
		m.setInfoMsg(fmt.Sprintf("载入 %s@%s 失败：%v", name, plasma.ShortHash(hash), err))
		return nil
	}
	res := &plasma.PromoteResult{
		Name: name, Hash: hash, Grade: string(meta.Grade),
		Runs: meta.Runs, MinRuns: config.GlobalPlasma().PromoteMinRuns,
	}
	switch {
	case meta.Grade == plasma.GradePromoted:
		// 已经是 promoted：不再重复升档，只把指针指过去（回滚一个旧版本的路径）。
		res.Promoted = true
	case plasma.CanPromote(meta.Grade, meta.Runs, res.MinRuns):
		if err := store.SetGrade(name, hash, plasma.GradePromoted); err != nil {
			m.setInfoMsg("升档失败：" + err.Error())
			return nil
		}
		res.Grade = string(plasma.GradePromoted)
		res.Promoted = true
	}
	if !res.Promoted {
		// 判据不过：**绝不**动 index。一次静默的指针切换比拒绝更危险 ——
		// 它让"下一次委派用的版本"在没有任何人说好的情况下变了。
		m.appendStatusLine(renderPlasmaPromoteLine(res))
		return nil
	}
	if err := store.Promote(name, hash); err != nil {
		m.setInfoMsg("改 index 失败：" + err.Error())
		return nil
	}
	m.appendStatusLine(renderPlasmaPromoteLine(res))
	return nil
}

// renderPlasmaPromoteLine 把 PromoteResult 渲染成一行（复用 plasma.FormatPromote）。
func renderPlasmaPromoteLine(res *plasma.PromoteResult) string {
	var b strings.Builder
	if err := plasma.FormatPromote(&b, res); err != nil {
		return "promote 结果渲染失败：" + err.Error()
	}
	return strings.TrimRight(b.String(), "\n")
}

// splitPanelLines 按行切块并去掉尾部空行：面板里多一个空行就少一行可见内容。
func splitPanelLines(text string) []string {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}
