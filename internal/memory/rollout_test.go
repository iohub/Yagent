package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"

	"yagent/internal/llm"
)

func TestResponseItemMarshal(t *testing.T) {
	tests := []struct {
		name        string
		item        ResponseItem
		contains    []string
		notContains []string
	}{
		{
			name: "message user",
			item: ResponseItem{
				Type:    "message",
				Role:    "user",
				ID:      "msg-1",
				Content: []MessageContentItem{{Type: "input_text", Text: "hello"}},
			},
			contains: []string{
				`"type":"message"`,
				`"role":"user"`,
				`"id":"msg-1"`,
				`"content":[{"type":"input_text","text":"hello"}]`,
			},
			notContains: []string{
				`"call_id"`, `"name"`, `"arguments"`, `"output"`,
				`"summary"`, `"encrypted_content"`,
			},
		},
		{
			name: "message assistant",
			item: ResponseItem{
				Type:    "message",
				Role:    "assistant",
				ID:      "msg-2",
				Content: []MessageContentItem{{Type: "output_text", Text: "I'll help you."}},
			},
			contains: []string{
				`"type":"message"`,
				`"role":"assistant"`,
				`"content":[{"type":"output_text","text":"I'll help you."}]`,
			},
			notContains: []string{
				`"call_id"`, `"name"`, `"arguments"`, `"output"`,
			},
		},
		{
			name: "function_call",
			item: ResponseItem{
				Type:      "function_call",
				ID:        "call-abc123",
				CallID:    "toolcall-xyz",
				Name:      "shell",
				Namespace: "default",
				Arguments: `{"command":"ls -la"}`,
			},
			contains: []string{
				`"type":"function_call"`,
				`"id":"call-abc123"`,
				`"call_id":"toolcall-xyz"`,
				`"name":"shell"`,
				`"namespace":"default"`,
				`"arguments":"{\"command\":\"ls -la\"}"`,
			},
			notContains: []string{
				`"role"`, `"content"`, `"output"`, `"summary"`,
			},
		},
		{
			name: "function_call_output",
			item: ResponseItem{
				Type:   "function_call_output",
				CallID: "toolcall-xyz",
				Output: `{"output":"file1\nfile2","metadata":{}}`,
			},
			contains: []string{
				`"type":"function_call_output"`,
				`"call_id":"toolcall-xyz"`,
				`"output":"{\"output\":\"file1\\nfile2\",\"metadata\":{}}"`,
			},
			notContains: []string{
				`"role"`, `"content"`, `"name"`, `"arguments"`,
			},
		},
		{
			name: "reasoning",
			item: ResponseItem{
				Type:             "reasoning",
				ID:               "reason-1",
				Summary:          []MessageContentItem{{Type: "summary_text", Text: "thinking..."}},
				EncryptedContent: "encrypted-data",
			},
			contains: []string{
				`"type":"reasoning"`,
				`"id":"reason-1"`,
				`"summary":[{"type":"summary_text","text":"thinking..."}]`,
				`"encrypted_content":"encrypted-data"`,
			},
			notContains: []string{
				`"role"`, `"output"`,
			},
		},
		{
			name: "unknown type",
			item: ResponseItem{
				Type:    "custom_type",
				Role:    "custom",
				Content: []MessageContentItem{{Type: "text", Text: "hello"}},
			},
			contains: []string{
				`"type":"custom_type"`,
				`"role":"custom"`,
				`"content"`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := json.Marshal(tt.item)
			if err != nil {
				t.Fatalf("MarshalJSON error: %v", err)
			}
			got := string(b)
			for _, want := range tt.contains {
				if !strings.Contains(got, want) {
					t.Errorf("JSON missing expected substring %q.\nGot: %s", want, got)
				}
			}
			for _, dontWant := range tt.notContains {
				if strings.Contains(got, dontWant) {
					t.Errorf("JSON should not contain %q.\nGot: %s", dontWant, got)
				}
			}
		})
	}
}

func TestEventMsgMarshal(t *testing.T) {
	tests := []struct {
		name        string
		event       EventMsg
		contains    []string
		notContains []string
	}{
		{
			name:  "task_started",
			event: EventMsg{Type: "task_started"},
			contains: []string{
				`{"type":"task_started"}`,
			},
		},
		{
			name:  "task_complete",
			event: EventMsg{Type: "task_complete"},
			contains: []string{
				`{"type":"task_complete"}`,
			},
		},
		{
			name:  "turn_aborted",
			event: EventMsg{Type: "turn_aborted", Reason: "user cancelled"},
			contains: []string{
				`"type":"turn_aborted"`,
				`"reason":"user cancelled"`,
			},
		},
		{
			name: "sub_agent_activity",
			event: EventMsg{
				Type:       "sub_agent_activity",
				StepNumber: 1,
				ToolName:   "shell",
				Success:    true,
			},
			contains: []string{
				`"type":"sub_agent_activity"`,
				`"step_number":1`,
				`"tool_name":"shell"`,
				`"success":true`,
			},
		},
		{
			name: "token_count",
			event: EventMsg{
				Type: "token_count",
				Info: map[string]interface{}{"prompt_tokens": 100, "completion_tokens": 50},
			},
			contains: []string{
				`"type":"token_count"`,
				`"info"`,
			},
		},
		{
			name: "agent_reasoning",
			event: EventMsg{
				Type: "agent_reasoning",
				Text: "Let me think about this...",
			},
			contains: []string{
				`"type":"agent_reasoning"`,
				`"text":"Let me think about this..."`,
			},
		},
		{
			name: "agent_message",
			event: EventMsg{
				Type:    "agent_message",
				Message: "Processing request...",
			},
			contains: []string{
				`"type":"agent_message"`,
				`"message":"Processing request..."`,
			},
		},
		{
			name:  "user_message",
			event: EventMsg{Type: "user_message"},
			contains: []string{
				`{"type":"user_message"}`,
			},
		},
		{
			name: "context_compacted",
			event: EventMsg{
				Type:    "context_compacted",
				Summary: "Compressed 50 messages",
			},
			contains: []string{
				`"type":"context_compacted"`,
				`"summary":"Compressed 50 messages"`,
			},
		},
		{
			name:  "unknown type",
			event: EventMsg{Type: "unknown_event", Text: "test"},
			contains: []string{
				`"type":"unknown_event"`,
				`"text":"test"`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := json.Marshal(tt.event)
			if err != nil {
				t.Fatalf("MarshalJSON error: %v", err)
			}
			got := string(b)
			for _, want := range tt.contains {
				if !strings.Contains(got, want) {
					t.Errorf("JSON missing expected substring %q.\nGot: %s", want, got)
				}
			}
			for _, dontWant := range tt.notContains {
				if strings.Contains(got, dontWant) {
					t.Errorf("JSON should not contain %q.\nGot: %s", dontWant, got)
				}
			}
		})
	}
}

