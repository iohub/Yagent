package agents

import (
	"sort"
	"strings"
)

// 本文件把内建 Agent 的 prompt 全文以**只读**形态导出（P12a：`yagent plasma propose`
// 要把基准文本交给 Python 侧做确定性 diff 提案）。
//
// 为什么不在根包里再 //go:embed 一遍：
//   - prompt 的唯一来源是各 Agent 文件里的 //go:embed（coding.go:19、repo.go:17 ……）。
//     在 cmd/ 里复制一份 embed，第二份事实就多了一条会漂的路径：改了"哪个 Agent 用哪个
//     文件"不会有任何编译错误，症状是"提案改的是 A，线上跑的是 B"。
//   - 这里**不读盘、不做模板格式化**。PromptFormatter 是运行期的拼装（塞仓库约定、
//     git 章节等），不属于提案的基准文本：base_hash 必须能对上文件字节，否则审批时
//     那个 hash 指向的基准根本不存在。
//
// 安全边界（§1.2 / §8.1 T5）：本文件只把字符串交给调用方，既不写任何 *.prompt.md，
// 也没有打开它们的代码路径。

// PromptInfo 是一条内建 prompt 的只读描述。
type PromptInfo struct {
	// Agent 是遥测账本里的 agent 名（`delegate_` 后缀），也是 --agent 的取值。
	Agent string
	// Path 是仓库相对路径：提案的 diff 头要用它（`--- a/<path>`）。
	Path string
	// Text 是 embed 进来的原文，一字不改。
	Text string
}

// builtinPromptTable 是内建 prompt 的唯一清单。
//
// 新增一个内建 Agent 就在这里加一行 —— 漏加的表现是 `plasma propose --agent x` 报
// "未知内建 Agent"（可见），而不是静默拿到别的 prompt。
var builtinPromptTable = map[string]PromptInfo{
	"coding":   {Agent: "coding", Path: "internal/agents/coding.prompt.md", Text: codingPrompt},
	"repo":     {Agent: "repo", Path: "internal/agents/repo.prompt.md", Text: repoPrompt},
	"devops":   {Agent: "devops", Path: "internal/agents/devops.prompt.md", Text: devopsPrompt},
	"browser":  {Agent: "browser", Path: "internal/agents/browser.prompt.md", Text: browserPrompt},
	"chat":     {Agent: "chat", Path: "internal/agents/chat.prompt.md", Text: chatPrompt},
	"director": {Agent: "director", Path: "internal/agents/director.prompt.md", Text: directorPrompt},
}

// LookupPrompt 按 agent 名取 prompt 全文。
//
// 第二返回值同时兜住"没有这个内建 Agent"与"这个 Agent 的 prompt 是空的"：两种情况下
// 提案都没有意义（空基准会算出一个 sha256("") 的 base_hash，看着像真的）。
func LookupPrompt(agent string) (PromptInfo, bool) {
	info, ok := builtinPromptTable[strings.TrimSpace(strings.ToLower(agent))]
	if !ok || strings.TrimSpace(info.Text) == "" {
		return PromptInfo{}, false
	}
	return info, true
}

// PromptAgents 返回全部内建 agent 名（字典序）。用法提示与错误文案用它，
// 免得"可用取值"在三个地方各抄一份（抄一份必漂）。
func PromptAgents() []string {
	out := make([]string, 0, len(builtinPromptTable))
	for name := range builtinPromptTable {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
