package memory

// rollout_replay.go — 把一份 rollout 重建成"可以直接喂回模型"的消息序列。
//
// 为什么不复用 ReadRolloutMemory：那一版是为 TUI 展示与 Director memory 服务的，
// 它会丢 system 消息、把 Reasoning 压成可选的 summary、把工具输出截到 20000 rune、
// 上限 2000 条，并且在 opts.SubAgent 时把所有消息打上 IsSubAgent —— 而 planner 构造
// LLM messages 时正好把这些全过滤掉。重放的判据是"模型看到的世界与暂停前一致"，
// 上面任何一条都让那个判据失效，而且是静默失效。
//
// 所以这里是一条独立的读侧：逐条还原 llm.Message，保留 Reasoning 与完整工具输出，
// 并且把任何"没能完整还原"的事情写进 Degraded —— 恢复侧与 UI 都读它，绝不静默降级。
//
// 消息还原规则与 LLMMessageToResponseItems 的写出规则一一对偶（改一边必须改另一边，
// 由 rollout_replay_test.go 的双向用例钉住）：
//   - message(role=system|user)          → 同角色消息
//   - reasoning                          → 附着到下一条 assistant 消息的 Reasoning
//   - message(role=assistant) 后紧跟的 function_call → 合并进同一条 assistant 消息
//   - function_call_output               → role=tool（Output 是 {"output":…} 信封）
//   - compacted                          → 丢弃此前消息只留摘要（那正是当时模型看到的）

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"yagent/internal/llm"
)

// ErrReplayTooLarge 表示重建出的上下文超出调用方给的 token 预算。
// 这不是"读失败"，而是"重放会立刻触发压缩、从而失去重放意义"——
// 调用方据此降级到交接报告（docs/pause-resume-design.md §6.4 判据 4）。
var ErrReplayTooLarge = errors.New("rollout replay: rebuilt context exceeds token budget")

// ReplayThinkLinkEntry 是 memory 自己的 T&P 条目形状。
// 不用 thinklink.Entry：那会让 memory 反向依赖 internal/thinklink；
// 由 agents 侧做一次机械转换，换来的是依赖图不变。
type ReplayThinkLinkEntry struct {
	Kind    string // thinklink 的 Kind 字符串（user_input / thought_plan）
	Content string
	Step    int
}

// openCall 是一个已发出但还没收到结果的调用。
type openCall struct {
	id   string
	name string
	args string
}

// Replay 是一份 rollout 的重建结果。
type Replay struct {
	Messages        []llm.Message
	Step            int // 重放起点（已完成步数）
	Materialized    []string
	ThinkLink       []ReplayThinkLinkEntry
	Digest          string
	Checkpoint      *AgentCheckpoint // 文件里没有则为 nil（崩溃 / 存量文件）
	DelegationID    string
	ParentSessionID string
	AgentName       string
	Model           string
	// Dangling 是"发起了却没等到结果"的调用，顺序即文件顺序。恢复侧用它回答两件事：
	// Director 那一份里停住的是哪一次委派，以及子 Agent 那一份最后一步做完没有。
	Dangling  []DanglingToolCall
	EstTokens int
	Degraded  []string

	danglingArgs map[string]string
}

// ReplayOptions 是重建的限制条件；零值 = 不限制。
type ReplayOptions struct {
	MaxMessages    int
	TokenBudget    int
	EstimateTokens func(string) int
}

// DanglingCallArgs 取某个悬空调用的原始参数 JSON（找不到返回空串）。
// 重放一次委派必须原样复用它的 args —— 重新生成参数等于换了个任务，
// 那正是"看起来恢复了，其实做的是另一件事"的入口。
func (r *Replay) DanglingCallArgs(callID string) string {
	if r == nil || r.danglingArgs == nil {
		return ""
	}
	return r.danglingArgs[callID]
}

// LastDangling 返回最后一个悬空调用；没有则 ok=false。
// Director 那份文件里它就是"停在哪个委派上"的答案。
func (r *Replay) LastDangling() (DanglingToolCall, bool) {
	if len(r.Dangling) == 0 {
		return DanglingToolCall{}, false
	}
	return r.Dangling[len(r.Dangling)-1], true
}