func TestLLMMessageToResponseItems(t *testing.T) {
	tests := []struct {
		name         string
		msg          llm.Message
		msgID        string
		wantCount    int
		wantTypes    []string
		wantContents [][]string // per-item expected content substrings; empty slice means skip content check
	}{
		{
			name:      "system message",
			msg:       llm.Message{Role: llm.RoleSystem, Content: "You are a helpful assistant."},
			msgID:     "msg-0",
			wantCount: 1,
			wantTypes: []string{"message"},
			wantContents: [][]string{
				{`"role":"system"`, `"type":"input_text"`, `"text":"You are a helpful assistant."`},
			},
		},
		{
			name:      "user message",
			msg:       llm.Message{Role: llm.RoleUser, Content: "Hello, world!"},
			msgID:     "msg-1",
			wantCount: 1,
			wantTypes: []string{"message"},
			wantContents: [][]string{
				{`"role":"user"`, `"type":"input_text"`, `"text":"Hello, world!"`},
			},
		},
		{
			name:      "assistant plain text",
			msg:       llm.Message{Role: llm.RoleAssistant, Content: "I'll help you with that."},
			msgID:     "msg-2",
			wantCount: 1,
			wantTypes: []string{"message"},
			wantContents: [][]string{
				{`"role":"assistant"`, `"type":"output_text"`, `"text":"I'll help you with that."`},
			},
		},
		{
			name: "assistant with one tool call",
			msg: llm.Message{
				Role:    llm.RoleAssistant,
				Content: "Let me check the files.",
				ToolCalls: []llm.ToolCall{
					{ID: "call-1", Type: "function", Function: llm.FunctionCall{Name: "shell", Arguments: `{"command":"ls"}`}},
				},
			},
			msgID:     "msg-3",
			wantCount: 2,
			wantTypes: []string{"message", "function_call"},
			wantContents: [][]string{
				{`"role":"assistant"`, `"type":"output_text"`},
				{`"name":"shell"`, `"call_id":"call-1"`},
			},
		},
		{
			name: "assistant with two tool calls",
			msg: llm.Message{
				Role:    llm.RoleAssistant,
				Content: "Running commands.",
				ToolCalls: []llm.ToolCall{
					{ID: "call-a", Type: "function", Function: llm.FunctionCall{Name: "shell", Arguments: `{"command":"ls"}`}},
					{ID: "call-b", Type: "function", Function: llm.FunctionCall{Name: "read_file", Arguments: `{"path":"/etc/hosts"}`}},
				},
			},
			msgID:     "msg-4",
			wantCount: 3,
			wantTypes: []string{"message", "function_call", "function_call"},
			wantContents: [][]string{
				{`"role":"assistant"`, `"type":"output_text"`},
				{`"name":"shell"`, `"call_id":"call-a"`},
				{`"name":"read_file"`, `"call_id":"call-b"`},
			},
		},
		{
			name:      "tool message",
			msg:       llm.Message{Role: llm.RoleTool, ToolCallID: "call-1", Content: "file1\nfile2"},
			msgID:     "msg-5",
			wantCount: 1,
			wantTypes: []string{"function_call_output"},
			wantContents: [][]string{
				{`"call_id":"call-1"`, `"output"`},
			},
		},
		{
			name:      "tool message empty call id",
			msg:       llm.Message{Role: llm.RoleTool, ToolCallID: "", Content: "error output"},
			msgID:     "msg-6",
			wantCount: 1,
			wantTypes: []string{"function_call_output"},
			wantContents: [][]string{
				{`"call_id":"unknown"`},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := LLMMessageToResponseItems(tt.msg, tt.msgID)
			if len(got) != tt.wantCount {
				t.Fatalf("expected %d items, got %d", tt.wantCount, len(got))
			}
			for i, item := range got {
				if item.Type != tt.wantTypes[i] {
					t.Errorf("item[%d]: expected type %q, got %q", i, tt.wantTypes[i], item.Type)
				}
			}
			for i, item := range got {
				b, err := json.Marshal(item)
				if err != nil {
					t.Fatalf("MarshalJSON error: %v", err)
				}
				gotJSON := string(b)
				for _, want := range tt.wantContents[i] {
					if !strings.Contains(gotJSON, want) {
						t.Errorf("item[%d] JSON missing %q.\nGot: %s", i, want, gotJSON)
					}
				}
			}
		})
	}
}

