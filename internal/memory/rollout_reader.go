package memory

// rollout_reader.go — 基于 Rollout JSONL 的会话恢复（读侧）。
//
// 设计约定：
//   - 只读流式解析，绝不修改/移动/重命名 rollout 文件；
//   - response_item 是消息的唯一事实源；event_msg 只喂运行态（token 用量、结束/中断
//     状态、步数），绝不产消息，避免双通道重复；
//   - 解析 payload 一律复用 rollout_types.go 的类型定义，不复制；
//   - 行级 JSON 解析失败（含崩溃半行）容忍跳过并计数；
//   - 收尾统一做 tool 配对归一化（悬空 call 合成占位输出、孤儿输出丢弃）。

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

// ─── 压缩恢复模式 ───

// CompactionMode 控制 compacted 条目的恢复语义。
type CompactionMode int

const (
	// CompactionFull（默认）全部恢复：compacted 条目前后的所有消息均重建，
	// 是「压缩前 + 压缩后」的超集，语义对 LLM 有效。
	CompactionFull CompactionMode = iota

	// CompactionSummaryTail 只恢复最后一次 compacted 之后的内容：
	// 将其 summary 注入一条 System 消息，之后仅转换其后条目。
	CompactionSummaryTail
)

// ─── 读取选项 ───

// RolloutReadOptions 会话恢复选项。
type RolloutReadOptions struct {
	// MaxMessages 消息容量上限，超过做 tail-keep（截头保留尾部），0=取默认 2000。
	MaxMessages int

	// Compaction 压缩条目的恢复模式，零值=CompactionFull。
	Compaction CompactionMode

	// AttachReasoningSummary 为 true 时，把 reasoning.summary 挂到其后第一条
	// assistant 消息的 Metadata["reasoning_summary"]（encrypted_content 无法还原，始终跳过）。
	AttachReasoningSummary bool

	// MaxToolOutputRunes 单条工具输出的 rune 上限，超出截断。0=取默认 20000；负数=不截断。
	MaxToolOutputRunes int

	// SubAgent 为 true 时，恢复出的全部消息标记 IsSubAgent=true（文件级 sub-agent 标记）。
	SubAgent bool

	// KeepSystemMessages 为 true 时保留写侧落盘的 role=system 消息（Director 完整
	// system prompt，实测约 15k 字符）。默认丢弃：Planner 构造 LLM 消息时本就跳过
	// MessageTypeSystem（planner_run.go 的 system 过滤分支），保留它只会在 TUI 刷屏
	// 并白占 memory/磁盘。压缩摘要锚点由 CompactionSummaryTail 另行注入，不经此路径。
	KeepSystemMessages bool
}

// DefaultRolloutReadOptions 返回默认读取选项。
func DefaultRolloutReadOptions() RolloutReadOptions {
	return RolloutReadOptions{
		MaxMessages:        2000,
		Compaction:         CompactionFull,
		MaxToolOutputRunes: 20000,
	}
}

// normalizeOpts 归一化零值选项（应用默认值）。
func (o RolloutReadOptions) normalizeOpts() RolloutReadOptions {
	if o.MaxMessages <= 0 {
		o.MaxMessages = 2000
	}
	// MaxToolOutputRunes: 0→默认；负数=显式不截断
	if o.MaxToolOutputRunes == 0 {
		o.MaxToolOutputRunes = 20000
	}
	return o
}

// ─── 恢复信息 ───

// RolloutTokenTotals rollout token_count 事件的累计用量（写侧 total_token_usage 口径）。
//
// 字段与 TUI 实时路径（ai_response 的 metadata.usage）逐项对齐，便于恢复后直接落地：
// Input=Σprompt_tokens、CachedInput=Σcache_read_input_tokens、Output=Σcompletion_tokens。
// cache_creation_input_tokens 与 total_input_tokens 未写入 rollout，无法精确恢复——
// 前者恒为 0，后者由读侧按 TUI 既有回退公式（input+cached）近似。
type RolloutTokenTotals struct {
	Input       int64 // Σ prompt_tokens（不含缓存）
	CachedInput int64 // Σ cache_read_input_tokens
	Output      int64 // Σ completion_tokens
	Total       int64 // Σ total_tokens（prompt+completion）

	// LastContextTokens 最后一次 LLM 调用的输入总量（input+cached_input），
	// 作为「上下文瞬时大小」的近似值，对应实时路径写入
	// AgentRunTokens.ContextTokens 的 total_input_tokens。
	LastContextTokens int64
}

