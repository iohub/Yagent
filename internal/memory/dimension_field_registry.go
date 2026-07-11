package memory

import (
	"encoding/json"
	"fmt"
	"strings"
)

// FieldType 表示字段值的类型
type FieldType string

const (
	FieldTypeScalar FieldType = "scalar" // 标量值（string/number/bool）
	FieldTypeArray  FieldType = "array"  // 数组类型
	FieldTypeMap    FieldType = "map"    // map 类型
)

// FieldSchema 描述每个字段的元信息
type FieldSchema struct {
	Type         FieldType  `json:"type"`
	ItemType     string     `json:"item_type,omitempty"`     // 数组项类型: "string" / "object"
	ValidActions []string   `json:"valid_actions"`           // 支持的 action: set/add/remove
	Description  string     `json:"description"`
	Example      string     `json:"example"`
	LegacyNames  []string   `json:"legacy_names,omitempty"`  // 旧字段名（自动纠正用）
}

// DimensionSchema 描述一个维度的所有字段
type DimensionSchema struct {
	Dimension string                  `json:"dimension"`
	Fields    map[string]*FieldSchema `json:"fields"`
}

// DimensionFieldRegistry 维度字段注册表
type DimensionFieldRegistry struct {
	dimensions map[string]*DimensionSchema
	// legacyMap[dimension][legacyName] = canonicalName
	legacyMap map[string]map[string]string
}

// NewDimensionFieldRegistry 创建注册表
func NewDimensionFieldRegistry() *DimensionFieldRegistry {
	r := &DimensionFieldRegistry{
		dimensions: make(map[string]*DimensionSchema),
		legacyMap:  make(map[string]map[string]string),
	}
	r.registerAll()
	return r
}

