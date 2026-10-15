package memory

import (
	"encoding/json"
	"fmt"
	"time"
)

// ─── RolloutEnvelope — 顶层包装结构 ───

// RolloutEnvelope 是 Codex Rollout JSONL 每行的顶层结构
type RolloutEnvelope struct {
	Timestamp string      `json:"timestamp"`
	Type      string      `json:"type"`
	Payload   interface{} `json:"payload"`
}

// ─── SessionMeta — 会话元数据 payload ───

// SessionMeta 会话元数据
type SessionMeta struct {
	ID        string `json:"id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	TaskID    string `json:"task_id,omitempty"`
	// BoardID 是这次会话用的便签板。board 作用域提到 session 级之后，
	// task_id 已经不足以在 /resume 时找回那块板（一个板对应多个 task），
	// 所以要把板本身记下来。存量 rollout 没有这个字段 ⇒ 读出来是空串。
	BoardID string `json:"board_id,omitempty"`
	// DelegationID 是父 Director 那一次 `delegate_*` 的 tool_call_id。它是父子
	// rollout 之间唯一的关联坐标：恢复时靠它从"Director 停在哪个 call 上"走到
	// "子 Agent 的那份文件"。Director 自己的文件里为空。
	DelegationID string `json:"delegation_id,omitempty"`
	// ParentSessionID 是父 rollout 的 session_id，让父子可以双向定位
	// （只有 DelegationID 时，找到 call 还得先猜是哪份 Director 会话）。
	ParentSessionID  string  `json:"parent_session_id,omitempty"`
	Cwd              string  `json:"cwd,omitempty"`
	CliVersion       string  `json:"cli_version,omitempty"`
	Originator       string  `json:"originator,omitempty"`
	ModelProvider    string  `json:"model_provider,omitempty"`
	Source           string  `json:"source,omitempty"`
	BaseInstructions string  `json:"base_instructions,omitempty"`
	ContextWindow    int     `json:"context_window,omitempty"`
	HistoryMode      string  `json:"history_mode,omitempty"`
	Git              GitInfo `json:"git,omitempty"`
}

// GitInfo git 元数据
type GitInfo struct {
	SHA       string `json:"sha,omitempty"`
	Branch    string `json:"branch,omitempty"`
	OriginURL string `json:"origin_url,omitempty"`
}

// Empty 报告三项是否全部缺失。WriteSessionMeta 用它决定要不要自己去问 git ——
// 显式传了值的调用点永远优先。
func (g GitInfo) Empty() bool {
	return g.SHA == "" && g.Branch == "" && g.OriginURL == ""
}

// ─── TurnContext — 回合上下文 payload ───

// TurnContext 回合上下文
type TurnContext struct {
	TurnID            string      `json:"turn_id,omitempty"`
	Cwd               string      `json:"cwd,omitempty"`
	Model             string      `json:"model,omitempty"`
	Effort            string      `json:"effort,omitempty"`
	ApprovalPolicy    string      `json:"approval_policy,omitempty"`
	SandboxPolicy     interface{} `json:"sandbox_policy,omitempty"`
	Summary           string      `json:"summary,omitempty"`
	WorkspaceRoots    []string    `json:"workspace_roots,omitempty"`
	CollaborationMode string      `json:"collaboration_mode,omitempty"`
}

// ─── MessageContentItem — 消息内容块 ───

// MessageContentItem 消息内容块（content block）
type MessageContentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ─── ResponseItem — response_item 的 payload ───

// ResponseItem 模型响应项，使用 Type 字段做联合体
type ResponseItem struct {
	Type             string               `json:"type"`
	Role             string               `json:"role,omitempty"`
	ID               string               `json:"id,omitempty"`
	Content          []MessageContentItem `json:"content,omitempty"`
	Summary          []MessageContentItem `json:"summary,omitempty"`
	EncryptedContent string               `json:"encrypted_content,omitempty"`
	CallID           string               `json:"call_id,omitempty"`
	Name             string               `json:"name,omitempty"`
	Namespace        string               `json:"namespace,omitempty"`
	Arguments        string               `json:"arguments,omitempty"`
	Output           string               `json:"output,omitempty"`
}

// MarshalJSON 根据 Type 字段只输出对应子类型的字段
func (r ResponseItem) MarshalJSON() ([]byte, error) {
	switch r.Type {
	case "message":
		type msgAlias struct {
			Type    string               `json:"type"`
			Role    string               `json:"role,omitempty"`
			ID      string               `json:"id,omitempty"`
			Content []MessageContentItem `json:"content,omitempty"`
		}
		return json.Marshal(msgAlias{
			Type:    r.Type,
			Role:    r.Role,
			ID:      r.ID,
			Content: r.Content,
		})
	case "reasoning":
		type reasonAlias struct {
			Type             string               `json:"type"`
			ID               string               `json:"id,omitempty"`
			Summary          []MessageContentItem `json:"summary,omitempty"`
			Content          interface{}          `json:"content"` // 显式输出 null
			EncryptedContent string               `json:"encrypted_content,omitempty"`
		}
		return json.Marshal(reasonAlias{
			Type:             r.Type,
			ID:               r.ID,
			Summary:          r.Summary,
			Content:          nil,
			EncryptedContent: r.EncryptedContent,
		})
	case "function_call":
		type fcAlias struct {
			Type      string `json:"type"`
			ID        string `json:"id,omitempty"`
			CallID    string `json:"call_id,omitempty"`
			Name      string `json:"name,omitempty"`
			Namespace string `json:"namespace,omitempty"`
			Arguments string `json:"arguments,omitempty"`
		}
		return json.Marshal(fcAlias{
			Type:      r.Type,
			ID:        r.ID,
			CallID:    r.CallID,
			Name:      r.Name,
			Namespace: r.Namespace,
			Arguments: r.Arguments,
		})
	case "function_call_output":
		type fcoAlias struct {
			Type   string `json:"type"`
			CallID string `json:"call_id,omitempty"`
			Output string `json:"output,omitempty"`
		}
		return json.Marshal(fcoAlias{
			Type:   r.Type,
			CallID: r.CallID,
			Output: r.Output,
		})
	default:
		// 未知类型，输出所有字段
		return json.Marshal(map[string]interface{}{
			"type":              r.Type,
			"role":              r.Role,
			"id":                r.ID,
			"content":           r.Content,
			"summary":           r.Summary,
			"encrypted_content": r.EncryptedContent,
			"call_id":           r.CallID,
			"name":              r.Name,
			"namespace":         r.Namespace,
			"arguments":         r.Arguments,
			"output":            r.Output,
		})
	}
}

// ─── EventMsg — event_msg 的 payload ───

// EventMsg 运行时事件，使用 Type 字段做联合体
type EventMsg struct {
	Type       string      `json:"type"`
	Text       string      `json:"text,omitempty"`
	Message    string      `json:"message,omitempty"`
	Reason     string      `json:"reason,omitempty"`
	Info       interface{} `json:"info,omitempty"`
	StepNumber int         `json:"step_number,omitempty"`
	ToolName   string      `json:"tool_name,omitempty"`
	ToolInput  interface{} `json:"tool_input,omitempty"`
	Success    bool        `json:"success,omitempty"`
	AgentName  string      `json:"agent_name,omitempty"`
	Summary    string      `json:"summary,omitempty"`

	// ── 终止事件（task_complete / turn_aborted）携带的断点状态 ──
	// 恢复侧据此判断会话为何结束、停在第几步，而不是只能靠"有没有终止事件"猜。
	// StepNumber 复用上面的字段（终止事件里它表示已执行步数）。
	MaxSteps     int    `json:"max_steps,omitempty"`
	StopReason   string `json:"stop_reason,omitempty"`
	HasDelegated bool   `json:"has_delegated,omitempty"`

	// Kind thinklink_entry 事件的条目类别（"user_input" / "thought_plan"），
	// 与 thinklink.Kind.String() 对齐；条目正文走 Text。
	Kind string `json:"kind,omitempty"`
}

// MarshalJSON 根据 Type 字段只输出对应字段
func (e EventMsg) MarshalJSON() ([]byte, error) {
	switch e.Type {
	case "token_count":
		type tcAlias struct {
			Type string      `json:"type"`
			Info interface{} `json:"info,omitempty"`
		}
		return json.Marshal(tcAlias{Type: e.Type, Info: e.Info})
	case "agent_reasoning":
		type arAlias struct {
			Type string `json:"type"`
			Text string `json:"text,omitempty"`
		}
		return json.Marshal(arAlias{Type: e.Type, Text: e.Text})
	case "agent_message":
		type amAlias struct {
			Type    string `json:"type"`
			Message string `json:"message,omitempty"`
		}
		return json.Marshal(amAlias{Type: e.Type, Message: e.Message})
	case "user_message":
		type umAlias struct {
			Type string `json:"type"`
		}
		return json.Marshal(umAlias{Type: e.Type})
	case "task_started":
		type tsAlias struct {
			Type string `json:"type"`
		}
		return json.Marshal(tsAlias{Type: e.Type})
	case "task_complete":
		type tcmAlias struct {
			Type         string `json:"type"`
			StepNumber   int    `json:"step_number,omitempty"`
			MaxSteps     int    `json:"max_steps,omitempty"`
			StopReason   string `json:"stop_reason,omitempty"`
			HasDelegated bool   `json:"has_delegated,omitempty"`
		}
		return json.Marshal(tcmAlias{
			Type:         e.Type,
			StepNumber:   e.StepNumber,
			MaxSteps:     e.MaxSteps,
			StopReason:   e.StopReason,
			HasDelegated: e.HasDelegated,
		})
	case "turn_aborted":
		type taAlias struct {
			Type         string `json:"type"`
			Reason       string `json:"reason,omitempty"`
			StepNumber   int    `json:"step_number,omitempty"`
			MaxSteps     int    `json:"max_steps,omitempty"`
			StopReason   string `json:"stop_reason,omitempty"`
			HasDelegated bool   `json:"has_delegated,omitempty"`
		}
		return json.Marshal(taAlias{
			Type:         e.Type,
			Reason:       e.Reason,
			StepNumber:   e.StepNumber,
			MaxSteps:     e.MaxSteps,
			StopReason:   e.StopReason,
			HasDelegated: e.HasDelegated,
		})
	case "context_compacted":
		type ccAlias struct {
			Type    string `json:"type"`
			Summary string `json:"summary,omitempty"`
		}
		return json.Marshal(ccAlias{Type: e.Type, Summary: e.Summary})
	case "thinklink_entry":
		// 正文走 "text" 键：EventMsg 无自定义 UnmarshalJSON，标准反序列化按字段
		// tag 对齐（Text ↔ "text"），读侧无需为该事件写特例解析。
		type tleAlias struct {
			Type       string `json:"type"`
			Kind       string `json:"kind,omitempty"`
			Text       string `json:"text,omitempty"`
			StepNumber int    `json:"step_number,omitempty"`
		}
		return json.Marshal(tleAlias{
			Type:       e.Type,
			Kind:       e.Kind,
			Text:       e.Text,
			StepNumber: e.StepNumber,
		})
	case "sub_agent_activity":
		type saaAlias struct {
			Type       string      `json:"type"`
			StepNumber int         `json:"step_number,omitempty"`
			ToolName   string      `json:"tool_name,omitempty"`
			ToolInput  interface{} `json:"tool_input,omitempty"`
			Success    bool        `json:"success,omitempty"`
			AgentName  string      `json:"agent_name,omitempty"`
		}
		return json.Marshal(saaAlias{
			Type:       e.Type,
			StepNumber: e.StepNumber,
			ToolName:   e.ToolName,
			ToolInput:  e.ToolInput,
			Success:    e.Success,
			AgentName:  e.AgentName,
		})
	default:
		// 未知类型，输出所有字段
		return json.Marshal(map[string]interface{}{
			"type":          e.Type,
			"text":          e.Text,
			"message":       e.Message,
			"reason":        e.Reason,
			"info":          e.Info,
			"step_number":   e.StepNumber,
			"tool_name":     e.ToolName,
			"tool_input":    e.ToolInput,
			"success":       e.Success,
			"agent_name":    e.AgentName,
			"summary":       e.Summary,
			"max_steps":     e.MaxSteps,
			"stop_reason":   e.StopReason,
			"has_delegated": e.HasDelegated,
		})
	}
}

// ─── CompactedPayload — 压缩快照 payload ───

// ─── CompactedPayload — 压缩快照 payload ───

// 压缩档位名。写入端（executor / director planner）与读取端（TUI 的 ⧉ 段）共用这一份
// 词表 —— 写成裸字符串的话，一个 typo 就会让 /resume 静默丢掉一整档统计。
const (
	CompactionKindRule      = "rule"
	CompactionKindEmergency = "emergency"
	CompactionKindUltimate  = "ultimate"
)

// 终止词表：event_msg 的 stop_reason 取值（task_complete 与 turn_aborted 都带它）。
//
// 写入端有两处——executor.go 的子 Agent 循环与 director/planner_run.go 的 Director 循环；
// 读取端有 TUI 的断点面板和恢复候选扫描。这三处分属三个包，而 director 子包不得
// import agents（scripts/check_no_agents_import.sh），所以词表只能放在共同依赖的 memory。
// 与上面的 CompactionKind* 同一个理由：写成裸字符串的话，一个 typo 只会让恢复侧
// 少认一类停止原因，而没有任何地方会报错。
const (
	// StopKindPaused 是执行停在暂停闸门（A 层）上、随后被取消的那一轮。
	// 它与 cancelled 的唯一但关键的差别是**停在哪**：闸门只在 step 边界生效，
	// 所以到这一步的工具批次已整体提交完，重放不会重复半截副作用；
	// 而 cancelled 可能停在某个在飞的 run_bash 中间。
	// 只有这两类应当被当作"可以原样继续"。
	StopKindPaused = "paused"
	// StopKindCancelled 是没有别的解释的取消：用户按停止、任务被丢弃、上层 ctx 关掉。
	StopKindCancelled = "cancelled"
	// StopKindIdleTimeout / StopKindTotalTimeout 来自 activity 的两个上限。
	// 刻意不与 cancelled 合并：超时的那一步可能已经把 "Error: context canceled"
	// 记进了 rollout，原样重放会重复执行副作用。
	StopKindIdleTimeout  = "idle_timeout"
	StopKindTotalTimeout = "total_timeout"
	StopKindMaxSteps     = "max_steps"
	// StopKindLLMError 涵盖重试耗尽；StopKindCircuitBreakerOpen 是 Director 独有的
	// 更早出口（连续失败到阈值直接拦截调用）。两者都不能重放，但保留区分是因为
	// 熔断器意味着 provider 侧出了问题，这个信息对排查有用。
	StopKindLLMError           = "llm_error"
	StopKindCircuitBreakerOpen = "circuit_breaker_open"
	StopKindPanic              = "panic"
	StopKindPlainText          = "plain_text"
	StopKindAgentExit          = "agent_exit"
	// StopKindUnaccounted 表示"这一轮结束了，但没有任何出口认领原因"。
	// 存在的意义是让新增的 return 路径立刻可诊断，而不是静默留下空的 stop_reason。
	StopKindUnaccounted = "unaccounted"
)

// ─── AgentCheckpoint — 停止时的坐标（docs/pause-resume-design.md §5.3）───

// RecordAgentCheckpoint 是检查点记录的顶层类型名（与 session_meta / turn_aborted /
// compacted 同级）。取代已删除的 world_state：那份记录想表达的"世界停在什么状态"，
// 现在由"会话文件本身 + 这一条坐标"合起来回答。
const RecordAgentCheckpoint = "agent_checkpoint"

// CheckpointSchemaVersion 是 AgentCheckpoint 的当前 schema 版本。
const CheckpointSchemaVersion = 1

// WorkspaceState 是停止那一刻的工作区事实。
//
// 只记 sha 不够：未提交的改动同样会让子 Agent 报告里的"我已经改了 X"变成假的。
// StatusError 非空表示**探测失败**，与 Dirty=false 是两件不同的事 —— 混为一谈就是
// §2 R6 那个 bug 的形状（git 命令根本没跑起来，却被记成"工作区干净"）。
type WorkspaceState struct {
	GitSHA              string   `json:"git_sha,omitempty"`
	GitBranch           string   `json:"git_branch,omitempty"`
	GitOrigin           string   `json:"git_origin,omitempty"`
	Dirty               bool     `json:"dirty"`
	DirtyFiles          []string `json:"dirty_files,omitempty"` // git status --porcelain，上限 200 条
	DirtyFilesTruncated bool     `json:"dirty_files_truncated,omitempty"`
	StatusError         string   `json:"status_error,omitempty"`
	Cwd                 string   `json:"cwd,omitempty"`
}

// AgentCheckpoint 一条"这一轮停在这里"的坐标。
//
// 刻意不存三样东西，因为它们都能从同一份 rollout 派生，存了就会与代码漂移：
// 消息正文（response_item 已经是同步全量）、已取回的冷工具（扫 load_tool 的
// 成功配对）、thinklink T&P 块（从 assistant 正文重新提取）。
//
// Digest 是防漂移的那一环：重放侧按同一套规则重算，对不上就说明这份文件被截断、
// 损坏或被 GC 动过 —— 那时必须降级到交接报告，而不是把一个残缺的上下文喂回模型。
type AgentCheckpoint struct {
	SchemaVersion int            `json:"schema_version"`
	StopKind      string         `json:"stop_kind"` // StopKind* 词表
	AgentName     string         `json:"agent_name"`
	DelegationID  string         `json:"delegation_id,omitempty"`
	Step          int            `json:"step"`      // 已完成步数 = 重放起点
	MaxSteps      int            `json:"max_steps"` // 0 = 这一层没有步数上限（Director 之外都有）
	MessageCount  int            `json:"message_count"`
	Digest        string         `json:"digest"`
	Model         string         `json:"model,omitempty"`
	Tier          string         `json:"tier,omitempty"`
	Workspace     WorkspaceState `json:"workspace"`
	Nested        []string       `json:"nested_delegation_ids,omitempty"`
	CreatedAt     string         `json:"created_at"`
}

// CompactedPayload 上下文压缩后的快照。
//
// Kind/SavedTokens 是给 /resume 重建 ⧉ 压缩段用的（TUI 侧的三档统计否则只能靠
// Summary 前缀猜）。老 rollout 里这两个字段是空的，读取端负责回落。
type CompactedPayload struct {
	Summary      string `json:"summary,omitempty"`
	TokensBefore int    `json:"tokens_before,omitempty"`
	TokensAfter  int    `json:"tokens_after,omitempty"`
	Kind         string `json:"kind,omitempty"`         // CompactionKind*
	SavedTokens  int    `json:"saved_tokens,omitempty"` // 与事件的 saved_tokens 同源
}

// ─── 辅助函数 ───

// nowISO8601 返回当前 UTC 时间的 ISO 8601 格式字符串
func nowISO8601() string {
	return fmt.Sprintf("%d-%02d-%02dT%02d:%02d:%02d.%03dZ",
		time.Now().UTC().Year(),
		time.Now().UTC().Month(),
		time.Now().UTC().Day(),
		time.Now().UTC().Hour(),
		time.Now().UTC().Minute(),
		time.Now().UTC().Second(),
		time.Now().UTC().Nanosecond()/1e6,
	)
}