// DanglingToolCall 悬空工具调用：有 function_call 但无 function_call_output
//（进程中断/崩溃，或输出被 MaxMessages 截头丢弃）。读侧已为其合成占位输出，
// 此处保留 id+name 供 TUI 时间线标记与续跑提示使用。
type DanglingToolCall struct {
	ID   string
	Name string
}

// RolloutFinishState 会话结束状态。
//
// 三态而非布尔：终止事件在存量文件里大面积缺失——executor 的 task_complete/turn_aborted
// 写入曾被 `cfg.OnAgentExit != nil` 条件包住（未挂该 hook 的 sub-agent 运行一条都不写），
// 实测 146 个可读文件中 89 个无任何终止事件。把「缺终止事件」直接当成「中断」会大量误报，
// 故对无终止事件的文件再按消息形态推断，形态也不足时老实返回 Unknown。
type RolloutFinishState int

const (
	// FinishUnknown 无终止事件，且消息形态不足以推断。
	FinishUnknown RolloutFinishState = iota
	// FinishComplete 正常结束：观测到 task_complete，或虽缺终止事件但末条是
	//「有正文、无待决工具调用」的 assistant（最终答复已产出）。
	FinishComplete
	// FinishInterrupted 中断：观测到 turn_aborted，或存在悬空工具调用（在工具执行中被切断）。
	FinishInterrupted
)

// String 返回结束状态的可读名（用于日志与 TUI 展示）。
func (s RolloutFinishState) String() string {
	switch s {
	case FinishComplete:
		return "complete"
	case FinishInterrupted:
		return "interrupted"
	default:
		return "unknown"
	}
}

// RolloutInfo 描述一次 rollout 恢复的元数据与统计。
type RolloutInfo struct {
	SessionID string
	CWD       string
	Model     string
	// TaskID 原任务 ID（session_meta.task_id）。写侧自 Phase B1 起落盘；
	// 老文件为空，调用方需回退 SessionID。
	TaskID     string
	Originator string
	Source     string
	GitBranch  string
	GitSHA     string

	StartedAt time.Time

	// Entries 有效解析的 envelope 条目数（不含坏行）。
	Entries int
	// Messages 归一化后 memory 中的消息总数。
	Messages int
	// ToolCalls 恢复出的 assistant ToolCalls 总数（含合成 assistant 上的）。
	ToolCalls int

	// SkippedReasoning 跳过的 reasoning 条目数（encrypted_content 不可还原）。
	SkippedReasoning int
	// SkippedBadLines 行级解析失败（含崩溃半行/超限失败）的行数。
	SkippedBadLines int
	// SkippedSystemMessages 因 KeepSystemMessages=false 丢弃的 role=system 消息数。
	SkippedSystemMessages int
	// SyntheticOutputs R12 归一化为悬空 call 合成的占位 tool 输出条数
	//（R5 无前导 assistant 时合成的 assistant 消息不计于此）。
	SyntheticOutputs int
	// DroppedOrphanOutputs 丢弃的孤儿输出（call_id 无对应 call）与重复输出条数。
	DroppedOrphanOutputs int

	// ── 断点状态（从 event_msg 派生；Phase B1 落盘 stop_reason/steps 后转为精确值）──

	// Finished 是否观测到 task_complete 事件。
	Finished bool
	// Aborted 是否观测到 turn_aborted 事件。
	Aborted bool
	// AbortReason 最后一条 turn_aborted 的 reason（无则空）。
	AbortReason string
	// AssistantTurns 恢复出的 assistant 消息数，即 LLM 回合数——Director rollout
	// 无步数事件时的步数近似值。
	AssistantTurns int
	// MaxStepObserved sub_agent_activity 事件的最大 step_number（仅 sub-agent
	// rollout 有该事件，Director 文件恒为 0）。
	MaxStepObserved int
	// LastUserInput 最后一条 user 消息文本，即本轮任务目标（续跑提示用）。
	LastUserInput string
	// DanglingToolCalls 无输出的工具调用清单（与 SyntheticOutputs 同源）。
	DanglingToolCalls []DanglingToolCall
	// EndsWithFinalAnswer 归一化后末条消息是否为「有正文、无待决工具调用」的
	// assistant，即最终答复已产出。缺终止事件时用它推断会话其实已正常结束。
	EndsWithFinalAnswer bool

	// Tokens token_count 事件的累计用量（无该事件时为零值）。
	Tokens RolloutTokenTotals
}