func TestChatMessageToResponseItems(t *testing.T) {
	tests := []struct {
		name         string
		msg          ChatMessage
		msgID        string
		wantCount    int
		wantTypes    []string
		wantContents [][]string
	}{
		{
			name:      "system message",
			msg:       ChatMessage{Type: MessageTypeSystem, Content: "System prompt"},
			msgID:     "msg-sys",
			wantCount: 1,
			wantTypes: []string{"message"},
			wantContents: [][]string{
				{`"role":"system"`, `"type":"input_text"`, `"text":"System prompt"`},
			},
		},
		{
			name:      "human message",
			msg:       ChatMessage{Type: MessageTypeHuman, Content: "User query"},
			msgID:     "msg-human",
			wantCount: 1,
			wantTypes: []string{"message"},
			wantContents: [][]string{
				{`"role":"user"`, `"type":"input_text"`, `"text":"User query"`},
			},
		},
		{
			name:      "assistant plain text",
			msg:       ChatMessage{Type: MessageTypeAssistant, Content: "Assistant reply"},
			msgID:     "msg-ast",
			wantCount: 1,
			wantTypes: []string{"message"},
			wantContents: [][]string{
				{`"role":"assistant"`, `"type":"output_text"`, `"text":"Assistant reply"`},
			},
		},
		{
			name: "assistant with tool calls",
			msg: ChatMessage{
				Type:    MessageTypeAssistant,
				Content: "Calling tools",
				ToolCalls: []ToolCallData{
					{ID: "tc-1", Type: "function", Function: ToolCallFunction{Name: "shell", Arguments: json.RawMessage(`{"cmd":"ls"}`)}},
				},
			},
			msgID:     "msg-ast2",
			wantCount: 2,
			wantTypes: []string{"message", "function_call"},
			wantContents: [][]string{
				{`"role":"assistant"`, `"type":"output_text"`},
				{`"name":"shell"`, `"call_id":"tc-1"`},
			},
		},
		{
			name:      "tool message",
			msg:       ChatMessage{Type: MessageTypeTool, Content: "tool result", ToolCallID: strPtr("tc-1")},
			msgID:     "msg-tool",
			wantCount: 1,
			wantTypes: []string{"function_call_output"},
			wantContents: [][]string{
				{`"call_id":"tc-1"`},
			},
		},
		{
			name:      "tool message nil call id",
			msg:       ChatMessage{Type: MessageTypeTool, Content: "error", ToolCallID: nil},
			msgID:     "msg-tool2",
			wantCount: 1,
			wantTypes: []string{"function_call_output"},
			wantContents: [][]string{
				{`"call_id":"unknown"`},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ChatMessageToResponseItems(tt.msg, tt.msgID)
			if len(got) != tt.wantCount {
				t.Fatalf("expected %d items, got %d", tt.wantCount, len(got))
			}
			for i, item := range got {
				if item.Type != tt.wantTypes[i] {
					t.Errorf("item[%d]: expected type %q, got %q", i, tt.wantTypes[i], item.Type)
				}
			}
			for i, item := range got {
				b, err := json.Marshal(item)
				if err != nil {
					t.Fatalf("MarshalJSON error: %v", err)
				}
				gotJSON := string(b)
				for _, want := range tt.wantContents[i] {
					if !strings.Contains(gotJSON, want) {
						t.Errorf("item[%d] JSON missing %q.\nGot: %s", i, want, gotJSON)
					}
				}
			}
		})
	}
}

// isolateRolloutHome 把 rollout 根目录隔离到 t.TempDir() —— 输出路径由 $HOME 派生（RolloutRootDir → homeDirOrFallback），不隔离就会把测试残留写进 ~/.yagent/data/rollout，而 TUI /resume 只按 mtime 倒序取前 200 条，测试垃圾会把真实会话挤出列表。
func isolateRolloutHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

func TestRolloutWriter(t *testing.T) {
	isolateRolloutHome(t)
	writer, err := NewRolloutWriter("test-agent", "test-task-001", "test-project")
	if err != nil {
		t.Fatalf("NewRolloutWriter error: %v", err)
	}
	if !writer.Enabled() {
		t.Fatal("expected writer to be enabled")
	}

	filePath := writer.FilePath()
	if filePath == "" {
		t.Fatal("expected non-empty file path")
	}

	meta := SessionMeta{
		ID:               "sess-001",
		SessionID:        "sess-001",
		Cwd:              "/tmp/test",
		CliVersion:       "0.118.0",
		Originator:       "yagent",
		ModelProvider:    "openai",
		Source:           "yagent-cli",
		BaseInstructions: "You are a helpful agent.",
		ContextWindow:    200000,
		HistoryMode:      "full",
		Git: GitInfo{
			SHA:       "abc123def",
			Branch:    "main",
			OriginURL: "https://github.com/test/repo",
		},
	}
	if err := writer.WriteSessionMeta(meta); err != nil {
		t.Fatalf("WriteSessionMeta error: %v", err)
	}

	tc := TurnContext{
		TurnID:            writer.NextTurn(),
		Cwd:               "/tmp/test",
		Model:             "o4-mini",
		Effort:            "medium",
		ApprovalPolicy:    "suggest",
		SandboxPolicy:     map[string]string{"mode": "danger-full-access"},
		Summary:           "auto",
		WorkspaceRoots:    []string{"/tmp/test"},
		CollaborationMode: "single",
	}
	if err := writer.WriteTurnContext(tc); err != nil {
		t.Fatalf("WriteTurnContext error: %v", err)
	}

	items := []ResponseItem{
		{Type: "message", Role: "user", ID: writer.NextMessageID(), Content: []MessageContentItem{{Type: "input_text", Text: "hello"}}},
		{Type: "message", Role: "assistant", ID: writer.NextMessageID(), Content: []MessageContentItem{{Type: "output_text", Text: "Hi there!"}}},
		{
			Type: "function_call", ID: writer.NextMessageID() + "_fc_0",
			CallID: "call-123", Name: "shell", Namespace: "default",
			Arguments: `{"command":"ls"}`,
		},
		{
			Type: "function_call_output", CallID: "call-123",
			Output: `{"output":"file1\nfile2","metadata":{"exit_code":0,"duration_seconds":0}}`,
		},
	}
	for _, item := range items {
		if err := writer.WriteResponseItem(item); err != nil {
			t.Fatalf("WriteResponseItem error: %v", err)
		}
	}

	events := []EventMsg{
		{Type: "task_started"},
		{Type: "task_complete"},
		{Type: "turn_aborted", Reason: "test abort"},
		StepInfoToEventMsg(1, "shell", map[string]string{"command": "ls"}, true),
	}
	for _, evt := range events {
		if err := writer.WriteEventMsg(evt); err != nil {
			t.Fatalf("WriteEventMsg error: %v", err)
		}
	}

	compacted := CompactedPayload{
		Summary:      "Compressed history",
		TokensBefore: 5000,
		TokensAfter:  2000,
	}
	if err := writer.WriteCompacted(compacted); err != nil {
		t.Fatalf("WriteCompacted error: %v", err)
	}

	// world_state / inter_agent_communication_metadata 的写入侧已于 2026-10-14 删除（无生产调用方）；读侧对这两类记录及任何未知类型的容忍仍由 rollout_reader_test.go 钉住——/resume 会读到老二进制留下的文件。

	if err := writer.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}

	f, err := os.Open(filePath)
	if err != nil {
		t.Fatalf("Failed to open file: %v", err)
	}
	defer f.Close()

	content, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) == 0 {
		t.Fatal("expected non-empty file")
	}

	// 信封格式（timestamp 形态、type 白名单、恰好三个顶层字段、首条是 session_meta）由 TestRolloutEnvelopeFormat 统一钉住，这里只保证每行都是可解析的回执信封。
	for i, line := range lines {
		var envelope RolloutEnvelope
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			t.Fatalf("line %d: invalid JSON: %v\nLine: %s", i, err, line)
		}
	}

	var env RolloutEnvelope
	json.Unmarshal([]byte(lines[0]), &env)
	smMap, ok := env.Payload.(map[string]interface{})
	if !ok {
		t.Fatalf("expected payload to be map[string]interface{}, got %T", env.Payload)
	}
	if smMap["id"] != "sess-001" {
		t.Errorf("expected session ID 'sess-001', got %v", smMap["id"])
	}
	if smMap["cli_version"] != "0.118.0" {
		t.Errorf("expected cli version '0.118.0', got %v", smMap["cli_version"])
	}

	t.Logf("Wrote %d records to %s", len(lines), filePath)
}

