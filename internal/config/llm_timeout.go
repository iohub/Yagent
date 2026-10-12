// 本文件维护 LLM API 调用超时的运行时全局值。
//
// 背景：LLM 调用超时的默认值分散在配置默认值与多处兜底常量中，且引擎持有
// config.LLMConfig 的值拷贝，运行期修改配置文件无法生效。该全局值提供
// "运行时唯一事实源"：
//   - 启动时由配置加载链路（internal/llm 的 LoadConfig / NewClient）初始化；
//   - TUI 模型设置面板可动态修改（唯一写入口，atomic 保证并发安全，
//     不给引擎加锁）；
//   - 所有引擎（internal/llm）与执行层（internal/agents）在发起 LLM 调用时
//     按"全局值 > 局部配置 > DefaultLLMCallTimeout"的优先级取值。
package config

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// globalLLMTimeoutNS 保存全局 LLM API 调用超时（单位：纳秒）。
// 0 表示尚未初始化，读取方应回落到各自的局部配置或 DefaultLLMCallTimeout。
var globalLLMTimeoutNS atomic.Int64

// SetGlobalLLMTimeout 设置全局 LLM API 调用超时（运行时唯一写入口）。
// d <= 0 时拒绝并保留现值——把全局超时错误归零会导致 LLM 调用永久挂起。
func SetGlobalLLMTimeout(d time.Duration) {
	if d <= 0 {
		slog.Warn("config: refused to set global LLM timeout to non-positive value", "value", d)
		return
	}
	globalLLMTimeoutNS.Store(int64(d))
}

// GlobalLLMTimeout 返回全局 LLM API 调用超时；未初始化时返回 0，
// 调用方需自行回落到局部配置或 DefaultLLMCallTimeout。
func GlobalLLMTimeout() time.Duration {
	return time.Duration(globalLLMTimeoutNS.Load())
}

// FormatLLMTimeout 将时长格式化为 TOML/输出友好的复数单位字符串，
// 输出必须能被 time.ParseDuration 正确解析（如 "10m"、"1h30m"、"90s"、"1.5s"）。
// 规则：小时/分钟一律复数单位；不足一分钟取整秒；不足一秒保留毫秒精度。
func FormatLLMTimeout(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	rem := d
	var parts []string
	if h := rem / time.Hour; h > 0 {
		parts = append(parts, fmt.Sprintf("%dh", h))
		rem -= h * time.Hour
	}
	if m := rem / time.Minute; m > 0 {
		parts = append(parts, fmt.Sprintf("%dm", m))
		rem -= m * time.Minute
	}
	if rem <= 0 {
		return strings.Join(parts, "")
	}
	if rem%time.Second == 0 {
		parts = append(parts, fmt.Sprintf("%ds", rem/time.Second))
		return strings.Join(parts, "")
	}
	// 不足整秒：保留毫秒精度的小数秒（time.ParseDuration 可解析）
	secs := float64(rem) / float64(time.Second)
	parts = append(parts, strconv.FormatFloat(secs, 'g', -1, 64)+"s")
	return strings.Join(parts, "")
}
