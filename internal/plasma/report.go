package plasma

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// 本文件是 report.json 的**落盘形态**与它唯一的读回入口。
//
// 为什么单独成文件：写侧在 store.go（Put 落盘），读侧有两个读者 —— 进化管线与
// TUI 的 `/plasma explain`。类型放在读写的公共处、由两侧共用，才不会出现"写的时候
// 有 outcomes，读的时候按另一个 struct 解析"这种每次都能编译通过的静默。
//
// 它刻意**不是** Report 全量：Report 是编译期的对象，report.json 是可变快照
// （grade 会升降，诊断与逐条观测是那一次编译的账）。

// ReportRecord 是 report.json 的形态（不是 Report 全量：Report 里有非导出字段）。
// Outcomes 一并落盘：进化的评分侧与 `yagent plasma list` 的读者要能看到"哪条夹具红了"，
// 而 reasons 只写在 report.json 里，别处没有第二份。
type ReportRecord struct {
	Grade              Grade        `json:"grade"`
	EscalationRequired bool         `json:"escalation_required"`
	Diagnostics        []Diagnostic `json:"diagnostics,omitempty"`
	Outcomes           Outcomes     `json:"outcomes,omitempty"`
}

func newReportRecord(r *Report) ReportRecord {
	return ReportRecord{
		Grade:              r.Grade,
		EscalationRequired: r.EscalationRequired,
		Diagnostics:        r.Diagnostics,
		Outcomes:           r.Outcomes,
	}
}

// ErrNoReport 表示这个 hash 目录里没有 report.json。
//
// 单列一个哨兵而不是让调用方 os.IsNotExist 一把梭：三种"没读到"的修法完全不同 ——
// 文件不存在（编译期没落过诊断，属正常的一类）、目录不存在（坐标错了）、
// JSON 坏了（磁盘被改过）。把前两者混成一种，症状是"explain 说没有诊断，其实是没有这个 Agent"。
var ErrNoReport = errors.New("plasma: report.json 不存在")

// ReadReport 读某份已落盘 bundle 的诊断快照。
//
// 目录布局与 HashBundle 无关（这里只读、不校验主键），校验 hash 是 Load 的职责：
// 诊断快照不参与"这份 bundle 可信吗"的判定，它只回答"上一次编译说了什么"。
func (s *Store) ReadReport(name, hash string) (*ReportRecord, error) {
	path := filepath.Join(s.Root, name, hash, "report.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%s@%s: %w（%s）", name, hash, ErrNoReport, path)
		}
		return nil, fmt.Errorf("read report %s: %w", path, err)
	}
	var rec ReportRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, fmt.Errorf("parse report %s: %w", path, err)
	}
	return &rec, nil
}

// Text 把快照里的诊断按 Report 的同一套格式渲染。
//
// 委托而不是另写一份排版：诊断文本的唯一实现在 diag.go 的 Report.Text()，
// 抄一份的症状是"CLI 说 PLS-210 note…，面板里少了 note"。
func (r *ReportRecord) Text() string {
	if r == nil {
		return ""
	}
	return (&Report{Diagnostics: r.Diagnostics}).Text()
}
