package agents

// director_characterization_test.go — P0-1 Phase 0 characterization 测试（回归防线资产）。
//
// 目的：固化 internal/agents/director.go 与 internal/agents/director/ 子包的当前行为，
// 作为后续拆分搬迁逻辑时的回归防线。断言只锁定"关键特征"（输入→输出的关键不变量），
// 不与实现细节强耦合，避免后续合法重构误报。所有用例稳定可重复（无随机、无真实时间依赖）。
//
// 覆盖四组行为：
//  1. 压缩行为：applyEmergencyCompression / applyUltimateCompression
//  2. 恢复行为：director.RecoveryHandler 熔断语义、ComputeBackoff、RetryWithBackoff
//  3. Meta 解析：parseMetaAgentOutput
//  4. 项目上下文加载缓存：loadProjectContext

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	director "yagent/internal/agents/director"
	"yagent/internal/config"
	"yagent/internal/llm"
	"yagent/internal/memory"
)

// ─── 局部 helper ─────────────────────────────────────────────────────────────

// newCharDirectorAgent 构造一个带自定义 mockEngine 的 DirectorAgent（子 agent 全 nil）。
func newCharDirectorAgent(t *testing.T, engine llm.Engine, workDir string) *DirectorAgent {
	t.Helper()
	gctx := newTestGlobalCtx(workDir)
	return newDirectorAgentForTest(gctx, engine, nil, nil, nil, nil, nil, nil, 10, nil, 3, config.Config{}, nil)
}

// charAssistantMessages 构造 n 条 assistant 文本消息（无 Thought & Plan 关键字，
// 紧急压缩时每条消息整体作为一个过程块）。
func charAssistantMessages(n int, prefix string) []llm.Message {
	msgs := make([]llm.Message, 0, n)
	for i := 0; i < n; i++ {
		msgs = append(msgs, llm.Message{
			Role:    llm.RoleAssistant,
			Content: prefix + " " + string(rune('A'+i)),
		})
	}
	return msgs
}

// charWriteFile 在临时目录写入文件，失败即 Fatal。
func charWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// ─── 组 1a：紧急压缩 applyEmergencyCompression ───────────────────────────────

