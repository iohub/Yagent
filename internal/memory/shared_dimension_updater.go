package memory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// RestraintPolicy 定义了各维度的容量上限和克制规则
type RestraintPolicy struct {
	MaxUserExpertise      int // 用户专长领域上限
	MaxUserPrefsOther     int // 自定义偏好上限
	MaxCorrections        int // 纠正记录上限
	MaxEndorsements       int // 认可记录上限
	MaxFeedbackPatterns   int // 反馈模式上限
	MaxActiveObjectives   int // 活跃目标上限
	MaxTeamMembers        int // 团队成员上限
	MaxDeadlines          int // 截止日期上限
	MaxResources          int // 参考资源上限
	MinPatternEvidence    int // 形成模式所需的最小证据数
	MinReasonLength       int // 理由最小长度
}

// DefaultRestraintPolicy 返回默认克制策略
func DefaultRestraintPolicy() RestraintPolicy {
	return RestraintPolicy{
		MaxUserExpertise:    10,
		MaxUserPrefsOther:   5,
		MaxCorrections:      15,
		MaxEndorsements:     10,
		MaxFeedbackPatterns: 10,
		MaxActiveObjectives: 5,
		MaxTeamMembers:      10,
		MaxDeadlines:        5,
		MaxResources:        20,
		MinPatternEvidence:  2,
		MinReasonLength:     10,
	}
}

// SharedDimensionUpdater 处理更新提议，应用克制过滤
type SharedDimensionUpdater struct {
	store  *SharedDimensionStore
	policy RestraintPolicy
	Logger *SharedMemoryLogger // 可选的操作日志记录器，nil表示不记录
}

// NewSharedDimensionUpdater 创建更新处理器
func NewSharedDimensionUpdater(store *SharedDimensionStore, policy RestraintPolicy) *SharedDimensionUpdater {
	return &SharedDimensionUpdater{store: store, policy: policy}
}

// UpdateResult 更新操作的结果
type UpdateResult struct {
	Accepted bool
	Reason   string
}

// ApplyUpdate 处理一个内存更新提议，经过克制过滤后合并写入
// proposal中的Payload可以是JSON map[string]interface{}（来自LLM工具调用）
// 或者是类型化的payload结构体（来自代码内调用）
func (u *SharedDimensionUpdater) ApplyUpdate(proposal *MemoryUpdateProposal) UpdateResult {
	// Layer 1: 基本的proposal验证
	if err := u.validateProposal(proposal); err != nil {
		result := UpdateResult{Accepted: false, Reason: fmt.Sprintf("❌ Validation failed: %v", err)}
		u.tryLog(proposal, result, "", "")
		return result
	}

	// 捕获更新前的状态（用于日志）
	var beforeJSON string
	if u.Logger != nil {
		beforeJSON = u.captureBeforeState(proposal)
	}

	// Layer 2: 按维度应用具体更新逻辑
	var result UpdateResult
	switch proposal.Dimension {
	case DimUser:
		result = u.applyUserUpdate(proposal)
	case DimFeedback:
		result = u.applyFeedbackUpdate(proposal)
	case DimProject:
		result = u.applyProjectUpdate(proposal)
	case DimReference:
		result = u.applyReferenceUpdate(proposal)
	default:
		result = UpdateResult{Accepted: false, Reason: fmt.Sprintf("❌ Unknown dimension: %s", proposal.Dimension)}
	}

	// 记录日志（仅Accepted时捕获after状态）
	if u.Logger != nil {
		var afterJSON string
		if result.Accepted {
			afterJSON = u.captureAfterState(proposal)
		}
		u.tryLog(proposal, result, beforeJSON, afterJSON)
	}

	return result
}

