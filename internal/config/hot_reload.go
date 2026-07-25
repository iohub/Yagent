package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/fsnotify/fsnotify"
)

// ConfigSubscriber 配置变更回调
type ConfigSubscriber func(old, new *Config)

// subscriberID 用于唯一标识订阅者
type subscriberID int

// ConfigManager 提供线程安全的配置访问 + 热加载
type ConfigManager struct {
	current     atomic.Value          // 存储 *Config
	configPath  string
	watcher     *fsnotify.Watcher
	subscribers map[subscriberID]ConfigSubscriber  // 使用 map 替代 slice
	nextID      subscriberID
	debounce    time.Duration
	stopCh      chan struct{}
	mu          sync.Mutex
	stopped     bool
}

// NewConfigManager 创建配置管理器
func NewConfigManager(path string) (*ConfigManager, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve config path: %w", err)
	}

	cm := &ConfigManager{
		configPath:  absPath,
		debounce:    500 * time.Millisecond,
		stopCh:      make(chan struct{}),
		subscribers: make(map[subscriberID]ConfigSubscriber),
	}

	// 初始加载
	cfg, err := LoadFromFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("initial config load failed: %w", err)
	}
	cm.current.Store(cfg)

	return cm, nil
}

// Get 获取当前配置（线程安全，无锁读取）
func (cm *ConfigManager) Get() *Config {
	return cm.current.Load().(*Config)
}

// Load 加载配置（可多次调用）
func (cm *ConfigManager) Load() error {
	cfg, err := LoadFromFile(cm.configPath)
	if err != nil {
		return err
	}
	old := cm.Get()
	cm.current.Store(cfg)
	slog.Info("Config loaded successfully", "path", cm.configPath)
	cm.notifySubscribers(old, cfg)
	return nil
}

// Reload 强制重新加载配置
func (cm *ConfigManager) Reload() error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	newCfg, err := LoadFromFile(cm.configPath)
	if err != nil {
		slog.Error("Config reload failed (keeping old config)", "error", err)
		return fmt.Errorf("config reload failed: %w", err)
	}

	old := cm.Get()
	cm.current.Store(newCfg)

	changed := diffConfigs(old, newCfg)
	slog.Info("Config reloaded successfully",
		"path", cm.configPath,
		"changed_keys", changed,
	)

	// 异步通知订阅者（避免死锁）
	go cm.notifySubscribers(old, newCfg)

	return nil
}

// Watch 开始监听配置文件变更
func (cm *ConfigManager) Watch() error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if cm.watcher != nil {
		return nil // 已在监听
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create fsnotify watcher: %w", err)
	}

	// 监听配置文件本身
	if err := watcher.Add(cm.configPath); err != nil {
		watcher.Close()
		return fmt.Errorf("failed to watch config file: %w", err)
	}

	// 也监听目录（处理编辑器 swap-and-replace 模式）
	dir := filepath.Dir(cm.configPath)
	if err := watcher.Add(dir); err != nil {
		slog.Warn("Failed to watch config directory, file-only watching", "error", err)
	}

	cm.watcher = watcher
	cm.stopped = false

	go cm.watchLoop()

	slog.Info("Config hot-reload started", "path", cm.configPath)
	return nil
}

// Stop 停止监听
func (cm *ConfigManager) Stop() {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if cm.stopped {
		return
	}
	cm.stopped = true
	close(cm.stopCh)

	if cm.watcher != nil {
		cm.watcher.Close()
		cm.watcher = nil
	}

	slog.Info("Config hot-reload stopped")
}

// Subscribe 注册配置变更回调
// 返回 unsubscribe 函数
func (cm *ConfigManager) Subscribe(fn ConfigSubscriber) func() {
	cm.mu.Lock()
	if cm.stopped {
		cm.mu.Unlock()
		// 如果已停止，返回空操作
		return func() {}
	}
	id := cm.nextID
	cm.nextID++
	cm.subscribers[id] = fn
	cm.mu.Unlock()

	return func() {
		cm.mu.Lock()
		defer cm.mu.Unlock()
		delete(cm.subscribers, id)
	}
}

// SetDebounce 设置去抖延迟
func (cm *ConfigManager) SetDebounce(d time.Duration) {
	if d > 0 {
		cm.debounce = d
	}
}

// Close 关闭管理器
func (cm *ConfigManager) Close() error {
	cm.Stop()
	return nil
}