func TestRolloutWriterThreadSafety(t *testing.T) {
	isolateRolloutHome(t)
	writer, err := NewRolloutWriter("test-agent", "test-task-thread", "test-project")
	if err != nil {
		t.Fatalf("NewRolloutWriter error: %v", err)
	}

	const numGoroutines = 10
	const writesPerGoroutine = 100
	var wg sync.WaitGroup

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(gID int) {
			defer wg.Done()
			for i := 0; i < writesPerGoroutine; i++ {
				item := ResponseItem{
					Type:    "message",
					Role:    "user",
					ID:      fmt.Sprintf("msg-g%d-%d", gID, i),
					Content: []MessageContentItem{{Type: "input_text", Text: fmt.Sprintf("hello from goroutine %d, write %d", gID, i)}},
				}
				if err := writer.WriteResponseItem(item); err != nil {
					t.Errorf("goroutine %d, write %d: %v", gID, i, err)
				}
			}
		}(g)
	}

	wg.Wait()

	if err := writer.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}

	filePath := writer.FilePath()
	f, err := os.Open(filePath)
	if err != nil {
		t.Fatalf("Failed to open file: %v", err)
	}
	defer f.Close()

	content, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	expectedLines := numGoroutines * writesPerGoroutine
	if len(lines) != expectedLines {
		t.Errorf("expected %d lines, got %d", expectedLines, len(lines))
	}

	for i, line := range lines {
		var envelope RolloutEnvelope
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			t.Errorf("line %d: invalid JSON: %v", i, err)
		}
	}

	t.Logf("Thread safety test: wrote %d records, verified %d lines", expectedLines, len(lines))
}

func TestRolloutWriterContext(t *testing.T) {
	isolateRolloutHome(t)
	writer, err := NewRolloutWriter("test-agent", "test-task-ctx", "test-project")
	if err != nil {
		t.Fatalf("NewRolloutWriter error: %v", err)
	}
	defer writer.Close()

	ctx := context.Background()
	if GetRolloutWriter(ctx) != nil {
		t.Fatal("expected nil writer from empty context")
	}

	ctxWithWriter := WithRolloutWriter(ctx, writer)
	got := GetRolloutWriter(ctxWithWriter)
	if got == nil {
		t.Fatal("expected non-nil writer from context")
	}
	if got != writer {
		t.Fatal("retrieved writer is not the same as injected writer")
	}

	ctxWithNil := WithRolloutWriter(ctx, nil)
	if GetRolloutWriter(ctxWithNil) != nil {
		t.Fatal("expected nil writer after injecting nil")
	}

	disabledWriter := &RolloutWriter{enabled: false}
	ctxWithDisabled := WithRolloutWriter(ctx, disabledWriter)
	if GetRolloutWriter(ctxWithDisabled) != nil {
		t.Fatal("expected nil writer after injecting disabled writer")
	}
}

