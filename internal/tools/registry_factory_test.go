package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// makeDef 构造测试用 ToolDefinition（模拟 tools.json 中的一项）
func makeDef(name string) ToolDefinition {
	return ToolDefinition{
		Name:        name,
		Description: "test tool " + name,
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"input": map[string]interface{}{
					"type":        "string",
					"description": "input for " + name,
				},
			},
		},
	}
}

// okFactory 测试用工厂：正常生成 Adapter，schema 取自 def.Parameters
func okFactory(def ToolDefinition) (*Adapter, error) {
	return NewAdapter(def.Name, def.Description, func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		return def.Name, nil
	}).WithSchema(def.Parameters), nil
}

// disabledFactory 测试用工厂：返回 (nil, nil) 表示工具被禁用
func disabledFactory(def ToolDefinition) (*Adapter, error) {
	return nil, nil
}

// failingFactory 测试用工厂：返回装配错误
func failingFactory(def ToolDefinition) (*Adapter, error) {
	return nil, fmt.Errorf("assemble %s failed", def.Name)
}

// TestRegisterFactory 验证 RegisterFactory 成功注册与重复注册报错
func TestRegisterFactory(t *testing.T) {
	r := NewRegistry()

	if err := r.RegisterFactory("read_file", okFactory); err != nil {
		t.Fatalf("Expected no error on first RegisterFactory, got %v", err)
	}

	// 重复注册应返回错误（风格同 Register）
	err := r.RegisterFactory("read_file", okFactory)
	if err == nil {
		t.Fatal("Expected error on duplicate RegisterFactory, got nil")
	}
	if !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("Expected 'already registered' in error, got %q", err.Error())
	}
}

// TestMustRegisterFactory 验证正常注册成功、冲突 panic
func TestMustRegisterFactory(t *testing.T) {
	r := NewRegistry()

	r.MustRegisterFactory("tool_a", okFactory) // 不应 panic

	defer func() {
		if rec := recover(); rec == nil {
			t.Fatal("Expected panic on duplicate MustRegisterFactory, got none")
		}
	}()
	r.MustRegisterFactory("tool_a", okFactory)
}

// TestBuildFactoryBuilt 验证工厂已注册时 Build 生成正确的 Adapter
func TestBuildFactoryBuilt(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterFactory("read_file", okFactory); err != nil {
		t.Fatalf("RegisterFactory failed: %v", err)
	}

	def := makeDef("read_file")
	built, err := r.Build([]ToolDefinition{def})
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
	if len(built) != 1 {
		t.Fatalf("Expected 1 built adapter, got %d", len(built))
	}

	adapter := built[0]
	if adapter.Name() != def.Name {
		t.Fatalf("Expected name %q, got %q", def.Name, adapter.Name())
	}
	if adapter.Description() != def.Description {
		t.Fatalf("Expected description %q, got %q", def.Description, adapter.Description())
	}
	// WithSchema(def.Parameters) 应正确生效（包内测试直接访问私有字段）
	if adapter.schema == nil || len(adapter.schema) != len(def.Parameters) {
		t.Fatalf("Expected schema to equal def.Parameters, got %v", adapter.schema)
	}
	if fmt.Sprint(adapter.schema) != fmt.Sprint(def.Parameters) {
		t.Fatalf("Expected schema %v, got %v", def.Parameters, adapter.schema)
	}
}

// TestBuildSkipsUnregistered 验证工厂未注册时 Build 跳过该工具
func TestBuildSkipsUnregistered(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterFactory("read_file", okFactory); err != nil {
		t.Fatalf("RegisterFactory failed: %v", err)
	}

	// "run_bash" 无工厂 → 跳过
	built, err := r.Build([]ToolDefinition{makeDef("read_file"), makeDef("run_bash")})
	if err != nil {
		t.Fatalf("Expected no error (unregistered should be skipped), got %v", err)
	}
	if len(built) != 1 || built[0].Name() != "read_file" {
		t.Fatalf("Expected only [read_file], got %v", built)
	}
}

// TestBuildSkipsDisabled 验证工厂返回 (nil, nil)（禁用）时 Build 跳过该工具
func TestBuildSkipsDisabled(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterFactory("ask_user_for_help", disabledFactory); err != nil {
		t.Fatalf("RegisterFactory failed: %v", err)
	}
	if err := r.RegisterFactory("read_file", okFactory); err != nil {
		t.Fatalf("RegisterFactory failed: %v", err)
	}

	built, err := r.Build([]ToolDefinition{makeDef("ask_user_for_help"), makeDef("read_file")})
	if err != nil {
		t.Fatalf("Expected no error (disabled should be skipped), got %v", err)
	}
	if len(built) != 1 || built[0].Name() != "read_file" {
		t.Fatalf("Expected only [read_file] (disabled skipped), got %v", built)
	}
}

