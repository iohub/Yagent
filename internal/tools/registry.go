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

	"codeactor/internal/llm"
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
	mu       sync.RWMutex
	hash     string // 工具列表哈希（用于缓存一致性检测）
	dirty    bool   // 需要重新计算哈希
}

// NewRegistry 创建空注册表
func NewRegistry() *Registry {
	return &Registry{
		adapters: make(map[string]*Adapter),
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

// Adapters 返回所有适配器（用于 ExecutorConfig）
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
