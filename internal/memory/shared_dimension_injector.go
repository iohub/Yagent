package memory

import (
	"fmt"
	"strings"
)

// SharedMemoryInjector 将4维共享记忆格式化为system prompt可注入的文本
type SharedMemoryInjector struct {
	store *SharedDimensionStore
}

// NewSharedMemoryInjector 创建注入器
func NewSharedMemoryInjector(store *SharedDimensionStore) *SharedMemoryInjector {
	return &SharedMemoryInjector{store: store}
}

// InjectContext 构建共享记忆的Markdown文本，用于注入Agent system prompt
// 只包含非空的记忆维度
func (i *SharedMemoryInjector) InjectContext(userID, projectID string) string {
	userMem, fbMem, projMem, refMem, err := i.store.GetAllForUser(userID, projectID)
	if err != nil {
		return ""
	}

	var sections []string

	if !userMem.IsEmpty() {
		sections = append(sections, i.formatUserMemory(userMem))
	}
	if !fbMem.IsEmpty() {
		sections = append(sections, i.formatFeedbackMemory(fbMem))
	}
	if !projMem.IsEmpty() {
		sections = append(sections, i.formatProjectMemory(projMem))
	}
	if !refMem.IsEmpty() {
		sections = append(sections, i.formatReferenceMemory(refMem))
	}

	if len(sections) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\n<shared_memory>\n")
	sb.WriteString("The following shared memory is visible to all agents and persisted across conversations. ")
	sb.WriteString("Use this context to personalize your responses and avoid repeating mistakes.\n\n")
	sb.WriteString(strings.Join(sections, "\n"))
	sb.WriteString("</shared_memory>\n")
	return sb.String()
}

// formatUserMemory 格式化用户维度的记忆
func (i *SharedMemoryInjector) formatUserMemory(m *UserMemory) string {
	var sb strings.Builder
	sb.WriteString("### 👤 User Profile\n")

	var profileParts []string
	if m.Profile.Name != "" {
		profileParts = append(profileParts, fmt.Sprintf("Name: %s", m.Profile.Name))
	}
	if m.Profile.Role != "" {
		profileParts = append(profileParts, fmt.Sprintf("Role: %s", m.Profile.Role))
	}
	if m.Profile.Team != "" {
		profileParts = append(profileParts, fmt.Sprintf("Team: %s", m.Profile.Team))
	}
	if m.Profile.Seniority != "" {
		profileParts = append(profileParts, fmt.Sprintf("Level: %s", m.Profile.Seniority))
	}
	if len(profileParts) > 0 {
		sb.WriteString(fmt.Sprintf("- Profile: %s\n", strings.Join(profileParts, " | ")))
	}

	if len(m.Expertise) > 0 {
		sb.WriteString(fmt.Sprintf("- Expertise: %s\n", strings.Join(m.Expertise, ", ")))
	}

	var prefParts []string
	if m.Prefs.Language != "" {
		prefParts = append(prefParts, fmt.Sprintf("Language: %s", m.Prefs.Language))
	}
	if m.Prefs.DetailLevel != "" {
		prefParts = append(prefParts, fmt.Sprintf("Detail: %s", m.Prefs.DetailLevel))
	}
	if m.Prefs.CodeStyle != "" {
		prefParts = append(prefParts, fmt.Sprintf("Code style: %s", m.Prefs.CodeStyle))
	}
	if m.Prefs.ResponseFormat != "" {
		prefParts = append(prefParts, fmt.Sprintf("Format: %s", m.Prefs.ResponseFormat))
	}
	if len(prefParts) > 0 {
		sb.WriteString(fmt.Sprintf("- Preferences: %s\n", strings.Join(prefParts, " | ")))
	}

	for k, v := range m.Prefs.Other {
		sb.WriteString(fmt.Sprintf("- %s: %s\n", k, v))
	}

	return sb.String()
}

// formatFeedbackMemory 格式化反馈维度的记忆
func (i *SharedMemoryInjector) formatFeedbackMemory(m *FeedbackMemory) string {
	var sb strings.Builder
	sb.WriteString("### 🔄 Feedback History\n")

	if len(m.Patterns) > 0 {
		sb.WriteString("- Learned Patterns:\n")
		for _, p := range m.Patterns {
			sb.WriteString(fmt.Sprintf("  - %s (confidence: %.0f%%)\n", p.Pattern, p.Confidence*100))
		}
	}

	if len(m.Corrections) > 0 {
		sb.WriteString("- Recent Corrections:\n")
		shown := m.Corrections
		if len(shown) > 5 {
			shown = shown[len(shown)-5:]
		}
		for _, c := range shown {
			sb.WriteString(fmt.Sprintf("  - [%s] %s → %s\n", c.Topic, c.Wrong, c.Correct))
		}
	}

	if len(m.Endorsements) > 0 {
		sb.WriteString("- Endorsed Approaches:\n")
		shown := m.Endorsements
		if len(shown) > 3 {
			shown = shown[len(shown)-3:]
		}
		for _, e := range shown {
			sb.WriteString(fmt.Sprintf("  - [%s] %s\n", e.Topic, e.Approach))
		}
	}

	return sb.String()
}

// formatProjectMemory 格式化项目维度的记忆
func (i *SharedMemoryInjector) formatProjectMemory(m *ProjectMemory) string {
	var sb strings.Builder
	sb.WriteString("### 📋 Project Context\n")

	if m.Status != "" {
		sb.WriteString(fmt.Sprintf("- Status: %s\n", m.Status))
	}

	activeObjectives := filterActiveObjectives(m.Objectives)
	if len(activeObjectives) > 0 {
		sb.WriteString("- Active Objectives:\n")
		for _, o := range activeObjectives {
			sb.WriteString(fmt.Sprintf("  - [%s] %s\n", o.Priority, o.Description))
		}
	}

	if len(m.Deadlines) > 0 {
		sb.WriteString("- Deadlines:\n")
		for _, d := range m.Deadlines {
			sb.WriteString(fmt.Sprintf("  - %s by %s (%s)\n", d.Description, d.Date, d.Priority))
		}
	}

	if len(m.Team) > 0 {
		sb.WriteString("- Team:\n")
		for _, t := range m.Team {
			sb.WriteString(fmt.Sprintf("  - %s (%s): %s\n", t.Name, t.Role, t.Responsibility))
		}
	}

	return sb.String()
}

// formatReferenceMemory 格式化参考维度的记忆
func (i *SharedMemoryInjector) formatReferenceMemory(m *ReferenceMemory) string {
	var sb strings.Builder
	sb.WriteString("### 📚 Reference Resources\n")

	// 按Category分组
	byCategory := make(map[string][]Resource)
	for _, r := range m.Resources {
		byCategory[r.Category] = append(byCategory[r.Category], r)
	}

	for cat, resources := range byCategory {
		sb.WriteString(fmt.Sprintf("- %s:\n", cat))
		for _, r := range resources {
			desc := r.Description
			if desc == "" {
				desc = r.Location
			}
			sb.WriteString(fmt.Sprintf("  - %s: %s\n", r.Name, desc))
		}
	}

	return sb.String()
}

// filterActiveObjectives 过滤出活跃状态的目标
func filterActiveObjectives(objectives []Objective) []Objective {
	var active []Objective
	for _, o := range objectives {
		if o.Status == "active" || o.Status == "" {
			active = append(active, o)
		}
	}
	return active
}
