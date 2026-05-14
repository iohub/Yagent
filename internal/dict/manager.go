package dict

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/fsnotify/fsnotify"
)

// KeywordsConfig 词典配置
type KeywordsConfig struct {
	// DefaultPath 默认关键词文件路径（用户级词典）
	DefaultPath string `toml:"default_path"`

	// HotReload 是否启用热重载
	HotReload bool `toml:"hot_reload"`

	// AutoComplete 补全词典配置
	AutoComplete *AutoCompleteConfig `toml:"autocomplete"`

	// Scanners 扫描词典配置列表
	Scanners map[string]*ScannerConfig `toml:"scanners"`
}

// AutoCompleteConfig 补全词典配置
type AutoCompleteConfig struct {
	// Sources 关键词源列表（文件路径）
	Sources []string `toml:"sources"`
}

// ScannerConfig 扫描词典配置
type ScannerConfig struct {
	// Sources 关键词源列表
	Sources []string `toml:"sources"`
}

// ManagerImpl 词典管理器实现
type ManagerImpl struct {
	name            string
	completion      *CompletionDict
	scanners        map[string]*ScannerMatcher
	mu              sync.RWMutex
	defaultCompPath string          // 默认关键词文件路径
	config          KeywordsConfig  // 词典配置
	hotReload       bool            // 是否启用热重载
	watcher         *fsnotify.Watcher // 文件监控器
	fileWatchers    map[string]*fsnotify.Watcher // 每个文件的独立 watcher
	reloadPending   atomic.Bool     // 标记是否有待处理的重载
}

// NewManager 创建词典管理器
// config: 词典配置
// defaultPath: 默认关键词文件路径（用于向后兼容）
func NewManager(config *KeywordsConfig, defaultPath string) (*ManagerImpl, error) {
	m := &ManagerImpl{
		name:            "dict-manager",
		scanners:        make(map[string]*ScannerMatcher),
		defaultCompPath: defaultPath,
		fileWatchers:    make(map[string]*fsnotify.Watcher),
	}

	// 设置配置（如果为 nil，使用默认配置）
	if config == nil {
		config = &KeywordsConfig{}
	}
	m.config = *config

	// 如果 defaultPath 为空但配置中有 DefaultPath，使用配置的
	if defaultPath == "" && m.config.DefaultPath != "" {
		defaultPath = m.config.DefaultPath
	}

	// 设置热重载标志
	m.hotReload = m.config.HotReload

	// 如果配置为空，自动创建默认的 autocomplete 词典（向后兼容）
	if (config.AutoComplete == nil || len(config.AutoComplete.Sources) == 0) && len(config.Scanners) == 0 {
		if err := m.initDefaultCompletion(defaultPath); err != nil {
			return nil, fmt.Errorf("初始化默认补全词典失败: %w", err)
		}
	} else {
		// 创建补全词典
		if config.AutoComplete != nil && len(config.AutoComplete.Sources) > 0 {
			m.completion = NewCompletionDict("autocomplete", config.AutoComplete.Sources)
		}

		// 创建扫描词典
		for name, scannerCfg := range config.Scanners {
			if len(scannerCfg.Sources) == 0 {
				continue
			}

			scanner, err := NewScannerMatcher(name, scannerCfg.Sources)
			if err != nil {
				return nil, fmt.Errorf("创建扫描器 %s 失败: %w", name, err)
			}
			m.scanners[name] = scanner
		}

		// 如果没有创建任何词典，创建默认的
		if m.completion == nil && len(m.scanners) == 0 {
			if err := m.initDefaultCompletion(defaultPath); err != nil {
				return nil, fmt.Errorf("初始化默认补全词典失败: %w", err)
			}
		}
	}

	// 如果启用了热重载，启动文件监控
	if m.hotReload {
		if err := m.initHotReload(); err != nil {
			// 热重载初始化失败不影响词典加载，只记录错误
			fmt.Printf("[dict] 警告: 热重载初始化失败: %v\n", err)
			m.hotReload = false
		}
	}

	return m, nil
}

// initDefaultCompletion 初始化默认补全词典（向后兼容）
func (m *ManagerImpl) initDefaultCompletion(defaultPath string) error {
	// 优先使用默认路径创建词典（直接加载，以便 Reload 能正确工作）
	completion := NewCompletionDict("autocomplete", nil)
	if defaultPath != "" {
		if err := completion.LoadFromFile(defaultPath); err != nil {
			return fmt.Errorf("加载默认词典文件失败: %w", err)
		}
	} else {
		// 否则使用内置默认关键词
		completion.AddWords(DefaultKeywords())
	}
	m.completion = completion
	return nil
}

