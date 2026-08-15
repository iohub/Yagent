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
	ID             string     `json:"id,omitempty"`
	SessionID      string     `json:"session_id,omitempty"`
	Cwd            string     `json:"cwd,omitempty"`
	CliVersion     string     `json:"cli_version,omitempty"`
	Originator     string     `json:"originator,omitempty"`
	ModelProvider  string     `json:"model_provider,omitempty"`
	Source         string     `json:"source,omitempty"`
	BaseInstructions string   `json:"base_instructions,omitempty"`
	ContextWindow  int        `json:"context_window,omitempty"`
	HistoryMode    string     `json:"history_mode,omitempty"`
	Git            GitInfo    `json:"git,omitempty"`
}

// GitInfo git 元数据
type GitInfo struct {
	SHA       string `json:"sha,omitempty"`
	Branch    string `json:"branch,omitempty"`
	OriginURL string `json:"origin_url,omitempty"`
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
	Type                string                  `json:"type"`
	Role                string                  `json:"role,omitempty"`
	ID                  string                  `json:"id,omitempty"`
	Content             []MessageContentItem    `json:"content,omitempty"`
	Summary             []MessageContentItem    `json:"summary,omitempty"`
	EncryptedContent    string                  `json:"encrypted_content,omitempty"`
	CallID              string                  `json:"call_id,omitempty"`
	Name                string                  `json:"name,omitempty"`
	Namespace           string                  `json:"namespace,omitempty"`
	Arguments           string                  `json:"arguments,omitempty"`
	Output              string                  `json:"output,omitempty"`
}

// MarshalJSON 根据 Type 字段只输出对应子类型的字段
func (r ResponseItem) MarshalJSON() ([]byte, error) {
	switch r.Type {
	case "message":
		type msgAlias struct {
			Type    string                  `json:"type"`
			Role    string                  `json:"role,omitempty"`
			ID      string                  `json:"id,omitempty"`
			Content []MessageContentItem    `json:"content,omitempty"`
		}
		return json.Marshal(msgAlias{
			Type:    r.Type,
			Role:    r.Role,
			ID:      r.ID,
			Content: r.Content,
		})
	case "reasoning":
		type reasonAlias struct {
			Type               string                  `json:"type"`
			ID                 string                  `json:"id,omitempty"`
			Summary            []MessageContentItem    `json:"summary,omitempty"`
			Content            interface{}             `json:"content"` // 显式输出 null
			EncryptedContent   string                  `json:"encrypted_content,omitempty"`
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
			"type":               r.Type,
			"role":               r.Role,
			"id":                 r.ID,
			"content":            r.Content,
			"summary":            r.Summary,
			"encrypted_content":  r.EncryptedContent,
			"call_id":            r.CallID,
			"name":               r.Name,
			"namespace":          r.Namespace,
			"arguments":          r.Arguments,
			"output":             r.Output,
		})
	}
}

// ─── EventMsg — event_msg 的 payload ───

// EventMsg 运行时事件，使用 Type 字段做联合体
type EventMsg struct {
	Type        string      `json:"type"`
	Text        string      `json:"text,omitempty"`
	Message     string      `json:"message,omitempty"`
	Reason      string      `json:"reason,omitempty"`
	Info        interface{} `json:"info,omitempty"`
	StepNumber  int         `json:"step_number,omitempty"`
	ToolName    string      `json:"tool_name,omitempty"`
	ToolInput   interface{} `json:"tool_input,omitempty"`
	Success     bool        `json:"success,omitempty"`
	AgentName   string      `json:"agent_name,omitempty"`
	Summary     string      `json:"summary,omitempty"`
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
			Type string `json:"type"`
		}
		return json.Marshal(tcmAlias{Type: e.Type})
	case "turn_aborted":
		type taAlias struct {
			Type   string `json:"type"`
			Reason string `json:"reason,omitempty"`
		}
		return json.Marshal(taAlias{Type: e.Type, Reason: e.Reason})
	case "context_compacted":
		type ccAlias struct {
			Type    string `json:"type"`
			Summary string `json:"summary,omitempty"`
		}
		return json.Marshal(ccAlias{Type: e.Type, Summary: e.Summary})
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
			"type":       e.Type,
			"text":       e.Text,
			"message":    e.Message,
			"reason":     e.Reason,
			"info":       e.Info,
			"step_number": e.StepNumber,
			"tool_name":  e.ToolName,
			"tool_input": e.ToolInput,
			"success":    e.Success,
			"agent_name": e.AgentName,
			"summary":    e.Summary,
		})
	}
}

// ─── InterAgentCommunicationMetadata — 多 Agent 通信元数据 payload ───

// InterAgentCommunicationMetadata 多 Agent 通信元数据
type InterAgentCommunicationMetadata struct {
	ParentAgent      string `json:"parent_agent,omitempty"`
	ChildAgent       string `json:"child_agent,omitempty"`
	ParentToolCallID string `json:"parent_tool_call_id,omitempty"`
	Direction        string `json:"direction,omitempty"`
	Summary          string `json:"summary,omitempty"`
	Timestamp        string `json:"timestamp,omitempty"`
}

// ─── CompactedPayload — 压缩快照 payload ───

// CompactedPayload 上下文压缩后的快照
type CompactedPayload struct {
	Summary      string `json:"summary,omitempty"`
	TokensBefore int    `json:"tokens_before,omitempty"`
	TokensAfter  int    `json:"tokens_after,omitempty"`
}

// ─── WorldStatePayload — 世界状态 payload ───

// WorldStatePayload 世界状态快照
type WorldStatePayload struct {
	Files      []string `json:"files,omitempty"`
	GitBranch  string   `json:"git_branch,omitempty"`
	GitStatus  string   `json:"git_status,omitempty"`
	Timestamp  string   `json:"timestamp,omitempty"`
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
