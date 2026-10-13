package memory

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

type CompactionMode int

const (
	CompactionFull CompactionMode = iota

	CompactionSummaryTail
)

type RolloutReadOptions struct {
	MaxMessages int

	Compaction CompactionMode

	AttachReasoningSummary bool

	MaxToolOutputRunes int

	SubAgent bool

	KeepSystemMessages bool
}

func DefaultRolloutReadOptions() RolloutReadOptions {
	return RolloutReadOptions{
		MaxMessages:        2000,
		Compaction:         CompactionFull,
		MaxToolOutputRunes: 20000,
	}
}

func (o RolloutReadOptions) normalizeOpts() RolloutReadOptions {
	if o.MaxMessages <= 0 {
		o.MaxMessages = 2000
	}
	if o.MaxToolOutputRunes == 0 {
		o.MaxToolOutputRunes = 20000
	}
	return o
}

type RolloutTokenTotals struct {
	Input       int64
	CachedInput int64
	Output      int64
	Total       int64

	LastContextTokens int64
}

type DanglingToolCall struct {
	ID   string
	Name string
}

type RolloutFinishState int

const (
	FinishUnknown RolloutFinishState = iota
	FinishComplete
	FinishInterrupted
)

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

type RolloutInfo struct {
	SessionID  string
	CWD        string
	Model      string
	TaskID     string
	// BoardID 是这次会话用的便签板（session_meta.board_id）。board 是 session 级的，
	// 一块板横跨多个 task，/resume 只有拿到它才能续上便签；存量 rollout 里是空串。
	BoardID    string
	Originator string
	Source     string
	GitBranch  string
	GitSHA     string

	StartedAt time.Time

	Entries   int
	Messages  int
	ToolCalls int

	SkippedReasoning      int
	SkippedBadLines       int
	SkippedSystemMessages int
	SyntheticOutputs      int
	DroppedOrphanOutputs  int

	Finished            bool
	Aborted             bool
	AbortReason         string
	Steps               int
	MaxSteps            int
	StopReason          string
	HasDelegated        bool
	AssistantTurns      int
	MaxStepObserved     int
	LastUserInput       string
	DanglingToolCalls   []DanglingToolCall
	EndsWithFinalAnswer bool

	Tokens RolloutTokenTotals

	ThinklinkEntries []RolloutThinklinkEntry
}

type RolloutThinklinkEntry struct {
	Kind    string
	Content string
	Step    int
}

func (i RolloutInfo) BestStep() int {
	switch {
	case i.Steps > 0:
		return i.Steps
	case i.MaxStepObserved > 0:
		return i.MaxStepObserved
	default:
		return i.AssistantTurns
	}
}

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

type rawEnvelope struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

func parseRolloutTimestamp(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func rolloutFirstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

type rolloutParser struct {
	opts                    RolloutReadOptions
	info                    RolloutInfo
	messages                []ChatMessage
	lastTs                  time.Time
	metaSeen                bool
	pendingReasoningSummary string
}

func newRolloutParser(opts RolloutReadOptions) *rolloutParser {
	return &rolloutParser{opts: opts}
}

func (p *rolloutParser) reset() {
	p.info = RolloutInfo{}
	p.messages = nil
	p.lastTs = time.Time{}
	p.metaSeen = false
	p.pendingReasoningSummary = ""
}

func (p *rolloutParser) stream(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("rollout reader: open %s: %w", path, err)
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		p.handleLine(scanner.Text())
	}
	scanErr := scanner.Err()
	f.Close()

	if errors.Is(scanErr, bufio.ErrTooLong) {
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

func (p *rolloutParser) handleLine(line string) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return
	}
	p.handleEnvelope([]byte(trimmed))
}

func (p *rolloutParser) handleEnvelope(line []byte) {
	var env rawEnvelope
	if err := jsonUnmarshalRollout(line, &env); err != nil {
		p.info.SkippedBadLines++
		return
	}

	ts, ok := parseRolloutTimestamp(env.Timestamp)
	if !ok {
		ts = p.lastTs.Add(time.Millisecond)
	}
	p.lastTs = ts
	if p.info.StartedAt.IsZero() {
		p.info.StartedAt = ts
	}

	switch env.Type {
	case "session_meta":
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
			p.info.BoardID = meta.BoardID
			p.info.CWD = meta.Cwd
			p.info.Originator = meta.Originator
			p.info.Source = meta.Source
			p.info.GitBranch = meta.Git.Branch
			p.info.GitSHA = meta.Git.SHA
		}

	case "turn_context":
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

	case "response_item":
		var item ResponseItem
		if err := jsonUnmarshalRollout(env.Payload, &item); err != nil {
			p.info.SkippedBadLines++
			return
		}
		p.info.Entries++
		p.handleResponseItem(item, ts)

	case "event_msg":
		var em EventMsg
		if err := jsonUnmarshalRollout(env.Payload, &em); err != nil {
			p.info.SkippedBadLines++
			return
		}
		p.info.Entries++
		p.handleEventMsg(em)

	case "compacted":
		var cp CompactedPayload
		if err := jsonUnmarshalRollout(env.Payload, &cp); err != nil {
			p.info.SkippedBadLines++
			return
		}
		p.info.Entries++
		p.handleCompacted(cp, ts)

	default:
		p.info.Entries++
	}
}

