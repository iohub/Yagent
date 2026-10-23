package memory

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"yagent/internal/gitinfo"
	"yagent/internal/supervise"
)

type rolloutWriterCtxKey struct{}

func WithRolloutWriter(ctx context.Context, w *RolloutWriter) context.Context {
	if w == nil || !w.Enabled() {
		return ctx
	}
	return context.WithValue(ctx, rolloutWriterCtxKey{}, w)
}

func GetRolloutWriter(ctx context.Context) *RolloutWriter {
	if w, ok := ctx.Value(rolloutWriterCtxKey{}).(*RolloutWriter); ok {
		return w
	}
	return nil
}

// 落盘批量化（docs/structured-concurrency-design.md §7）：每条记录一次 write(2) 全压在 LLM 关键路径上，所以每个 writer 有一条自己的写循环，公开方法只做"序列化 + 入队"。
const (
	rolloutQueueSize    = 256 // 入队上限；满了就阻塞生产者（背压），绝不丢记录
	rolloutFlushBatch   = 32
	rolloutFlushEvery   = 20 * time.Millisecond
	rolloutWriteBufSize = 64 * 1024
)

// rolloutJob 的 reply 非 nil 表示一次刷盘请求，写循环必须把结果发回（容量 1，永远不会阻塞写循环）。
type rolloutJob struct {
	data  []byte
	reply chan error
}

// 锁的分工是唯一不能妥协的地方：**写循环绝不取 w.mu** —— 生产者在持锁状态下往 q 里塞，写循环若也要拿 w.mu 才能刷盘就是"生产者等消费者排空、消费者等生产者放锁"的死锁，而且只在大输出时出现。
type RolloutWriter struct {
	mu      sync.Mutex
	enabled bool
	closed  bool

	// ── 以下字段只由构造时写入，之后只读 ──
	filePath  string
	sessionID string
	agentName string
	taskID    string
	// boardID 由 SetBoardID 注入（构造签名有 10 个测试调用点）；记进 session_meta 是因为 board 作用域是 session 级，一块板对应多个 task，只靠 task_id 找不回原来的板。
	boardID string
	// delegationID / parentSessionID 是父子 rollout 之间的两个坐标，由 createRolloutWriter 从 ctx 取到后注入（与 boardID 同一个理由），WriteSessionMeta 兜底填充。
	delegationID       string
	parentSessionID    string
	projectID          string
	turnID             string
	turnCounter        int
	msgCounter         int32 // 只在 w.mu 下读写（R9：原来 NextMessageID 用 atomic、这里用裸 ++，两套同步管同一个字段）
	startTime          time.Time
	sessionMetaWritten bool

	totalInputTokens  int64
	totalOutputTokens int64
	totalCachedTokens int64
	totalTokens       int64

	// ── 写循环的收件箱；q/quit/loopDone 在构造后不再重新赋值 ──
	q         chan rolloutJob
	quit      chan struct{}
	loopDone  chan struct{}
	closeOnce sync.Once
	closeErr  error

	// file/buf/pendingErr **只归写循环那个 goroutine 碰**，于是天然无竞态；Close 在 <-loopDone 之后才收 file，happens-before 由 channel 保证。
	file       *os.File
	buf        *bufio.Writer
	pendingErr error
}

func generateSessionID() string {
	timestamp := time.Now().Format("20060102_150405")
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return timestamp + "_00000000"
	}
	return fmt.Sprintf("%s_%s", timestamp, hex.EncodeToString(b))
}

// RolloutRootDir 返回 rollout 根目录，读侧 ListRolloutSessions/ReadRolloutMemory 与写侧 NewRolloutWriter 共用，避免路径构造漂移。
func RolloutRootDir() string {
	return filepath.Join(homeDirOrFallback(), ".yagent", "data", "rollout")
}

