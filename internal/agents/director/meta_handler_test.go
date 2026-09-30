// meta_handler_test.go — MetaAgentHandler 组件单测（P0-1 Phase 2d）。
//
// 独立于 DirectorAgent 可测：NewMetaAgentHandler 构造空注册表，CustomAgent 为纯
// 数据类型。断言锁定关键特征（与原 agents 门面层行为逐字一致的部分）：
//   - Register 正常注册（key = "delegate_" + Name）；重复注册返回包装
//     ErrAlreadyRegistered 的错误、错误文案精确、原条目不被覆盖；
//   - Get 命中返回同一指针；未命中（空/非空注册表）返回 (nil, false)；
//   - List/Names/Len 按注册（插入）序确定性返回，且为副本（外部修改不影响内部）；
//   - 空注册表 List/Names/Len 返回空结果不 panic；
//   - ParseMetaAgentOutput：合法输出（纯 JSON / markdown 围栏 / 周围文本，含
//     systemPrompt 与执行结果字段）解析正确；非法输出返回 error 且错误文案
//     与实现逐字一致（no JSON / 非法 JSON / 缺 agent_design / 空 agent_name）；
//   - ExtractJSONObject：围栏剥离、周围文本、嵌套花括号原样保留、未闭合返回空串；
//   - 并发安全：多 goroutine 并发 Register/Get/List/Names 无 data race；
//     同 key 并发注册恰一个成功、其余 ErrAlreadyRegistered（防重复语义并发成立）。

package director

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// makeCustomAgent 构造测试用 CustomAgent（纯数据，无闭包/执行器引用）。
func makeCustomAgent(name, prompt string) *CustomAgent {
	return &CustomAgent{
		Name:         name,
		DisplayName:  name + " display",
		SystemPrompt: prompt,
		ToolsUsed:    []string{"read_file"},
		Description:  "test custom agent " + name,
	}
}

// ─── Register Tests ──────────────────────────────────────────────────────

// TestMetaAgentHandlerRegister 固化注册与防重复语义：
// 首次注册成功；重复注册返回包装 ErrAlreadyRegistered 的错误（文案与实现一致），
// 且原条目不被覆盖（与原 registerCustomAgent 行为逐字一致）。
func TestMetaAgentHandlerRegister(t *testing.T) {
	h := NewMetaAgentHandler()
	ca := makeCustomAgent("security_auditor", "You are a security auditor.")

	if err := h.Register(ca); err != nil {
		t.Fatalf("unexpected error on first register: %v", err)
	}
	if got := h.Len(); got != 1 {
		t.Fatalf("Len() = %d, want 1", got)
	}

	// 重复注册同一 Name：拒绝并保持原条目不变。
	dup := makeCustomAgent("security_auditor", "DIFFERENT prompt")
	err := h.Register(dup)
	if err == nil {
		t.Fatal("expected error on duplicate register")
	}
	if !errors.Is(err, ErrAlreadyRegistered) {
		t.Errorf("error should wrap ErrAlreadyRegistered, got %v", err)
	}
	if want := "custom agent already registered: delegate_security_auditor"; err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}

	got, ok := h.Get("delegate_security_auditor")
	if !ok || got != ca {
		t.Errorf("duplicate register should keep the original entry, got %+v (ok=%v)", got, ok)
	}
	if got != nil && got.SystemPrompt != "You are a security auditor." {
		t.Errorf("original SystemPrompt was overwritten, got %q", got.SystemPrompt)
	}
	if h.Len() != 1 {
		t.Errorf("Len() after duplicate register = %d, want 1", h.Len())
	}
}

// ─── Get Tests ───────────────────────────────────────────────────────────

