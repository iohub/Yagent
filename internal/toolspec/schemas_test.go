package toolspec

import (
	"strings"
	"testing"
)

// Spec.Inline 是动态规格（外部 MCP 工具）自带接口声明的通道。
// 三档优先级必须是 Describe > Inline > schemas.json：
// 写反了的表现是外部工具的 schema 被一张不存在的静态声明顶掉，
// 而静态工具的文案又会被一张空 Inline 改掉 —— 两种都只在运行期才看得出来。
func TestInlineSchemaBeatsStaticLookup(t *testing.T) {
	dynamic := &Spec{
		Name: "mcp__probe__echo",
		Inline: &Schema{
			Name:        "mcp__probe__echo",
			Description: "Echo the arguments back.",
			Parameters:  map[string]interface{}{"type": "object", "properties": map[string]interface{}{"text": map[string]interface{}{"type": "string"}}},
		},
	}
	if got := dynamic.Description(nil); got != "Echo the arguments back." {
		t.Errorf("Description() = %q, want Inline 的文案（外部工具不在 schemas.json 里）", got)
	}
	params := dynamic.Parameters()
	if params == nil {
		t.Fatal("Parameters() = nil, want Inline.Parameters")
	}
	if _, ok := params["properties"]; !ok {
		t.Errorf("Parameters() = %v, want Inline 里那份带 properties 的声明", params)
	}

	// 静态路径必须逐字不变：Inline 为 nil 时 Description/Parameters 完全来自 schemas.json。
	static := &Spec{Name: "read_file"}
	wantSchema, ok := GetSchema("read_file")
	if !ok {
		t.Fatal("read_file 不在 schemas.json 里，夹具失效")
	}
	if got := static.Description(nil); got != wantSchema.Description {
		t.Errorf("静态 Description() 漂移：got %q, want schemas.json 原文", got)
	}
	if static.Parameters() == nil {
		t.Error("静态 Parameters() = nil, want schemas.json 的 parameters")
	}

	// Describe 钩子压过 Inline（能力降级文案属于运行期事实，比任何声明都新）。
	withHook := *dynamic
	withHook.Describe = func(*Deps) string { return "降级后的说明" }
	if got := withHook.Description(nil); got != "降级后的说明" {
		t.Errorf("Description() = %q, want Describe 钩子的文案", got)
	}

	// 都缺失时回落工具名：模型至少能看见一个非空标识，而不是空描述。
	bare := &Spec{Name: "no_such_tool_anywhere"}
	if got := bare.Description(nil); got != bare.Name || strings.TrimSpace(got) == "" {
		t.Errorf("Description() = %q, want 回落工具名 %q", got, bare.Name)
	}
	if bare.Parameters() != nil {
		t.Errorf("Parameters() = %v, want nil（没有声明就是没有）", bare.Parameters())
	}
}