// FinishState 综合判定会话结束状态，强证据优先：
//  1. task_complete → 正常结束；
//  2. turn_aborted → 中断（原因见 AbortReason）；
//  3. 有悬空工具调用 → 中断（在工具执行中被切断，即使终止事件缺失）；
//  4. 末条为最终答复 → 视为正常结束（仅缺终止事件的存量文件）；
//  5. 其余 → Unknown，调用方不应据此断言中断。
func (i RolloutInfo) FinishState() RolloutFinishState {
	switch {
	case i.Finished:
		return FinishComplete
	case i.Aborted:
		return FinishInterrupted
	case len(i.DanglingToolCalls) > 0:
		return FinishInterrupted
	case i.EndsWithFinalAnswer:
		return FinishComplete
	default:
		return FinishUnknown
	}
}

// ─── 顶层 API ───

// ReadRolloutMemory 从 rollout JSONL 文件恢复 ConversationMemory。
//
// 只读流式解析，不修改文件。返回值：
//   - 恢复的 memory（消息为空时返回错误，见下）；
//   - 元数据与统计信息（即使出错也尽量填充已解析部分）；
//   - 错误：文件不可读、或最终零有效消息（空文件/全部坏行/仅有 meta 等）。
func ReadRolloutMemory(path string, opts RolloutReadOptions) (*ConversationMemory, RolloutInfo, error) {
	opts = opts.normalizeOpts()
	p := newRolloutParser(opts)
	if err := p.stream(path); err != nil {
		return nil, p.info, err
	}
	msgs, err := p.finalize()
	info := p.info
	info.Messages = len(msgs)
	if err != nil {
		if errors.Is(err, errNoRolloutMessages) {
			return nil, info, fmt.Errorf("rollout reader %s: %w", path, err)
		}
		return nil, info, err
	}
	mem := NewConversationMemory(0)
	mem.Messages = msgs
	return mem, info, nil
}

// ─── envelope 元解析（raw 解码，避免 Payload 双重反序列化） ───

// rawEnvelope envelope 的原始解码形态：Payload 保留 RawMessage，
// 由调用方按 Type 精确反序列化到 rollout_types.go 的具体 payload 类型。
type rawEnvelope struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

// parseRolloutTimestamp 解析 envelope 时间戳（writer 侧产出 RFC3339 毫秒精度）。
// 第二返回值 false 表示无法解析，调用方需以「上一条时间 +1ms」兜底保证单调。
func parseRolloutTimestamp(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	// RFC3339Nano 优先（兼容纳秒精度）；Go 的 time.Parse 对 RFC3339 布局天然接受小数秒，
	// 两者并列以覆盖不同 writer 产出的精度。
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// rolloutFirstNonEmpty 返回第一个非空字符串。
func rolloutFirstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// ─── 解析器 ───

// rolloutParser rollout 流式解析器。
type rolloutParser struct {
	opts     RolloutReadOptions
	info     RolloutInfo
	messages []ChatMessage
	lastTs   time.Time // 当前时间基准（失败行 +1ms 兜底）
	metaSeen bool      // 是否已记录基准 session_meta（首个为准）
	// pendingReasoningSummary 待挂载的 reasoning summary（挂到其后第一条 assistant）。
	pendingReasoningSummary string
}

// newRolloutParser 创建解析器。
func newRolloutParser(opts RolloutReadOptions) *rolloutParser {
	return &rolloutParser{opts: opts}
}

// reset 重置解析状态（Scanner 超长行降级 Reader 全量重扫前调用）。
func (p *rolloutParser) reset() {
	p.info = RolloutInfo{}
	p.messages = nil
	p.lastTs = time.Time{}
	p.metaSeen = false
	p.pendingReasoningSummary = ""
}

// stream 打开文件并流式解析（Scanner 主路径，超长行降级 Reader 重扫）。
func (p *rolloutParser) stream(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("rollout reader: open %s: %w", path, err)
	}

	scanner := bufio.NewScanner(f)
	// 初始缓冲 1MB，行上限 16MB；超过则整文件降级用 bufio.Reader 拼接重读。
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		p.handleLine(scanner.Text())
	}
	scanErr := scanner.Err()
	f.Close()

	if errors.Is(scanErr, bufio.ErrTooLong) {
		// 超限行降级：重置已累计状态，改用 bufio.Reader 全量重扫。
		p.reset()
		if rerr := p.streamWithReader(path); rerr != nil {
			return rerr
		}
		return nil
	}
	if scanErr != nil {
		return fmt.Errorf("rollout reader: read %s: %w", path, scanErr)
	}
	return nil
}