// TestCharacterizationApplyEmergencyCompression 固化紧急压缩行为：
//   - 块数 <= DefaultEmergencyCompressKeepLastN(3) 时全部保留且不调用 LLM；
//   - 块数 > 3 时超出部分交给 LLM 总结（保留最近 3 个块），LLM 失败则降级为截断拼接；
//   - 输出 messages 保留首条（system 原样）并追加单条 user 消息；
//   - 原始任务取自 currentMemory 的第一条 human 消息（优先于消息列表中的 user 消息）；
//   - 覆盖 currentMemory 为单条 human 消息，内容与输出末条 user 一致。
func TestCharacterizationApplyEmergencyCompression(t *testing.T) {
	const (
		systemPrompt  = "SYSTEM_PROMPT_KEEP"
		originalTask  = "原始任务XYZ"
		inlineUserMsg = "USER_TASK_INLINE"
		summaryText   = "MOCK_SUMMARY_TEXT"
	)

	tests := []struct {
		name                 string
		assistantCount       int
		llmErr               bool // mock LLM 是否返回错误
		wantLLMCalls         int
		wantSummarizedByLLM  bool
		wantSummarizedBlocks int
		wantKeptBlocks       int
		wantContentContains  []string
		wantContentExcludes  []string
	}{
		{
			name:           "few blocks keeps all without LLM",
			assistantCount: 2,
			wantLLMCalls:   0,
			wantKeptBlocks: 2,
			wantContentContains: []string{
				"### 原始任务",
				originalTask,
				"assistant block A",
				"assistant block B",
			},
		},
		{
			name:                 "many blocks summarized by LLM keeping last N",
			assistantCount:       4,
			wantLLMCalls:         1,
			wantSummarizedByLLM:  true,
			wantSummarizedBlocks: 1,
			wantKeptBlocks:       3,
			wantContentContains: []string{
				"### 原始任务",
				originalTask,
				summaryText,
				"assistant block B",
				"assistant block C",
				"assistant block D",
			},
			wantContentExcludes: []string{
				"assistant block A", // 最早的块被 LLM 总结替换，不出现在保留区
			},
		},
		{
			name:                 "LLM failure falls back to truncation",
			assistantCount:       4,
			llmErr:               true,
			wantLLMCalls:         1,
			wantSummarizedByLLM:  false,
			wantSummarizedBlocks: 1,
			wantKeptBlocks:       3,
			wantContentContains: []string{
				"### 原始任务",
				originalTask,
				"assistant block A", // 降级时 beforeBlocks 以原文拼接保留
				"assistant block B",
				"assistant block D",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			llmCalls := 0
			engine := &mockEngine{
				generateContent: func(ctx context.Context, messages []llm.Message, tools []llm.ToolDef, opts *llm.CallOptions) (*llm.Response, error) {
					llmCalls++
					if tt.llmErr {
						return nil, errors.New("forced llm failure for characterization test")
					}
					return &llm.Response{Choices: []llm.Choice{{Content: summaryText}}}, nil
				},
			}

			agent := newCharDirectorAgent(t, engine, t.TempDir())
			agent.currentMemory = memory.NewConversationMemory(10)
			agent.currentMemory.AddHumanMessage(originalTask)

			msgs := []llm.Message{
				{Role: llm.RoleSystem, Content: systemPrompt},
				{Role: llm.RoleUser, Content: inlineUserMsg},
			}
			msgs = append(msgs, charAssistantMessages(tt.assistantCount, "assistant block")...)

			out, stats := agent.applyEmergencyCompression(context.Background(), msgs, 100000)

			if llmCalls != tt.wantLLMCalls {
				t.Errorf("LLM calls = %d, want %d", llmCalls, tt.wantLLMCalls)
			}

			// 结构关键特征：保留首条 system（原样）+ 追加单条 user
			if len(out) != 2 {
				t.Fatalf("len(out) = %d, want 2", len(out))
			}
			if out[0].Role != llm.RoleSystem || out[0].Content != systemPrompt {
				t.Errorf("out[0] should preserve first system message, got role=%q content=%q", out[0].Role, out[0].Content)
			}
			if out[1].Role != llm.RoleUser {
				t.Errorf("out[1].Role = %q, want %q", out[1].Role, llm.RoleUser)
			}

			// 内容关键特征
			for _, sub := range tt.wantContentContains {
				if !strings.Contains(out[1].Content, sub) {
					t.Errorf("compressed user content should contain %q", sub)
				}
			}
			for _, sub := range tt.wantContentExcludes {
				if strings.Contains(out[1].Content, sub) {
					t.Errorf("compressed user content should not contain %q", sub)
				}
			}

			// stats 关键特征
			if stats.OriginalTokens <= 0 {
				t.Errorf("stats.OriginalTokens = %d, want > 0", stats.OriginalTokens)
			}
			if stats.CompressedTokens <= 0 {
				t.Errorf("stats.CompressedTokens = %d, want > 0", stats.CompressedTokens)
			}
			if stats.SavedTokens < 0 {
				t.Errorf("stats.SavedTokens = %d, want >= 0", stats.SavedTokens)
			}
			if stats.ExtractedBlocks != tt.assistantCount {
				t.Errorf("stats.ExtractedBlocks = %d, want %d (each assistant msg yields one block)", stats.ExtractedBlocks, tt.assistantCount)
			}
			if stats.SummarizedByLLM != tt.wantSummarizedByLLM {
				t.Errorf("stats.SummarizedByLLM = %v, want %v", stats.SummarizedByLLM, tt.wantSummarizedByLLM)
			}
			if stats.SummarizedBlocks != tt.wantSummarizedBlocks {
				t.Errorf("stats.SummarizedBlocks = %d, want %d", stats.SummarizedBlocks, tt.wantSummarizedBlocks)
			}
			if stats.KeptBlocks != tt.wantKeptBlocks {
				t.Errorf("stats.KeptBlocks = %d, want %d", stats.KeptBlocks, tt.wantKeptBlocks)
			}

			// memory 覆盖：清空后只剩一条 human 消息，内容与输出末条 user 一致
			if len(agent.currentMemory.Messages) != 1 {
				t.Fatalf("currentMemory should be overwritten to a single message, got %d", len(agent.currentMemory.Messages))
			}
			m := agent.currentMemory.Messages[0]
			if m.Type != memory.MessageTypeHuman {
				t.Errorf("memory message type = %q, want %q", m.Type, memory.MessageTypeHuman)
			}
			if m.Content != out[1].Content {
				t.Errorf("memory message content should equal out[1].Content")
			}
		})
	}
}

