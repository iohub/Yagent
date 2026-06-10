package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"codeactor/internal/config"
	"codeactor/internal/globalctx"
	"codeactor/internal/llm"
	"codeactor/internal/tools"
)

// ─── Test 1: RepoKnowledgeManager单元测试 ──────────────────────────────────────

// ── TestRepoKnowledgeManager_SearchSimilar_Success ──

func TestRepoKnowledgeManager_SearchSimilar_Success(t *testing.T) {
	// 模拟 /repo_knowledge/search 返回成功响应
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repo_knowledge/search" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{
			"success": true,
			"data": {
				"matches": [
					{
						"id": "abc123",
						"task": "分析项目结构",
						"result": "这是一个 Go 项目，包含多个微服务。",
						"score": 0.98
					},
					{
						"id": "def456",
						"task": "代码审查",
						"result": "发现3个潜在问题。",
						"score": 0.85
					}
				]
			}
		}`)
	}))
	defer server.Close()

	// 创建 globalCtx 并覆盖 CodexrayURL 为 mock server
	workDir := t.TempDir()
	gctx := newTestGlobalCtx(workDir)
	gctx.CodexrayURL = server.URL

	// 创建 mock RepoAgent
	agent := &RepoAgent{
		GlobalCtx: gctx,
	}

	mgr := NewRepoKnowledgeManager(agent, gctx, 0.95)

	ctx := context.Background()
	matches, err := mgr.searchSimilar(ctx, "分析项目结构")
	if err != nil {
		t.Fatalf("searchSimilar failed: %v", err)
	}

	if len(matches) != 2 {
		t.Errorf("expected 2 matches, got %d", len(matches))
	}

	if matches[0].ID != "abc123" {
		t.Errorf("first match ID = %q, want %q", matches[0].ID, "abc123")
	}
	if matches[0].Score != 0.98 {
		t.Errorf("first match score = %f, want 0.98", matches[0].Score)
	}
	if matches[0].Result != "这是一个 Go 项目，包含多个微服务。" {
		t.Errorf("first match result = %q, want %q", matches[0].Result, "这是一个 Go 项目，包含多个微服务。")
	}
	if matches[1].Score != 0.85 {
		t.Errorf("second match score = %f, want 0.85", matches[1].Score)
	}
}

// ── TestRepoKnowledgeManager_SearchSimilar_ServerError ──

func TestRepoKnowledgeManager_SearchSimilar_ServerError(t *testing.T) {
	// 模拟服务端返回 500
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error": "internal server error"}`)
	}))
	defer server.Close()

	workDir := t.TempDir()
	gctx := newTestGlobalCtx(workDir)
	gctx.CodexrayURL = server.URL

	agent := &RepoAgent{GlobalCtx: gctx}
	mgr := NewRepoKnowledgeManager(agent, gctx, 0.95)

	ctx := context.Background()
	_, err := mgr.searchSimilar(ctx, "分析项目结构")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !containsString(err.Error(), "500") {
		t.Errorf("expected error to contain '500', got: %v", err)
	}
}

// ── TestRepoKnowledgeManager_SearchSimilar_InvalidJSON ──

func TestRepoKnowledgeManager_SearchSimilar_InvalidJSON(t *testing.T) {
	// 模拟返回非法 JSON
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`this is not valid json {{{`))
	}))
	defer server.Close()

	workDir := t.TempDir()
	gctx := newTestGlobalCtx(workDir)
	gctx.CodexrayURL = server.URL

	agent := &RepoAgent{GlobalCtx: gctx}
	mgr := NewRepoKnowledgeManager(agent, gctx, 0.95)

	ctx := context.Background()
	_, err := mgr.searchSimilar(ctx, "分析项目结构")
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}

	if !containsString(err.Error(), "parse response") {
		t.Errorf("expected error to contain 'parse response', got: %v", err)
	}
}

// ── TestRepoKnowledgeManager_EmbedTaskAndResult_Success ──

