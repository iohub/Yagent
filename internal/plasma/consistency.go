package plasma

import (
	"fmt"
	"sort"
	"strings"

	"yagent/internal/thinklink"
	"yagent/internal/tokenutil"
	"yagent/internal/toolspec"
)

// PromptTokenBudget 是系统提示的 token 上限（warning 档，不阻断）。
// 留余量的理由：tokenutil 的降级是一次性粘滞的，退回 len/4 后对中文约 4 倍低估，
// 于是"刚好压线"的提示词在降级进程里会看起来远低于预算。
const PromptTokenBudget = 8000

// 阶段 4：prompt 与声明之间的一致性对账。全部是 warning —— 它们指出"会静默坏掉"的形状，
// 但不足以断定这份 bundle 不可用；阻断由阶段 1-3 的 error 负责。
// warning 进 quality.lint，作为进化适应度的一项（把警告优化掉成为自动发生的压力）。
//
// 刻意不做"从自然语言推断调用图/终止性"：那构不出来，写进诊断只是文档表演。
// 这里只做可判定的对账：名字在不在、参数名对不对、预算超没超、格式契约有没有。
func (c *Compiler) consistency(b *Bundle, r *Report) {
	prompt := b.Prompt.System

	c.exitContract(prompt, r)
	c.paramNames(b, prompt, r)
	c.thoughtContract(b, prompt, r)
	c.tokenBudget(prompt, r)
	c.deadSelection(b, prompt, r)
	c.deniedMention(b, prompt, r)
}

// PLS-301：没有 agent_exit 输出契约。交付全文只经 deliverableText 取，
// 而它认的是 agent_exit 收尾的形态；prompt 不提 agent_exit，模型很可能纯文本收尾，
// 于是便签正文与 artifact 存的都不是那份交付。
func (c *Compiler) exitContract(prompt string, r *Report) {
	if !strings.Contains(prompt, toolspec.ExitToolName) {
		r.add(CodeNoExitContract, SeverityWarning, "bundle.prompt.system",
			"系统提示里没有 agent_exit 输出契约",
			"模型可能纯文本收尾；交付物只经 agent_exit 采集，便签正文会是空的")
	}
}

// PLS-302：参数名错配（坑 38 的形状：工具层只回显、Agent 层才解读，少一个键就静默消失）。
// 判据刻意收窄到"同一行里同时出现反引号包住的工具名与另一个反引号 token，
// 而那个 token 不是该工具的属性、却是**别的**注册工具的属性" —— 要求"是别的工具的属性"
// 这一条把普通散文里的反引号词挡在外面（坑 42：按形状识别的校验必须放过同形正常散文）。
func (c *Compiler) paramNames(b *Bundle, prompt string, r *Report) {
	owners := paramOwners()
	for _, line := range strings.Split(prompt, "\n") {
		tokens := backticked(line)
		if len(tokens) < 2 {
			continue
		}
		for _, tool := range tokens {
			sc, ok := toolspec.GetSchema(tool)
			if !ok || !containsStr(b.Tools, tool) {
				continue
			}
			for _, tok := range tokens {
				if tok == tool || sc.HasProperty(tok) {
					continue
				}
				other := owners[tok]
				if len(other) == 0 {
					continue // 不是任何工具的参数名 ⇒ 普通散文，放过
				}
				r.add(CodeParamMismatch, SeverityWarning, "bundle.prompt.system",
					fmt.Sprintf("提示把参数 %q 与工具 %q 写在同一行，但该工具的 schema 里没有它", tok, tool),
					fmt.Sprintf("%q 是 %s 的参数；调用会静默丢掉这个键", tok, strings.Join(other, "/")))
			}
		}
	}
}

// PLS-303：<thought> 契约。解析失败"不报错、只静默丢失"，所以必须在编译期查：
// 否则 Agent 看起来在跑，thinklink 面板与终极压缩的重建原料却全空。
// 规则复用 thinklink.ThoughtPlan.Validate，不在这里重写一份。
func (c *Compiler) thoughtContract(b *Bundle, prompt string, r *Report) {
	if !b.Contract.ThoughtFormat {
		return
	}
	blocks := thinklink.ExtractThoughtBlocks(prompt)
	if len(blocks) == 0 {
		r.add(CodeThoughtContract, SeverityWarning, "bundle.prompt.system",
			"contract.thought_format=true 但提示里没有 <thought> 模板",
			"格式契约缺失时解析会静默丢失，thinklink 面板与终极压缩原料都会是空的")
		return
	}
	for i, block := range blocks {
		for _, issue := range thinklink.ParseThoughtPlan(block).Validate() {
			r.add(CodeThoughtContract, SeverityWarning, "bundle.prompt.system",
				fmt.Sprintf("<thought> 模板第 %d 块不合规: %s", i+1, issue),
				"判据来自 thinklink.ThoughtPlan.Validate（goal/reason 必填、至多一条进行中）")
		}
	}
}