// ─── 组 1b：终极压缩 applyUltimateCompression ────────────────────────────────

// TestCharacterizationApplyUltimateCompression 固化终极压缩行为（不依赖 LLM，
// 由 thinklink Store 重建上下文）：
//   - system 消息原样保留，其余消息重置为单条 user 重建消息；
//   - 预算充足时保留全部 T&P 块（keep == totalPlans），含原始任务与 [CURRENT TASK] 标记；
//   - 预算极小时循环递减保留数量直至硬截断兜底（truncated=true，keep=0）；
//   - 清理 pendingSubAgentMemory 残留；覆盖 currentMemory 为单条 human 消息。
func TestCharacterizationApplyUltimateCompression(t *testing.T) {
	const (
		systemContent = "SYSTEM_PROMPT_KEEP"
		userTask      = "原始任务U"
	)

	buildAgent := func(t *testing.T) *DirectorAgent {
		t.Helper()
		agent := newCharDirectorAgent(t, &mockEngine{}, t.TempDir())
		agent.EnhancedCommanderCfg.UltimateCompressionKeepPlans = 0 // 0 = 不限制保留数量
		agent.pendingSubAgentMemory = &AgentResult{Text: "stale sub-agent result"}
		agent.currentMemory = memory.NewConversationMemory(10)
		agent.currentMemory.AddHumanMessage("old stale history")
		agent.thinkLink.AddUserInput(userTask, 1)
		agent.thinkLink.AddThoughtPlan("PLAN_ONE", 2)
		agent.thinkLink.AddThoughtPlan("PLAN_TWO", 3)
		return agent
	}

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: systemContent},
		{Role: llm.RoleUser, Content: "stale user message"},
		{Role: llm.RoleAssistant, Content: "stale assistant message"},
	}

	tests := []struct {
		name          string
		threshold     int
		wantTruncated bool
		wantKeptPlans int
		wantContains  []string
	}{
		{
			name:          "under budget keeps all plans",
			threshold:     100000,
			wantTruncated: false,
			wantKeptPlans: 2,
			wantContains: []string{
				"=== Original user input(s) ===",
				"[CURRENT TASK]",
				userTask,
				"PLAN_ONE",
				"PLAN_TWO",
			},
		},
		{
			name:          "tiny budget decrements plans and hard-truncates",
			threshold:     10, // 远小于重建提示的固有体积 → 递减到 0 后硬截断兜底
			wantTruncated: true,
			wantKeptPlans: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := buildAgent(t)
			out, stats := agent.applyUltimateCompression(messages, tt.threshold)

			// 结构关键特征：system 原样保留 + 单条 user 重建消息
			if len(out) != 2 {
				t.Fatalf("len(out) = %d, want 2", len(out))
			}
			if out[0].Role != llm.RoleSystem || out[0].Content != systemContent {
				t.Errorf("out[0] should preserve system message, got role=%q content=%q", out[0].Role, out[0].Content)
			}
			if out[1].Role != llm.RoleUser {
				t.Errorf("out[1].Role = %q, want %q", out[1].Role, llm.RoleUser)
			}
			if strings.TrimSpace(out[1].Content) == "" {
				t.Errorf("rebuilt user content should not be empty (truncated=%v)", stats.Truncated)
			}

			// 内容关键特征
			for _, sub := range tt.wantContains {
				if !strings.Contains(out[1].Content, sub) {
					t.Errorf("rebuilt user content should contain %q", sub)
				}
			}

			// stats 关键特征
			if stats.Truncated != tt.wantTruncated {
				t.Errorf("stats.Truncated = %v, want %v", stats.Truncated, tt.wantTruncated)
			}
			if stats.KeptPlans != tt.wantKeptPlans {
				t.Errorf("stats.KeptPlans = %d, want %d", stats.KeptPlans, tt.wantKeptPlans)
			}
			if stats.TotalPlans != 2 {
				t.Errorf("stats.TotalPlans = %d, want 2", stats.TotalPlans)
			}
			if stats.UserInputs != 1 {
				t.Errorf("stats.UserInputs = %d, want 1", stats.UserInputs)
			}
			if stats.OriginalTokens <= 0 || stats.CompressedTokens <= 0 {
				t.Errorf("stats tokens invalid: original=%d compressed=%d", stats.OriginalTokens, stats.CompressedTokens)
			}
			if stats.SavedTokens < 0 {
				t.Errorf("stats.SavedTokens = %d, want >= 0", stats.SavedTokens)
			}

			// 残留清理：pendingSubAgentMemory 必须被清为 nil
			if agent.pendingSubAgentMemory != nil {
				t.Errorf("pendingSubAgentMemory should be cleared to nil after ultimate compression")
			}

			// memory 覆盖：只剩一条 human 消息，内容与重建消息一致
			if len(agent.currentMemory.Messages) != 1 {
				t.Fatalf("currentMemory should be overwritten to a single message, got %d", len(agent.currentMemory.Messages))
			}
			m := agent.currentMemory.Messages[0]
			if m.Type != memory.MessageTypeHuman {
				t.Errorf("memory message type = %q, want %q", m.Type, memory.MessageTypeHuman)
			}
			if m.Content != out[1].Content {
				t.Errorf("memory message content should equal out[1].Content")
			}
		})
	}
}

