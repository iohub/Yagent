package compact

import (
	"codeactor/internal/llm"
	"fmt"
)

// CompressionDirection 定义压缩时的保留方向
type CompressionDirection int

const (
	// PreserveRecent 保留最近的对话（丢弃旧消息），默认策略
	PreserveRecent CompressionDirection = iota
	// PreserveOld 保留旧的对话（丢弃最近消息），适合缓存友好场景
	PreserveOld
)

func (d CompressionDirection) String() string {
	switch d {
	case PreserveRecent:
		return "preserve_recent"
	case PreserveOld:
		return "preserve_old"
	default:
		return "unknown"
	}
}

// ThresholdResult 动态阈值计算结果
type ThresholdResult struct {
	// ModelWindow 模型的完整上下文窗口大小（tokens）
	ModelWindow int
	// SummaryReserved 为摘要预留的 token 数（tokens）
	SummaryReserved int
	// BufferBand 安全缓冲带（tokens），防止边界溢出
	BufferBand int
	// EffectiveWindow 有效窗口 = ModelWindow - SummaryReserved - BufferBand
	EffectiveWindow int
	// TriggerThreshold 触发压缩的阈值（tokens），通常等于 EffectiveWindow
	TriggerThreshold int
	// Direction 压缩方向
	Direction CompressionDirection
}

// DynamicConfig 动态阈值计算配置
type DynamicConfig struct {
	// CompressionDirection 压缩方向策略
	//   "auto"  - 根据上下文自动判断（默认）
	//   "recent"- 始终保留最近的对话（保前）
	//   "old"   - 始终保留旧的对话（保后）
	CompressionDirection string `toml:"compression_direction"`

	// SummaryReservedFraction 摘要预留比例（0~1），占 ModelWindow 的百分比
	// 默认 0.165，即 200K 窗口预留 33K 给摘要
	SummaryReservedFraction float64 `toml:"summary_reserved_fraction"`

	// BufferBandTokens 固定缓冲带 token 数，默认 0
	// 可根据需要设置为 1000~5000 防止边界溢出
	BufferBandTokens int `toml:"buffer_band_tokens"`

	// AutoOldRatioThreshold 自动决策中"开头消息占比"阈值
	// 如果前 N% 消息的 token 占比 > 此值 → PreserveOld
	// 默认 0.4（40%）
	AutoOldRatioThreshold float64 `toml:"auto_old_ratio_threshold"`

	// AutoLookbackPercent 自动决策时"开头"的定义比例
	// 计算前 N% 的消息 token 占比
	// 默认 0.3（前 30% 消息）
	AutoLookbackPercent float64 `toml:"auto_lookback_percent"`
}

// DefaultDynamicConfig 默认动态配置
var DefaultDynamicConfig = DynamicConfig{
	CompressionDirection:    "auto",
	SummaryReservedFraction: 0.165, // 200K 窗口 → 33K 预留 → 167K 触发
	BufferBandTokens:        0,
	AutoOldRatioThreshold:   0.4,
	AutoLookbackPercent:     0.3,
}

// DynamicEngine 动态阈值计算引擎
// 根据当前上下文动态计算压缩触发阈值和方向
type DynamicEngine struct {
	config   *DynamicConfig
	baseCfg  *Config
	tokenizer Tokenizer
}

// NewDynamicEngine 创建动态引擎
func NewDynamicEngine(baseCfg *Config, tokenizer Tokenizer) *DynamicEngine {
	return &DynamicEngine{
		config:    &DefaultDynamicConfig,
		baseCfg:   baseCfg,
		tokenizer: tokenizer,
	}
}

// SetConfig 动态设置配置
func (de *DynamicEngine) SetConfig(cfg *DynamicConfig) {
	if cfg != nil {
		de.config = cfg
	}
}

// CalculateDynamicThreshold 核心计算：根据消息列表计算动态阈值
//
// 公式:
//   EffectiveWindow = ModelWindow - SummaryReserved - BufferBand
//   TriggerThreshold = EffectiveWindow
//
// 例如: 200K 窗口 → 33K 摘要预留 → 0K 缓冲 → 167K 触发
func (de *DynamicEngine) CalculateDynamicThreshold(msgs []llm.Message) *ThresholdResult {
	// 1. 获取模型窗口大小（使用配置值）
	modelWindow := de.baseCfg.MaxContextTokens
	if modelWindow <= 0 {
		modelWindow = DefaultConfig.MaxContextTokens
	}

	// 2. 计算摘要预留
	summaryReserved := int(float64(modelWindow) * de.config.SummaryReservedFraction)
	if summaryReserved < 1000 {
		summaryReserved = 1000 // 最小预留 1K
	}

	// 3. 缓冲带
	bufferBand := de.config.BufferBandTokens
	if bufferBand < 0 {
		bufferBand = 0
	}

	// 4. 有效窗口 = 模型窗口 - 摘要预留 - 缓冲带
	effectiveWindow := modelWindow - summaryReserved - bufferBand
	if effectiveWindow < 1000 {
		effectiveWindow = 1000 // 最小有效窗口 1K
	}

	// 5. 触发阈值 = 有效窗口
	triggerThreshold := effectiveWindow

	// 6. 压缩方向
	direction := de.DetermineDirection(msgs)

	return &ThresholdResult{
		ModelWindow:      modelWindow,
		SummaryReserved:  summaryReserved,
		BufferBand:       bufferBand,
		EffectiveWindow:  effectiveWindow,
		TriggerThreshold: triggerThreshold,
		Direction:        direction,
	}
}

