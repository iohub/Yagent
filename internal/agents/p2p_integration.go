package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"codeactor/internal/messaging/bus"
	"codeactor/internal/messaging/peer"
	"codeactor/internal/registry"
)

// ============================================================================
// 通用 P2P 工具（不限于符号表），供所有 Agent 使用
// ============================================================================

// P2PQuery 通用 P2P 查询工具
// 可以通过此工具直接向其他 Agent 查询信息（例如 coding-agent 向 repo-agent 查询代码分析）
func P2PQuery(ctx context.Context, peer peer.AgentPeer, targetID string, method string, payload interface{}, timeout time.Duration) ([]byte, error) {
	if peer == nil {
		return nil, fmt.Errorf("p2p: peer is nil")
	}
	if targetID == "" || method == "" {
		return nil, fmt.Errorf("p2p: targetID and method are required")
	}
	var payloadBytes []byte
	if payload != nil {
		var err error
		payloadBytes, err = json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("p2p: marshal payload: %w", err)
		}
	} else {
		payloadBytes = []byte("{}")
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return peer.Request(ctx, method, targetID, payloadBytes, timeout)
}

// P2PNotify 通用 P2P 通知工具
// 向所有订阅了特定 topic 的 Agent 广播事件
func P2PNotify(ctx context.Context, peer peer.AgentPeer, topic string, payload interface{}) error {
	if peer == nil {
		return fmt.Errorf("p2p: peer is nil")
	}
	if topic == "" {
		return fmt.Errorf("p2p: topic is required")
	}
	var payloadBytes []byte
	if payload != nil {
		var err error
		payloadBytes, err = json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("p2p: marshal payload: %w", err)
		}
	} else {
		payloadBytes = []byte("{}")
	}
	return peer.Publish(ctx, topic, payloadBytes)
}

// ============================================================================
// 符号表结构
// ============================================================================

// Symbol 表示代码中的一个符号（函数、类型、变量等）
type Symbol struct {
	Name     string `json:"name"`
	FilePath string `json:"file_path"`
	Kind     string `json:"kind"` // "function", "type", "variable", "const"
	Line     int    `json:"line"`
	Doc      string `json:"doc,omitempty"`
}

// SymbolTable 符号表
type SymbolTable struct {
	Version   int64    `json:"version"`
	Symbols   []Symbol `json:"symbols"`
	UpdatedAt string   `json:"updated_at"`
}

// SymbolRequest 符号表查询请求
type SymbolRequest struct {
	FileGlob   string `json:"file_glob,omitempty"`   // 文件 glob 模式过滤
	SymbolType string `json:"symbol_type,omitempty"` // 符号类型过滤
	Query      string `json:"query,omitempty"`       // 搜索关键词
}

// ============================================================================
// RepoAgent P2P 扩展方法
// ============================================================================

// InitRepoP2P 初始化 RepoAgent 的 P2P 能力。
// 注册符号表请求处理器，使 CodingAgent 能通过 Request/Response 直接查询符号表。
func InitRepoP2P(repo *RepoAgent) error {
	if repo.Peer == nil {
		return fmt.Errorf("repo: peer not initialized")
	}
	return repo.Peer.RegisterRequestHandler(peer.TopicSymbolsRequest, repo.handleSymbolRequest)
}

// handleSymbolRequest 处理来自 CodingAgent 的符号表查询请求。
func (a *RepoAgent) handleSymbolRequest(ctx context.Context, ev *bus.Event) ([]byte, error) {
	var req SymbolRequest
	if len(ev.Payload) > 0 {
		if err := json.Unmarshal(ev.Payload, &req); err != nil {
			// 忽略解析错误，返回全部符号
			_ = err
		}
	}

	// 这里使用 GlobalCtx 中的符号信息
	// 实际项目中，这应该调用 codexray 或本地符号索引
	symbols := &SymbolTable{
		Version:   time.Now().UnixNano(),
		Symbols:   []Symbol{}, // 实际应从索引中查询
		UpdatedAt: time.Now().Format(time.RFC3339),
	}

	payload, err := json.Marshal(symbols)
	if err != nil {
		return nil, fmt.Errorf("repo: marshal symbols: %w", err)
	}
	return payload, nil
}