// initHotReload 初始化热重载功能
func (m *ManagerImpl) initHotReload() error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("创建文件监控器失败: %w", err)
	}

	m.watcher = watcher

	// 收集所有需要监控的文件路径
	var watchPaths []string

	if m.completion != nil {
		// 获取补全词典的源文件路径
		watchPaths = append(watchPaths, m.getCompletionSources()...)
	}

	// 获取所有扫描器的源文件路径
	for _, scanner := range m.scanners {
		watchPaths = append(watchPaths, scanner.Sources()...)
	}

	// 添加监控
	for _, path := range watchPaths {
		if err := m.addFileWatch(path); err != nil {
			fmt.Printf("[dict] 警告: 无法监控文件 %s: %v\n", path, err)
		}
	}

	// 启动事件处理 goroutine
	go m.watchEvents()

	return nil
}

// getCompletionSources 获取补全词典的源文件路径
func (m *ManagerImpl) getCompletionSources() []string {
	if m.completion == nil {
		return nil
	}
	// 从补全词典获取源路径
	// 注意：这里需要调用 CompletionDict 的公开方法，或者通过其他方式获取
	// 由于 CompletionDict 没有公开获取源路径的方法，我们使用默认路径
	if m.defaultCompPath != "" {
		return []string{m.defaultCompPath}
	}
	return nil
}

// addFileWatch 添加文件监控
// 处理编辑器的原子写入（rename+write）场景
func (m *ManagerImpl) addFileWatch(path string) error {
	// 规范化路径
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("无法获取绝对路径: %w", err)
	}

	// 检查文件是否存在
	if _, err := os.Stat(absPath); os.IsNotExist(err) {
		// 文件不存在，监控父目录
		parentDir := filepath.Dir(absPath)
		return m.addDirWatch(parentDir, filepath.Base(absPath))
	}

	// 文件存在，直接监控文件
	// 先检查是否已经有 watcher 监控这个文件
	if _, exists := m.fileWatchers[absPath]; exists {
		return nil // 已经监控
	}

	if err := m.watcher.Add(absPath); err != nil {
		return fmt.Errorf("无法添加文件监控: %w", err)
	}

	// 创建一个独立的 watcher 用于追踪文件句柄
	fileWatcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("创建文件监控器失败: %w", err)
	}

	if err := fileWatcher.Add(absPath); err != nil {
		fileWatcher.Close()
		return fmt.Errorf("无法添加文件监控: %w", err)
	}

	m.fileWatchers[absPath] = fileWatcher

	return nil
}

// addDirWatch 添加目录监控，检测文件的创建/重命名
func (m *ManagerImpl) addDirWatch(dirPath, fileName string) error {
	absDir, err := filepath.Abs(dirPath)
	if err != nil {
		return fmt.Errorf("无法获取目录绝对路径: %w", err)
	}

	if _, err := os.Stat(absDir); os.IsNotExist(err) {
		return fmt.Errorf("目录不存在: %s", absDir)
	}

	// 检查目录是否已经被监控
	// 这里简化处理，直接添加监控
	return m.watcher.Add(absDir)
}

// watchEvents 处理文件监控事件
func (m *ManagerImpl) watchEvents() {
	for {
		select {
		case event, ok := <-m.watcher.Events:
			if !ok {
				return
			}

			m.handleFileEvent(event)

		case err, ok := <-m.watcher.Errors:
			if !ok {
				return
			}
			fmt.Printf("[dict] 监控错误: %v\n", err)
		}
	}
}

// handleFileEvent 处理单个文件事件
func (m *ManagerImpl) handleFileEvent(event fsnotify.Event) {
	path := event.Name

	// 忽略临时文件和备份文件
	if isTempFile(path) {
		return
	}

	fmt.Printf("[dict] 文件变化: %s\n", event.String())

	switch {
	case event.Has(fsnotify.Write):
		// 文件写入，触发重载
		m.triggerReload(path)

	case event.Has(fsnotify.Rename):
		// 文件被重命名/移动，通常是编辑器原子写入的前奏
		// 移除旧文件监控
		m.removeFileWatch(path)
		// 触发重载（文件可能已被新内容替换）
		m.triggerReload(path)

	case event.Has(fsnotify.Remove):
		// 文件被删除，移除监控
		m.removeFileWatch(path)

	case event.Has(fsnotify.Create):
		// 文件被创建（编辑器的原子写入会创建新文件）
		// 添加新文件监控
		if err := m.addFileWatch(path); err != nil {
			fmt.Printf("[dict] 警告: 添加新文件监控失败: %v\n", err)
		}
		// 触发重载
		m.triggerReload(path)
	}
}