// ─── 组 2：恢复行为（director.RecoveryHandler）────────────────────────────────

// TestCharacterizationDefaultRecoveryConfig 固化默认恢复配置：
// MaxRetries=3、CircuitBreakerThreshold=5（默认熔断阈值）、ResetTimeout=30s。
func TestCharacterizationDefaultRecoveryConfig(t *testing.T) {
	cfg := director.DefaultRecoveryConfig()
	if cfg.MaxRetries != 3 {
		t.Errorf("DefaultRecoveryConfig().MaxRetries = %d, want 3", cfg.MaxRetries)
	}
	if cfg.CircuitBreakerThreshold != 5 {
		t.Errorf("DefaultRecoveryConfig().CircuitBreakerThreshold = %d, want 5", cfg.CircuitBreakerThreshold)
	}
	if cfg.CircuitBreakerResetTimeout != 30*time.Second {
		t.Errorf("DefaultRecoveryConfig().CircuitBreakerResetTimeout = %v, want 30s", cfg.CircuitBreakerResetTimeout)
	}
}

// TestCharacterizationRecoveryCircuitBreaker 固化熔断语义：
//   - 初始 closed → IsCircuitBreakerOpen()==false；
//   - 连续失败达到 threshold 后 open → IsCircuitBreakerOpen()==true；
//   - Success 关闭熔断并清零失败计数；
//   - 超过 ResetTimeout 后放行试运行（half-open）；试运行失败重新打开，试运行成功恢复 closed；
//   - threshold=0 时熔断器不启用，永不打开；
//   - Reset 重置到 closed。
func TestCharacterizationRecoveryCircuitBreaker(t *testing.T) {
	t.Run("opens after threshold consecutive failures", func(t *testing.T) {
		r := director.NewRecoveryHandler(director.RecoveryConfig{
			MaxRetries:                 3,
			CircuitBreakerThreshold:    2,
			CircuitBreakerResetTimeout: time.Hour, // 测试期间不会超时转 half-open
		})
		if r.IsCircuitBreakerOpen() {
			t.Fatalf("initial state should allow requests")
		}
		r.RecordLLMFailure()
		if r.IsCircuitBreakerOpen() {
			t.Fatalf("should stay closed after 1 failure (< threshold 2)")
		}
		r.RecordLLMFailure()
		if !r.IsCircuitBreakerOpen() {
			t.Fatalf("should be open after 2 consecutive failures (== threshold 2)")
		}
		r.RecordLLMSuccess()
		if r.IsCircuitBreakerOpen() {
			t.Fatalf("success should close an open breaker")
		}
		// 成功清零失败计数：随后一次失败不足以重新打开
		r.RecordLLMFailure()
		if r.IsCircuitBreakerOpen() {
			t.Fatalf("failure count should have been reset by success")
		}
	})

	t.Run("half-open trial after reset timeout", func(t *testing.T) {
		r := director.NewRecoveryHandler(director.RecoveryConfig{
			MaxRetries:                 1,
			CircuitBreakerThreshold:    1,
			CircuitBreakerResetTimeout: 2 * time.Millisecond,
		})
		r.RecordLLMFailure()
		if !r.IsCircuitBreakerOpen() {
			t.Fatalf("should be open right after failure")
		}
		time.Sleep(5 * time.Millisecond) // 超过 ResetTimeout，确定性触发 half-open
		if r.IsCircuitBreakerOpen() {
			t.Fatalf("half-open state should allow a trial request")
		}
		// 试运行失败 → 重新打开
		r.RecordLLMFailure()
		if !r.IsCircuitBreakerOpen() {
			t.Fatalf("failed half-open trial should re-open the breaker")
		}
		// 再次超过 ResetTimeout 并试运行成功 → 恢复 closed
		time.Sleep(5 * time.Millisecond)
		_ = r.IsCircuitBreakerOpen() // 触发 open→half-open 转换（Allow 的副作用）
		r.RecordLLMSuccess()
		if r.IsCircuitBreakerOpen() {
			t.Fatalf("successful half-open trial should close the breaker")
		}
		// 注："成功清零失败计数"语义已在 threshold=2 的子测试中固化；
		// threshold=1 时再一次失败即达到阈值重新打开，无法在此验证。
	})

	t.Run("disabled when threshold is zero", func(t *testing.T) {
		r := director.NewRecoveryHandler(director.RecoveryConfig{})
		for i := 0; i < 10; i++ {
			r.RecordLLMFailure()
		}
		if r.IsCircuitBreakerOpen() {
			t.Fatalf("breaker with threshold 0 must never open")
		}
	})

	t.Run("reset returns to closed", func(t *testing.T) {
		r := director.NewRecoveryHandler(director.RecoveryConfig{
			CircuitBreakerThreshold:    1,
			CircuitBreakerResetTimeout: time.Hour,
		})
		r.RecordLLMFailure()
		if !r.IsCircuitBreakerOpen() {
			t.Fatalf("should be open after failure (threshold 1)")
		}
		r.Reset()
		if r.IsCircuitBreakerOpen() {
			t.Fatalf("Reset should return breaker to closed state")
		}
	})
}

