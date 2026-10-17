// Package blueprint 是仓库级导航知识的落盘形态：一份 markdown 文档，
// 内容由 Repo agent 每次执行完成时提交的"原子"折叠而来
// （设计稿 docs/repo-blueprint-design.md v4，本包只做 R1 的骨架：
// 文档模型、段落枚举、原子行编解码、存储根解析与两条根护栏）。
//
// 叶子性：本包只准 import 标准库 + `yagent/internal/util` + `yagent/internal/projectid`。
// 它不认识 agents / tools / blackboard / config —— 那些方向由
// scripts/check_blueprint_leaf.sh 守住（`check_toolspec_leaf.sh` 同族）。
// 启用判据也不在这里：唯一判据是 `config.BlueprintEnabled()`（I8），
// 由调用方把结果作为参数传进来，避免"配置一个开关、包里又一个开关"。
package blueprint

import "strings"

// Section 是文档里一个段落的标题名。它们**同时是** schema 的 enum 值、
// 索引的分组 key 与折叠算法的分派依据 —— 所以英文、固定、不许本地化。
type Section = string

const (
	SecArchitecture Section = "Architecture"
	SecFlows        Section = "Flows"
	SecConventions  Section = "Conventions"
	SecDependencies Section = "Dependencies"
	SecGotchas      Section = "Gotchas"
	// SecModules 是**前缀段**：实际标题是 "Modules/<project-relative dir>"。
	// 它的叶子集合由派生层决定（§5.3 的孤儿判据），模型不能凭空加一片。
	SecModules Section = "Modules/"
)

// AssertedSections 是模型可以提交原子的段落，顺序即文档里段落的顺序。
// derived 段（Structure / Coverage）不在此列：它们由程序每次渲染现算，不落盘。
var AssertedSections = []Section{
	SecArchitecture,
	SecFlows,
	SecConventions,
	SecDependencies,
	SecGotchas,
	SecModules,
}

// ModuleFields 是 `Modules/*` 段里一条原子允许的字段前缀。
// 三个值沿用 v3 定案的语义：purpose（这片负责什么）、
// data_flow（什么进来、什么出去，用模块/外部系统说，不用符号）、
// add_here（这类新东西该落在哪儿，写成步骤）。
var ModuleFields = []string{"purpose", "data_flow", "add_here"}

// IsSection 报告名字是否是一个**具体**段落名（不含前缀段自身）。
func IsSection(name string) bool {
	for _, s := range AssertedSections {
		if s == SecModules {
			continue
		}
		if s == name {
			return true
		}
	}
	return false
}

// IsModuleHeading 报告标题是不是 "Modules/<path>" 形态。
// 只写 "Modules" 不算 —— 前缀段必须落到具体一片，否则孤儿判据无从谈起。
func IsModuleHeading(heading string) bool {
	path, ok := ModulePath(heading)
	return ok && path != ""
}

// ModulePath 从 "Modules/internal/tui" 取出 "internal/tui"。
// 返回 (path, true) 仅当它是合法的前缀段标题（分隔符后面非空，且不含 ".."）。
func ModulePath(heading string) (string, bool) {
	if !strings.HasPrefix(heading, SecModules) {
		return "", false
	}
	path := strings.TrimPrefix(heading, SecModules)
	if path == "" || strings.Contains(path, "..") {
		return "", false
	}
	return path, true
}

// ModuleHeading 是 ModulePath 的反向。
func ModuleHeading(path string) string {
	return SecModules + path
}

// IsModuleField 报告名字是否属于三个模块字段。
func IsModuleField(name string) bool {
	for _, f := range ModuleFields {
		if f == name {
			return true
		}
	}
	return false
}

// NormaliseSection 把 schema 里的小写值（"architecture"、"modules"）换成段落常量形态。
// 返回 false 表示这个词不在枚举里 —— 调用方必须把它记成一笔拒收，不许静默当默认段。
func NormaliseSection(name string) (Section, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	switch n {
	case "architecture":
		return SecArchitecture, true
	case "flows":
		return SecFlows, true
	case "conventions":
		return SecConventions, true
	case "dependencies":
		return SecDependencies, true
	case "gotchas":
		return SecGotchas, true
	case "modules", "modules/":
		return SecModules, true
	}
	return "", false
}
