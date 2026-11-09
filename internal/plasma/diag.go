package plasma

import (
	"fmt"
	"strings"
)

// Severity 只有两档：error 阻断出包，warning 不阻断但进 quality.lint 作为进化适应度的一项。
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

// 诊断码是稳定契约：Python 侧经 manifest 只读它，所以码表不可能两侧漂移。
// 码族：0xx parse · 1xx resolve · 2xx typecheck/权限 · 3xx consistency/lint（P2）·
// 5xx verify（P6）· 6xx emit/store（P4）· 7xx load（P4）。
const (
	CodeBadJSON        = "PLS-000"
	CodeSchemaVersion  = "PLS-001"
	CodeName           = "PLS-002"
	CodePromptEmpty    = "PLS-003"
	CodeToolsEmpty     = "PLS-004"
	CodeUnknownTool    = "PLS-101"
	CodeOutsidePalette = "PLS-201"
	CodeNoExit         = "PLS-202"
	CodeMaxSteps       = "PLS-203"
	CodeLLMTimeout     = "PLS-204"
	CodeParallelTools  = "PLS-205"
	CodeDenyConflict   = "PLS-206"
	CodeUnknownEffect  = "PLS-207"
	CodeEscalation     = "PLS-210"

	CodeNoExitContract  = "PLS-301"
	CodeParamMismatch   = "PLS-302"
	CodeThoughtContract = "PLS-303"
	CodePromptBudget    = "PLS-304"
	CodeDeadSelection   = "PLS-306"
	CodeDeniedMention   = "PLS-307"

	// 5xx 是阶段 5a（结构夹具）的失败族。一条失败一条诊断，Path 指向
	// fixtures.structural[<下标>]，Reason 原样进 note —— 生成侧靠它改下一轮。
	CodeFixtureFailed     = "PLS-501" // 未登记的夹具类型失败（回落码）
	CodeFixtureExit       = "PLS-502" // exit 契约违反：交付内容或退出形状不符
	CodeFixtureStepBudget = "PLS-503" // 步数预算未生效：stop_kind 或步数不符
	CodeFixtureToolScope  = "PLS-504" // Surface 含未声明工具（或多出、或缺失）
	CodeFixturePalette    = "PLS-505" // 工具能力超出 PlasmaPalette 且未批准
)

// Diagnostic 的 Path 用 bundle 相对定位（bundle.tools[3]），让生成侧能精确改一处而不是整份重写。
type Diagnostic struct {
	Code     string
	Severity Severity
	Path     string
	Message  string
	Note     string
}

// CodeInfo 是码表的一行：码 + 它在发射点的严重度 + 一句话说明。
//
// Severity 记的是**这个码在发射点的固定档位**（3xx 全是 warning、5xx 全是 error、
// PLS-210 是 warning、其余是 error）。它不参与运行时判定 —— Report 里每条诊断自带
// Severity —— 存在的理由是让 Python 侧经 manifest 拿到一份不漂移的码表。
type CodeInfo struct {
	Code     string
	Severity Severity
	Summary  string
}

