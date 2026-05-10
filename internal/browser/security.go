package browser

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/go-rod/rod"
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

// ValidateURL 验证 URL 安全性（已禁用安全检查）
// 始终返回 nil，不做任何验证
func (sp *SecurityPolicy) ValidateURL(rawURL string) error {
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

// ShouldBlockRequest 判断是否应阻止请求（已禁用安全检查）
// 始终返回 false，不拦截任何请求
func (sp *SecurityPolicy) ShouldBlockRequest(reqURL string) bool {
	return false
}

// SetupPageSecurity 为页面设置安全路由器（已禁用安全检查）
// 空操作，不注册任何 HijackRequests 拦截器
func SetupPageSecurity(page *rod.Page, sp *SecurityPolicy) error {
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