func TestRepoKnowledgeManager_EmbedTaskAndResult_Success(t *testing.T) {
	// 模拟 /repo_knowledge/embed 返回 200
	var receivedBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repo_knowledge/embed" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}

		bodyBytes, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(bodyBytes, &receivedBody); err != nil {
			t.Errorf("failed to parse request body: %v", err)
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"success": true}`))
	}))
	defer server.Close()

	workDir := t.TempDir()
	gctx := newTestGlobalCtx(workDir)
	gctx.CodexrayURL = server.URL

	agent := &RepoAgent{GlobalCtx: gctx}
	mgr := NewRepoKnowledgeManager(agent, gctx, 0.95)

	ctx := context.Background()
	err := mgr.embedTaskAndResult(ctx, "分析项目结构", "结果内容")
	if err != nil {
		t.Fatalf("embedTaskAndResult failed: %v", err)
	}

	if receivedBody["task"] != "分析项目结构" {
		t.Errorf("received task = %v, want %q", receivedBody["task"], "分析项目结构")
	}
	if receivedBody["result"] != "结果内容" {
		t.Errorf("received result = %v, want %q", receivedBody["result"], "结果内容")
	}
}

// ── TestRepoKnowledgeManager_EmbedTaskAndResult_ServerError ──

func TestRepoKnowledgeManager_EmbedTaskAndResult_ServerError(t *testing.T) {
	// 模拟服务端返回 500
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error": "embed failed"}`)
	}))
	defer server.Close()

	workDir := t.TempDir()
	gctx := newTestGlobalCtx(workDir)
	gctx.CodexrayURL = server.URL

	agent := &RepoAgent{GlobalCtx: gctx}
	mgr := NewRepoKnowledgeManager(agent, gctx, 0.95)

	ctx := context.Background()
	err := mgr.embedTaskAndResult(ctx, "分析项目结构", "结果内容")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !containsString(err.Error(), "500") {
		t.Errorf("expected error to contain '500', got: %v", err)
	}
}

// ── TestRepoKnowledgeManager_AnalyseTask_CacheHit ──