// validateProposal Layer 1: 基本合法性验证
func (u *SharedDimensionUpdater) validateProposal(p *MemoryUpdateProposal) error {
	if p.Dimension == "" {
		return fmt.Errorf("dimension is required (user/feedback/project/reference)")
	}
	if p.Action == "" {
		return fmt.Errorf("action is required (add/update/remove)")
	}
	if p.Reason == "" {
		return fmt.Errorf("reason is required — explain why this update matters")
	}
	reason := strings.TrimSpace(p.Reason)
	if len(reason) < 3 {
		return fmt.Errorf("reason too brief (%d chars, min 3) — briefly describe why this update is useful, e.g. 'user prefers concise responses'", len(reason))
	}
	// 注意：不再有通用词黑名单，agent 的任何合理理由都应被接受
	return nil
}

// ---- User Memory Updates ----

func (u *SharedDimensionUpdater) applyUserUpdate(proposal *MemoryUpdateProposal) UpdateResult {
	// 从proposal的Metadata中提取userID（由调用方注入）
	userID := u.extractStringMeta(proposal, "user_id")
	if userID == "" {
		return UpdateResult{Accepted: false, Reason: "❌ user_id is required in metadata"}
	}

	payload := new(UserMemoryUpdatePayload)
	validFields := ValidFieldsForDimension(DimUser)
	if result := u.parsePayloadStrict(proposal.Payload, payload, validFields); !result.Success {
		msg := u.buildPayloadErrorMsg("user", result)
		return UpdateResult{Accepted: false, Reason: msg}
	}

	current, err := u.store.GetUserMemory(userID)
	if err != nil {
		return UpdateResult{Accepted: false, Reason: fmt.Sprintf("❌ Read error: %v", err)}
	}

	changed := false

	// 合并Profile
	if payload.Profile != nil {
		if payload.Profile.Name != "" && current.Profile.Name != payload.Profile.Name {
			current.Profile.Name = payload.Profile.Name
			changed = true
		}
		if payload.Profile.Role != "" && current.Profile.Role != payload.Profile.Role {
			current.Profile.Role = payload.Profile.Role
			changed = true
		}
		if payload.Profile.Team != "" && current.Profile.Team != payload.Profile.Team {
			current.Profile.Team = payload.Profile.Team
			changed = true
		}
		if payload.Profile.Seniority != "" && current.Profile.Seniority != payload.Profile.Seniority {
			current.Profile.Seniority = payload.Profile.Seniority
			changed = true
		}
	}

	// 合并Expertise，去重
	if len(payload.Expertise) > 0 {
		existing := toSet(current.Expertise)
		for _, e := range payload.Expertise {
			if !existing[e] {
				if len(current.Expertise) >= u.policy.MaxUserExpertise {
					return UpdateResult{Accepted: false,
						Reason: fmt.Sprintf("❌ Expertise limit (%d) reached. Remove old entries first.", u.policy.MaxUserExpertise)}
				}
				current.Expertise = append(current.Expertise, e)
				existing[e] = true
				changed = true
			}
		}
	}

	// 合并Preferences
	if payload.Prefs != nil {
		if payload.Prefs.Language != "" && current.Prefs.Language != payload.Prefs.Language {
			current.Prefs.Language = payload.Prefs.Language
			changed = true
		}
		if payload.Prefs.DetailLevel != "" && current.Prefs.DetailLevel != payload.Prefs.DetailLevel {
			current.Prefs.DetailLevel = payload.Prefs.DetailLevel
			changed = true
		}
		if payload.Prefs.CodeStyle != "" && current.Prefs.CodeStyle != payload.Prefs.CodeStyle {
			current.Prefs.CodeStyle = payload.Prefs.CodeStyle
			changed = true
		}
		if payload.Prefs.ResponseFormat != "" && current.Prefs.ResponseFormat != payload.Prefs.ResponseFormat {
			current.Prefs.ResponseFormat = payload.Prefs.ResponseFormat
			changed = true
		}
		if len(payload.Prefs.Other) > 0 {
			if current.Prefs.Other == nil {
				current.Prefs.Other = make(map[string]string)
			}
			for k, v := range payload.Prefs.Other {
				if current.Prefs.Other[k] != v {
					if len(current.Prefs.Other) >= u.policy.MaxUserPrefsOther {
						return UpdateResult{Accepted: false,
							Reason: fmt.Sprintf("❌ Custom preferences limit (%d) reached.", u.policy.MaxUserPrefsOther)}
					}
					current.Prefs.Other[k] = v
					changed = true
				}
			}
		}
	}

	if !changed {
		return UpdateResult{Accepted: false, Reason: "⏭️ No new information — all proposed values already exist"}
	}

	current.Version++
	current.UpdatedAt = time.Now()
	current.UpdatedBy = proposal.ProposedBy

	if err := u.store.SetUserMemory(current); err != nil {
		return UpdateResult{Accepted: false, Reason: fmt.Sprintf("❌ Write error: %v", err)}
	}

	return UpdateResult{Accepted: true, Reason: "✅ User memory updated"}
}