// streamWithReader 超长行降级路径：bufio.Reader 逐行拼接读取（无上限，坏行容忍）。
func (p *rolloutParser) streamWithReader(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("rollout reader: open %s: %w", path, err)
	}
	defer f.Close()

	reader := bufio.NewReaderSize(f, 1024*1024)
	for {
		line, readErr := reader.ReadString('\n')
		p.handleLine(line)
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return fmt.Errorf("rollout reader: read %s: %w", path, readErr)
		}
	}
}

// handleLine 处理单行（空白行直接忽略，不计坏行）。
func (p *rolloutParser) handleLine(line string) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return
	}
	p.handleEnvelope([]byte(trimmed))
}

// handleEnvelope 解析并分发一条 envelope。
func (p *rolloutParser) handleEnvelope(line []byte) {
	var env rawEnvelope
	if err := jsonUnmarshalRollout(line, &env); err != nil {
		p.info.SkippedBadLines++
		return
	}

	// 时间戳：解析失败 → 上一条 +1ms，保证单调（R11）。
	ts, ok := parseRolloutTimestamp(env.Timestamp)
	if !ok {
		ts = p.lastTs.Add(time.Millisecond)
	}
	p.lastTs = ts
	if p.info.StartedAt.IsZero() {
		p.info.StartedAt = ts
	}

	switch env.Type {
	case "session_meta": // R1：不进消息；首个 meta 为基准（容忍续写重复 meta）
		var meta SessionMeta
		if err := jsonUnmarshalRollout(env.Payload, &meta); err != nil {
			p.info.SkippedBadLines++
			return
		}
		p.info.Entries++
		if !p.metaSeen {
			p.metaSeen = true
			p.info.SessionID = rolloutFirstNonEmpty(meta.SessionID, meta.ID)
			p.info.TaskID = meta.TaskID
			p.info.CWD = meta.Cwd
			p.info.Originator = meta.Originator
			p.info.Source = meta.Source
			p.info.GitBranch = meta.Git.Branch
			p.info.GitSHA = meta.Git.SHA
		}

	case "turn_context": // R2：不进消息；记录最新 model/cwd
		var tc TurnContext
		if err := jsonUnmarshalRollout(env.Payload, &tc); err != nil {
			p.info.SkippedBadLines++
			return
		}
		p.info.Entries++
		if tc.Model != "" {
			p.info.Model = tc.Model
		}
		if tc.Cwd != "" {
			p.info.CWD = tc.Cwd
		}

	case "response_item": // R3~R8：消息唯一事实源
		var item ResponseItem
		if err := jsonUnmarshalRollout(env.Payload, &item); err != nil {
			p.info.SkippedBadLines++
			return
		}
		p.info.Entries++
		p.handleResponseItem(item, ts)

	case "event_msg": // R16：运行态通道（token 用量/结束状态/步数），不产消息
		var em EventMsg
		if err := jsonUnmarshalRollout(env.Payload, &em); err != nil {
			p.info.SkippedBadLines++
			return
		}
		p.info.Entries++
		p.handleEventMsg(em)

	case "compacted": // R9
		var cp CompactedPayload
		if err := jsonUnmarshalRollout(env.Payload, &cp); err != nil {
			p.info.SkippedBadLines++
			return
		}
		p.info.Entries++
		p.handleCompacted(cp, ts)

	default:
		// R8/R10：inter_agent_communication_metadata、world_state 及任何未知 type
		// 一律不进消息。
		p.info.Entries++
	}
}