// TestCharacterizationComputeBackoff 固化指数退避序列：
// 2^attempt 秒，上限 30 秒封顶。
func TestCharacterizationComputeBackoff(t *testing.T) {
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 1 * time.Second},
		{1, 2 * time.Second},
		{2, 4 * time.Second},
		{3, 8 * time.Second},
		{4, 16 * time.Second},
		{5, 30 * time.Second}, // 封顶
		{10, 30 * time.Second},
	}
	for _, tt := range tests {
		if got := director.ComputeBackoff(tt.attempt); got != tt.want {
			t.Errorf("ComputeBackoff(%d) = %v, want %v", tt.attempt, got, tt.want)
		}
	}
}

// TestCharacterizationRetryWithBackoff 固化重试语义：
//   - 首次成功立即返回 nil 且只调用一次；
//   - maxRetries=0 时失败不重试（无 sleep）；
//   - ctx 已取消时在重试等待处返回 ctx.Err()。
func TestCharacterizationRetryWithBackoff(t *testing.T) {
	t.Run("returns nil on first success", func(t *testing.T) {
		calls := 0
		err := director.RetryWithBackoff(context.Background(), 3, func(attempt int) error {
			calls++
			return nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if calls != 1 {
			t.Errorf("calls = %d, want 1 (no retry after success)", calls)
		}
	})

	t.Run("no retry when maxRetries is zero", func(t *testing.T) {
		calls := 0
		err := director.RetryWithBackoff(context.Background(), 0, func(attempt int) error {
			calls++
			return errors.New("boom")
		})
		if err == nil {
			t.Fatalf("expected error when all attempts fail")
		}
		if calls != 1 {
			t.Errorf("calls = %d, want 1", calls)
		}
	})

	t.Run("returns ctx.Err on cancelled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		calls := 0
		err := director.RetryWithBackoff(ctx, 2, func(attempt int) error {
			calls++
			return errors.New("boom")
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if calls != 1 {
			t.Errorf("calls = %d, want 1 (cancelled before first retry)", calls)
		}
	})
}

// ─── 组 3：Meta-Agent 输出解析 parseMetaAgentOutput ──────────────────────────

// TestCharacterizationParseMetaAgentOutput 固化解析行为：
//   - 支持纯 JSON、markdown code fence 包裹、前后文本包围三种形态；
//   - systemPrompt 返回值等于 JSON 的 agent_design 字段；
//   - 无 JSON / 非法 JSON / 缺 agent_name / 缺 agent_design 均返回 error 且
//     systemPrompt 为空、execResult 为 nil。
func TestCharacterizationParseMetaAgentOutput(t *testing.T) {
	validJSON := `{"thinking":"plan","agent_name":"custom_helper","agent_design":"You are a helper.","tools_used":["read_file"],"task_for_agent":"do stuff"}`

	tests := []struct {
		name            string
		input           string
		wantErr         bool
		wantErrContains string
		wantPrompt      string
		wantAgentName   string
	}{
		{
			name:          "pure json object",
			input:         validJSON,
			wantPrompt:    "You are a helper.",
			wantAgentName: "custom_helper",
		},
		{
			name:          "markdown fenced json",
			input:         "```json\n" + validJSON + "\n```",
			wantPrompt:    "You are a helper.",
			wantAgentName: "custom_helper",
		},
		{
			name:          "json with surrounding text",
			input:         "Sure! Here is the design:\n" + validJSON + "\nDone.",
			wantPrompt:    "You are a helper.",
			wantAgentName: "custom_helper",
		},
		{
			name:            "no json at all",
			input:           "plain text without any braces",
			wantErr:         true,
			wantErrContains: "no JSON object found",
		},
		{
			name:            "malformed json body",
			input:           `{"bad": }`,
			wantErr:         true,
			wantErrContains: "failed to parse Meta-Agent JSON",
		},
		{
			name:            "missing agent_name",
			input:           `{"agent_design":"You are a helper."}`,
			wantErr:         true,
			wantErrContains: "agent_name is empty",
		},
		{
			name:            "missing agent_design",
			input:           `{"agent_name":"custom_helper"}`,
			wantErr:         true,
			wantErrContains: "agent_design is empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prompt, execResult, err := director.ParseMetaAgentOutput(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (prompt=%q)", prompt)
				}
				if tt.wantErrContains != "" && !strings.Contains(err.Error(), tt.wantErrContains) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.wantErrContains)
				}
				if execResult != nil {
					t.Errorf("execResult should be nil on error, got %+v", execResult)
				}
				if prompt != "" {
					t.Errorf("systemPrompt should be empty on error, got %q", prompt)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if prompt != tt.wantPrompt {
				t.Errorf("systemPrompt = %q, want %q", prompt, tt.wantPrompt)
			}
			if execResult == nil {
				t.Fatalf("execResult should not be nil on success")
			}
			if execResult.AgentName != tt.wantAgentName {
				t.Errorf("execResult.AgentName = %q, want %q", execResult.AgentName, tt.wantAgentName)
			}
			if execResult.AgentDesign != tt.wantPrompt {
				t.Errorf("execResult.AgentDesign = %q, want %q (systemPrompt mirrors AgentDesign)", execResult.AgentDesign, tt.wantPrompt)
			}
		})
	}
}

