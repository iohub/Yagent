package tui

import (
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"yagent/internal/config"
	"yagent/internal/plasma"
)

// 本文件是 `/proposals [sub]` 命令：列待审批的内建 Agent prompt 提案、看 diff、判决。
//
// **最重要的不变量：判决只改提案 JSON 里的 `status` 字段，绝不改写任何 `*.prompt.md`。**
// 内建 Agent 的 prompt 与 Go 代码是"改行为要同时改两处"的耦合（根 AGENTS.md §4、§6.9），
// 自动 apply 会把两者改成不一致 —— 而这种不一致编译得过、测试也绿，只在行为上慢慢漂。
// 纪律的落点在 `plasma.SetProposalStatus`（唯一写入口，按 RawMessage 改写只动 status），
// 本文件连 `PromptPath` 都只当文本显示。
//
// 输出通道同 /plasma：多行走信息面板、单行走 appendStatusLine、错误走 setInfoMsg。

// proposalsPanelID 是 /proposals 面板的 id。
const proposalsPanelID = "proposals_dialog"

// proposalsHelpLines 与下面的 switch 一一对应。
func proposalsHelpLines() []string {
	return []string{
		"/proposals                  列出待审批的提案（status=pending）",
		"/proposals show <id>        该提案的 diff / 理由 / 证据",
		"/proposals approve <id>     把 status 改成 approved（不动任何 prompt.md）",
		"/proposals reject <id>      把 status 改成 rejected（不动任何 prompt.md）",
		"",
		"提案由 `yagent plasma propose` 生成，落在 [plasma] proposal_dir（默认 ~/.yagent/agents/proposals）。",
		"批准也不自动 apply：内建 Agent 的 prompt 与 Go 代码要同时改。",
	}
}

// proposalsCommand 处理 `/proposals [sub] [args...]`。
func (m *model) proposalsCommand(args []string) tea.Cmd {
	dir := config.PlasmaProposalDir()
	if len(args) == 0 {
		return m.showProposalsList(dir)
	}
	switch args[0] {
	case "help", "-h", "--help":
		return m.showProposalsPanel(proposalsHelpLines())

	case "list", "ls":
		return m.showProposalsList(dir)

	case "show":
		if len(args) < 2 {
			m.setInfoMsg("用法：/proposals show <id>")
			return nil
		}
		p, err := plasma.LoadProposal(dir, args[1])
		if err != nil {
			m.setInfoMsg(proposalErrorText(args[1], err))
			return nil
		}
		return m.showProposalsPanel(proposalDetailLines(p))

	case "approve", "reject":
		if len(args) < 2 {
			m.setInfoMsg("用法：/proposals " + args[0] + " <id>")
			return nil
		}
		to := plasma.ProposalStatusApproved
		if args[0] == "reject" {
			to = plasma.ProposalStatusRejected
		}
		return m.decideProposal(dir, args[1], to)

	default:
		m.setInfoMsg(fmt.Sprintf("未知子命令 %q（/proposals help）", args[0]))
		return nil
	}
}

// showProposalsList 打开列表面板。
func (m *model) showProposalsList(dir string) tea.Cmd {
	lines, err := proposalListLines(dir)
	if err != nil {
		m.setInfoMsg(err.Error())
		return nil
	}
	return m.showProposalsPanel(lines)
}

// showProposalsPanel 把若干行文本放进信息面板（几何与按键复用 components.TextPanel）。
func (m *model) showProposalsPanel(lines []string) tea.Cmd {
	return m.showTextPanel(proposalsPanelID, "◆ proposals", lines)
}

