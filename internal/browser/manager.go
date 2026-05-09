package browser

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/launcher/flags"
	"github.com/go-rod/rod/lib/proto"
)

// Manager 浏览器管理器单例
// 负责管理 Chromium 浏览器实例的完整生命周期
type Manager struct {
	mu       sync.Mutex
	browser  *rod.Browser
	launcher *launcher.Launcher

	// 配置
	cfg        BrowserCfg
	security   *SecurityPolicy
	browserURL string // 浏览器 WebSocket URL

	// 并发控制
	sem chan struct{} // 信号量控制最大并发页面数

	// 生命周期
	lastUsed    time.Time
	tempDir     string // 临时用户数据目录
	closed      bool
	closeCh     chan struct{}
	idleTimer   *time.Timer
	idleTimeout time.Duration

	// 统计
	stats ManagerStats
}

// ManagerStats 管理器统计信息
type ManagerStats struct {
	mu            sync.Mutex
	TotalAcquired int64 // 总获取页面次数
	TotalReleased int64 // 总释放页面次数
	ActivePages   int   // 当前活跃页面数
	CrashCount    int   // 浏览器崩溃次数
	RestartCount  int   // 浏览器重启次数
}

// NewManager 创建浏览器管理器
func NewManager(cfg BrowserCfg, allowedDomains, blockedDomains []string) *Manager {
	// 解析空闲超时
	var idleTimeout time.Duration
	if cfg.IdleTimeout != "" {
		var err error
		idleTimeout, err = time.ParseDuration(cfg.IdleTimeout)
		if err != nil {
			log.Printf("[BrowserManager] 无效的空闲超时配置 '%s'，使用默认 5m: %v", cfg.IdleTimeout, err)
			idleTimeout = 5 * time.Minute
		}
	} else {
		idleTimeout = 5 * time.Minute
	}

	// 并发页面数
	maxPages := cfg.MaxConcurrentPages
	if maxPages <= 0 {
		maxPages = 4
	}

	m := &Manager{
		cfg:         cfg,
		security:    NewSecurityPolicy(allowedDomains, blockedDomains),
		sem:         make(chan struct{}, maxPages),
		closeCh:     make(chan struct{}),
		idleTimeout: idleTimeout,
	}

	return m
}

// AcquirePage 获取一个浏览器页面（受信号量控制）
// 返回页面、释放函数和错误
// 调用方必须在完成后调用 release 函数
func (m *Manager) AcquirePage(ctx context.Context) (*rod.Page, func(), error) {
	// 信号量控制并发
	select {
	case m.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case <-m.closeCh:
		// 管理器已关闭
		return nil, nil, errors.New("浏览器管理器已关闭")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// 懒启动浏览器
	if m.browser == nil {
		if err := m.launch(); err != nil {
			<-m.sem // 释放信号量
			return nil, nil, fmt.Errorf("启动浏览器失败: %w", err)
		}
	}

	// 健康检查
	if err := m.ping(ctx); err != nil {
		log.Printf("[BrowserManager] 浏览器健康检查失败，尝试重启: %v", err)
		if err := m.restart(); err != nil {
			<-m.sem
			return nil, nil, fmt.Errorf("重启浏览器失败: %w", err)
		}
	}

	// 取消空闲计时器
	if m.idleTimer != nil {
		m.idleTimer.Stop()
	}

	// 创建新页面
	page, err := m.browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		<-m.sem
		return nil, nil, fmt.Errorf("创建页面失败: %w", err)
	}

	// 设置安全策略
	if err := SetupPageSecurity(page, m.security); err != nil {
		log.Printf("[BrowserManager] 页面安全设置失败: %v", err)
		page.Close()
		<-m.sem
		return nil, nil, err
	}

	// 更新统计
	m.stats.mu.Lock()
	m.stats.TotalAcquired++
	m.stats.ActivePages++
	m.stats.mu.Unlock()

	// 构建释放函数
	released := false
	release := func() {
		if released {
			return
		}
		released = true

		// 关闭页面
		if page != nil {
			page.Close()
		}

		// 更新统计
		m.stats.mu.Lock()
		m.stats.ActivePages--
		m.stats.TotalReleased++
		m.stats.mu.Unlock()

		// 释放信号量
		<-m.sem

		// 更新最后使用时间
		m.mu.Lock()
		m.lastUsed = time.Now()
		m.mu.Unlock()

		// 启动空闲计时器
		m.resetIdleTimer()
	}

	return page, release, nil
}