// noteDegraded 记一条"没能完整还原"的事实。
func (r *Replay) noteDegraded(what string) {
	r.Degraded = append(r.Degraded, what)
}

// CheckpointKind 返回这份会话是怎么停的；没有检查点时返回空串（调用方必须把
// "空"当成"不知道"，而不是当成"正常结束"）。
func (r *Replay) CheckpointKind() string {
	if r == nil || r.Checkpoint == nil {
		return ""
	}
	return r.Checkpoint.StopKind
}

// CheckpointWorkspace 返回检查点里的工作区事实；没有检查点则 nil。
func (r *Replay) CheckpointWorkspace() *WorkspaceState {
	if r == nil || r.Checkpoint == nil {
		return nil
	}
	ws := r.Checkpoint.Workspace
	return &ws
}

// ReadRolloutForReplay 重建一份 rollout。
//
// 预算超限时它**同时**返回已重建的 Replay 和 ErrReplayTooLarge：调用方要用同一份
// 事实去生成降级报告，只返回 error 的话那一步就得再读一遍文件（两遍之间文件可能变）。
func ReadRolloutForReplay(path string, opts ReplayOptions) (*Replay, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("rollout replay: open %s: %w", path, err)
	}
	defer f.Close()

	r := &Replay{danglingArgs: map[string]string{}}
	scanner := bufio.NewScanner(f)
	// 与 ReadRolloutMemory 同一档行缓冲：一条工具输出就是一行，几十万像素的截图或
	// 长 diff 会超过 bufio 默认的 64KB，表现成"这份 rollout 读不动"的假损坏。
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)

	var pendingReasoning string
	var open []openCall
	// callNames: call_id → 工具名。function_call_output 不带名字，只能从配对的
	// function_call 拿（见 applyResponseItem）。
	callNames := map[string]string{}

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var env rawEnvelope
		if err := json.Unmarshal(line, &env); err != nil {
			r.noteDegraded("unparsable line skipped")
			continue
		}

		switch env.Type {
		case "session_meta":
			var meta SessionMeta
			if err := json.Unmarshal(env.Payload, &meta); err == nil {
				r.DelegationID = meta.DelegationID
				r.ParentSessionID = meta.ParentSessionID
			}
		case "turn_context":
			var tc TurnContext
			if err := json.Unmarshal(env.Payload, &tc); err == nil && tc.Model != "" {
				r.Model = tc.Model
			}
		case "response_item":
			var item ResponseItem
			if err := json.Unmarshal(env.Payload, &item); err != nil {
				r.noteDegraded("unparsable response_item skipped")
				continue
			}
			r.applyResponseItem(&pendingReasoning, &open, callNames, item)
		case "event_msg":
			var ev EventMsg
			if err := json.Unmarshal(env.Payload, &ev); err == nil && ev.Type == "thinklink_entry" {
				r.ThinkLink = append(r.ThinkLink, ReplayThinkLinkEntry{
					Kind: ev.Kind, Content: ev.Text, Step: ev.StepNumber,
				})
			}
		case "compacted":
			var cp CompactedPayload
			if err := json.Unmarshal(env.Payload, &cp); err == nil {
				kept := make([]llm.Message, 0, 2)
				if len(r.Messages) > 0 && r.Messages[0].Role == llm.RoleSystem {
					kept = append(kept, r.Messages[0])
				}
				kept = append(kept, llm.Message{Role: llm.RoleUser, Content: cp.Summary})
				r.Messages = kept
				r.clearOpenCalls(&open)
				pendingReasoning = ""
				r.noteDegraded("history rebuilt from a compacted record; exact pre-stop messages are not reconstructible")
			}
		case RecordAgentCheckpoint:
			var cp AgentCheckpoint
			if err := json.Unmarshal(env.Payload, &cp); err == nil {
				// 最后一条为准：一次会话可以暂停-继续多轮，每轮各写一条。
				c := cp
				r.Checkpoint = &c
			}
		}

		if opts.MaxMessages > 0 && len(r.Messages) >= opts.MaxMessages {
			r.noteDegraded(fmt.Sprintf("message cap %d reached", opts.MaxMessages))
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("rollout replay: scan %s: %w", path, err)
	}

	for _, oc := range open {
		r.Dangling = append(r.Dangling, DanglingToolCall{ID: oc.id, Name: oc.name})
		r.danglingArgs[oc.id] = oc.args
	}

	if r.AgentName == "" {
		stem := strings.TrimSuffix(filepath.Base(path), rolloutFileExt)
		_, r.AgentName = parseRolloutFilename(stem)
	}

	r.Digest = DigestMessages(r.Messages)
	r.Materialized = materializedFrom(r.Messages)

	if r.Checkpoint == nil {
		r.Step = derivedStepFromMessages(r.Messages)
		r.noteDegraded("checkpoint missing (crash or pre-checkpoint rollout): step derived from message count")
	} else {
		r.Step = r.Checkpoint.Step
	}

	if opts.TokenBudget > 0 && opts.EstimateTokens != nil {
		total := 0
		for _, m := range r.Messages {
			total += opts.EstimateTokens(m.Content) + opts.EstimateTokens(m.Reasoning)
		}
		r.EstTokens = total
		if total > opts.TokenBudget {
			return r, fmt.Errorf("%w: %d tokens over budget %d", ErrReplayTooLarge, total, opts.TokenBudget)
		}
	}
	return r, nil
}

