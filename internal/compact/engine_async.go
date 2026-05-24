package compact

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"codeactor/internal/llm"
)

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
	jobChan    chan *CompactJob   // 任务队列
	cancelFunc context.CancelFunc // 取消函数
	wg         sync.WaitGroup     // worker 等待组
	started    bool               // 是否已启动
	mu         sync.Mutex         // 启动保护
}

// NewAsyncCompactor 创建异步压缩管理器
func NewAsyncCompactor(engine *Engine, config *Config) *AsyncCompactor {
	return &AsyncCompactor{
		engine:  engine,
		config:  config,
		jobChan: make(chan *CompactJob, 1), // 缓冲1个，避免堆积
	}
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

	ac.wg.Add(1)
	go ac.workerLoop(workerCtx)

	ac.started = true
	slog.Info("Async compactor worker started",
		"interval", ac.config.CompactWorkerInterval)
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

// SubmitJob 提交压缩任务（非阻塞）
// 如果任务队列已满，丢弃旧任务并提交新任务
// 返回 true 表示提交成功，false 表示已有任务在队列中且未被替换
func (ac *AsyncCompactor) SubmitJob(job *CompactJob) bool {
	select {
	case ac.jobChan <- job:
		slog.Debug("Async compact job submitted",
			"message_count", len(job.MessageSnapshot))
		return true
	default:
		// 队列已满，尝试丢弃旧任务（替换）
		// 清空当前任务
		select {
		case <-ac.jobChan:
			// 丢弃旧任务
		default:
		}
		// 重新提交
		select {
		case ac.jobChan <- job:
			slog.Debug("Async compact job replaced previous job",
				"message_count", len(job.MessageSnapshot))
			return true
		default:
			slog.Warn("Async compact job submission failed (channel full)")
			return false
		}
	}
}

// workerLoop worker 主循环
func (ac *AsyncCompactor) workerLoop(ctx context.Context) {
	defer ac.wg.Done()

	ticker := time.NewTicker(ac.config.CompactWorkerInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("Async compactor worker stopped by context")
			return

		case job := <-ac.jobChan:
			ac.processJob(ctx, job)

		case <-ticker.C:
			// 定期检查，不做额外操作
		}
	}
}

// processJob 处理单个压缩任务
func (ac *AsyncCompactor) processJob(ctx context.Context, job *CompactJob) {
	startTime := time.Now()
	slog.Info("Async compactor processing job",
		"message_count", len(job.MessageSnapshot))

	// 创建子上下文，支持超时
	timeout := ac.config.SummarizationTimeout * 2 // 给压缩任务双倍超时时间
	jobCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// 执行增量压缩
	result, newState, err := ac.engine.CompressIncremental(
		jobCtx,
		job.MessageSnapshot,
		job.State,
	)

	duration := time.Since(startTime)

	// 构建结果
	jobResult := &CompactJobResult{
		Duration: duration,
		Stats:    "",
	}

	if err != nil {
		jobResult.Err = err
		slog.Error("Async compaction failed",
			"error", err,
			"duration", duration)
	} else {
		jobResult.CompressedMessages = result.CompressedMessages
		jobResult.NewState = newState
		jobResult.Stats = result.CompressionStats

		slog.Info("Async compaction completed",
			"duration", duration,
			"original_tokens", result.OriginalTokens,
			"compressed_tokens", result.CompressedTokens,
			"ratio", result.CompressionRatio)
	}

	// 发送结果
	select {
	case job.ResultCh <- jobResult:
	case <-ctx.Done():
		slog.Warn("Async compactor result channel send cancelled")
	default:
		// 如果没有人接收结果，记录日志
		slog.Warn("Async compactor result channel full, discarding result")
	}
}