// ─── event_msg 运行态提取（R16） ───

// handleEventMsg 从 event_msg 提取运行态。
//
// 严格只写 p.info 的状态字段，不追加任何消息——response_item 仍是消息的唯一事实源，
// 否则 agent_message/user_message 等事件会与 response_item 产生双通道重复。
// 每个 run 独占一个 rollout 文件，故 task_complete/turn_aborted 至多出现一次；
// 仍按「后到者胜」处理以容忍续写。
func (p *rolloutParser) handleEventMsg(em EventMsg) {
	switch em.Type {
	case "token_count":
		p.applyTokenCount(em.Info)

	case "task_complete":
		p.info.Finished = true

	case "turn_aborted":
		p.info.Aborted = true
		if em.Reason != "" {
			p.info.AbortReason = em.Reason
		}

	case "sub_agent_activity":
		// Director rollout 无步数事件；sub-agent 文件经 StepInfoToEventMsg 落盘
		// step_number，取最大值作为已观测步数。
		if em.StepNumber > p.info.MaxStepObserved {
			p.info.MaxStepObserved = em.StepNumber
		}
	}
}

// applyTokenCount 提取 token_count 事件的用量。
//
// 写侧（RolloutWriter.WriteTokenCount）的 total_token_usage 已是累计值，故直接覆盖
//（取最后一条即全量），不能再累加；last_token_usage 是本次调用值，用于上下文瞬时大小。
func (p *rolloutParser) applyTokenCount(info interface{}) {
	payload, ok := info.(map[string]interface{})
	if !ok {
		return
	}
	if total, ok := payload["total_token_usage"].(map[string]interface{}); ok {
		p.info.Tokens.Input = rolloutInt64(total, "input_tokens")
		p.info.Tokens.CachedInput = rolloutInt64(total, "cached_input_tokens")
		p.info.Tokens.Output = rolloutInt64(total, "output_tokens")
		p.info.Tokens.Total = rolloutInt64(total, "total_tokens")
	}
	if last, ok := payload["last_token_usage"].(map[string]interface{}); ok {
		p.info.Tokens.LastContextTokens = rolloutInt64(last, "input_tokens") +
			rolloutInt64(last, "cached_input_tokens")
	}
}

// rolloutInt64 从 JSON 解码后的 map 取整数字段（encoding/json 把数字解为 float64）。
func rolloutInt64(m map[string]interface{}, key string) int64 {
	if v, ok := m[key].(float64); ok {
		return int64(v)
	}
	return 0
}

// jsonUnmarshalRollout roll.out 专用 json.Unmarshal 薄封装（预留统一错误增强点）。
func jsonUnmarshalRollout(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}

// ─── response_item 转换（R3~R8） ───

// handleResponseItem 将一条 response_item 转为消息（或忽略）。
func (p *rolloutParser) handleResponseItem(item ResponseItem, ts time.Time) {
	switch item.Type {
	case "message":
		p.handleMessageItem(item, ts)
	case "function_call": // R5
		p.attachToolCall(item, ts)
	case "function_call_output": // R6
		p.addToolOutput(item, ts)
	case "reasoning": // R7：跳过（encrypted_content 不可还原）
		p.info.SkippedReasoning++
		if p.opts.AttachReasoningSummary {
			if summary := rolloutExtractText(item.Summary, "summary_text"); summary != "" {
				p.pendingReasoningSummary = summary
			}
		}
	default:
		// 未知的 response_item 子类型：忽略。
	}
}