// applyResponseItem 把一条 response_item 折进重建中的消息序列。
func (r *Replay) applyResponseItem(pendingReasoning *string, open *[]openCall, callNames map[string]string, item ResponseItem) {
	switch item.Type {
	case "reasoning":
		*pendingReasoning = rolloutExtractText(item.Summary, "summary_text")

	case "message":
		content := rolloutExtractText(item.Content, textTypeForRole(item.Role))
		switch item.Role {
		case "assistant":
			r.Messages = append(r.Messages, llm.Message{
				Role:      llm.RoleAssistant,
				Content:   content,
				Reasoning: *pendingReasoning,
			})
			*pendingReasoning = ""
		case "system":
			r.Messages = append(r.Messages, llm.Message{Role: llm.RoleSystem, Content: content})
		case "user":
			r.Messages = append(r.Messages, llm.Message{Role: llm.RoleUser, Content: content})
		default:
			r.Messages = append(r.Messages, llm.Message{Role: llm.Role(item.Role), Content: content})
		}

	case "function_call":
		idx := len(r.Messages) - 1
		// 只有"上一条就是 assistant 且它还没有任何输出进来"时才合并；否则那是一次
		// 新的 assistant 回合（文件被截断或写出顺序与约定不符时宁可补一条，
		// 把调用丢掉等于把一次工具调用从模型的世界里抹掉）。
		if idx < 0 || r.Messages[idx].Role != llm.RoleAssistant || r.toolOutputArrived(idx) {
			r.Messages = append(r.Messages, llm.Message{Role: llm.RoleAssistant})
			idx = len(r.Messages) - 1
			r.noteDegraded("function_call without a preceding assistant message")
		}
		r.Messages[idx].ToolCalls = append(r.Messages[idx].ToolCalls, llm.ToolCall{
			ID:       item.CallID,
			Type:     "function",
			Function: llm.FunctionCall{Name: item.Name, Arguments: item.Arguments},
		})
		*open = append(*open, openCall{id: item.CallID, name: item.Name, args: item.Arguments})
		if item.Name != "" {
			callNames[item.CallID] = item.Name
		}

	case "function_call_output":
		*open = removeOpenCall(*open, item.CallID)
		text, ok := unwrapToolOutput(item.Output)
		if !ok {
			text = item.Output
			r.noteDegraded("tool output envelope unparsable for call " + item.CallID)
		}
		// function_call_output 落盘时不带工具名（写侧只有 call_id + output 信封），
		// 名字要从配对的 function_call 拿。不补这一步，报告与压缩里的"哪个工具产出的"
		// 就全空了 —— 又是一处"能跑但信息没了"的静默丢失。
		name := item.Name
		if name == "" {
			name = callNames[item.CallID]
		}
		r.Messages = append(r.Messages, llm.Message{
			Role:       llm.RoleTool,
			Content:    text,
			ToolCallID: item.CallID,
			ToolName:   name,
		})
	}
}