// TestBuildPropagatesError 验证工厂返回 (nil, err) 时收集式错误策略：
// 返回首个错误（包装失败工具名），同时保留已成功构建的其余工具（部分成功）。
func TestBuildPropagatesError(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterFactory("tool_err", failingFactory); err != nil {
		t.Fatalf("RegisterFactory failed: %v", err)
	}
	if err := r.RegisterFactory("tool_ok", okFactory); err != nil {
		t.Fatalf("RegisterFactory failed: %v", err)
	}

	built, err := r.Build([]ToolDefinition{makeDef("tool_err"), makeDef("tool_ok")})
	if err == nil {
		t.Fatal("Expected error from failing factory, got nil")
	}
	if !strings.Contains(err.Error(), "tool_err") {
		t.Fatalf("Expected error to mention failed tool %q, got %q", "tool_err", err.Error())
	}
	var cause *failingFactoryError
	if !errors.As(err, &cause) {
		// 收集式：错误应包装底层原因（%w），验证可解包
		if !strings.Contains(err.Error(), "assemble tool_err failed") {
			t.Fatalf("Expected wrapped cause 'assemble tool_err failed', got %q", err.Error())
		}
	}
	// 部分成功：失败的 tool_err 被跳过，tool_ok 仍构建完成
	if len(built) != 1 || built[0].Name() != "tool_ok" {
		t.Fatalf("Expected partial success [tool_ok], got %v", built)
	}
}

// failingFactoryError 用于验证 %w 包装可被 errors.As 解包
type failingFactoryError struct{ msg string }

func (e *failingFactoryError) Error() string { return e.msg }

// TestBuildWrappedErrorUnwraps 验证 Build 的错误通过 %w 包装，可解包到底层错误
func TestBuildWrappedErrorUnwraps(t *testing.T) {
	sentinel := &failingFactoryError{msg: "boom"}
	r := NewRegistry()
	if err := r.RegisterFactory("broken", func(def ToolDefinition) (*Adapter, error) {
		return nil, sentinel
	}); err != nil {
		t.Fatalf("RegisterFactory failed: %v", err)
	}

	_, err := r.Build([]ToolDefinition{makeDef("broken")})
	if err == nil {
		t.Fatal("Expected error, got nil")
	}
	var unwrapped *failingFactoryError
	if !errors.As(err, &unwrapped) {
		t.Fatalf("Expected errors.As to unwrap to sentinel, got %v", err)
	}
	if unwrapped != sentinel {
		t.Fatalf("Expected unwrapped error to be the sentinel itself, got %v", unwrapped)
	}
}

// TestBuildPreservesOrder 验证 Build 输出顺序与 defs 传入顺序（tools.json 顺序）一致
func TestBuildPreservesOrder(t *testing.T) {
	r := NewRegistry()
	names := []string{"zeta", "alpha", "midway"}
	for _, n := range names {
		if err := r.RegisterFactory(n, okFactory); err != nil {
			t.Fatalf("RegisterFactory(%s) failed: %v", n, err)
		}
	}

	defs := []ToolDefinition{makeDef("zeta"), makeDef("alpha"), makeDef("midway")}
	built, err := r.Build(defs)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
	if len(built) != len(defs) {
		t.Fatalf("Expected %d built adapters, got %d", len(defs), len(built))
	}
	for i, want := range names {
		if built[i].Name() != want {
			t.Fatalf("Built[%d]: expected %q (input order preserved), got %q", i, want, built[i].Name())
		}
	}
}

// TestHasFactory 验证 HasFactory 的 true/false 语义
func TestHasFactory(t *testing.T) {
	r := NewRegistry()

	if r.HasFactory("read_file") {
		t.Fatal("Expected HasFactory=false before registration")
	}
	if err := r.RegisterFactory("read_file", okFactory); err != nil {
		t.Fatalf("RegisterFactory failed: %v", err)
	}
	if !r.HasFactory("read_file") {
		t.Fatal("Expected HasFactory=true after registration")
	}
	if r.HasFactory("run_bash") {
		t.Fatal("Expected HasFactory=false for unregistered tool")
	}
}