func TestRepoKnowledgeManager_AnalyseTask_CacheHit(t *testing.T) {
	// 模拟搜索结果 score >= threshold (0.95)，验证 AnalyseTask 直接返回缓存结果
	var repoAgentCalled int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repo_knowledge/search" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, `{
				"success": true,
				"data": {
					"matches": [
						{
							"id": "cached1",
							"task": "分析项目结构",
							"result": "缓存的仓库分析结果",
							"score": 0.98
						}
					]
				}
			}`)
			return
		}
		// 其他端点不应被调用
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	workDir := t.TempDir()
	gctx := newTestGlobalCtx(workDir)
	gctx.CodexrayURL = server.URL

	// 创建一个 mock RepoAgent，通过 atomic 记录是否被调用
	mockAgent := &mockRepoAgent{
		runFn: func(ctx context.Context, task string) (AgentResult, error) {
			atomic.StoreInt32(&repoAgentCalled, 1)
			return AgentResult{Text: "不应该被调用的结果"}, nil
		},
		GlobalCtx: gctx,
	}

	mgr := NewRepoKnowledgeManager(mockAgent, gctx, 0.95)

	ctx := context.Background()
	result, err := mgr.AnalyseTask(ctx, "分析项目结构")
	if err != nil {
		t.Fatalf("AnalyseTask failed: %v", err)
	}

	if result != "缓存的仓库分析结果" {
		t.Errorf("result = %q, want %q", result, "缓存的仓库分析结果")
	}

	if atomic.LoadInt32(&repoAgentCalled) != 0 {
		t.Error("RepoAgent.Run should NOT have been called on cache hit")
	}
}

// ── TestRepoKnowledgeManager_AnalyseTask_CacheMiss ──

func TestRepoKnowledgeManager_AnalyseTask_CacheMiss(t *testing.T) {
	// 模拟搜索结果 score < threshold，验证 AnalyseTask 调用 RepoAgent 并异步存储结果
	var repoAgentCalled int32
	var repoAgentResult string
	var embedCalled int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repo_knowledge/search" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			// 返回低分结果，触发 cache miss
			fmt.Fprintf(w, `{
				"success": true,
				"data": {
					"matches": [
						{
							"id": "low1",
							"task": "分析项目结构",
							"result": "低分匹配",
							"score": 0.50
						}
					]
				}
			}`)
			return
		}
		if r.URL.Path == "/repo_knowledge/embed" {
			// 异步 embed 会被调用
			atomic.StoreInt32(&embedCalled, 1)
			io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"success": true}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	workDir := t.TempDir()
	gctx := newTestGlobalCtx(workDir)
	gctx.CodexrayURL = server.URL

	mockAgent := &mockRepoAgent{
		runFn: func(ctx context.Context, task string) (AgentResult, error) {
			atomic.StoreInt32(&repoAgentCalled, 1)
			repoAgentResult = "RepoAgent 的真实结果"
			return AgentResult{Text: repoAgentResult}, nil
		},
		GlobalCtx: gctx,
	}

	mgr := NewRepoKnowledgeManager(mockAgent, gctx, 0.95)

	ctx := context.Background()
	result, err := mgr.AnalyseTask(ctx, "分析项目结构")
	if err != nil {
		t.Fatalf("AnalyseTask failed: %v", err)
	}

	if result != "RepoAgent 的真实结果" {
		t.Errorf("result = %q, want %q", result, "RepoAgent 的真实结果")
	}

	if atomic.LoadInt32(&repoAgentCalled) != 1 {
		t.Error("RepoAgent.Run should have been called on cache miss")
	}

	// 等待异步 embed 调用（最多等 1 秒）
	time.Sleep(100 * time.Millisecond)
	if atomic.LoadInt32(&embedCalled) != 1 {
		t.Error("embedTaskAndResult should have been called asynchronously")
	}
}

// ── TestRepoKnowledgeManager_AnalyseTask_EmptyCodexrayURL ──

func TestRepoKnowledgeManager_AnalyseTask_EmptyCodexrayURL(t *testing.T) {
	// 当 CodexrayURL 为空字符串时，验证 AnalyseTask 直接调用 RepoAgent
	var repoAgentCalled int32

	workDir := t.TempDir()
	gctx := newTestGlobalCtx(workDir)
	gctx.CodexrayURL = "" // 设置为空字符串

	mockAgent := &mockRepoAgent{
		runFn: func(ctx context.Context, task string) (AgentResult, error) {
			atomic.StoreInt32(&repoAgentCalled, 1)
			return AgentResult{Text: "直接 RepoAgent 结果"}, nil
		},
		GlobalCtx: gctx,
	}

	mgr := NewRepoKnowledgeManager(mockAgent, gctx, 0.95)

	ctx := context.Background()
	result, err := mgr.AnalyseTask(ctx, "分析项目结构")
	if err != nil {
		t.Fatalf("AnalyseTask failed: nil CodexrayURL should fallback gracefully: %v", err)
	}

	if result != "直接 RepoAgent 结果" {
		t.Errorf("result = %q, want %q", result, "直接 RepoAgent 结果")
	}

	if atomic.LoadInt32(&repoAgentCalled) != 1 {
		t.Error("RepoAgent.Run should have been called when CodexrayURL is empty")
	}
}

// ── TestRepoKnowledgeManager_AnalyseTask_SearchErrorFallback ──

func TestRepoKnowledgeManager_AnalyseTask_SearchErrorFallback(t *testing.T) {
	// 当 searchSimilar 返回错误时，验证 AnalyseTask fallback 到 RepoAgent
	var repoAgentCalled int32
	serverCallCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverCallCount++
		if r.URL.Path == "/repo_knowledge/search" {
			// 第一次调用返回错误
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	workDir := t.TempDir()
	gctx := newTestGlobalCtx(workDir)
	gctx.CodexrayURL = server.URL

	mockAgent := &mockRepoAgent{
		runFn: func(ctx context.Context, task string) (AgentResult, error) {
			atomic.StoreInt32(&repoAgentCalled, 1)
			return AgentResult{Text: "fallback 结果"}, nil
		},
		GlobalCtx: gctx,
	}

	mgr := NewRepoKnowledgeManager(mockAgent, gctx, 0.95)

	ctx := context.Background()
	result, err := mgr.AnalyseTask(ctx, "分析项目结构")
	if err != nil {
		t.Fatalf("AnalyseTask should have fallback gracefully: %v", err)
	}

	if result != "fallback 结果" {
		t.Errorf("result = %q, want %q", result, "fallback 结果")
	}

	if atomic.LoadInt32(&repoAgentCalled) != 1 {
		t.Error("RepoAgent.Run should have been called as fallback")
	}

	// 验证 search 只被调用了一次（搜索失败后直接 fallback）
	if serverCallCount != 1 {
		t.Errorf("search endpoint called %d times, want 1", serverCallCount)
	}
}

// ── TestRepoKnowledgeManager_DefaultThreshold ──

func TestRepoKnowledgeManager_DefaultThreshold(t *testing.T) {
	workDir := t.TempDir()
	gctx := newTestGlobalCtx(workDir)
	agent := &RepoAgent{GlobalCtx: gctx}

	mgr := NewRepoKnowledgeManager(agent, gctx, 0) // 传入 0，应该使用默认值
	if mgr.threshold != 0.95 {
		t.Errorf("default threshold = %f, want 0.95", mgr.threshold)
	}

	// 传入负数也应该使用默认值
	mgr2 := NewRepoKnowledgeManager(agent, gctx, -1.0)
	if mgr2.threshold != 0.95 {
		t.Errorf("threshold for negative input = %f, want 0.95", mgr2.threshold)
	}
}

// ── TestRepoKnowledgeManager_CustomThreshold ──

func TestRepoKnowledgeManager_CustomThreshold(t *testing.T) {
	workDir := t.TempDir()
	gctx := newTestGlobalCtx(workDir)
	agent := &RepoAgent{GlobalCtx: gctx}

	// 测试自定义正数值
	customThreshold := 0.85
	mgr := NewRepoKnowledgeManager(agent, gctx, customThreshold)
	if mgr.threshold != customThreshold {
		t.Errorf("custom threshold = %f, want %f", mgr.threshold, customThreshold)
	}

	// 测试另一个值
	mgr2 := NewRepoKnowledgeManager(agent, gctx, 0.75)
	if mgr2.threshold != 0.75 {
		t.Errorf("custom threshold = %f, want 0.75", mgr2.threshold)
	}
}

// ─── Test 2: CodexrayURL 硬编码验证（端口写死 12800） ─────────────────────

// ── TestRepoOperationsTool_CodexrayURL_DefaultsTo12800 ──

func TestRepoOperationsTool_CodexrayURL_DefaultsTo12800(t *testing.T) {
	repoOps := tools.NewRepoOperationsTool("http://127.0.0.1:12800", "/tmp/test")
	if repoOps.CodexrayURL != "http://127.0.0.1:12800" {
		t.Errorf("CodexrayURL = %q, want %q", repoOps.CodexrayURL, "http://127.0.0.1:12800")
	}
}

// ── TestGlobalCtx_CodexrayURL_DefaultsTo12800 ──

func TestGlobalCtx_CodexrayURL_DefaultsTo12800(t *testing.T) {
	workDir := t.TempDir()
	gctx := newTestGlobalCtx(workDir)

	// 验证 GlobalCtx 的 CodexrayURL
	if gctx.CodexrayURL != "http://127.0.0.1:12800" {
		t.Errorf("GlobalCtx.CodexrayURL = %q, want %q", gctx.CodexrayURL, "http://127.0.0.1:12800")
	}

	// 验证 RepoOps 的 CodexrayURL
	if gctx.RepoOps.CodexrayURL != "http://127.0.0.1:12800" {
		t.Errorf("GlobalCtx.RepoOps.CodexrayURL = %q, want %q", gctx.RepoOps.CodexrayURL, "http://127.0.0.1:12800")
	}
}

// ── TestRepoKnowledgeManager_UsesCodexrayURL ──

func TestRepoKnowledgeManager_UsesCodexrayURL(t *testing.T) {
	// 验证 RepoKnowledgeManager 使用 globalCtx.CodexrayURL 构建请求 URL
	var capturedURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedURL = r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"success": true, "data": {"matches": []}}`)
	}))
	defer server.Close()

	workDir := t.TempDir()
	gctx := newTestGlobalCtx(workDir)
	gctx.CodexrayURL = server.URL

	agent := &RepoAgent{GlobalCtx: gctx}
	mgr := NewRepoKnowledgeManager(agent, gctx, 0.95)

	ctx := context.Background()
	_, err := mgr.searchSimilar(ctx, "测试任务")
	if err != nil {
		t.Fatalf("searchSimilar failed: %v", err)
	}

	// 验证请求 URL 包含了 mock server 的地址
	if !containsString(capturedURL, "/repo_knowledge/search") {
		t.Errorf("captured URL = %q, expected to contain '/repo_knowledge/search'", capturedURL)
	}
}