// PublishSymbolsUpdated 发布符号表更新事件（通知 CodingAgent）。
// CodingAgent 通过订阅 "symbols.updated" topic 直接获取更新。
func (a *RepoAgent) PublishSymbolsUpdated(ctx context.Context, symbols *SymbolTable) error {
	if a.Peer == nil {
		return fmt.Errorf("repo: peer not initialized")
	}
	payload, err := json.Marshal(symbols)
	if err != nil {
		return fmt.Errorf("repo: marshal symbols: %w", err)
	}
	return a.Peer.Publish(ctx, peer.TopicSymbolsUpdated, payload,
		peer.WithHeader("symbol-count", fmt.Sprintf("%d", len(symbols.Symbols))),
		peer.WithHeader("repo-version", fmt.Sprintf("%d", symbols.Version)),
	)
}

// ============================================================================
// CodingAgent P2P 扩展方法
// ============================================================================

// 符号表本地缓存（原子指针，无锁读取）
var codingSymbolCache atomic.Pointer[SymbolTable]

// InitCodingP2P 初始化 CodingAgent 的 P2P 能力。
// 订阅符号表更新事件，使 RepoAgent 的 P2P 发布能直达 CodingAgent。
func InitCodingP2P(coding *CodingAgent) error {
	if coding.Peer == nil {
		return fmt.Errorf("coding: peer not initialized")
	}
	// 订阅符号表更新
	_, err := coding.Peer.Subscribe(peer.TopicSymbolsUpdated, handleCodingSymbolUpdate)
	if err != nil {
		return fmt.Errorf("coding: subscribe symbols.updated: %w", err)
	}
	// 订阅符号表失效通知
	_, err = coding.Peer.Subscribe(peer.TopicSymbolsInvalidate, func(ctx context.Context, ev *bus.Event) error {
		codingSymbolCache.Store(nil)
		return nil
	})
	if err != nil {
		return fmt.Errorf("coding: subscribe symbols.invalidate: %w", err)
	}
	return nil
}

// handleCodingSymbolUpdate 处理来自 RepoAgent 的符号表更新。
// 使用版本号防止乱序覆盖。
func handleCodingSymbolUpdate(ctx context.Context, ev *bus.Event) error {
	var symbols SymbolTable
	if err := json.Unmarshal(ev.Payload, &symbols); err != nil {
		return err
	}
	current := codingSymbolCache.Load()
	if current != nil && symbols.Version < current.Version {
		return nil // 丢弃过期更新
	}
	codingSymbolCache.Store(&symbols)
	return nil
}

// GetCachedSymbols 返回 CodingAgent 本地缓存的符号表（无锁，O(1)）。
func GetCachedSymbols() *SymbolTable {
	return codingSymbolCache.Load()
}

// RequestSymbolsFromRepo 同步请求 RepoAgent 的符号表（按需查询，非缓存）。
func RequestSymbolsFromRepo(ctx context.Context, coding *CodingAgent, req SymbolRequest) (*SymbolTable, error) {
	if coding.Peer == nil {
		return nil, fmt.Errorf("coding: peer not initialized")
	}
	payload, _ := json.Marshal(req)
	resp, err := coding.Peer.Request(ctx, peer.TopicSymbolsRequest, "repo-agent", payload, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("coding: request symbols: %w", err)
	}
	var symbols SymbolTable
	if err := json.Unmarshal(resp, &symbols); err != nil {
		return nil, err
	}
	// 更新本地缓存
	codingSymbolCache.Store(&symbols)
	return &symbols, nil
}

// ============================================================================
// 初始化网格中所有 Agent 的 P2P 能力
// ============================================================================

