// 本文件维护流式响应"活着没有"的三个超时旋钮的运行时全局值与程序保证默认值。
//
// 背景：流式请求曾经只有一层"总预算"（ctx 的 [llm].timeout，默认 10 分钟）。连接被中间
// 设备静默丢弃、或 provider 卡在推理里不吐字节时，读循环会**永久阻塞**，只能等满那 10 分钟，
// 用户看到的是"死机后超时"。三个旋钮各管一段：
//
//   - first_byte_timeout     从发请求到收到响应头（http.Transport.ResponseHeaderTimeout）
//   - stream_idle_timeout    流内任意两个 chunk/SSE 事件之间的最长静默（引擎内的看门狗）
//   - retry_attempt_timeout  单次尝试的独立上限，0 = 不额外限制（只受总预算约束）
//
// 生效方式分两类，改动时别混：
//   - stream_idle_timeout / retry_attempt_timeout 由引擎在**每次调用**时现取，
//     运行期修改立即生效；
//   - first_byte_timeout 落在 http.Transport 上，而 Transport 是引擎构造期建的，
//     **改了要重启**（本文件仍提供全局值，供装配层在构造引擎时读取）。
package config

import (
	"log/slog"
	"sync/atomic"
	"time"
)

const (
	// DefaultLLMStreamIdleTimeout 是流内空闲看门狗的程序保证默认值。
	// 90s 的取法：正常推理型模型在思考阶段也可能几十秒不吐可见内容，但 SSE 的
	// keep-alive（anthropic 的 ping、多数网关的空注释行）通常 15~30s 一次，
	// 90s 足够容纳"慢但活着"的流，又能把"已经死了"的流在一分半内判出来。
	DefaultLLMStreamIdleTimeout = 90 * time.Second

	// DefaultLLMFirstByteTimeout 是"从发请求到收到响应头"的程序保证默认值。
	// 正常 provider 的首字节在几秒内到达；60s 已经非常宽容。
	DefaultLLMFirstByteTimeout = 60 * time.Second

	// minUsefulStreamTimeout 是三个旋钮的"写错单位"下限。
	// TOML 里把时长写成裸整数（`stream_idle_timeout = 90`）会被解码成 **90 纳秒**，
	// 照抄就是一个"每个流都当场判死"的配置。低于 1s 的取值对 LLM 流没有任何意义，
	// 一律按"没配"处理并告警（见 Config.normalizeStreamTimeouts）。
	minUsefulStreamTimeout = time.Second
)

// 三个全局值都用纳秒存。0 表示尚未初始化，读取方回落到 Default* 常量。
var (
	globalStreamIdleTimeoutNS atomic.Int64
	globalFirstByteTimeoutNS  atomic.Int64
)

// SetGlobalStreamIdleTimeout 设置全局流内空闲超时。
// d <= 0 时拒绝并保留现值：把看门狗关掉等于恢复"永久阻塞"那个缺陷。
func SetGlobalStreamIdleTimeout(d time.Duration) {
	if d <= 0 {
		slog.Warn("config: refused to disable the LLM stream idle watchdog", "value", d)
		return
	}
	globalStreamIdleTimeoutNS.Store(int64(d))
}

// GlobalStreamIdleTimeout 返回全局流内空闲超时；未初始化时返回 0，
// 调用方需回落到 DefaultLLMStreamIdleTimeout。
func GlobalStreamIdleTimeout() time.Duration {
	return time.Duration(globalStreamIdleTimeoutNS.Load())
}

// SetGlobalFirstByteTimeout 设置全局首字节超时。
// d <= 0 时拒绝并保留现值（理由同 SetGlobalStreamIdleTimeout）。
func SetGlobalFirstByteTimeout(d time.Duration) {
	if d <= 0 {
		slog.Warn("config: refused to disable the LLM first-byte timeout", "value", d)
		return
	}
	globalFirstByteTimeoutNS.Store(int64(d))
}

// GlobalFirstByteTimeout 返回全局首字节超时；未初始化时返回 0，
// 调用方需回落到 DefaultLLMFirstByteTimeout。
func GlobalFirstByteTimeout() time.Duration {
	return time.Duration(globalFirstByteTimeoutNS.Load())
}