// --- 内部方法 ---

// watchLoop 文件监听循环
func (cm *ConfigManager) watchLoop() {
	var debounceTimer *time.Timer

	for {
		select {
		case <-cm.stopCh:
			return

		case event, ok := <-cm.watcher.Events:
			if !ok {
				return
			}

			// 只处理目标配置文件的事件
			eventPath := filepath.Clean(event.Name)
			configPath := filepath.Clean(cm.configPath)
			if eventPath != configPath {
				continue
			}

			// 只处理写入/创建/重命名事件
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
				continue
			}

			slog.Debug("Config file change detected", "event", event)

			// 去抖：重置定时器
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			debounceTimer = time.AfterFunc(cm.debounce, func() {
				if err := cm.Reload(); err != nil {
					slog.Error("Config hot-reload failed", "error", err)
				}
			})

		case err, ok := <-cm.watcher.Errors:
			if !ok {
				return
			}
			slog.Error("Config watcher error", "error", err)
		}
	}
}

// notifySubscribers 通知所有订阅者
func (cm *ConfigManager) notifySubscribers(old, new *Config) {
	cm.mu.Lock()
	// 复制订阅者 map 以避免持有锁时调用回调
	subscribers := make([]ConfigSubscriber, 0, len(cm.subscribers))
	for _, fn := range cm.subscribers {
		subscribers = append(subscribers, fn)
	}
	cm.mu.Unlock()

	for _, fn := range subscribers {
		fn(old, new)
	}
}

// --- 辅助函数 ---

// LoadFromFile 从文件加载配置（提取自 LoadConfig，不执行 validate）
func LoadFromFile(path string) (*Config, error) {
	cfg := &Config{}
	if _, err := toml.DecodeFile(path, cfg); err != nil {
		return nil, fmt.Errorf("failed to decode config file: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}
	return cfg, nil
}

// diffConfigs 比较两个配置的差异，返回变更的顶级 key 列表
func diffConfigs(old, new *Config) []string {
	var changed []string

	if !compareDeep(old.Global, new.Global) {
		changed = append(changed, "global")
	}
	if !compareDeep(old.Agents, new.Agents) {
		changed = append(changed, "agents")
	}
	if !compareDeep(old.Tools, new.Tools) {
		changed = append(changed, "tools")
	}
	if !compareDeep(old.App, new.App) {
		changed = append(changed, "app")
	}
	if !compareDeep(old.Agent, new.Agent) {
		changed = append(changed, "agent")
	}
	if !compareDeep(old.LLM, new.LLM) {
		changed = append(changed, "llm")
	}
	if !compareDeep(old.Browser, new.Browser) {
		changed = append(changed, "browser")
	}
	if !compareDeep(old.Keywords, new.Keywords) {
		changed = append(changed, "keywords")
	}
	if old.TaskTimeout != new.TaskTimeout {
		changed = append(changed, "task_timeout")
	}

	return changed
}

// compareDeep 使用 JSON 序列化进行深度比较，处理 map/slice 类型
func compareDeep(a, b interface{}) bool {
	aJSON, _ := json.Marshal(a)
	bJSON, _ := json.Marshal(b)
	return string(aJSON) == string(bJSON)
}

// HasChanges 检查配置是否有任何变化
func HasChanges(old, new *Config) bool {
	return len(diffConfigs(old, new)) > 0
}

// IsProviderChanged 检查 LLM 提供商配置是否变化
func IsProviderChanged(old, new *Config) bool {
	if old.Global.LLM == nil || new.Global.LLM == nil {
		return old.Global.LLM != new.Global.LLM
	}
	if old.Global.LLM.UseProvider != new.Global.LLM.UseProvider {
		return true
	}
	return !compareDeep(old.Global.LLM.Providers, new.Global.LLM.Providers)
}

// IsStreamingChanged 检查流式输出配置是否变化
func IsStreamingChanged(old, new *Config) bool {
	return old.App.EnableStreaming != new.App.EnableStreaming
}

// --- 类型断言辅助 ---

// ConfigIsLoaded 检查配置管理器是否已加载配置
func (cm *ConfigManager) ConfigIsLoaded() bool {
	return cm.current.Load() != nil
}

// GetSubscriberCount 返回当前订阅者数量
func (cm *ConfigManager) GetSubscriberCount() int {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return len(cm.subscribers)
}