// handleMessageItem role=message → user/system/assistant。
func (p *rolloutParser) handleMessageItem(item ResponseItem, ts time.Time) {
	switch item.Role {
	case "user": // R3
		content := rolloutExtractText(item.Content, "input_text")
		// 写侧每轮把完整历史重写入新文件，故最后一条 user 即本轮任务目标。
		if content != "" {
			p.info.LastUserInput = content
		}
		p.messages = append(p.messages, ChatMessage{
			Type:      MessageTypeHuman,
			Content:   content,
			Timestamp: ts,
		})
	case "assistant": // R4：空内容但紧随 function_call 的 assistant 也保留（挂载点）
		msg := ChatMessage{
			Type:      MessageTypeAssistant,
			Content:   rolloutExtractText(item.Content, "output_text"),
			Timestamp: ts,
		}
		if p.pendingReasoningSummary != "" {
			msg.Metadata = map[string]interface{}{"reasoning_summary": p.pendingReasoningSummary}
			p.pendingReasoningSummary = ""
		}
		p.messages = append(p.messages, msg)
	case "system":
		// 写侧（ChatMessageToResponseItems）会把 system 消息以 role=system 落盘。
		// 默认丢弃：Planner 续跑时本就跳过 MessageTypeSystem，保留只会刷屏 + 白占空间
		//（Director system prompt 实测约 15k 字符）。KeepSystemMessages 时对称恢复。
		if !p.opts.KeepSystemMessages {
			p.info.SkippedSystemMessages++
			return
		}
		p.messages = append(p.messages, ChatMessage{
			Type:      MessageTypeSystem,
			Content:   rolloutExtractText(item.Content, "input_text"),
			Timestamp: ts,
		})
	default:
		// 其他 role（如未来的 developer 等）：忽略。
	}
}

// lastAssistant 返回当前尾部的 assistant 消息（存在则返回指针），无则 nil。
func (p *rolloutParser) lastAssistant() *ChatMessage {
	if len(p.messages) == 0 {
		return nil
	}
	last := &p.messages[len(p.messages)-1]
	if last.Type != MessageTypeAssistant {
		return nil
	}
	return last
}

// withMetadata 在 msg.Metadata 中写入键值（惰性创建 map）。
func withMetadata(msg *ChatMessage, key string, value interface{}) {
	if msg.Metadata == nil {
		msg.Metadata = make(map[string]interface{})
	}
	msg.Metadata[key] = value
}

// attachToolCall R5：function_call 挂到紧邻前一条 assistant 的 ToolCalls；
// 前一条不是 assistant（或不存在）→ 先合成一条空内容 assistant 再挂。
func (p *rolloutParser) attachToolCall(item ResponseItem, ts time.Time) {
	host := p.lastAssistant()
	if host == nil {
		// 合成空 assistant（SyntheticOutputs 不计于此——仅 R12 悬空 call 计合成输出）。
		p.messages = append(p.messages, ChatMessage{
			Type:      MessageTypeAssistant,
			Content:   "",
			Timestamp: ts,
		})
		host = &p.messages[len(p.messages)-1]
	}
	host.ToolCalls = append(host.ToolCalls, ToolCallData{
		ID:   item.CallID,
		Type: "function",
		Function: ToolCallFunction{
			Name:      item.Name,
			Arguments: json.RawMessage(item.Arguments), // 保留原始 JSON 字符串
		},
	})
	// namespace 非空时存入 Metadata，不污染 name。
	if item.Namespace != "" {
		withMetadata(host, "tool_namespace", item.Namespace)
	}
	p.info.ToolCalls++
}

// addToolOutput R6：function_call_output → Tool 消息。
func (p *rolloutParser) addToolOutput(item ResponseItem, ts time.Time) {
	meta := map[string]interface{}{}
	content := decodeRolloutToolOutput(item.Output, meta)
	content = truncateRolloutText(content, p.opts.MaxToolOutputRunes)
	msg := ChatMessage{
		Type:       MessageTypeTool,
		Content:    content,
		ToolCallID: strPtr(item.CallID),
		Timestamp:  ts,
	}
	if len(meta) > 0 {
		msg.Metadata = meta
	}
	p.messages = append(p.messages, msg)
}

// handleCompacted R9：
//   - CompactionFull：条目本身不进消息（前后内容全量恢复，超集语义）；
//   - CompactionSummaryTail：丢弃此前全部消息，将 summary 注入一条 System 消息，
//     仅转换其后条目（reasoning 挂载等待状态一并清除）。
func (p *rolloutParser) handleCompacted(cp CompactedPayload, ts time.Time) {
	if p.opts.Compaction != CompactionSummaryTail {
		return
	}
	var kept []ChatMessage
	if cp.Summary != "" {
		kept = append(kept, ChatMessage{
			Type:      MessageTypeSystem,
			Content:   cp.Summary,
			Timestamp: ts,
			IsAnchored: true, // 压缩摘要锚点，后续对话中永不压缩
		})
	}
	p.messages = kept
	p.pendingReasoningSummary = ""
}

