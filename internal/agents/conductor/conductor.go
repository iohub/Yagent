package conductor

import (
	_ "embed"
	"fmt"
	"log/slog"
	"sync"
)

//go:embed conductor.prompt.md
var conductorPrompt string

// ConductorAgent 是顶层协调器，负责编排多 Agent 系统。
// 它将具体职责委托给子组件（Planner, Router, MemoryManager, RecoveryHandler, MetricsCollector），
// 自身仅负责顶层执行流程。
type ConductorAgent struct {
	config   Config
	prompt   string
	router   *Router
	planner  *Planner
	memory   *MemoryManager
	recovery *RecoveryHandler
	metrics  *MetricsCollector
	mu       sync.Mutex
}

// Config 是 ConductorAgent 的配置。
type Config struct {
	MaxSteps        int
	MetaRetryCount  int
	MetricsEnabled  bool
	RecoveryCfg     RecoveryConfig
	PlannerMaxDepth int
}

// DefaultConfig 返回默认配置。
func DefaultConfig() Config {
	return Config{
		MaxSteps:        30,
		MetaRetryCount:  3,
		MetricsEnabled:  true,
		RecoveryCfg:     DefaultRecoveryConfig(),
		PlannerMaxDepth: 5,
	}
}

// NewConductorAgent 创建并初始化 ConductorAgent。
// 使用 Option 模式提供灵活的配置。
func NewConductorAgent(opts ...Option) *ConductorAgent {
	cfg := DefaultConfig()
	for _, opt := range opts {
		opt(&cfg)
	}

	agent := &ConductorAgent{
		config:   cfg,
		prompt:   conductorPrompt,
		router:   NewRouter(),
		planner:  NewPlanner(cfg.PlannerMaxDepth),
		memory:   NewMemoryManager(),
		recovery: NewRecoveryHandler(cfg.RecoveryCfg),
		metrics:  NewMetricsCollector(cfg.MetricsEnabled),
	}

	return agent
}

// Option 是 ConductorAgent 的配置选项函数。
type Option func(*Config)

// WithMaxSteps 设置最大执行步数。
func WithMaxSteps(n int) Option {
	return func(c *Config) { c.MaxSteps = n }
}

// WithMetaRetryCount 设置 Meta-Agent 重试次数。
func WithMetaRetryCount(n int) Option {
	return func(c *Config) { c.MetaRetryCount = n }
}

// WithMetricsEnabled 设置是否启用指标收集。
func WithMetricsEnabled(b bool) Option {
	return func(c *Config) { c.MetricsEnabled = b }
}

// WithRecoveryConfig 设置恢复配置。
func WithRecoveryConfig(rc RecoveryConfig) Option {
	return func(c *Config) { c.RecoveryCfg = rc }
}

// WithPlannerMaxDepth 设置规划器最大深度。
func WithPlannerMaxDepth(n int) Option {
	return func(c *Config) { c.PlannerMaxDepth = n }
}

// Name 返回 Agent 名称。
func (a *ConductorAgent) Name() string {
	return "Conductor"
}

// Router 返回路由器的引用，方便外部注册 Agent。
func (a *ConductorAgent) Router() *Router {
	return a.router
}

// MemoryManager 返回内存管理器的引用。
func (a *ConductorAgent) MemoryManager() *MemoryManager {
	return a.memory
}

// RecoveryHandler 返回恢复管理器的引用。
func (a *ConductorAgent) RecoveryHandler() *RecoveryHandler {
	return a.recovery
}

// MetricsCollector 返回指标收集器的引用。
func (a *ConductorAgent) MetricsCollector() *MetricsCollector {
	return a.metrics
}

// GetPrompt 返回嵌入的系统提示词。
func (a *ConductorAgent) GetPrompt() string {
	return a.prompt
}

// GetConfig 返回当前配置的副本。
func (a *ConductorAgent) GetConfig() Config {
	return a.config
}

// Snapshot 返回当前 Conductor 状态的快照（用于持久化和监控）。
func (a *ConductorAgent) Snapshot() map[string]interface{} {
	a.mu.Lock()
	defer a.mu.Unlock()

	snapshot := map[string]interface{}{
		"name":           a.Name(),
		"max_steps":      a.config.MaxSteps,
		"agent_count":    a.router.AgentCount(),
		"agent_names":    a.router.ListAgents(),
		"metrics":        a.metrics.Snapshot(),
		"recovery_stats": a.recovery.GetFailureStats(),
	}

	return snapshot
}

// Run 是顶层执行入口。
// 它编排规划 → 路由 → 执行 → 恢复的完整流程。
// 参数：
//
//	executorFn: 执行函数，由外部注入具体的执行逻辑
//	task:       用户任务描述
//
// 返回最终结果文本。
func (a *ConductorAgent) Run(executorFn func(router *Router, memory *MemoryManager, recovery *RecoveryHandler, metrics *MetricsCollector, prompt string) (string, error)) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.metrics.RecordTaskStart()
	slog.Info("[Conductor] Starting execution",
		"agent_count", a.router.AgentCount(),
		"max_steps", a.config.MaxSteps)

	result, err := executorFn(a.router, a.memory, a.recovery, a.metrics, a.prompt)
	if err != nil {
		slog.Error("[Conductor] Execution failed", "error", err)
		return "", fmt.Errorf("conductor execution failed: %w", err)
	}

	slog.Info("[Conductor] Execution completed")
	return result, nil
}

// RegisterAgent 注册一个 Agent 到路由器（便捷方法）。
func (a *ConductorAgent) RegisterAgent(name string, agent AgentRunner) {
	a.router.Register(name, agent)
}

// RegisterDefaultAgent 注册默认 Agent（便捷方法）。
func (a *ConductorAgent) RegisterDefaultAgent(agent AgentRunner) {
	a.router.RegisterDefault(agent)
}
