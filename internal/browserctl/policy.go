package browserctl

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
)

// Policy 是浏览器侧的访问守卫。
// 只拦"参数里的 URL"是不够的：页面内点一个链接就能跳走，
// 所以除 CheckURL 外还提供 BlockedURLPatterns，用于在会话建立时让浏览器自己阻断请求。
type Policy struct {
	Allowed []string
	Denied  []string
}

func NewPolicy(allowed, denied []string) *Policy {
	return &Policy{Allowed: allowed, Denied: denied}
}

// CheckURL 校验一个即将导航的目标地址。
// 只允许 http/https：file:// 会让浏览器变成绕过 WorkspaceGuard 读本地文件的通道。
func (p *Policy) CheckURL(raw string) error {
	target := strings.TrimSpace(raw)
	if target == "" {
		return fmt.Errorf("url 不能为空")
	}

	u, err := url.Parse(target)
	if err != nil {
		return fmt.Errorf("url 无法解析: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("只允许 http/https 导航（%q 被拒绝）", scheme)
	}

	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("url 缺少主机名: %s", raw)
	}

	if p == nil {
		return nil
	}
	for _, d := range p.Denied {
		if matchHost(d, host) {
			return fmt.Errorf("目标主机 %s 命中拒绝列表（%s）", host, d)
		}
	}
	if len(p.Allowed) > 0 {
		for _, a := range p.Allowed {
			if matchHost(a, host) {
				return nil
			}
		}
		return fmt.Errorf("目标主机 %s 不在 [browser] allowed_domains 白名单内", host)
	}
	return nil
}

// BlockedURLPatterns 产出 CDP Network.setBlockedURLs 需要的 URL 通配。
// CIDR 条目无法用 URL 通配表达，只由 CheckURL 承担 —— 而 CheckURL 只在导航入参上跑一次，
// 页面内跳转/子资源不会被 Go 侧复检，所以 CIDR 的实际拦截力只有浏览器侧那一份通配。
func (p *Policy) BlockedURLPatterns() []string {
	if p == nil {
		return nil
	}
	seen := map[string]bool{}
	out := []string{}
	add := func(pattern string) {
		if !seen[pattern] {
			seen[pattern] = true
			out = append(out, pattern)
		}
	}
	// 无条件阻断本地文件协议
	add("file://*")
	for _, d := range p.Denied {
		if strings.Contains(d, "://") || strings.Contains(d, "/") {
			continue
		}
		add("*://" + d + "/*")
	}
	return out
}

// BlockedURLsPayload 生成 setBlockedURLs 的参数体。
func (p *Policy) BlockedURLsPayload() (string, error) {
	patterns := p.BlockedURLPatterns()
	if len(patterns) == 0 {
		return "", nil
	}
	b, err := json.Marshal(map[string]interface{}{"urls": patterns})
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// matchHost 支持四种写法：`*.example.com` 通配、裸域名、CIDR 网段、IPv4/IPv6 字面量。
func matchHost(pattern, host string) bool {
	p := strings.ToLower(strings.TrimSpace(pattern))
	h := strings.ToLower(strings.TrimSpace(host))
	if p == "" || h == "" {
		return false
	}
	if strings.Contains(p, "://") {
		return false
	}
	if strings.Contains(p, "/") {
		ip := net.ParseIP(h)
		if ip == nil {
			return false
		}
		_, ipnet, err := net.ParseCIDR(p)
		return err == nil && ipnet.Contains(ip)
	}
	if strings.HasPrefix(p, "*.") {
		suffix := strings.TrimPrefix(p, "*.")
		return h == suffix || strings.HasSuffix(h, "."+suffix)
	}
	return h == p
}

// riskyJS 是需要用户确认的 JS 特征：读写凭据/存储、发起网络请求、提交表单、动态执行。
// 只按特征拦，是为了不把 `location.reload()` 这类良性逃生口也变成弹窗。
var riskyJS = []struct {
	pattern *regexp.Regexp
	reason  string
}{
	{regexp.MustCompile(`(?i)document\s*\.\s*cookie`), "读写 cookie"},
	{regexp.MustCompile(`(?i)(localStorage|sessionStorage|indexedDB)`), "读写浏览器存储"},
	{regexp.MustCompile(`(?i)\bfetch\s*\(`), "发起网络请求"},
	{regexp.MustCompile(`(?i)XMLHttpRequest`), "发起网络请求"},
	{regexp.MustCompile(`(?i)sendBeacon`), "外发数据"},
	{regexp.MustCompile(`(?i)\.\s*submit\s*\(`), "提交表单"},
	{regexp.MustCompile(`(?i)window\s*\.\s*open`), "打开新窗口"},
	{regexp.MustCompile(`(?i)serviceWorker`), "注册 Service Worker"},
	{regexp.MustCompile(`(?i)\beval\s*\(|new\s+Function\s*\(`), "动态执行代码"},
	{regexp.MustCompile(`(?i)document\s*\.\s*write`), "改写文档"},
}

// RiskyJS 判断一段 JS 是否触及敏感能力，返回命中的原因（供授权提示展示）。
func RiskyJS(js string) (bool, string) {
	for _, r := range riskyJS {
		if r.pattern.MatchString(js) {
			return true, r.reason
		}
	}
	return false, ""
}
