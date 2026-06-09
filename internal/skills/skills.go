package skills

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Skill 表示一个技能
type Skill struct {
	Name        string // 文件名去掉 .md（如 "code-review"）
	Description string // 第一行内容（去掉开头的 # 标记）
	Content     string // 完整文件内容
	FilePath    string // 源文件路径
}

// SkillRegistry 技能注册表
type SkillRegistry struct {
	skills map[string]*Skill
	mu     sync.RWMutex
}

// LoadSkills 从指定目录加载所有 .md 文件作为技能
func LoadSkills(dirPath string) (*SkillRegistry, error) {
	registry := &SkillRegistry{
		skills: make(map[string]*Skill),
	}

	// 检查目录是否存在，不存在则返回空 registry（不报错）
	if _, err := os.Stat(dirPath); os.IsNotExist(err) {
		return registry, nil
	}

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory %s: %w", dirPath, err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		// 只处理 .md 文件
		if !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		filePath := filepath.Join(dirPath, entry.Name())
		content, err := os.ReadFile(filePath)
		if err != nil {
			slog.Warn("读取技能文件失败", "component", "skills", "path", filePath, "error", err)
			continue
		}

		contentStr := string(content)

		// 文件名去掉 .md 作为 Name
		name := strings.TrimSuffix(entry.Name(), ".md")

		// 提取第一行作为 Description（去掉 # 前缀）
		var description string
		lines := strings.SplitN(contentStr, "\n", 2)
		if len(lines) > 0 {
			description = strings.TrimSpace(lines[0])
			// 去掉开头的 # 或 # 前缀
			description = strings.TrimPrefix(description, "#")
			description = strings.TrimSpace(description)
		}

		skill := &Skill{
			Name:        name,
			Description: description,
			Content:     contentStr,
			FilePath:    filePath,
		}

		registry.skills[name] = skill
	}

	return registry, nil
}

// Get 按名称获取 skill
func (sr *SkillRegistry) Get(name string) (*Skill, bool) {
	sr.mu.RLock()
	defer sr.mu.RUnlock()
	skill, ok := sr.skills[name]
	return skill, ok
}

// Match 去掉命令前面的 : 前缀，然后查找 skill
func (sr *SkillRegistry) Match(command string) (*Skill, bool) {
	command = strings.TrimSpace(command)
	if strings.HasPrefix(command, ":") {
		command = command[1:]
	}
	return sr.Get(command)
}

// List 列出所有已注册的 skill 名称
func (sr *SkillRegistry) List() []string {
	sr.mu.RLock()
	defer sr.mu.RUnlock()
	names := make([]string, 0, len(sr.skills))
	for name := range sr.skills {
		names = append(names, name)
	}
	return names
}

// Count 返回已注册的 skill 数量
func (sr *SkillRegistry) Count() int {
	sr.mu.RLock()
	defer sr.mu.RUnlock()
	return len(sr.skills)
}
