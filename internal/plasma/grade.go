package plasma

import "strings"

// 本文件是"这份 Agent 能不能被**自动**委派"的唯一判据（ADR-9 的准入列 + ADR-4 的升级门）。
//
// 为什么需要一个函数而不是让调用方各自比字符串：判据有两半，而两半都容易被写漏一半 ——
//   - 档位：`auto_delegate_min_grade`（默认 verified）；
//   - 升级：用到越权能力且**未批准**的 Agent 不论档位都不可自动委派（ADR-4 明写
//     "grade 上限 provisional"，而 provisional 本身就已低于默认门槛）。
// 写两处的症状是"某个入口放行了一个没批准过写/shell 的 Agent"，而那条路径平时不跑。

// GradeAtLeast 判断 g 是否达到或高于 min（promoted > verified > provisional）。
// 空串（= 编译失败 / 没算过）永远不达标：把"没判过"当成合格是最典型的默认放行。
func GradeAtLeast(g, min Grade) bool {
	if g == "" {
		return false
	}
	if min == "" {
		min = GradeVerified
	}
	return gradeRank(g) <= gradeRank(min)
}

// CanAutoDelegate 是自动委派的准入判据。
//
// escalationRequired && !approved 一票否决：批准是**按 hash** 记的用户明示，未批准时
// 越权工具根本没进 `LinkPlan.Agent.Tools`（阶段 2 已拒），此时"自动委派"等于把一个
// 能力残缺的 Agent 派出去干需要写权限的活 —— 它会烧完步数再失败。
func CanAutoDelegate(grade Grade, escalationRequired, escalationApproved bool, minGrade Grade) bool {
	if escalationRequired && !escalationApproved {
		return false
	}
	return GradeAtLeast(grade, minGrade)
}

// NormalizeMinGrade 把配置里的档位名归一成 Grade；认不出来的值回落 verified。
//
// 只服务**展示/装配**侧：配置校验（`config.NormalizePlasma`）对认不出的值报错而不是回落，
// 那条 fail-closed 判据不能因为这里有个宽容的默认值而失效 —— 所以这个函数只在
// "配置已经过校验"的路径上使用（装配层），不要拿它去校验用户输入。
func NormalizeMinGrade(s string) Grade {
	switch g := Grade(strings.ToLower(strings.TrimSpace(s))); g {
	case GradeProvisional, GradeVerified, GradePromoted:
		return g
	default:
		return GradeVerified
	}
}
