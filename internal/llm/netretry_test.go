package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"syscall"
	"testing"

	"github.com/openai/openai-go/v3"
)

// TestIsTransientNetworkError 表驱动测试：瞬时网络错误分类（不访问网络）。
func TestIsTransientNetworkError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		// ── 可重试：连接被拒/重置等 errno（经 OpError 包装，模拟真实 dial/read 失败）──
		{"conn refused via OpError", fmt.Errorf("dial tcp 1.2.3.4:443: %w", &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}), true},
		{"conn reset errno", fmt.Errorf("read tcp: %w", syscall.ECONNRESET), true},
		{"broken pipe errno", fmt.Errorf("write tcp: %w", syscall.EPIPE), true},
		{"host unreachable", fmt.Errorf("dial tcp: %w", syscall.EHOSTUNREACH), true},
		{"errno etimedout", fmt.Errorf("dial tcp: %w", syscall.ETIMEDOUT), true},

		// ── 可重试：DNS 错误 ──
		{"dns no such host", &net.DNSError{Err: "no such host", Name: "api.example.com"}, true},
		{"dns temporary", &net.DNSError{Err: "temporary failure in name resolution", Name: "api.example.com", IsTemporary: true}, true},

		// ── 可重试：连接中断/响应截断 ──
		{"unexpected eof", fmt.Errorf("read: %w", io.ErrUnexpectedEOF), true},
		{"io eof", fmt.Errorf("stream ended: %w", io.EOF), true},
		{"conn reset by peer string", errors.New("read tcp 1.2.3.4:443: connection reset by peer"), true},
		{"http2 goaway", errors.New("http2: server sent GOAWAY and closed the connection"), true},
		{"use of closed network conn", errors.New("read: use of closed network connection"), true},
		{"server closed idle connection", errors.New("net/http: server closed idle connection"), true},
		{"tls handshake timeout", errors.New("net/http: TLS handshake timeout"), true},
		{"broken pipe string", errors.New("write: broken pipe"), true},
		{"no such host string", errors.New("dial tcp: lookup api.example.com: no such host"), true},
		{"conn lost string", errors.New("http2: connection lost mid-stream"), true},

		// ── 可重试：超时类 ──
		{"context deadline", fmt.Errorf("call: %w", context.DeadlineExceeded), true},
		{"os timeout sentinel", os.ErrDeadlineExceeded, true},
		{"op error with timeout", &net.OpError{Op: "read", Err: os.ErrDeadlineExceeded}, true},

		// ── 不可重试 ──
		{"nil error", nil, false},
		{"context canceled", context.Canceled, false},
		{"wrapped canceled", fmt.Errorf("llm call: %w", context.Canceled), false},
		{"http 401", errors.New("401 invalid api key"), false},
		{"http 400", errors.New("400 invalid_request: max_tokens is too large"), false},
		{"json syntax error", &json.SyntaxError{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTransientNetworkError(tt.err); got != tt.want {
				t.Errorf("isTransientNetworkError(%v) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

// TestIsRetriableError_OpenAIStatus 覆盖 openai.Error 状态码分支。
// openai.Error 的 StatusCode/Request/Response 字段均可导出构造，可直接使用。
// 注意：4xx（不可重试）用例必须填充 Request/Response，
// 因为 SDK 的 (*Error).Error() 会解引用这两个字段（nil 时 panic）。
func TestIsRetriableError_OpenAIStatus(t *testing.T) {
	// 最小化 Request/Response，保证 (*Error).Error() 可安全调用
	safeAPIErr := func(status int) *openai.Error {
		return &openai.Error{
			StatusCode: status,
			Request: &http.Request{
				Method: http.MethodPost,
				URL:    &url.URL{Scheme: "https", Host: "api.example.com", Path: "/v1/chat/completions"},
			},
			Response: &http.Response{StatusCode: status},
		}
	}

	tests := []struct {
		name string
		err  error
		want bool
	}{
		// 429/5xx 在 StatusCode 分支直接返回，不调用 Error()，可省略 Request/Response
		{"429 rate limit", &openai.Error{StatusCode: 429}, true},
		{"500 server error", &openai.Error{StatusCode: 500}, true},
		{"503 unavailable", &openai.Error{StatusCode: 503}, true},
		// 4xx 走完整个链路落到字符串兜底，需要完整构造
		{"400 bad request", safeAPIErr(400), false},
		{"401 invalid key", safeAPIErr(401), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRetriableError(tt.err); got != tt.want {
				t.Errorf("isRetriableError(%s) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

// TestIsHTTPRetriableError 验证 Anthropic 路径复用同一分类函数。
func TestIsHTTPRetriableError(t *testing.T) {
	if !isHTTPRetriableError(fmt.Errorf("dial tcp: %w", &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED})) {
		t.Errorf("isHTTPRetriableError(conn refused) = false, want true")
	}
	if !isHTTPRetriableError(fmt.Errorf("read: %w", io.ErrUnexpectedEOF)) {
		t.Errorf("isHTTPRetriableError(unexpected EOF) = false, want true")
	}
	if isHTTPRetriableError(context.Canceled) {
		t.Errorf("isHTTPRetriableError(context.Canceled) = true, want false")
	}
	if isHTTPRetriableError(nil) {
		t.Errorf("isHTTPRetriableError(nil) = true, want false")
	}
}
