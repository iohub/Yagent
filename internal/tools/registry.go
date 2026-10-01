package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"sync"

	"yagent/internal/llm"
)

// ToolDefinition 工具定义（从 tools.json 加载的 JSON 结构）
type ToolDefinition struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

// Registry 工具注册表 - 线程安全的插件化工具管理器
type Registry struct {
	adapters map[string]*Adapter
	// factories 工具工厂表："如何构建"（与 adapters 的"已构建实例"分离）。
	// 工厂从 tools.json 的 ToolDefinition 生成 Adapter，供 Build/BuildFromFactory 使用。
	factories map[string]FactoryFunc
	mu        sync.RWMutex
	hash      string // 工具列表哈希（用于缓存一致性检测）
	dirty     bool   // 需要重新计算哈希
}

// NewRegistry 创建空注册表
func NewRegistry() *Registry {
	return &Registry{
		adapters:  make(map[string]*Adapter),
		factories: make(map[string]FactoryFunc),
	}
}

// Register 注册一个工具
func (r *Registry) Register(adapter *Adapter) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	name := adapter.Name()
	if _, exists := r.adapters[name]; exists {
		return fmt.Errorf("tool %q already registered", name)
	}
	r.adapters[name] = adapter
	r.dirty = true

	slog.Debug("Tool registered", "name", name, "description", adapter.Description())
	return nil
}

// MustRegister 注册工具，冲突时 panic（用于初始化阶段）
func (r *Registry) MustRegister(adapter *Adapter) {
	if err := r.Register(adapter); err != nil {
		panic(fmt.Sprintf("failed to register tool %q: %v", adapter.Name(), err))
	}
}

// Get 获取已注册的工具
func (r *Registry) Get(name string) (*Adapter, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	adapter, ok := r.adapters[name]
	if !ok {
		return nil, fmt.Errorf("tool %q not found", name)
	}
	return adapter, nil
}

// Execute 执行指定工具
func (r *Registry) Execute(ctx context.Context, name string, params map[string]interface{}) (interface{}, error) {
	adapter, err := r.Get(name)
	if err != nil {
		return nil, err
	}
	return adapter.fn(ctx, params)
}

// Unregister 注销工具
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.adapters, name)
	r.dirty = true
}

// List 返回所有已注册的工具名列表（排序后）
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.adapters))
	for name := range r.adapters {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Adapters 返回所有适配器（用于子 Agent 装配）
func (r *Registry) Adapters() []*Adapter {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*Adapter, 0, len(r.adapters))
	for _, adapter := range r.adapters {
		result = append(result, adapter)
	}
	// 按名称排序保证确定性
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name() < result[j].Name()
	})
	return result
}

// ToolDefs 生成 LLM 工具定义列表
func (r *Registry) ToolDefs() []llm.ToolDef {
	r.mu.RLock()
	defer r.mu.RUnlock()

	defs := make([]llm.ToolDef, 0, len(r.adapters))
	for _, adapter := range r.adapters {
		defs = append(defs, adapter.ToToolDef())
	}
	SortToolDefs(defs)
	return defs
}

// Hash 返回工具列表的 SHA256 哈希（前16位 hex）
func (r *Registry) Hash() string {
	r.mu.RLock()
	hash := r.hash
	dirty := r.dirty
	r.mu.RUnlock()

	if !dirty && hash != "" {
		return hash
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.dirty && r.hash != "" {
		return r.hash
	}

	defs := make([]llm.ToolDef, 0, len(r.adapters))
	for _, adapter := range r.adapters {
		defs = append(defs, adapter.ToToolDef())
	}
	SortToolDefs(defs)

	data, _ := json.Marshal(defs)
	h := sha256.Sum256(data)
	r.hash = hex.EncodeToString(h[:8])
	r.dirty = false
	return r.hash
}

// Size 返回已注册工具数量
func (r *Registry) Size() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.adapters)
}

// FactoryFunc 工具工厂：从 tools.json 的定义生成 Adapter。
// 返回 (nil, nil) 表示"工具被禁用"（如 FullYoloMode 下 ask_user_for_help），调用方跳过；
// 返回 (nil, err) 表示装配错误，调用方传播。
type FactoryFunc func(def ToolDefinition) (*Adapter, error)

// RegisterFactory 注册一个工具工厂，重复注册返回错误（风格同 Register）。
func (r *Registry) RegisterFactory(name string, f FactoryFunc) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.factories[name]; exists {
		return fmt.Errorf("tool factory %q already registered", name)
	}
	r.factories[name] = f

	slog.Debug("Tool factory registered", "name", name)
	return nil
}

// MustRegisterFactory 注册工厂，冲突时 panic（用于初始化阶段，风格同 MustRegister）
func (r *Registry) MustRegisterFactory(name string, f FactoryFunc) {
	if err := r.RegisterFactory(name, f); err != nil {
		panic(fmt.Sprintf("failed to register tool factory %q: %v", name, err))
	}
}

// HasFactory 报告是否注册了指定工具的工厂
func (r *Registry) HasFactory(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	_, ok := r.factories[name]
	return ok
}

// BuildFromFactory 按工厂生成单个工具的 Adapter（registerCustomAgent 之类单工具场景用）。
// 与 Build 的差异：此处是显式单工具请求，工厂未注册时返回错误（调用方应感知缺失）；
// 工厂返回 (nil, nil) 表示工具被禁用，原样返回 (nil, nil) 由调用方跳过；
// 工厂返回 (nil, err) 时原样传播装配错误。
// 注意：调用工厂时已释放读锁，允许工厂内部回调 Registry（如 Register）。
func (r *Registry) BuildFromFactory(name string, def ToolDefinition) (*Adapter, error) {
	r.mu.RLock()
	f, ok := r.factories[name]
	r.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("no factory registered for tool %q", name)
	}
	return f(def)
}

// Build 按 defs（保持传入顺序，即 tools.json 顺序）生成 Adapter 列表。
//   - 工厂未注册 → 跳过（等价旧构造函数 switch 的 default: continue）
//   - 工厂返回 (nil, nil) → 跳过（等价 FullYoloMode 禁用）
//   - 工厂返回 (nil, err) → 收集式错误策略：记录错误并继续构建其余工具，
//     最终返回已构建列表与首个错误（包装了失败工具名），以便部分构建成功、
//     调用方可同时得到可用工具与失败原因。
//
// 注意：先快照工厂表再逐个调用工厂，调用期间不持锁，允许工厂内部回调 Registry。
func (r *Registry) Build(defs []ToolDefinition) ([]*Adapter, error) {
	r.mu.RLock()
	factories := make(map[string]FactoryFunc, len(r.factories))
	for name, f := range r.factories {
		factories[name] = f
	}
	r.mu.RUnlock()

	built := make([]*Adapter, 0, len(defs))
	var firstErr error
	for _, def := range defs {
		f, ok := factories[def.Name]
		if !ok {
			continue // 工厂未注册：跳过
		}
		adapter, err := f(def)
		if err != nil {
			// 装配错误：记录首个错误，继续构建其余工具（收集式）
			if firstErr == nil {
				firstErr = fmt.Errorf("build tool %q: %w", def.Name, err)
			}
			slog.Warn("Tool build failed", "name", def.Name, "error", err)
			continue
		}
		if adapter == nil {
			continue // 工厂返回 (nil, nil)：工具被禁用，跳过
		}
		built = append(built, adapter)
	}
	return built, firstErr
}