// registerAll 注册所有维度的字段定义
func (r *DimensionFieldRegistry) registerAll() {
	// ============ User 维度 ============
	r.dimensions["user"] = &DimensionSchema{
		Dimension: "user",
		Fields: map[string]*FieldSchema{
			"name": {
				Type: FieldTypeScalar, ItemType: "string",
				ValidActions: []string{"set"},
				Description:  "User's display name",
				Example:      "John Doe",
			},
			"role": {
				Type: FieldTypeScalar, ItemType: "string",
				ValidActions: []string{"set"},
				Description:  "Job role",
				Example:      "Senior Engineer",
			},
			"team": {
				Type: FieldTypeScalar, ItemType: "string",
				ValidActions: []string{"set"},
				Description:  "Team name",
				Example:      "Backend",
			},
			"seniority": {
				Type: FieldTypeScalar, ItemType: "string",
				ValidActions: []string{"set"},
				Description:  "Seniority level",
				Example:      "senior",
			},
			"expertise": {
				Type: FieldTypeArray, ItemType: "string",
				ValidActions: []string{"add", "remove", "set"},
				Description:  "Areas of expertise",
				Example:      "Go",
			},
			"language": {
				Type: FieldTypeScalar, ItemType: "string",
				ValidActions: []string{"set"},
				Description:  "Preferred language code",
				Example:      "zh-CN",
			},
			"detail_level": {
				Type: FieldTypeScalar, ItemType: "string",
				ValidActions: []string{"set"},
				Description:  "Desired response detail level",
				Example:      "detailed",
			},
			"code_style": {
				Type: FieldTypeScalar, ItemType: "string",
				ValidActions: []string{"set"},
				Description:  "Preferred code style",
				Example:      "verbose with comments",
			},
			"response_format": {
				Type: FieldTypeScalar, ItemType: "string",
				ValidActions: []string{"set"},
				Description:  "Preferred response format",
				Example:      "markdown",
			},
			"metadata": {
				Type: FieldTypeMap,
				ValidActions: []string{"set", "remove"},
				Description:  "Additional key-value metadata",
				Example:      `{"theme": "dark", "timezone": "UTC+8"}`,
			},
		},
	}

	// ============ Feedback 维度 ============
	r.dimensions["feedback"] = &DimensionSchema{
		Dimension: "feedback",
		Fields: map[string]*FieldSchema{
			"corrections": {
				Type: FieldTypeArray, ItemType: "object",
				ValidActions: []string{"add", "remove"},
				Description:  "User corrections — add a single correction object",
				Example:      `{"topic":"code-style","wrong":"tabs","correct":"spaces","context":"python project"}`,
				LegacyNames:  []string{"correction"},
			},
			"endorsements": {
				Type: FieldTypeArray, ItemType: "object",
				ValidActions: []string{"add", "remove"},
				Description:  "User endorsements — add a single endorsement object",
				Example:      `{"topic":"architecture","approach":"prefers composition over inheritance"}`,
				LegacyNames:  []string{"endorsement"},
			},
		},
	}

	// ============ Project 维度 ============
	r.dimensions["project"] = &DimensionSchema{
		Dimension: "project",
		Fields: map[string]*FieldSchema{
			"status": {
				Type: FieldTypeScalar, ItemType: "string",
				ValidActions: []string{"set"},
				Description:  "Project current status",
				Example:      "Implementing auth module",
			},
			"objectives": {
				Type: FieldTypeArray, ItemType: "object",
				ValidActions: []string{"add", "remove", "set"},
				Description:  "Project objectives — add a single objective or set full list",
				Example:      `{"description":"Finish API","priority":"high","status":"active"}`,
				LegacyNames:  []string{"objective"},
			},
			"team": {
				Type: FieldTypeArray, ItemType: "object",
				ValidActions: []string{"add", "remove", "set"},
				Description:  "Project team members — add a single member or set full list",
				Example:      `{"name":"Alice","role":"Engineer","responsibility":"Backend"}`,
				LegacyNames:  []string{"member"},
			},
			"deadlines": {
				Type: FieldTypeArray, ItemType: "object",
				ValidActions: []string{"add", "remove", "set"},
				Description:  "Project deadlines — add a single deadline or set full list",
				Example:      `{"description":"Beta release","date":"2025-06-01","priority":"high"}`,
				LegacyNames:  []string{"deadline"},
			},
		},
	}

	// ============ Reference 维度 ============
	r.dimensions["reference"] = &DimensionSchema{
		Dimension: "reference",
		Fields: map[string]*FieldSchema{
			"resources": {
				Type: FieldTypeArray, ItemType: "object",
				ValidActions: []string{"add", "remove"},
				Description:  "Reference resources — add a single resource object",
				Example:      `{"name":"API Docs","category":"docs","location":"https://example.com/docs","description":"API reference","tags":["api","docs"]}`,
				LegacyNames:  []string{"resource"},
			},
		},
	}

	// 构建 legacy map（旧字段名 → 规范字段名）
	for dim, schema := range r.dimensions {
		r.legacyMap[dim] = make(map[string]string)
		for canonical, field := range schema.Fields {
			for _, legacy := range field.LegacyNames {
				r.legacyMap[dim][legacy] = canonical
			}
		}
	}
}

// ============================================================
// Validation Result Types
// ============================================================

// ValidateResult 验证结果
type ValidateResult struct {
	Valid       bool               `json:"valid"`
	Corrected   bool               `json:"corrected,omitempty"`
	Corrections []FieldCorrection  `json:"corrections,omitempty"`
	Errors      []FieldError       `json:"errors,omitempty"`
	Resolved    *ResolvedUpdate    `json:"resolved,omitempty"`
}

// FieldCorrection 字段名自动纠正记录
type FieldCorrection struct {
	Original  string `json:"original"`
	Corrected string `json:"corrected"`
	Reason    string `json:"reason"`
}