// ─── 文本提取与截断 ───

// rolloutExtractText 从消息内容部件中提取文本：
//  1. 优先取 type==preferred 的部件，多部件以 "\n" 拼接（空文本跳过）；
//  2. 无匹配部件时回退拼接所有非空 text。
func rolloutExtractText(content []MessageContentItem, preferred string) string {
	var matched, all []string
	for _, c := range content {
		if c.Text == "" {
			continue
		}
		if preferred != "" && c.Type == preferred {
			matched = append(matched, c.Text)
		}
		all = append(all, c.Text)
	}
	if len(matched) > 0 {
		return strings.Join(matched, "\n")
	}
	return strings.Join(all, "\n")
}

// decodeRolloutToolOutput 解析工具输出的嵌套包装 {"output":...,"metadata":{...}}：
//   - 解析成功且含 "output" 键 → 取 output 字段（字符串直接用，其他 JSON 类型重新序列化），
//     metadata 中的 exit_code/duration_seconds 写入 msgMeta；
//   - 否则原样返回。
func decodeRolloutToolOutput(output string, msgMeta map[string]interface{}) string {
	if output == "" {
		return ""
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(output), &top); err != nil || top == nil {
		return output
	}
	outRaw, ok := top["output"]
	if !ok {
		return output
	}
	var outVal interface{}
	if err := json.Unmarshal(outRaw, &outVal); err != nil {
		return output
	}
	var content string
	switch v := outVal.(type) {
	case nil:
		content = ""
	case string:
		content = v
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return output
		}
		content = string(b)
	}
	if mdRaw, ok := top["metadata"]; ok {
		var md map[string]interface{}
		if json.Unmarshal(mdRaw, &md) == nil {
			for _, key := range []string{"exit_code", "duration_seconds"} {
				if v, ok := md[key]; ok {
					msgMeta[key] = v
				}
			}
		}
	}
	return content
}

// truncateRolloutText 按 rune 截断并追加截断标记（max<=0 不截断）。
func truncateRolloutText(s string, maxRunes int) string {
	if maxRunes <= 0 || utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxRunes]) +
		fmt.Sprintf("\n\n[...工具输出超长已截断：原始 %d 字符，保留前 %d 字符...]", len(runes), maxRunes)
}

// ─── 收尾（R12/R13/R14/R15） ───

// finalize 收尾：R14 容量 tail-keep → R12 配对归一化 → R13 SubAgent 标记 → 空消息检查。
func (p *rolloutParser) finalize() ([]ChatMessage, error) {
	// R14：超 MaxMessages 做 tail-keep（截头保留尾部），截断后再跑归一化。
	if p.opts.MaxMessages > 0 && len(p.messages) > p.opts.MaxMessages {
		p.messages = append([]ChatMessage(nil), p.messages[len(p.messages)-p.opts.MaxMessages:]...)
	}
	// R12：配对归一化（自写逻辑：repairToolCallPairsAfterTruncation 是「移除」语义，
	// 与此处「合成占位补齐」语义不符）。
	p.messages = normalizeToolPairing(p.messages, &p.info)

	// R13：文件级 SubAgent 标记。
	if p.opts.SubAgent {
		for i := range p.messages {
			p.messages[i].IsSubAgent = true
		}
	}

	// R16：断点状态收尾。AssistantTurns 统计归一化后的 assistant 消息（含 R5 合成的
	// 空 assistant，它同样代表一次真实 LLM 回合），作为 Director 文件的步数近似值；
	// EndsWithFinalAnswer 供 FinishState() 在终止事件缺失时推断会话是否其实已结束。
	// 结束状态本身不在此处落字段，统一由 RolloutInfo.FinishState() 按证据派生，
	// 避免出现互相矛盾的存储态。
	for i := range p.messages {
		if p.messages[i].Type == MessageTypeAssistant {
			p.info.AssistantTurns++
		}
	}
	if n := len(p.messages); n > 0 {
		last := p.messages[n-1]
		p.info.EndsWithFinalAnswer = last.Type == MessageTypeAssistant &&
			last.Content != "" && len(last.ToolCalls) == 0
	}

	// R15：零有效消息 → 明确错误（ReadRolloutMemory 会包装真实文件路径）。
	if len(p.messages) == 0 {
		return nil, fmt.Errorf("未恢复出任何有效消息 (entries=%d, bad_lines=%d, skipped_reasoning=%d): %w",
			p.info.Entries, p.info.SkippedBadLines, p.info.SkippedReasoning, errNoRolloutMessages)
	}
	return p.messages, nil
}