// launch 启动浏览器（内部方法，调用前需持有锁）
func (m *Manager) launch() error {
	if m.closed {
		return errors.New("管理器已关闭")
	}

	// 处理用户数据目录
	userDataDir := m.cfg.UserDataDir
	if userDataDir == "" {
		var err error
		userDataDir, err = GetTempUserDataDir()
		if err != nil {
			return fmt.Errorf("创建临时用户数据目录失败: %w", err)
		}
		m.tempDir = userDataDir
	}

	// 构建启动器
	l := launcher.New()

	// 设置浏览器路径
	if m.cfg.BrowserPath != "" {
		l = l.Bin(m.cfg.BrowserPath)
	}

	// 设置无头模式
	if m.cfg.Headless {
		l = l.HeadlessNew(true)
	} else {
		l = l.Headless(false)
	}

	// 构建标志
	chromeFlags := BuildChromeFlags(m.cfg, userDataDir)

	// 设置标志（解析 --flag=value 格式）
	for _, f := range chromeFlags {
		// 跳过 --headless 相关标志，因为已经通过 HeadlessNew/Headless 单独设置
		cleanFlag := strings.TrimPrefix(f, "--")
		if strings.HasPrefix(strings.ToLower(cleanFlag), "headless") {
			continue
		}

		// 解析 name=value 格式
		if eqIdx := strings.Index(cleanFlag, "="); eqIdx != -1 {
			name := cleanFlag[:eqIdx]
			value := cleanFlag[eqIdx+1:]
			l = l.Set(flags.Flag(name), value)
		} else {
			l = l.Set(flags.Flag(cleanFlag))
		}
	}

	log.Printf("[BrowserManager] 浏览器启动标志: %v", chromeFlags)

	// 启动浏览器并获取 WebSocket URL
	url, err := l.Launch()
	if err != nil {
		return fmt.Errorf("启动浏览器失败: %w", err)
	}
	m.browserURL = url

	// 连接到浏览器
	browser := rod.New().ControlURL(url)
	if err := browser.Connect(); err != nil {
		return fmt.Errorf("连接浏览器失败: %w", err)
	}

	m.browser = browser
	m.launcher = l
	m.lastUsed = time.Now()

	// 启动空闲计时器
	m.resetIdleTimer()

	log.Printf("[BrowserManager] 浏览器连接成功，WebSocket URL: %s", sanitizeURL(url))
	return nil
}

// restart 重启浏览器（内部方法，调用前需持有锁）
func (m *Manager) restart() error {
	log.Printf("[BrowserManager] 正在重启浏览器...")
	m.stats.RestartCount++

	// 关闭现有浏览器
	if m.browser != nil {
		m.browser.Close()
		m.browser = nil
	}

	// 重新启动
	return m.launch()
}

// ping 通过 CDP ping 检查浏览器是否存活
func (m *Manager) ping(ctx context.Context) error {
	if m.browser == nil {
		return nil // 尚未启动，不算错误
	}

	// 使用 CDP 命令检查连接
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	_, err := m.browser.Call(pingCtx, "", "Browser.getVersion", &proto.BrowserGetVersion{})
	if err != nil {
		return fmt.Errorf("browser ping failed: %w", err)
	}

	return nil
}

// HealthCheck 公开的健康检查方法
func (m *Manager) HealthCheck() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.browser == nil {
		return nil // 尚未启动
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := m.ping(ctx); err != nil {
		log.Printf("[BrowserManager] 健康检查失败: %v", err)
		return err
	}

	return nil
}

// resetIdleTimer 重置空闲计时器
func (m *Manager) resetIdleTimer() {
	if m.idleTimer != nil {
		m.idleTimer.Stop()
	}

	if m.idleTimeout <= 0 {
		return
	}

	m.idleTimer = time.AfterFunc(m.idleTimeout, func() {
		m.mu.Lock()
		defer m.mu.Unlock()

		// 检查是否真的空闲（没有活跃页面 + 超过空闲时间）
		m.stats.mu.Lock()
		activePages := m.stats.ActivePages
		m.stats.mu.Unlock()

		if activePages == 0 && time.Since(m.lastUsed) >= m.idleTimeout {
			log.Printf("[BrowserManager] 浏览器空闲超时 (%v)，自动关闭", m.idleTimeout)
			m.closeBrowser()
		}
	})
}

// closeBrowser 关闭浏览器（内部方法，调用前需持有锁）
func (m *Manager) closeBrowser() {
	if m.browser != nil {
		m.browser.Close()
		m.browser = nil
		log.Printf("[BrowserManager] 浏览器已关闭")
	}
}

// Close 优雅关闭浏览器管理器
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return nil
	}

	m.closed = true
	close(m.closeCh)

	// 停止空闲计时器
	if m.idleTimer != nil {
		m.idleTimer.Stop()
	}

	// 关闭浏览器
	m.closeBrowser()

	// 清理临时目录
	if m.tempDir != "" {
		if err := os.RemoveAll(m.tempDir); err != nil {
			log.Printf("[BrowserManager] 清理临时目录失败: %v", err)
		}
		m.tempDir = ""
	}

	log.Printf("[BrowserManager] 浏览器管理器已关闭")
	return nil
}

// GetStats 获取管理器统计信息
func (m *Manager) GetStats() ManagerStats {
	m.stats.mu.Lock()
	defer m.stats.mu.Unlock()
	return ManagerStats{
		TotalAcquired: m.stats.TotalAcquired,
		TotalReleased: m.stats.TotalReleased,
		ActivePages:   m.stats.ActivePages,
		CrashCount:    m.stats.CrashCount,
		RestartCount:  m.stats.RestartCount,
	}
}

// GetSecurityPolicy 获取安全策略
func (m *Manager) GetSecurityPolicy() *SecurityPolicy {
	return m.security
}

// GetConfig 获取配置
func (m *Manager) GetConfig() BrowserCfg {
	return m.cfg
}

// IsRunning 检查浏览器是否在运行
func (m *Manager) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.browser != nil && !m.closed
}

// GetBrowserURL 获取浏览器 WebSocket URL
func (m *Manager) GetBrowserURL() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.browserURL
}

// PageCtxKey 用于在 context 中传递 rod.Page 的 key
type contextKey string

const PageCtxKey contextKey = "browser_page"

// sanitizeURL 对 URL 进行脱敏处理（隐藏路径中的敏感信息）
func sanitizeURL(rawURL string) string {
	// 只返回前缀部分，隐藏具体的调试端口路径
	if idx := strings.Index(rawURL, "/devtools"); idx != -1 {
		return rawURL[:idx] + "/devtools/..."
	}
	return rawURL
}