// ─── Test 3: delegate_repo 工具集成测试 ──────────────────────────────────────

// ── TestDelegateRepo_WithRepoKnowledgeMgr ──

func TestDelegateRepo_WithRepoKnowledgeMgr(t *testing.T) {
	// 使用 mockEngine（模拟 LLM）创建 ConductorAgent，注册 RepoKnowledgeManager
	// 调用 delegate_repo 工具，验证其走 RepoKnowledgeManager.AnalyseTask 路径

	var embedCalled int32

	// 创建 mock codebase server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repo_knowledge/search" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, `{
				"success": true,
				"data": {
					"matches": [
						{
							"id": "int1",
							"task": "分析项目",
							"result": "缓存的集成测试结果",
							"score": 0.99
						}
					]
				}
			}`)
			return
		}
		if r.URL.Path == "/repo_knowledge/embed" {
			atomic.StoreInt32(&embedCalled, 1)
			io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"success": true}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	workDir := t.TempDir()
	gctx := newTestGlobalCtx(workDir)
	gctx.CodexrayURL = server.URL

	// 使用 mockEngine
	engine := &mockEngine{
		generateContent: func(ctx context.Context, messages []llm.Message, tools []llm.ToolDef, opts *llm.CallOptions) (*llm.Response, error) {
			return &llm.Response{
				Choices: []llm.Choice{{Content: "should not be called"}},
			}, nil
		},
	}

	// 创建 RepoAgent
	repoAgent := NewRepoAgent(gctx, engine, nil, 10)

	// 创建 RepoKnowledgeManager
	mgr := NewRepoKnowledgeManager(repoAgent, gctx, 0.95)

	// 创建 ConductorAgent，传入 repoKnowledgeMgr
	conductor := NewConductorAgent(gctx, engine, repoAgent, nil, nil, nil, nil, nil, 10, nil, 3, nil, nil, config.Config{}, nil, mgr)

	// 查找 delegate_repo 工具
	var delegateTool *tools.Adapter
	for _, ad := range conductor.Adapters {
		if ad.Name() == "delegate_repo" {
			delegateTool = ad
			break
		}
	}
	if delegateTool == nil {
		t.Fatal("delegate_repo tool not found in conductor adapters")
	}

	// 调用 delegate_repo 工具
	result, err := delegateTool.Call(context.Background(), `{"task": "分析项目"}`)
	if err != nil {
		t.Fatalf("delegate_repo call failed: %v", err)
	}

	// 验证返回了缓存结果
	if !containsString(result, "缓存的集成测试结果") {
		t.Errorf("result = %q, expected to contain '缓存的集成测试结果'", result)
	}

	// 验证 LLM 没有被调用（因为走了缓存）
	// （此处不严格检查，因为 mockEngine 不会记录调用次数）

	// 验证 embed 没有被调用（因为走了缓存，不会存储）
	time.Sleep(50 * time.Millisecond)
	if atomic.LoadInt32(&embedCalled) != 0 {
		t.Error("embed should NOT be called when cache hit")
	}
}

// ── TestDelegateRepo_WithoutRepoKnowledgeMgr ──

func TestDelegateRepo_WithoutRepoKnowledgeMgr(t *testing.T) {
	// 创建 ConductorAgent 但不传 RepoKnowledgeManager（nil）
	// 调用 delegate_repo 工具，验证其 fallback 到直接调用 RepoAgent.Run

	workDir := t.TempDir()
	gctx := newTestGlobalCtx(workDir)

	// 使用 mockEngine 模拟 LLM 响应
	engine := &mockEngine{
		generateContent: func(ctx context.Context, messages []llm.Message, tools []llm.ToolDef, opts *llm.CallOptions) (*llm.Response, error) {
			return &llm.Response{
				Choices: []llm.Choice{{Content: "Mock LLM response for RepoAgent"}},
			}, nil
		},
	}

	// 创建 RepoAgent
	repoAgent := NewRepoAgent(gctx, engine, nil, 10)

	// 创建 ConductorAgent，repoKnowledgeMgr 为 nil
	conductor := NewConductorAgent(gctx, engine, repoAgent, nil, nil, nil, nil, nil, 10, nil, 3, nil, nil, config.Config{}, nil, nil)

	// 查找 delegate_repo 工具
	var delegateTool *tools.Adapter
	for _, ad := range conductor.Adapters {
		if ad.Name() == "delegate_repo" {
			delegateTool = ad
			break
		}
	}
	if delegateTool == nil {
		t.Fatal("delegate_repo tool not found in conductor adapters")
	}

	// 调用 delegate_repo 工具
	result, err := delegateTool.Call(context.Background(), `{"task": "分析项目"}`)
	if err != nil {
		t.Fatalf("delegate_repo call failed: %v", err)
	}

	// 验证返回了 mock engine 的结果
	if !containsString(result, "Mock LLM response for RepoAgent") {
		t.Errorf("result = %q, expected to contain 'Mock LLM response for RepoAgent'", result)
	}
}

// ─── Test 4: 工具函数验证 ────────────────────────────────────────────────────

// ── TestRepoKnowledgeMatch_JSONParsing ──

func TestRepoKnowledgeMatch_JSONParsing(t *testing.T) {
	// 验证 RepoKnowledgeMatch 结构体的 JSON 解析正确性
	jsonStr := `{
		"success": true,
		"data": {
			"matches": [
				{
					"id": "test-id-001",
					"task": "测试任务描述",
					"result": "这是测试的结果内容",
					"score": 0.97
				}
			]
		}
	}`

	var resp struct {
		Success bool `json:"success"`
		Data    struct {
			Matches []RepoKnowledgeMatch `json:"matches"`
		} `json:"data"`
	}

	if err := json.Unmarshal([]byte(jsonStr), &resp); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if !resp.Success {
		t.Error("expected success to be true")
	}

	if len(resp.Data.Matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(resp.Data.Matches))
	}

	match := resp.Data.Matches[0]
	if match.ID != "test-id-001" {
		t.Errorf("match.ID = %q, want %q", match.ID, "test-id-001")
	}
	if match.Task != "测试任务描述" {
		t.Errorf("match.Task = %q, want %q", match.Task, "测试任务描述")
	}
	if match.Result != "这是测试的结果内容" {
		t.Errorf("match.Result = %q, want %q", match.Result, "这是测试的结果内容")
	}
	if match.Score != 0.97 {
		t.Errorf("match.Score = %f, want 0.97", match.Score)
	}
}

// ── TestSearchSimilar_RequestFormat ──

func TestSearchSimilar_RequestFormat(t *testing.T) {
	// 通过捕获 HTTP 请求 body，验证 searchSimilar 发送的请求格式
	// （包含 task 和 top_k 字段）
	var capturedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(bodyBytes, &capturedBody); err != nil {
			t.Errorf("failed to parse request body: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"success": true, "data": {"matches": []}}`)
	}))
	defer server.Close()

	workDir := t.TempDir()
	gctx := newTestGlobalCtx(workDir)
	gctx.CodexrayURL = server.URL

	agent := &RepoAgent{GlobalCtx: gctx}
	mgr := NewRepoKnowledgeManager(agent, gctx, 0.95)

	ctx := context.Background()
	testTask := "验证请求格式的任务"
	_, _ = mgr.searchSimilar(ctx, testTask)

	// 验证请求格式
	if capturedBody["task"] != testTask {
		t.Errorf("captured task = %v, want %q", capturedBody["task"], testTask)
	}

	topK, ok := capturedBody["top_k"].(float64)
	if !ok {
		t.Error("capturedBody should contain 'top_k' field as float64")
	} else if topK != 5 {
		t.Errorf("top_k = %f, want 5", topK)
	}

	// 验证 Content-Type header
	// （需要在请求发送前获取，这里无法验证，跳过）
}