// errNoRolloutMessages 零有效消息哨兵错误。
var errNoRolloutMessages = errors.New("no messages recovered from rollout")

// normalizeToolPairing R12：tool 配对归一化（保真优先）。
//   - 悬空 call（assistant ToolCall 无匹配输出，含文件尾中断）→ 在其回合结束处
//     合成 Tool 占位消息"会话中断前未返回结果"，SyntheticOutputs++，
//     并把 id+name 记入 info.DanglingToolCalls（供 TUI 时间线与续跑提示使用）；
//   - 孤儿输出（call_id 无对应 call）→ 丢弃，DroppedOrphanOutputs++；
//   - 同一 call_id 多条输出 → 保留第一条，其余丢弃并计数；
//   - 连续同角色消息保持原样（不做合并）。
//
// openCalls/openSet 维护待配对 call 的保序队列；flush 时机：
// 遇到下一条 assistant（回合结束）与文件尾。
func normalizeToolPairing(messages []ChatMessage, info *RolloutInfo) []ChatMessage {
	type openCall struct {
		id      string
		name    string
		hostIdx int // 宿主 assistant 的 index（用于继承时间戳）
	}
	var openCalls []openCall
	openSet := make(map[string]int) // call_id → openCalls 下标；-1=已消费

	// flushOpen 将所有未配对 call 转为合成 tool 消息（追加到 out 尾部）。
	// 已被真实输出消费的 call（openSet 置 -1）必须跳过，否则会二次合成占位。
	flushOpen := func(out []ChatMessage) []ChatMessage {
		for _, oc := range openCalls {
			if openSet[oc.id] == -1 {
				continue // 已消费：跳过
			}
			ts := time.Time{}
			if oc.hostIdx >= 0 && oc.hostIdx < len(messages) {
				ts = messages[oc.hostIdx].Timestamp
			}
			out = append(out, ChatMessage{
				Type:       MessageTypeTool,
				Content:    "会话中断前未返回结果",
				ToolCallID: strPtr(oc.id),
				Timestamp:  ts,
				Metadata: map[string]interface{}{
					"synthetic":         true,
					"synthetic_reason":  "missing_tool_output",
				},
			})
			info.DanglingToolCalls = append(info.DanglingToolCalls, DanglingToolCall{
				ID:   oc.id,
				Name: oc.name,
			})
			info.SyntheticOutputs++
		}
		openCalls = nil
		openSet = make(map[string]int)
		return out
	}

	out := make([]ChatMessage, 0, len(messages))
	for i, m := range messages {
		if m.Type == MessageTypeAssistant {
			// 新 assistant = 上一回合结束：未配对的 call 合成占位输出补齐在此消息之前。
			out = flushOpen(out)
			out = append(out, m)
			for _, tc := range m.ToolCalls {
				openCalls = append(openCalls, openCall{
					id:      tc.ID,
					name:    tc.Function.Name,
					hostIdx: i,
				})
				openSet[tc.ID] = len(openCalls) - 1
			}
			continue
		}
		if m.Type == MessageTypeTool && m.ToolCallID != nil {
			if idx, ok := openSet[*m.ToolCallID]; ok && idx >= 0 {
				openSet[*m.ToolCallID] = -1 // 已消费（同 id 再现视为重复）
				out = append(out, m)
				continue
			}
			// 孤儿输出或重复输出：丢弃。
			info.DroppedOrphanOutputs++
			continue
		}
		// system/human 等其他消息原样保留。
		out = append(out, m)
	}
	// 文件尾 flush（含中断中断场景）。
	out = flushOpen(out)
	return out
}