// TestMetaAgentHandlerGet 固化查找语义：
// 命中返回同一指针；未命中（空注册表与非空注册表）返回 (nil, false)，不 panic。
func TestMetaAgentHandlerGet(t *testing.T) {
	h := NewMetaAgentHandler()

	if got, ok := h.Get("delegate_missing"); ok || got != nil {
		t.Errorf("Get on empty registry = (%+v, %v), want (nil, false)", got, ok)
	}

	ca := makeCustomAgent("data_export", "You are a data exporter.")
	if err := h.Register(ca); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, ok := h.Get("delegate_data_export")
	if !ok {
		t.Fatal("Get should hit the registered agent")
	}
	if got != ca {
		t.Errorf("Get should return the same pointer, got %+v", got)
	}
	if got.SystemPrompt != "You are a data exporter." {
		t.Errorf("SystemPrompt = %q, want %q", got.SystemPrompt, "You are a data exporter.")
	}

	if got, ok := h.Get("delegate_nothing"); ok || got != nil {
		t.Errorf("Get miss on non-empty registry = (%+v, %v), want (nil, false)", got, ok)
	}
}

// ─── List / Names Tests ──────────────────────────────────────────────────

// TestMetaAgentHandlerListOrder 固化插入序与副本语义：
// 乱序注册时 List/Names 按注册序确定性返回（对齐可控改进：原 map 随机序已消除）；
// 返回切片为副本，外部修改不影响内部状态。
func TestMetaAgentHandlerListOrder(t *testing.T) {
	h := NewMetaAgentHandler()
	agents := []*CustomAgent{
		makeCustomAgent("beta", "B"),
		makeCustomAgent("alpha", "A"),
		makeCustomAgent("gamma", "G"),
	}
	for _, ca := range agents {
		if err := h.Register(ca); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	wantNames := []string{"delegate_beta", "delegate_alpha", "delegate_gamma"}
	gotNames := h.Names()
	if len(gotNames) != len(wantNames) {
		t.Fatalf("Names() = %v, want %v", gotNames, wantNames)
	}
	for i := range wantNames {
		if gotNames[i] != wantNames[i] {
			t.Errorf("Names()[%d] = %q, want %q (insertion order)", i, gotNames[i], wantNames[i])
		}
	}

	gotList := h.List()
	if len(gotList) != 3 {
		t.Fatalf("List() length = %d, want 3", len(gotList))
	}
	for i := range agents {
		if gotList[i] != agents[i] {
			t.Errorf("List()[%d] should be the agent registered at position %d", i, i)
		}
	}

	// 副本语义：修改返回切片不得影响内部状态。
	gotNames[0] = "MUTATED"
	gotList[0] = nil
	if h.Names()[0] != "delegate_beta" {
		t.Error("Names() should return a copy; internal state was mutated")
	}
	if h.List()[0] != agents[0] {
		t.Error("List() should return a copy; internal state was mutated")
	}
}

// TestMetaAgentHandlerEmptyRegistry 固化空注册表行为：
// Len 为 0，List/Names 返回空结果（len 0）且不 panic。
func TestMetaAgentHandlerEmptyRegistry(t *testing.T) {
	h := NewMetaAgentHandler()
	if got := h.Len(); got != 0 {
		t.Errorf("Len() = %d, want 0", got)
	}
	if l := h.List(); len(l) != 0 {
		t.Errorf("List() on empty registry = %v, want empty", l)
	}
	if n := h.Names(); len(n) != 0 {
		t.Errorf("Names() on empty registry = %v, want empty", n)
	}
}

// ─── ParseMetaAgentOutput Tests ──────────────────────────────────────────

// TestParseMetaAgentOutputValid 固化合法输出解析：
// 纯 JSON / markdown 围栏（含与不含语言标签）/ 周围文本均能提取并解析；
// systemPrompt 等于 agent_design 字段，执行结果字段（AgentName/ToolsUsed/
// TaskForAgent/Thinking）逐字段校验。
func TestParseMetaAgentOutputValid(t *testing.T) {
	const validJSON = `{"thinking": "Designing agent for the task.", "agent_name": "Security Auditor", "agent_design": "You are a security auditor.", "tools_used": ["read_file", "search_by_regex"], "task_for_agent": "Audit the code."}`
	tests := []struct {
		name  string
		input string
	}{
		{"pure json", validJSON},
		{"markdown fence with language tag", "```json\n" + validJSON + "\n```"},
		{"markdown fence without language tag", "```\n" + validJSON + "\n```"},
		{"surrounding text", "Here is the design:\n" + validJSON + "\nDone."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prompt, res, err := ParseMetaAgentOutput(tc.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if prompt != "You are a security auditor." {
				t.Errorf("systemPrompt = %q, want %q", prompt, "You are a security auditor.")
			}
			if res == nil {
				t.Fatal("execResult should not be nil on success")
			}
			if res.AgentName != "Security Auditor" {
				t.Errorf("AgentName = %q, want %q", res.AgentName, "Security Auditor")
			}
			if res.AgentDesign != "You are a security auditor." {
				t.Errorf("AgentDesign = %q, want the agent_design content", res.AgentDesign)
			}
			if len(res.ToolsUsed) != 2 || res.ToolsUsed[0] != "read_file" || res.ToolsUsed[1] != "search_by_regex" {
				t.Errorf("ToolsUsed = %v, want [read_file search_by_regex]", res.ToolsUsed)
			}
			if res.TaskForAgent != "Audit the code." {
				t.Errorf("TaskForAgent = %q, want %q", res.TaskForAgent, "Audit the code.")
			}
			if res.Thinking != "Designing agent for the task." {
				t.Errorf("Thinking = %q, want %q", res.Thinking, "Designing agent for the task.")
			}
		})
	}
}

// TestParseMetaAgentOutputErrors 固化非法输出语义：
// 四类非法输出均返回 error，错误文案与实现逐字一致；
// 失败时 systemPrompt/execResult 为零值。
func TestParseMetaAgentOutputErrors(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantErr   string // 精确文案（无动态部分时使用）
		errPrefix string // 非空时改用前缀断言（%w 包装含动态部分）
	}{
		{
			name:    "no json object",
			input:   "Just some plain text without JSON.",
			wantErr: "no JSON object found in Meta-Agent output",
		},
		{
			name:      "invalid json",
			input:     `{"thinking": "test", "agent_name": "Test", "agent_design": "prompt", "tools_used": ["read_file"], "result": {not valid json}}`,
			errPrefix: "failed to parse Meta-Agent JSON:",
		},
		{
			name:    "missing agent_design",
			input:   `{"thinking": "designing...", "agent_name": "Test", "tools_used": ["read_file"]}`,
			wantErr: "agent_design is empty in Meta-Agent JSON",
		},
		{
			name:    "empty agent_name",
			input:   `{"thinking": "test", "agent_name": "", "agent_design": "Some prompt"}`,
			wantErr: "agent_name is empty in Meta-Agent JSON",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prompt, res, err := ParseMetaAgentOutput(tc.input)
			if err == nil {
				t.Fatalf("expected error, got nil (prompt=%q, res=%+v)", prompt, res)
			}
			if tc.errPrefix != "" {
				if !strings.HasPrefix(err.Error(), tc.errPrefix) {
					t.Errorf("error = %q, want prefix %q", err.Error(), tc.errPrefix)
				}
			} else if err.Error() != tc.wantErr {
				t.Errorf("error = %q, want %q", err.Error(), tc.wantErr)
			}
			if prompt != "" || res != nil {
				t.Errorf("on error systemPrompt/execResult should be zero, got %q / %+v", prompt, res)
			}
		})
	}
}

