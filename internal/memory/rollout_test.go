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

	"codeactor/internal/llm"
)

// ─── TestResponseItemMarshal ───

func TestResponseItemMarshal(t *testing.T) {
	tests := []struct {
		name     string
		item     ResponseItem
		contains []string // expected substrings in JSON output
		notContains []string // unexpected substrings
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
				Type:               "reasoning",
				ID:                 "reason-1",
				Summary:            []MessageContentItem{{Type: "summary_text", Text: "thinking..."}},
				EncryptedContent:   "encrypted-data",
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

// ─── TestEventMsgMarshal ───

func TestEventMsgMarshal(t *testing.T) {
	tests := []struct {
		name     string
		event    EventMsg
		contains []string
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
			name: "user_message",
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

// ─── TestLLMMessageToResponseItems ───

func TestLLMMessageToResponseItems(t *testing.T) {
	tests := []struct {
		name             string
		msg              llm.Message
		msgID            string
		wantCount        int
		wantTypes        []string
		wantContents     [][]string // per-item expected content substrings; empty slice means skip content check
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
			name:  "tool message",
			msg:   llm.Message{Role: llm.RoleTool, ToolCallID: "call-1", Content: "file1\nfile2"},
			msgID: "msg-5",
			wantCount: 1,
			wantTypes: []string{"function_call_output"},
			wantContents: [][]string{
				{`"call_id":"call-1"`, `"output"`},
			},
		},
		{
			name:  "tool message empty call id",
			msg:   llm.Message{Role: llm.RoleTool, ToolCallID: "", Content: "error output"},
			msgID: "msg-6",
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
			// Verify JSON output for content checks per item
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

// ─── TestChatMessageToResponseItems ───

func TestChatMessageToResponseItems(t *testing.T) {
	tests := []struct {
		name         string
		msg          ChatMessage
		msgID        string
		wantCount    int
		wantTypes    []string
		wantContents [][]string // per-item expected content substrings
	}{
		{
			name:  "system message",
			msg:   ChatMessage{Type: MessageTypeSystem, Content: "System prompt"},
			msgID: "msg-sys",
			wantCount: 1,
			wantTypes: []string{"message"},
			wantContents: [][]string{
				{`"role":"system"`, `"type":"input_text"`, `"text":"System prompt"`},
			},
		},
		{
			name:  "human message",
			msg:   ChatMessage{Type: MessageTypeHuman, Content: "User query"},
			msgID: "msg-human",
			wantCount: 1,
			wantTypes: []string{"message"},
			wantContents: [][]string{
				{`"role":"user"`, `"type":"input_text"`, `"text":"User query"`},
			},
		},
		{
			name:  "assistant plain text",
			msg:   ChatMessage{Type: MessageTypeAssistant, Content: "Assistant reply"},
			msgID: "msg-ast",
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
			name:  "tool message",
			msg:   ChatMessage{Type: MessageTypeTool, Content: "tool result", ToolCallID: strPtr("tc-1")},
			msgID: "msg-tool",
			wantCount: 1,
			wantTypes: []string{"function_call_output"},
			wantContents: [][]string{
				{`"call_id":"tc-1"`},
			},
		},
		{
			name:  "tool message nil call id",
			msg:   ChatMessage{Type: MessageTypeTool, Content: "error", ToolCallID: nil},
			msgID: "msg-tool2",
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

// ─── TestRolloutWriter ───

func TestRolloutWriter(t *testing.T) {
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

	// Write SessionMeta
	meta := SessionMeta{
		ID:            "sess-001",
		SessionID:     "sess-001",
		Cwd:           "/tmp/test",
		CliVersion:    "0.118.0",
		Originator:    "codeactor-agent",
		ModelProvider: "openai",
		Source:        "codeactor-cli",
		BaseInstructions: "You are a helpful agent.",
		ContextWindow: 200000,
		HistoryMode:   "full",
		Git: GitInfo{
			SHA:       "abc123def",
			Branch:    "main",
			OriginURL: "https://github.com/test/repo",
		},
	}
	if err := writer.WriteSessionMeta(meta); err != nil {
		t.Fatalf("WriteSessionMeta error: %v", err)
	}

	// Write TurnContext
	tc := TurnContext{
		TurnID:          writer.NextTurn(),
		Cwd:             "/tmp/test",
		Model:           "o4-mini",
		Effort:          "medium",
		ApprovalPolicy:  "suggest",
		SandboxPolicy:   map[string]string{"mode": "danger-full-access"},
		Summary:         "auto",
		WorkspaceRoots:  []string{"/tmp/test"},
		CollaborationMode: "single",
	}
	if err := writer.WriteTurnContext(tc); err != nil {
		t.Fatalf("WriteTurnContext error: %v", err)
	}

	// Write ResponseItems
	items := []ResponseItem{
		// user message
		{Type: "message", Role: "user", ID: writer.NextMessageID(), Content: []MessageContentItem{{Type: "input_text", Text: "hello"}}},
		// assistant message (plain)
		{Type: "message", Role: "assistant", ID: writer.NextMessageID(), Content: []MessageContentItem{{Type: "output_text", Text: "Hi there!"}}},
		// function call
		{
			Type: "function_call", ID: writer.NextMessageID() + "_fc_0",
			CallID: "call-123", Name: "shell", Namespace: "default",
			Arguments: `{"command":"ls"}`,
		},
		// function_call_output
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

	// Write EventMsgs
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

	// Write InterAgentCommunicationMetadata
	comm := InterAgentCommunicationMetadata{
		ParentAgent:      "director",
		ChildAgent:       "worker",
		ParentToolCallID: "parent-call-id",
		Direction:        "outbound",
		Summary:          "Delegated task to worker agent",
	}
	if err := writer.WriteInterAgentComm(comm); err != nil {
		t.Fatalf("WriteInterAgentComm error: %v", err)
	}

	// Write Compacted
	compacted := CompactedPayload{
		Summary:      "Compressed history",
		TokensBefore: 5000,
		TokensAfter:  2000,
	}
	if err := writer.WriteCompacted(compacted); err != nil {
		t.Fatalf("WriteCompacted error: %v", err)
	}

	// Write WorldState
	worldState := WorldStatePayload{
		Files:     []string{"a.go", "b.go"},
		GitBranch: "main",
		GitStatus: "clean",
	}
	if err := writer.WriteWorldState(worldState); err != nil {
		t.Fatalf("WriteWorldState error: %v", err)
	}

	if err := writer.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}

	// Read and validate
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

	allowedTypes := map[string]bool{
		"session_meta":                    true,
		"turn_context":                    true,
		"response_item":                   true,
		"event_msg":                       true,
		"inter_agent_communication_metadata": true,
		"compacted":                       true,
		"world_state":                     true,
	}

	iso8601Re := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d+Z$`)

	for i, line := range lines {
		var envelope RolloutEnvelope
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			t.Fatalf("line %d: invalid JSON: %v\nLine: %s", i, err, line)
		}
		// Check top-level fields
		if envelope.Timestamp == "" {
			t.Errorf("line %d: empty timestamp", i)
		}
		if !iso8601Re.MatchString(envelope.Timestamp) {
			t.Errorf("line %d: timestamp %q is not valid ISO8601", i, envelope.Timestamp)
		}
		if envelope.Type == "" {
			t.Errorf("line %d: empty type", i)
		}
		if !allowedTypes[envelope.Type] {
			t.Errorf("line %d: unknown type %q", i, envelope.Type)
		}
		if envelope.Payload == nil {
			t.Errorf("line %d: nil payload", i)
		}
	}

	// Verify first line is session_meta
	var first RolloutEnvelope
	json.Unmarshal([]byte(lines[0]), &first)
	if first.Type != "session_meta" {
		t.Errorf("expected first record to be session_meta, got %q", first.Type)
	}

	// Verify session_meta content
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

// ─── TestRolloutWriterThreadSafety ───

func TestRolloutWriterThreadSafety(t *testing.T) {
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

	// Verify each line is valid JSON
	for i, line := range lines {
		var envelope RolloutEnvelope
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			t.Errorf("line %d: invalid JSON: %v", i, err)
		}
	}

	t.Logf("Thread safety test: wrote %d records, verified %d lines", expectedLines, len(lines))
}

// ─── TestRolloutWriterContext ───

func TestRolloutWriterContext(t *testing.T) {
	writer, err := NewRolloutWriter("test-agent", "test-task-ctx", "test-project")
	if err != nil {
		t.Fatalf("NewRolloutWriter error: %v", err)
	}
	defer writer.Close()

	// Test normal injection and retrieval
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

	// Test nil writer injection
	ctxWithNil := WithRolloutWriter(ctx, nil)
	if GetRolloutWriter(ctxWithNil) != nil {
		t.Fatal("expected nil writer after injecting nil")
	}

	// Test disabled writer
	disabledWriter := &RolloutWriter{enabled: false}
	ctxWithDisabled := WithRolloutWriter(ctx, disabledWriter)
	if GetRolloutWriter(ctxWithDisabled) != nil {
		t.Fatal("expected nil writer after injecting disabled writer")
	}
}

// ─── TestRolloutEnvelopeFormat ───

func TestRolloutEnvelopeFormat(t *testing.T) {
	writer, err := NewRolloutWriter("test-agent", "test-task-envelope", "test-project")
	if err != nil {
		t.Fatalf("NewRolloutWriter error: %v", err)
	}

	// Write a complete session sequence
	// 1. session_meta
	writer.WriteSessionMeta(SessionMeta{
		ID:            "sess-abc",
		SessionID:     "sess-abc",
		Cwd:           "/tmp/test",
		CliVersion:    "0.118.0",
		Originator:    "codeactor-agent",
		ModelProvider: "openai",
		Source:        "codeactor-cli",
		BaseInstructions: "Be helpful.",
		ContextWindow: 200000,
		HistoryMode:   "full",
	})

	// 2. turn_context
	writer.WriteTurnContext(TurnContext{
		TurnID:            writer.NextTurn(),
		Cwd:               "/tmp/test",
		Model:             "o4-mini",
		Effort:            "medium",
		ApprovalPolicy:    "suggest",
		WorkspaceRoots:    []string{"/tmp/test"},
		CollaborationMode: "single",
	})

	// 3. task_started event
	writer.WriteEventMsg(EventMsg{Type: "task_started"})

	// 4. user message
	userID := writer.NextMessageID()
	writer.WriteResponseItem(ResponseItem{
		Type:    "message",
		Role:    "user",
		ID:      userID,
		Content: []MessageContentItem{{Type: "input_text", Text: "What is Go?"}},
	})

	// 5. assistant message
	assistID := writer.NextMessageID()
	writer.WriteResponseItem(ResponseItem{
		Type:    "message",
		Role:    "assistant",
		ID:      assistID,
		Content: []MessageContentItem{{Type: "output_text", Text: "Go is a statically typed, compiled programming language."}},
	})

	// 6. function call
	fcID := writer.NextMessageID()
	writer.WriteResponseItem(ResponseItem{
		Type:      "function_call",
		ID:        fmt.Sprintf("%s_fc_0", fcID),
		CallID:    "tool-call-001",
		Name:      "shell",
		Namespace: "default",
		Arguments: `{"command":"go version"}`,
	})

	// 7. function_call_output
	writer.WriteResponseItem(ResponseItem{
		Type:   "function_call_output",
		CallID: "tool-call-001",
		Output: `{"output":"go version go1.22.0 linux/amd64","metadata":{"exit_code":0,"duration_seconds":0.01}}`,
	})

	// 8. task_complete event
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
		"session_meta":                    true,
		"turn_context":                    true,
		"response_item":                   true,
		"event_msg":                       true,
		"inter_agent_communication_metadata": true,
		"compacted":                       true,
		"world_state":                     true,
	}
	iso8601Re := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d+Z$`)

	for i, line := range lines {
		// Check top-level has exactly 3 fields: timestamp, type, payload
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

		// Validate timestamp format
		ts, ok := raw["timestamp"].(string)
		if !ok || ts == "" {
			t.Errorf("line %d: invalid timestamp", i)
		}
		if !iso8601Re.MatchString(ts) {
			t.Errorf("line %d: timestamp %q is not valid ISO8601", i, ts)
		}

		// Validate type
		typ, ok := raw["type"].(string)
		if !ok || typ == "" {
			t.Errorf("line %d: invalid type", i)
		}
		if !allowedTypes[typ] {
			t.Errorf("line %d: unknown type %q", i, typ)
		}

		// Validate payload is not null
		if raw["payload"] == nil {
			t.Errorf("line %d: payload is nil", i)
		}
	}

	// Verify first line is session_meta
	var first RolloutEnvelope
	json.Unmarshal([]byte(lines[0]), &first)
	if first.Type != "session_meta" {
		t.Errorf("expected first record to be session_meta, got %q", first.Type)
	}

	// Verify last line is task_complete
	var last RolloutEnvelope
	json.Unmarshal([]byte(lines[len(lines)-1]), &last)
	if last.Type != "event_msg" {
		t.Errorf("expected last record to be event_msg, got %q", last.Type)
	}
	// Verify the event_msg payload is task_complete
	var lastEvent EventMsg
	lastPayload, _ := json.Marshal(last.Payload)
	json.Unmarshal(lastPayload, &lastEvent)
	if lastEvent.Type != "task_complete" {
		t.Errorf("expected last event to be task_complete, got %q", lastEvent.Type)
	}

	t.Logf("Envelope format test passed: %d records, first=%s, last=%s", len(lines), first.Type, last.Type)
}

// ─── TestRolloutWriterTokenCount ───

func TestRolloutWriterTokenCount(t *testing.T) {
	writer, err := NewRolloutWriter("test-agent", "test-task-token", "test-project")
	if err != nil {
		t.Fatalf("NewRolloutWriter error: %v", err)
	}

	// 第一次 token_count 调用
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

	// 第二次 token_count 调用
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

	// 读取并验证
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
	// 应有 2 条 token_count 记录
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

	// 验证第一条
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

	// 验证第二条（累计）
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

// ─── TestRolloutWriterTokenCount_NilUsage ───

func TestRolloutWriterTokenCount_NilUsage(t *testing.T) {
	writer, err := NewRolloutWriter("test-agent", "test-task-nil", "test-project")
	if err != nil {
		t.Fatalf("NewRolloutWriter error: %v", err)
	}
	defer writer.Close()

	// 写入零 token（应被静默忽略）
	if err := writer.WriteTokenCount(0, 0, 0, 0, 0); err != nil {
		t.Fatalf("WriteTokenCount(0,0,0,0,0) error: %v", err)
	}

	// 验证文件为空
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