// ---- Feedback Memory Updates ----

func (u *SharedDimensionUpdater) applyFeedbackUpdate(proposal *MemoryUpdateProposal) UpdateResult {
	userID := u.extractStringMeta(proposal, "user_id")
	if userID == "" {
		return UpdateResult{Accepted: false, Reason: "❌ user_id is required in metadata"}
	}

	payload := new(FeedbackMemoryUpdatePayload)
	validFields := ValidFieldsForDimension(DimFeedback)
	if result := u.parsePayloadStrict(proposal.Payload, payload, validFields); !result.Success {
		msg := u.buildPayloadErrorMsg("feedback", result)
		return UpdateResult{Accepted: false, Reason: msg}
	}

	current, err := u.store.GetFeedbackMemory(userID)
	if err != nil {
		return UpdateResult{Accepted: false, Reason: fmt.Sprintf("❌ Read error: %v", err)}
	}

	changed := false

	// 添加Correction
	if payload.Correction != nil {
		if u.isDuplicateCorrection(current.Corrections, payload.Correction) {
			return UpdateResult{Accepted: false, Reason: "⏭️ Similar correction already exists — avoid duplication"}
		}
		if len(current.Corrections) >= u.policy.MaxCorrections {
			current.Corrections = current.Corrections[1:] // FIFO淘汰
		}
		payload.Correction.ID = fmt.Sprintf("corr_%d", time.Now().UnixNano())
		payload.Correction.Timestamp = time.Now()
		current.Corrections = append(current.Corrections, *payload.Correction)
		changed = true

		// 检查是否能提炼为模式
		u.updateFeedbackPatterns(current, payload.Correction.Topic, payload.Correction.Wrong, payload.Correction.Correct)
	}

	// 添加Endorsement
	if payload.Endorsement != nil {
		if len(current.Endorsements) >= u.policy.MaxEndorsements {
			current.Endorsements = current.Endorsements[1:] // FIFO淘汰
		}
		payload.Endorsement.ID = fmt.Sprintf("endo_%d", time.Now().UnixNano())
		payload.Endorsement.Timestamp = time.Now()
		current.Endorsements = append(current.Endorsements, *payload.Endorsement)
		changed = true
	}

	if !changed {
		return UpdateResult{Accepted: false, Reason: "⏭️ No feedback data to add"}
	}

	current.Version++
	current.UpdatedAt = time.Now()

	if err := u.store.SetFeedbackMemory(current); err != nil {
		return UpdateResult{Accepted: false, Reason: fmt.Sprintf("❌ Write error: %v", err)}
	}

	return UpdateResult{Accepted: true, Reason: "✅ Feedback memory updated"}
}

// isDuplicateCorrection 检查是否已有相似的纠正记录（同Topic+同Wrong）
func (u *SharedDimensionUpdater) isDuplicateCorrection(existing []Correction, newCorr *Correction) bool {
	for _, c := range existing {
		if c.Topic == newCorr.Topic && strings.EqualFold(strings.TrimSpace(c.Wrong), strings.TrimSpace(newCorr.Wrong)) {
			return true
		}
	}
	return false
}