// diagCatalog 是码表的**唯一来源**：上面的 const 块给 Go 侧引用，这张表给
// Manifest()/DiagCatalog() 发布。顺序按码族（0xx → 5xx），也就是字典序 ——
// 发布给 Python 的顺序因此是确定的。
//
// 漂移由两条测试钉住：TestDiagCatalogMatchesCorpus（表里的码集合 == corpus 真正
// 发射过的码集合）与 TestCorpusCoversEveryCode（每个码都得有一条 corpus 用例）。
// 新增一个码时必须三处同步：const、这张表、corpus —— 少一处就红。
var diagCatalog = []CodeInfo{
	{CodeBadJSON, SeverityError, "输入不是合法 JSON"},
	{CodeSchemaVersion, SeverityError, "schema_version 不受支持"},
	{CodeName, SeverityError, "name 为空或不是 snake_case"},
	{CodePromptEmpty, SeverityError, "prompt.system 为空"},
	{CodeToolsEmpty, SeverityError, "tools 列表为空"},

	{CodeUnknownTool, SeverityError, "工具名不在活注册表里"},
	{CodeOutsidePalette, SeverityError, "工具能力超出 palette"},
	{CodeNoExit, SeverityError, "工具面缺少 agent_exit"},
	{CodeMaxSteps, SeverityError, "max_steps 超出旋钮范围"},
	{CodeLLMTimeout, SeverityError, "llm_timeout 超出旋钮范围"},
	{CodeParallelTools, SeverityError, "max_parallel_readonly_tools 超出旋钮范围"},
	{CodeDenyConflict, SeverityError, "选中的工具与 policy.deny 冲突"},
	{CodeUnknownEffect, SeverityError, "policy.deny 里出现认不出的能力名"},
	{CodeEscalation, SeverityWarning, "用到 base palette 之外的能力（升级门）"},

	{CodeNoExitContract, SeverityWarning, "prompt 没有写收尾契约"},
	{CodeParamMismatch, SeverityWarning, "prompt 把不存在的参数名与工具写在一起"},
	{CodeThoughtContract, SeverityWarning, "prompt 缺少 Thought & Plan 契约"},
	{CodePromptBudget, SeverityWarning, "system prompt 超出预算"},
	{CodeDeadSelection, SeverityWarning, "选进来却从未在 prompt 里出现的工具"},
	{CodeDeniedMention, SeverityWarning, "prompt 提到了已声明放弃的能力"},

	{CodeFixtureFailed, SeverityError, "结构夹具失败（未登记类型的回落码）"},
	{CodeFixtureExit, SeverityError, "exit 契约违反"},
	{CodeFixtureStepBudget, SeverityError, "步数预算未生效"},
	{CodeFixtureToolScope, SeverityError, "Surface 的工具集合与声明不符"},
	{CodeFixturePalette, SeverityError, "工具能力超出 palette 且未批准"},
}

// DiagCatalog 返回码表副本。返回副本而不是包内 slice：调用方（manifest、
// 未来的 CLI 表格渲染）改一个字段不该污染下一次编译的诊断。
func DiagCatalog() []CodeInfo {
	out := make([]CodeInfo, len(diagCatalog))
	copy(out, diagCatalog)
	return out
}

// DiagError 让 Elaborate 的失败也带结构化诊断，而不是只有一句人话。
type DiagError struct {
	Diag Diagnostic
}

func (e *DiagError) Error() string { return e.Diag.Code + ": " + e.Diag.Message }

// Report 按发射顺序渲染（阶段顺序 + 阶段内按字段/下标顺序），不排序：
// 排序会把"先解析错还是先越权"这条因果线索抹掉，而生成侧靠它决定先修哪一处。
// 发射路径上一律不遍历 map，所以顺序是确定的。
type Report struct {
	Diagnostics []Diagnostic
	// Grade 是本次编译能达到的上限；P0 只可能产出 provisional（5a/5b 尚未接入）。
	Grade Grade
	// EscalationRequired 表示这份 bundle 用到了 base palette 之外的能力。
	// 它为真时 grade 上限钉在 provisional，直到用户批准 + 行为夹具通过（ADR-4/ADR-9）。
	// 即使升级已批准也要置真：防线生效过必须留下可见账目（坑 43）。
	EscalationRequired bool
}

func (r *Report) add(code string, sev Severity, path, msg, note string) {
	r.Diagnostics = append(r.Diagnostics, Diagnostic{
		Code: code, Severity: sev, Path: path, Message: msg, Note: note,
	})
}

func (r *Report) errorf(code, path, msg, note string) {
	r.add(code, SeverityError, path, msg, note)
}

func (r *Report) HasErrors() bool {
	for _, d := range r.Diagnostics {
		if d.Severity == SeverityError {
			return true
		}
	}
	return false
}

// Errors 只返回 error 档，供调用方判断"能不能装配"。
func (r *Report) Errors() []Diagnostic {
	out := make([]Diagnostic, 0, len(r.Diagnostics))
	for _, d := range r.Diagnostics {
		if d.Severity == SeverityError {
			out = append(out, d)
		}
	}
	return out
}

// Text 渲染 Clang 风格诊断。note 行缩进对齐到消息起点。
func (r *Report) Text() string {
	var b strings.Builder
	for _, d := range r.Diagnostics {
		prefix := d.Path + ": "
		fmt.Fprintf(&b, "%s%s[%s]: %s\n", prefix, d.Severity, d.Code, d.Message)
		if d.Note != "" {
			fmt.Fprintf(&b, "%snote: %s\n", strings.Repeat(" ", len(prefix)), d.Note)
		}
	}
	return b.String()
}
