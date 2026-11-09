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
)

// Diagnostic 的 Path 用 bundle 相对定位（bundle.tools[3]），让生成侧能精确改一处而不是整份重写。
type Diagnostic struct {
	Code     string
	Severity Severity
	Path     string
	Message  string
	Note     string
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
