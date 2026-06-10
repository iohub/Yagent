package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"codeactor/internal/config"
	"codeactor/internal/globalctx"
	"codeactor/internal/llm"
)

// CommitState 持久化 commit 学习状态
type CommitState struct {
	LastHeadHash string    `json:"last_head_hash"` // 上次学习的 HEAD hash
	UpdatedAt    time.Time `json:"updated_at"`     // 上次更新时间
	CommitCount  int       `json:"commit_count"`   // 已学习的 commit 数量
}

// commitStateFileName 状态文件路径（相对于项目根）
const commitStateFileName = ".codeactor/commit_state.json"

// CommitManager 负责初始化和提供 CommitLearner 实例
//
// 主要职责：
// 1. 根据配置创建 CommitLearner 实例
// 2. 在适当的时机（会话开始或按需）触发 commit 学习
// 3. 提供线程安全的访问接口
// 4. 持久化学习状态，支持增量学习
type CommitManager struct {
	learner *CommitLearner
	once    sync.Once
	err     error
	// 新增字段
	ready   atomic.Bool   // 初始学习是否完成
	stateMu sync.RWMutex
	state   *CommitState  // 持久化状态
}

// NewCommitManager 创建新的 CommitManager
//
// 参数:
//   - cfg: 全局配置（从中读取 CommitLearner 配置）
//   - llmEngine: LLM 引擎，用于生成 commit 摘要
//   - llmClient: LLM 客户端，用于获取专用引擎（如果配置了 summarization_provider）
//   - globalCtx: 全局上下文，包含 CodexrayURL
//
// 返回值:
//   - *CommitManager: 初始化的管理器实例
func NewCommitManager(cfg config.Config, llmEngine llm.Engine, llmClient *llm.Client, globalCtx *globalctx.GlobalCtx) *CommitManager {
	// 将 config.CommitLearnerConfig 转换为 agents.CommitLearnConfig
	agentConfig := convertConfig(cfg.CommitLearner)

	// 如果配置了专用的 summarization_provider，创建专用引擎
	var dedicatedEngine llm.Engine
	if agentConfig.SummarizationProvider != "" {
		dedicatedEngine = llmClient.GetAgentEngine("commit-learner")
		if dedicatedEngine == nil {
			// fallback to default
			dedicatedEngine = llmEngine
		}
	}

	return &CommitManager{
		learner: NewCommitLearner(agentConfig, llmEngine, dedicatedEngine, globalCtx),
	}
}

// convertConfig 将 config 包的 CommitLearnerConfig 转换为 agents 包的 CommitLearnConfig
//
// 处理空值到默认值的映射，确保 LLMSystemPrompt 有有效值
func convertConfig(c config.CommitLearnerConfig) CommitLearnConfig {
	// 如果 LLMSystemPrompt 为空，使用默认提示词
	if c.LLMSystemPrompt == "" {
		defaultConfig := DefaultCommitLearnConfig()
		c.LLMSystemPrompt = defaultConfig.LLMSystemPrompt
	}
	return CommitLearnConfig{
		Enabled:               c.Enabled,
		MaxCommits:            c.MaxCommits,
		SimilarityThreshold:   c.SimilarityThreshold,
		TopK:                  c.TopK,
		Trigger:               c.Trigger,
		CacheTTL:              c.CacheTTL,
		LLMSystemPrompt:       c.LLMSystemPrompt,
		SummarizationProvider: c.SummarizationProvider,
	}
}

// GetLearner 获取 CommitLearner 实例
//
// 返回值:
//   - *CommitLearner: commit 学习器实例（可能为 nil 如果未初始化）
//   - error: 如果初始化失败则返回错误
func (cm *CommitManager) GetLearner() (*CommitLearner, error) {
	if cm == nil {
		return nil, fmt.Errorf("commit manager is nil")
	}
	return cm.learner, cm.err
}