// updateFeedbackPatterns 从多个纠正中提炼反馈模式
func (u *SharedDimensionUpdater) updateFeedbackPatterns(m *FeedbackMemory, topic, wrong, correct string) {
	// 统计同Topic的纠正数量
	count := 0
	for _, c := range m.Corrections {
		if c.Topic == topic {
			count++
		}
	}

	if count < u.policy.MinPatternEvidence {
		return // 证据不足，不形成模式
	}

	// 生成模式描述
	patternDesc := fmt.Sprintf("[%s] user prefers %s over %s", topic, correct, wrong)

	// 检查是否已存在相似模式
	for i, p := range m.Patterns {
		if strings.Contains(strings.ToLower(p.Pattern), strings.ToLower(topic)) {
			// 更新已有模式
			m.Patterns[i].Pattern = patternDesc
			m.Patterns[i].Evidence = count
			m.Patterns[i].Confidence = float64(count) / float64(count+2)
			m.Patterns[i].LastSeen = time.Now()
			return
		}
	}

	// 添加新模式
	if len(m.Patterns) < u.policy.MaxFeedbackPatterns {
		m.Patterns = append(m.Patterns, FeedbackPattern{
			Pattern:    patternDesc,
			Evidence:   count,
			Confidence: float64(count) / float64(count+2),
			LastSeen:   time.Now(),
		})
	}
}

// ---- Project Memory Updates ----

func (u *SharedDimensionUpdater) applyProjectUpdate(proposal *MemoryUpdateProposal) UpdateResult {
	projectID := u.extractStringMeta(proposal, "project_id")
	if projectID == "" {
		return UpdateResult{Accepted: false, Reason: "❌ project_id is required in metadata"}
	}

	payload := new(ProjectMemoryUpdatePayload)
	validFields := ValidFieldsForDimension(DimProject)
	if result := u.parsePayloadStrict(proposal.Payload, payload, validFields); !result.Success {
		msg := u.buildPayloadErrorMsg("project", result)
		return UpdateResult{Accepted: false, Reason: msg}
	}

	current, err := u.store.GetProjectMemory(projectID)
	if err != nil {
		return UpdateResult{Accepted: false, Reason: fmt.Sprintf("❌ Read error: %v", err)}
	}

	changed := false

	// 更新Status
	if payload.Status != nil && *payload.Status != current.Status {
		current.Status = *payload.Status
		changed = true
	}

	// 添加/更新Objective
	if payload.Objective != nil {
		if payload.Objective.ID == "" {
			payload.Objective.ID = fmt.Sprintf("obj_%d", time.Now().UnixNano())
		}
		found := false
		for i, o := range current.Objectives {
			if o.ID == payload.Objective.ID {
				current.Objectives[i] = *payload.Objective
				found = true
				changed = true
				break
			}
		}
		if !found {
			// 检查活跃目标数量
			activeCount := 0
			for _, o := range current.Objectives {
				if o.Status == "active" || o.Status == "" {
					activeCount++
				}
			}
			if activeCount >= u.policy.MaxActiveObjectives {
				return UpdateResult{Accepted: false,
					Reason: fmt.Sprintf("❌ Active objectives limit (%d) reached. Complete or drop existing ones first.", u.policy.MaxActiveObjectives)}
			}
			current.Objectives = append(current.Objectives, *payload.Objective)
			changed = true
		}
	}

	// 添加/更新Team Member
	if payload.Member != nil {
		found := false
		for i, m := range current.Team {
			if m.Name == payload.Member.Name {
				current.Team[i] = *payload.Member
				found = true
				changed = true
				break
			}
		}
		if !found {
			if len(current.Team) >= u.policy.MaxTeamMembers {
				return UpdateResult{Accepted: false,
					Reason: fmt.Sprintf("❌ Team members limit (%d) reached.", u.policy.MaxTeamMembers)}
			}
			current.Team = append(current.Team, *payload.Member)
			changed = true
		}
	}

	// 添加Deadline
	if payload.Deadline != nil {
		if len(current.Deadlines) >= u.policy.MaxDeadlines {
			current.Deadlines = current.Deadlines[1:] // FIFO淘汰
		}
		current.Deadlines = append(current.Deadlines, *payload.Deadline)
		changed = true
	}

	if !changed {
		return UpdateResult{Accepted: false, Reason: "⏭️ No new project information"}
	}

	current.Version++
	current.UpdatedAt = time.Now()
	current.UpdatedBy = proposal.ProposedBy

	if err := u.store.SetProjectMemory(current); err != nil {
		return UpdateResult{Accepted: false, Reason: fmt.Sprintf("❌ Write error: %v", err)}
	}

	return UpdateResult{Accepted: true, Reason: "✅ Project memory updated"}
}