func NewRolloutWriter(agentName, taskID, projectID string) (*RolloutWriter, error) {
	sessionID := generateSessionID()

	outputDir := RolloutRootDir()
	if projectID == "" {
		projectID = "default"
	}
	outputDir = filepath.Join(outputDir, projectID)

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		slog.Warn("RolloutWriter: failed to create output directory", "path", outputDir, "error", err)
		return &RolloutWriter{enabled: false, sessionID: sessionID, agentName: agentName, taskID: taskID, projectID: projectID}, fmt.Errorf("failed to create output directory: %w", err)
	}

	timestamp := time.Now().Format("20060102_150405")
	var filename string
	if agentName != "" {
		filename = fmt.Sprintf("%s_%s_%s.jsonl", timestamp, sessionID, agentName)
	} else {
		filename = fmt.Sprintf("%s_%s.jsonl", timestamp, sessionID)
	}
	filePath := filepath.Join(outputDir, filename)

	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		slog.Warn("RolloutWriter: failed to open output file", "path", filePath, "error", err)
		return &RolloutWriter{enabled: false, sessionID: sessionID, agentName: agentName, taskID: taskID, projectID: projectID}, fmt.Errorf("failed to open output file: %w", err)
	}

	w := &RolloutWriter{
		enabled:     true,
		filePath:    filePath,
		sessionID:   sessionID,
		agentName:   agentName,
		taskID:      taskID,
		projectID:   projectID,
		startTime:   time.Now(),
		msgCounter:  0,
		turnCounter: 0,
		q:           make(chan rolloutJob, rolloutQueueSize),
		quit:        make(chan struct{}),
		loopDone:    make(chan struct{}),
	}
	// 写循环挂进 supervise：它会存活整个会话，必须有名字、有 recover、有人退出时等它（Yagent.Close 的根域 drain）；fn 参数不用，它认的是 w.quit。
	supervise.Root().Go("memory/rollout-writer/"+filepath.Base(filePath), func(context.Context) {
		w.writeLoop(file)
	})
	return w, nil
}

