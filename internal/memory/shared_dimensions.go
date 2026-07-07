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
	DimProject   Dimension = "project"
	DimReference Dimension = "reference"
)

// AllDimensions 返回所有维度列表
func AllDimensions() []Dimension {
	return []Dimension{DimUser, DimFeedback, DimProject, DimReference}
}

// ============================================================
// Dimension 1: User Memory — 用户画像
// ============================================================

// UserMemory 存储用户画像信息，帮助Agent个性化交互
type UserMemory struct {
	UserID    string          `json:"user_id"`
	Profile   UserProfile     `json:"profile"`
	Expertise []string        `json:"expertise"` // max 10
	Prefs     UserPreferences `json:"preferences"`
	Version   int64           `json:"version"`
	UpdatedAt time.Time       `json:"updated_at"`
	UpdatedBy string          `json:"updated_by"` // agent name
}

// UserProfile 用户基本档案
type UserProfile struct {
	Name      string `json:"name,omitempty"`
	Role      string `json:"role,omitempty"`      // e.g., "Senior Engineer"
	Team      string `json:"team,omitempty"`
	Seniority string `json:"seniority,omitempty"` // junior/mid/senior/staff
}

// UserPreferences 用户交互偏好
type UserPreferences struct {
	Language       string            `json:"language,omitempty"`         // zh/en
	DetailLevel    string            `json:"detail_level,omitempty"`    // brief/moderate/detailed
	CodeStyle      string            `json:"code_style,omitempty"`      // verbose/concise/idiomatic
	ResponseFormat string            `json:"response_format,omitempty"` // prose/bullet/step-by-step
	Other          map[string]string `json:"other,omitempty"`           // extensible
}

// IsEmpty 检查UserMemory是否为空
func (m *UserMemory) IsEmpty() bool {
	return m.Profile == (UserProfile{}) &&
		len(m.Expertise) == 0 &&
		m.Prefs.Language == "" &&
		m.Prefs.DetailLevel == "" &&
		m.Prefs.CodeStyle == "" &&
		m.Prefs.ResponseFormat == "" &&
		len(m.Prefs.Other) == 0
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
// Dimension 3: Project Memory — 项目上下文
// ============================================================

// ProjectMemory 存储当前项目上下文，帮助Agent理解工作重心
type ProjectMemory struct {
	ProjectID  string       `json:"project_id"`
	Status     string       `json:"status"`      // one-line current status
	Objectives []Objective  `json:"objectives"`  // max 5 active
	Team       []TeamMember `json:"team"`        // max 10
	Deadlines  []Deadline   `json:"deadlines"`   // max 5
	Version    int64        `json:"version"`
	UpdatedAt  time.Time    `json:"updated_at"`
	UpdatedBy  string       `json:"updated_by"`
}

// Objective 项目目标
type Objective struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Priority    string `json:"priority"` // critical/high/medium/low
	Status      string `json:"status"`   // active/completed/dropped
}

// TeamMember 团队成员
type TeamMember struct {
	Name           string `json:"name"`
	Role           string `json:"role"`
	Responsibility string `json:"responsibility"`
}

// Deadline 截止日期
type Deadline struct {
	Description string `json:"description"`
	Date        string `json:"date"`     // ISO 8601
	Priority    string `json:"priority"`
}

// IsEmpty 检查ProjectMemory是否为空
func (m *ProjectMemory) IsEmpty() bool {
	return m.Status == "" && len(m.Objectives) == 0 && len(m.Team) == 0 && len(m.Deadlines) == 0
}

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
// Dimension-specific update payloads
// ============================================================

// UserMemoryUpdatePayload 用户维度更新载荷
type UserMemoryUpdatePayload struct {
	Profile   *UserProfile     `json:"profile,omitempty"`
	Expertise []string         `json:"expertise,omitempty"`
	Prefs     *UserPreferences `json:"preferences,omitempty"`
}

// FeedbackMemoryUpdatePayload 反馈维度更新载荷
type FeedbackMemoryUpdatePayload struct {
	Correction  *Correction   `json:"correction,omitempty"`
	Endorsement *Endorsement  `json:"endorsement,omitempty"`
}

// ProjectMemoryUpdatePayload 项目维度更新载荷
type ProjectMemoryUpdatePayload struct {
	Objective *Objective  `json:"objective,omitempty"`
	Member    *TeamMember `json:"member,omitempty"`
	Deadline  *Deadline   `json:"deadline,omitempty"`
	Status    *string     `json:"status,omitempty"`
}

// ReferenceMemoryUpdatePayload 参考维度更新载荷
type ReferenceMemoryUpdatePayload struct {
	Resource   *Resource `json:"resource,omitempty"`
	RemoveByID string    `json:"remove_by_id,omitempty"`
}
