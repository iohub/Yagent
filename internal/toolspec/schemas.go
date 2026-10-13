package toolspec

import (
	_ "embed"
	"encoding/json"
	"sort"
	"sync"
)

// schemas.json 是 description + parameters 的单一事实来源。
// 它从 internal/agents/tools.json 搬来（D1）：注册表启动即与 Spec 集合双向对账，
// "声明了但没人实现"和"实现了但没声明"都不再可能静默存在。
//
//go:embed schemas.json
var schemasJSON []byte

// Schema 是一条工具声明里属于模型接口的部分。
type Schema struct {
	Name        string
	Description string
	Parameters  map[string]interface{}
}

// HasProperty 报告 parameters.properties 里是否存在该参数名。
func (s Schema) HasProperty(name string) bool {
	props, ok := s.Parameters["properties"].(map[string]interface{})
	if !ok {
		return false
	}
	_, ok = props[name]
	return ok
}

// Required 返回 parameters.required 列表（缺失时为空）。
func (s Schema) Required() []string {
	raw, ok := s.Parameters["required"].([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if str, ok := v.(string); ok {
			out = append(out, str)
		}
	}
	return out
}

var (
	schemaOnce sync.Once
	schemaMap  map[string]Schema
	schemaErr  error
)

// errBlankName / errDuplicateName 是 schemas.json 自身的结构错误。
// 声明文件坏掉时必须响，而不是让某个 Agent 安静地少一个工具。
var (
	errBlankName = jsonError("schemas.json: 存在 name 为空的条目")
)

type jsonError string

func (e jsonError) Error() string { return string(e) }

type duplicateNameError struct{ name string }

func (e duplicateNameError) Error() string {
	return "schemas.json: 工具名重复出现：" + e.name
}

func errDuplicateName(name string) error { return duplicateNameError{name: name} }

// loadSchemas 解析嵌入的 schemas.json。解析失败时返回错误而不是 panic：
// 调用方（Validate / 装配自检）会把它升级成启动失败或测试失败。
func loadSchemas() (map[string]Schema, error) {
	schemaOnce.Do(func() {
		var entries []Schema
		if err := json.Unmarshal(schemasJSON, &entries); err != nil {
			schemaErr = err
			return
		}
		schemaMap = make(map[string]Schema, len(entries))
		for _, e := range entries {
			if e.Name == "" {
				schemaErr = errBlankName
				return
			}
			if _, dup := schemaMap[e.Name]; dup {
				schemaErr = errDuplicateName(e.Name)
				return
			}
			schemaMap[e.Name] = e
		}
	})
	return schemaMap, schemaErr
}

// SchemasJSON 返回嵌入的声明原文，供尚未迁走的老调用点复用
// （internal/agents/tools.go 目前把它当作 ToolsJSON 的来源）。
func SchemasJSON() []byte { return schemasJSON }

// GetSchema 按名查找声明。
func GetSchema(name string) (Schema, bool) {
	m, err := loadSchemas()
	if err != nil || m == nil {
		return Schema{}, false
	}
	s, ok := m[name]
	return s, ok
}

// SchemaNames 返回 schemas.json 里的全部名字（排序后）。
func SchemaNames() []string {
	m, err := loadSchemas()
	if err != nil || m == nil {
		return nil
	}
	out := make([]string, 0, len(m))
	for name := range m {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// SchemasError 暴露解析错误，供启动自检与测试断言。
func SchemasError() error {
	_, err := loadSchemas()
	return err
}

// Description 返回模型可见描述：Spec.Describe 优先（能力降级等动态文案），
// 否则用 schemas.json 的静态文案。
func (s *Spec) Description(deps *Deps) string {
	if s.Describe != nil {
		if d := s.Describe(deps); d != "" {
			return d
		}
	}
	if sc, ok := GetSchema(s.Name); ok {
		return sc.Description
	}
	return s.Name
}

// Parameters 返回模型可见的参数 schema。缺失时为空对象（工具会声称无参数），
// Validate() 会把"有 Spec 无声明"报成致命问题，所以正常路径不会走到这里返回 nil。
func (s *Spec) Parameters() map[string]interface{} {
	if sc, ok := GetSchema(s.Name); ok {
		return sc.Parameters
	}
	return nil
}