// triggerReload 触发词典重载
func (m *ManagerImpl) triggerReload(changedPath string) {
	// 如果已经在重载中，跳过
	if m.reloadPending.Load() {
		return
	}

	m.reloadPending.Store(true)

	// 异步重载，避免阻塞事件处理
	go func() {
		defer m.reloadPending.Store(false)

		if err := m.ReloadAll(); err != nil {
			fmt.Printf("[dict] 重载失败: %v\n", err)
		} else {
			fmt.Printf("[dict] 重载成功: %s\n", changedPath)
		}
	}()
}

// removeFileWatch 移除文件监控
func (m *ManagerImpl) removeFileWatch(path string) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return
	}

	// 关闭独立 watcher
	if fileWatcher, exists := m.fileWatchers[absPath]; exists {
		fileWatcher.Close()
		delete(m.fileWatchers, absPath)
	}

	// 注意：不能从主 watcher 移除单个文件，因为 fsnotify 的限制
	// 但我们可以通过关闭并重新添加目录来间接实现
}

// isTempFile 检查是否是临时文件
func isTempFile(path string) bool {
	base := filepath.Base(path)
	// 忽略以 . 开头的临时文件（如 Emacs backup, Vim swap）
	if len(base) > 0 && base[0] == '.' {
		return true
	}
	// 忽略常见的备份扩展名
	if len(base) > 2 && base[len(base)-2:] == "~" {
		return true
	}
	return false
}

// AutoComplete 实现 Manager 接口的自动补全方法
func (m *ManagerImpl) AutoComplete(prefix string) []string {
	if m.completion == nil {
		return nil
	}
	return m.completion.Complete(prefix)
}

// MatchAll 实现 Manager 接口的匹配扫描方法
func (m *ManagerImpl) MatchAll(dictName string, text []byte) ([]Match, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if dictName == "" {
		// 如果未指定词典名，扫描所有扫描词典
		allMatches := make([]Match, 0)
		for _, scanner := range m.scanners {
			matches := scanner.MatchAll(text)
			allMatches = append(allMatches, matches...)
		}
		return allMatches, nil
	}

	scanner, ok := m.scanners[dictName]
	if !ok {
		return nil, fmt.Errorf("未找到词典: %s", dictName)
	}

	return scanner.MatchAll(text), nil
}

// ListDicts 实现 Manager 接口的列出所有词典方法
func (m *ManagerImpl) ListDicts() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	dicts := make([]string, 0, len(m.scanners))

	// 添加补全词典
	if m.completion != nil {
		dicts = append(dicts, m.completion.Name())
	}

	// 添加扫描词典
	for name := range m.scanners {
		dicts = append(dicts, name)
	}

	return dicts
}

// Close 实现 Manager 接口的关闭资源方法
func (m *ManagerImpl) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 关闭文件监控器
	if m.watcher != nil {
		m.watcher.Close()
		m.watcher = nil
	}

	// 关闭所有独立文件 watcher
	for path, watcher := range m.fileWatchers {
		watcher.Close()
		delete(m.fileWatchers, path)
	}

	// 关闭所有扫描词典
	for name, scanner := range m.scanners {
		if err := scanner.Reload(); err != nil {
			// 忽略重载错误，继续关闭
			_ = err
		}
		delete(m.scanners, name)
	}

	// 清空白补全词典
	if m.completion != nil {
		m.completion.Clear()
		m.completion = nil
	}

	return nil
}

// GetCompletionDict 获取补全词典（供内部使用）
func (m *ManagerImpl) GetCompletionDict() *CompletionDict {
	return m.completion
}

// GetScanner 获取指定名称的扫描器
func (m *ManagerImpl) GetScanner(name string) *ScannerMatcher {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.scanners[name]
}

// ReloadAll 重新加载所有词典
func (m *ManagerImpl) ReloadAll() error {
	var lastErr error

	if m.completion != nil {
		if err := m.completion.Reload(); err != nil {
			lastErr = err
		}
	}

	m.mu.RLock()
	for _, scanner := range m.scanners {
		if err := scanner.Reload(); err != nil {
			lastErr = err
		}
	}
	m.mu.RUnlock()

	return lastErr
}

// EnsureManagerImpl implements Manager interface
var _ Manager = (*ManagerImpl)(nil)
