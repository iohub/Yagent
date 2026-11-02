package memory

import (
	"encoding/json"
	"fmt"
	"time"
)

type RolloutEnvelope struct {
	Timestamp string      `json:"timestamp"`
	Type      string      `json:"type"`
	Payload   interface{} `json:"payload"`
}

type SessionMeta struct {
	ID               string  `json:"id,omitempty"`
	SessionID        string  `json:"session_id,omitempty"`
	TaskID           string  `json:"task_id,omitempty"`
	BoardID          string  `json:"board_id,omitempty"`
	DelegationID     string  `json:"delegation_id,omitempty"`
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

type GitInfo struct {
	SHA       string `json:"sha,omitempty"`
	Branch    string `json:"branch,omitempty"`
	OriginURL string `json:"origin_url,omitempty"`
}

func (g GitInfo) Empty() bool {
	return g.SHA == "" && g.Branch == "" && g.OriginURL == ""
}

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

type MessageContentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

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
			Content          interface{}          `json:"content"`
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

	MaxSteps     int    `json:"max_steps,omitempty"`
	StopReason   string `json:"stop_reason,omitempty"`
	HasDelegated bool   `json:"has_delegated,omitempty"`

	Kind string `json:"kind,omitempty"`
}

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

const (
	CompactionKindRule      = "rule"
	CompactionKindEmergency = "emergency"
	CompactionKindUltimate  = "ultimate"
)

const (
	StopKindPaused             = "paused"
	StopKindCancelled          = "cancelled"
	StopKindIdleTimeout        = "idle_timeout"
	StopKindTotalTimeout       = "total_timeout"
	StopKindMaxSteps           = "max_steps"
	StopKindLLMError           = "llm_error"
	StopKindCircuitBreakerOpen = "circuit_breaker_open"
	StopKindPanic              = "panic"
	StopKindPlainText          = "plain_text"
	StopKindAgentExit          = "agent_exit"
	StopKindUnaccounted        = "unaccounted"
)

const RecordAgentCheckpoint = "agent_checkpoint"

const CheckpointSchemaVersion = 1

type WorkspaceState struct {
	GitSHA              string   `json:"git_sha,omitempty"`
	GitBranch           string   `json:"git_branch,omitempty"`
	GitOrigin           string   `json:"git_origin,omitempty"`
	Dirty               bool     `json:"dirty"`
	DirtyFiles          []string `json:"dirty_files,omitempty"`
	DirtyFilesTruncated bool     `json:"dirty_files_truncated,omitempty"`
	StatusError         string   `json:"status_error,omitempty"`
	Cwd                 string   `json:"cwd,omitempty"`
}

type AgentCheckpoint struct {
	SchemaVersion int            `json:"schema_version"`
	StopKind      string         `json:"stop_kind"`
	AgentName     string         `json:"agent_name"`
	DelegationID  string         `json:"delegation_id,omitempty"`
	Step          int            `json:"step"`
	MaxSteps      int            `json:"max_steps"`
	MessageCount  int            `json:"message_count"`
	Digest        string         `json:"digest"`
	Model         string         `json:"model,omitempty"`
	Tier          string         `json:"tier,omitempty"`
	Workspace     WorkspaceState `json:"workspace"`
	Nested        []string       `json:"nested_delegation_ids,omitempty"`
	CreatedAt     string         `json:"created_at"`
}

type CompactedPayload struct {
	Summary      string `json:"summary,omitempty"`
	TokensBefore int    `json:"tokens_before,omitempty"`
	TokensAfter  int    `json:"tokens_after,omitempty"`
	Kind         string `json:"kind,omitempty"`
	SavedTokens  int    `json:"saved_tokens,omitempty"`
}

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