// DetermineDirection 根据配置和上下文自动判断压缩方向
//
// 策略:
//   - "recent"  → PreserveRecent
//   - "old"     → PreserveOld
//   - "auto"    → 调用 AutoDirection 自动决策
func (de *DynamicEngine) DetermineDirection(msgs []llm.Message) CompressionDirection {
	strategy := de.config.CompressionDirection
	if strategy == "" {
		strategy = DefaultDynamicConfig.CompressionDirection
	}

	switch strategy {
	case "recent":
		return PreserveRecent
	case "old":
		return PreserveOld
	default:
		// "auto" 或空 → 自动决策
		return de.AutoDirection(msgs)
	}
}

// AutoDirection 自动决策逻辑
//
// 算法:
//   1. 计算前 N%（AutoLookbackPercent）消息的 token 占比
//   2. 如果占比 > AutoOldRatioThreshold → PreserveOld（开头消息多，缓存友好）
//   3. 否则 → PreserveRecent（默认，保留最近的对话）
//
// 为什么 PreserveOld 缓存友好？
//   因为 LLM 的 prompt cache 对前缀敏感，保留开头的消息（System、早期决策）
//   意味着这些稳定的前缀更可能命中 cache。
func (de *DynamicEngine) AutoDirection(msgs []llm.Message) CompressionDirection {
	if len(msgs) == 0 {
		return PreserveRecent
	}

	// 计算总 token 数
	totalTokens := 0
	for _, msg := range msgs {
		tokens, err := de.tokenizer.CountTokens(msg.Content)
		if err == nil {
			totalTokens += tokens
		}
	}

	if totalTokens == 0 {
		return PreserveRecent
	}

	// 计算前 N% 消息的 token 占比
	lookbackCount := int(float64(len(msgs)) * de.config.AutoLookbackPercent)
	if lookbackCount < 1 {
		lookbackCount = 1
	}
	if lookbackCount > len(msgs) {
		lookbackCount = len(msgs)
	}

	oldTokens := 0
	for i := 0; i < lookbackCount; i++ {
		tokens, err := de.tokenizer.CountTokens(msgs[i].Content)
		if err == nil {
			oldTokens += tokens
		}
	}

	oldRatio := float64(oldTokens) / float64(totalTokens)
	threshold := de.config.AutoOldRatioThreshold

	// 如果开头消息占比超过阈值 → PreserveOld
	if oldRatio > threshold {
		return PreserveOld
	}

	return PreserveRecent
}

// FormatSummary 格式化阈值结果为可读字符串
func (tr *ThresholdResult) FormatSummary() string {
	return tr.FormatSummaryWithUnit("K")
}

// FormatSummaryWithUnit 格式化阈值结果为带单位的字符串
func (tr *ThresholdResult) FormatSummaryWithUnit(unit string) string {
	window := tr.formatTokens(tr.ModelWindow, unit)
	reserved := tr.formatTokens(tr.SummaryReserved, unit)
	buffer := tr.formatTokens(tr.BufferBand, unit)
	effective := tr.formatTokens(tr.EffectiveWindow, unit)
	trigger := tr.formatTokens(tr.TriggerThreshold, unit)

	return fmt.Sprintf("DynamicThreshold{window=%s, summary_reserved=%s, buffer=%s, effective=%s, trigger=%s, direction=%s}",
		window, reserved, buffer, effective, trigger, tr.Direction)
}

func (tr *ThresholdResult) formatTokens(tokens int, unit string) string {
	if unit == "K" {
		return fmt.Sprintf("%dK", tokens/1000)
	}
	return fmt.Sprintf("%d", tokens)
}

// IsOverThreshold 检查当前 token 数是否超过触发阈值
func (tr *ThresholdResult) IsOverThreshold(currentTokens int) bool {
	return currentTokens > tr.TriggerThreshold
}

// EffectiveRatio 计算触发阈值占模型窗口的比例
func (tr *ThresholdResult) EffectiveRatio() float64 {
	if tr.ModelWindow == 0 {
		return 0
	}
	return float64(tr.TriggerThreshold) / float64(tr.ModelWindow)
}

// RoundToPercent 将有效比例转换为百分比字符串
func (tr *ThresholdResult) RoundToPercent() string {
	return fmt.Sprintf("%.1f%%", tr.EffectiveRatio()*100)
}
