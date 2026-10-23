package app

import (
	"os"
	"path/filepath"

	"yagent/internal/config"
	"yagent/internal/skills"
	"yagent/internal/toolspec"
)

// skillDirs 是技能目录的加载顺序：用户级 → 项目级 → [skills] dirs。
// 后者覆盖前者（同名的最终获胜者是可预测的：越靠近项目越具体）。
func skillDirs(workDir string) []string {
	var dirs []string
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".yagent", "skills"))
	}
	dirs = append(dirs, filepath.Join(workDir, ".yagent", "skills"))
	dirs = append(dirs, config.GlobalSkills().Dirs...)
	return dirs
}

// Skills 返回技能注册表（TUI 的 /skills 面板与斜杠补全用）。nil = 技能被关掉。
// 接收者判 nil：TUI 的单元测试会拿零值 Yagent 跑界面分支。
func (ca *Yagent) Skills() *skills.Registry {
	if ca == nil {
		return nil
	}
	return ca.skills
}

// SetSkillsForTesting 只给测试用。真实路径由 Init 加载注册表；
// 不提供生产用的 setter —— "运行中换技能集"不是一个应该存在的入口
// （技能集是 system prompt 的稳定前缀，换了就要重启）。
func (ca *Yagent) SetSkillsForTesting(registry *skills.Registry) { ca.skills = registry }

// SkillSource 是工具层需要的窄接口（use_skill 的执行器）。
// 必须显式判 nil：把 nil 的具体类型指针装进 interface 之后它不等于 nil
// （根 AGENTS.md §9.2），执行器会以为技能通道存在。
func (ca *Yagent) SkillSource() toolspec.SkillSource {
	if ca == nil || ca.skills == nil {
		return nil
	}
	return ca.skills
}