// ─── ExtractJSONObject Tests ─────────────────────────────────────────────

// TestExtractJSONObject 固化 JSON 提取行为（与门面层 director_test.go 同源锁定）：
// 纯 JSON 原样、围栏剥离（含语言标签）、周围文本剥离、嵌套花括号返回外层完整对象、
// 无花括号与未闭合花括号返回空串。
func TestExtractJSONObject(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"pure json", `{"key": "value"}`, `{"key": "value"}`},
		{"markdown fence with language tag", "```json\n{\"key\": \"value\"}\n```", `{"key": "value"}`},
		{"markdown fence plain", "```\n{\"key\": \"value\"}\n```", `{"key": "value"}`},
		{"surrounding text", `Here's the output: {"key": "value"} with trailing text.`, `{"key": "value"}`},
		{"nested braces", `{"key": {"nested": true}, "list": [1,2,3]}`, `{"key": {"nested": true}, "list": [1,2,3]}`},
		{"no braces", "Just plain text without any braces.", ""},
		{"unclosed brace", `{"key": "value"`, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractJSONObject(tc.input)
			if got != tc.expected {
				t.Errorf("ExtractJSONObject = %q, want %q", got, tc.expected)
			}
		})
	}
}

// ─── Concurrency Tests ───────────────────────────────────────────────────