// ---- Reference Memory Updates ----

func (u *SharedDimensionUpdater) applyReferenceUpdate(proposal *MemoryUpdateProposal) UpdateResult {
	projectID := u.extractStringMeta(proposal, "project_id")
	if projectID == "" {
		return UpdateResult{Accepted: false, Reason: "❌ project_id is required in metadata"}
	}

	payload := new(ReferenceMemoryUpdatePayload)
	validFields := ValidFieldsForDimension(DimReference)
	if result := u.parsePayloadStrict(proposal.Payload, payload, validFields); !result.Success {
		msg := u.buildPayloadErrorMsg("reference", result)
		return UpdateResult{Accepted: false, Reason: msg}
	}

	current, err := u.store.GetReferenceMemory(projectID)
	if err != nil {
		return UpdateResult{Accepted: false, Reason: fmt.Sprintf("❌ Read error: %v", err)}
	}

	changed := false

	// 删除资源
	if payload.RemoveByID != "" {
		for i, r := range current.Resources {
			if r.ID == payload.RemoveByID {
				current.Resources = append(current.Resources[:i], current.Resources[i+1:]...)
				changed = true
				break
			}
		}
	}

	// 添加资源
	if payload.Resource != nil {
		// 检查是否已存在同Location的资源
		for _, r := range current.Resources {
			if r.Location == payload.Resource.Location {
				return UpdateResult{Accepted: false, Reason: "⏭️ Resource with same location already exists"}
			}
		}
		if len(current.Resources) >= u.policy.MaxResources {
			return UpdateResult{Accepted: false,
				Reason: fmt.Sprintf("❌ Resources limit (%d) reached. Remove unused ones first.", u.policy.MaxResources)}
		}
		payload.Resource.ID = fmt.Sprintf("res_%d", time.Now().UnixNano())
		current.Resources = append(current.Resources, *payload.Resource)
		changed = true
	}

	if !changed {
		return UpdateResult{Accepted: false, Reason: "⏭️ No reference changes applied"}
	}

	current.Version++
	current.UpdatedAt = time.Now()

	if err := u.store.SetReferenceMemory(current); err != nil {
		return UpdateResult{Accepted: false, Reason: fmt.Sprintf("❌ Write error: %v", err)}
	}

	return UpdateResult{Accepted: true, Reason: "✅ Reference memory updated"}
}

// ============================================================
// Helper methods
// ============================================================