// PLS-304：提示词 token 预算。计数器与压缩阈值同源（tokenutil），
// 并把"是否处于 len/4 降级"说出来 —— 不说就等于让人以为读到的是精确值。
func (c *Compiler) tokenBudget(prompt string, r *Report) {
	n := tokenutil.EstimateTokens(prompt)
	if n <= PromptTokenBudget {
		return
	}
	note := fmt.Sprintf("预算 %d；提示词每轮固定占用，会吃掉压缩阈值前的可用空间", PromptTokenBudget)
	if tokenutil.IsFallback() {
		note += "。注意：tokenutil 已退回 len/4 降级估算，对中文约 4 倍低估，实际占用更高"
	}
	r.add(CodePromptBudget, SeverityWarning, "bundle.prompt.system",
		fmt.Sprintf("系统提示约 %d token，超出预算", n), note)
}

// PLS-306：声明了但提示里从未提及的工具。白占工具面预算，还让 deferred 目录变噪。
func (c *Compiler) deadSelection(b *Bundle, prompt string, r *Report) {
	for i, name := range b.Tools {
		if name == toolspec.ExitToolName {
			continue // 收尾契约由 PLS-301 单独管
		}
		if !strings.Contains(prompt, name) {
			r.add(CodeDeadSelection, SeverityWarning, fmt.Sprintf("bundle.tools[%d]", i),
				fmt.Sprintf("工具 %q 声明了但系统提示里从未提及", name),
				"模型不知道何时该用它；要么写进提示，要么从清单里去掉")
		}
	}
}

// PLS-307：提示点名了 policy.deny 明确放弃的能力。模型会反复尝试一个拿不到的工具，烧步数。
func (c *Compiler) deniedMention(b *Bundle, prompt string, r *Report) {
	deny, unknown := toolspec.EffectFromNames(b.Policy.Deny)
	if len(unknown) > 0 || deny == 0 {
		return // 名字拼错由 PLS-207 管，这里不重复报
	}
	for _, s := range toolspec.Specs() { // Specs() 已按名排序，发射顺序确定
		if !s.Effect.Has(deny) {
			continue
		}
		if strings.Contains(prompt, s.Name) {
			r.add(CodeDeniedMention, SeverityWarning, "bundle.prompt.system",
				fmt.Sprintf("提示点名了 %q，但它的能力 %v 已被 policy.deny 放弃", s.Name, s.Effect.Names()),
				"模型会反复尝试一个拿不到的工具，白烧步数")
		}
	}
}

// paramOwners 建"参数名 → 拥有它的工具名"反查表。map 只用于查询，
// 取值后一律排序，所以发射顺序不受 map 迭代影响（硬约束 5）。
func paramOwners() map[string][]string {
	owners := map[string][]string{}
	for _, name := range toolspec.Names() { // Names() 已排序
		sc, ok := toolspec.GetSchema(name)
		if !ok {
			continue
		}
		for _, p := range schemaProps(sc) {
			owners[p] = append(owners[p], name)
		}
	}
	for k := range owners {
		sort.Strings(owners[k])
	}
	return owners
}

func schemaProps(sc toolspec.Schema) []string {
	props, ok := sc.Parameters["properties"].(map[string]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(props))
	for k := range props {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// backticked 取一行里成对反引号包住的单 token（含空格的放过，那是代码片段不是标识符）。
func backticked(line string) []string {
	var out []string
	rest := line
	for {
		i := strings.Index(rest, "`")
		if i < 0 {
			return out
		}
		rest = rest[i+1:]
		j := strings.Index(rest, "`")
		if j < 0 {
			return out
		}
		tok := strings.TrimSpace(rest[:j])
		if tok != "" && !strings.ContainsAny(tok, " \t") {
			out = append(out, tok)
		}
		rest = rest[j+1:]
	}
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Warnings 返回 warning 档诊断，供 Emit（P4）写进 quality.lint。
func (r *Report) Warnings() []Diagnostic {
	out := make([]Diagnostic, 0, len(r.Diagnostics))
	for _, d := range r.Diagnostics {
		if d.Severity == SeverityWarning {
			out = append(out, d)
		}
	}
	return out
}