// toolOutputArrived 判断下标 idx 之后是否已经出现过工具输出 —— 出现过就说明那个
// assistant 回合已经闭合，后来的 function_call 属于新的一条消息。
func (r *Replay) toolOutputArrived(idx int) bool {
	// Messages 里只留 message/user/assistant/tool 四种角色，assistant 之后紧跟着的
	// 若还是 assistant，说明前一条已经闭合（工具输出会以 tool 角色插在中间）。
	// 这里用简单可靠的判据：idx 这条已经有 ToolCalls 且下一条不是 tool ⇒ 已闭合。
	if idx+1 >= len(r.Messages) {
		return false
	}
	return len(r.Messages[idx].ToolCalls) > 0 && r.Messages[idx+1].Role != llm.RoleTool
}

// clearOpenCalls 在压缩之后清空在飞调用：压缩改写的是消息历史，
// 那些调用本身已经不存在了，留着会伪造出"停在某个工具上"的悬空状态。
func (r *Replay) clearOpenCalls(open *[]openCall) { *open = nil }

func removeOpenCall(open []openCall, id string) []openCall {
	out := open[:0]
	for _, oc := range open {
		if oc.id != id {
			out = append(out, oc)
		}
	}
	return out
}

func textTypeForRole(role string) string {
	if role == "assistant" {
		return "output_text"
	}
	return "input_text"
}

// unwrapToolOutput 解出 function_call_output 的信封（LLMMessageToResponseItems 里
// 包了一层 {"output":…,"metadata":…}）。
func unwrapToolOutput(raw string) (string, bool) {
	if raw == "" {
		return "", true
	}
	var obj struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return "", false
	}
	return obj.Output, true
}

// materializedFrom 派生"已经取回过哪些冷工具"（§5.3：这是可派生态，不进检查点）。
// 判据是 load_tool 的调用**且**它的输出不是错误 —— 只调用了却没成功取回的工具
// 在目录里仍然是 deferred，恢复后模型看不到它的 schema，重放时也就不能假定它存在。
func materializedFrom(msgs []llm.Message) []string {
	var names []string
	seen := map[string]bool{}
	outputs := map[string]string{}
	for _, m := range msgs {
		if m.Role == llm.RoleTool {
			outputs[m.ToolCallID] = m.Content
		}
	}
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			if tc.Function.Name != "load_tool" {
				continue
			}
			out, ok := outputs[tc.ID]
			if !ok || strings.HasPrefix(out, "Error:") {
				continue
			}
			for _, name := range loadToolNames(tc.Function.Arguments) {
				if !seen[name] {
					seen[name] = true
					names = append(names, name)
				}
			}
		}
	}
	return names
}

// loadToolNames 从 load_tool 的参数里取 names 数组。解析失败返回空 ——
// 少恢复一个冷工具的代价是模型自己会再 load 一次，猜错名字则可能启用一个没启用的工具。
func loadToolNames(argsJSON string) []string {
	if strings.TrimSpace(argsJSON) == "" {
		return nil
	}
	var args struct {
		Names []string `json:"names"`
		Name  string   `json:"name"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return nil
	}
	if len(args.Names) > 0 {
		return args.Names
	}
	if args.Name != "" {
		return []string{args.Name}
	}
	return nil
}

// derivedStepFromMessages 在没有检查点时（崩溃、或存量文件）按 assistant 消息数推步数。
// 这是**下界**：一轮 assistant 消息可能只发了工具调用没算一步，也可能一步发了多条。
// 用它当起点最坏情况是重跑一步，绝不会跳过一步。
func derivedStepFromMessages(msgs []llm.Message) int {
	n := 0
	for _, m := range msgs {
		if m.Role == llm.RoleAssistant {
			n++
		}
	}
	return n
}