// writeLoop 是每个 writer 唯一的落盘者：攒批写进 bufio，到点/攒够/被要求/被关闭时刷盘。它绝不取 w.mu（见结构体注释），也不读任何归生产路径的 w 可变字段。
func (w *RolloutWriter) writeLoop(file *os.File) {
	defer close(w.loopDone)

	w.file = file
	w.buf = bufio.NewWriterSize(file, rolloutWriteBufSize)
	ticker := time.NewTicker(rolloutFlushEvery)
	defer ticker.Stop()

	pending := 0
	flush := func() {
		if pending == 0 {
			return
		}
		if err := w.buf.Flush(); err != nil && w.pendingErr == nil {
			w.pendingErr = err
		}
		pending = 0
	}

	for {
		select {
		case job := <-w.q:
			if job.reply != nil {
				// 读回之前必须看见全部已入队的记录（§7.3：检查点指纹由读侧算）。
				flush()
				job.reply <- w.pendingErr
				continue
			}
			if len(job.data) == 0 {
				continue
			}
			if _, err := w.buf.Write(job.data); err != nil && w.pendingErr == nil {
				w.pendingErr = err
			}
			pending++
			if pending >= rolloutFlushBatch {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-w.quit:
			// 收尾先排空已入队的再刷：生产者入队时在锁里先看 w.closed，Close 也是先置 closed 再 close(quit)，所以排空后不会再有新项进来。
			for {
				select {
				case job := <-w.q:
					if len(job.data) > 0 {
						if _, err := w.buf.Write(job.data); err != nil && w.pendingErr == nil {
							w.pendingErr = err
						}
						pending++
					}
					if job.reply != nil {
						job.reply <- w.pendingErr
					}
				default:
					flush()
					return
				}
			}
		}
	}
}

// writeEnvelope 序列化一条记录并交给写循环（调用时必须持有 w.mu）；返回的 error 只描述"这条没进去"，落盘本身的错误由 Flush/Close 回报。
func (w *RolloutWriter) writeEnvelope(recordType string, payload interface{}) error {
	var buf bytes.Buffer
	envelope := RolloutEnvelope{
		Timestamp: nowISO8601(),
		Type:      recordType,
		Payload:   payload,
	}
	if err := json.NewEncoder(&buf).Encode(envelope); err != nil {
		slog.Warn("RolloutWriter: failed to encode record", "error", err)
		return fmt.Errorf("failed to encode record: %w", err)
	}
	return w.enqueueLocked(recordType, buf.Bytes())
}

// enqueueLocked 把一条已序列化的记录投进收件箱：队列满时**阻塞等空位**而不是丢弃（rollout 是 /resume 唯一的事实来源，丢一条就是账本造假）；同时盯着 quit 与 loopDone —— 后者意味着写循环已经不在了（panic 被 supervise 兜住），那时必须说话，不能把持着 w.mu 的生产者挂死。
func (w *RolloutWriter) enqueueLocked(recordType string, data []byte) error {
	if w.q == nil || w.closed {
		return nil // 未启用的 writer（目录/打开失败），或已经 Close：Close 之后不收新记录
	}
	select {
	case w.q <- rolloutJob{data: data}:
		return nil
	case <-w.quit:
		return nil
	case <-w.loopDone:
		slog.Error("RolloutWriter: 写循环已退出，这条记录没能落盘",
			"file", w.filePath, "record_type", recordType)
		return fmt.Errorf("rollout writer loop exited")
	}
}

// Flush 等已入队的记录真的落到文件里再回报落盘错误：检查点指纹是**读侧**对整份文件重算的（见 FinalizeCheckpoint），队列里还有没写出去的记录时算出来的指纹必然对不上，恢复侧就把一个本来能重放的会话判成损坏。
func (w *RolloutWriter) Flush(ctx context.Context) error {
	if w == nil || !w.enabled || w.q == nil {
		return nil
	}
	reply := make(chan error, 1)
	select {
	case w.q <- rolloutJob{reply: reply}:
	case <-w.quit:
		return nil
	case <-w.loopDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-reply:
		return err
	case <-w.loopDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SetBoardID 记下本次会话的便签板，供 WriteSessionMeta 兜底填充。
func (w *RolloutWriter) SetBoardID(boardID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.boardID = boardID
}

// SetDelegation 记下父子会话的两个坐标，调用点是 createRolloutWriter（它手上就有 ctx）：两个 SessionMeta 写入点都不该各自记得去取，漏一个就是一份没有坐标的文件、被恢复侧判成"不可重放"。
func (w *RolloutWriter) SetDelegation(delegationID, parentSessionID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.delegationID = delegationID
	w.parentSessionID = parentSessionID
}

// WriteSessionMeta 对 task_id / board_id / 两个会话坐标做兜底填充（调用点不该各自记得传，漏传就让恢复侧无法把 rollout 回连到原任务；board 是 session 级的）；git 身份也在这里补齐，因为探测要在**取消路径**上也能跑，而两个写入点分属 agents 与 agents/director，谁都不该抄一份带 WithoutCancel 的 subprocess 代码。
func (w *RolloutWriter) WriteSessionMeta(meta SessionMeta) error {
	if !w.enabled {
		return nil
	}
	if meta.Git.Empty() && meta.Cwd != "" {
		// 探测放在取锁之前：gitinfo 自带 2s 预算，但不能让它在 w.mu 上占住其它写入；writer 没有 ctx，用 Background（gitinfo.Collect 内部保证有界）。
		g := gitinfo.Collect(context.Background(), meta.Cwd)
		meta.Git = GitInfo{SHA: g.SHA, Branch: g.Branch, OriginURL: g.OriginURL}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sessionMetaWritten = true
	if meta.TaskID == "" {
		meta.TaskID = w.taskID
	}
	if meta.BoardID == "" {
		meta.BoardID = w.boardID
	}
	if meta.DelegationID == "" {
		meta.DelegationID = w.delegationID
	}
	if meta.ParentSessionID == "" {
		meta.ParentSessionID = w.parentSessionID
	}
	return w.writeEnvelope("session_meta", meta)
}

// WriteAgentCheckpoint 写入一条停止坐标，且**只在停止路径上写一次**（文件尾本身就是游标，per-step 快照只会把体积翻倍，G8 钉住这条）；需要"读回整份文件"的调用点用 (*RolloutWriter).FinalizeCheckpoint —— 它会先排干队列。
func (w *RolloutWriter) WriteAgentCheckpoint(c AgentCheckpoint) error {
	if !w.enabled {
		return nil
	}
	if c.SchemaVersion == 0 {
		c.SchemaVersion = CheckpointSchemaVersion
	}
	if c.CreatedAt == "" {
		c.CreatedAt = nowRFC3339()
	}
	if c.DelegationID == "" {
		c.DelegationID = w.delegationID
	}
	if c.AgentName == "" {
		c.AgentName = w.agentName
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writeEnvelope(RecordAgentCheckpoint, c)
}

func (w *RolloutWriter) WriteTurnContext(tc TurnContext) error {
	if !w.enabled {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if tc.TurnID != "" {
		w.turnID = tc.TurnID
	}
	return w.writeEnvelope("turn_context", tc)
}

func (w *RolloutWriter) WriteResponseItem(item ResponseItem) error {
	if !w.enabled {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.msgCounter++
	return w.writeEnvelope("response_item", item)
}

func (w *RolloutWriter) WriteEventMsg(event EventMsg) error {
	if !w.enabled {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writeEnvelope("event_msg", event)
}

// RunState 一次运行的断点状态，随终止事件落盘；恢复侧优先用 StopReason 判定结局，缺失时（存量文件）才退化为按消息形态推断。StopReason 取值沿用 director.PlanResult.StopReason：agent_exit / plain_text（正常）与 max_steps / cancelled / llm_error / circuit_breaker_open（中断）。
type RunState struct {
	Steps        int
	MaxSteps     int
	StopReason   string
	HasDelegated bool
}

func (w *RolloutWriter) WriteRunComplete(rs RunState) error {
	return w.WriteEventMsg(EventMsg{
		Type:         "task_complete",
		StepNumber:   rs.Steps,
		MaxSteps:     rs.MaxSteps,
		StopReason:   rs.StopReason,
		HasDelegated: rs.HasDelegated,
	})
}

// WriteRunAborted 写入 turn_aborted 终止事件；reason 是面向人的中止原因（如 ctx.Err()），StopReason 是机器可判定的分类。
func (w *RolloutWriter) WriteRunAborted(reason string, rs RunState) error {
	return w.WriteEventMsg(EventMsg{
		Type:         "turn_aborted",
		Reason:       reason,
		StepNumber:   rs.Steps,
		MaxSteps:     rs.MaxSteps,
		StopReason:   rs.StopReason,
		HasDelegated: rs.HasDelegated,
	})
}

func (w *RolloutWriter) WriteCompacted(payload CompactedPayload) error {
	if !w.enabled {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writeEnvelope("compacted", payload)
}

// WriteTokenCount 写入 token_count 事件，自动累计 total 用量
func (w *RolloutWriter) WriteTokenCount(promptTokens, completionTokens, totalTokens, cacheCreationTokens, cacheReadTokens int64) error {
	if !w.enabled || promptTokens <= 0 && completionTokens <= 0 {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	w.totalInputTokens += promptTokens
	w.totalOutputTokens += completionTokens
	w.totalCachedTokens += cacheReadTokens
	w.totalTokens += totalTokens

	// 形状必须符合 rollout.md 规范
	info := map[string]interface{}{
		"total_token_usage": map[string]interface{}{
			"input_tokens":        w.totalInputTokens,
			"cached_input_tokens": w.totalCachedTokens,
			"output_tokens":       w.totalOutputTokens,
			"total_tokens":        w.totalTokens,
		},
		"last_token_usage": map[string]interface{}{
			"input_tokens":        promptTokens,
			"cached_input_tokens": cacheReadTokens,
			"output_tokens":       completionTokens,
			"total_tokens":        totalTokens,
		},
	}

	return w.writeEnvelope("event_msg", EventMsg{
		Type: "token_count",
		Info: info,
	})
}

func (w *RolloutWriter) NextTurn() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.turnCounter++
	w.turnID = fmt.Sprintf("%s_turn_%d", w.sessionID, w.turnCounter)
	return w.turnID
}

// NextMessageID 计数统一走 w.mu —— 原来它用 atomic.AddInt32、而 WriteResponseItem 在锁内裸 ++，两套同步管同一个字段就是 R9 那个竞态（AGENTS.md §11 坑 19/20 的同族）。
func (w *RolloutWriter) NextMessageID() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.msgCounter++
	return fmt.Sprintf("msg_%d", w.msgCounter-1)
}

func (w *RolloutWriter) CurrentTurnID() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.turnID
}

func (w *RolloutWriter) SessionID() string {
	return w.sessionID
}

func (w *RolloutWriter) SessionMetaWritten() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.sessionMetaWritten
}

func (w *RolloutWriter) Enabled() bool {
	return w.enabled
}

func (w *RolloutWriter) FilePath() string {
	return w.filePath
}

// Close 停止接收、排干队列、刷盘、关文件，并**等写循环真的退出**：原来不 join，于是"Close 返回了但最后几条还在队列里"是可能状态，而 /resume 恰好就读这份文件；join 之后 file 才归调用者所有。
func (w *RolloutWriter) Close() error {
	if !w.enabled || w.q == nil {
		return nil
	}
	w.closeOnce.Do(func() {
		w.mu.Lock()
		w.closed = true
		w.mu.Unlock()

		close(w.quit)
		<-w.loopDone
		w.closeErr = w.file.Close()
	})
	return w.closeErr
}

func homeDirOrFallback() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return homeDir
}
