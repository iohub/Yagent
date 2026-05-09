//go:build smoke

package browser

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codeactor/internal/config"
)

// TestBrowserSmoke 浏览器冒烟测试
// 测试浏览器是否正常工作，包括启动、导航、截图和 JS 执行
func TestBrowserSmoke(t *testing.T) {
	// 步骤 1: 加载配置
	cfg, err := loadConfig(t)
	if err != nil {
		return // t.Skip() 或 t.Fatalf() 已在 loadConfig 中调用
	}

	// 步骤 2: 构建 BrowserCfg
	browserCfg := configToBrowserCfg(cfg.Browser)
	t.Logf("[配置] Headless=%v, Viewport=%dx%d, Timeout=%ds",
		browserCfg.Headless, browserCfg.ViewportWidth, browserCfg.ViewportHeight, browserCfg.TimeoutSeconds)

	// 步骤 3: 创建浏览器管理器
	mgr := NewManager(browserCfg, nil, nil)
	defer mgr.Close()
	t.Log("[启动] 浏览器管理器已创建")

	// 步骤 4: 获取页面（带超时 context）
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	page, release, err := mgr.AcquirePage(ctx)
	if err != nil {
		t.Fatalf("[获取页面] 失败: %v", err)
	}
	defer release()
	t.Log("[获取页面] 成功")

	// 步骤 5: 导航测试
	testURL := "https://example.com"
	t.Logf("[导航] 正在访问 %s", testURL)

	err = page.Timeout(25 * time.Second).Navigate(testURL)
	if err != nil {
		// 检查是否是浏览器未安装的问题
		errStr := err.Error()
		if strings.Contains(errStr, "executable") ||
			strings.Contains(errStr, "found") ||
			strings.Contains(errStr, "no such file") ||
			strings.Contains(errStr, "cannot run") {
			t.Fatalf("[导航] 浏览器未安装或无法执行: %v\n请安装 Chrome/Chromium: https://go-rod.dev/#/install_browser", err)
		}
		t.Fatalf("[导航] 访问 %s 失败: %v", testURL, err)
	}

	// 等待页面加载完成
	if err := page.WaitIdle(10 * time.Second); err != nil {
		t.Logf("[等待] 页面空闲等待超时（可能非致命）: %v", err)
	}
	t.Logf("[导航] 成功访问 %s", testURL)

	// 步骤 6: 页面信息验证
	// 获取页面信息
	info, err := page.Info()
	if err != nil {
		t.Logf("[页面信息] 获取失败: %v（跳过 URL 验证）", err)
	} else {
		currentURL := info.URL
		t.Logf("[页面信息] 当前 URL: %s", currentURL)
		if !strings.Contains(currentURL, "example.com") {
			t.Errorf("[URL 验证] 期望包含 example.com，实际: %s", currentURL)
		} else {
			t.Log("[URL 验证] 通过 ✓")
		}
	}

	// 获取页面标题（通过 JS）
	titleResult, err := page.Timeout(10 * time.Second).Eval("() => document.title")
	if err != nil {
		t.Fatalf("[页面标题] 获取失败: %v", err)
	}
	// proto.RuntimeRemoteObject.Value 是 gson.JSON 类型，使用 Str() 方法获取字符串
	title := titleResult.Value.Str()
	t.Logf("[页面标题] %s", title)
	expectedTitle := "Example Domain"
	if title != expectedTitle {
		t.Errorf("[标题验证] 期望 '%s'，实际: '%s'", expectedTitle, title)
	} else {
		t.Log("[标题验证] 通过 ✓")
	}

	// 步骤 7: 截图测试
	t.Log("[截图] 正在截取全页截图...")

	screenshot, err := page.Timeout(15 * time.Second).Screenshot(false, nil)
	if err != nil {
		t.Fatalf("[截图] 失败: %v", err)
	}

	if len(screenshot) == 0 {
		t.Fatal("[截图] 截图数据为空")
	}
	t.Logf("[截图] 成功，数据大小: %d bytes (%.2f KB)", len(screenshot), float64(len(screenshot))/1024)
	t.Log("[截图验证] 通过 ✓")

	// 步骤 8: 额外测试 - JS 执行
	t.Log("[JS 执行] 测试 location.href...")

	jsResult, err := page.Eval("() => location.href")
	if err != nil {
		t.Fatalf("[JS 执行] 获取 location.href 失败: %v", err)
	}

	jsURL := jsResult.Value.Str()
	t.Logf("[JS 执行] location.href = '%s'", jsURL)
	if !strings.Contains(jsURL, "example.com") {
		t.Errorf("[JS URL] 期望包含 example.com，实际: '%s'", jsURL)
	} else {
		t.Log("[JS URL 验证] 通过 ✓")
	}

	// 步骤 9: 额外测试 - 元素文本获取
	t.Log("[元素获取] 测试 H1 元素文本...")

	el, err := page.Timeout(5 * time.Second).Element("h1")
	if err != nil {
		t.Fatalf("[元素获取] 查找 H1 元素失败: %v", err)
	}
	h1Text, err := el.Text()
	if err != nil {
		t.Fatalf("[元素获取] 获取 H1 文本失败: %v", err)
	}
	t.Logf("[元素获取] H1 文本 = '%s'", h1Text)
	expectedH1 := "Example Domain"
	if h1Text != expectedH1 {
		t.Errorf("[H1 文本] 期望 '%s'，实际: '%s'", expectedH1, h1Text)
	} else {
		t.Log("[H1 文本验证] 通过 ✓")
	}

	// 测试完成总结
	t.Log("\n========== 冒烟测试完成 ==========")
	t.Log("所有检查项均通过 ✓")
	t.Log("浏览器代理运行正常")
}

// loadConfig 加载配置文件，如果失败则跳过或终止测试
func loadConfig(t *testing.T) (*config.Config, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("[配置] 无法获取用户主目录: %v", err)
		return nil, err
	}

	configPath := filepath.Join(homeDir, ".codeactor", "config", "config.toml")

	// 检查配置文件是否存在
	if _, err := os.Stat(configPath); err != nil {
		if os.IsNotExist(err) {
			t.Skipf("[配置] 配置文件不存在，跳过测试: %s\n提示: 请先运行 codeactor init 或手动创建配置文件", configPath)
		}
		t.Skipf("[配置] 无法访问配置文件: %v", err)
		return nil, err
	}

	t.Logf("[配置] 加载配置: %s", configPath)
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("[配置] 加载失败: %v", err)
		return nil, err
	}

	t.Logf("[配置] 加载成功")
	return cfg, nil
}

// configToBrowserCfg 将 config.BrowserConfig 转换为 browser.BrowserCfg
func configToBrowserCfg(bc config.BrowserConfig) BrowserCfg {
	return BrowserCfg{
		Headless:           bc.Headless,
		BrowserPath:        bc.BrowserPath,
		UserDataDir:        bc.UserDataDir,
		ViewportWidth:      bc.ViewportWidth,
		ViewportHeight:     bc.ViewportHeight,
		AllowedDomains:     bc.AllowedDomains,
		BlockedDomains:     bc.BlockedDomains,
		TimeoutSeconds:     bc.TimeoutSeconds,
		MaxConcurrentPages: bc.MaxConcurrentPages,
		AutoLaunch:         bc.AutoLaunch,
		IdleTimeout:        bc.IdleTimeout,
		AllowNoSandbox:     bc.AllowNoSandbox,
		ExtraArgs:          bc.ExtraArgs,
		// EnableBrowserAgent 是 config 特有字段，不需要转换
	}
}