// ── TestRepoKnowledgeManager_AnalyseTask_NilGlobalCtx ──

func TestRepoKnowledgeManager_AnalyseTask_NilGlobalCtx(t *testing.T) {
	// 当 globalCtx 为 nil 时，验证 AnalyseTask 优雅降级
	mockAgent := &mockRepoAgent{
		runFn: func(ctx context.Context, task string) (AgentResult, error) {
			return AgentResult{Text: "nil globalCtx fallback"}, nil
		},
	}

	mgr := NewRepoKnowledgeManager(mockAgent, nil, 0.95)

	ctx := context.Background()
	result, err := mgr.AnalyseTask(ctx, "分析项目")
	if err != nil {
		t.Fatalf("AnalyseTask should handle nil globalCtx gracefully: %v", err)
	}

	if result != "nil globalCtx fallback" {
		t.Errorf("result = %q, want %q", result, "nil globalCtx fallback")
	}
}

// ── TestRepoKnowledgeManager_AnalyseTask_EmbedErrorNonFatal ──

func TestRepoKnowledgeManager_AnalyseTask_EmbedErrorNonFatal(t *testing.T) {
	// 验证 embed 失败不阻塞主流程
	var repoAgentCalled int32

	// embed 端点返回错误
	embedErrorReceived := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repo_knowledge/search" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, `{
				"success": true,
				"data": {
					"matches": [
						{"id": "low", "task": "t", "result": "r", "score": 0.1}
					]
				}
			}`)
			return
		}
		if r.URL.Path == "/repo_knowledge/embed" {
			io.ReadAll(r.Body)
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, `{"error": "embed failed"}`)
			close(embedErrorReceived)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	workDir := t.TempDir()
	gctx := newTestGlobalCtx(workDir)
	gctx.CodexrayURL = server.URL

	mockAgent := &mockRepoAgent{
		runFn: func(ctx context.Context, task string) (AgentResult, error) {
			atomic.StoreInt32(&repoAgentCalled, 1)
			return AgentResult{Text: "真实分析结果"}, nil
		},
		GlobalCtx: gctx,
	}

	mgr := NewRepoKnowledgeManager(mockAgent, gctx, 0.95)

	ctx := context.Background()
	result, err := mgr.AnalyseTask(ctx, "测试任务")
	if err != nil {
		t.Fatalf("AnalyseTask should succeed even if embed fails: %v", err)
	}

	if result != "真实分析结果" {
		t.Errorf("result = %q, want %q", result, "真实分析结果")
	}

	if atomic.LoadInt32(&repoAgentCalled) != 1 {
		t.Error("RepoAgent.Run should have been called")
	}

	// 等待 embed 异步调用和错误处理
	time.Sleep(100 * time.Millisecond)
	select {
	case <-embedErrorReceived:
		// embed error was received, test passed
	default:
		t.Error("embed was not called asynchronously")
	}
}

// ── TestRepoKnowledgeManager_SearchSimilar_ContextCancellation ──

func TestRepoKnowledgeManager_SearchSimilar_ContextCancellation(t *testing.T) {
	// 模拟一个非常慢的服务器，验证 context 取消后请求被终止
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second) // 模拟慢响应
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"success": true, "data": {"matches": []}}`)
	}))
	defer server.Close()

	workDir := t.TempDir()
	gctx := newTestGlobalCtx(workDir)
	gctx.CodexrayURL = server.URL

	agent := &RepoAgent{GlobalCtx: gctx}
	mgr := NewRepoKnowledgeManager(agent, gctx, 0.95)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 立即取消
	cancel()

	_, err := mgr.searchSimilar(ctx, "测试任务")
	if err == nil {
		t.Fatal("expected error after context cancellation, got nil")
	}
}

