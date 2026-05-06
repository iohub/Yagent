package compact

import (
	"context"
	"math"
	"sync"
	"codeactor/internal/llm"
)

// MessagePriority 消息优先级信息
type MessagePriority struct {
	Index          int     // 消息在列表中的索引
	Score          float64 // 优先级分数（越高越重要）
	Role           string
	IsSystem       bool
	IsUser         bool
	IsRecent       bool // 是否在"保留窗口"内
	IsEarly        bool // 是否是早期对话（前1/3）
	IsIntermediate bool // 是否是中间对话（优先压缩区域）
	Depth          int  // 距离最近的轮数（0=最新）
	ContentLen     int  // 内容长度（字符数）
}

// PriorityCalculator 优先级计算器
type PriorityCalculator struct {
	weights PriorityWeights
	cache   map[string]float64
	mu      sync.RWMutex
}

// PriorityWeights 优先级权重配置
type PriorityWeights struct {
	// 角色基础权重
	RoleSystem       float64 // System消息: 最高
	RoleUser         float64 // User消息: 次高
	RoleAssistant    float64 // Assistant消息: 中等
	RoleTool         float64 // Tool消息: 最低
	
	// 时间衰减系数
	TimeDecayRate float64 // 每轮衰减率，默认0.08
	
	// 任务状态加成
	CompletedBonus float64 // 已完成任务加成，默认1.3
	
	// 长度惩罚系数
	LengthPenalty float64 // 超长消息惩罚，默认0.7
}

// DefaultPriorityWeights 默认权重
var DefaultPriorityWeights = PriorityWeights{
	RoleSystem:      10.0,
	RoleUser:        8.0,
	RoleAssistant:   4.0,
	RoleTool:        2.0,
	
	TimeDecayRate:    0.08,
	CompletedBonus:   1.3,
	LengthPenalty:    0.7,
}

// NewPriorityCalculator 创建优先级计算器
func NewPriorityCalculator(weights PriorityWeights) *PriorityCalculator {
	if weights.RoleSystem == 0 {
		weights = DefaultPriorityWeights
	}
	return &PriorityCalculator{
		weights: weights,
		cache:   make(map[string]float64),
	}
}

// CalculatePriorities 计算所有消息的优先级
func (pc *PriorityCalculator) CalculatePriorities(
	ctx context.Context, 
	messages []llm.Message, 
	config *Config,
) []MessagePriority {
	priorities := make([]MessagePriority, len(messages))
	totalLen := len(messages)
	
	for i, msg := range messages {
		depth := totalLen - i // 距离末尾的深度（0=最新）
		isRecent := depth <= config.KeepRecentRounds
		isEarly := i < totalLen/3 // 前1/3是早期对话
		isIntermediate := !isRecent && !isEarly // 中间区域
		
		// 计算基础分数
		score := pc.calculateBaseScore(msg, isRecent, isEarly, isIntermediate)
		
		// 时间衰减：越新的消息分数越高
		timeFactor := math.Pow(1.0+pc.weights.TimeDecayRate, float64(depth))
		score *= timeFactor
		
		// 长度惩罚：超长消息降低优先级
		contentLen := len([]rune(msg.Content))
		if contentLen > 5000 {
			score *= pc.weights.LengthPenalty
		}
		
		priorities[i] = MessagePriority{
			Index:          i,
			Score:          score,
			Role:           string(msg.Role),
			IsSystem:       msg.Role == llm.RoleSystem,
			IsUser:         msg.Role == llm.RoleUser,
			Depth:          depth,
			IsRecent:       isRecent,
			IsEarly:        isEarly,
			IsIntermediate: isIntermediate,
			ContentLen:     contentLen,
		}
	}
	
	return priorities
}

// calculateBaseScore 计算基础分数
func (pc *PriorityCalculator) calculateBaseScore(msg llm.Message, isRecent, isEarly, isIntermediate bool) float64 {
	var score float64
	
	switch msg.Role {
	case llm.RoleSystem:
		score = pc.weights.RoleSystem
	case llm.RoleUser:
		score = pc.weights.RoleUser
	case llm.RoleAssistant:
		score = pc.weights.RoleAssistant
	case llm.RoleTool:
		score = pc.weights.RoleTool
	default:
		score = 1.0
	}
	
	// 近期消息加成（最近N轮完整保留）
	if isRecent {
		score *= 2.0
	}
	
	// 早期对话轻微加成（保留一些上下文）
	if isEarly {
		score *= 1.2
	}
	
	// 中间对话减分（优先压缩区域）
	if isIntermediate && msg.Role == llm.RoleTool {
		score *= 0.5 // 中间tool消息优先压缩
	}
	
	return score
}

// GetScores 获取所有消息的优先级分数
func (pc *PriorityCalculator) GetScores(messages []llm.Message, config *Config) map[int]float64 {
	priorities := pc.CalculatePriorities(context.Background(), messages, config)
	scores := make(map[int]float64)
	for _, p := range priorities {
		scores[p.Index] = p.Score
	}
	return scores
}
