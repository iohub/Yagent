package browser

import (
	"fmt"
	"os"
	"path/filepath"
)

// DefaultChromeFlags 返回安全的 Chrome 启动标志列表
func DefaultChromeFlags() []string {
	return []string{
		"--headless=new",                // 新版无头模式
		"--disable-gpu",                 // 禁用 GPU
		"--no-first-run",                // 跳过首次运行向导
		"--disable-default-apps",        // 禁用默认应用
		"--disable-extensions",          // 禁用扩展
		"--disable-background-networking", // 禁用后台网络
		"--disable-sync",                // 禁用同步
		"--disable-translate",           // 禁用翻译
		"--hide-scrollbars",             // 隐藏滚动条
		"--metrics-recording-only",      // 仅记录指标
		"--mute-audio",                  // 静音
		"--disable-dev-shm-usage",       // 使用 /tmp 而非 /dev/shm（Docker兼容）
	}
}

// BuildChromeFlags 根据配置构建完整的 Chrome 启动标志
func BuildChromeFlags(cfg BrowserCfg, userDataDir string) []string {
	flags := DefaultChromeFlags()

	// 视口大小
	if cfg.ViewportWidth > 0 && cfg.ViewportHeight > 0 {
		flags = append(flags, fmt.Sprintf("--window-size=%d,%d", cfg.ViewportWidth, cfg.ViewportHeight))
	}

	// 用户数据目录
	if userDataDir != "" {
		flags = append(flags, fmt.Sprintf("--user-data-dir=%s", userDataDir))
	}

	// 无沙盒模式（Docker 环境需要）
	if cfg.AllowNoSandbox {
		flags = append(flags, "--no-sandbox")
	}

	// JS 内存限制
	flags = append(flags, "--js-flags=--max-old-space-size=256")

	// 渲染进程限制
	flags = append(flags, "--renderer-process-limit=4")

	// 额外参数
	flags = append(flags, cfg.ExtraArgs...)

	return flags
}

// BrowserCfg 浏览器配置接口（避免循环依赖）
type BrowserCfg struct {
	Headless           bool
	BrowserPath        string
	UserDataDir        string
	ViewportWidth      int
	ViewportHeight     int
	AllowedDomains     []string
	BlockedDomains     []string
	TimeoutSeconds     int
	MaxConcurrentPages int
	AutoLaunch         bool
	IdleTimeout        string
	AllowNoSandbox     bool
	ExtraArgs          []string
}

// GetTempUserDataDir 创建临时用户数据目录
func GetTempUserDataDir() (string, error) {
	tmpDir, err := os.MkdirTemp("", "codeactor-browser-*")
	if err != nil {
		return "", fmt.Errorf("创建临时用户数据目录失败: %w", err)
	}
	return tmpDir, nil
}

// DefaultBrowserConfig 返回默认浏览器配置
func DefaultBrowserConfig() BrowserCfg {
	return BrowserCfg{
		Headless:           true,
		ViewportWidth:      1280,
		ViewportHeight:     720,
		TimeoutSeconds:     30,
		MaxConcurrentPages: 4,
		AutoLaunch:         true,
		IdleTimeout:        "5m",
		AllowNoSandbox:     false,
	}
}

// workspaceDir 用于文件保存的基础目录，由 Manager 设置
var workspaceDir string

// SetWorkspaceDir 设置工作区目录
func SetWorkspaceDir(dir string) {
	workspaceDir = dir
}

// GetWorkspaceDir 获取工作区目录
func GetWorkspaceDir() string {
	return workspaceDir
}

// GetBrowserOutputDir 获取浏览器输出目录（截图、PDF等）
func GetBrowserOutputDir() string {
	dir := filepath.Join(workspaceDir, "browser")
	os.MkdirAll(dir, 0755)
	return dir
}

// GetScreenshotsDir 获取截图目录
func GetScreenshotsDir() string {
	dir := filepath.Join(GetBrowserOutputDir(), "screenshots")
	os.MkdirAll(dir, 0755)
	return dir
}

// GetPDFsDir 获取PDF目录
func GetPDFsDir() string {
	dir := filepath.Join(GetBrowserOutputDir(), "pdfs")
	os.MkdirAll(dir, 0755)
	return dir
}