// EffectiveStreamIdleTimeout 把"引擎自己配置的值 / 全局值 / 默认值"折成一个可直接
// 使用的正数时长。返回值恒 > 0 —— 看门狗拿到 0 就等于关掉，而关掉它正是要被修的缺陷。
//
// 优先级：configured > 全局值 > DefaultLLMStreamIdleTimeout。
// 与 [llm].timeout 的"全局 > 局部"相反，是因为这里的 configured 就是引擎构造时从
// 同一份配置里读到的**显式意图**（且 TUI 没有这个旋钮），让它说话更符合直觉；
// 全局值只服务于"用零值 LLMConfig 直接构造引擎"的路径（测试、第三方装配）。
func EffectiveStreamIdleTimeout(configured time.Duration) time.Duration {
	if configured > 0 {
		return configured
	}
	if g := GlobalStreamIdleTimeout(); g > 0 {
		return g
	}
	return DefaultLLMStreamIdleTimeout
}

// EffectiveFirstByteTimeout 同 EffectiveStreamIdleTimeout，折首字节超时。
func EffectiveFirstByteTimeout(configured time.Duration) time.Duration {
	if configured > 0 {
		return configured
	}
	if g := GlobalFirstByteTimeout(); g > 0 {
		return g
	}
	return DefaultLLMFirstByteTimeout
}

// normalizeStreamTimeouts 在 validate() 里把三个新旋钮归一化成"要么是合理正数、
// 要么是明确的 0（仅 retry_attempt_timeout 有 0 语义）"。
//
// 所有落回默认的分支都留一条 WARN：这三个旋钮出错的表现都是"超时判得莫名其妙"，
// 静默归一化会让人以为自己的配置生效了（根 AGENTS.md §9.2）。
func (c *Config) normalizeStreamTimeouts(defaults LLMConfig) {
	// 判据是「< 1s」而不是「<= 0」：负数与"写成裸整数后被解码成几十纳秒"同属
	// "这个值不可能是真的打算"，两者的处理与告警也该一致。
	if c.LLM.StreamIdleTimeout < minUsefulStreamTimeout {
		if c.LLM.StreamIdleTimeout != 0 {
			slog.Warn("[llm] stream_idle_timeout 不是有效时长（<1s 或为负，注意 TOML 里要写成 \"90s\" 而不是 90），按默认值生效",
				"configured", c.LLM.StreamIdleTimeout, "default", defaults.StreamIdleTimeout)
		}
		c.LLM.StreamIdleTimeout = defaults.StreamIdleTimeout
	}

	if c.LLM.FirstByteTimeout < minUsefulStreamTimeout {
		if c.LLM.FirstByteTimeout != 0 {
			slog.Warn("[llm] first_byte_timeout 不是有效时长（<1s 或为负，注意 TOML 里要写成 \"60s\" 而不是 60），按默认值生效",
				"configured", c.LLM.FirstByteTimeout, "default", defaults.FirstByteTimeout)
		}
		c.LLM.FirstByteTimeout = defaults.FirstByteTimeout
	}

	// retry_attempt_timeout 的 0 有语义（不额外限制），只有负值与"写错单位"的小值
	// 没有语义，按 0 处理。
	if c.LLM.RetryAttemptTimeout < 0 ||
		(c.LLM.RetryAttemptTimeout > 0 && c.LLM.RetryAttemptTimeout < minUsefulStreamTimeout) {
		slog.Warn("[llm] retry_attempt_timeout 不是有效时长（<1s 或为负，注意 TOML 里要写成 \"2m\" 而不是 120），按 0（不额外限制）生效",
			"configured", c.LLM.RetryAttemptTimeout)
		c.LLM.RetryAttemptTimeout = 0
	}

	// 看门狗比总预算还长 = 它永远不会先触发，配了等于没配。说出来比让人以为
	// "我设了 90s 怎么还是等满 10 分钟"便宜。
	// 没写 [llm].timeout 时真实预算来自 DefaultLLMCallTimeout（各调用方兜底），
	// 所以比较基准也用它 —— 拿 0 去比等于这条告警永远不触发。
	budget := c.LLM.Timeout
	if budget <= 0 {
		budget = DefaultLLMCallTimeout
	}
	if c.LLM.StreamIdleTimeout >= budget {
		slog.Warn("[llm] stream_idle_timeout 不小于可用的总预算：空闲看门狗永远不会先于总预算触发，长静默仍会等满总预算",
			"stream_idle_timeout", c.LLM.StreamIdleTimeout,
			"budget", budget)
	}
}