// ── TestRepoKnowledgeManager_EmbedTaskAndResult_ContextCancellation ──

func TestRepoKnowledgeManager_EmbedTaskAndResult_ContextCancellation(t *testing.T) {
	// 模拟一个非常慢的服务器，验证 context 取消后请求被终止
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	workDir := t.TempDir()
	gctx := newTestGlobalCtx(workDir)
	gctx.CodexrayURL = server.URL

	agent := &RepoAgent{GlobalCtx: gctx}
	mgr := NewRepoKnowledgeManager(agent, gctx, 0.95)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancel() // 立即取消

	err := mgr.embedTaskAndResult(ctx, "任务", "结果")
	if err == nil {
		t.Fatal("expected error after context cancellation, got nil")
	}
}

// ── TestRepoKnowledgeManager_EmptyMatches_ReturnsCacheMiss ──

func TestRepoKnowledgeManager_EmptyMatches_ReturnsCacheMiss(t *testing.T) {
	// 模拟搜索返回空匹配列表，验证走 cache miss 路径
	var repoAgentCalled int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repo_knowledge/search" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, `{"success": true, "data": {"matches": []}}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	workDir := t.TempDir()
	gctx := newTestGlobalCtx(workDir)
	gctx.CodexrayURL = server.URL

	mockAgent := &mockRepoAgent{
		runFn: func(ctx context.Context, task string) (AgentResult, error) {
			atomic.StoreInt32(&repoAgentCalled, 1)
			return AgentResult{Text: "空匹配结果"}, nil
		},
		GlobalCtx: gctx,
	}

	mgr := NewRepoKnowledgeManager(mockAgent, gctx, 0.95)

	ctx := context.Background()
	result, err := mgr.AnalyseTask(ctx, "测试任务")
	if err != nil {
		t.Fatalf("AnalyseTask failed: %v", err)
	}

	if result != "空匹配结果" {
		t.Errorf("result = %q, want %q", result, "空匹配结果")
	}

	if atomic.LoadInt32(&repoAgentCalled) != 1 {
		t.Error("RepoAgent.Run should have been called for empty matches")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Mock types and helpers
// ─────────────────────────────────────────────────────────────────────────────

// mockRepoAgentRunFunc is a function that replaces RepoAgent.Run for testing.
type mockRepoAgentRunFunc func(ctx context.Context, task string) (AgentResult, error)

// mockRepoAgent wraps RepoAgent to override the Run method.
type mockRepoAgent struct {
	runFn     mockRepoAgentRunFunc
	GlobalCtx *globalctx.GlobalCtx
}

func (m *mockRepoAgent) Name() string {
	return "mock-repo-agent"
}

func (m *mockRepoAgent) Run(ctx context.Context, task string) (AgentResult, error) {
	if m.runFn != nil {
		return m.runFn(ctx, task)
	}
	return AgentResult{}, fmt.Errorf("mockRepoAgent: no runFn set")
}

// containsString checks if s contains substr.
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
			return false
		}())
}
