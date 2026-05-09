package browser

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// SecurityPolicy 浏览器安全策略
type SecurityPolicy struct {
	AllowedDomains  []string // 允许访问的域名列表（空=全部允许）
	BlockedDomains  []string // 阻止访问的域名列表
	AllowFileAccess bool     // 是否允许 file:// 协议（默认 false）
	AllowDataURL    bool     // 是否允许 data: URL（默认 false）
}

// NewSecurityPolicy 创建安全策略
func NewSecurityPolicy(allowedDomains, blockedDomains []string) *SecurityPolicy {
	return &SecurityPolicy{
		AllowedDomains:  allowedDomains,
		BlockedDomains:  blockedDomains,
		AllowFileAccess: false,
		AllowDataURL:    false,
	}
}

// ValidateURL 验证 URL 安全性
// 返回错误如果 URL 不被允许
func (sp *SecurityPolicy) ValidateURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("无效的 URL: %w", err)
	}

	// 检查协议
	scheme := strings.ToLower(parsed.Scheme)
	switch scheme {
	case "http", "https":
		// 允许，继续检查域名
	case "file":
		if !sp.AllowFileAccess {
			return fmt.Errorf("file:// 协议不允许访问")
		}
	case "data":
		if !sp.AllowDataURL {
			return fmt.Errorf("data: URL 不允许访问")
		}
	default:
		return fmt.Errorf("不允许的协议: %s，仅支持 http/https", scheme)
	}

	// 检查域名
	hostname := strings.ToLower(parsed.Hostname())
	if hostname == "" {
		// 对于 file:// 或没有主机名的 URL，跳过域名检查
		if scheme == "http" || scheme == "https" {
			return fmt.Errorf("URL 缺少主机名: %s", rawURL)
		}
		return nil
	}

	// 检查阻止列表
	for _, blocked := range sp.BlockedDomains {
		if matchDomain(hostname, blocked) {
			return fmt.Errorf("域名 %s 在阻止列表中 (匹配规则: %s)", hostname, blocked)
		}
	}

	// 如果配置了允许列表，检查是否在列表中
	if len(sp.AllowedDomains) > 0 {
		for _, allowed := range sp.AllowedDomains {
			if matchDomain(hostname, allowed) {
				return nil
			}
		}
		return fmt.Errorf("域名 %s 不在允许列表中", hostname)
	}

	return nil
}

// matchDomain 检查域名是否匹配规则（支持通配符 *.example.com）
func matchDomain(hostname, pattern string) bool {
	hostname = strings.ToLower(hostname)
	pattern = strings.ToLower(pattern)

	// 精确匹配
	if hostname == pattern {
		return true
	}

	// 通配符匹配: *.example.com
	if strings.HasPrefix(pattern, "*.") {
		suffix := pattern[1:] // .example.com
		return strings.HasSuffix(hostname, suffix)
	}

	return false
}

// ShouldBlockRequest 判断是否应阻止请求（用于 HijackRequests）
func (sp *SecurityPolicy) ShouldBlockRequest(reqURL string) bool {
	parsed, err := url.Parse(reqURL)
	if err != nil {
		return true // 解析失败则阻止
	}

	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return true
	}

	hostname := strings.ToLower(parsed.Hostname())

	// 检查阻止列表
	for _, blocked := range sp.BlockedDomains {
		if matchDomain(hostname, blocked) {
			return true
		}
	}

	// 如果配置了允许列表
	if len(sp.AllowedDomains) > 0 {
		for _, allowed := range sp.AllowedDomains {
			if matchDomain(hostname, allowed) {
				return false
			}
		}
		return true // 不在允许列表中
	}

	return false
}

// SetupPageSecurity 为页面设置安全路由器
// 使用 rod 的 HijackRequests 拦截不允许的请求
func SetupPageSecurity(page *rod.Page, sp *SecurityPolicy) error {
	if page == nil {
		return fmt.Errorf("页面为空")
	}

	// 使用 router 拦截请求
	router := page.HijackRequests()
	if router == nil {
		return fmt.Errorf("无法创建请求路由器")
	}

	// 必须调用 router.MustAdd 或 router.Add 来添加规则
	// 拦截所有请求，检查是否应阻止
	router.MustAdd("*", func(ctx *rod.Hijack) {
		reqURL := ctx.Request.URL().String()
		if sp.ShouldBlockRequest(reqURL) {
			ctx.Response.Fail(proto.NetworkErrorReasonBlockedByClient)
			return
		}
		ctx.ContinueRequest(&proto.FetchContinueRequest{})
	})

	go router.Run()
	return nil
}

// ValidateFilePath 验证输出文件路径是否在工作区目录内
func ValidateFilePath(outputPath string) error {
	wsDir := GetWorkspaceDir()
	if wsDir == "" {
		return fmt.Errorf("工作区目录未设置")
	}

	// 解析为绝对路径
	absPath, err := filepath.Abs(outputPath)
	if err != nil {
		return fmt.Errorf("无法解析文件路径: %w", err)
	}

	absWsDir, err := filepath.Abs(wsDir)
	if err != nil {
		return fmt.Errorf("无法解析工作区目录: %w", err)
	}

	// 确保路径在工作区内
	relPath, err := filepath.Rel(absWsDir, absPath)
	if err != nil {
		return fmt.Errorf("路径不在工作区内: %w", err)
	}

	if strings.HasPrefix(relPath, "..") {
		return fmt.Errorf("文件路径不在工作区目录内: %s", outputPath)
	}

	return nil
}

// SanitizeURL 对 URL 进行日志安全的脱敏处理
func SanitizeURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "[无效URL]"
	}
	// 移除敏感查询参数
	parsed.RawQuery = ""
	parsed.Fragment = ""
	// 截断路径
	if len(parsed.Path) > 50 {
		parsed.Path = parsed.Path[:50] + "..."
	}
	return parsed.String()
}