// extractStringMeta 从proposal的Metadata中提取字符串值
func (u *SharedDimensionUpdater) extractStringMeta(proposal *MemoryUpdateProposal, key string) string {
	if proposal.Metadata == nil {
		return ""
	}
	if v, ok := proposal.Metadata[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// ParsePayloadResult 解析结果
type ParsePayloadResult struct {
	Success       bool
	UnknownFields []string
	ValidFields   []string
	TypeErrors    []string
	Err           error
}

// parsePayloadStrict 严格解析 payload，检测未知字段和类型错误
// validFields 为合法字段列表，传 nil 则不检查未知字段
func (u *SharedDimensionUpdater) parsePayloadStrict(payload interface{}, target interface{}, validFields []string) *ParsePayloadResult {
	result := &ParsePayloadResult{Success: false}

	// 情况1：已经是目标类型
	if reflect.TypeOf(payload).AssignableTo(reflect.TypeOf(target)) {
		reflect.ValueOf(target).Elem().Set(reflect.ValueOf(payload))
		result.Success = true
		return result
	}

	// 情况2：是 map（来自LLM工具调用）
	dataMap, ok := payload.(map[string]interface{})
	if !ok {
		result.Err = fmt.Errorf("payload must be a JSON object")
		return result
	}

	// 检测未知字段
	if len(validFields) > 0 {
		validSet := make(map[string]bool, len(validFields))
		for _, f := range validFields {
			validSet[f] = true
		}
		for k := range dataMap {
			if !validSet[k] {
				result.UnknownFields = append(result.UnknownFields, k)
			}
		}
	}

	// 使用 DisallowUnknownFields 严格反序列化
	data, err := json.Marshal(payload)
	if err != nil {
		result.Err = fmt.Errorf("failed to marshal payload: %w", err)
		return result
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		// 提取更友好的错误信息
		errStr := err.Error()
		if strings.Contains(errStr, "unknown field") {
			// 从错误中提取字段名
			parts := strings.Split(errStr, "\"")
			if len(parts) >= 2 {
				unknownField := parts[1]
				result.UnknownFields = append(result.UnknownFields, unknownField)
			}
		} else if strings.Contains(errStr, "cannot unmarshal") {
			result.TypeErrors = append(result.TypeErrors, errStr)
		}
		result.Err = err
		return result
	}

	result.Success = true
	return result
}

// parsePayload 将payload从interface{}解析为具体类型（保留旧方法用于向后兼容）
// 兼容两种情况：已经是类型化的struct / 来自JSON反序列化的map
// 注意：由于Go泛型方法限制，使用反射+JSON marshal/unmarshal实现
func (u *SharedDimensionUpdater) parsePayload(payload interface{}, target interface{}) bool {
	// 情况1：已经是目标类型
	if reflect.TypeOf(payload).AssignableTo(reflect.TypeOf(target)) {
		// 将值复制到目标指针指向的内存
		reflect.ValueOf(target).Elem().Set(reflect.ValueOf(payload))
		return true
	}
	// 情况2：是map（来自LLM工具调用的JSON解析后）
	if _, ok := payload.(map[string]interface{}); ok {
		data, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		if err := json.Unmarshal(data, target); err != nil {
			return false
		}
		return true
	}
	return false
}

// ValidFieldsForDimension 返回指定维度的合法字段列表
// 用于 parsePayloadStrict 的 validFields 参数
func ValidFieldsForDimension(dim Dimension) []string {
	switch dim {
	case DimUser:
		return []string{"profile", "expertise", "preferences"}
	case DimFeedback:
		return []string{"correction", "endorsement"}
	case DimProject:
		return []string{"objective", "member", "deadline", "status"}
	case DimReference:
		return []string{"resource", "remove_by_id"}
	default:
		return nil
	}
}

// buildPayloadErrorMsg 从 ParsePayloadResult 生成友好的错误信息
// 帮助 agent 理解 payload 格式错误并自行修正
func (u *SharedDimensionUpdater) buildPayloadErrorMsg(dimName string, result *ParsePayloadResult) string {
	var parts []string
	parts = append(parts, fmt.Sprintf("❌ Invalid payload format for %s dimension.", dimName))

	if len(result.UnknownFields) > 0 {
		parts = append(parts, fmt.Sprintf("Unknown field(s): %s.", strings.Join(result.UnknownFields, ", ")))
		validFields := ValidFieldsForDimension(Dimension(dimName))
		if len(validFields) > 0 {
			parts = append(parts, fmt.Sprintf("Available fields for %s: %s.", dimName, strings.Join(validFields, ", ")))
		}
	}

	if len(result.TypeErrors) > 0 {
		for _, te := range result.TypeErrors {
			// 简化类型错误信息
			simplified := strings.ReplaceAll(te, "json: cannot unmarshal", "wrong type")
			simplified = strings.ReplaceAll(simplified, "into Go struct field", "for field")
			parts = append(parts, simplified)
		}
		parts = append(parts, "Check field types in memory_query_schema or the tool description.")
	}

	if result.Err != nil && len(result.UnknownFields) == 0 && len(result.TypeErrors) == 0 {
		parts = append(parts, fmt.Sprintf("Error: %s", result.Err))
	}

	parts = append(parts, "Tip: Use proper field names and types. Each field expects specific data (string, array, or object).")
	return strings.Join(parts, " ")
}

// toSet 将字符串切片转为去重集合
func toSet(slice []string) map[string]bool {
	m := make(map[string]bool, len(slice))
	for _, s := range slice {
		m[s] = true
	}
	return m
}

// ============================================================
// MemoryUpdateProposal — 更新提议结构
// ============================================================

// MemoryUpdateProposal Agent向共享记忆系统提交的更新提议
type MemoryUpdateProposal struct {
	Dimension  Dimension              `json:"dimension"`
	Action     UpdateAction           `json:"action"`
	Payload    interface{}            `json:"payload"`
	Reason     string                 `json:"reason"`
	ProposedBy string                 `json:"proposed_by"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"` // 携带user_id/project_id等上下文
}

// tryLog 尝试记录更新日志（Logger为nil时跳过）
func (u *SharedDimensionUpdater) tryLog(proposal *MemoryUpdateProposal, result UpdateResult, beforeJSON, afterJSON string) {
	if u.Logger != nil {
		u.Logger.LogUpdate(proposal, result, beforeJSON, afterJSON)
	}
}

// captureBeforeState 捕获更新前的记忆状态（JSON格式）
func (u *SharedDimensionUpdater) captureBeforeState(proposal *MemoryUpdateProposal) string {
	userID := u.extractStringMeta(proposal, "user_id")
	projectID := u.extractStringMeta(proposal, "project_id")
	if userID == "" && projectID == "" {
		return ""
	}

	switch proposal.Dimension {
	case DimUser:
		if mem, err := u.store.GetUserMemory(userID); err == nil {
			return mustMarshalJSON(mem)
		}
	case DimFeedback:
		if mem, err := u.store.GetFeedbackMemory(userID); err == nil {
			return mustMarshalJSON(mem)
		}
	case DimProject:
		if mem, err := u.store.GetProjectMemory(projectID); err == nil {
			return mustMarshalJSON(mem)
		}
	case DimReference:
		if mem, err := u.store.GetReferenceMemory(projectID); err == nil {
			return mustMarshalJSON(mem)
		}
	}
	return ""
}

// captureAfterState 捕获更新后的记忆状态（JSON格式）
func (u *SharedDimensionUpdater) captureAfterState(proposal *MemoryUpdateProposal) string {
	userID := u.extractStringMeta(proposal, "user_id")
	projectID := u.extractStringMeta(proposal, "project_id")
	if userID == "" && projectID == "" {
		return ""
	}

	switch proposal.Dimension {
	case DimUser:
		if mem, err := u.store.GetUserMemory(userID); err == nil {
			return mustMarshalJSON(mem)
		}
	case DimFeedback:
		if mem, err := u.store.GetFeedbackMemory(userID); err == nil {
			return mustMarshalJSON(mem)
		}
	case DimProject:
		if mem, err := u.store.GetProjectMemory(projectID); err == nil {
			return mustMarshalJSON(mem)
		}
	case DimReference:
		if mem, err := u.store.GetReferenceMemory(projectID); err == nil {
			return mustMarshalJSON(mem)
		}
	}
	return ""
}