// ─── 组 4：项目上下文加载与缓存 loadProjectContext ───────────────────────────

// TestCharacterizationLoadProjectContextCache 固化项目上下文加载与缓存行为：
//   - 按序尝试读取 YAGENT.md、CLAUDE.md、AGENTS.md，不存在的文件忽略；
//   - Content 按序拼接为 "### <file>\n```\n<content>\n```\n" 格式；
//   - 同一实例二次调用命中缓存（返回同一指针），即使文件系统已变化；
//   - 无任何文件时返回空结果，且空结果同样被缓存。
func TestCharacterizationLoadProjectContextCache(t *testing.T) {
	t.Run("loads existing files in priority order", func(t *testing.T) {
		dir := t.TempDir()
		charWriteFile(t, filepath.Join(dir, "YAGENT.md"), "YAGENT-CONTENT")
		charWriteFile(t, filepath.Join(dir, "AGENTS.md"), "AGENTS-CONTENT")

		agent := newCharDirectorAgent(t, &mockEngine{}, dir)
		res := agent.loadProjectContext()

		if len(res.LoadedFiles) != 2 {
			t.Fatalf("len(LoadedFiles) = %d, want 2", len(res.LoadedFiles))
		}
		if res.LoadedFiles[0].FileName != "YAGENT.md" || res.LoadedFiles[0].Content != "YAGENT-CONTENT" {
			t.Errorf("LoadedFiles[0] = %+v, want YAGENT.md with its content", res.LoadedFiles[0])
		}
		if res.LoadedFiles[1].FileName != "AGENTS.md" || res.LoadedFiles[1].Content != "AGENTS-CONTENT" {
			t.Errorf("LoadedFiles[1] = %+v, want AGENTS.md with its content", res.LoadedFiles[1])
		}
		if !strings.Contains(res.Content, "### YAGENT.md") || !strings.Contains(res.Content, "YAGENT-CONTENT") {
			t.Errorf("Content should be formatted with file headings, got %q", res.Content)
		}
	})

	t.Run("second call hits cache even if files change", func(t *testing.T) {
		dir := t.TempDir()
		charWriteFile(t, filepath.Join(dir, "YAGENT.md"), "YAGENT-CONTENT")

		agent := newCharDirectorAgent(t, &mockEngine{}, dir)
		first := agent.loadProjectContext()

		// 缓存命中后修改文件系统：新增文件不应出现在二次结果中
		charWriteFile(t, filepath.Join(dir, "CLAUDE.md"), "CLAUDE-CONTENT")
		second := agent.loadProjectContext()

		if first != second {
			t.Errorf("second call should return the cached result (same pointer)")
		}
		if len(second.LoadedFiles) != 1 {
			t.Errorf("cache should prevent re-reading, LoadedFiles = %d, want 1", len(second.LoadedFiles))
		}
	})

	t.Run("empty dir yields empty result which is also cached", func(t *testing.T) {
		agent := newCharDirectorAgent(t, &mockEngine{}, t.TempDir())
		first := agent.loadProjectContext()
		if len(first.LoadedFiles) != 0 {
			t.Errorf("LoadedFiles = %d, want 0 for empty dir", len(first.LoadedFiles))
		}
		if first.Content != "" {
			t.Errorf("Content = %q, want empty for empty dir", first.Content)
		}
		second := agent.loadProjectContext()
		if first != second {
			t.Errorf("empty result should also be cached (same pointer)")
		}
	})
}