// Initialize 初始化 CommitLearner（异步）
//
// 根据配置的 Trigger 模式决定是否自动初始化：
// - "on_demand": 不自动初始化，等待外部调用
// - "on_session_start": 在会话开始时异步初始化
// - "both": 在会话开始时异步初始化
//
// 增强功能：
// - 加载持久化状态，支持增量学习
// - 如果状态存在且有 last HEAD hash → 增量学习
// - 如果状态不存在 → 全量学习
// - 学习完成后更新 ready 标志
//
// 参数:
//   - ctx: 上下文
//   - repoPath: 仓库路径
//
// 返回值:
//   - error: 如果之前初始化失败则返回错误
func (cm *CommitManager) Initialize(ctx context.Context, repoPath string) error {
	cm.once.Do(func() {
		if cm.learner == nil {
			return
		}

		// 如果功能未启用，跳过初始化
		if !cm.learner.Config().Enabled {
			cm.ready.Store(true)
			return
		}

		learner := cm.learner
		trigger := learner.Config().Trigger

		// on_demand 模式下不自动初始化，但标记 ready
		if trigger == "on_demand" {
			cm.ready.Store(true)
			return
		}

		// on_session_start 或 both 模式下异步初始化
		go func() {
			// 尝试加载持久化状态
			state, err := cm.loadState(repoPath)
			if err != nil {
				slog.Warn("[CommitManager] Failed to load state, will do full learn", "error", err)
			}

			fullLearn := false
			if state == nil || state.LastHeadHash == "" {
				// 无状态或首次运行 → 全量学习
				fullLearn = true
			} else if err := learner.EnsureLatest(ctx, repoPath); err != nil {
				slog.Warn("[CommitManager] Incremental learn failed, fallback to full learn", "error", err)
				fullLearn = true
			}

			if fullLearn {
				if err := learner.EnsureLatest(ctx, repoPath); err != nil {
					cm.err = fmt.Errorf("failed to initialize commit learner: %w", err)
					slog.Error("[CommitManager] Full learn failed", "error", err)
				}
			}

			// 获取当前 HEAD 并保存状态
			head, headErr := learner.getCurrentHead(repoPath)
			if headErr == nil {
				newState := &CommitState{
					LastHeadHash: head,
					UpdatedAt:    time.Now(),
				}
				if saveErr := cm.saveState(repoPath, newState); saveErr != nil {
					slog.Warn("[CommitManager] Failed to save state", "error", saveErr)
				}
			}

			// 标记为就绪
			cm.ready.Store(true)
			slog.Info("[CommitManager] Initialization complete")
		}()
	})

	return cm.err
}

// SearchSimilar 搜索与用户输入最相似的 commit
//
// 这是一个便捷方法，直接委托给底层 learner
//
// 参数:
//   - ctx: 上下文
//   - userInput: 用户输入文本
//   - topK: 返回结果数量
//
// 返回值:
//   - []CommitSummary: 匹配的 commit 摘要列表
//   - error: 搜索失败时返回错误
func (cm *CommitManager) SearchSimilar(ctx context.Context, userInput string, topK int) ([]CommitSummary, error) {
	if cm == nil || cm.learner == nil {
		return nil, fmt.Errorf("commit manager or learner is nil")
	}
	return cm.learner.SearchSimilar(ctx, userInput, topK)
}

// Enabled 检查 CommitLearner 是否启用
func (cm *CommitManager) Enabled() bool {
	if cm == nil || cm.learner == nil {
		return false
	}
	return cm.learner.Config().Enabled
}

// loadState 从文件加载 commit 学习状态
func (cm *CommitManager) loadState(projectPath string) (*CommitState, error) {
	statePath := filepath.Join(projectPath, commitStateFileName)
	data, err := os.ReadFile(statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // 首次运行，没有状态
		}
		return nil, err
	}
	var state CommitState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

// saveState 保存 commit 学习状态到文件
func (cm *CommitManager) saveState(projectPath string, state *CommitState) error {
	statePath := filepath.Join(projectPath, commitStateFileName)
	// 确保目录存在
	dir := filepath.Dir(statePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(statePath, data, 0644)
}

// IsReady 返回初始学习是否已完成
func (cm *CommitManager) IsReady() bool {
	return cm.ready.Load()
}
