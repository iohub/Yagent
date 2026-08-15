package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// rolloutWriterCtxKey 私有 context key 类型
type rolloutWriterCtxKey struct{}

// WithRolloutWriter 将 RolloutWriter 注入到 context 中
func WithRolloutWriter(ctx context.Context, w *RolloutWriter) context.Context {
	if w == nil || !w.Enabled() {
		return ctx
	}
	return context.WithValue(ctx, rolloutWriterCtxKey{}, w)
}

// GetRolloutWriter 从 context 中取出 RolloutWriter，不存在则返回 nil
func GetRolloutWriter(ctx context.Context) *RolloutWriter {
	if w, ok := ctx.Value(rolloutWriterCtxKey{}).(*RolloutWriter); ok {
		return w
	}
	return nil
}

// RolloutWriter Codex Rollout JSONL 实时写入器
type RolloutWriter struct {
	mu                sync.Mutex
	file              *os.File
	encoder           *json.Encoder
	enabled           bool
	closed            bool
	filePath          string
	sessionID         string
	agentName         string
	taskID            string
	projectID         string
	turnID            string
	turnCounter       int
	msgCounter        int32
	startTime         time.Time
	sessionMetaWritten bool
}

// generateSessionID 生成会话 ID（时间戳 + 随机数）
func generateSessionID() string {
	timestamp := time.Now().Format("20060102_150405")
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return timestamp + "_00000000"
	}
	return fmt.Sprintf("%s_%s", timestamp, hex.EncodeToString(b))
}

// NewRolloutWriter 创建 Rollout 写入器
func NewRolloutWriter(agentName, taskID, projectID string) (*RolloutWriter, error) {
	sessionID := generateSessionID()

	// 确定输出目录
	outputDir := filepath.Join(homeDirOrFallback(), ".codeactor", "data", "rollout")
	if projectID == "" {
		projectID = "default"
	}
	outputDir = filepath.Join(outputDir, projectID)

	// 创建目录
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		slog.Warn("RolloutWriter: failed to create output directory", "path", outputDir, "error", err)
		return &RolloutWriter{enabled: false, sessionID: sessionID, agentName: agentName, taskID: taskID, projectID: projectID}, fmt.Errorf("failed to create output directory: %w", err)
	}

	// 生成文件名
	timestamp := time.Now().Format("20060102_150405")
	var filename string
	if agentName != "" {
		filename = fmt.Sprintf("%s_%s_%s.jsonl", timestamp, sessionID, agentName)
	} else {
		filename = fmt.Sprintf("%s_%s.jsonl", timestamp, sessionID)
	}
	filePath := filepath.Join(outputDir, filename)

	// 打开文件
	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		slog.Warn("RolloutWriter: failed to open output file", "path", filePath, "error", err)
		return &RolloutWriter{enabled: false, sessionID: sessionID, agentName: agentName, taskID: taskID, projectID: projectID}, fmt.Errorf("failed to open output file: %w", err)
	}

	return &RolloutWriter{
		file:        file,
		encoder:     json.NewEncoder(file),
		enabled:     true,
		sessionID:   sessionID,
		agentName:   agentName,
		taskID:      taskID,
		projectID:   projectID,
		startTime:   time.Now(),
		msgCounter:  0,
		turnCounter: 0,
	}, nil
}

// writeEnvelope 写入一条 rollout 记录（内部方法，调用时需已加锁）
func (w *RolloutWriter) writeEnvelope(recordType string, payload interface{}) error {
	envelope := RolloutEnvelope{
		Timestamp: nowISO8601(),
		Type:      recordType,
		Payload:   payload,
	}
	if err := w.encoder.Encode(envelope); err != nil {
		slog.Warn("RolloutWriter: failed to encode record", "error", err)
		return fmt.Errorf("failed to encode record: %w", err)
	}
	return nil
}

// WriteSessionMeta 写入会话元数据
func (w *RolloutWriter) WriteSessionMeta(meta SessionMeta) error {
	if !w.enabled || w.file == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sessionMetaWritten = true
	return w.writeEnvelope("session_meta", meta)
}

// WriteTurnContext 写入回合上下文
func (w *RolloutWriter) WriteTurnContext(tc TurnContext) error {
	if !w.enabled || w.file == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	// 更新当前 turnID
	if tc.TurnID != "" {
		w.turnID = tc.TurnID
	}
	return w.writeEnvelope("turn_context", tc)
}

// WriteResponseItem 写入模型响应项
func (w *RolloutWriter) WriteResponseItem(item ResponseItem) error {
	if !w.enabled || w.file == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	// 递增消息计数器
	w.msgCounter++
	return w.writeEnvelope("response_item", item)
}

// WriteEventMsg 写入运行时事件
func (w *RolloutWriter) WriteEventMsg(event EventMsg) error {
	if !w.enabled || w.file == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writeEnvelope("event_msg", event)
}

// WriteInterAgentComm 写入多 Agent 通信元数据
func (w *RolloutWriter) WriteInterAgentComm(meta InterAgentCommunicationMetadata) error {
	if !w.enabled || w.file == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writeEnvelope("inter_agent_communication_metadata", meta)
}

// WriteCompacted 写入上下文压缩快照
func (w *RolloutWriter) WriteCompacted(payload CompactedPayload) error {
	if !w.enabled || w.file == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writeEnvelope("compacted", payload)
}

// WriteWorldState 写入世界状态快照
func (w *RolloutWriter) WriteWorldState(payload WorldStatePayload) error {
	if !w.enabled || w.file == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writeEnvelope("world_state", payload)
}

// NextTurn 递增 turnCounter，生成 turnID 并返回
func (w *RolloutWriter) NextTurn() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.turnCounter++
	w.turnID = fmt.Sprintf("%s_turn_%d", w.sessionID, w.turnCounter)
	return w.turnID
}

// NextMessageID 返回 "msg_{n}" 并原子递增
func (w *RolloutWriter) NextMessageID() string {
	n := atomic.AddInt32(&w.msgCounter, 1)
	return fmt.Sprintf("msg_%d", n-1)
}

// CurrentTurnID 返回当前 turnID
func (w *RolloutWriter) CurrentTurnID() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.turnID
}

// SessionID 返回会话 ID
func (w *RolloutWriter) SessionID() string {
	return w.sessionID
}

// SessionMetaWritten 返回是否已写入 session_meta
func (w *RolloutWriter) SessionMetaWritten() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.sessionMetaWritten
}

// Enabled 返回是否启用
func (w *RolloutWriter) Enabled() bool {
	return w.enabled
}

// FilePath 返回文件路径
func (w *RolloutWriter) FilePath() string {
	return w.filePath
}

// Close 关闭文件
func (w *RolloutWriter) Close() error {
	if w.file == nil || w.closed {
		return nil
	}
	w.closed = true
	return w.file.Close()
}

// homeDirOrFallback 获取用户家目录，失败时返回当前目录
func homeDirOrFallback() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return homeDir
}
