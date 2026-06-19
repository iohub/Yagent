package compact

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"codeactor/internal/llm"
)

// DropPolicy 队列满时的丢弃策略
type DropPolicy int

const (
	// DropPolicyBlock 阻塞等待（适用于必须完成压缩的场景）
	DropPolicyBlock DropPolicy = iota

	// DropPolicyDropOldest 丢弃最旧的任务（适用于需要最新状态的场景）
	// 这是默认策略，因为压缩总是处理最新消息快照
	DropPolicyDropOldest

	// DropPolicyDropNewest 丢弃新提交的任务（适用于优先保证已提交任务完成的场景）
	DropPolicyDropNewest
)

func (p DropPolicy) String() string {
	switch p {
	case DropPolicyBlock:
		return "block"
	case DropPolicyDropOldest:
		return "drop_oldest"
	case DropPolicyDropNewest:
		return "drop_newest"
	default:
		return "unknown"
	}
}

// CompactJob 异步压缩任务
type CompactJob struct {
	// MessageSnapshot 消息快照（压缩时的消息列表副本）
	MessageSnapshot []llm.Message

	// State 当前压缩状态快照
	State *CompressionState

	// ResultCh 结果通道，压缩完成后通过该通道返回结果
	ResultCh chan *CompactJobResult
}

// CompactJobResult 压缩任务结果
type CompactJobResult struct {
	// CompressedMessages 压缩后的消息列表
	CompressedMessages []llm.Message

	// NewState 更新后的压缩状态
	NewState *CompressionState

	// Err 压缩过程中的错误（nil 表示成功）
	Err error

	// Duration 压缩耗时
	Duration time.Duration

	// Stats 压缩统计信息
	Stats string
}

// AsyncCompactor 异步压缩管理器
type AsyncCompactor struct {
	engine     *Engine            // 底层压缩引擎
	config     *Config            // 配置

	// 任务队列
	jobQueue    chan *CompactJob
	queueSize   int
	dropPolicy  DropPolicy        // 队列满时的丢弃策略

	// Worker 生命周期
	cancelFunc context.CancelFunc
	wg         sync.WaitGroup
	started    bool
	mu         sync.Mutex

	// Worker 健康检查
	workerID        int
	lastActiveTime  atomic.Value // time.Time
	jobsCompleted   atomic.Int64
	jobsFailed      atomic.Int64
	jobsDropped     atomic.Int64 // 因队列满被丢弃的 job 数量

	// 当前队列深度（近似值）
	currentQueueDepth atomic.Int64
}

// NewAsyncCompactor 创建异步压缩管理器
func NewAsyncCompactor(engine *Engine, config *Config) *AsyncCompactor {
	// 队列大小：默认 3，Minimum 1
	queueSize := 3
	if config != nil {
		// 可以用 MaxConcurrentSummaries 作为队列大小的参考
		if config.MaxConcurrentSummaries > 1 {
			queueSize = config.MaxConcurrentSummaries
		}
	}
	if queueSize < 1 {
		queueSize = 1
	}

	ac := &AsyncCompactor{
		engine:     engine,
		config:     config,
		jobQueue:   make(chan *CompactJob, queueSize),
		queueSize:  queueSize,
		dropPolicy: DropPolicyDropOldest,
	}
	ac.lastActiveTime.Store(time.Now())

	return ac
}

// Start 启动 worker goroutine
func (ac *AsyncCompactor) Start(ctx context.Context) {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	if ac.started {
		return
	}

	workerCtx, cancel := context.WithCancel(ctx)
	ac.cancelFunc = cancel
	ac.workerID = int(ac.jobsCompleted.Load()) + 1

	ac.wg.Add(1)
	go ac.workerLoop(workerCtx, ac.workerID)

	ac.started = true
	slog.Info("Async compactor worker started",
		"worker_id", ac.workerID,
		"interval", ac.config.CompactWorkerInterval,
		"queue_size", ac.queueSize)
}

// Stop 停止 worker goroutine
func (ac *AsyncCompactor) Stop() {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	if !ac.started {
		return
	}

	if ac.cancelFunc != nil {
		ac.cancelFunc()
	}
	ac.wg.Wait()
	ac.started = false
	slog.Info("Async compactor worker stopped")
}

// IsRunning 检查 worker 是否在运行
func (ac *AsyncCompactor) IsRunning() bool {
	ac.mu.Lock()
	defer ac.mu.Unlock()
	return ac.started
}

