package compression

import (
	"log/slog"
	"os"
	"strconv"
	"sync"

	"yagent/internal/models"
)

// ContextWindowRatio 压缩阈值与模型上下文窗口的比例系数：
// token 用量超过窗口的 75%（0.75）即触发压缩，为工具结果和模型输出预留余量。
const ContextWindowRatio = 0.75

// EnvThreshold 是**调试用**的阈值覆盖：设了它就直接拿它当压缩阈值，跳过模型目录与配置项。
//
// 存在的理由很实际：目录收录的模型动辄 1M 窗口，阈值 = 窗口 × 0.75 = 750k token，
// 想在真机上肉眼验收压缩行为（以及 TUI 的 ⧉ 面板）根本攒不到那么长。
// 生产配置不要设它 —— 它会让压缩早于模型真实容量触发，白丢上下文。
const EnvThreshold = "YAGENT_COMPRESS_THRESHOLD"

// envThresholdWarn 只管"提醒一次"，不缓存取值：
// 取值必须每次现读，否则测试里 t.Setenv 与运行期改环境都会读到旧值。
var envThresholdWarn sync.Once

func envThreshold() int {
	raw, ok := os.LookupEnv(EnvThreshold)
	if !ok || raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		envThresholdWarn.Do(func() {
			slog.Warn("ignoring invalid context compression override",
				"env", EnvThreshold, "value", raw, "want", "正整数 token 数")
		})
		return 0
	}
	envThresholdWarn.Do(func() {
		slog.Warn("context compression threshold overridden",
			"env", EnvThreshold, "threshold", n,
			"note", "调试用：模型目录与 context_compression_threshold 均已忽略")
	})
	return n
}

// ResolveThreshold 返回触发上下文压缩的 token 阈值。两个来源都是**上限**：
// 目录回答"这个模型装得下多少"，配置回答"我愿意为延迟与成本付多少"，
// 所以两者都给值时取小；只有一个给值就用那个；都不给用兜底默认。
//
//  0. EnvThreshold（仅调试用；显式设置时压过下面一切）；
//  1. 内嵌模型目录（models.json）收录的上下文窗口 * ContextWindowRatio；
//  2. configured（配置项 context_compression_threshold，>0 时）；
//  3. 1 与 2 都给值 → min(1, 2)；都不给 → DefaultContextCompressionThreshold。
//
// model 为 LLM 引擎实际使用的模型名，空串或未收录时目录那一路不参与。
//
// ⚠️ 调用方有两种语义，改动前先读 docs/compression-trigger-design.md §5.4：
// 压缩触发传配置值，而重放预算（resume_delegation.go:187 的 ResolveThreshold(model, 0)）
// 传 0，要的是"纯模型容量"，不参与取小。
func ResolveThreshold(model string, configured int) int {
	if n := envThreshold(); n > 0 {
		return n
	}
	catalog := 0
	if window := models.ContextWindow(model); window > 0 {
		catalog = int(float64(window) * ContextWindowRatio)
	}
	switch {
	case catalog > 0 && configured > 0:
		return min(catalog, configured)
	case catalog > 0:
		return catalog
	case configured > 0:
		return configured
	default:
		return DefaultContextCompressionThreshold
	}
}
