package memory

import "time"

// ============================================================
// Dimension Types
// ============================================================

// Dimension 表示共享记忆的维度
type Dimension string

const (
	DimUser      Dimension = "user"
	DimFeedback  Dimension = "feedback"
	DimReference Dimension = "reference"
)

// AllDimensions 返回所有维度列表
func AllDimensions() []Dimension {
	return []Dimension{DimUser, DimFeedback, DimReference}
}

// ============================================================
// Dimension 1: User Memory — 用户画像
// ============================================================

// UserMemory 存储用户画像信息，帮助 Agent 个性化交互
type UserMemory struct {
	UserID    string              `json:"user_id"`
	Name      string              `json:"name,omitempty"`         // 从 profile.name 提升
	Role      string              `json:"role,omitempty"`         // 从 profile.role 提升
	Team      string              `json:"team,omitempty"`         // 从 profile.team 提升
	Seniority string              `json:"seniority,omitempty"`    // 从 profile.seniority 提升
	Expertise []string            `json:"expertise,omitempty"`
	Language       string            `json:"language,omitempty"`         // 从 preferences.language 提升
	DetailLevel    string            `json:"detail_level,omitempty"`     // 从 preferences.detail_level 提升
	CodeStyle      string            `json:"code_style,omitempty"`       // 从 preferences.code_style 提升
	ResponseFormat string            `json:"response_format,omitempty"`  // 从 preferences.response_format 提升
	Metadata       map[string]string `json:"metadata,omitempty"`         // 从 preferences.other 提升
	Version     int64             `json:"version"`
	UpdatedAt   time.Time         `json:"updated_at"`
	UpdatedBy   string            `json:"updated_by"` // agent name
}

// Deprecated: 已扁平化到 UserMemory 顶层。仅用于向后兼容数据读取。
type UserProfile struct {
	Name      string `json:"name,omitempty"`
	Role      string `json:"role,omitempty"`      // e.g., "Senior Engineer"
	Team      string `json:"team,omitempty"`
	Seniority string `json:"seniority,omitempty"` // junior/mid/senior/staff
}

// Deprecated: 已扁平化到 UserMemory 顶层。仅用于向后兼容数据读取。
type UserPreferences struct {
	Language       string            `json:"language,omitempty"`         // zh/en
	DetailLevel    string            `json:"detail_level,omitempty"`    // brief/moderate/detailed
	CodeStyle      string            `json:"code_style,omitempty"`      // verbose/concise/idiomatic
	ResponseFormat string            `json:"response_format,omitempty"` // prose/bullet/step-by-step
	Other          map[string]string `json:"other,omitempty"`           // extensible
}

// IsEmpty 检查 UserMemory 是否为空
func (m *UserMemory) IsEmpty() bool {
	return m.Name == "" && m.Role == "" && m.Team == "" && m.Seniority == "" &&
		len(m.Expertise) == 0 &&
		m.Language == "" && m.DetailLevel == "" && m.CodeStyle == "" && m.ResponseFormat == "" &&
		len(m.Metadata) == 0
}

// ============================================================
// Dimension 2: Feedback Memory — 用户反馈
// ============================================================

// FeedbackMemory 存储用户纠正和认可的历史，帮助Agent避免重复错误
type FeedbackMemory struct {
	UserID       string            `json:"user_id"`
	Corrections  []Correction      `json:"corrections"`   // max 15
	Endorsements []Endorsement     `json:"endorsements"`  // max 10
	Patterns     []FeedbackPattern `json:"patterns"`      // max 10
	Version      int64             `json:"version"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

// Correction 用户纠正记录
type Correction struct {
	ID        string    `json:"id"`
	Topic     string    `json:"topic"`      // e.g., "code-style", "architecture"
	Wrong     string    `json:"wrong"`      // what agent said
	Correct   string    `json:"correct"`    // what user corrected to
	Context   string    `json:"context"`    // brief context
	Timestamp time.Time `json:"timestamp"`
}

// Endorsement 用户认可记录
type Endorsement struct {
	ID        string    `json:"id"`
	Topic     string    `json:"topic"`
	Approach  string    `json:"approach"`  // what was endorsed
	Timestamp time.Time `json:"timestamp"`
}

// FeedbackPattern 从多次反馈中提炼的模式
type FeedbackPattern struct {
	Pattern    string    `json:"pattern"`     // e.g., "prefers composition over inheritance"
	Evidence   int       `json:"evidence"`    // how many corrections support this
	Confidence float64   `json:"confidence"`  // 0.0 - 1.0
	LastSeen   time.Time `json:"last_seen"`
}

// IsEmpty 检查FeedbackMemory是否为空
func (m *FeedbackMemory) IsEmpty() bool {
	return len(m.Corrections) == 0 && len(m.Endorsements) == 0 && len(m.Patterns) == 0
}

// ============================================================
// Dimension 3（已移除）: Project Memory — 项目上下文
// ============================================================

// ============================================================
// Dimension 4: Reference Memory — 参考资源
// ============================================================

// ReferenceMemory 存储外部资源位置，帮助Agent找到所需信息
type ReferenceMemory struct {
	ProjectID string     `json:"project_id"`
	Resources []Resource `json:"resources"` // max 20
	Version   int64      `json:"version"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// Resource 参考资源
type Resource struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`        // e.g., "Jira Board"
	Category    string   `json:"category"`    // e.g., "project-mgmt", "monitoring"
	Location    string   `json:"location"`    // URL or path
	Description string   `json:"description"` // what you can find there
	Tags        []string `json:"tags"`
}

// IsEmpty 检查ReferenceMemory是否为空
func (m *ReferenceMemory) IsEmpty() bool {
	return len(m.Resources) == 0
}

// ============================================================
// Update Action Types
// ============================================================

// UpdateAction 表示更新操作类型
type UpdateAction string

const (
	ActionAdd    UpdateAction = "add"
	ActionUpdate UpdateAction = "update"
	ActionRemove UpdateAction = "remove"
)

// ============================================================
// Dimension-specific update payloads (field-targeted)
// ============================================================

// UserMemoryUpdatePayload 用户维度更新载荷（新 field-targeted 模式）
type UserMemoryUpdatePayload struct {
	Name           *string           `json:"name,omitempty"`
	Role           *string           `json:"role,omitempty"`
	Team           *string           `json:"team,omitempty"`
	Seniority      *string           `json:"seniority,omitempty"`
	Expertise      []string          `json:"expertise,omitempty"`
	Language       *string           `json:"language,omitempty"`
	DetailLevel    *string           `json:"detail_level,omitempty"`
	CodeStyle      *string           `json:"code_style,omitempty"`
	ResponseFormat *string           `json:"response_format,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
}

// FeedbackMemoryUpdatePayload 反馈维度更新载荷
type FeedbackMemoryUpdatePayload struct {
	Corrections  *Correction   `json:"corrections,omitempty"`  // 单数→复数
	Endorsements *Endorsement  `json:"endorsements,omitempty"` // 单数→复数
}

// ReferenceMemoryUpdatePayload 参考维度更新载荷
type ReferenceMemoryUpdatePayload struct {
	Resources *Resource `json:"resources,omitempty"` // 原 resource
	// remove_by_id 被移除，改用 item_id 参数
}
