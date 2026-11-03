// project_context.go — projectID 的唯一算法入口（转发到 internal/projectid）。
//
// 这里原本还负责读取并缓存工作区根的 YAGENT.md/CLAUDE.md/AGENTS.md。那份注入已整体删除：
// Director 不再预置任何根知识文件，子 Agent 只经 internal/agents/module_docs.go 拿目录级
// AGENTS.md（browser 目标连目录级都不注入）。别再在这里加回根文件加载——它烧的是每次会话的
// 固定 token，而收益是"模型可能读它"。
//
// 依赖约束：本包不得 import yagent/internal/agents（循环依赖守卫
// scripts/check_no_agents_import.sh）。
//
// 留下来的 ComputeProjectID 仍被门面层 director.go 的 computeProjectID 消费：rollout、
// artifact、蓝图共用同一把主键，所以算法只能有一份。

package director

import "yagent/internal/projectid"

// ComputeProjectID 从项目路径计算文件系统安全的 projectID。
// 实现已抽到 `internal/projectid`（rollout、artifact、蓝图共用一把主键），
// 这里只留转发：调用点与既有测试(`TestComputeProjectID`)都不用改一个字。
func ComputeProjectID(projectPath string) string {
	return projectid.Compute(projectPath)
}
