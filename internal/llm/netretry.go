package llm

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"syscall"
)

// transientNetErrs 常见瞬时网络 errno，通过 errors.Is 匹配包装链。
var transientNetErrs = []error{
	syscall.ECONNRESET,
	syscall.ECONNREFUSED,
	syscall.ECONNABORTED,
	syscall.EHOSTUNREACH,
	syscall.ENETUNREACH,
	syscall.ENETDOWN,
	syscall.EPIPE,
	syscall.ETIMEDOUT,
}

// transientNetErrKeywords 字符串兜底关键词（需先 ToLower），
// 覆盖无法用 errors.Is 捕获的传输层错误（keep-alive 断开、HTTP/2 GOAWAY 等）。
var transientNetErrKeywords = []string{
	"connection reset",
	"connection refused",
	"broken pipe",
	"no route to host",
	"network is unreachable",
	"network is down",
	"no such host",
	"server closed idle connection",
	"connection lost",
	"goaway",
	"tls handshake timeout",
	"unexpected eof",
	"use of closed network connection",
}

// isTransientNetworkError 判断是否为可重试的瞬时网络/传输错误。
// 注意：context.Canceled（用户主动取消）永远不可重试。
func isTransientNetworkError(err error) bool {
	if err == nil {
		return false
	}

	// 用户主动取消，绝不重试
	if errors.Is(err, context.Canceled) {
		return false
	}

	// 超时类：context deadline / os.IsTimeout
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if os.IsTimeout(err) {
		return true
	}

	// 服务端断开连接 / 响应截断（keep-alive、流式读取中断）
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	// net.Error 的 Timeout()/Temporary()（dialer、TLS 握手超时等）
	var netErr net.Error
	if errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary()) {
		return true
	}

	// 任何 DNS 错误（no such host、临时失败等）
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}

	// 常见瞬时 errno
	for _, target := range transientNetErrs {
		if errors.Is(err, target) {
			return true
		}
	}

	// 字符串兜底
	msg := strings.ToLower(err.Error())
	for _, kw := range transientNetErrKeywords {
		if strings.Contains(msg, kw) {
			return true
		}
	}

	return false
}