// InitAgentMeshP2P 在 AgentMesh 创建后，初始化所有 Agent 的 P2P 通信能力。
// 包括注册请求处理器、订阅事件等。
func InitAgentMeshP2P(repo *RepoAgent, coding *CodingAgent) error {
	// 初始化 RepoAgent 的 P2P
	if err := InitRepoP2P(repo); err != nil {
		return fmt.Errorf("init repo p2p: %w", err)
	}
	// 初始化 CodingAgent 的 P2P
	if err := InitCodingP2P(coding); err != nil {
		return fmt.Errorf("init coding p2p: %w", err)
	}
	return nil
}

// ============================================================================
// 默认能力注册（供 AgentMesh 在初始化时调用）
// ============================================================================

// DefaultRepoCapability 返回 RepoAgent 的默认能力定义
func DefaultRepoCapability() registry.AgentCapability {
	return registry.AgentCapability{
		Name:        "Code Analyst",
		Description: "Analyzes code repositories using semantic search and codebase tools. Can find function definitions, explore code structure, search for patterns, and analyze code architecture.",
		Tags:        []string{"go", "python", "javascript", "typescript", "rust", "code-analysis", "code-search", "repository", "semantic-search", "codebase"},
		Version:     "1.0.0",
	}
}

// DefaultCodingCapability 返回 CodingAgent 的默认能力定义
func DefaultCodingCapability() registry.AgentCapability {
	return registry.AgentCapability{
		Name:        "Code Engineer",
		Description: "Writes, edits, and debugs code. Can create new files, apply patches, run shell commands, execute tests, and perform debugging. Supports Go, Python, JavaScript, TypeScript, Rust, and other languages.",
		Tags:        []string{"go", "python", "javascript", "typescript", "rust", "coding", "debugging", "testing", "file-editing", "shell"},
		Version:     "1.0.0",
	}
}

// DefaultChatCapability 返回 ChatAgent 的默认能力定义
func DefaultChatCapability() registry.AgentCapability {
	return registry.AgentCapability{
		Name:        "Communicator",
		Description: "Provides general knowledge, explanations, and casual conversation. Can answer questions about programming concepts, technologies, and general topics.",
		Tags:        []string{"general-knowledge", "explanation", "qa", "documentation", "chat"},
		Version:     "1.0.0",
	}
}

// DefaultDevOpsCapability 返回 DevOpsAgent 的默认能力定义
func DefaultDevOpsCapability() registry.AgentCapability {
	return registry.AgentCapability{
		Name:        "DevOps Engineer",
		Description: "Handles system administration and operational tasks. Can run shell commands, inspect files, check logs, manage processes, and perform system diagnostics.",
		Tags:        []string{"shell", "system", "devops", "infrastructure", "monitoring", "logging", "process"},
		Version:     "1.0.0",
	}
}

// DefaultBrowserCapability 返回 BrowserAgent 的默认能力定义
func DefaultBrowserCapability() registry.AgentCapability {
	return registry.AgentCapability{
		Name:        "Web Navigator",
		Description: "Controls a headless browser to navigate websites, extract data, take screenshots, fill forms, and execute JavaScript in web pages.",
		Tags:        []string{"browser", "web", "screenshot", "form-filling", "data-extraction", "javascript"},
		Version:     "1.0.0",
	}
}

// DefaultMetaCapability 返回 MetaAgent 的默认能力定义
func DefaultMetaCapability() registry.AgentCapability {
	return registry.AgentCapability{
		Name:        "Agent Architect",
		Description: "Designs and instantiates custom specialized agents on-the-fly. Creates tailored system prompts and selects appropriate tools for novel tasks.",
		Tags:        []string{"agent-design", "prompt-engineering", "meta", "custom-agent"},
		Version:     "1.0.0",
	}
}

// GetAllDefaultCapabilities 返回所有 Agent 的默认能力映射（agentID → capability）
// 用于在系统初始化时批量注册
func GetAllDefaultCapabilities() map[string]registry.AgentCapability {
	return map[string]registry.AgentCapability{
		"repo-agent":    DefaultRepoCapability(),
		"coding-agent":  DefaultCodingCapability(),
		"chat-agent":    DefaultChatCapability(),
		"devops-agent":  DefaultDevOpsCapability(),
		"browser-agent": DefaultBrowserCapability(),
		"meta-agent":    DefaultMetaCapability(),
	}
}