func (p *rolloutParser) handleEventMsg(em EventMsg) {
	switch em.Type {
	case "token_count":
		p.applyTokenCount(em.Info)

	case "task_complete":
		p.info.Finished = true
		p.applyRunState(em)

	case "turn_aborted":
		p.info.Aborted = true
		if em.Reason != "" {
			p.info.AbortReason = em.Reason
		}
		p.applyRunState(em)

	case "sub_agent_activity":

		if em.StepNumber > p.info.MaxStepObserved {
			p.info.MaxStepObserved = em.StepNumber
		}

	case "thinklink_entry":
		p.info.ThinklinkEntries = append(p.info.ThinklinkEntries, RolloutThinklinkEntry{
			Kind:    em.Kind,
			Content: em.Text,
			Step:    em.StepNumber,
		})
	}
}

func (p *rolloutParser) applyRunState(em EventMsg) {
	if em.StepNumber > 0 {
		p.info.Steps = em.StepNumber
	}
	if em.MaxSteps > 0 {
		p.info.MaxSteps = em.MaxSteps
	}
	if em.StopReason != "" {
		p.info.StopReason = em.StopReason
	}
	p.info.HasDelegated = em.HasDelegated
}

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

func rolloutInt64(m map[string]interface{}, key string) int64 {
	if v, ok := m[key].(float64); ok {
		return int64(v)
	}
	return 0
}

func jsonUnmarshalRollout(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}

func (p *rolloutParser) handleResponseItem(item ResponseItem, ts time.Time) {
	switch item.Type {
	case "message":
		p.handleMessageItem(item, ts)
	case "function_call":
		p.attachToolCall(item, ts)
	case "function_call_output":
		p.addToolOutput(item, ts)
	case "reasoning":
		p.info.SkippedReasoning++
		if p.opts.AttachReasoningSummary {
			if summary := rolloutExtractText(item.Summary, "summary_text"); summary != "" {
				p.pendingReasoningSummary = summary
			}
		}
	default:
	}
}

func (p *rolloutParser) handleMessageItem(item ResponseItem, ts time.Time) {
	switch item.Role {
	case "user":
		content := rolloutExtractText(item.Content, "input_text")
		if content != "" {
			p.info.LastUserInput = content
		}
		p.messages = append(p.messages, ChatMessage{
			Type:      MessageTypeHuman,
			Content:   content,
			Timestamp: ts,
		})
	case "assistant":
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
	}
}

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

func withMetadata(msg *ChatMessage, key string, value interface{}) {
	if msg.Metadata == nil {
		msg.Metadata = make(map[string]interface{})
	}
	msg.Metadata[key] = value
}

func (p *rolloutParser) attachToolCall(item ResponseItem, ts time.Time) {
	host := p.lastAssistant()
	if host == nil {
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
			Arguments: json.RawMessage(item.Arguments),
		},
	})
	if item.Namespace != "" {
		withMetadata(host, "tool_namespace", item.Namespace)
	}
	p.info.ToolCalls++
}

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

func (p *rolloutParser) handleCompacted(cp CompactedPayload, ts time.Time) {
	if p.opts.Compaction != CompactionSummaryTail {
		return
	}
	var kept []ChatMessage
	if cp.Summary != "" {
		kept = append(kept, ChatMessage{
			Type:       MessageTypeSystem,
			Content:    cp.Summary,
			Timestamp:  ts,
			IsAnchored: true,
		})
	}
	p.messages = kept
	p.pendingReasoningSummary = ""
}

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

func truncateRolloutText(s string, maxRunes int) string {
	if maxRunes <= 0 || utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxRunes]) +
		fmt.Sprintf("\n\n[...工具输出超长已截断：原始 %d 字符，保留前 %d 字符...]", len(runes), maxRunes)
}

func (p *rolloutParser) finalize() ([]ChatMessage, error) {
	if p.opts.MaxMessages > 0 && len(p.messages) > p.opts.MaxMessages {
		p.messages = append([]ChatMessage(nil), p.messages[len(p.messages)-p.opts.MaxMessages:]...)
	}
	p.messages = normalizeToolPairing(p.messages, &p.info)

	if p.opts.SubAgent {
		for i := range p.messages {
			p.messages[i].IsSubAgent = true
		}
	}

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

	if len(p.messages) == 0 {
		return nil, fmt.Errorf("未恢复出任何有效消息 (entries=%d, bad_lines=%d, skipped_reasoning=%d): %w",
			p.info.Entries, p.info.SkippedBadLines, p.info.SkippedReasoning, errNoRolloutMessages)
	}
	return p.messages, nil
}

var errNoRolloutMessages = errors.New("no messages recovered from rollout")

func normalizeToolPairing(messages []ChatMessage, info *RolloutInfo) []ChatMessage {
	type openCall struct {
		id      string
		name    string
		hostIdx int
	}
	var openCalls []openCall
	openSet := make(map[string]int)

	flushOpen := func(out []ChatMessage) []ChatMessage {
		for _, oc := range openCalls {
			if openSet[oc.id] == -1 {
				continue
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
					"synthetic":        true,
					"synthetic_reason": "missing_tool_output",
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
				openSet[*m.ToolCallID] = -1
				out = append(out, m)
				continue
			}
			info.DroppedOrphanOutputs++
			continue
		}
		out = append(out, m)
	}
	out = flushOpen(out)
	return out
}