// TestMetaAgentHandlerConcurrent 固化并发安全：
// 多 goroutine 并发注册唯一 name 全部成功，并发 Get/List/Names/Len 无 data race；
// 同 key 并发注册恰一个成功、其余返回 ErrAlreadyRegistered（防重复语义并发成立）。
func TestMetaAgentHandlerConcurrent(t *testing.T) {
	h := NewMetaAgentHandler()
	const workers = 50

	var wg sync.WaitGroup
	var success atomic.Int64
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("agent_%02d", i)
			if err := h.Register(makeCustomAgent(name, "prompt "+name)); err != nil {
				t.Errorf("unexpected error registering %q: %v", name, err)
				return
			}
			success.Add(1)
			// 并发读：Get/List/Names/Len 与写并发，验证无 data race。
			if got, ok := h.Get("delegate_" + name); !ok || got == nil {
				t.Errorf("Get(%q) after own register should hit", "delegate_"+name)
			}
			_ = h.Len()
			_ = h.Names()
			_ = h.List()
		}(i)
	}
	wg.Wait()

	if got := success.Load(); got != workers {
		t.Errorf("successful registers = %d, want %d", got, workers)
	}
	if got := h.Len(); got != workers {
		t.Errorf("Len() = %d, want %d", got, workers)
	}
	// 并发下插入序不确定，但集合必须确定：排序后与预期一致。
	names := append([]string(nil), h.Names()...)
	sort.Strings(names)
	if len(names) != workers {
		t.Fatalf("len(Names()) = %d, want %d", len(names), workers)
	}
	for i, n := range names {
		if want := fmt.Sprintf("delegate_agent_%02d", i); n != want {
			t.Errorf("sorted Names()[%d] = %q, want %q", i, n, want)
		}
	}

	// 同 key 并发注册：恰一个成功，其余 ErrAlreadyRegistered。
	h2 := NewMetaAgentHandler()
	var wg2 sync.WaitGroup
	var dupOK, dupErr atomic.Int64
	for i := 0; i < 20; i++ {
		wg2.Add(1)
		go func() {
			defer wg2.Done()
			if err := h2.Register(makeCustomAgent("same", "same prompt")); err != nil {
				if errors.Is(err, ErrAlreadyRegistered) {
					dupErr.Add(1)
				} else {
					t.Errorf("unexpected error type: %v", err)
				}
				return
			}
			dupOK.Add(1)
		}()
	}
	wg2.Wait()
	if got := dupOK.Load(); got != 1 {
		t.Errorf("same-key concurrent register: ok = %d, want 1", got)
	}
	if got := dupErr.Load(); got != 19 {
		t.Errorf("same-key concurrent register: err = %d, want 19", got)
	}
	if got, ok := h2.Get("delegate_same"); !ok || got == nil {
		t.Error("winner entry should remain registered")
	}
}
