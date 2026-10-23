package agents

import (
	"strings"

	"yagent/internal/blueprint"
)

// 本文件是设计稿 §6 的 Agent 侧视图：一处把"派生快照 + 落盘散文"渲染成能进
// system prompt 的字符串，三处注入点共用同一份。
//
// 为什么只做**常驻形状②**：§6.1 的定案是"边是唯一无可替代的确定性事实，
// purpose 是唯一不可验证的散文"，所以散文永不进常驻，入口只有 read_blueprint。
//
// 为什么注入侧只 Request() 而不等扫描完成：§5.1 明确"注入侧渲染当前已有结果"。
// 于是每轮看到的骨架最坏滞后一次扫描（本仓实测 6.6ms），换来的是
// "派生层永不在任务关键路径上做磁盘工作"，以及 no-lock、no-blocking 的并发表述。

// 常驻块的固定抬头。文案是模型读的，所以英文（与蓝图正文同一条口径，§0.2 第 7 条）。
const (
	blueprintBlockTitle = "# Repository Blueprint (derived structure, refreshed every round)"
	blueprintBlockHint  = "Directory-level map only: which module talks to which. For symbols inside a module use semantic_search; for annotations call read_blueprint."
)

// BlueprintView 把一个 Agent 的蓝图注入需求收成一个对象。
// 预算与 token 计数器由装配层带进来 —— internal/blueprint 是 leaf 包，
// 既不读 [blueprint] 配置，也不 import tokenutil。
type BlueprintView struct {
	src         BlueprintSource
	site        string // inject_into 里的名字：director / repo / coding
	inject      bool
	indexTokens int
	proseTokens int
	counter     func(string) int
}

// NewBlueprintView 建视图。src 为 nil 时返回 nil，所有方法对 nil 接收者安全。
func NewBlueprintView(src BlueprintSource, site string, inject bool, indexTokens, proseTokens int, counter func(string) int) *BlueprintView {
	if src == nil {
		return nil
	}
	return &BlueprintView{
		src: src, site: site, inject: inject,
		indexTokens: indexTokens, proseTokens: proseTokens, counter: counter,
	}
}

// Source 给工具层（read_blueprint 的适配器）拿派生层。
func (v *BlueprintView) Source() BlueprintSource {
	if v == nil {
		return nil
	}
	return v.src
}

// Resident 是常驻注入块；关掉注入、没有快照来源时返回 ""（调用方直接拼接即可）。
func (v *BlueprintView) Resident() string {
	if v == nil || !v.inject {
		return ""
	}
	v.src.Request()
	idx := blueprint.RenderIndex(v.src.Snapshot(), v.doc(), blueprint.IndexSpec{
		MaxTokens:   v.indexTokens,
		CountTokens: v.counter,
	})
	return "\n\n" + blueprintBlockTitle + "\n" + blueprintBlockHint + "\n" + idx.String() + "\n"
}

// renderModule 收 doc 而不是自己 Load：ProseForTask 会在一行任务文本里匹配到多个片，
// 每个片各读一次 blueprint.md 是最不该出现的重复 IO（一次读、多次渲染）。
func (v *BlueprintView) renderModule(doc *blueprint.Doc, module string) string {
	if v == nil || module == "" {
		return ""
	}
	p := blueprint.RenderProse(v.src.Snapshot(), doc, blueprint.ProseSpec{
		Section:      blueprint.SecModules,
		Module:       module,
		BudgetTokens: v.proseTokens,
		CountTokens:  v.counter,
	})
	if !p.Ready || len(p.Lines) == 0 {
		return ""
	}
	// 只回真正带注解的行：UnknownModule 回执与"整段只有标题"对委派前缀毫无意义。
	body := strings.TrimSpace(strings.Join(p.Lines, "\n"))
	if body == "" || !strings.Contains(body, "- (") {
		return ""
	}
	return body
}

// ProseForTask 是给委派前缀用的"目标模块现有散文"（§6 的 R4 承重件）：
// Repo/Coding 拿到的是"这片已经被说成什么了"，它的提交才是**更新**而不是重新发明。
//
// 判据是"派生集合里的片路径逐字出现在任务文本里"，而不是"任务文本里长得像路径的串"。
// §6.3 当年否决"按委派文本里的路径字符串匹配、只注入该片+邻居"的理由仍然成立 ——
// 但那否决的是**猜路径**；这里匹配的分母是扫描器给出的封闭集合，匹配不上就是没有，
// 不存在"匹配失败静默退回一条危险路径"的分支。
func (v *BlueprintView) ProseForTask(task string) string {
	if v == nil || v.src == nil || task == "" {
		return ""
	}
	snap := v.src.Snapshot()
	if snap == nil {
		return ""
	}
	doc := v.doc() // 一次读，多个匹配片共用（见 renderModule）
	var blocks []string
	for _, dir := range snap.DerivedModuleList() {
		if dir == "." || !strings.Contains(task, dir) {
			continue
		}
		if prose := v.renderModule(doc, dir); prose != "" {
			blocks = append(blocks, prose)
		}
	}
	if len(blocks) == 0 {
		return ""
	}
	return strings.Join(blocks, "\n")
}

// doc 读回落盘散文。每次注入现读：一次几 KB 的 ReadFile 换"跨进程 fold 立刻可见"，
// 而缓存它就得再发明一套失效判据（同一条理由见 §5.1 的结构层）。
func (v *BlueprintView) doc() *blueprint.Doc {
	root := v.src.Root()
	if root == "" {
		return nil
	}
	doc, err := blueprint.Load(root)
	if err != nil {
		return nil // 读不动就当没有注解；索引里的坏行账目由 fold 侧负责（I18）
	}
	return doc
}