// proposalListLines 只列 pending，但**不隐瞒**已判决的与坏掉的那几份：
// 一份读不出来的提案如果从列表里消失，"审批队列是空的"就成了一个错误结论。
func proposalListLines(dir string) ([]string, error) {
	entries, err := plasma.ListProposals(dir)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return []string{
			"尚无提案（pending=0）",
			"",
			"生成一份：yagent plasma propose --agent <内建 agent>",
			"目录：" + dir,
		}, nil
	}
	lines := make([]string, 0, len(entries)*2+4)
	decided := make([]string, 0, len(entries))
	broken := make([]string, 0)
	for _, e := range entries {
		if e.Err != nil {
			broken = append(broken, fmt.Sprintf("⚠ %s  读不出来：%v", e.ID, e.Err))
			continue
		}
		p := e.Proposal
		if p.Status != plasma.ProposalStatusPending {
			decided = append(decided, p.ID)
			continue
		}
		// 时间在前、理由在后：审批时先看"什么时候提的"，再看"为什么提"。
		lines = append(lines, fmt.Sprintf("[%s] %s  agent=%s  %s", p.Status, p.ID, p.Agent, p.CreatedAt))
		lines = append(lines, "    "+firstLine(p.Rationale))
	}
	if len(lines) == 0 {
		lines = append(lines, "没有待审批的提案（pending=0）")
	}
	if len(decided) > 0 {
		lines = append(lines, "", fmt.Sprintf("另有 %d 份已判决（/proposals show <id>）：%s",
			len(decided), strings.Join(decided, ", ")))
	}
	if len(broken) > 0 {
		lines = append(lines, "", "读不动的提案：")
		lines = append(lines, broken...)
	}
	lines = append(lines, "",
		"目录："+dir,
		"审批只改提案 JSON 的 status 字段；不会改写任何 *.prompt.md。")
	return lines, nil
}

// proposalDetailLines 渲染一份提案的全文（含 diff）。
// `PromptPath` 只当文本显示：读它没有意义，写它正是被禁止的那件事。
func proposalDetailLines(p *plasma.Proposal) []string {
	if p == nil {
		return []string{"（提案为空）"}
	}
	lines := []string{
		"id:        " + p.ID,
		"agent:     " + p.Agent + "    status: " + p.Status,
		"created:   " + p.CreatedAt,
		"base_hash: " + plasma.ShortHash(p.BaseHash),
	}
	if p.PromptPath != "" {
		lines = append(lines, "prompt:    "+p.PromptPath+"（只显示路径，不读也不写）")
	}
	if len(p.RuleIDs) > 0 {
		lines = append(lines, "rules:     "+strings.Join(p.RuleIDs, ", "))
	}
	if len(p.EditableFields) > 0 {
		lines = append(lines, "可改字段:  "+strings.Join(p.EditableFields, ", "))
	}
	if strings.TrimSpace(p.Rationale) != "" {
		lines = append(lines, "", "理由：", p.Rationale)
	}
	if len(p.Evidence) > 0 {
		lines = append(lines, "", "证据：")
		for _, item := range p.Evidence {
			lines = append(lines, "  - "+item)
		}
	}
	lines = append(lines, "", "建议的改动（diff，仅供人读）：")
	if diff := splitPanelLines(p.Diff); len(diff) > 0 {
		lines = append(lines, diff...)
	} else {
		lines = append(lines, "  （提案里没有 diff）")
	}
	lines = append(lines, "", "审批只改 status；不会改写任何 *.prompt.md。")
	return lines
}

// decideProposal 是审批动作：写 status → 回显（状态行 + 详情面板）。
//
// 顺序不能反：先写盘再回显。反过来（先画面板再写）在写失败时会显示一份"已批准"
// 的面板，而磁盘上仍是 pending —— 那种界面骗人的问题查起来最费时间。
func (m *model) decideProposal(dir, id, to string) tea.Cmd {
	p, err := plasma.SetProposalStatus(dir, id, to)
	if err != nil {
		m.setInfoMsg(proposalErrorText(id, err))
		return nil
	}
	verb := "已批准"
	if to == plasma.ProposalStatusRejected {
		verb = "已拒绝"
	}
	m.appendStatusLine(fmt.Sprintf("✔ %s %s（只写了 status 字段；未改任何 *.prompt.md）", verb, p.ID))
	return m.showProposalsPanel(proposalDetailLines(p))
}

// proposalErrorText 把错误翻成"下一步该做什么"。不 panic、不退出 TUI —— 一次手误
// 不该把整个会话带走。
func proposalErrorText(id string, err error) string {
	switch {
	case errors.Is(err, plasma.ErrProposalNotFound):
		return fmt.Sprintf("提案 %q 不存在（/proposals 看待审批列表）", id)
	default:
		return fmt.Sprintf("提案 %q 处理失败：%v", id, err)
	}
}