// SubmitJob 提交压缩任务
//
// 背压策略：
//   - DropPolicyBlock: 阻塞直到队列有空位
//   - DropPolicyDropOldest: 丢弃最旧的 job，提交新的
//   - DropPolicyDropNewest: 丢弃新 job 并返回 false
//
// 返回 true 表示提交成功，false 表示被丢弃
func (ac *AsyncCompactor) SubmitJob(job *CompactJob) bool {
	switch ac.dropPolicy {
	case DropPolicyBlock:
		// 阻塞等待
		select {
		case ac.jobQueue <- job:
			ac.currentQueueDepth.Add(1)
			slog.Debug("Async compact job submitted (blocking)",
				"message_count", len(job.MessageSnapshot))
			return true
		}

	case DropPolicyDropOldest:
		// 尝试非阻塞提交
		select {
		case ac.jobQueue <- job:
			ac.currentQueueDepth.Add(1)
			slog.Debug("Async compact job submitted",
				"message_count", len(job.MessageSnapshot))
			return true
		default:
			// 队列满，丢弃最旧的
			select {
			case <-ac.jobQueue:
				// 丢弃旧任务
				ac.jobsDropped.Add(1)
				ac.currentQueueDepth.Add(-1)
			default:
			}
			// 重新提交
			select {
			case ac.jobQueue <- job:
				ac.currentQueueDepth.Add(1)
				slog.Debug("Async compact job replaced oldest job",
					"message_count", len(job.MessageSnapshot))
				return true
			default:
				slog.Warn("Async compact job submission failed (queue full after drain)")
				ac.jobsDropped.Add(1)
				return false
			}
		}

	case DropPolicyDropNewest:
		// 非阻塞尝试
		select {
		case ac.jobQueue <- job:
			ac.currentQueueDepth.Add(1)
			slog.Debug("Async compact job submitted",
				"message_count", len(job.MessageSnapshot))
			return true
		default:
			// 直接丢弃新 job
			ac.jobsDropped.Add(1)
			slog.Warn("Async compact job dropped (queue full, drop_newest policy)",
				"message_count", len(job.MessageSnapshot))
			return false
		}

	default:
		// 未知策略，尝试非阻塞提交
		select {
		case ac.jobQueue <- job:
			ac.currentQueueDepth.Add(1)
			return true
		default:
			return false
		}
	}
}

// workerLoop worker 主循环
func (ac *AsyncCompactor) workerLoop(ctx context.Context, workerID int) {
	defer ac.wg.Done()

	ticker := time.NewTicker(ac.config.CompactWorkerInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("Async compactor worker stopped by context",
				"worker_id", workerID,
				"completed", ac.jobsCompleted.Load(),
				"failed", ac.jobsFailed.Load())
			return

		case job := <-ac.jobQueue:
			ac.currentQueueDepth.Add(-1)
			ac.lastActiveTime.Store(time.Now())
			ac.processJob(ctx, job)

		case <-ticker.C:
			// 健康检查：如果 worker 长时间无响应，记录警告
			lastActive := ac.lastActiveTime.Load().(time.Time)
			if time.Since(lastActive) > 2*time.Minute {
				slog.Warn("Async compactor worker idle for long period",
					"worker_id", workerID,
					"idle_seconds", time.Since(lastActive).Seconds(),
					"queue_depth", ac.currentQueueDepth.Load())
			}
		}
	}
}

// processJob 处理单个压缩任务
func (ac *AsyncCompactor) processJob(ctx context.Context, job *CompactJob) {
	startTime := time.Now()
	slog.Info("Async compactor processing job",
		"message_count", len(job.MessageSnapshot))

	// 创建子上下文，支持超时
	timeout := ac.config.SummarizationTimeout * 2
	jobCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	result, newState, err := ac.engine.CompressIncremental(
		jobCtx,
		job.MessageSnapshot,
		job.State,
	)

	duration := time.Since(startTime)

	jobResult := &CompactJobResult{
		Duration: duration,
		Stats:    "",
	}

	if err != nil {
		ac.jobsFailed.Add(1)
		jobResult.Err = err
		slog.Error("Async compaction failed",
			"error", err,
			"duration", duration,
			"total_failed", ac.jobsFailed.Load())
	} else {
		ac.jobsCompleted.Add(1)
		jobResult.CompressedMessages = result.CompressedMessages
		jobResult.NewState = newState
		jobResult.Stats = result.CompressionStats

		slog.Info("Async compaction completed",
			"duration", duration,
			"original_tokens", result.OriginalTokens,
			"compressed_tokens", result.CompressedTokens,
			"ratio", result.CompressionRatio,
			"total_completed", ac.jobsCompleted.Load())
	}

	// 发送结果
	select {
	case job.ResultCh <- jobResult:
	case <-ctx.Done():
		slog.Warn("Async compactor result channel send cancelled")
	default:
		slog.Warn("Async compactor result channel full, discarding result")
	}
}

// SetDropPolicy 设置队列满时的丢弃策略
func (ac *AsyncCompactor) SetDropPolicy(policy DropPolicy) {
	ac.mu.Lock()
	defer ac.mu.Unlock()
	ac.dropPolicy = policy
}

// GetDropPolicy 获取当前丢弃策略
func (ac *AsyncCompactor) GetDropPolicy() DropPolicy {
	ac.mu.Lock()
	defer ac.mu.Unlock()
	return ac.dropPolicy
}

// Stats 返回异步压缩器的统计信息
func (ac *AsyncCompactor) Stats() map[string]interface{} {
	return map[string]interface{}{
		"queue_capacity":  ac.queueSize,
		"queue_depth":     ac.currentQueueDepth.Load(),
		"drop_policy":     ac.dropPolicy.String(),
		"jobs_completed":  ac.jobsCompleted.Load(),
		"jobs_failed":     ac.jobsFailed.Load(),
		"jobs_dropped":    ac.jobsDropped.Load(),
		"is_running":      ac.IsRunning(),
		"last_active":     ac.lastActiveTime.Load().(time.Time).Format(time.RFC3339),
	}
}

// Health 返回健康检查结果
func (ac *AsyncCompactor) Health() error {
	if !ac.IsRunning() {
		return fmt.Errorf("async compactor is not running")
	}

	lastActive := ac.lastActiveTime.Load().(time.Time)
	if time.Since(lastActive) > 5*time.Minute {
		return fmt.Errorf("async compactor worker inactive for %v", time.Since(lastActive))
	}

	return nil
}