// FieldError 验证错误
type FieldError struct {
	Code    string `json:"code"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
	Hint    string `json:"hint"`
}

// ResolvedUpdate 验证通过后的解析结果
type ResolvedUpdate struct {
	Dimension string      `json:"dimension"`
	Field     string      `json:"field"`
	Action    string      `json:"action"`
	Value     interface{} `json:"value,omitempty"`
	ItemID    string      `json:"item_id,omitempty"`
}

// Validate 验证更新请求，包含自动纠正
// dimension: 维度名
// field: 字段名（支持旧名称自动纠正）
// action: 操作类型（set/add/remove）
// value: 值
// itemID: 删除时的 item ID
func (r *DimensionFieldRegistry) Validate(dimension, field, action string, value interface{}, itemID string) *ValidateResult {
	result := &ValidateResult{}

	// 1. 检查维度
	dimSchema, ok := r.dimensions[dimension]
	if !ok {
		result.Errors = append(result.Errors, FieldError{
			Code:    "INVALID_DIMENSION",
			Message: fmt.Sprintf("Unknown dimension: '%s'", dimension),
			Hint:    "Valid dimensions: user, feedback, project, reference",
		})
		return result
	}

	// 2. 检查字段名 + 自动纠正
	resolvedField := field
	fieldSchema, exists := dimSchema.Fields[field]
	if !exists {
		// 检查是否为旧名称
		if canonical, isLegacy := r.legacyMap[dimension][field]; isLegacy {
			resolvedField = canonical
			fieldSchema = dimSchema.Fields[canonical]
			result.Corrected = true
			result.Corrections = append(result.Corrections, FieldCorrection{
				Original:  field,
				Corrected: canonical,
				Reason:    fmt.Sprintf("'%s' is deprecated for dimension '%s'. Use '%s' instead.", field, dimension, canonical),
			})
		} else {
			// 无效字段
			validFields := make([]string, 0, len(dimSchema.Fields))
			for k := range dimSchema.Fields {
				validFields = append(validFields, k)
			}
			result.Errors = append(result.Errors, FieldError{
				Code:  "INVALID_FIELD",
				Field: field,
				Message: fmt.Sprintf("Field '%s' is not valid for dimension '%s'", field, dimension),
				Hint:   fmt.Sprintf("Valid fields for '%s': %s", dimension, strings.Join(validFields, ", ")),
			})
			return result
		}
	}

	// 3. 检查 action 兼容性
	actionValid := false
	for _, validAction := range fieldSchema.ValidActions {
		if action == validAction {
			actionValid = true
			break
		}
	}
	if !actionValid {
		result.Errors = append(result.Errors, FieldError{
			Code:  "INVALID_ACTION",
			Field: resolvedField,
			Message: fmt.Sprintf("Action '%s' is not valid for field '%s' (type: %s)", action, resolvedField, fieldSchema.Type),
			Hint:   fmt.Sprintf("Valid actions for '%s': %s", resolvedField, strings.Join(fieldSchema.ValidActions, ", ")),
		})
		return result
	}

	// 4. 对于 remove 操作，需要 item_id（对于标量或 map 类型，remove 不需要 value）
	if action == "remove" && fieldSchema.Type == FieldTypeArray && itemID == "" {
		result.Errors = append(result.Errors, FieldError{
			Code:  "MISSING_ITEM_ID",
			Field: resolvedField,
			Message: fmt.Sprintf("Action 'remove' on array field '%s' requires an item_id", resolvedField),
			Hint:   "Provide the item_id of the array element to remove",
		})
		return result
	}

	result.Valid = true
	result.Resolved = &ResolvedUpdate{
		Dimension: dimension,
		Field:     resolvedField,
		Action:    action,
		Value:     value,
		ItemID:    itemID,
	}
	return result
}

// GetDimensionSchema 获取指定维度的 schema
func (r *DimensionFieldRegistry) GetDimensionSchema(dimension string) *DimensionSchema {
	return r.dimensions[dimension]
}

// GetAllDimensions 获取所有维度名
func (r *DimensionFieldRegistry) GetAllDimensions() []string {
	dims := make([]string, 0, len(r.dimensions))
	for d := range r.dimensions {
		dims = append(dims, d)
	}
	return dims
}

// GetValidFields 获取指定维度的合法字段列表
func (r *DimensionFieldRegistry) GetValidFields(dimension string) []string {
	dimSchema, ok := r.dimensions[dimension]
	if !ok {
		return nil
	}
	fields := make([]string, 0, len(dimSchema.Fields))
	for k := range dimSchema.Fields {
		fields = append(fields, k)
	}
	return fields
}

// FormatSchemaAsJSON 将维度 schema 格式化为 JSON 字符串（用于 memory_query_schema 工具）
func (r *DimensionFieldRegistry) FormatSchemaAsJSON(dimension string) string {
	dimSchema, ok := r.dimensions[dimension]
	if !ok {
		return ""
	}
	data, err := json.MarshalIndent(dimSchema, "", "  ")
	if err != nil {
		return ""
	}
	return string(data)
}