// TestBuildFromFactory 验证单工具生成：正常 / 未注册报错 / 禁用返回 (nil, nil) / 装配错误传播
func TestBuildFromFactory(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterFactory("read_file", okFactory); err != nil {
		t.Fatalf("RegisterFactory failed: %v", err)
	}
	if err := r.RegisterFactory("ask_user_for_help", disabledFactory); err != nil {
		t.Fatalf("RegisterFactory failed: %v", err)
	}
	if err := r.RegisterFactory("tool_err", failingFactory); err != nil {
		t.Fatalf("RegisterFactory failed: %v", err)
	}

	// 正常生成
	adapter, err := r.BuildFromFactory("read_file", makeDef("read_file"))
	if err != nil || adapter == nil {
		t.Fatalf("Expected adapter, got (%v, %v)", adapter, err)
	}
	if adapter.Name() != "read_file" {
		t.Fatalf("Expected name read_file, got %q", adapter.Name())
	}

	// 未注册工厂：显式请求应返回错误
	if _, err := r.BuildFromFactory("run_bash", makeDef("run_bash")); err == nil {
		t.Fatal("Expected error for unregistered factory, got nil")
	}

	// 禁用：返回 (nil, nil) 由调用方跳过
	adapter, err = r.BuildFromFactory("ask_user_for_help", makeDef("ask_user_for_help"))
	if err != nil || adapter != nil {
		t.Fatalf("Expected (nil, nil) for disabled tool, got (%v, %v)", adapter, err)
	}

	// 装配错误：原样传播
	_, err = r.BuildFromFactory("tool_err", makeDef("tool_err"))
	if err == nil || !strings.Contains(err.Error(), "assemble tool_err failed") {
		t.Fatalf("Expected assemble error propagated, got %v", err)
	}
}

// TestRegistryExistingMethodsRegression 回归验证既有方法行为不变（纯加法）
func TestRegistryExistingMethodsRegression(t *testing.T) {
	r := NewRegistry()

	if r.Size() != 0 {
		t.Fatalf("Expected empty registry size 0, got %d", r.Size())
	}
	if len(r.List()) != 0 {
		t.Fatalf("Expected empty List, got %v", r.List())
	}
	if len(r.Adapters()) != 0 {
		t.Fatalf("Expected empty Adapters, got %v", r.Adapters())
	}
	if len(r.ToolDefs()) != 0 {
		t.Fatalf("Expected empty ToolDefs, got %v", r.ToolDefs())
	}

	// Register + 重复注册报错
	a1 := NewAdapter("tool_x", "desc x", func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		return "x", nil
	})
	if err := r.Register(a1); err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	if err := r.Register(NewAdapter("tool_x", "dup", nil)); err == nil {
		t.Fatal("Expected duplicate Register error, got nil")
	}
	if _, err := r.Get("tool_x"); err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if _, err := r.Get("missing"); err == nil {
		t.Fatal("Expected Get error for missing tool, got nil")
	}

	// 工厂注册不影响已构建实例（factories 与 adapters 分离）
	if err := r.RegisterFactory("tool_y", okFactory); err != nil {
		t.Fatalf("RegisterFactory failed: %v", err)
	}
	if r.Size() != 1 {
		t.Fatalf("Expected size 1 (factories must not affect adapters), got %d", r.Size())
	}

	// ToolDefs 数量与排序
	defs := r.ToolDefs()
	if len(defs) != 1 {
		t.Fatalf("Expected 1 ToolDef, got %d", len(defs))
	}

	// Hash：稳定且非空
	h1 := r.Hash()
	if h1 == "" {
		t.Fatal("Expected non-empty hash")
	}
	if h2 := r.Hash(); h1 != h2 {
		t.Fatalf("Expected stable hash, got %q vs %q", h1, h2)
	}

	// List 排序
	_ = r.Register(NewAdapter("tool_a", "desc a", func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		return "a", nil
	}))
	list := r.List()
	if len(list) != 2 || list[0] != "tool_a" || list[1] != "tool_x" {
		t.Fatalf("Expected sorted [tool_a tool_x], got %v", list)
	}

	// Unregister
	r.Unregister("tool_a")
	if r.Size() != 1 {
		t.Fatalf("Expected size 1 after Unregister, got %d", r.Size())
	}
	if _, err := r.Get("tool_a"); err == nil {
		t.Fatal("Expected Get error after Unregister, got nil")
	}
}

// TestBuildEmpty 验证空 defs 与空注册表的边界行为
func TestBuildEmpty(t *testing.T) {
	r := NewRegistry()

	built, err := r.Build(nil)
	if err != nil {
		t.Fatalf("Expected no error for nil defs, got %v", err)
	}
	if len(built) != 0 {
		t.Fatalf("Expected 0 built adapters, got %d", len(built))
	}

	if err := r.RegisterFactory("read_file", okFactory); err != nil {
		t.Fatalf("RegisterFactory failed: %v", err)
	}
	built, err = r.Build(nil)
	if err != nil || len(built) != 0 {
		t.Fatalf("Expected (0, nil) for nil defs with factories, got (%d, %v)", len(built), err)
	}
}