func TestRolloutEnvelopeFormat(t *testing.T) {
	isolateRolloutHome(t)
	writer, err := NewRolloutWriter("test-agent", "test-task-envelope", "test-project")
	if err != nil {
		t.Fatalf("NewRolloutWriter error: %v", err)
	}

	writer.WriteSessionMeta(SessionMeta{
		ID:               "sess-abc",
		SessionID:        "sess-abc",
		Cwd:              "/tmp/test",
		CliVersion:       "0.118.0",
		Originator:       "yagent",
		ModelProvider:    "openai",
		Source:           "yagent-cli",
		BaseInstructions: "Be helpful.",
		ContextWindow:    200000,
		HistoryMode:      "full",
	})

	writer.WriteTurnContext(TurnContext{
		TurnID:            writer.NextTurn(),
		Cwd:               "/tmp/test",
		Model:             "o4-mini",
		Effort:            "medium",
		ApprovalPolicy:    "suggest",
		WorkspaceRoots:    []string{"/tmp/test"},
		CollaborationMode: "single",
	})

	writer.WriteEventMsg(EventMsg{Type: "task_started"})

	userID := writer.NextMessageID()
	writer.WriteResponseItem(ResponseItem{
		Type:    "message",
		Role:    "user",
		ID:      userID,
		Content: []MessageContentItem{{Type: "input_text", Text: "What is Go?"}},
	})

	assistID := writer.NextMessageID()
	writer.WriteResponseItem(ResponseItem{
		Type:    "message",
		Role:    "assistant",
		ID:      assistID,
		Content: []MessageContentItem{{Type: "output_text", Text: "Go is a statically typed, compiled programming language."}},
	})

	fcID := writer.NextMessageID()
	writer.WriteResponseItem(ResponseItem{
		Type:      "function_call",
		ID:        fmt.Sprintf("%s_fc_0", fcID),
		CallID:    "tool-call-001",
		Name:      "shell",
		Namespace: "default",
		Arguments: `{"command":"go version"}`,
	})

	writer.WriteResponseItem(ResponseItem{
		Type:   "function_call_output",
		CallID: "tool-call-001",
		Output: `{"output":"go version go1.22.0 linux/amd64","metadata":{"exit_code":0,"duration_seconds":0.01}}`,
	})

	writer.WriteEventMsg(EventMsg{Type: "task_complete"})

	if err := writer.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}

	filePath := writer.FilePath()
	f, err := os.Open(filePath)
	if err != nil {
		t.Fatalf("Failed to open file: %v", err)
	}
	defer f.Close()

	content, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) != 8 {
		t.Fatalf("expected 8 records, got %d", len(lines))
	}

	allowedTypes := map[string]bool{
		"session_meta":                       true,
		"turn_context":                       true,
		"response_item":                      true,
		"event_msg":                          true,
		"inter_agent_communication_metadata": true,
		"compacted":                          true,
		"world_state":                        true,
	}
	iso8601Re := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d+Z$`)

	for i, line := range lines {
		var raw map[string]interface{}
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			t.Fatalf("line %d: invalid JSON: %v", i, err)
		}
		if len(raw) != 3 {
			t.Errorf("line %d: expected 3 top-level fields, got %d", i, len(raw))
		}
		if _, ok := raw["timestamp"]; !ok {
			t.Errorf("line %d: missing 'timestamp' field", i)
		}
		if _, ok := raw["type"]; !ok {
			t.Errorf("line %d: missing 'type' field", i)
		}
		if _, ok := raw["payload"]; !ok {
			t.Errorf("line %d: missing 'payload' field", i)
		}

		ts, ok := raw["timestamp"].(string)
		if !ok || ts == "" {
			t.Errorf("line %d: invalid timestamp", i)
		}
		if !iso8601Re.MatchString(ts) {
			t.Errorf("line %d: timestamp %q is not valid ISO8601", i, ts)
		}

		typ, ok := raw["type"].(string)
		if !ok || typ == "" {
			t.Errorf("line %d: invalid type", i)
		}
		if !allowedTypes[typ] {
			t.Errorf("line %d: unknown type %q", i, typ)
		}

		if raw["payload"] == nil {
			t.Errorf("line %d: payload is nil", i)
		}
	}

	var first RolloutEnvelope
	json.Unmarshal([]byte(lines[0]), &first)
	if first.Type != "session_meta" {
		t.Errorf("expected first record to be session_meta, got %q", first.Type)
	}

	var last RolloutEnvelope
	json.Unmarshal([]byte(lines[len(lines)-1]), &last)
	if last.Type != "event_msg" {
		t.Errorf("expected last record to be event_msg, got %q", last.Type)
	}
	var lastEvent EventMsg
	lastPayload, _ := json.Marshal(last.Payload)
	json.Unmarshal(lastPayload, &lastEvent)
	if lastEvent.Type != "task_complete" {
		t.Errorf("expected last event to be task_complete, got %q", lastEvent.Type)
	}

	t.Logf("Envelope format test passed: %d records, first=%s, last=%s", len(lines), first.Type, last.Type)
}

func TestRolloutWriterTokenCount(t *testing.T) {
	isolateRolloutHome(t)
	writer, err := NewRolloutWriter("test-agent", "test-task-token", "test-project")
	if err != nil {
		t.Fatalf("NewRolloutWriter error: %v", err)
	}

	usage1 := &llm.TokenUsage{
		PromptTokens:             100,
		CompletionTokens:         50,
		TotalTokens:              150,
		CacheCreationInputTokens: 10,
		CacheReadInputTokens:     30,
	}
	if err := writer.WriteTokenCount(usage1.PromptTokens, usage1.CompletionTokens, usage1.TotalTokens, usage1.CacheCreationInputTokens, usage1.CacheReadInputTokens); err != nil {
		t.Fatalf("WriteTokenCount(1) error: %v", err)
	}

	usage2 := &llm.TokenUsage{
		PromptTokens:             200,
		CompletionTokens:         80,
		TotalTokens:              280,
		CacheCreationInputTokens: 20,
		CacheReadInputTokens:     60,
	}
	if err := writer.WriteTokenCount(usage2.PromptTokens, usage2.CompletionTokens, usage2.TotalTokens, usage2.CacheCreationInputTokens, usage2.CacheReadInputTokens); err != nil {
		t.Fatalf("WriteTokenCount(2) error: %v", err)
	}

	if err := writer.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}

	filePath := writer.FilePath()
	f, err := os.Open(filePath)
	if err != nil {
		t.Fatalf("Failed to open file: %v", err)
	}
	defer f.Close()

	content, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	var tokenCountLines []string
	for _, line := range lines {
		var envelope RolloutEnvelope
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			t.Fatalf("invalid JSON: %v\nLine: %s", err, line)
		}
		if envelope.Type == "event_msg" {
			tokenCountLines = append(tokenCountLines, line)
		}
	}

	if len(tokenCountLines) != 2 {
		t.Fatalf("expected 2 token_count event_msg records, got %d", len(tokenCountLines))
	}

	var firstEnvelope RolloutEnvelope
	json.Unmarshal([]byte(tokenCountLines[0]), &firstEnvelope)
	firstEvent, _ := json.Marshal(firstEnvelope.Payload)
	var firstTokenEvent EventMsg
	json.Unmarshal(firstEvent, &firstTokenEvent)
	if firstTokenEvent.Type != "token_count" {
		t.Fatalf("expected first event type 'token_count', got %q", firstTokenEvent.Type)
	}
	firstInfo, ok := firstTokenEvent.Info.(map[string]interface{})
	if !ok {
		t.Fatalf("expected first info to be map[string]interface{}")
	}
	firstTotalUsage := firstInfo["total_token_usage"].(map[string]interface{})
	firstLastUsage := firstInfo["last_token_usage"].(map[string]interface{})
	if int64(firstTotalUsage["input_tokens"].(float64)) != 100 {
		t.Errorf("first: expected total input_tokens=100, got %v", firstTotalUsage["input_tokens"])
	}
	if int64(firstLastUsage["input_tokens"].(float64)) != 100 {
		t.Errorf("first: expected last input_tokens=100, got %v", firstLastUsage["input_tokens"])
	}
	if int64(firstLastUsage["cached_input_tokens"].(float64)) != 30 {
		t.Errorf("first: expected last cached_input_tokens=30, got %v", firstLastUsage["cached_input_tokens"])
	}

	var secondEnvelope RolloutEnvelope
	json.Unmarshal([]byte(tokenCountLines[1]), &secondEnvelope)
	secondEvent, _ := json.Marshal(secondEnvelope.Payload)
	var secondTokenEvent EventMsg
	json.Unmarshal(secondEvent, &secondTokenEvent)
	if secondTokenEvent.Type != "token_count" {
		t.Fatalf("expected second event type 'token_count', got %q", secondTokenEvent.Type)
	}
	secondInfo, ok := secondTokenEvent.Info.(map[string]interface{})
	if !ok {
		t.Fatalf("expected second info to be map[string]interface{}")
	}
	secondTotalUsage := secondInfo["total_token_usage"].(map[string]interface{})
	secondLastUsage := secondInfo["last_token_usage"].(map[string]interface{})
	if int64(secondTotalUsage["input_tokens"].(float64)) != 300 {
		t.Errorf("second: expected total input_tokens=300, got %v", secondTotalUsage["input_tokens"])
	}
	if int64(secondLastUsage["input_tokens"].(float64)) != 200 {
		t.Errorf("second: expected last input_tokens=200, got %v", secondLastUsage["input_tokens"])
	}
	if int64(secondTotalUsage["cached_input_tokens"].(float64)) != 90 {
		t.Errorf("second: expected total cached_input_tokens=90 (30+60), got %v", secondTotalUsage["cached_input_tokens"])
	}
	if int64(secondTotalUsage["output_tokens"].(float64)) != 130 {
		t.Errorf("second: expected total output_tokens=130 (50+80), got %v", secondTotalUsage["output_tokens"])
	}
	if int64(secondTotalUsage["total_tokens"].(float64)) != 430 {
		t.Errorf("second: expected total total_tokens=430 (150+280), got %v", secondTotalUsage["total_tokens"])
	}

	t.Logf("Token count test passed: %d token_count records, cumulative totals verified", len(tokenCountLines))
}

func TestRolloutWriterTokenCount_NilUsage(t *testing.T) {
	isolateRolloutHome(t)
	writer, err := NewRolloutWriter("test-agent", "test-task-nil", "test-project")
	if err != nil {
		t.Fatalf("NewRolloutWriter error: %v", err)
	}
	defer writer.Close()

	// 写入零 token（应被静默忽略）
	if err := writer.WriteTokenCount(0, 0, 0, 0, 0); err != nil {
		t.Fatalf("WriteTokenCount(0,0,0,0,0) error: %v", err)
	}

	filePath := writer.FilePath()
	f, err := os.Open(filePath)
	if err != nil {
		t.Fatalf("Failed to open file: %v", err)
	}
	defer f.Close()

	content, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}

	if len(strings.TrimSpace(string(content))) != 0 {
		t.Errorf("expected empty file after nil usage write, got: %s", string(content))
	}

	t.Log("Nil usage test passed: no records written for zero token usage")
}

// B1 的写读契约：task_id 兜底填充、终止事件携带 steps/max_steps/stop_reason/has_delegated，且恢复侧能原样读回并据此判定结束状态。
func TestRolloutRunStateRoundTrip(t *testing.T) {
	cases := []struct {
		name       string
		write      func(w *RolloutWriter) error
		wantState  RolloutFinishState
		wantReason string
		wantSteps  int
		wantMax    int
		wantDeleg  bool
	}{
		{
			name: "aborted_max_steps",
			write: func(w *RolloutWriter) error {
				return w.WriteRunAborted("DirectorAgent exceeded max steps", RunState{
					Steps: 30, MaxSteps: 30, StopReason: "max_steps", HasDelegated: true,
				})
			},
			wantState: FinishInterrupted, wantReason: "max_steps",
			wantSteps: 30, wantMax: 30, wantDeleg: true,
		},
		{
			name: "complete_agent_exit",
			write: func(w *RolloutWriter) error {
				return w.WriteRunComplete(RunState{
					Steps: 7, MaxSteps: 30, StopReason: "agent_exit", HasDelegated: true,
				})
			},
			wantState: FinishComplete, wantReason: "agent_exit",
			wantSteps: 7, wantMax: 30, wantDeleg: true,
		},
		{
			name: "complete_without_run_state",
			// sub-agent 侧只带步数、不标注 stop_reason（见 executor.go 的说明）
			write: func(w *RolloutWriter) error {
				return w.WriteRunComplete(RunState{Steps: 12, MaxSteps: 50})
			},
			wantState: FinishComplete, wantReason: "",
			wantSteps: 12, wantMax: 50, wantDeleg: false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			isolateRolloutHome(t)
			w, err := NewRolloutWriter("director", "task-uuid-123", "roundtrip-test")
			if err != nil || !w.Enabled() {
				t.Skipf("rollout writer unavailable: %v", err)
			}
			path := w.FilePath()

			// session_meta 不显式传 task_id → writer 用构造时持有的 taskID 兜底填充
			if err := w.WriteSessionMeta(SessionMeta{SessionID: w.SessionID(), Cwd: "/repo"}); err != nil {
				t.Fatalf("WriteSessionMeta: %v", err)
			}
			// 至少要有一条消息，否则 ReadRolloutMemory 走零消息错误路径
			if err := w.WriteResponseItem(ResponseItem{
				Type: "message", Role: "user", ID: "u1",
				Content: []MessageContentItem{{Type: "input_text", Text: "do it"}},
			}); err != nil {
				t.Fatalf("WriteResponseItem: %v", err)
			}
			if err := tc.write(w); err != nil {
				t.Fatalf("terminal event: %v", err)
			}
			w.Close()

			_, info, err := ReadRolloutMemory(path, DefaultRolloutReadOptions())
			if err != nil {
				t.Fatalf("ReadRolloutMemory: %v", err)
			}

			if info.TaskID != "task-uuid-123" {
				t.Errorf("TaskID = %q, want task-uuid-123（writer 应兜底填充）", info.TaskID)
			}
			if got := info.FinishState(); got != tc.wantState {
				t.Errorf("FinishState = %v, want %v", got, tc.wantState)
			}
			if info.StopReason != tc.wantReason {
				t.Errorf("StopReason = %q, want %q", info.StopReason, tc.wantReason)
			}
			if info.Steps != tc.wantSteps || info.MaxSteps != tc.wantMax {
				t.Errorf("Steps/MaxSteps = %d/%d, want %d/%d",
					info.Steps, info.MaxSteps, tc.wantSteps, tc.wantMax)
			}
			if info.HasDelegated != tc.wantDeleg {
				t.Errorf("HasDelegated = %v, want %v", info.HasDelegated, tc.wantDeleg)
			}
			if got := info.BestStep(); got != tc.wantSteps {
				t.Errorf("BestStep = %d, want %d（显式步数应优先于回合数近似）", got, tc.wantSteps)
			}

			// 清单侧也要能拿到 task_id（B5 按 taskID 归并依赖它）
			sessions, err := ListRolloutSessions(RolloutRootDir(), 0)
			if err != nil {
				t.Fatalf("ListRolloutSessions: %v", err)
			}
			if len(sessions) != 1 {
				t.Fatalf("sessions = %d, want 1", len(sessions))
			}
			if sessions[0].TaskID != "task-uuid-123" {
				t.Errorf("session TaskID = %q, want task-uuid-123", sessions[0].TaskID)
			}
		})
	}
}

func TestRolloutSessionMetaKeepsExplicitTaskID(t *testing.T) {
	isolateRolloutHome(t)
	w, err := NewRolloutWriter("director", "writer-task-id", "explicit-test")
	if err != nil || !w.Enabled() {
		t.Skipf("rollout writer unavailable: %v", err)
	}
	path := w.FilePath()
	if err := w.WriteSessionMeta(SessionMeta{
		SessionID: w.SessionID(),
		TaskID:    "explicit-task-id",
	}); err != nil {
		t.Fatalf("WriteSessionMeta: %v", err)
	}
	if err := w.WriteResponseItem(ResponseItem{
		Type: "message", Role: "user", ID: "u1",
		Content: []MessageContentItem{{Type: "input_text", Text: "hi"}},
	}); err != nil {
		t.Fatalf("WriteResponseItem: %v", err)
	}
	w.Close()

	_, info, err := ReadRolloutMemory(path, DefaultRolloutReadOptions())
	if err != nil {
		t.Fatalf("ReadRolloutMemory: %v", err)
	}
	if info.TaskID != "explicit-task-id" {
		t.Errorf("TaskID = %q, want explicit-task-id（显式值优先）", info.TaskID)
	}
}

// BestStep 的回退顺序：显式步数 → sub_agent_activity 观测值 → assistant 回合数近似。
func TestRolloutInfoBestStepFallback(t *testing.T) {
	cases := []struct {
		name string
		info RolloutInfo
		want int
	}{
		{"explicit steps win", RolloutInfo{Steps: 5, MaxStepObserved: 9, AssistantTurns: 12}, 5},
		{"observed steps next", RolloutInfo{MaxStepObserved: 9, AssistantTurns: 12}, 9},
		{"turns as approximation", RolloutInfo{AssistantTurns: 12}, 12},
		{"nothing known", RolloutInfo{}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.info.BestStep(); got != tc.want {
				t.Errorf("BestStep = %d, want %d", got, tc.want)
			}
		})
	}
}

// B4 往返：thinklink_entry 与 compacted 落盘后可被读侧恢复——journal 条目进 RolloutInfo.ThinklinkEntries（不进消息），compacted 在 SummaryTail 模式下注入锚点 System 消息。
func TestRolloutThinklinkAndCompactedRestore(t *testing.T) {
	isolateRolloutHome(t)
	w, err := NewRolloutWriter("director", "task-thinklink", "thinklink-test")
	if err != nil {
		t.Fatalf("NewRolloutWriter() error = %v", err)
	}
	path := w.FilePath()
	if err := w.WriteSessionMeta(SessionMeta{SessionID: w.SessionID(), Cwd: "/repo"}); err != nil {
		t.Fatalf("WriteSessionMeta: %v", err)
	}
	// 至少一条消息，避免零消息错误路径
	if err := w.WriteResponseItem(ResponseItem{
		Type: "message", Role: "user",
		Content: []MessageContentItem{{Type: "input_text", Text: "do the thing"}},
	}); err != nil {
		t.Fatalf("WriteResponseItem: %v", err)
	}
	writeThinklink := func(kind, content string, step int) {
		t.Helper()
		if err := w.WriteEventMsg(EventMsg{Type: "thinklink_entry", Kind: kind, Text: content, StepNumber: step}); err != nil {
			t.Fatalf("WriteEventMsg(thinklink_entry): %v", err)
		}
	}
	writeThinklink("user_input", "do the thing", 0)
	writeThinklink("thought_plan", "## Thought & Plan\n* [ ] 1. step", 2)
	if err := w.WriteCompacted(CompactedPayload{
		Summary: "context truncated: 3 results compacted", TokensBefore: 90000, TokensAfter: 50000,
	}); err != nil {
		t.Fatalf("WriteCompacted: %v", err)
	}
	if err := w.WriteRunComplete(RunState{Steps: 3, MaxSteps: 30, StopReason: "agent_exit"}); err != nil {
		t.Fatalf("WriteRunComplete: %v", err)
	}
	w.Close()

	t.Run("full_mode_recovers_journal", func(t *testing.T) {
		_, info, err := ReadRolloutMemory(path, RolloutReadOptions{})
		if err != nil {
			t.Fatalf("ReadRolloutMemory: %v", err)
		}
		if len(info.ThinklinkEntries) != 2 {
			t.Fatalf("ThinklinkEntries len = %d, want 2", len(info.ThinklinkEntries))
		}
		if info.ThinklinkEntries[0].Kind != "user_input" || info.ThinklinkEntries[0].Content != "do the thing" || info.ThinklinkEntries[0].Step != 0 {
			t.Errorf("entry[0] = %+v", info.ThinklinkEntries[0])
		}
		if info.ThinklinkEntries[1].Kind != "thought_plan" || info.ThinklinkEntries[1].Step != 2 ||
			!strings.Contains(info.ThinklinkEntries[1].Content, "## Thought & Plan") {
			t.Errorf("entry[1] = %+v", info.ThinklinkEntries[1])
		}
		// journal 条目不产生消息：消息数不因事件变化
		if info.Messages < 1 {
			t.Errorf("Messages = %d, want >= 1 (user message)", info.Messages)
		}
	})

	// tail 模式的条数/顺序/"compacted 后无消息"边界由 TestReadRolloutMemory_CompactedModes 钉住，统计字段往返由 TestRolloutWriter_CompactedRoundTrip 钉住；这里只留这一件：真实写入路径产出的 Summary 原文必须在锚定 System 消息里原样出现。
	t.Run("summary_tail_round_trips_written_summary", func(t *testing.T) {
		mem, info, err := ReadRolloutMemory(path, RolloutReadOptions{Compaction: CompactionSummaryTail})
		if err != nil {
			t.Fatalf("ReadRolloutMemory: %v", err)
		}
		msgs := mem.GetMessages()
		if len(msgs) == 0 || msgs[0].Type != MessageTypeSystem || !msgs[0].IsAnchored {
			t.Fatalf("first message = %+v, want anchored system summary", firstOr(msgs))
		}
		if msgs[0].Content != "context truncated: 3 results compacted" {
			t.Errorf("summary content = %q, want the exact text passed to WriteCompacted", msgs[0].Content)
		}
		if info.FinishState() != FinishComplete {
			t.Errorf("FinishState = %v, want complete", info.FinishState())
		}
	})
}

func firstOr(msgs []ChatMessage) ChatMessage {
	if len(msgs) > 0 {
		return msgs[0]
	}
	return ChatMessage{}
}

// B5：清单按 task_id 归并的前置数据、空文件过滤与超长 preview 注入块的跳过。
func TestListRolloutSessionsGroupsAndFiltering(t *testing.T) {
	isolateRolloutHome(t)

	wMain, err := NewRolloutWriter("director", "task-grp-1", "grp-test")
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	if err := wMain.WriteSessionMeta(SessionMeta{SessionID: wMain.SessionID(), Cwd: "/repo"}); err != nil {
		t.Fatal(err)
	}
	if err := wMain.WriteResponseItem(ResponseItem{
		Type: "message", Role: "user",
		Content: []MessageContentItem{{Type: "input_text", Text: "build the feature"}},
	}); err != nil {
		t.Fatal(err)
	}
	wMain.Close()

	wSub, err := NewRolloutWriter("coder", "task-grp-1", "grp-test")
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	if err := wSub.WriteSessionMeta(SessionMeta{SessionID: wSub.SessionID(), Cwd: "/repo"}); err != nil {
		t.Fatal(err)
	}
	// 首条 user 消息是超长注入块（>2000 rune），随后才是真实任务描述
	blob := strings.Repeat("ctx ", 600) + "\nactual task: fix bug"
	if err := wSub.WriteResponseItem(ResponseItem{
		Type: "message", Role: "user",
		Content: []MessageContentItem{{Type: "input_text", Text: blob}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := wSub.WriteResponseItem(ResponseItem{
		Type: "message", Role: "user",
		Content: []MessageContentItem{{Type: "input_text", Text: "fix bug"}},
	}); err != nil {
		t.Fatal(err)
	}
	wSub.Close()

	// 独立会话（无 task_id）：各自成组
	wSolo, err := NewRolloutWriter("director", "", "grp-test")
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	if err := wSolo.WriteSessionMeta(SessionMeta{SessionID: wSolo.SessionID(), Cwd: "/repo"}); err != nil {
		t.Fatal(err)
	}
	if err := wSolo.WriteResponseItem(ResponseItem{
		Type: "message", Role: "user",
		Content: []MessageContentItem{{Type: "input_text", Text: "solo task"}},
	}); err != nil {
		t.Fatal(err)
	}
	wSolo.Close()

	// 空文件：只有 session_meta + 终止事件，无任何 message → 清单过滤
	wEmpty, err := NewRolloutWriter("director", "task-empty", "grp-test")
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	if err := wEmpty.WriteSessionMeta(SessionMeta{SessionID: wEmpty.SessionID(), Cwd: "/repo"}); err != nil {
		t.Fatal(err)
	}
	if err := wEmpty.WriteRunComplete(RunState{Steps: 1}); err != nil {
		t.Fatal(err)
	}
	wEmpty.Close()

	items, err := ListRolloutSessions(RolloutRootDir(), 0)
	if err != nil {
		t.Fatalf("ListRolloutSessions: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("items len = %d, want 3 (empty file filtered)", len(items))
	}

	byPath := make(map[string]RolloutSessionInfo, len(items))
	for _, it := range items {
		byPath[it.Path] = it
	}
	main := byPath[wMain.FilePath()]
	if main.TaskID != "task-grp-1" {
		t.Errorf("main task id = %q, want task-grp-1", main.TaskID)
	}
	sub := byPath[wSub.FilePath()]
	if sub.AgentName != "coder" {
		t.Errorf("sub agent name = %q, want coder", sub.AgentName)
	}
	if sub.Preview != "fix bug" {
		t.Errorf("sub preview = %q, want %q (skip injected blob)", sub.Preview, "fix bug")
	}
	if sub.HeadMessages != 2 {
		t.Errorf("sub HeadMessages = %d, want 2", sub.HeadMessages)
	}
	solo := byPath[wSolo.FilePath()]
	if solo.TaskID != "" || solo.Preview != "solo task" {
		t.Errorf("solo = %+v", solo)
	}
}
